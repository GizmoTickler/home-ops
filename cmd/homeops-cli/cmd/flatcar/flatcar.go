// Package flatcar implements the `homeops-cli flatcar` command group: Flatcar
// Container Linux + kubeadm provisioning (Ignition render, kubeadm config gen, and
// Proxmox VM deploy). It is additive to the Talos command group.
package flatcar

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/ui"

	"github.com/spf13/cobra"
)

// Swappable function vars for testability (mirrors cmd/talos patterns).
var (
	getVersionsFn        = versionconfig.GetVersions
	workingDirectoryFn   = common.GetWorkingDirectory
	get1PasswordSecretFn = common.Get1PasswordSecretSilent
	// capturePKIFn reads the cluster PKI from node0 over SSH (swappable for tests).
	capturePKIFn = func(sshUser, node0IP string) (map[string]string, error) {
		orch := flatcar.NewOrchestrator(flatcar.OrchestratorConfig{SSHUser: sshUser, SSHItemRef: constants.OpFlatcarSSHPrivateKey})
		return orch.CapturePKI(node0IP)
	}
	// fetchAdminKubeconfigFn reads /etc/kubernetes/admin.conf from a node over SSH.
	fetchAdminKubeconfigFn = func(sshUser, node0IP string) (string, error) {
		orch := flatcar.NewOrchestrator(flatcar.OrchestratorConfig{SSHUser: sshUser, SSHItemRef: constants.OpFlatcarSSHPrivateKey})
		return orch.FetchAdminKubeconfig(node0IP)
	}
	saveKubeconfigFn = common.SaveKubeconfigTo1Password
	pullKubeconfigFn = common.PullKubeconfigFrom1Password
	// confirmActionFn is the interactive confirmation prompt (swappable for tests).
	confirmActionFn = ui.Confirm
	// resetNodeFn runs `kubeadm reset` on a node over SSH (swappable for tests).
	resetNodeFn = func(sshUser, nodeIP string) error {
		orch := flatcar.NewOrchestrator(flatcar.OrchestratorConfig{SSHUser: sshUser, SSHItemRef: constants.OpFlatcarSSHPrivateKey})
		return orch.ResetNode(nodeIP)
	}
	// savePKIToOpFn persists captured PKI fields to 1Password (swappable for tests).
	savePKIToOpFn = savePKIToOp
	// runOpFn runs the op CLI with NO stdin so op never tries to read a JSON
	// template from a pipe (the trap that bites interactive/over-ssh op invocations).
	runOpFn = func(args ...string) error {
		c := common.Command("op", args...)
		c.Stdin = nil
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("op %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// runOpStdinFn runs op with `stdin` piped in (an item template), so secret
	// field values travel via stdin and never appear in argv / /proc/<pid>/cmdline.
	runOpStdinFn = func(stdin []byte, args ...string) error {
		c := common.Command("op", args...)
		c.Stdin = bytes.NewReader(stdin)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("op %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	renderIgnitionFn        = flatcar.RenderIgnition
	renderKubeadmInitFn     = flatcar.RenderKubeadmInitConfig
	renderKubeadmJoinFn     = flatcar.RenderKubeadmJoinConfig
	getFlatcarNodeConfigFn  = proxmox.GetFlatcarNodeConfig
	getProxmoxCredentialsFn = proxmox.GetCredentials
	proxmoxDefaultVMConfig  = proxmox.DefaultVMConfig
	newProxmoxVMManagerFn   = func(host, tokenID, secret, nodeName string, insecure bool) (proxmoxVMManager, error) {
		return proxmox.NewVMManager(host, tokenID, secret, nodeName, insecure)
	}
	// uploadIgnitionToPVEFn writes the rendered Ignition to the snippets dir ON the
	// Proxmox node over SSH. The fw_cfg file= path is read by qemu on that host, so the
	// file must live there — not on whatever box runs this CLI (e.g. varunlnx0).
	// Swappable for tests. Auth is via the ambient ssh-agent (sshArgs sets no -i);
	// SSHItemRef is the 1Password key used by the macOS 1Password SSH agent.
	uploadIgnitionToPVEFn = func(sshHost, sshUser, sshPort, remotePath string, content []byte) error {
		client := ssh.NewSSHClient(ssh.SSHConfig{
			Host:       sshHost,
			Username:   sshUser,
			Port:       sshPort,
			SSHItemRef: constants.OpProxmoxSSHKey,
		})
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect to Proxmox host %s@%s:%s: %w", sshUser, sshHost, sshPort, err)
		}
		defer func() { _ = client.Close() }()

		dir := remotePath[:strings.LastIndex(remotePath, "/")]
		if dir == "" {
			dir = "/"
		}
		// base64-encode so the JSON travels safely inside the remote shell command.
		// Use a heredoc so the base64 payload is never interpolated by the shell.
		// Paths are single-quoted to survive spaces (though they are fixed deploy-time
		// paths from --snippets-dir + --node, which are both validated and alphanumeric).
		b64 := base64.StdEncoding.EncodeToString(content)
		cmd := fmt.Sprintf("mkdir -p '%s' && base64 -d <<'HOMEOPS_EOF' > '%s'\n%s\nHOMEOPS_EOF",
			dir, remotePath, b64)
		if _, err := client.ExecuteCommand(cmd); err != nil {
			return fmt.Errorf("write ignition to %s on %s: %w", remotePath, sshHost, err)
		}
		ok, size, err := client.VerifyFile(remotePath)
		if err != nil {
			return fmt.Errorf("verify ignition on %s: %w", sshHost, err)
		}
		if !ok || size == 0 {
			return fmt.Errorf("ignition not present/empty on %s after upload (path=%s, size=%d)", sshHost, remotePath, size)
		}
		return nil
	}
)

// proxmoxVMManager is the subset of the Proxmox VM manager flatcar deploy needs.
type proxmoxVMManager interface {
	Close() error
	DeployVM(proxmox.VMConfig) error
}

// NewCommand builds the `flatcar` command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flatcar",
		Short: "Manage Flatcar Container Linux + kubeadm nodes",
		Long: `Provision Flatcar Container Linux control-plane nodes with kubeadm.

Subcommands:
  deploy-vm        Deploy Flatcar k8s VM(s) on Proxmox (Ignition via fw_cfg)
  render-ignition  Render and print the Ignition JSON for a node (debug)
  gen-kubeadm      Render the kubeadm init/join config for a node`,
	}

	cmd.AddCommand(
		newRenderIgnitionCommand(),
		newGenKubeadmCommand(),
		newDeployVMCommand(),
		newSavePKICommand(),
		newKubeconfigCommand(),
		newResetNodeCommand(),
		newResetClusterCommand(),
	)

	return cmd
}

// nodeNames returns the sorted predefined Flatcar node names.
func nodeNames() []string {
	names := make([]string, 0, len(proxmox.FlatcarNodeConfigs))
	for name := range proxmox.FlatcarNodeConfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// buildNodeEnv assembles a flatcar.NodeEnv for a named node using predefined node
// configs + versions + the configurable knobs. Join material (cert key/token/hash)
// is left empty here and supplied separately for join configs.
func buildNodeEnv(nodeName string, vip, pauseImage, kubeVipVersion, nodeInterface string) (flatcar.NodeEnv, error) {
	nodeConfig, ok := getFlatcarNodeConfigFn(nodeName)
	if !ok {
		return flatcar.NodeEnv{}, fmt.Errorf("unknown flatcar node %q (known: %s)", nodeName, strings.Join(nodeNames(), ", "))
	}

	versions := getVersionsFn(workingDirectoryFn())

	if vip == "" {
		vip = constants.DefaultControlPlaneVIP
	}
	if pauseImage == "" {
		pauseImage = versions.PauseImage
	}
	if kubeVipVersion == "" {
		kubeVipVersion = versions.KubeVipVersion
	}
	if nodeInterface == "" {
		nodeInterface = constants.DefaultNodeInterface
	}

	// Non-secret node identifiers sourced from 1Password (kept out of the repo):
	// the apiserver cert SAN DNS (k8s.<SECRET_DOMAIN>) and the node SSH public key.
	domain := strings.TrimSpace(get1PasswordSecretFn(constants.OpSecretDomain))
	k8sEndpoint := ""
	if domain != "" {
		k8sEndpoint = "k8s." + domain
	}
	sshKey := strings.TrimSpace(get1PasswordSecretFn(constants.OpFlatcarPublicKey))

	return flatcar.NodeEnv{
		NodeName:          nodeConfig.Name,
		NodeIP:            nodeConfig.NodeIP,
		Node0IP:           constants.FlatcarNode0IP,
		Node1IP:           constants.FlatcarNode1IP,
		Node2IP:           constants.FlatcarNode2IP,
		KubernetesVersion: versions.KubernetesVersion,
		KubernetesMinor:   flatcar.KubernetesMinor(versions.KubernetesVersion),
		ControlPlaneVIP:   vip,
		PauseImage:        pauseImage,
		KubeVipVersion:    kubeVipVersion,
		NodeInterface:     nodeInterface,
		K8sEndpoint:       k8sEndpoint,
		SSHAuthorizedKey:  sshKey,
	}, nil
}

// opField / opItemTemplate model the `op item create` JSON template piped on stdin.
type opField struct {
	ID      string `json:"id,omitempty"`
	Label   string `json:"label,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	Type    string `json:"type"`
	Value   string `json:"value"`
}

type opItemTemplate struct {
	Title    string    `json:"title"`
	Category string    `json:"category"`
	Fields   []opField `json:"fields"`
}

// buildPKITemplate turns captured PKI (1Password field -> base64) into an op item
// template. *.key fields are CONCEALED. Field order is deterministic.
func buildPKITemplate(fields map[string]string) opItemTemplate {
	t := opItemTemplate{
		Title:    "kubernetes-pki",
		Category: "SECURE_NOTE",
		Fields: []opField{{
			ID: "notesPlain", Type: "STRING", Purpose: "NOTES",
			Value: "kubeadm cluster PKI (base64). Restored by homeops-cli before kubeadm init for a stable cluster identity across rebuilds. Managed by 'flatcar save-pki'.",
		}},
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		typ := "STRING"
		if strings.HasSuffix(k, "_key") {
			typ = "CONCEALED"
		}
		t.Fields = append(t.Fields, opField{Label: k, Type: typ, Value: fields[k]})
	}
	return t
}

// savePKIToOp persists captured PKI to op://Infrastructure/kubernetes-pki,
// replacing any existing item. The base64 CA/SA/etcd PRIVATE keys are passed via
// an item template on STDIN (never argv), so they don't appear in /proc/<pid>/cmdline.
func savePKIToOp(fields map[string]string) error {
	_ = runOpFn("item", "delete", "kubernetes-pki", "--vault", "Infrastructure") // ignore if absent
	doc, err := json.Marshal(buildPKITemplate(fields))
	if err != nil {
		return fmt.Errorf("marshal op item template: %w", err)
	}
	return runOpStdinFn(doc, "item", "create", "--vault", "Infrastructure")
}

// flatcarSSHUser resolves the node SSH username from 1Password, defaulting to "core".
func flatcarSSHUser() string {
	if u := strings.TrimSpace(get1PasswordSecretFn(constants.OpFlatcarSSHUser)); u != "" {
		return u
	}
	return "core"
}

// newResetNodeCommand runs `kubeadm reset` on a single Flatcar node (destructive).
func newResetNodeCommand() *cobra.Command {
	var node string
	var force bool
	cmd := &cobra.Command{
		Use:   "reset-node",
		Short: "Run `kubeadm reset` on a Flatcar node (destructive)",
		Long: `SSH to a control-plane node and run 'kubeadm reset -f', tearing down its cluster
state (removes /etc/kubernetes including the PKI). DESTRUCTIVE — prompts for
confirmation unless --force.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			if node == "" {
				return fmt.Errorf("--node is required (one of: %s)", strings.Join(nodeNames(), ", "))
			}
			nodeConfig, ok := getFlatcarNodeConfigFn(node)
			if !ok {
				return fmt.Errorf("unknown flatcar node %q (known: %s)", node, strings.Join(nodeNames(), ", "))
			}
			if !force {
				ok, err := confirmActionFn(fmt.Sprintf("Reset node %s (%s)? This runs 'kubeadm reset' and destroys its cluster state.", node, nodeConfig.NodeIP), false)
				if err != nil || !ok {
					return fmt.Errorf("reset cancelled")
				}
			}
			logger.Warn("Resetting node %s (%s)...", node, nodeConfig.NodeIP)
			if err := resetNodeFn(flatcarSSHUser(), nodeConfig.NodeIP); err != nil {
				return err
			}
			logger.Success("Node %s reset", node)
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Flatcar node to reset (required)")
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// newResetClusterCommand runs `kubeadm reset` on every Flatcar node (destructive).
func newResetClusterCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reset-cluster",
		Short: "Run `kubeadm reset` on ALL Flatcar nodes (destructive)",
		Long: `Reset every control-plane node ('kubeadm reset -f'), tearing down the entire
cluster. Nodes are reset in reverse order (init node last). DESTRUCTIVE — prompts
unless --force.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			names := nodeNames()
			if !force {
				ok, err := confirmActionFn(fmt.Sprintf("Reset the ENTIRE cluster (%s)? This destroys all cluster state.", strings.Join(names, ", ")), false)
				if err != nil || !ok {
					return fmt.Errorf("reset cancelled")
				}
			}
			sshUser := flatcarSSHUser()
			// Reverse order so the init node (k8s-0) is reset last.
			for i := len(names) - 1; i >= 0; i-- {
				cfg, ok := getFlatcarNodeConfigFn(names[i])
				if !ok {
					continue
				}
				logger.Warn("Resetting node %s (%s)...", names[i], cfg.NodeIP)
				if err := resetNodeFn(sshUser, cfg.NodeIP); err != nil {
					return fmt.Errorf("reset %s: %w", names[i], err)
				}
			}
			logger.Success("Cluster reset (%d nodes)", len(names))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// patchKubeconfigServer rewrites the cluster server URL to the control-plane VIP
// (the stable endpoint for ongoing use, unlike the bootstrap-time node-IP patch).
func patchKubeconfigServer(kubeconfig, vip string) string {
	re := regexp.MustCompile(`(?m)^(\s*server:\s+).*$`)
	return re.ReplaceAllString(kubeconfig, "${1}https://"+vip+":6443")
}

// newKubeconfigCommand fetches the cluster kubeconfig from a node (parity with
// `talos kubeconfig`): SSH admin.conf, point server at the VIP, push/pull op.
func newKubeconfigCommand() *cobra.Command {
	var node, output, vip string
	var push, pull bool
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Fetch the cluster kubeconfig from a node (with --push/--pull 1Password)",
		Long: `Fetch /etc/kubernetes/admin.conf from a control-plane node over SSH, point its
server at the control-plane VIP, and write it locally (0600). --push also stores
it in 1Password (op://Infrastructure/kubeconfig); --pull retrieves it from there
instead of contacting a node.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			if output == "" {
				output = os.Getenv(constants.EnvKubeconfig)
			}
			if output == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve home dir for default kubeconfig path: %w", err)
				}
				output = filepath.Join(home, ".kube", "config")
			}

			if pull {
				if err := pullKubeconfigFn(output, logger); err != nil {
					return fmt.Errorf("pull kubeconfig from 1Password: %w", err)
				}
				logger.Success("Kubeconfig written to %s (from 1Password)", output)
				return nil
			}

			nodeConfig, ok := getFlatcarNodeConfigFn(node)
			if !ok {
				return fmt.Errorf("unknown flatcar node %q (known: %s)", node, strings.Join(nodeNames(), ", "))
			}
			sshUser := strings.TrimSpace(get1PasswordSecretFn(constants.OpFlatcarSSHUser))
			if sshUser == "" {
				sshUser = "core"
			}
			if vip == "" {
				vip = constants.DefaultControlPlaneVIP
			}

			logger.Info("Fetching kubeconfig from %s (%s)...", node, nodeConfig.NodeIP)
			kc, err := fetchAdminKubeconfigFn(sshUser, nodeConfig.NodeIP)
			if err != nil {
				return fmt.Errorf("fetch admin.conf from %s: %w", node, err)
			}
			kc = patchKubeconfigServer(kc, vip)

			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				return fmt.Errorf("create kubeconfig dir: %w", err)
			}
			if err := os.WriteFile(output, []byte(kc), 0o600); err != nil {
				return fmt.Errorf("write kubeconfig %s: %w", output, err)
			}
			logger.Success("Kubeconfig written to %s (server https://%s:6443)", output, vip)

			if push {
				if err := saveKubeconfigFn([]byte(kc), logger); err != nil {
					return fmt.Errorf("save kubeconfig to 1Password: %w", err)
				}
				logger.Success("Kubeconfig saved to 1Password")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "k8s-0", "control-plane node to fetch the kubeconfig from")
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)
	cmd.Flags().StringVar(&output, "output", "", "kubeconfig output path (default $KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVar(&vip, "vip", "", "control-plane VIP for the server field (default from constants)")
	cmd.Flags().BoolVar(&push, "push", false, "also save the fetched kubeconfig to 1Password")
	cmd.Flags().BoolVar(&pull, "pull", false, "pull the kubeconfig from 1Password instead of a node")
	cmd.MarkFlagsMutuallyExclusive("push", "pull")
	return cmd
}

// newSavePKICommand captures the live cluster PKI into 1Password so bootstrap can
// restore it for a stable identity across rebuilds.
func newSavePKICommand() *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "save-pki",
		Short: "Capture the live cluster PKI into op://Infrastructure/kubernetes-pki",
		Long: `Read /etc/kubernetes/pki (cluster CA, ServiceAccount keys, front-proxy CA, etcd
CA) from a control-plane node over SSH and store it in 1Password, so 'bootstrap'
restores it before 'kubeadm init' and the cluster keeps a STABLE identity across
nuke/pave. Run once after the cluster is up, and again after any CA rotation.
Leaf certs are not captured (kubeadm regenerates them off the CAs).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			nodeConfig, ok := getFlatcarNodeConfigFn(node)
			if !ok {
				return fmt.Errorf("unknown flatcar node %q (known: %s)", node, strings.Join(nodeNames(), ", "))
			}
			sshUser := strings.TrimSpace(get1PasswordSecretFn(constants.OpFlatcarSSHUser))
			if sshUser == "" {
				sshUser = "core"
			}
			logger.Info("Capturing cluster PKI from %s (%s) over SSH...", node, nodeConfig.NodeIP)
			fields, err := capturePKIFn(sshUser, nodeConfig.NodeIP)
			if err != nil {
				return fmt.Errorf("capture PKI: %w", err)
			}
			if err := savePKIToOpFn(fields); err != nil {
				return fmt.Errorf("persist PKI to 1Password: %w", err)
			}
			logger.Success("Persisted %d PKI fields to op://Infrastructure/kubernetes-pki (bootstrap restores them; --fresh-pki opts out)", len(fields))
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "k8s-0", "control-plane node to read the PKI from")
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)
	return cmd
}

func newRenderIgnitionCommand() *cobra.Command {
	var (
		nodeName       string
		vip            string
		pauseImage     string
		kubeVipVersion string
		nodeInterface  string
		outFile        string
	)

	cmd := &cobra.Command{
		Use:   "render-ignition",
		Short: "Render the Ignition JSON for a Flatcar node",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := buildNodeEnv(nodeName, vip, pauseImage, kubeVipVersion, nodeInterface)
			if err != nil {
				return err
			}
			ign, err := renderIgnitionFn(env)
			if err != nil {
				return err
			}
			if outFile != "" {
				if err := os.WriteFile(outFile, ign, 0o644); err != nil {
					return fmt.Errorf("failed to write ignition to %s: %w", outFile, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Ignition written to %s\n", outFile)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(ign))
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeName, "node", "k8s-0", "Flatcar node name")
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)
	cmd.Flags().StringVar(&vip, "vip", "", "Control-plane VIP (default from constants)")
	cmd.Flags().StringVar(&pauseImage, "pause-image", "", "Pause/sandbox image (default from versions)")
	cmd.Flags().StringVar(&kubeVipVersion, "kube-vip-version", "", "kube-vip image tag (default from versions)")
	cmd.Flags().StringVar(&nodeInterface, "interface", "", "Node primary interface (default eth0)")
	cmd.Flags().StringVar(&outFile, "out", "", "Write Ignition JSON to file instead of stdout")

	return cmd
}

func newGenKubeadmCommand() *cobra.Command {
	var (
		nodeName       string
		mode           string
		vip            string
		certKey        string
		token          string
		caCertHash     string
		pauseImage     string
		kubeVipVersion string
		nodeInterface  string
	)

	cmd := &cobra.Command{
		Use:   "gen-kubeadm",
		Short: "Render the kubeadm init or join config for a node",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := buildNodeEnv(nodeName, vip, pauseImage, kubeVipVersion, nodeInterface)
			if err != nil {
				return err
			}

			switch strings.ToLower(mode) {
			case "init":
				out, err := renderKubeadmInitFn(env)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), out)
			case "join":
				env.CertificateKey = certKey
				env.BootstrapToken = token
				env.CACertHash = caCertHash
				out, err := renderKubeadmJoinFn(env)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), out)
			default:
				return fmt.Errorf("invalid --mode %q (want init or join)", mode)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeName, "node", "k8s-0", "Flatcar node name")
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)
	cmd.Flags().StringVar(&mode, "mode", "init", "Config to render: init or join")
	cmd.Flags().StringVar(&vip, "vip", "", "Control-plane VIP (default from constants)")
	cmd.Flags().StringVar(&certKey, "cert-key", "", "Certificate key (join mode)")
	cmd.Flags().StringVar(&token, "token", "", "Bootstrap token (join mode)")
	cmd.Flags().StringVar(&caCertHash, "ca-cert-hash", "", "CA cert hash (join mode)")
	cmd.Flags().StringVar(&pauseImage, "pause-image", "", "Pause/sandbox image (default from versions)")
	cmd.Flags().StringVar(&kubeVipVersion, "kube-vip-version", "", "kube-vip image tag (default from versions)")
	cmd.Flags().StringVar(&nodeInterface, "interface", "", "Node primary interface (default eth0)")

	return cmd
}

func newDeployVMCommand() *cobra.Command {
	var (
		nodes          []string
		imagePath      string
		imageVolume    string
		snippetsDir    string
		pveSSHHost     string
		pveSSHUser     string
		pveSSHPort     string
		vip            string
		pauseImage     string
		kubeVipVersion string
		nodeInterface  string
		concurrent     int
		powerOn        bool
		dryRun         bool
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Flatcar k8s VM(s) on Proxmox with Ignition",
		Long: `Deploy one or more Flatcar control-plane VMs on Proxmox.

Each VM boots from a pre-staged Flatcar image (--image-path to import, or
--image-volume to attach an existing volume) and receives its rendered Ignition
config via fw_cfg. The Ignition JSON is written to the Proxmox snippets directory
(--snippets-dir) so the Proxmox node can read it for the fw_cfg file= attach.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeployVM(cmd, deployVMOptions{
				nodes:          nodes,
				imagePath:      imagePath,
				imageVolume:    imageVolume,
				snippetsDir:    snippetsDir,
				pveSSHHost:     pveSSHHost,
				pveSSHUser:     pveSSHUser,
				pveSSHPort:     pveSSHPort,
				vip:            vip,
				pauseImage:     pauseImage,
				kubeVipVersion: kubeVipVersion,
				nodeInterface:  nodeInterface,
				concurrent:     concurrent,
				powerOn:        powerOn,
				dryRun:         dryRun,
			})
		},
	}

	cmd.Flags().StringSliceVar(&nodes, "nodes", nodeNames(), "Flatcar node names to deploy")
	_ = cmd.RegisterFlagCompletionFunc("nodes", completion.ValidNodeNames)
	cmd.Flags().StringVar(&imagePath, "image-path", "", "Path on Proxmox to import the Flatcar disk image from (import-from)")
	cmd.Flags().StringVar(&imageVolume, "image-volume", "", "Existing storage volume to attach as scsi0 (alternative to --image-path)")
	cmd.Flags().StringVar(&snippetsDir, "snippets-dir", "/var/lib/vz/snippets", "Proxmox snippets dir for Ignition files (on the Proxmox node)")
	cmd.Flags().StringVar(&pveSSHHost, "pve-ssh-host", "", "Proxmox host to SSH the Ignition to (default: the Proxmox API host)")
	cmd.Flags().StringVar(&pveSSHUser, "pve-ssh-user", "root", "SSH user on the Proxmox host for Ignition upload")
	cmd.Flags().StringVar(&pveSSHPort, "pve-ssh-port", "22", "SSH port on the Proxmox host")
	cmd.Flags().StringVar(&vip, "vip", "", "Control-plane VIP (default from constants)")
	cmd.Flags().StringVar(&pauseImage, "pause-image", "", "Pause/sandbox image (default from versions)")
	cmd.Flags().StringVar(&kubeVipVersion, "kube-vip-version", "", "kube-vip image tag (default from versions)")
	cmd.Flags().StringVar(&nodeInterface, "interface", "", "Node primary interface (default eth0)")
	cmd.Flags().IntVar(&concurrent, "concurrency", 1, "Max concurrent deployments")
	cmd.Flags().IntVar(&concurrent, "concurrent", 1, "Max concurrent deployments (deprecated: use --concurrency)")
	_ = cmd.Flags().MarkDeprecated("concurrent", "use --concurrency")
	cmd.Flags().BoolVar(&powerOn, "power-on", false, "Power on VMs after creation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Render and build configs but do not create VMs")

	return cmd
}

type deployVMOptions struct {
	nodes          []string
	imagePath      string
	imageVolume    string
	snippetsDir    string
	pveSSHHost     string
	pveSSHUser     string
	pveSSHPort     string
	vip            string
	pauseImage     string
	kubeVipVersion string
	nodeInterface  string
	concurrent     int
	powerOn        bool
	dryRun         bool
}

func runDeployVM(cmd *cobra.Command, opts deployVMOptions) error {
	logger := common.NewColorLogger()

	if opts.imagePath == "" && opts.imageVolume == "" && !opts.dryRun {
		return fmt.Errorf("one of --image-path or --image-volume is required")
	}

	// Resolve Proxmox credentials up front. The rendered Ignition must be written to
	// the snippets dir ON the Proxmox node (qemu reads the fw_cfg file= path on that
	// host), so we upload it over SSH rather than writing it wherever this CLI runs.
	// The SSH host defaults to the Proxmox API host.
	var pveHost, tokenID, secret, pveNode string
	var err error
	if !opts.dryRun {
		pveHost, tokenID, secret, pveNode, err = getProxmoxCredentialsFn()
		if err != nil {
			return err
		}
	}
	sshHost := opts.pveSSHHost
	if sshHost == "" {
		sshHost = pveHost
	}
	sshUser := opts.pveSSHUser
	if sshUser == "" {
		sshUser = "root"
	}
	sshPort := opts.pveSSHPort
	if sshPort == "" {
		sshPort = "22"
	}

	configs := make([]proxmox.VMConfig, 0, len(opts.nodes))
	for _, nodeName := range opts.nodes {
		nodeConfig, ok := getFlatcarNodeConfigFn(nodeName)
		if !ok {
			return fmt.Errorf("unknown flatcar node %q (known: %s)", nodeName, strings.Join(nodeNames(), ", "))
		}

		env, err := buildNodeEnv(nodeName, opts.vip, opts.pauseImage, opts.kubeVipVersion, opts.nodeInterface)
		if err != nil {
			return err
		}

		ign, err := renderIgnitionFn(env)
		if err != nil {
			return fmt.Errorf("failed to render ignition for %s: %w", nodeName, err)
		}

		// Upload Ignition to the snippets dir ON the Proxmox node so qemu can read it
		// for the fw_cfg file= attach (works when this CLI runs off-host, e.g. varunlnx0).
		ignPath := fmt.Sprintf("%s/ignition-%s.json", strings.TrimRight(opts.snippetsDir, "/"), nodeName)
		if opts.dryRun {
			dst := sshHost
			if dst == "" {
				dst = "<proxmox-host>"
			}
			logger.Info("[DRY RUN] would upload %d bytes of Ignition to %s@%s:%s for %s", len(ign), sshUser, dst, ignPath, nodeName)
		} else {
			logger.Info("Uploading Ignition for %s to %s@%s:%s", nodeName, sshUser, sshHost, ignPath)
			if uerr := uploadIgnitionToPVEFn(sshHost, sshUser, sshPort, ignPath, ign); uerr != nil {
				return fmt.Errorf("failed to upload ignition for %s to Proxmox host %s: %w", nodeName, sshHost, uerr)
			}
		}

		vmConfig := proxmoxDefaultVMConfig
		vmConfig.Name = nodeName
		vmConfig.BootStorage = nodeConfig.BootStorage
		vmConfig.OpenEBSStorage = nodeConfig.OpenEBSStorage
		vmConfig.CephDiskByID = nodeConfig.CephDiskByID
		vmConfig.CPUAffinity = nodeConfig.CPUAffinity
		vmConfig.NUMANode = nodeConfig.NUMANode
		vmConfig.MacAddress = nodeConfig.MacAddress
		vmConfig.IgnitionConfig = string(ign)
		vmConfig.IgnitionPath = ignPath
		vmConfig.ImageDiskPath = opts.imagePath
		vmConfig.ImageVolume = opts.imageVolume
		vmConfig.PowerOn = opts.powerOn

		configs = append(configs, vmConfig)
	}

	if opts.dryRun {
		for _, c := range configs {
			logger.Info("[DRY RUN] would deploy %s (boot=%s, mac=%s, vmid via predefined)", c.Name, deployBootSource(c), c.MacAddress)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "[DRY RUN] %d Flatcar VM(s) planned\n", len(configs))
		return nil
	}

	return deployConcurrently(logger, pveHost, tokenID, secret, pveNode, configs, opts.concurrent)
}

func deployBootSource(c proxmox.VMConfig) string {
	if c.ImageVolume != "" {
		return "volume:" + c.ImageVolume
	}
	return "import:" + c.ImageDiskPath
}

// deployConcurrently mirrors the Talos proxmox concurrent deploy pattern.
func deployConcurrently(logger *common.ColorLogger, host, tokenID, secret, nodeName string, configs []proxmox.VMConfig, concurrent int) error {
	if concurrent <= 0 {
		concurrent = 1
	}
	if concurrent > len(configs) {
		concurrent = len(configs)
	}
	if concurrent == 0 {
		concurrent = 1
	}

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, concurrent)
		mu       sync.Mutex
		failures []string
	)

	for _, cfg := range configs {
		cfg := cfg
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			logger.Info("Deploying Flatcar VM %s", cfg.Name)
			vmManager, err := newProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, true))
			if err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: failed to create Proxmox VM manager: %v", cfg.Name, err))
				mu.Unlock()
				return
			}
			defer func() {
				if closeErr := vmManager.Close(); closeErr != nil {
					logger.Warn("Failed to close Proxmox VM manager for %s: %v", cfg.Name, closeErr)
				}
			}()

			if err := vmManager.DeployVM(cfg); err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", cfg.Name, err))
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	if len(failures) > 0 {
		return fmt.Errorf("failed to deploy %d/%d Flatcar VMs: %s", len(failures), len(configs), strings.Join(failures, "; "))
	}
	return nil
}
