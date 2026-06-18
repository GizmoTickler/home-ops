package talos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"homeops-cli/cmd/completion"
	"homeops-cli/cmd/vm"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/iso"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/secrets"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/state"
	"homeops-cli/internal/talos"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"
	"homeops-cli/internal/vsphere"
	localyaml "homeops-cli/internal/yaml"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// talosCommandTimeout caps how long we wait for talosctl invocations that don't
// have their own dedicated timeout knob (apply-config, kubeconfig).
const talosCommandTimeout = 10 * time.Minute

var (
	chooseTalosNodeFn                 = ui.Choose
	chooseOptionFn                    = ui.Choose
	inputPromptFn                     = ui.Input
	confirmActionFn                   = ui.Confirm
	proxmoxGetTalosNodeConfigFn       = proxmox.GetTalosNodeConfig
	proxmoxDefaultVMConfig            = proxmox.GetDefaultVMConfig
	getTalosNodeIPsFn                 = talos.GetNodeIPs
	getTalosTemplateFn                = templates.GetTalosTemplate
	workingDirectoryFn                = common.GetWorkingDirectory
	getMachineTypeFromNodeFn          = getMachineTypeFromNode
	renderMachineConfigFromEmbeddedFn = renderMachineConfigFromEmbedded
	injectSecretsFn                   = secrets.Inject
	ensure1PasswordAuthFn             = secrets.EnsureOpAuth
	talosctlOutputFn                  = common.Output
	talosctlCombinedOutputFn          = common.CombinedOutput
	talosApplyConfigFn                = func(nodeIP, mode, config string) ([]byte, error) {
		cmd := common.Command("talosctl", "--nodes", nodeIP, "apply-config", "--mode", mode, "--file", "/dev/stdin")
		cmd.Stdin = bytes.NewReader([]byte(config))
		out, err := cmd.CombinedOutput()
		// Redact before returning — apply-config error output may echo a snippet of
		// the machineconfig that contains secrets.
		return []byte(common.RedactCommandOutput(string(out))), err
	}
	talosctlNodeOutputFn = func(nodeIP string, args ...string) ([]byte, error) {
		commandArgs := append([]string{"--nodes", nodeIP}, args...)
		return common.Output("talosctl", commandArgs...)
	}
	generateKubeconfigFn = func(node, rootDir string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), talosCommandTimeout)
		defer cancel()
		result, err := common.RunCommand(ctx, common.CommandOptions{
			Name: "talosctl",
			Args: []string{"kubeconfig", "--nodes", node, "--force", "--force-context-name", "home-ops-cluster", rootDir},
		})
		// Combined output preserves diagnostic information without leaking raw secrets
		// (kubeconfig content goes to disk via talosctl, not stdout).
		combined := []byte(result.Stdout + result.Stderr)
		return combined, err
	}
	// pushKubeconfigFn / pullKubeconfigFn persist the kubeconfig through the
	// configured state store (1Password item or local file).
	pushKubeconfigFn = func(sourcePath string, logger *common.ColorLogger) error {
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return fmt.Errorf("failed to read kubeconfig file: %w", err)
		}
		return state.NewKubeconfigStore(versionconfig.Get().State.Kubeconfig).Save(content, logger)
	}
	pullKubeconfigFn = func(destPath string, logger *common.ColorLogger) error {
		return state.NewKubeconfigStore(versionconfig.Get().State.Kubeconfig).Pull(destPath, logger)
	}
	prepareISOForTrueNASFn             = prepareISOForTrueNAS
	prepareISOForProxmoxFn             = prepareISOForProxmox
	prepareISOForVSphereFn             = prepareISOForVSphere
	prepareISOForTargetFn              = prepareISOForTarget
	spinWithFuncFn                     = ui.SpinWithFunc
	spinCommandFn                      = ui.Spin
	updateNodeTemplatesWithSchematicFn = updateNodeTemplatesWithSchematic
	uploadISOToVSphereFn               = uploadISOToVSphere
	// isoDownloadClient bounds ISO downloads: without a timeout a stalled
	// mirror hangs prepare-iso/deploy-vm forever. 30m accommodates slow links.
	isoDownloadClient        = &http.Client{Timeout: 30 * time.Minute}
	httpGetFn                = isoDownloadClient.Get
	controlplaneTemplatePath = "cmd/homeops-cli/internal/templates/talos/controlplane.yaml"
	newISODownloaderFn       = func() isoDownloader {
		return iso.NewDownloader()
	}
	newTalosFactoryClientFn = func() talosFactoryClient {
		return talos.NewFactoryClient()
	}
	newTrueNASSSHClientFn = func(config ssh.SSHConfig) trueNASSSHClient {
		return ssh.NewSSHClient(config)
	}
	newVSphereDeployerFn = func(host, username, password string) (vsphereVMDeployer, error) {
		client := vsphere.NewClient(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false))
		if err := client.Connect(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false)); err != nil {
			return nil, fmt.Errorf("failed to connect to vSphere: %w", err)
		}
		return &defaultVSphereDeployer{client: client}, nil
	}
	newESXiK8sVMDeployerFn = func(host, username string) (esxiK8sVMDeployer, error) {
		return vsphere.NewESXiSSHClient(host, username)
	}
)

type talosFactoryClient interface {
	LoadSchematicFromTemplate() (*talos.SchematicConfig, error)
	GenerateISOFromSchematic(*talos.SchematicConfig, string, string, string) (*talos.ISOInfo, error)
}

type isoDownloader interface {
	DownloadCustomISO(iso.DownloadConfig) error
}

type trueNASSSHClient interface {
	Connect() error
	Close() error
	VerifyFile(string) (bool, int64, error)
}

type vsphereVMDeployer interface {
	CreateVM(vsphere.VMConfig) error
	DeployVMsConcurrently([]vsphere.VMConfig) error
	Close() error
}

type defaultVSphereDeployer struct {
	client *vsphere.Client
}

func (d *defaultVSphereDeployer) CreateVM(config vsphere.VMConfig) error {
	_, err := d.client.CreateVM(config)
	return err
}

func (d *defaultVSphereDeployer) DeployVMsConcurrently(configs []vsphere.VMConfig) error {
	return d.client.DeployVMsConcurrently(configs)
}

func (d *defaultVSphereDeployer) Close() error {
	return d.client.Close()
}

type esxiK8sVMDeployer interface {
	CreateK8sVM(vsphere.VMConfig) error
	Close()
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "talos",
		Short: "Manage Talos Linux nodes and clusters",
		Long: `Commands for managing Talos Linux nodes, including configuration, upgrades, and VM deployments.

Use ` + "`homeops-cli vm`" + ` for VM lifecycle operations such as list, start, stop, info, poweron, poweroff, and delete.`,
	}

	// Add subcommands
	cmd.AddCommand(
		newApplyNodeCommand(),
		newUpgradeNodeCommand(),
		newUpgradeK8sCommand(),
		newRebootNodeCommand(),
		newShutdownClusterCommand(),
		newResetNodeCommand(),
		newResetClusterCommand(),
		newKubeconfigCommand(),
		newPrepareISOCommand(),
		newDeployVMCommand(),
		vm.NewManageVMCommand(),
		vm.NewVMLifecycleRootGuidanceCommand("list"),
		vm.NewVMLifecycleRootGuidanceCommand("start"),
		vm.NewVMLifecycleRootGuidanceCommand("stop"),
		vm.NewVMLifecycleRootGuidanceCommand("info"),
		vm.NewVMLifecycleRootGuidanceCommand("poweron"),
		vm.NewVMLifecycleRootGuidanceCommand("poweroff"),
		vm.NewVMLifecycleRootGuidanceCommand("delete"),
	)

	return cmd
}

type talosConfigInfo struct {
	Endpoints []string `json:"endpoints"`
	Nodes     []string `json:"nodes"`
}

func selectTalosNode(prompt string) (string, error) {
	nodeIPs, err := getTalosNodeIPsFn()
	if err != nil {
		return "", err
	}

	selectedNode, err := chooseTalosNodeFn(prompt, nodeIPs)
	if err != nil {
		if ui.IsCancellation(err) {
			return "", nil
		}
		return "", fmt.Errorf("node selection failed: %w", err)
	}

	return selectedNode, nil
}

func getTalosConfigInfo() (*talosConfigInfo, error) {
	output, err := talosctlOutputFn("talosctl", "config", "info", "--output", "json")
	if err != nil {
		return nil, err
	}

	var configInfo talosConfigInfo
	if err := json.Unmarshal(output, &configInfo); err != nil {
		return nil, err
	}

	return &configInfo, nil
}

func runTalosctlCombinedOutput(args ...string) ([]byte, error) {
	return talosctlCombinedOutputFn("talosctl", args...)
}

func newApplyNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "apply-node",
		Short: "Apply Talos config to a node",
		Long:  `Apply Talos configuration to a node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyNodeConfig(nodeIP, mode, dryRun)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&mode, "mode", "auto", "Apply mode (auto, interactive, etc.)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Render and validate, but do not apply the configuration")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func applyNodeConfig(nodeIP, mode string, dryRun bool) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Get machine type
	machineType, err := getMachineTypeFromNodeFn(nodeIP)
	if err != nil {
		return fmt.Errorf("failed to get machine type: %w", err)
	}

	logger.Info("Applying configuration to node %s (type: %s)", nodeIP, machineType)

	// Render machine config using embedded templates
	machineConfigTemplate := fmt.Sprintf("talos/%s.yaml", machineType)
	nodeConfigTemplate := fmt.Sprintf("talos/nodes/%s.yaml", nodeIP)

	// Render the configuration
	renderedConfig, err := renderMachineConfigFromEmbeddedFn(machineConfigTemplate, nodeConfigTemplate)
	if err != nil {
		return fmt.Errorf("failed to render config: %w", err)
	}

	// Resolve 1Password references in the rendered config with signin-once retry
	logger.Info("Resolving 1Password references in Talos configuration...")
	resolvedConfig, err := injectSecretsFn(string(renderedConfig))
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not authenticated") || strings.Contains(errStr, "not signed in") || strings.Contains(errStr, "please run 'op signin'") {
			logger.Info("Attempting 1Password CLI signin due to authentication error...")
			if err2 := ensure1PasswordAuthFn(); err2 != nil {
				return fmt.Errorf("1Password signin failed: %w (original: %v)", err2, err)
			}
			// Retry once after successful signin
			if retryResolved, retryErr := injectSecretsFn(string(renderedConfig)); retryErr == nil {
				resolvedConfig = retryResolved
			} else {
				return fmt.Errorf("secret resolution failed after signin: %w", retryErr)
			}
		} else {
			return fmt.Errorf("failed to resolve 1Password references: %w", err)
		}
	}

	if dryRun {
		// Basic YAML validation to ensure the rendered config is structurally valid
		var data any
		if err := yaml.Unmarshal([]byte(resolvedConfig), &data); err != nil {
			return fmt.Errorf("rendered config failed YAML validation: %w", err)
		}
		logger.Info("[DRY RUN] Would apply config to %s (type: %s)", nodeIP, machineType)
		return nil
	}

	// Apply the configuration
	output, err := talosApplyConfigFn(nodeIP, mode, resolvedConfig)
	if err != nil {
		return fmt.Errorf("failed to apply config: %w\n%s", err, output)
	}

	logger.Success("Configuration applied successfully to %s", nodeIP)
	return nil
}

func getMachineTypeFromNode(nodeIP string) (string, error) {
	output, err := talosctlNodeOutputFn(nodeIP, "get", "machinetypes", "--output=jsonpath={.spec}")
	if err != nil {
		return "", fmt.Errorf("failed to get machine type: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate string) ([]byte, error) {
	return renderMachineConfigFromEmbeddedWithSchematic(baseTemplate, patchTemplate, "")
}

func renderMachineConfigFromEmbeddedWithSchematic(baseTemplate, patchTemplate, schematicID string) ([]byte, error) {
	logger := common.NewColorLogger()
	metricsCollector := metrics.NewPerformanceCollector()
	defer metricsCollector.LogReport(logger)

	// Create unified template renderer
	renderer := templates.NewTemplateRenderer(common.GetWorkingDirectory(), logger, metricsCollector)

	// Prepare environment variables for template rendering
	env := make(map[string]string)
	env["SCHEMATIC_ID"] = schematicID

	// Add other common environment variables
	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	env["KUBERNETES_VERSION"] = versionConfig.KubernetesVersion
	env["TALOS_VERSION"] = versionConfig.TalosVersion

	// Use the unified renderer for Talos config rendering and merging
	return renderer.RenderTalosConfigWithMerge(baseTemplate, patchTemplate, env)
}

func newUpgradeNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "upgrade-node",
		Short: "Upgrade Talos on a single node",
		Long:  `Upgrade Talos on a node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return upgradeNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func upgradeNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node to upgrade:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Get factory image from controlplane config instead of individual node configs
	controlplaneTemplate := "talos/controlplane.yaml"
	configOutput, err := getTalosTemplateFn(controlplaneTemplate)
	if err != nil {
		return fmt.Errorf("failed to get controlplane config: %w", err)
	}

	// Extract factory image using Go YAML processor
	metricsCollector := metrics.NewPerformanceCollector()
	defer metricsCollector.LogReport(common.NewColorLogger())
	processor := localyaml.NewProcessor(nil, metricsCollector)

	// Parse YAML content into a map
	configData, err := processor.ParseString(configOutput)
	if err != nil {
		return fmt.Errorf("failed to parse controlplane config: %w", err)
	}

	// Extract factory image using GetValue
	factoryImageValue, err := processor.GetValue(configData, "machine.install.image")
	if err != nil {
		return fmt.Errorf("failed to get factory image: %w", err)
	}

	factoryImage, ok := factoryImageValue.(string)
	if !ok {
		return fmt.Errorf("factory image is not a string: %v", factoryImageValue)
	}

	logger.Info("Upgrading node %s to image: %s", nodeIP, factoryImage)

	// Perform upgrade with spinner
	err = spinCommandFn(fmt.Sprintf("Upgrading node %s", nodeIP),
		"talosctl", "--nodes", nodeIP, "upgrade",
		"--image", factoryImage,
		"--reboot-mode", mode,
		"--timeout", "10m")

	if err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	logger.Success("Node %s upgraded successfully", nodeIP)
	return nil
}

func newUpgradeK8sCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "upgrade-k8s",
		Short:   "Upgrade Kubernetes across the whole cluster",
		Example: `  homeops-cli talos upgrade-k8s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return upgradeK8s()
		},
	}

	return cmd
}

func upgradeK8s() error {
	logger := common.NewColorLogger()

	// Get a random node
	node, err := getRandomNode()
	if err != nil {
		return err
	}

	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	k8sVersion := vmlifecycle.GetEnvOrDefault("KUBERNETES_VERSION", versionConfig.KubernetesVersion)
	if k8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}

	logger.Info("Upgrading Kubernetes to version %s via node %s", k8sVersion, node)

	// Perform Kubernetes upgrade with spinner
	err = spinCommandFn(fmt.Sprintf("Upgrading Kubernetes to %s", k8sVersion),
		"talosctl", "--nodes", node, "upgrade-k8s", "--to", k8sVersion)

	if err != nil {
		return fmt.Errorf("kubernetes upgrade failed: %w", err)
	}

	logger.Success("Kubernetes upgraded successfully to %s", k8sVersion)
	return nil
}

func getRandomNode() (string, error) {
	configInfo, err := getTalosConfigInfo()
	if err != nil {
		return "", err
	}

	if len(configInfo.Endpoints) == 0 {
		return "", fmt.Errorf("no endpoints found")
	}

	return configInfo.Endpoints[0], nil
}

func newRebootNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "reboot-node",
		Short: "Reboot Talos on a single node",
		Long:  `Reboot a Talos node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return rebootNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func rebootNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node to reboot:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Add confirmation for reboot
	confirmed, err := confirmActionFn(fmt.Sprintf("Are you sure you want to reboot node %s?", nodeIP), false)
	if err != nil {
		return fmt.Errorf("confirmation failed: %w", err)
	}
	if !confirmed {
		logger.Info("Reboot cancelled")
		return nil
	}
	logger.Info("Rebooting node %s with mode %s", nodeIP, mode)

	output, err := runTalosctlCombinedOutput("--nodes", nodeIP, "reboot", "--mode", mode)
	if err != nil {
		return fmt.Errorf("reboot failed: %w\n%s", err, output)
	}

	logger.Success("Node %s reboot initiated", nodeIP)
	return nil
}

func newShutdownClusterCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "shutdown-cluster",
		Short:   "Shutdown Talos across the whole cluster",
		Example: `  homeops-cli talos shutdown-cluster --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := confirmActionFn("Shutdown the Talos cluster?", false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
					return fmt.Errorf("shutdown cancelled")
				}
			}
			return shutdownCluster()
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force shutdown without confirmation")

	return cmd
}

func shutdownCluster() error {
	logger := common.NewColorLogger()

	// Get all nodes
	nodes, err := getAllNodes()
	if err != nil {
		return err
	}

	logger.Info("Shutting down cluster nodes: %s", strings.Join(nodes, ", "))

	output, err := runTalosctlCombinedOutput("shutdown", "--nodes", strings.Join(nodes, ","), "--force")
	if err != nil {
		return fmt.Errorf("shutdown failed: %w\n%s", err, output)
	}

	logger.Success("Cluster shutdown initiated")
	return nil
}

func getAllNodes() ([]string, error) {
	configInfo, err := getTalosConfigInfo()
	if err != nil {
		return nil, err
	}

	return configInfo.Nodes, nil
}

func newResetNodeCommand() *cobra.Command {
	var (
		nodeIP string
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "reset-node",
		Short: "Reset Talos on a single node",
		Long:  `Reset a Talos node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return resetNode(nodeIP, force)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func resetNode(nodeIP string, force bool) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node to reset:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Add confirmation for reset
	if !force {
		confirmed, err := confirmActionFn(fmt.Sprintf("Reset Talos node '%s'? This is destructive!", nodeIP), false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			logger.Info("Reset cancelled")
			return fmt.Errorf("reset cancelled")
		}
	}

	logger.Info("Resetting node %s", nodeIP)

	output, err := runTalosctlCombinedOutput("reset", "--nodes", nodeIP, "--graceful=false")
	if err != nil {
		return fmt.Errorf("reset failed: %w\n%s", err, output)
	}

	logger.Success("Node %s reset initiated", nodeIP)
	return nil
}

func newResetClusterCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset-cluster",
		Short: "Reset Talos across the whole cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := confirmActionFn("Reset the Talos cluster? This is destructive!", false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
					return fmt.Errorf("reset cancelled")
				}
			}
			return resetCluster()
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")

	return cmd
}

func resetCluster() error {
	logger := common.NewColorLogger()

	// Get all nodes
	nodes, err := getAllNodes()
	if err != nil {
		return err
	}

	logger.Info("Resetting cluster nodes: %s", strings.Join(nodes, ", "))

	output, err := runTalosctlCombinedOutput("reset", "--nodes", strings.Join(nodes, ","), "--graceful=false")
	if err != nil {
		return fmt.Errorf("reset failed: %w\n%s", err, output)
	}

	logger.Success("Cluster reset initiated")
	return nil
}

func newKubeconfigCommand() *cobra.Command {
	var push, pull bool

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate, push, or pull kubeconfig for a Talos cluster",
		Long: `Manage kubeconfig for a Talos cluster.

Without flags: generates kubeconfig from a cluster node
With --push: generates and saves to 1Password
With --pull: retrieves kubeconfig from 1Password`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()

			if pull {
				return pullKubeconfigFromStore(logger)
			}
			if err := generateKubeconfig(); err != nil {
				return err
			}
			if push {
				return pushKubeconfigToStore(logger)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&push, "push", false, "Save generated kubeconfig to 1Password")
	cmd.Flags().BoolVar(&pull, "pull", false, "Pull kubeconfig from 1Password")
	cmd.MarkFlagsMutuallyExclusive("push", "pull")

	return cmd
}

func generateKubeconfig() error {
	logger := common.NewColorLogger()

	// Get a random node
	node, err := getRandomNode()
	if err != nil {
		return err
	}

	rootDir := workingDirectoryFn()
	logger.Info("Generating kubeconfig from node %s", node)

	output, err := generateKubeconfigFn(node, rootDir)
	if err != nil {
		return fmt.Errorf("failed to generate kubeconfig: %w\n%s", err, output)
	}

	logger.Success("Kubeconfig generated successfully")
	return nil
}

func pushKubeconfigToStore(logger *common.ColorLogger) error {
	rootDir := workingDirectoryFn()
	kubeconfigPath := filepath.Join(rootDir, "kubeconfig")

	store := state.NewKubeconfigStore(versionconfig.Get().State.Kubeconfig)
	logger.Info("Pushing kubeconfig to %s...", store.Describe())
	if err := pushKubeconfigFn(kubeconfigPath, logger); err != nil {
		return err
	}

	logger.Success("Kubeconfig saved to %s", store.Describe())
	return nil
}

func pullKubeconfigFromStore(logger *common.ColorLogger) error {
	rootDir := workingDirectoryFn()
	kubeconfigPath := filepath.Join(rootDir, "kubeconfig")

	logger.Info("Pulling kubeconfig from %s...", state.NewKubeconfigStore(versionconfig.Get().State.Kubeconfig).Describe())
	if err := pullKubeconfigFn(kubeconfigPath, logger); err != nil {
		return err
	}

	logger.Success("Kubeconfig saved to %s", kubeconfigPath)
	return nil
}

func promptDeployVMOptions(name, provider *string, memory, vcpus, diskSize, openebsSize *int, generateISO, dryRun *bool, datastore, network *string, nodeCount, concurrent, startIndex *int) error {
	logger := common.NewColorLogger()

	// Step 1: Select deployment pattern
	patternOptions := []string{
		"Default - 3-node k8s cluster (16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS each)",
		"Custom - Choose your own configuration",
	}

	selectedPattern, err := chooseOptionFn("Select deployment pattern:", patternOptions)
	if err != nil {
		return err
	}

	isCustom := strings.HasPrefix(selectedPattern, "Custom")

	// Step 2: Select provider
	providerOptions := deployVMProviderOptions()

	selectedProvider, err := chooseOptionFn("Select virtualization provider:", providerOptions)
	if err != nil {
		return err
	}

	*provider = providerFromDeployOption(selectedProvider)
	logSelectedDeployProvider(logger, *provider)

	// Step 3: Get VM name
	vmName, err := inputPromptFn("Enter VM name (base name for multi-node):", "k8s")
	if err != nil {
		return err
	}
	if vmName == "" {
		vmName = "k8s" // Use default if empty
	}
	*name = vmName
	logger.Info("VM name: %s", vmName)

	// Step 4: Configure resources based on pattern
	if isCustom {
		// Custom configuration
		logger.Info("Custom configuration mode - enter resource values")
		if err := promptDeployVMResourceOptions(*provider, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex); err != nil {
			return err
		}

		if providerSupportsBatchDeploy(*provider) {
			logger.Info("Custom resources: %d VMs with %d vCPUs, %dGB RAM, %dGB boot, %dGB OpenEBS each",
				*nodeCount, *vcpus, *memory/1024, *diskSize, *openebsSize)
		} else {
			logger.Info("Custom resources: %d vCPUs, %dGB RAM, %dGB boot, %dGB OpenEBS",
				*vcpus, *memory/1024, *diskSize, *openebsSize)
		}
	} else {
		applyDefaultDeployVMOptions(*provider, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex)
		logDefaultDeployResources(logger, *provider)
	}

	// Step 5: vSphere-specific configuration (if applicable)
	if *provider == "vsphere" {
		datastoreInput, err := inputPromptFn("Enter datastore name:", "truenas-iscsi")
		if err != nil {
			return err
		}
		if datastoreInput != "" {
			*datastore = datastoreInput
		} else {
			*datastore = "truenas-iscsi"
		}

		networkInput, err := inputPromptFn("Enter network port group:", "vl999")
		if err != nil {
			return err
		}
		if networkInput != "" {
			*network = networkInput
		} else {
			*network = "vl999"
		}
	}

	// Step 6: Ask about ISO generation
	generateOptions := []string{
		"No - Use existing ISO",
		"Yes - Generate custom ISO using schematic.yaml",
	}

	selectedGenerate, err := chooseOptionFn("Generate custom Talos ISO?", generateOptions)
	if err != nil {
		return err
	}

	*generateISO = strings.HasPrefix(selectedGenerate, "Yes")
	if *generateISO {
		logger.Info("Will generate custom ISO using schematic.yaml")
	}

	// Step 7: Ask about dry-run mode
	dryRunOptions := []string{
		"Real Deployment - Actually create the VM",
		"Dry-Run - Preview what would be done without creating the VM",
	}

	selectedDryRun, err := chooseOptionFn("Select deployment mode:", dryRunOptions)
	if err != nil {
		return err
	}

	*dryRun = strings.HasPrefix(selectedDryRun, "Dry-Run")
	if *dryRun {
		logger.Info("🔍 Dry-run mode enabled - no changes will be made")
	}

	return nil
}

func providerFromDeployOption(selectedProvider string) string {
	switch {
	case strings.HasPrefix(selectedProvider, "TrueNAS"):
		return "truenas"
	case strings.HasPrefix(selectedProvider, "Proxmox"):
		return "proxmox"
	default:
		return "vsphere"
	}
}

func logSelectedDeployProvider(logger *common.ColorLogger, provider string) {
	switch provider {
	case "truenas":
		logger.Info("Selected provider: TrueNAS")
	case "proxmox":
		logger.Info("Selected provider: Proxmox VE")
	default:
		logger.Info("Selected provider: vSphere/ESXi")
	}
}

func providerSupportsBatchDeploy(provider string) bool {
	return provider == "proxmox" || provider == "vsphere"
}

func promptIntWithDefault(prompt, placeholder string, defaultValue int) (int, error) {
	input, err := inputPromptFn(prompt, placeholder)
	if err != nil {
		return 0, err
	}
	if input == "" {
		return defaultValue, nil
	}

	value := defaultValue
	_, _ = fmt.Sscanf(input, "%d", &value)
	return value, nil
}

func promptDeployVMBatchOptions(provider string, nodeCount, concurrent, startIndex *int) error {
	if !providerSupportsBatchDeploy(provider) {
		*nodeCount = 1
		*concurrent = 1
		*startIndex = 0
		return nil
	}

	value, err := promptIntWithDefault("Enter number of VMs to deploy:", "3", 3)
	if err != nil {
		return err
	}
	*nodeCount = value

	if *nodeCount > 1 {
		*startIndex, err = promptIntWithDefault("Enter starting index for VM naming:", "0", 0)
		if err != nil {
			return err
		}
		*concurrent, err = promptIntWithDefault("Enter number of concurrent deployments:", "3", 3)
		if err != nil {
			return err
		}
		return nil
	}

	*startIndex = 0
	*concurrent = 1
	return nil
}

func promptDeployVMResourceOptions(provider string, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex *int) error {
	if err := promptDeployVMBatchOptions(provider, nodeCount, concurrent, startIndex); err != nil {
		return err
	}

	var err error
	*vcpus, err = promptIntWithDefault("Enter number of vCPUs:", "16", 16)
	if err != nil {
		return err
	}

	memoryGB, err := promptIntWithDefault("Enter memory in GB:", "48", 48)
	if err != nil {
		return err
	}
	*memory = memoryGB * 1024

	*diskSize, err = promptIntWithDefault("Enter boot disk size in GB:", "250", 250)
	if err != nil {
		return err
	}
	*openebsSize, err = promptIntWithDefault("Enter OpenEBS disk size in GB:", "1024", 1024)
	if err != nil {
		return err
	}

	return nil
}

func applyDefaultDeployVMOptions(provider string, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex *int) {
	*vcpus = 16
	*memory = 49152
	*diskSize = 250
	*openebsSize = 1024

	if providerSupportsBatchDeploy(provider) {
		*nodeCount = 3
		*startIndex = 0
		*concurrent = 3
		return
	}

	*nodeCount = 1
	*startIndex = 0
	*concurrent = 1
}

func logDefaultDeployResources(logger *common.ColorLogger, provider string) {
	if providerSupportsBatchDeploy(provider) {
		logger.Info("Default resources: 3 VMs with 16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS each")
		return
	}

	logger.Info("Default resources: 16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS")
}

func deployVMProviderOptions() []string {
	return []string{
		"Proxmox - Deploy to Proxmox VE (default)",
		"TrueNAS - Deploy to TrueNAS Scale",
		"vSphere/ESXi - Deploy to vSphere or ESXi",
	}
}

func newDeployVMCommand() *cobra.Command {
	var (
		name           string
		memory         int
		vcpus          int
		diskSize       int
		openebsSize    int
		macAddress     string
		pool           string
		skipZVolCreate bool
		generateISO    bool
		provider       string
		dryRun         bool
		// vSphere specific flags
		datastore  string
		network    string
		concurrent int
		nodeCount  int
		startIndex int
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Talos VM on TrueNAS, vSphere/ESXi, or Proxmox",
		Example: `  # Deploy a Talos VM on Proxmox (default provider)
  homeops-cli talos deploy-vm --name k8s-0

  # Deploy on TrueNAS with a generated custom ISO
  homeops-cli talos deploy-vm --provider truenas --name k8s-0 --generate-iso`,
		Long: `Deploy a new Talos VM on TrueNAS, vSphere/ESXi, or Proxmox VE.

Defaults to Proxmox VE deployment. Use --provider truenas for TrueNAS or --provider vsphere/esxi for vSphere/ESXi.

For TrueNAS: Uses proper ZVol naming convention and SPICE console.
For vSphere/ESXi: Deploys to specified datastore with enhanced VM configuration.
For Proxmox: Uses predefined node configs (k8s-0, k8s-1, k8s-2) with UEFI, NUMA, and disk passthrough.

Use --generate-iso to create a custom ISO using the schematic.yaml configuration.

If no flags are provided, presents an interactive menu with default and custom patterns.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()

			// Check if running in interactive mode (no flags set)
			if name == "" && !cmd.Flags().Changed("provider") && !cmd.Flags().Changed("dry-run") {
				// Show interactive prompts
				err := promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent, &startIndex)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return err
				}
			}

			normalizedProvider, err := vmlifecycle.NormalizeVMProvider(provider)
			if err != nil {
				return err
			}
			provider = normalizedProvider

			// Validate required name
			if name == "" {
				return fmt.Errorf("VM name is required (use --name flag or interactive mode)")
			}

			// Show dry-run mode indicator
			if dryRun {
				logger.Info("🔍 DRY-RUN MODE - No changes will be made")
			}

			// Deploy to appropriate provider
			switch provider {
			case "truenas":
				return deployVMWithPatternDryRun(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO, dryRun)
			case "proxmox":
				return deployVMOnProxmoxDryRun(name, memory, vcpus, diskSize, openebsSize, generateISO, concurrent, nodeCount, startIndex, dryRun)
			default:
				return deployVMOnVSphereDryRun(name, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, startIndex, dryRun)
			}
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required for single VM, base name for multiple VMs)")
	cmd.Flags().StringVar(&pool, "pool", vmlifecycle.TruenasDefaultPool("flashstor/VM"), "Storage pool (TrueNAS only)")
	cmd.Flags().IntVar(&memory, "memory", 64*1024, "Memory in MB (default: 64GB)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 16, "Number of vCPUs (default: 16)")
	cmd.Flags().IntVar(&diskSize, "disk-size", 250, "Boot disk size in GB (default: 250GB)")
	cmd.Flags().IntVar(&openebsSize, "openebs-size", 800, "OpenEBS disk size in GB (default: 800GB)")
	cmd.Flags().StringVar(&macAddress, "mac-address", "", "MAC address (optional)")
	cmd.Flags().BoolVar(&skipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation (TrueNAS only)")
	cmd.Flags().BoolVar(&generateISO, "generate-iso", false, "Generate custom ISO using schematic.yaml")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Perform a dry run without creating the VM")

	// vSphere specific flags
	cmd.Flags().StringVar(&datastore, "datastore", "truenas-iscsi", "Datastore name (vSphere: truenas-iscsi, datastore1, etc.)")
	cmd.Flags().StringVar(&network, "network", "vl999", "Network port group name (vSphere only)")
	cmd.Flags().IntVar(&concurrent, "concurrency", 3, "Number of concurrent VM deployments (Proxmox and vSphere)")
	cmd.Flags().IntVar(&concurrent, "concurrent", 3, "Number of concurrent VM deployments (deprecated: use --concurrency)")
	_ = cmd.Flags().MarkDeprecated("concurrent", "use --concurrency")
	cmd.Flags().IntVar(&nodeCount, "node-count", 1, "Number of VMs to deploy (Proxmox and vSphere)")
	cmd.Flags().IntVar(&startIndex, "start-index", 0, "Starting index for generated VM names in batch deployments")

	return cmd
}

func buildVSphereVMNames(baseName string, nodeCount, startIndex int) ([]string, error) {
	return buildDeploymentVMNames(baseName, nodeCount, startIndex)
}

func buildDeploymentVMNames(baseName string, nodeCount, startIndex int) ([]string, error) {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		return nil, fmt.Errorf("VM name is required")
	}
	if nodeCount <= 0 {
		return nil, fmt.Errorf("node count must be greater than 0")
	}
	if startIndex < 0 {
		return nil, fmt.Errorf("start index must be greater than or equal to 0")
	}
	if nodeCount == 1 {
		return []string{baseName}, nil
	}
	if _, exists := vsphere.GetK8sNodeConfig(baseName); exists {
		return nil, fmt.Errorf("multi-node deployment cannot start from a numbered k8s node name (%s). Use the shared base name 'k8s' with --node-count instead", baseName)
	}
	if _, exists := proxmox.GetTalosNodeConfig(baseName); exists {
		return nil, fmt.Errorf("multi-node deployment cannot start from a numbered k8s node name (%s). Use the shared base name 'k8s' with --node-count instead", baseName)
	}

	vmNames := make([]string, 0, nodeCount)
	for i := 0; i < nodeCount; i++ {
		vmNames = append(vmNames, fmt.Sprintf("%s-%d", baseName, startIndex+i))
	}

	return vmNames, nil
}

func buildGenericVSphereVMConfig(name string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network, isoPath string) vsphere.VMConfig {
	return vsphere.VMConfig{
		Name:                 name,
		Memory:               memory,
		VCPUs:                vcpus,
		DiskSize:             diskSize,
		OpenEBSSize:          openebsSize,
		Datastore:            datastore,
		Network:              network,
		ISO:                  isoPath,
		MacAddress:           macAddress,
		PowerOn:              true,
		EnableIOMMU:          true,
		ExposeCounters:       true,
		ThinProvisioned:      true,
		EnablePrecisionClock: true,
		EnableWatchdog:       true,
	}
}

func buildGenericVSphereVMConfigs(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network, isoPath string, nodeCount, startIndex int) ([]vsphere.VMConfig, error) {
	vmNames, err := buildVSphereVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	configs := make([]vsphere.VMConfig, 0, len(vmNames))
	for idx, vmName := range vmNames {
		configMAC := ""
		if idx == 0 && nodeCount == 1 {
			configMAC = macAddress
		}
		configs = append(configs, buildGenericVSphereVMConfig(vmName, memory, vcpus, diskSize, openebsSize, configMAC, datastore, network, isoPath))
	}

	return configs, nil
}

func buildK8sVSphereVMConfigs(baseName string, memory, vcpus, diskSize, openebsSize int, network string, nodeCount, startIndex int) ([]vsphere.VMConfig, error) {
	vmNames, err := buildVSphereVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	configs := make([]vsphere.VMConfig, 0, len(vmNames))
	for _, vmName := range vmNames {
		if _, exists := vsphere.GetK8sNodeConfig(vmName); !exists {
			return nil, fmt.Errorf("no predefined configuration for k8s node: %s (valid nodes: k8s-0, k8s-1, k8s-2, k8s-3)", vmName)
		}

		config := vsphere.GetK8sVMConfig(vmName)
		config.Memory = memory
		config.VCPUs = vcpus
		config.DiskSize = diskSize
		config.OpenEBSSize = openebsSize
		config.Network = network
		config.ISO = vsphere.DefaultISOPath()
		config.PowerOn = true
		configs = append(configs, config)
	}

	return configs, nil
}

type vmDeploymentDryRunSummary struct {
	Provider string
	VMNames  []string
	Lines    []string
}

type vsphereDeploymentPlan struct {
	Mode        string
	VMNames     []string
	Configs     []vsphere.VMConfig
	NodeConfigs []vsphere.K8sNodeConfig
	Concurrent  int
	ISOPath     string
}

type trueNASISOSelection struct {
	ISOPath      string
	SchematicID  string
	TalosVersion string
	CustomISO    bool
}

func emitVMDeploymentDryRunSummary(logger *common.ColorLogger, summary vmDeploymentDryRunSummary, generateISO bool) {
	logger.Info("[DRY RUN] Would deploy VM with the following configuration:")
	logger.Info("  Provider: %s", summary.Provider)
	if len(summary.VMNames) == 1 {
		logger.Info("  VM Name: %s", summary.VMNames[0])
	} else {
		logger.Info("  VM Names: %s", strings.Join(summary.VMNames, ", "))
	}
	for _, line := range summary.Lines {
		logger.Info("  %s", line)
	}
	if generateISO {
		logger.Info("  Generate Custom ISO: Yes")
	}
	logger.Success("[DRY RUN] VM deployment preview complete - no changes made")
}

func buildTrueNASDryRunSummary(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate bool) vmDeploymentDryRunSummary {
	lines := []string{
		fmt.Sprintf("Pool: %s", pool),
		fmt.Sprintf("Memory: %d MB (%d GB)", memory, memory/1024),
		fmt.Sprintf("vCPUs: %d", vcpus),
		fmt.Sprintf("Boot Disk: %d GB", diskSize),
		fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
	}
	if macAddress != "" {
		lines = append(lines, fmt.Sprintf("MAC Address: %s", macAddress))
	}
	if skipZVolCreate {
		lines = append(lines, "Skip ZVol Creation: Yes")
	}

	return vmDeploymentDryRunSummary{
		Provider: "TrueNAS",
		VMNames:  []string{name},
		Lines:    lines,
	}
}

func appendBatchDeploymentLines(lines []string, nodeCount, startIndex, concurrent int) []string {
	if nodeCount <= 1 {
		return lines
	}

	lines = append(lines, fmt.Sprintf("Node Count: %d", nodeCount))
	lines = append(lines, fmt.Sprintf("Start Index: %d", startIndex))
	lines = append(lines, fmt.Sprintf("Concurrent Deployments: %d", concurrent))
	return lines
}

func buildVSphereDryRunSummary(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, concurrent, nodeCount, startIndex int) (vmDeploymentDryRunSummary, error) {
	vmNames, err := buildVSphereVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return vmDeploymentDryRunSummary{}, err
	}

	summary := vmDeploymentDryRunSummary{
		Provider: "vSphere/ESXi",
		VMNames:  vmNames,
	}

	if strings.HasPrefix(baseName, "k8s") {
		configs, err := buildK8sVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, network, nodeCount, startIndex)
		if err != nil {
			return vmDeploymentDryRunSummary{}, err
		}
		if len(configs) > 0 {
			nodeConfig, _ := vsphere.GetK8sNodeConfig(configs[0].Name)
			summary.Lines = append(summary.Lines,
				"Deployment Mode: SSH-based (production k8s node)",
				fmt.Sprintf("Boot Datastore: %s", nodeConfig.BootDatastore),
				"OpenEBS Datastore: truenas-iscsi",
				fmt.Sprintf("RDM (Ceph): %s", nodeConfig.RDMPath),
				fmt.Sprintf("SR-IOV PCI Device: %s", nodeConfig.PCIDevice),
			)
			if len(configs) == 1 {
				summary.Lines = append(summary.Lines,
					fmt.Sprintf("MAC Address: %s", nodeConfig.MacAddress),
					fmt.Sprintf("CPU Affinity: %s", nodeConfig.CPUAffinity),
				)
			} else {
				summary.Lines = append(summary.Lines, fmt.Sprintf("Node Presets: %s", strings.Join(vmNames, ", ")))
			}
			summary.Lines = append(summary.Lines,
				fmt.Sprintf("Memory: %d MB (%d GB) - pinned reservation", memory, memory/1024),
				fmt.Sprintf("vCPUs: %d", vcpus),
				fmt.Sprintf("Boot Disk: %d GB", diskSize),
				fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
				fmt.Sprintf("Network: %s (SR-IOV passthrough)", network),
			)
		}
	} else {
		configs, err := buildGenericVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, vsphere.DefaultISOPath(), nodeCount, startIndex)
		if err != nil {
			return vmDeploymentDryRunSummary{}, err
		}
		summary.Lines = append(summary.Lines,
			"Deployment Mode: govmomi (generic VM)",
			fmt.Sprintf("Datastore: %s", datastore),
			fmt.Sprintf("Network: %s (vmxnet3)", network),
			fmt.Sprintf("Memory: %d MB (%d GB)", memory, memory/1024),
			fmt.Sprintf("vCPUs: %d", vcpus),
			fmt.Sprintf("Boot Disk: %d GB", diskSize),
			fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
		)
		if len(configs) == 1 && configs[0].MacAddress != "" {
			summary.Lines = append(summary.Lines, fmt.Sprintf("MAC Address: %s", configs[0].MacAddress))
		}
	}

	summary.Lines = appendBatchDeploymentLines(summary.Lines, len(vmNames), startIndex, concurrent)
	return summary, nil
}

func buildProxmoxDryRunSummary(plan *proxmoxDeploymentPlan, memory, vcpus, diskSize, openebsSize int) vmDeploymentDryRunSummary {
	summary := vmDeploymentDryRunSummary{
		Provider: "Proxmox VE",
		VMNames:  plan.VMNames,
	}

	if plan.AllPredefined {
		summary.Lines = append(summary.Lines, "Deployment Mode: Predefined Talos node configuration")
		if len(plan.Presets) == 1 {
			preset := plan.Presets[0]
			summary.Lines = append(summary.Lines,
				fmt.Sprintf("VMID: %d", preset.VMID),
				fmt.Sprintf("Boot Storage: %s", preset.BootStorage),
				fmt.Sprintf("OpenEBS Storage: %s", preset.OpenEBSStorage),
				fmt.Sprintf("Ceph Disk (passthrough): /dev/disk/by-id/%s", preset.CephDiskByID),
				fmt.Sprintf("CPU Affinity: %s", preset.CPUAffinity),
				fmt.Sprintf("NUMA Node: %d", preset.NUMANode),
				fmt.Sprintf("MAC Address: %s", preset.MacAddress),
			)
		} else {
			summary.Lines = append(summary.Lines, fmt.Sprintf("Node Presets: %s", strings.Join(plan.VMNames, ", ")))
		}

		defaultConfig := proxmox.GetDefaultVMConfig()
		summary.Lines = append(summary.Lines,
			fmt.Sprintf("Memory: %d MB (%d GB)", defaultConfig.Memory, defaultConfig.Memory/1024),
			fmt.Sprintf("vCPUs: %d", defaultConfig.Cores),
			fmt.Sprintf("CPU Type: %s", defaultConfig.CPUType),
			fmt.Sprintf("Boot Disk: %d GB", defaultConfig.BootDiskSize),
			fmt.Sprintf("OpenEBS Disk: %d GB", defaultConfig.OpenEBSSize),
			fmt.Sprintf("Network: %s (VLAN %d, MTU %d)", defaultConfig.NetworkBridge, defaultConfig.VLANID, defaultConfig.NetworkMTU),
			fmt.Sprintf("BIOS: %s (UEFI)", defaultConfig.BIOS),
			"NUMA: Enabled",
			fmt.Sprintf("SCSI Controller: %s", defaultConfig.SCSIController),
		)
	} else {
		summary.Lines = append(summary.Lines,
			"Deployment Mode: Custom configuration",
			fmt.Sprintf("Memory: %d MB (%d GB)", memory, memory/1024),
			fmt.Sprintf("vCPUs: %d", vcpus),
			fmt.Sprintf("Boot Disk: %d GB", diskSize),
			fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
		)
	}

	summary.Lines = appendBatchDeploymentLines(summary.Lines, len(plan.VMNames), plan.StartIndex, plan.Concurrent)
	return summary
}

func buildVSphereDeploymentPlan(mode string, configs []vsphere.VMConfig, nodeConfigs []vsphere.K8sNodeConfig, concurrent int, isoPath string) *vsphereDeploymentPlan {
	vmNames := make([]string, 0, len(configs))
	for _, config := range configs {
		vmNames = append(vmNames, config.Name)
	}

	return &vsphereDeploymentPlan{
		Mode:        mode,
		VMNames:     vmNames,
		Configs:     configs,
		NodeConfigs: nodeConfigs,
		Concurrent:  normalizeDeploymentConcurrency(concurrent, len(configs)),
		ISOPath:     isoPath,
	}
}

func buildGenericVSphereDeploymentPlan(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network, isoPath string, concurrent, nodeCount, startIndex int) (*vsphereDeploymentPlan, error) {
	configs, err := buildGenericVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, isoPath, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	return buildVSphereDeploymentPlan("generic", configs, nil, concurrent, isoPath), nil
}

func buildK8sVSphereDeploymentPlan(baseName string, memory, vcpus, diskSize, openebsSize int, network string, concurrent, nodeCount, startIndex int) (*vsphereDeploymentPlan, error) {
	configs, err := buildK8sVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, network, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	nodeConfigs := make([]vsphere.K8sNodeConfig, 0, len(configs))
	for _, config := range configs {
		nodeConfig, exists := vsphere.GetK8sNodeConfig(config.Name)
		if !exists {
			return nil, fmt.Errorf("no predefined configuration for k8s node: %s", config.Name)
		}
		nodeConfigs = append(nodeConfigs, nodeConfig)
	}

	return buildVSphereDeploymentPlan("k8s", configs, nodeConfigs, concurrent, vsphere.DefaultISOPath()), nil
}

func buildTrueNASVMConfig(name string, memory, vcpus, diskSize, openebsSize int, host, apiKey, isoURL, networkBridge, pool, macAddress, spicePassword, schematicID, talosVersion string, skipZVolCreate, customISO bool) truenas.VMConfig {
	return truenas.VMConfig{
		Name:           name,
		Memory:         memory,
		VCPUs:          vcpus,
		DiskSize:       diskSize,
		OpenEBSSize:    openebsSize,
		TrueNASHost:    host,
		TrueNASAPIKey:  apiKey,
		TrueNASPort:    443,
		NoSSL:          false,
		TalosISO:       isoURL,
		NetworkBridge:  networkBridge,
		StoragePool:    pool,
		MacAddress:     macAddress,
		SkipZVolCreate: skipZVolCreate,
		SpicePassword:  spicePassword,
		UseSpice:       true,
		SchematicID:    schematicID,
		TalosVersion:   talosVersion,
		CustomISO:      customISO,
	}
}

func trueNASPreparedISORequiredError() error {
	return fmt.Errorf("schema-based ISO generation is required for VM deployment. Please use the --generate-iso flag to create a custom Talos ISO, or run 'homeops-cli talos prepare-iso' first to prepare the ISO")
}

func requiredSpicePassword() (string, error) {
	password := vmlifecycle.GetSpicePassword()
	if password == "" {
		return "", fmt.Errorf("SPICE password is required - use SPICE_PASSWORD env var or configure 1Password")
	}
	return password, nil
}

func resolveTrueNASDeploymentAccess(logger *common.ColorLogger) (host, apiKey, spicePassword string, err error) {
	logger.Debug("Retrieving TrueNAS credentials")
	host, apiKey, err = vmlifecycle.GetTrueNASCredentials()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get TrueNAS credentials: %w", err)
	}
	logger.Debug("TrueNAS host: %s", host)

	logger.Debug("Retrieving SPICE password")
	spicePassword, err = requiredSpicePassword()
	if err != nil {
		return "", "", "", err
	}
	logger.Debug("SPICE password retrieved successfully")

	return host, apiKey, spicePassword, nil
}

func connectedTrueNASVMManager(logger *common.ColorLogger, host, apiKey string) (vmlifecycle.TrueNASVMManager, error) {
	logger.Debug("Creating VM manager for TrueNAS host: %s", host)
	vmManager := vmlifecycle.NewTrueNASVMManagerFn(host, apiKey, 443, true)
	if vmManager == nil {
		return nil, fmt.Errorf("failed to create VM manager")
	}

	logger.Debug("Connecting to TrueNAS API")
	if err := vmManager.Connect(); err != nil {
		return nil, fmt.Errorf("TrueNAS connection failed: %w", err)
	}
	logger.Debug("Successfully connected to TrueNAS")
	return vmManager, nil
}

func executeTrueNASVMDeployment(logger *common.ColorLogger, vmManager vmlifecycle.TrueNASVMManager, config truenas.VMConfig) error {
	if err := spinWithFuncFn(fmt.Sprintf("Deploying VM %s", config.Name), func() error {
		logger.Debug("Calling vmManager.DeployVM with configuration")
		if err := vmManager.DeployVM(config); err != nil {
			return fmt.Errorf("VM deployment failed: %w", err)
		}
		return nil
	}); err != nil {
		logger.Error("VM deployment failed: %v", err)
		return err
	}

	return nil
}

func logNamedVMDeploymentStart(logger *common.ColorLogger, provider string, vmNames []string) {
	if len(vmNames) == 1 {
		logger.Info("Starting %s VM deployment: %s", provider, vmNames[0])
		return
	}

	logger.Info("Starting %s deployment for %d VMs: %s", provider, len(vmNames), strings.Join(vmNames, ", "))
}

func logNamedVMDeploymentSuccess(logger *common.ColorLogger, provider string, vmNames []string) {
	if len(vmNames) == 1 {
		logger.Success("VM %s deployed successfully on %s", vmNames[0], provider)
		return
	}

	logger.Success("Deployed %d VMs successfully on %s", len(vmNames), provider)
}

func logTrueNASDeploymentSuccess(logger *common.ColorLogger, config truenas.VMConfig) {
	logger.Success("VM %s deployed successfully!", config.Name)
	logger.Info("VM deployment completed with the following configuration:")
	logger.Info("  VM Name:      %s", config.Name)
	logger.Info("  Memory:       %d MB", config.Memory)
	logger.Info("  vCPUs:        %d", config.VCPUs)
	logger.Info("  Storage Pool: %s", config.StoragePool)
	logger.Info("  Network:      %s", config.NetworkBridge)
	if config.MacAddress != "" {
		logger.Info("  MAC Address:  %s", config.MacAddress)
	}
	logger.Info("  ISO Source:   %s", config.TalosISO)
	if config.CustomISO {
		logger.Info("  Schematic ID: %s", config.SchematicID)
		logger.Info("  Talos Ver:    %s", config.TalosVersion)
	}
	logger.Info("ZVol naming pattern:")
	logger.Info("  Boot disk:   %s/%s-boot (%dGB)", config.StoragePool, config.Name, config.DiskSize)
	if config.OpenEBSSize > 0 {
		logger.Info("  OpenEBS disk: %s/%s-openebs (%dGB)", config.StoragePool, config.Name, config.OpenEBSSize)
	}
}

func prepareGeneratedTrueNASISO(logger *common.ColorLogger) (*trueNASISOSelection, error) {
	logger.Info("STEP 1: Generating custom Talos ISO using schematic.yaml...")

	logger.Debug("Creating Talos factory client")
	factoryClient := newTalosFactoryClientFn()
	if factoryClient == nil {
		return nil, fmt.Errorf("failed to create factory client")
	}

	logger.Debug("Loading schematic from embedded template")
	schematic, err := factoryClient.LoadSchematicFromTemplate()
	if err != nil {
		return nil, fmt.Errorf("failed to load schematic template: %w", err)
	}

	versionConfig := versionconfig.GetVersions(workingDirectoryFn())
	logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=metal", versionConfig.TalosVersion)
	isoInfo, err := factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "metal")
	if err != nil {
		return nil, fmt.Errorf("ISO generation failed: %w", err)
	}
	if isoInfo == nil {
		return nil, fmt.Errorf("ISO generation returned nil result")
	}

	if err := os.Setenv("SCHEMATIC_ID", isoInfo.SchematicID); err != nil {
		logger.Warn("Failed to set SCHEMATIC_ID environment variable: %v", err)
	} else {
		logger.Debug("Set SCHEMATIC_ID environment variable: %s", isoInfo.SchematicID)
	}

	logger.Success("Custom ISO generated successfully")
	logger.Debug("ISO Details: URL=%s, SchematicID=%s, Version=%s", isoInfo.URL, isoInfo.SchematicID, isoInfo.TalosVersion)
	logger.Info("STEP 2: Downloading custom ISO to TrueNAS (REQUIRED BEFORE VM CREATION)...")

	downloader := newISODownloaderFn()
	downloadConfig := iso.GetDefaultConfig()
	downloadConfig.ISOURL = isoInfo.URL
	downloadConfig.ISOFilename = fmt.Sprintf("metal-amd64-%s.iso", isoInfo.SchematicID[:8])

	if err := downloader.DownloadCustomISO(downloadConfig); err != nil {
		return nil, fmt.Errorf("CRITICAL: Failed to download custom ISO to TrueNAS - VM deployment cannot proceed: %w", err)
	}

	logger.Success("Custom ISO downloaded to TrueNAS successfully")
	selection := &trueNASISOSelection{
		ISOPath:      filepath.Join(downloadConfig.ISOStoragePath, downloadConfig.ISOFilename),
		SchematicID:  isoInfo.SchematicID,
		TalosVersion: isoInfo.TalosVersion,
		CustomISO:    true,
	}
	logger.Debug("Updated ISO path: %s", selection.ISOPath)
	logger.Info("ISO preparation completed - proceeding with VM deployment...")
	return selection, nil
}

func verifyPreparedTrueNASISO(logger *common.ColorLogger, host string) (*trueNASISOSelection, error) {
	standardISOPath := versionconfig.Get().TrueNASISOPath()
	logger.Debug("Checking for prepared ISO at: %s", standardISOPath)

	sshConfig := ssh.SSHConfig{
		Host:     host,
		Username: vmlifecycle.ResolveSecretKey(versionconfig.KeyTrueNASUsername),
		Port:     "22",
	}
	sshClient := newTrueNASSSHClientFn(sshConfig)

	if err := sshClient.Connect(); err != nil {
		logger.Warn("Cannot verify prepared ISO due to SSH connection failure: %v", err)
		return nil, trueNASPreparedISORequiredError()
	}
	defer func() {
		if closeErr := sshClient.Close(); closeErr != nil {
			logger.Warn("Failed to close SSH client: %v", closeErr)
		}
	}()

	exists, size, err := sshClient.VerifyFile(standardISOPath)
	if err != nil {
		logger.Warn("Failed to verify prepared ISO: %v", err)
		return nil, trueNASPreparedISORequiredError()
	}
	if !exists {
		logger.Info("No prepared ISO found at %s", standardISOPath)
		return nil, fmt.Errorf("no prepared ISO found. Please run 'homeops-cli talos prepare-iso' first to prepare the ISO, or use the --generate-iso flag to generate a new one")
	}

	versionConfig := versionconfig.GetVersions(workingDirectoryFn())
	logger.Success("Using prepared ISO: %s (size: %d bytes)", standardISOPath, size)
	logger.Info("Prepared ISO found - proceeding with VM deployment...")

	return &trueNASISOSelection{
		ISOPath:      standardISOPath,
		TalosVersion: versionConfig.TalosVersion,
		CustomISO:    true,
	}, nil
}

func resolveTrueNASISOSelection(logger *common.ColorLogger, host string, generateISO bool) (*trueNASISOSelection, error) {
	logger.Debug("Determining ISO configuration (generateISO=%t)", generateISO)
	if generateISO {
		return prepareGeneratedTrueNASISO(logger)
	}

	return verifyPreparedTrueNASISO(logger, host)
}

func executeProxmoxDeploymentPlan(logger *common.ColorLogger, host, tokenID, secret, nodeName string, plan *proxmoxDeploymentPlan) error {
	if len(plan.Configs) == 1 || plan.Concurrent == 1 {
		vmManager, err := vmlifecycle.NewProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
		if err != nil {
			return fmt.Errorf("failed to create Proxmox VM manager: %w", err)
		}
		defer func() {
			if closeErr := vmManager.Close(); closeErr != nil {
				logger.Warn("Failed to close VM manager: %v", closeErr)
			}
		}()

		for _, vmConfig := range plan.Configs {
			if err := vmManager.DeployVM(vmConfig); err != nil {
				return fmt.Errorf("failed to deploy VM %s: %w", vmConfig.Name, err)
			}
		}
		return nil
	}

	logger.Info("Deploying %d Proxmox VMs with concurrency %d", len(plan.Configs), plan.Concurrent)
	return deployProxmoxVMsConcurrently(host, tokenID, secret, nodeName, plan.Configs, plan.Concurrent)
}

func logVSphereGenericSingleVMConfig(logger *common.ColorLogger, config vsphere.VMConfig) {
	logger.Info("Deploying VM: %s", config.Name)
	logger.Info("Enhanced Configuration:")
	logger.Info("  Memory: %d MB", config.Memory)
	logger.Info("  vCPUs: %d", config.VCPUs)
	logger.Info("  Boot Disk: %d GB (thin provisioned: %v)", config.DiskSize, config.ThinProvisioned)
	logger.Info("  OpenEBS Disk: %d GB (thin provisioned: %v)", config.OpenEBSSize, config.ThinProvisioned)
	logger.Info("  Datastore: %s", config.Datastore)
	logger.Info("  Network: %s (vmxnet3)", config.Network)
	logger.Info("  ISO: %s", config.ISO)
	if config.MacAddress != "" {
		logger.Info("  MAC Address: %s", config.MacAddress)
	}
	logger.Info("  IOMMU Enabled: %v", config.EnableIOMMU)
	logger.Info("  CPU Counters Exposed: %v", config.ExposeCounters)
	logger.Info("  Precision Clock: %v", config.EnablePrecisionClock)
	logger.Info("  Watchdog Timer: %v", config.EnableWatchdog)
	logger.Info("  EFI Firmware: enabled")
	logger.Info("  UEFI Secure Boot: disabled")
	logger.Info("  NVME Controllers: 2 (separate for each disk)")
}

func logVSphereGenericParallelPlan(logger *common.ColorLogger, plan *vsphereDeploymentPlan, memory, vcpus, diskSize, openebsSize int, datastore, network string) {
	logger.Info("Deploying %d VMs in parallel (max concurrent: %d)", len(plan.Configs), plan.Concurrent)
	logger.Info("Enhanced VM Configuration (for all VMs):")
	logger.Info("  Memory: %d MB", memory)
	logger.Info("  vCPUs: %d", vcpus)
	logger.Info("  Boot Disk: %d GB (thin provisioned)", diskSize)
	logger.Info("  OpenEBS Disk: %d GB (thin provisioned)", openebsSize)
	logger.Info("  Datastore: %s", datastore)
	logger.Info("  Network: %s (vmxnet3)", network)
	logger.Info("  ISO: %s", plan.ISOPath)
	logger.Info("  IOMMU Enabled: true")
	logger.Info("  CPU Counters Exposed: true")
	logger.Info("  Precision Clock: enabled")
	logger.Info("  Watchdog Timer: enabled")
	logger.Info("  EFI Firmware: enabled")
	logger.Info("  UEFI Secure Boot: disabled")
	logger.Info("  NVME Controllers: 2 (separate for each disk)")
	logger.Info("")
	logger.Info("VMs to deploy:")
	for _, config := range plan.Configs {
		if config.MacAddress != "" {
			logger.Info("  - %s (MAC: %s)", config.Name, config.MacAddress)
		} else {
			logger.Info("  - %s", config.Name)
		}
	}
}

func executeVSphereGenericDeploymentPlan(logger *common.ColorLogger, client vsphereVMDeployer, plan *vsphereDeploymentPlan) error {
	if len(plan.Configs) == 1 {
		if err := client.CreateVM(plan.Configs[0]); err != nil {
			return fmt.Errorf("failed to create VM: %w", err)
		}
		return nil
	}

	if err := client.DeployVMsConcurrently(plan.Configs); err != nil {
		return fmt.Errorf("parallel deployment failed: %w", err)
	}
	return nil
}

func deployVMWithPatternDryRun(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate, generateISO, dryRun bool) error {
	if dryRun {
		logger := common.NewColorLogger()
		summary := buildTrueNASDryRunSummary(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate)
		emitVMDeploymentDryRunSummary(logger, summary, generateISO)
		return nil
	}
	return deployVMWithPattern(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO)
}

func deployVMOnVSphereDryRun(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount, startIndex int, dryRun bool) error {
	if dryRun {
		logger := common.NewColorLogger()
		summary, err := buildVSphereDryRunSummary(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, concurrent, nodeCount, startIndex)
		if err != nil {
			return err
		}
		emitVMDeploymentDryRunSummary(logger, summary, generateISO)
		return nil
	}
	return deployVMOnVSphere(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, startIndex)
}

// deployVMOnProxmoxDryRun handles Proxmox VM deployment with dry-run support
func deployVMOnProxmoxDryRun(baseName string, memory, vcpus, diskSize, openebsSize int, generateISO bool, concurrent, nodeCount, startIndex int, dryRun bool) error {
	logger := common.NewColorLogger()
	plan, err := buildProxmoxDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, concurrent, nodeCount, startIndex)
	if err != nil {
		return err
	}

	if dryRun {
		summary := buildProxmoxDryRunSummary(plan, memory, vcpus, diskSize, openebsSize)
		emitVMDeploymentDryRunSummary(logger, summary, generateISO)
		return nil
	}

	return deployVMOnProxmox(baseName, memory, vcpus, diskSize, openebsSize, generateISO, concurrent, nodeCount, startIndex)
}

// deployVMOnProxmox deploys a VM on Proxmox VE
func deployVMOnProxmox(baseName string, memory, vcpus, diskSize, openebsSize int, generateISO bool, concurrent, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()
	plan, err := buildProxmoxDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, concurrent, nodeCount, startIndex)
	if err != nil {
		return err
	}
	logNamedVMDeploymentStart(logger, "Proxmox VE", plan.VMNames)

	// Get Proxmox credentials
	host, tokenID, secret, nodeName, err := vmlifecycle.GetProxmoxCredentialsFn()
	if err != nil {
		return err
	}

	// Generate ISO if requested
	if generateISO {
		logger.Info("Generating custom Talos ISO...")
		if err := prepareISOForProxmoxFn(); err != nil {
			return fmt.Errorf("failed to prepare ISO: %w", err)
		}
	}

	if err := executeProxmoxDeploymentPlan(logger, host, tokenID, secret, nodeName, plan); err != nil {
		return err
	}

	logNamedVMDeploymentSuccess(logger, "Proxmox VE", plan.VMNames)
	return nil
}

type proxmoxDeploymentPlan struct {
	StartIndex    int
	Concurrent    int
	VMNames       []string
	Configs       []proxmox.VMConfig
	Presets       []proxmox.TalosNodeConfig
	AllPredefined bool
}

func buildProxmoxDeploymentPlan(baseName string, memory, vcpus, diskSize, openebsSize int, concurrent, nodeCount, startIndex int) (*proxmoxDeploymentPlan, error) {
	vmNames, err := buildDeploymentVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	plan := &proxmoxDeploymentPlan{
		StartIndex: startIndex,
		Concurrent: normalizeDeploymentConcurrency(concurrent, len(vmNames)),
		VMNames:    vmNames,
	}

	configs, presets := buildProxmoxVMConfigs(vmNames, memory, vcpus, diskSize, openebsSize)
	plan.Configs = configs
	plan.Presets = presets
	plan.AllPredefined = len(presets) == len(vmNames)

	return plan, nil
}

func buildProxmoxVMConfigs(vmNames []string, memory, vcpus, diskSize, openebsSize int) ([]proxmox.VMConfig, []proxmox.TalosNodeConfig) {
	configs := make([]proxmox.VMConfig, 0, len(vmNames))
	presets := make([]proxmox.TalosNodeConfig, 0, len(vmNames))
	for _, name := range vmNames {
		nodeConfig, isPredefined := proxmoxGetTalosNodeConfigFn(name)

		var vmConfig proxmox.VMConfig
		if isPredefined {
			presets = append(presets, nodeConfig)
			vmConfig = proxmoxDefaultVMConfig()
			vmConfig.Name = name
			vmConfig.BootStorage = nodeConfig.BootStorage
			vmConfig.OpenEBSStorage = nodeConfig.OpenEBSStorage
			vmConfig.CephDiskByID = nodeConfig.CephDiskByID
			vmConfig.CPUAffinity = nodeConfig.CPUAffinity
			vmConfig.NUMANode = nodeConfig.NUMANode
			vmConfig.MacAddress = nodeConfig.MacAddress
		} else {
			vmConfig = proxmoxDefaultVMConfig()
			vmConfig.Name = name
			vmConfig.Memory = memory
			vmConfig.Cores = vcpus
			vmConfig.BootDiskSize = diskSize
			vmConfig.OpenEBSSize = openebsSize
		}

		configs = append(configs, vmConfig)
	}

	return configs, presets
}

func normalizeDeploymentConcurrency(concurrent, total int) int {
	if total <= 1 {
		return 1
	}
	if concurrent <= 0 {
		return 1
	}
	if concurrent > total {
		return total
	}
	return concurrent
}

func deployProxmoxVMsConcurrently(host, tokenID, secret, nodeName string, configs []proxmox.VMConfig, concurrent int) error {
	logger := common.NewColorLogger()
	concurrent = normalizeDeploymentConcurrency(concurrent, len(configs))

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

			logger.Info("Starting Proxmox deployment worker for %s", cfg.Name)
			vmManager, err := vmlifecycle.NewProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
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
		return fmt.Errorf("failed to deploy %d/%d Proxmox VMs: %s", len(failures), len(configs), strings.Join(failures, "; "))
	}

	return nil
}

// prepareISOForProxmox handles Proxmox-specific ISO preparation
func prepareISOForProxmox() error {
	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	isoFilename := fmt.Sprintf("talos-%s-nocloud-amd64.iso", versionConfig.TalosVersion)
	target := isoPreparationTarget{
		providerName:   "Proxmox",
		platform:       "nocloud",
		uploadStep:     "Uploading ISO to Proxmox storage...",
		uploadSpinner:  "Uploading ISO to Proxmox",
		location:       proxmox.GetISOPath("local", isoFilename),
		deployCommand:  "homeops-cli talos deploy-vm --provider proxmox --name <vm_name> [other flags]",
		summaryMessage: "Custom ISO generated and uploaded to Proxmox local storage",
		uploadISO: func(isoInfo *talos.ISOInfo) error {
			return vmlifecycle.WithProxmoxVMManager(common.NewColorLogger(), func(vmManager vmlifecycle.ProxmoxVMManager) error {
				if err := vmManager.UploadISOFromURL(isoInfo.URL, isoFilename, "local"); err != nil {
					return fmt.Errorf("failed to upload custom ISO to Proxmox: %w", err)
				}
				return nil
			})
		},
	}

	return prepareISOForTargetFn(target)
}

func deployVMWithPattern(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate, generateISO bool) error {
	logger := common.NewColorLogger()
	logger.Info("Starting VM deployment: %s", name)
	logger.Debug("VM Configuration: pool=%s, memory=%dMB, vcpus=%d, diskSize=%dGB, openebsSize=%dGB, macAddress=%s, skipZVolCreate=%t, generateISO=%t",
		pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO)

	// Validate input parameters
	if name == "" {
		return fmt.Errorf("VM name cannot be empty")
	}
	if pool == "" {
		return fmt.Errorf("storage pool cannot be empty")
	}
	if memory <= 0 {
		return fmt.Errorf("memory must be greater than 0, got %d", memory)
	}
	if vcpus <= 0 {
		return fmt.Errorf("vCPUs must be greater than 0, got %d", vcpus)
	}
	if diskSize <= 0 {
		return fmt.Errorf("disk size must be greater than 0, got %d", diskSize)
	}
	if openebsSize < 0 {
		return fmt.Errorf("openebs size cannot be negative, got %d", openebsSize)
	}

	// Validate VM name - no dashes allowed
	logger.Debug("Validating VM name: %s", name)
	if err := vmlifecycle.ValidateVMName(name); err != nil {
		return fmt.Errorf("VM name validation failed: %w", err)
	}

	host, apiKey, spicePassword, err := resolveTrueNASDeploymentAccess(logger)
	if err != nil {
		return err
	}

	isoSelection, err := resolveTrueNASISOSelection(logger, host, generateISO)
	if err != nil {
		return err
	}

	vmManager, err := connectedTrueNASVMManager(logger, host, apiKey)
	if err != nil {
		return err
	}

	defer func() {
		logger.Debug("Closing VM manager connection")
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		} else {
			logger.Debug("VM manager connection closed successfully")
		}
	}()

	// Build VM configuration with auto-generated ZVol paths matching the pattern from working scripts
	logger.Debug("Building VM configuration")
	networkBridge := vmlifecycle.TrueNASNetworkBridge()
	logger.Debug("Network bridge: %s", networkBridge)

	config := buildTrueNASVMConfig(name, memory, vcpus, diskSize, openebsSize, host, apiKey, isoSelection.ISOPath, networkBridge, pool, macAddress, spicePassword, isoSelection.SchematicID, isoSelection.TalosVersion, skipZVolCreate, isoSelection.CustomISO)

	logger.Debug("VM configuration built successfully")
	logger.Debug("Configuration summary: Name=%s, Memory=%dMB, vCPUs=%d, ISO=%s, Bridge=%s, Pool=%s",
		name, memory, vcpus, isoSelection.ISOPath, networkBridge, pool)

	// STEP 3: Deploy the VM (ISO is now ready on TrueNAS)
	logger.Info("STEP 3: Starting VM deployment process...")

	if err := executeTrueNASVMDeployment(logger, vmManager, config); err != nil {
		return err
	}
	logTrueNASDeploymentSuccess(logger, config)

	logger.Debug("VM deployment function completed successfully")
	return nil
}

// newPrepareISOCommand creates the prepare-iso command
func newPrepareISOCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "prepare-iso",
		Short: "Generate custom Talos ISO from schematic and upload to storage provider",
		Long: `Generate a custom Talos ISO using schematic.yaml configuration and upload it to the specified provider.
This command will:
1. Generate a Talos schematic from the embedded schematic.yaml template
2. Create a custom ISO from the Talos factory
3. Upload the ISO to Proxmox storage, TrueNAS storage, or a vSphere datastore
4. Update the node configuration templates with the new schematic ID

This separates ISO preparation from VM deployment, allowing you to prepare the ISO once
and deploy multiple VMs using the same custom configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return prepareISOWithProvider(provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Storage provider: proxmox, truenas, or vsphere/esxi (default: hypervisors.default in homeops.yaml)")

	return cmd
}

type isoPreparationTarget struct {
	providerName   string
	platform       string
	uploadStep     string
	uploadSpinner  string
	location       string
	deployCommand  string
	uploadISO      func(*talos.ISOInfo) error
	summaryMessage string
}

// prepareISOWithProvider handles the ISO generation and upload process for different providers
func prepareISOWithProvider(provider string) error {
	normalizedProvider, err := vmlifecycle.NormalizeVMProvider(provider)
	if err != nil {
		return err
	}

	switch normalizedProvider {
	case "truenas":
		return prepareISOForTrueNASFn()
	case "proxmox":
		return prepareISOForProxmoxFn()
	case "vsphere":
		return prepareISOForVSphereFn()
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

func prepareISOForTarget(target isoPreparationTarget) error {
	logger := common.NewColorLogger()
	logger.Info("Starting custom Talos ISO preparation for %s...", target.providerName)

	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	logger.Debug("Using versions: Kubernetes=%s, Talos=%s", versionConfig.KubernetesVersion, versionConfig.TalosVersion)

	logger.Debug("Creating Talos factory client")
	factoryClient := newTalosFactoryClientFn()
	if factoryClient == nil {
		return fmt.Errorf("failed to create factory client")
	}

	logger.Info("STEP 1: Loading schematic configuration...")
	schematic, err := factoryClient.LoadSchematicFromTemplate()
	if err != nil {
		return fmt.Errorf("failed to load schematic template: %w", err)
	}
	logger.Success("Schematic configuration loaded successfully")

	logger.Info("STEP 2: Generating custom Talos ISO...")
	var isoInfo *talos.ISOInfo
	err = spinWithFuncFn("Generating custom Talos ISO", func() error {
		logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=%s", versionConfig.TalosVersion, target.platform)
		var genErr error
		isoInfo, genErr = factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", target.platform)
		if genErr != nil {
			return fmt.Errorf("ISO generation failed: %w", genErr)
		}
		if isoInfo == nil {
			return fmt.Errorf("ISO generation returned nil result")
		}
		return nil
	})
	if err != nil {
		return err
	}

	logger.Success("Custom ISO generated successfully")
	logger.Info("ISO Details:")
	logger.Info("  URL: %s", isoInfo.URL)
	logger.Info("  Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  Talos Version: %s", isoInfo.TalosVersion)

	logger.Info("STEP 3: %s", target.uploadStep)
	err = spinWithFuncFn(target.uploadSpinner, func() error {
		return target.uploadISO(isoInfo)
	})
	if err != nil {
		return err
	}

	logger.Success("Custom ISO uploaded to %s successfully", target.providerName)
	logger.Info("ISO Location: %s", target.location)

	logger.Info("STEP 4: Updating node configuration templates...")
	if err := updateNodeTemplatesWithSchematicFn(isoInfo.SchematicID, isoInfo.TalosVersion); err != nil {
		logger.Warn("Failed to update node templates: %v", err)
		logger.Warn("You may need to manually update the templates with schematic ID: %s", isoInfo.SchematicID)
	} else {
		logger.Success("Node configuration templates updated successfully")
	}

	logger.Success("ISO preparation completed successfully!")
	logger.Info("Summary:")
	logger.Info("  - %s", target.summaryMessage)
	logger.Info("  - Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  - Talos Version: %s", isoInfo.TalosVersion)
	logger.Info("  - ISO Path: %s", target.location)
	logger.Info("  - Node templates updated with new schematic ID")
	logger.Info("")
	logger.Info("You can now deploy VMs using: %s", target.deployCommand)
	logger.Info("(The deploy-vm command will automatically use the prepared ISO)")

	return nil
}

// prepareISOForTrueNAS handles TrueNAS-specific ISO preparation
func prepareISOForTrueNAS() error {
	target := isoPreparationTarget{
		providerName:   "TrueNAS",
		platform:       "metal",
		uploadStep:     "Uploading ISO to TrueNAS...",
		uploadSpinner:  "Uploading ISO to TrueNAS",
		location:       versionconfig.Get().TrueNASISOPath(),
		deployCommand:  "homeops-cli talos deploy-vm --provider truenas --name <vm_name> [other flags]",
		summaryMessage: "Custom ISO generated and uploaded to TrueNAS",
		uploadISO: func(isoInfo *talos.ISOInfo) error {
			downloader := newISODownloaderFn()
			downloadConfig := iso.GetDefaultConfig()
			downloadConfig.ISOURL = isoInfo.URL
			downloadConfig.ISOFilename = filepath.Base(versionconfig.Get().TrueNASISOPath())

			if err := downloader.DownloadCustomISO(downloadConfig); err != nil {
				return fmt.Errorf("failed to upload custom ISO to TrueNAS: %w", err)
			}
			return nil
		},
	}

	return prepareISOForTargetFn(target)
}

// prepareISOForVSphere handles vSphere-specific ISO preparation
func prepareISOForVSphere() error {
	target := isoPreparationTarget{
		providerName:   "vSphere",
		platform:       "nocloud",
		uploadStep:     "Uploading ISO to vSphere datastore...",
		uploadSpinner:  "Uploading ISO to vSphere",
		location:       vsphere.DefaultISOPath(),
		deployCommand:  "homeops-cli talos deploy-vm --provider vsphere --name <vm_name> [other flags]",
		summaryMessage: "Custom ISO generated and uploaded to vSphere datastore1",
		uploadISO: func(isoInfo *talos.ISOInfo) error {
			return uploadISOToVSphereFn(isoInfo.URL)
		},
	}

	return prepareISOForTargetFn(target)
}

// uploadISOToVSphere downloads ISO from URL and uploads it to vSphere datastore
func uploadISOToVSphere(isoURL string) error {
	logger := common.NewColorLogger()

	// Download ISO to temporary file
	logger.Info("Downloading ISO from factory...")
	tempFile, err := downloadISOToTemp(isoURL)
	if err != nil {
		return fmt.Errorf("failed to download ISO: %w", err)
	}
	defer func() {
		if err := os.Remove(tempFile); err != nil {
			logger.Warn("Failed to remove temporary file %s: %v", tempFile, err)
		}
	}()

	logger.Success("ISO downloaded to temporary file: %s", tempFile)

	// Upload to vSphere datastore
	return vmlifecycle.WithVSphereClient(logger, func(client vmlifecycle.VSphereClient) error {
		logger.Info("Uploading ISO to vSphere datastore1...")
		if err := client.UploadISOToDatastore(tempFile, vsphere.DefaultISODatastore, vsphere.DefaultISOFilename); err != nil {
			return fmt.Errorf("failed to upload ISO to datastore: %w", err)
		}

		logger.Success("ISO uploaded to vSphere datastore successfully")
		return nil
	})
}

// downloadISOToTemp downloads ISO from URL to a temporary file and returns the file path
func downloadISOToTemp(isoURL string) (string, error) {
	logger := common.NewColorLogger()

	// Create temporary file
	tempFile, err := os.CreateTemp("", "talos-*.iso")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temporary file: %w", err)
	}

	tempPath := tempFile.Name()
	logger.Debug("Created temporary file: %s", tempPath)

	// Download ISO
	logger.Debug("Downloading from URL: %s", isoURL)
	resp, err := httpGetFn(isoURL)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to download ISO: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to download ISO: HTTP %d", resp.StatusCode)
	}

	// Create file for writing
	outFile, err := os.Create(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer func() { _ = outFile.Close() }()

	// Copy with progress tracking
	size := resp.ContentLength
	if size > 0 {
		logger.Info("Downloading %d MB ISO...", size/(1024*1024))
	}

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to write ISO data: %w", err)
	}

	return tempPath, nil
}

// updateNodeTemplatesWithSchematic updates the controlplane template with the new schematic ID
func updateNodeTemplatesWithSchematic(schematicID, talosVersion string) error {
	logger := common.NewColorLogger()

	// Update controlplane.yaml template with schematic ID
	templateFile := controlplaneTemplatePath
	logger.Debug("Updating controlplane template: %s", templateFile)

	// Read the template file
	content, err := os.ReadFile(templateFile)
	if err != nil {
		return fmt.Errorf("failed to read controlplane template %s: %w", templateFile, err)
	}

	// Build the new factory image URL with the schematic ID
	newFactoryImage := fmt.Sprintf("factory.talos.dev/installer/%s:%s", schematicID, talosVersion)

	// Replace the existing factory image URL with the new schematic-based URL
	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	for i, line := range lines {
		if strings.Contains(line, "image: factory.talos.dev/installer/") {
			// Extract the indentation to maintain YAML formatting
			var indent strings.Builder
			for _, char := range line {
				if char == ' ' {
					indent.WriteRune(char)
				} else {
					break
				}
			}
			lines[i] = indent.String() + "image: " + newFactoryImage
			logger.Debug("Updated factory image to: %s", newFactoryImage)
			break
		}
	}

	updatedContent := strings.Join(lines, "\n")

	// Write the updated content back
	if err := os.WriteFile(templateFile, []byte(updatedContent), 0644); err != nil {
		return fmt.Errorf("failed to write controlplane template %s: %w", templateFile, err)
	}

	logger.Debug("Updated controlplane template %s with schematic ID %s", templateFile, schematicID)
	return nil
}

// deployVMOnVSphere deploys one or more VMs on vSphere/ESXi
func deployVMOnVSphere(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()
	logger.Info("Starting vSphere/ESXi VM deployment with enhanced configuration")

	host, err := vmlifecycle.GetVSphereHostFn()
	if err != nil {
		return err
	}

	// Check if this is a k8s node deployment - use SSH-based method for exact config match.
	// Batch deployments commonly use the shared base name "k8s", which expands to k8s-0, k8s-1, ...
	isK8sNode := strings.HasPrefix(baseName, "k8s")
	if isK8sNode {
		return deployK8sVMViaSSH(baseName, host, memory, vcpus, diskSize, openebsSize, network, generateISO, nodeCount, startIndex)
	}

	// For non-k8s VMs, use the standard govmomi approach
	return deployGenericVMOnVSphere(baseName, host, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, startIndex)
}

// deployK8sVMViaSSH deploys k8s VMs using SSH for exact configuration control
// This ensures the VMs match the existing manually-deployed production VMs exactly
func deployK8sVMViaSSH(baseName string, host string, memory, vcpus, diskSize, openebsSize int, network string, _generateISO bool, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()
	logger.Info("Deploying k8s VM(s) via SSH with production configuration")

	// Create ESXi SSH client (fetches SSH key from 1Password)
	esxiClient, err := newESXiK8sVMDeployerFn(host, "root")
	if err != nil {
		return fmt.Errorf("failed to create ESXi SSH client: %w", err)
	}
	defer esxiClient.Close() // Clean up SSH key file

	plan, err := buildK8sVSphereDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, network, nodeCount, nodeCount, startIndex)
	if err != nil {
		return err
	}

	// Deploy each VM
	for idx, config := range plan.Configs {
		nodeConfig := plan.NodeConfigs[idx]

		logger.Info("Deploying %s with production configuration:", config.Name)
		logger.Info("  Boot Datastore: %s", nodeConfig.BootDatastore)
		logger.Info("  OpenEBS Datastore: truenas-iscsi")
		logger.Info("  RDM (Ceph): %s", nodeConfig.RDMPath)
		logger.Info("  SR-IOV PCI: %s", nodeConfig.PCIDevice)
		logger.Info("  MAC Address: %s", nodeConfig.MacAddress)
		logger.Info("  CPU Affinity: %s", nodeConfig.CPUAffinity)
		logger.Info("  Memory: %d MB (%d GB) - pinned reservation", config.Memory, config.Memory/1024)
		logger.Info("  vCPUs: %d", config.VCPUs)
		logger.Info("  Boot Disk: %d GB", config.DiskSize)
		logger.Info("  OpenEBS Disk: %d GB", config.OpenEBSSize)

		// Create the VM
		if err := esxiClient.CreateK8sVM(config); err != nil {
			return fmt.Errorf("failed to create VM %s: %w", config.Name, err)
		}

		logger.Success("VM %s deployed successfully!", config.Name)
	}

	return nil
}

// deployGenericVMOnVSphere deploys non-k8s VMs using govmomi (legacy behavior)
func deployGenericVMOnVSphere(baseName string, host string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()

	_, username, password, err := vmlifecycle.GetVSphereCredsFn()
	if err != nil {
		return err
	}

	client, err := newVSphereDeployerFn(host, username, password)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

	// Handle ISO generation if requested
	var isoPath string
	if generateISO {
		logger.Info("Generating custom Talos ISO...")
		logger.Warn("For vSphere, please ensure the ISO is already uploaded to the datastore")
		logger.Warn("Run 'homeops-cli talos prepare-iso' first if needed")
		isoPath = vsphere.DefaultISOPath()
	} else {
		isoPath = vsphere.DefaultISOPath()
	}

	plan, err := buildGenericVSphereDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, isoPath, concurrent, nodeCount, startIndex)
	if err != nil {
		return err
	}

	if len(plan.Configs) == 1 {
		logVSphereGenericSingleVMConfig(logger, plan.Configs[0])
	} else {
		logVSphereGenericParallelPlan(logger, plan, memory, vcpus, diskSize, openebsSize, datastore, network)
	}

	if err := executeVSphereGenericDeploymentPlan(logger, client, plan); err != nil {
		return err
	}

	if len(plan.Configs) == 1 {
		logger.Success("VM %s deployed successfully with enhanced configuration!", plan.Configs[0].Name)
	} else {
		logger.Success("Successfully deployed %d VMs with enhanced configuration!", len(plan.Configs))
	}

	return nil
}

// deleteVMOnVSphere deletes a VM from vSphere/ESXi
