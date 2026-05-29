// Package flatcar implements the `homeops-cli flatcar` command group: Flatcar
// Container Linux + kubeadm provisioning (Ignition render, kubeadm config gen, and
// Proxmox VM deploy). It is additive to the Talos command group.
package flatcar

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"

	"github.com/spf13/cobra"
)

// Swappable function vars for testability (mirrors cmd/talos patterns).
var (
	getVersionsFn            = versionconfig.GetVersions
	workingDirectoryFn       = common.GetWorkingDirectory
	renderIgnitionFn         = flatcar.RenderIgnition
	renderKubeadmInitFn      = flatcar.RenderKubeadmInitConfig
	renderKubeadmJoinFn      = flatcar.RenderKubeadmJoinConfig
	getFlatcarNodeConfigFn   = proxmox.GetFlatcarNodeConfig
	getProxmoxCredentialsFn  = proxmox.GetCredentials
	proxmoxDefaultVMConfig   = proxmox.DefaultVMConfig
	newProxmoxVMManagerFn    = func(host, tokenID, secret, nodeName string, insecure bool) (proxmoxVMManager, error) {
		return proxmox.NewVMManager(host, tokenID, secret, nodeName, insecure)
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

	return flatcar.NodeEnv{
		NodeName:          nodeConfig.Name,
		NodeIP:            nodeConfig.NodeIP,
		Node0IP:           constants.FlatcarNode0IP,
		Node1IP:           constants.FlatcarNode1IP,
		Node2IP:           constants.FlatcarNode2IP,
		KubernetesVersion: versions.KubernetesVersion,
		KubernetesMinor:   kubernetesMinor(versions.KubernetesVersion),
		ControlPlaneVIP:   vip,
		PauseImage:        pauseImage,
		KubeVipVersion:    kubeVipVersion,
		NodeInterface:     nodeInterface,
	}, nil
}

// kubernetesMinor derives "vX.Y" from "vX.Y.Z".
func kubernetesMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
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
	cmd.Flags().StringVar(&imagePath, "image-path", "", "Path on Proxmox to import the Flatcar disk image from (import-from)")
	cmd.Flags().StringVar(&imageVolume, "image-volume", "", "Existing storage volume to attach as scsi0 (alternative to --image-path)")
	cmd.Flags().StringVar(&snippetsDir, "snippets-dir", "/var/lib/vz/snippets", "Proxmox snippets dir for Ignition files")
	cmd.Flags().StringVar(&vip, "vip", "", "Control-plane VIP (default from constants)")
	cmd.Flags().StringVar(&pauseImage, "pause-image", "", "Pause/sandbox image (default from versions)")
	cmd.Flags().StringVar(&kubeVipVersion, "kube-vip-version", "", "kube-vip image tag (default from versions)")
	cmd.Flags().StringVar(&nodeInterface, "interface", "", "Node primary interface (default eth0)")
	cmd.Flags().IntVar(&concurrent, "concurrent", 1, "Max concurrent deployments")
	cmd.Flags().BoolVar(&powerOn, "power-on", false, "Power on VMs after creation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Render and build configs but do not create VMs")

	return cmd
}

type deployVMOptions struct {
	nodes          []string
	imagePath      string
	imageVolume    string
	snippetsDir    string
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

		// Write Ignition to the snippets dir so Proxmox can read it for fw_cfg.
		ignPath := fmt.Sprintf("%s/ignition-%s.json", strings.TrimRight(opts.snippetsDir, "/"), nodeName)
		if opts.dryRun {
			logger.Info("[DRY RUN] would write %d bytes of Ignition to %s for %s", len(ign), ignPath, nodeName)
		} else {
			if err := os.WriteFile(ignPath, ign, 0o644); err != nil {
				return fmt.Errorf("failed to write ignition file %s (must be a Proxmox-readable path): %w", ignPath, err)
			}
			logger.Info("Wrote Ignition for %s to %s", nodeName, ignPath)
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

	host, tokenID, secret, nodeName, err := getProxmoxCredentialsFn()
	if err != nil {
		return err
	}

	return deployConcurrently(logger, host, tokenID, secret, nodeName, configs, opts.concurrent)
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
			vmManager, err := newProxmoxVMManagerFn(host, tokenID, secret, nodeName, true)
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
