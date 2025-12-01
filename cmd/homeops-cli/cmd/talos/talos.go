package talos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/iso"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/talos"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vsphere"
	localyaml "homeops-cli/internal/yaml"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "talos",
		Short: "Manage Talos Linux nodes and clusters",
		Long:  `Commands for managing Talos Linux nodes, including configuration, upgrades, and VM deployments`,
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
		newManageVMCommand(),
	)

	return cmd
}

// getEnvOrDefault returns the value of an environment variable or a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getTrueNASCredentials retrieves TrueNAS credentials from 1Password or environment variables
func getTrueNASCredentials() (host, apiKey string, err error) {
	logger := common.NewColorLogger()
	usedEnvFallback := false

	// Try 1Password first - batch lookup for better performance
	secrets := common.Get1PasswordSecretsBatch([]string{
		constants.OpTrueNASHost,
		constants.OpTrueNASAPI,
	})
	host = secrets[constants.OpTrueNASHost]
	apiKey = secrets[constants.OpTrueNASAPI]

	// Fall back to environment variables if 1Password fails
	if host == "" {
		host = os.Getenv(constants.EnvTrueNASHost)
		if host != "" {
			usedEnvFallback = true
		}
	}
	if apiKey == "" {
		apiKey = os.Getenv(constants.EnvTrueNASAPIKey)
		if apiKey != "" {
			usedEnvFallback = true
		}
	}

	// Check if we have both credentials
	if host == "" || apiKey == "" {
		return "", "", fmt.Errorf("TrueNAS credentials not found. Please set %s and %s environment variables or configure 1Password with '%s' and '%s'",
			constants.EnvTrueNASHost, constants.EnvTrueNASAPIKey, constants.OpTrueNASHost, constants.OpTrueNASAPI)
	}

	// Warn if using environment variables (less secure than 1Password)
	if usedEnvFallback {
		logger.Warn("Using environment variables for TrueNAS credentials. Consider using 1Password for better security.")
	}

	return host, apiKey, nil
}

// get1PasswordSecret retrieves a secret from 1Password using the shared common function
func get1PasswordSecret(reference string) string {
	return common.Get1PasswordSecretSilent(reference)
}

// getSpicePassword retrieves SPICE password from 1Password or environment variables
func getSpicePassword() string {
	// Try 1Password first
	password := get1PasswordSecret(constants.OpTrueNASSPICEPass)
	if password != "" {
		return password
	}
	// Fall back to environment variable
	return os.Getenv(constants.EnvSPICEPassword)
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
		nodeIPs, err := talos.GetNodeIPs()
		if err != nil {
			return err
		}

		// Use interactive selector
		selectedNode, err := ui.Choose("Select a Talos node:", nodeIPs)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("node selection failed: %w", err)
		}
		nodeIP = selectedNode
	}

	// Get machine type
	machineType, err := getMachineTypeFromNode(nodeIP)
	if err != nil {
		return fmt.Errorf("failed to get machine type: %w", err)
	}

	logger.Info("Applying configuration to node %s (type: %s)", nodeIP, machineType)

	// Render machine config using embedded templates
	machineConfigTemplate := fmt.Sprintf("talos/%s.yaml", machineType)
	nodeConfigTemplate := fmt.Sprintf("talos/nodes/%s.yaml", nodeIP)

	// Render the configuration
	renderedConfig, err := renderMachineConfigFromEmbedded(machineConfigTemplate, nodeConfigTemplate)
	if err != nil {
		return fmt.Errorf("failed to render config: %w", err)
	}

	// Resolve 1Password references in the rendered config with signin-once retry
	logger.Info("Resolving 1Password references in Talos configuration...")
	resolvedConfig, err := common.InjectSecrets(string(renderedConfig))
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not authenticated") || strings.Contains(errStr, "not signed in") || strings.Contains(errStr, "please run 'op signin'") {
			logger.Info("Attempting 1Password CLI signin due to authentication error...")
			if err2 := common.Ensure1PasswordAuth(); err2 != nil {
				return fmt.Errorf("1Password signin failed: %w (original: %v)", err2, err)
			}
			// Retry once after successful signin
			if retryResolved, retryErr := common.InjectSecrets(string(renderedConfig)); retryErr == nil {
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
		var any interface{}
		if err := yaml.Unmarshal([]byte(resolvedConfig), &any); err != nil {
			return fmt.Errorf("rendered config failed YAML validation: %w", err)
		}
		logger.Info("[DRY RUN] Would apply config to %s (type: %s)", nodeIP, machineType)
		return nil
	}

	// Apply the configuration
	cmd := exec.Command("talosctl", "--nodes", nodeIP, "apply-config", "--mode", mode, "--file", "/dev/stdin")
	cmd.Stdin = bytes.NewReader([]byte(resolvedConfig))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply config: %w\n%s", err, output)
	}

	logger.Success("Configuration applied successfully to %s", nodeIP)
	return nil
}

// getESXiVMNames retrieves the list of VM names from ESXi/vSphere
func getESXiVMNames() ([]string, error) {
	return vsphere.GetVMNames()
}

// getTrueNASVMNames retrieves the list of VM names from TrueNAS
func getTrueNASVMNames() ([]string, error) {
	// Get TrueNAS connection details
	host, apiKey, err := truenas.GetCredentials()
	if err != nil {
		return nil, err
	}

	// Create TrueNAS client and connect
	client := truenas.NewWorkingClient(host, apiKey, 443, true)
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Query VMs
	vms, err := client.QueryVMs(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query VMs: %w", err)
	}

	// Extract VM names
	vmNames := make([]string, 0, len(vms))
	for _, vm := range vms {
		if vm.Name != "" {
			vmNames = append(vmNames, vm.Name)
		}
	}

	if len(vmNames) == 0 {
		return nil, fmt.Errorf("no VMs found on TrueNAS")
	}

	return vmNames, nil
}

func getMachineTypeFromNode(nodeIP string) (string, error) {
	cmd := exec.Command("talosctl", "--nodes", nodeIP, "get", "machinetypes", "--output=jsonpath={.spec}")
	output, err := cmd.Output()
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
		nodeIPs, err := talos.GetNodeIPs()
		if err != nil {
			return err
		}

		// Use interactive selector
		selectedNode, err := ui.Choose("Select a Talos node to upgrade:", nodeIPs)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("node selection failed: %w", err)
		}
		nodeIP = selectedNode
	}

	// Get factory image from controlplane config instead of individual node configs
	controlplaneTemplate := "talos/controlplane.yaml"
	configOutput, err := templates.GetTalosTemplate(controlplaneTemplate)
	if err != nil {
		return fmt.Errorf("failed to get controlplane config: %w", err)
	}

	// Extract factory image using Go YAML processor
	metrics := metrics.NewPerformanceCollector()
	processor := localyaml.NewProcessor(nil, metrics)

	// Parse YAML content into a map
	configData, err := processor.ParseString(string(configOutput))
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
	err = ui.Spin(fmt.Sprintf("Upgrading node %s", nodeIP),
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
		Use:   "upgrade-k8s",
		Short: "Upgrade Kubernetes across the whole cluster",
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
	k8sVersion := getEnvOrDefault("KUBERNETES_VERSION", versionConfig.KubernetesVersion)
	if k8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}

	logger.Info("Upgrading Kubernetes to version %s via node %s", k8sVersion, node)

	// Perform Kubernetes upgrade with spinner
	err = ui.Spin(fmt.Sprintf("Upgrading Kubernetes to %s", k8sVersion),
		"talosctl", "--nodes", node, "upgrade-k8s", "--to", k8sVersion)

	if err != nil {
		return fmt.Errorf("kubernetes upgrade failed: %w", err)
	}

	logger.Success("Kubernetes upgraded successfully to %s", k8sVersion)
	return nil
}

func getRandomNode() (string, error) {
	cmd := exec.Command("talosctl", "config", "info", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	var configInfo struct {
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal(output, &configInfo); err != nil {
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
		nodeIPs, err := talos.GetNodeIPs()
		if err != nil {
			return err
		}

		// Use interactive selector
		selectedNode, err := ui.Choose("Select a Talos node to reboot:", nodeIPs)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("node selection failed: %w", err)
		}
		nodeIP = selectedNode
	}

	// Add confirmation for reboot
	confirmed, err := ui.Confirm(fmt.Sprintf("Are you sure you want to reboot node %s?", nodeIP), false)
	if err != nil {
		return fmt.Errorf("confirmation failed: %w", err)
	}
	if !confirmed {
		logger.Info("Reboot cancelled")
		return nil
	}
	logger.Info("Rebooting node %s with mode %s", nodeIP, mode)

	cmd := exec.Command("talosctl", "--nodes", nodeIP, "reboot", "--mode", mode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reboot failed: %w\n%s", err, output)
	}

	logger.Success("Node %s reboot initiated", nodeIP)
	return nil
}

func newShutdownClusterCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "shutdown-cluster",
		Short: "Shutdown Talos across the whole cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := ui.Confirm("Shutdown the Talos cluster?", false)
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

	cmd := exec.Command("talosctl", "shutdown", "--nodes", strings.Join(nodes, ","), "--force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shutdown failed: %w\n%s", err, output)
	}

	logger.Success("Cluster shutdown initiated")
	return nil
}

func getAllNodes() ([]string, error) {
	cmd := exec.Command("talosctl", "config", "info", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var configInfo struct {
		Nodes []string `json:"nodes"`
	}
	if err := json.Unmarshal(output, &configInfo); err != nil {
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
		nodeIPs, err := talos.GetNodeIPs()
		if err != nil {
			return err
		}

		// Use interactive selector
		selectedNode, err := ui.Choose("Select a Talos node to reset:", nodeIPs)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("node selection failed: %w", err)
		}
		nodeIP = selectedNode
	}

	// Add confirmation for reset
	if !force {
		confirmed, err := ui.Confirm(fmt.Sprintf("Reset Talos node '%s'? This is destructive!", nodeIP), false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			logger.Info("Reset cancelled")
			return fmt.Errorf("reset cancelled")
		}
	}

	logger.Info("Resetting node %s", nodeIP)

	cmd := exec.Command("talosctl", "reset", "--nodes", nodeIP, "--graceful=false")
	output, err := cmd.CombinedOutput()
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
				confirmed, err := ui.Confirm("Reset the Talos cluster? This is destructive!", false)
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

	cmd := exec.Command("talosctl", "reset", "--nodes", strings.Join(nodes, ","), "--graceful=false")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reset failed: %w\n%s", err, output)
	}

	logger.Success("Cluster reset initiated")
	return nil
}

func newKubeconfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate the kubeconfig for a Talos cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return generateKubeconfig()
		},
	}

	return cmd
}

func generateKubeconfig() error {
	logger := common.NewColorLogger()

	// Get a random node
	node, err := getRandomNode()
	if err != nil {
		return err
	}

	rootDir := common.GetWorkingDirectory()
	logger.Info("Generating kubeconfig from node %s", node)

	cmd := exec.Command("talosctl", "kubeconfig", "--nodes", node,
		"--force", "--force-context-name", "home-ops-cluster", rootDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to generate kubeconfig: %w\n%s", err, output)
	}

	logger.Success("Kubeconfig generated successfully")
	return nil
}

func promptDeployVMOptions(name, provider *string, memory, vcpus, diskSize, openebsSize *int, generateISO, dryRun *bool, datastore, network *string, nodeCount, concurrent *int) error {
	logger := common.NewColorLogger()

	// Step 1: Select deployment pattern
	patternOptions := []string{
		"Default - 3-node k8s cluster (16 vCPUs, 48GB RAM, 500GB boot, 1TB OpenEBS each)",
		"Custom - Choose your own configuration",
	}

	selectedPattern, err := ui.Choose("Select deployment pattern:", patternOptions)
	if err != nil {
		return err
	}

	isCustom := strings.HasPrefix(selectedPattern, "Custom")

	// Step 2: Select provider
	providerOptions := []string{
		"vSphere/ESXi - Deploy to vSphere or ESXi (default)",
		"TrueNAS - Deploy to TrueNAS Scale",
	}

	selectedProvider, err := ui.Choose("Select virtualization provider:", providerOptions)
	if err != nil {
		return err
	}

	if strings.HasPrefix(selectedProvider, "TrueNAS") {
		*provider = "truenas"
		logger.Info("Selected provider: TrueNAS")
	} else {
		*provider = "vsphere"
		logger.Info("Selected provider: vSphere/ESXi")
	}

	// Step 3: Get VM name
	vmName, err := ui.Input("Enter VM name (base name for multi-node):", "k8s")
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

		// Node count (vSphere only)
		if *provider == "vsphere" {
			nodeCountInput, err := ui.Input("Enter number of VMs to deploy:", "3")
			if err != nil {
				return err
			}
			if nodeCountInput != "" {
				_, _ = fmt.Sscanf(nodeCountInput, "%d", nodeCount)
			} else {
				*nodeCount = 3 // Default
			}

			// Concurrent deployments
			concurrentInput, err := ui.Input("Enter number of concurrent deployments:", "3")
			if err != nil {
				return err
			}
			if concurrentInput != "" {
				_, _ = fmt.Sscanf(concurrentInput, "%d", concurrent)
			} else {
				*concurrent = 3 // Default
			}
		}

		// vCPUs
		vcpuInput, err := ui.Input("Enter number of vCPUs:", "16")
		if err != nil {
			return err
		}
		if vcpuInput != "" {
			_, _ = fmt.Sscanf(vcpuInput, "%d", vcpus)
		} else {
			*vcpus = 16 // Default
		}

		// Memory
		memoryInput, err := ui.Input("Enter memory in GB:", "48")
		if err != nil {
			return err
		}
		if memoryInput != "" {
			var memoryGB int
			_, _ = fmt.Sscanf(memoryInput, "%d", &memoryGB)
			*memory = memoryGB * 1024 // Convert to MB
		} else {
			*memory = 49152 // Default (48GB)
		}

		// Boot disk
		bootDiskInput, err := ui.Input("Enter boot/OpenEBS disk size in GB:", "500")
		if err != nil {
			return err
		}
		if bootDiskInput != "" {
			_, _ = fmt.Sscanf(bootDiskInput, "%d", diskSize)
		} else {
			*diskSize = 500 // Default
		}

		// OpenEBS disk
		openebsInput, err := ui.Input("Enter OpenEBS disk size in GB:", "1024")
		if err != nil {
			return err
		}
		if openebsInput != "" {
			_, _ = fmt.Sscanf(openebsInput, "%d", openebsSize)
		} else {
			*openebsSize = 1024 // Default
		}

		if *provider == "vsphere" {
			logger.Info("Custom resources: %d VMs with %d vCPUs, %dGB RAM, %dGB boot, %dGB OpenEBS each",
				*nodeCount, *vcpus, *memory/1024, *diskSize, *openebsSize)
		} else {
			logger.Info("Custom resources: %d vCPUs, %dGB RAM, %dGB boot, %dGB OpenEBS",
				*vcpus, *memory/1024, *diskSize, *openebsSize)
		}
	} else {
		// Default pattern - 3-node k8s cluster
		*vcpus = 16
		*memory = 49152     // 48GB in MB
		*diskSize = 500     // 500GB
		*openebsSize = 1024 // 1TB

		if *provider == "vsphere" {
			*nodeCount = 3
			*concurrent = 3
			logger.Info("Default resources: 3 VMs with 16 vCPUs, 48GB RAM, 500GB boot, 1TB OpenEBS each")
		} else {
			logger.Info("Default resources: 16 vCPUs, 48GB RAM, 500GB boot, 1TB OpenEBS")
		}
	}

	// Step 5: vSphere-specific configuration (if applicable)
	if *provider == "vsphere" {
		datastoreInput, err := ui.Input("Enter datastore name:", "truenas-nfs")
		if err != nil {
			return err
		}
		if datastoreInput != "" {
			*datastore = datastoreInput
		} else {
			*datastore = "truenas-nfs"
		}

		networkInput, err := ui.Input("Enter network port group:", "vl999")
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

	selectedGenerate, err := ui.Choose("Generate custom Talos ISO?", generateOptions)
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

	selectedDryRun, err := ui.Choose("Select deployment mode:", dryRunOptions)
	if err != nil {
		return err
	}

	*dryRun = strings.HasPrefix(selectedDryRun, "Dry-Run")
	if *dryRun {
		logger.Info("🔍 Dry-run mode enabled - no changes will be made")
	}

	return nil
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
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Talos VM on TrueNAS or vSphere/ESXi",
		Long: `Deploy a new Talos VM on TrueNAS or vSphere/ESXi.

Defaults to vSphere/ESXi deployment. Use --provider truenas for TrueNAS deployment.

For TrueNAS: Uses proper ZVol naming convention and SPICE console.
For vSphere/ESXi: Deploys to specified datastore with enhanced VM configuration.

Use --generate-iso to create a custom ISO using the schematic.yaml configuration.

If no flags are provided, presents an interactive menu with default and custom patterns.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()

			// Check if running in interactive mode (no flags set)
			if name == "" && !cmd.Flags().Changed("provider") && !cmd.Flags().Changed("dry-run") {
				// Show interactive prompts
				err := promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return err
				}
			}

			// Validate required name
			if name == "" {
				return fmt.Errorf("VM name is required (use --name flag or interactive mode)")
			}

			// Show dry-run mode indicator
			if dryRun {
				logger.Info("🔍 DRY-RUN MODE - No changes will be made")
			}

			// Deploy to vSphere unless explicitly set to truenas
			if provider == "truenas" {
				return deployVMWithPatternDryRun(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO, dryRun)
			}
			return deployVMOnVSphereDryRun(name, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, dryRun)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required for single VM, base name for multiple VMs)")
	cmd.Flags().StringVar(&pool, "pool", "flashstor/VM", "Storage pool (TrueNAS only)")
	cmd.Flags().IntVar(&memory, "memory", 48*1024, "Memory in MB (default: 48GB)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 16, "Number of vCPUs (default: 16)")
	cmd.Flags().IntVar(&diskSize, "disk-size", 500, "Boot disk size in GB (default: 500GB)")
	cmd.Flags().IntVar(&openebsSize, "openebs-size", 1000, "OpenEBS disk size in GB (default: 1TB)")
	cmd.Flags().StringVar(&macAddress, "mac-address", "", "MAC address (optional)")
	cmd.Flags().BoolVar(&skipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation (TrueNAS only)")
	cmd.Flags().BoolVar(&generateISO, "generate-iso", false, "Generate custom ISO using schematic.yaml")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Perform a dry run without creating the VM")

	// vSphere specific flags
	cmd.Flags().StringVar(&datastore, "datastore", "truenas-nfs", "Datastore name (vSphere: truenas-nfs, datastore1, etc.)")
	cmd.Flags().StringVar(&network, "network", "vl999", "Network port group name (vSphere only)")
	cmd.Flags().IntVar(&concurrent, "concurrent", 3, "Number of concurrent VM deployments (vSphere only)")
	cmd.Flags().IntVar(&nodeCount, "node-count", 1, "Number of VMs to deploy (vSphere only)")

	return cmd
}

// validateVMName checks if the VM name contains invalid characters
func validateVMName(name string) error {
	if strings.Contains(name, "-") {
		return fmt.Errorf("VM name '%s' cannot contain dashes (-). Use underscores (_) or alphanumeric characters only", name)
	}
	if name == "" {
		return fmt.Errorf("VM name cannot be empty")
	}
	return nil
}

func deployVMWithPatternDryRun(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate, generateISO, dryRun bool) error {
	if dryRun {
		logger := common.NewColorLogger()
		logger.Info("[DRY RUN] Would deploy VM with the following configuration:")
		logger.Info("  Provider: TrueNAS")
		logger.Info("  VM Name: %s", name)
		logger.Info("  Pool: %s", pool)
		logger.Info("  Memory: %d MB (%d GB)", memory, memory/1024)
		logger.Info("  vCPUs: %d", vcpus)
		logger.Info("  Boot Disk: %d GB", diskSize)
		logger.Info("  OpenEBS Disk: %d GB", openebsSize)
		if macAddress != "" {
			logger.Info("  MAC Address: %s", macAddress)
		}
		if generateISO {
			logger.Info("  Generate Custom ISO: Yes")
		}
		if skipZVolCreate {
			logger.Info("  Skip ZVol Creation: Yes")
		}
		logger.Success("[DRY RUN] VM deployment preview complete - no changes made")
		return nil
	}
	return deployVMWithPattern(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO)
}

func deployVMOnVSphereDryRun(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount int, dryRun bool) error {
	if dryRun {
		logger := common.NewColorLogger()
		logger.Info("[DRY RUN] Would deploy VM with the following configuration:")
		logger.Info("  Provider: vSphere/ESXi")
		logger.Info("  VM Name: %s", baseName)
		logger.Info("  Datastore: %s", datastore)
		logger.Info("  Network: %s", network)
		logger.Info("  Memory: %d MB (%d GB)", memory, memory/1024)
		logger.Info("  vCPUs: %d", vcpus)
		logger.Info("  Boot Disk: %d GB", diskSize)
		logger.Info("  OpenEBS Disk: %d GB", openebsSize)
		if macAddress != "" {
			logger.Info("  MAC Address: %s", macAddress)
		}
		if nodeCount > 1 {
			logger.Info("  Node Count: %d", nodeCount)
			logger.Info("  Concurrent Deployments: %d", concurrent)
		}
		if generateISO {
			logger.Info("  Generate Custom ISO: Yes")
		}
		logger.Success("[DRY RUN] VM deployment preview complete - no changes made")
		return nil
	}
	return deployVMOnVSphere(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount)
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
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("VM name validation failed: %w", err)
	}

	// Get TrueNAS connection details
	logger.Debug("Retrieving TrueNAS credentials")
	host, apiKey, err := getTrueNASCredentials()
	if err != nil {
		return fmt.Errorf("failed to get TrueNAS credentials: %w", err)
	}
	logger.Debug("TrueNAS host: %s", host)

	// FIRST STEP: ISO generation and download to TrueNAS (if requested)
	var isoURL, schematicID, talosVersion string
	var customISO bool

	logger.Debug("Determining ISO configuration (generateISO=%t)", generateISO)
	if generateISO {
		logger.Info("STEP 1: Generating custom Talos ISO using schematic.yaml...")

		// Create factory client
		logger.Debug("Creating Talos factory client")
		factoryClient := talos.NewFactoryClient()
		if factoryClient == nil {
			return fmt.Errorf("failed to create factory client")
		}

		// Load schematic from embedded templates
		logger.Debug("Loading schematic from embedded template")
		schematic, err := factoryClient.LoadSchematicFromTemplate()
		if err != nil {
			return fmt.Errorf("failed to load schematic template: %w", err)
		}
		logger.Debug("Schematic loaded successfully")

		// Generate ISO with default parameters
		versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
		logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=metal", versionConfig.TalosVersion)
		isoInfo, err := factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "metal")
		if err != nil {
			return fmt.Errorf("ISO generation failed: %w", err)
		}

		if isoInfo == nil {
			return fmt.Errorf("ISO generation returned nil result")
		}

		isoURL = isoInfo.URL
		schematicID = isoInfo.SchematicID
		talosVersion = isoInfo.TalosVersion
		customISO = true

		// Set schematic ID as environment variable for template rendering
		if err := os.Setenv("SCHEMATIC_ID", schematicID); err != nil {
			logger.Warn("Failed to set SCHEMATIC_ID environment variable: %v", err)
		} else {
			logger.Debug("Set SCHEMATIC_ID environment variable: %s", schematicID)
		}

		logger.Success("Custom ISO generated successfully")
		logger.Debug("ISO Details: URL=%s, SchematicID=%s, Version=%s", isoURL, schematicID, talosVersion)

		// CRITICAL: Download custom ISO to TrueNAS BEFORE any VM operations
		logger.Info("STEP 2: Downloading custom ISO to TrueNAS (REQUIRED BEFORE VM CREATION)...")
		downloader := iso.NewDownloader()

		// Create download configuration
		downloadConfig := iso.GetDefaultConfig()
		downloadConfig.TrueNASHost = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_HOST")
		downloadConfig.TrueNASUsername = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_USERNAME")
		downloadConfig.ISOURL = isoURL
		downloadConfig.ISOFilename = fmt.Sprintf("metal-amd64-%s.iso", schematicID[:8]) // Use schematic ID prefix for unique filename

		if err := downloader.DownloadCustomISO(downloadConfig); err != nil {
			return fmt.Errorf("CRITICAL: Failed to download custom ISO to TrueNAS - VM deployment cannot proceed: %w", err)
		}

		logger.Success("Custom ISO downloaded to TrueNAS successfully")
		// Update ISO URL to point to local TrueNAS path
		isoURL = filepath.Join(downloadConfig.ISOStoragePath, downloadConfig.ISOFilename)
		logger.Debug("Updated ISO path: %s", isoURL)
		logger.Info("ISO preparation completed - proceeding with VM deployment...")
	} else {
		// Check if a prepared ISO exists at the standard location
		standardISOPath := "/mnt/flashstor/ISO/vmware-amd64.iso"
		logger.Debug("Checking for prepared ISO at: %s", standardISOPath)

		// Connect to TrueNAS to check if the prepared ISO exists
		host, _, err := getTrueNASCredentials()
		if err != nil {
			return fmt.Errorf("failed to get TrueNAS credentials to check for prepared ISO: %w", err)
		}

		// Create SSH client to check if ISO exists
		sshConfig := ssh.SSHConfig{
			Host:       host,
			Username:   get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_USERNAME"),
			Port:       "22",
			SSHItemRef: "op://Infrastructure/NAS01/private key",
		}
		sshClient := ssh.NewSSHClient(sshConfig)

		if err := sshClient.Connect(); err != nil {
			logger.Warn("Cannot verify prepared ISO due to SSH connection failure: %v", err)
			return fmt.Errorf("schema-based ISO generation is required for VM deployment. Please use the --generate-iso flag to create a custom Talos ISO, or run 'homeops talos prepare-iso' first to prepare the ISO")
		}
		defer func() {
			if closeErr := sshClient.Close(); closeErr != nil {
				logger.Warn("Failed to close SSH client: %v", closeErr)
			}
		}()

		// Check if the standard ISO exists
		exists, size, err := sshClient.VerifyFile(standardISOPath)
		if err != nil {
			logger.Warn("Failed to verify prepared ISO: %v", err)
			return fmt.Errorf("schema-based ISO generation is required for VM deployment. Please use the --generate-iso flag to create a custom Talos ISO, or run 'homeops talos prepare-iso' first to prepare the ISO")
		}

		if !exists {
			logger.Info("No prepared ISO found at %s", standardISOPath)
			return fmt.Errorf("no prepared ISO found. Please run 'homeops talos prepare-iso' first to prepare the ISO, or use the --generate-iso flag to generate a new one")
		}

		// Prepared ISO exists, use it
		isoURL = standardISOPath
		customISO = true // Mark as custom since it's from prepare-iso
		logger.Success("Using prepared ISO: %s (size: %d bytes)", standardISOPath, size)
		logger.Info("Prepared ISO found - proceeding with VM deployment...")

		// Get version info for logging
		versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
		talosVersion = versionConfig.TalosVersion
		// Note: schematicID won't be available here, but that's okay for VM creation
	}

	// Get SPICE password
	logger.Debug("Retrieving SPICE password")
	spicePassword := getSpicePassword()
	if spicePassword == "" {
		return fmt.Errorf("SPICE password is required - use SPICE_PASSWORD env var or configure 1Password")
	}
	logger.Debug("SPICE password retrieved successfully")

	// Create VM manager
	logger.Debug("Creating VM manager for TrueNAS host: %s", host)
	vmManager := truenas.NewVMManager(host, apiKey, 443, true)
	if vmManager == nil {
		return fmt.Errorf("failed to create VM manager")
	}

	logger.Debug("Connecting to TrueNAS API")
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("TrueNAS connection failed: %w", err)
	}
	logger.Debug("Successfully connected to TrueNAS")

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
	networkBridge := getEnvOrDefault("NETWORK_BRIDGE", "br0")
	logger.Debug("Network bridge: %s", networkBridge)

	config := truenas.VMConfig{
		Name:          name,
		Memory:        memory,
		VCPUs:         vcpus,
		DiskSize:      diskSize,
		OpenEBSSize:   openebsSize,
		TrueNASHost:   host,
		TrueNASAPIKey: apiKey,
		TrueNASPort:   443,
		NoSSL:         false,
		TalosISO:      isoURL,
		NetworkBridge: networkBridge,
		StoragePool:   pool,
		MacAddress:    macAddress,
		// Let getZVolPaths handle path construction to avoid duplication
		// BootZVol, OpenEBSZVol, RookZVol will be auto-generated
		SkipZVolCreate: skipZVolCreate,
		SpicePassword:  spicePassword,
		UseSpice:       true, // Always use SPICE as per working scripts
		// Schematic configuration fields
		SchematicID:  schematicID,
		TalosVersion: talosVersion,
		CustomISO:    customISO,
	}

	logger.Debug("VM configuration built successfully")
	logger.Debug("Configuration summary: Name=%s, Memory=%dMB, vCPUs=%d, ISO=%s, Bridge=%s, Pool=%s",
		name, memory, vcpus, isoURL, networkBridge, pool)

	// STEP 3: Deploy the VM (ISO is now ready on TrueNAS)
	logger.Info("STEP 3: Starting VM deployment process...")

	// Deploy VM with spinner
	err = ui.SpinWithFunc(fmt.Sprintf("Deploying VM %s", name), func() error {
		logger.Debug("Calling vmManager.DeployVM with configuration")
		if err := vmManager.DeployVM(config); err != nil {
			return fmt.Errorf("VM deployment failed: %w", err)
		}
		return nil
	})

	if err != nil {
		logger.Error("VM deployment failed: %v", err)
		return err
	}

	logger.Success("VM %s deployed successfully!", name)
	logger.Info("VM deployment completed with the following configuration:")
	logger.Info("  VM Name:      %s", name)
	logger.Info("  Memory:       %d MB", memory)
	logger.Info("  vCPUs:        %d", vcpus)
	logger.Info("  Storage Pool: %s", pool)
	logger.Info("  Network:      %s", networkBridge)
	if macAddress != "" {
		logger.Info("  MAC Address:  %s", macAddress)
	}
	logger.Info("  ISO Source:   %s", isoURL)
	if customISO {
		logger.Info("  Schematic ID: %s", schematicID)
		logger.Info("  Talos Ver:    %s", talosVersion)
	}
	logger.Info("ZVol naming pattern:")
	logger.Info("  Boot disk:   %s/%s-boot (%dGB)", pool, name, diskSize)
	if openebsSize > 0 {
		logger.Info("  OpenEBS disk: %s/%s-openebs (%dGB)", pool, name, openebsSize)
	}

	logger.Debug("VM deployment function completed successfully")
	return nil
}

func newManageVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manage-vm",
		Short: "Manage VMs on TrueNAS or vSphere",
		Long:  `Commands for managing VMs on TrueNAS Scale or vSphere/ESXi`,
	}

	cmd.AddCommand(
		newListVMsCommand(),
		newStartVMCommand(),
		newStopVMCommand(),
		newPowerOnVMCommand(),
		newPowerOffVMCommand(),
		newDeleteVMCommand(),
		newInfoVMCommand(),
		newCleanupZVolsCommand(),
	)

	return cmd
}

func newListVMsCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all VMs on TrueNAS or vSphere",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listVMs(provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")

	return cmd
}

func listVMs(provider string) error {
	logger := common.NewColorLogger()

	if provider == "truenas" {
		// Get TrueNAS connection details
		host, apiKey, err := getTrueNASCredentials()
		if err != nil {
			return err
		}

		// Create VM manager
		vmManager := truenas.NewVMManager(host, apiKey, 443, true)
		if err := vmManager.Connect(); err != nil {
			return fmt.Errorf("failed to connect to TrueNAS: %w", err)
		}
		defer func() {
			if closeErr := vmManager.Close(); closeErr != nil {
				logger.Warn("Failed to close VM manager: %v", closeErr)
			}
		}()

		return vmManager.ListVMs()
	}

	// vSphere provider - get credentials
	host := get1PasswordSecret("op://Infrastructure/esxi/add more/host")
	if host == "" {
		host = os.Getenv("VSPHERE_HOST")
	}
	username := get1PasswordSecret("op://Infrastructure/esxi/username")
	if username == "" {
		username = os.Getenv("VSPHERE_USERNAME")
	}
	password := get1PasswordSecret("op://Infrastructure/esxi/password")
	if password == "" {
		password = os.Getenv("VSPHERE_PASSWORD")
	}

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("vSphere credentials not found")
	}

	// Create vSphere client
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() { _ = client.Close() }()

	vms, err := client.ListVMs()
	if err != nil {
		return fmt.Errorf("failed to list VMs: %w", err)
	}

	fmt.Println("\nVMs on vSphere:")
	fmt.Println("================")
	for _, vm := range vms {
		fmt.Printf("- %s\n", vm.Name())
	}
	fmt.Printf("\nTotal: %d VMs\n", len(vms))

	return nil
}

func newStartVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a VM on TrueNAS or vSphere/ESXi",
		Long:  `Start a VM on TrueNAS or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return startVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

// startVMWithProvider starts a VM on the specified provider with interactive selector
func startVMWithProvider(name, provider string) error {
	// If VM name is not provided, prompt for selection based on provider
	if name == "" {
		var vmNames []string
		var err error

		// Use appropriate VM listing based on provider
		if provider == "truenas" {
			vmNames, err = getTrueNASVMNames()
		} else {
			// Default to ESXi/vSphere for vsphere, esxi, or any other value
			vmNames, err = getESXiVMNames()
		}

		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to start:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Call appropriate start function based on provider
	if provider == "truenas" {
		return startVM(name)
	}
	return powerOnVMOnVSphere(name)
}

// stopVMWithProvider stops a VM on the specified provider with interactive selector
func stopVMWithProvider(name, provider string) error {
	// If VM name is not provided, prompt for selection based on provider
	if name == "" {
		var vmNames []string
		var err error

		// Use appropriate VM listing based on provider
		if provider == "truenas" {
			vmNames, err = getTrueNASVMNames()
		} else {
			// Default to ESXi/vSphere for vsphere, esxi, or any other value
			vmNames, err = getESXiVMNames()
		}

		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to stop:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Call appropriate stop function based on provider
	if provider == "truenas" {
		return stopVM(name)
	}
	return powerOffVMOnVSphere(name)
}

// infoVMWithProvider gets VM info from the specified provider with interactive selector
func infoVMWithProvider(name, provider string) error {
	// If VM name is not provided, prompt for selection based on provider
	if name == "" {
		var vmNames []string
		var err error

		// Use appropriate VM listing based on provider
		if provider == "truenas" {
			vmNames, err = getTrueNASVMNames()
		} else {
			// Default to ESXi/vSphere for vsphere, esxi, or any other value
			vmNames, err = getESXiVMNames()
		}

		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to get info:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Call appropriate info function based on provider
	if provider == "truenas" {
		return infoVM(name)
	}
	return infoVMOnVSphere(name)
}

// infoVMOnVSphere gets detailed VM information from vSphere/ESXi
func infoVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Getting vSphere/ESXi VM info: %s", vmName)

	// Get vSphere credentials from 1Password or environment
	host := get1PasswordSecret("op://Infrastructure/esxi/add more/host")
	if host == "" {
		host = os.Getenv("VSPHERE_HOST")
	}
	username := get1PasswordSecret("op://Infrastructure/esxi/username")
	if username == "" {
		username = os.Getenv("VSPHERE_USERNAME")
	}
	password := get1PasswordSecret("op://Infrastructure/esxi/password")
	if password == "" {
		password = os.Getenv("VSPHERE_PASSWORD")
	}

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("vSphere credentials not found. Please set VSPHERE_HOST, VSPHERE_USERNAME, and VSPHERE_PASSWORD environment variables or configure 1Password")
	}

	// Create vSphere client
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

	// Find the VM
	vm, err := client.FindVM(vmName)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", vmName, err)
	}

	// Get VM info
	vmInfo, err := client.GetVMInfo(vm)
	if err != nil {
		return fmt.Errorf("failed to get VM info for %s: %w", vmName, err)
	}

	// Display VM information
	logger.Info("VM Information for: %s", vmName)
	logger.Info("  Power State: %s", vmInfo.Runtime.PowerState)
	logger.Info("  Guest OS: %s", vmInfo.Config.GuestFullName)
	logger.Info("  CPUs: %d", vmInfo.Config.Hardware.NumCPU)
	logger.Info("  Memory: %d MB", vmInfo.Config.Hardware.MemoryMB)
	logger.Info("  UUID: %s", vmInfo.Config.Uuid)

	if vmInfo.Guest != nil && vmInfo.Guest.IpAddress != "" {
		logger.Info("  IP Address: %s", vmInfo.Guest.IpAddress)
	}

	return nil
}

func startVM(name string) error {
	logger := common.NewColorLogger()

	// If VM name is not provided, prompt for selection
	if name == "" {
		vmNames, err := getTrueNASVMNames()
		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to start:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Get TrueNAS connection details
	host, apiKey, err := getTrueNASCredentials()
	if err != nil {
		return err
	}

	// Create VM manager
	vmManager := truenas.NewVMManager(host, apiKey, 443, true)
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	return vmManager.StartVM(name)
}

func newStopVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a VM on TrueNAS or vSphere/ESXi",
		Long:  `Stop a VM on TrueNAS or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func stopVM(name string) error {
	logger := common.NewColorLogger()

	// If VM name is not provided, prompt for selection
	if name == "" {
		vmNames, err := getTrueNASVMNames()
		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to stop:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Get TrueNAS connection details
	host, apiKey, err := getTrueNASCredentials()
	if err != nil {
		return err
	}

	// Create VM manager
	vmManager := truenas.NewVMManager(host, apiKey, 443, true)
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	return vmManager.StopVM(name, false)
}

func newDeleteVMCommand() *cobra.Command {
	var (
		name     string
		force    bool
		provider string
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a VM on TrueNAS or vSphere/ESXi",
		Long:  `Delete a VM on TrueNAS (with ZVols) or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return deleteVMWithConfirmation(name, provider, force)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func deleteVMWithConfirmation(name, provider string, force bool) error {
	// If VM name is not provided, prompt for selection based on provider
	if name == "" {
		var vmNames []string
		var err error

		// Use appropriate VM listing based on provider
		if provider == "truenas" {
			vmNames, err = getTrueNASVMNames()
		} else {
			// Default to ESXi/vSphere for vsphere, esxi, or empty provider
			vmNames, err = getESXiVMNames()
		}

		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to delete:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Add confirmation for deletion
	if !force {
		var message string
		if provider == "vsphere" || provider == "esxi" {
			message = fmt.Sprintf("Delete VM '%s' on vSphere/ESXi? This is destructive!", name)
		} else {
			message = fmt.Sprintf("Delete VM '%s' and all its ZVols on TrueNAS? This is destructive!", name)
		}

		confirmed, err := ui.Confirm(message, false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			return fmt.Errorf("deletion cancelled")
		}
	}

	if provider == "truenas" {
		return deleteVM(name)
	}
	return deleteVMOnVSphere(name)
}

func deleteVM(name string) error {
	logger := common.NewColorLogger()

	// Get TrueNAS connection details
	host, apiKey, err := getTrueNASCredentials()
	if err != nil {
		return err
	}

	// Create VM manager
	vmManager := truenas.NewVMManager(host, apiKey, 443, true)
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	// Delete VM and ZVols
	// Use the most common storage pool path where VMs are deployed
	// The VM manager will try multiple patterns to find the actual ZVols
	storagePool := getEnvOrDefault("STORAGE_POOL", "flashstor")
	return vmManager.DeleteVM(name, true, storagePool)
}

func newInfoVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Get detailed information about a VM on TrueNAS or vSphere/ESXi",
		Long:  `Get detailed information about a VM on TrueNAS or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return infoVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func infoVM(name string) error {
	logger := common.NewColorLogger()

	// Get TrueNAS connection details
	host, apiKey, err := getTrueNASCredentials()
	if err != nil {
		return err
	}

	// Create VM manager
	vmManager := truenas.NewVMManager(host, apiKey, 443, true)
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	return vmManager.GetVMInfo(name)
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
3. Upload the ISO to TrueNAS storage or vSphere datastore
4. Update the node configuration templates with the new schematic ID

This separates ISO preparation from VM deployment, allowing you to prepare the ISO once
and deploy multiple VMs using the same custom configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return prepareISOWithProvider(provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Storage provider: vsphere (default) or truenas")

	return cmd
}

// prepareISOWithProvider handles the ISO generation and upload process for different providers
func prepareISOWithProvider(provider string) error {
	logger := common.NewColorLogger()
	logger.Info("Starting custom Talos ISO preparation for provider: %s", provider)

	switch provider {
	case "truenas":
		return prepareISOForTrueNAS()
	case "vsphere":
		return prepareISOForVSphere()
	default:
		return fmt.Errorf("unsupported provider: %s. Supported providers: truenas, vsphere", provider)
	}
}

// prepareISOForTrueNAS handles TrueNAS-specific ISO preparation
func prepareISOForTrueNAS() error {
	logger := common.NewColorLogger()
	logger.Info("Starting custom Talos ISO preparation for TrueNAS...")

	// Load version configuration
	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	logger.Debug("Using versions: Kubernetes=%s, Talos=%s", versionConfig.KubernetesVersion, versionConfig.TalosVersion)

	// Create factory client
	logger.Debug("Creating Talos factory client")
	factoryClient := talos.NewFactoryClient()
	if factoryClient == nil {
		return fmt.Errorf("failed to create factory client")
	}

	// Load schematic from embedded templates
	logger.Info("STEP 1: Loading schematic configuration...")
	schematic, err := factoryClient.LoadSchematicFromTemplate()
	if err != nil {
		return fmt.Errorf("failed to load schematic template: %w", err)
	}
	logger.Success("Schematic configuration loaded successfully")

	// Generate ISO from schematic
	logger.Info("STEP 2: Generating custom Talos ISO...")

	var isoInfo *talos.ISOInfo
	err = ui.SpinWithFunc("Generating custom Talos ISO", func() error {
		logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=metal", versionConfig.TalosVersion)
		var genErr error
		isoInfo, genErr = factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "metal")
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

	// Upload ISO to TrueNAS
	logger.Info("STEP 3: Uploading ISO to TrueNAS...")
	downloader := iso.NewDownloader()

	// Create download configuration with standard filename
	downloadConfig := iso.GetDefaultConfig()
	downloadConfig.TrueNASHost = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_HOST")
	downloadConfig.TrueNASUsername = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_USERNAME")
	downloadConfig.ISOURL = isoInfo.URL
	downloadConfig.ISOFilename = "vmware-amd64.iso" // Standard filename for vSphere provider

	err = ui.SpinWithFunc("Uploading ISO to TrueNAS", func() error {
		if dlErr := downloader.DownloadCustomISO(downloadConfig); dlErr != nil {
			return fmt.Errorf("failed to upload custom ISO to TrueNAS: %w", dlErr)
		}
		return nil
	})

	if err != nil {
		return err
	}

	logger.Success("Custom ISO uploaded to TrueNAS successfully")
	logger.Info("ISO Location: %s/%s", downloadConfig.ISOStoragePath, downloadConfig.ISOFilename)

	// Update node templates with schematic ID
	logger.Info("STEP 4: Updating node configuration templates...")
	if err := updateNodeTemplatesWithSchematic(isoInfo.SchematicID, isoInfo.TalosVersion); err != nil {
		logger.Warn("Failed to update node templates: %v", err)
		logger.Warn("You may need to manually update the templates with schematic ID: %s", isoInfo.SchematicID)
	} else {
		logger.Success("Node configuration templates updated successfully")
	}

	logger.Success("ISO preparation completed successfully!")
	logger.Info("Summary:")
	logger.Info("  - Custom ISO generated and uploaded to TrueNAS")
	logger.Info("  - Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  - Talos Version: %s", isoInfo.TalosVersion)
	logger.Info("  - ISO Path: %s/%s", downloadConfig.ISOStoragePath, downloadConfig.ISOFilename)
	logger.Info("  - Node templates updated with new schematic ID")
	logger.Info("")
	logger.Info("You can now deploy VMs using: homeops talos deploy-vm --name <vm_name> [other flags]")
	logger.Info("(The deploy-vm command will automatically use the prepared ISO)")

	return nil
}

// prepareISOForVSphere handles vSphere-specific ISO preparation
func prepareISOForVSphere() error {
	logger := common.NewColorLogger()
	logger.Info("Starting custom Talos ISO preparation for vSphere...")

	// Load version configuration
	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	logger.Debug("Using versions: Kubernetes=%s, Talos=%s", versionConfig.KubernetesVersion, versionConfig.TalosVersion)

	// Create factory client
	logger.Debug("Creating Talos factory client")
	factoryClient := talos.NewFactoryClient()
	if factoryClient == nil {
		return fmt.Errorf("failed to create factory client")
	}

	// Load schematic from embedded templates
	logger.Info("STEP 1: Loading schematic configuration...")
	schematic, err := factoryClient.LoadSchematicFromTemplate()
	if err != nil {
		return fmt.Errorf("failed to load schematic template: %w", err)
	}
	logger.Success("Schematic configuration loaded successfully")

	// Generate ISO from schematic
	logger.Info("STEP 2: Generating custom Talos ISO...")

	var isoInfo *talos.ISOInfo
	err = ui.SpinWithFunc("Generating custom Talos ISO", func() error {
		logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=vmware", versionConfig.TalosVersion)
		var genErr error
		isoInfo, genErr = factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "vmware")
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

	// Upload ISO to vSphere datastore
	logger.Info("STEP 3: Uploading ISO to vSphere datastore...")
	err = ui.SpinWithFunc("Uploading ISO to vSphere", func() error {
		if uploadErr := uploadISOToVSphere(isoInfo.URL); uploadErr != nil {
			return fmt.Errorf("failed to upload custom ISO to vSphere: %w", uploadErr)
		}
		return nil
	})

	if err != nil {
		return err
	}

	logger.Success("Custom ISO uploaded to vSphere datastore successfully")
	logger.Info("ISO Location: [datastore1] vmware-amd64.iso")

	// Update node templates with schematic ID
	logger.Info("STEP 4: Updating node configuration templates...")
	if err := updateNodeTemplatesWithSchematic(isoInfo.SchematicID, isoInfo.TalosVersion); err != nil {
		logger.Warn("Failed to update node templates: %v", err)
		logger.Warn("You may need to manually update the templates with schematic ID: %s", isoInfo.SchematicID)
	} else {
		logger.Success("Node configuration templates updated successfully")
	}

	logger.Success("ISO preparation completed successfully!")
	logger.Info("Summary:")
	logger.Info("  - Custom ISO generated and uploaded to vSphere datastore1")
	logger.Info("  - Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  - Talos Version: %s", isoInfo.TalosVersion)
	logger.Info("  - ISO Path: [datastore1] vmware-amd64.iso")
	logger.Info("  - Node templates updated with new schematic ID")
	logger.Info("")
	logger.Info("You can now deploy VMs using: homeops talos deploy-vm --provider vsphere --name <vm_name> [other flags]")
	logger.Info("(The deploy-vm command will automatically use the prepared ISO)")

	return nil
}

// uploadISOToVSphere downloads ISO from URL and uploads it to vSphere datastore
func uploadISOToVSphere(isoURL string) error {
	logger := common.NewColorLogger()

	// Get vSphere credentials
	host := common.Get1PasswordSecretSilent("op://Infrastructure/esxi/host")
	username := common.Get1PasswordSecretSilent("op://Infrastructure/esxi/username")
	password := common.Get1PasswordSecretSilent("op://Infrastructure/esxi/password")

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("failed to get vSphere credentials from 1Password")
	}

	logger.Debug("Connecting to vSphere host: %s", host)
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

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
	logger.Info("Uploading ISO to vSphere datastore1...")
	if err := client.UploadISOToDatastore(tempFile, "datastore1", "vmware-amd64.iso"); err != nil {
		return fmt.Errorf("failed to upload ISO to datastore: %w", err)
	}

	logger.Success("ISO uploaded to vSphere datastore successfully")
	return nil
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
	resp, err := http.Get(isoURL)
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
	templateFile := "cmd/homeops-cli/internal/templates/talos/controlplane.yaml"
	logger.Debug("Updating controlplane template: %s", templateFile)

	// Read the template file
	content, err := os.ReadFile(templateFile)
	if err != nil {
		return fmt.Errorf("failed to read controlplane template %s: %w", templateFile, err)
	}

	// Build the new factory image URL with the schematic ID
	newFactoryImage := fmt.Sprintf("factory.talos.dev/vmware-installer/%s:%s", schematicID, talosVersion)

	// Replace the existing factory image URL with the new schematic-based URL
	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	for i, line := range lines {
		if strings.Contains(line, "image: factory.talos.dev/vmware-installer/") {
			// Extract the indentation to maintain YAML formatting
			indent := ""
			for _, char := range line {
				if char == ' ' {
					indent += " "
				} else {
					break
				}
			}
			lines[i] = indent + "image: " + newFactoryImage
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

// newCleanupZVolsCommand creates a command to cleanup orphaned ZVols
func newCleanupZVolsCommand() *cobra.Command {
	var (
		vmName      string
		storagePool string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "cleanup-zvols",
		Short: "Clean up orphaned ZVols for a VM that was already deleted",
		Long:  `Clean up orphaned ZVols when a VM was deleted but its ZVols remain. This is useful when VM deletion didn't properly clean up the storage volumes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := ui.Confirm(fmt.Sprintf("Delete orphaned ZVols for VM '%s'?", vmName), false)
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("cleanup cancelled")
				}
			}
			return cleanupOrphanedZVols(vmName, storagePool)
		},
	}

	cmd.Flags().StringVar(&vmName, "vm-name", "", "Name of the VM whose ZVols to clean up (required)")
	cmd.Flags().StringVar(&storagePool, "pool", "flashstor", "Storage pool (default: flashstor)")
	cmd.Flags().BoolVar(&force, "force", false, "Force cleanup without confirmation")
	_ = cmd.MarkFlagRequired("vm-name")

	return cmd
}

// cleanupOrphanedZVols deletes orphaned ZVols for a VM that no longer exists
func cleanupOrphanedZVols(vmName, storagePool string) error {
	logger := common.NewColorLogger()
	logger.Info("Starting cleanup of orphaned ZVols for VM: %s", vmName)

	// Get TrueNAS connection details
	host, apiKey, err := getTrueNASCredentials()
	if err != nil {
		return err
	}

	// Create VM manager
	vmManager := truenas.NewVMManager(host, apiKey, 443, true)
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	// Use the CleanupOrphanedZVols method from VM manager
	if err := vmManager.CleanupOrphanedZVols(vmName, storagePool); err != nil {
		return fmt.Errorf("failed to cleanup orphaned ZVols: %w", err)
	}

	logger.Success("Successfully cleaned up orphaned ZVols for VM: %s", vmName)
	return nil
}

// deployVMOnVSphere deploys one or more VMs on vSphere/ESXi
// getNodeMacAddress reads the MAC address from the node's YAML configuration file
func getNodeMacAddress(nodeName string) (string, error) {
	// Map node names to IP addresses
	nodeIPs := map[string]string{
		"k8s-0": "192.168.122.10",
		"k8s-1": "192.168.122.11",
		"k8s-2": "192.168.122.12",
	}

	// Get the IP for this node
	nodeIP, ok := nodeIPs[nodeName]
	if !ok {
		// Not a predefined k8s node, return empty for auto-generation
		return "", nil
	}

	// Read the node configuration from embedded templates
	nodeConfigPath := fmt.Sprintf("talos/nodes/%s.yaml", nodeIP)
	templateContent, err := templates.GetTalosTemplate(nodeConfigPath)
	if err != nil {
		// If template doesn't exist, return empty for auto-generation
		return "", nil
	}
	data := []byte(templateContent)

	// Parse the YAML to extract MAC address
	var nodeConfig map[string]interface{}
	if err := yaml.Unmarshal(data, &nodeConfig); err != nil {
		return "", fmt.Errorf("failed to parse node config: %w", err)
	}

	// Navigate through the YAML structure to find the MAC address
	// Path: machine.network.interfaces[0].deviceSelector.hardwareAddr
	if machine, ok := nodeConfig["machine"].(map[string]interface{}); ok {
		if network, ok := machine["network"].(map[string]interface{}); ok {
			if interfaces, ok := network["interfaces"].([]interface{}); ok && len(interfaces) > 0 {
				if iface, ok := interfaces[0].(map[string]interface{}); ok {
					if deviceSelector, ok := iface["deviceSelector"].(map[string]interface{}); ok {
						if hardwareAddr, ok := deviceSelector["hardwareAddr"].(string); ok {
							return hardwareAddr, nil
						}
					}
				}
			}
		}
	}

	return "", nil // Return empty if MAC not found in config
}

func deployVMOnVSphere(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount int) error {
	logger := common.NewColorLogger()
	logger.Info("Starting vSphere/ESXi VM deployment with enhanced configuration")

	// Get vSphere credentials from 1Password or environment
	host := get1PasswordSecret("op://Infrastructure/esxi/add more/host")
	if host == "" {
		host = os.Getenv("VSPHERE_HOST")
	}
	username := get1PasswordSecret("op://Infrastructure/esxi/username")
	if username == "" {
		username = os.Getenv("VSPHERE_USERNAME")
	}
	password := get1PasswordSecret("op://Infrastructure/esxi/password")
	if password == "" {
		password = os.Getenv("VSPHERE_PASSWORD")
	}

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("vSphere credentials not found. Please set VSPHERE_HOST, VSPHERE_USERNAME, and VSPHERE_PASSWORD environment variables or configure 1Password")
	}

	// Create vSphere client
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
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
		// Note: For vSphere, we assume the ISO is already on the datastore
		// The user should run prepare-iso first or we need to handle upload differently
		logger.Warn("For vSphere, please ensure the ISO is already uploaded to the datastore")
		logger.Warn("Run 'homeops talos prepare-iso' first if needed")
		isoPath = "[datastore1] vmware-amd64.iso" // Use datastore1 to match manual VMs
	} else {
		// Use existing ISO on datastore1 (matches manual VMs)
		isoPath = "[datastore1] vmware-amd64.iso"
	}

	// Prepare VM configurations with enhanced settings
	var configs []vsphere.VMConfig

	if nodeCount == 1 {
		// Single VM deployment
		// Check if this is a k8s node and get the appropriate MAC address
		vmMacAddress := macAddress
		if vmMacAddress == "" && strings.HasPrefix(baseName, "k8s-") {
			// Try to read MAC address from node configuration
			if mac, err := getNodeMacAddress(baseName); err != nil {
				logger.Warn("Failed to read MAC address from node config: %v", err)
			} else if mac != "" {
				vmMacAddress = mac
				logger.Info("Using MAC address from node config: %s", mac)
			}
		}

		config := vsphere.VMConfig{
			Name:                 baseName,
			Memory:               memory,
			VCPUs:                vcpus,
			DiskSize:             diskSize,
			OpenEBSSize:          openebsSize,
			Datastore:            datastore,
			Network:              network,
			ISO:                  isoPath,
			MacAddress:           vmMacAddress,
			PowerOn:              true, // Power on with auto unregister/re-register on failure
			EnableIOMMU:          true, // Enable IOMMU for Talos VMs
			ExposeCounters:       true, // Expose CPU performance counters
			ThinProvisioned:      true, // Use thin provisioned disks (matches manual VM)
			EnablePrecisionClock: true, // Add precision clock device
			EnableWatchdog:       true, // Add watchdog timer device
		}
		configs = append(configs, config)
	} else {
		// Multiple VM deployment with zero-indexed numbering (0, 1, 2, ...)
		for i := 0; i < nodeCount; i++ {
			vmName := fmt.Sprintf("%s-%d", baseName, i)

			// Get MAC address for k8s nodes from configuration files
			vmMacAddress := ""
			if strings.HasPrefix(vmName, "k8s-") {
				if mac, err := getNodeMacAddress(vmName); err != nil {
					logger.Warn("Failed to read MAC address for %s: %v", vmName, err)
				} else if mac != "" {
					vmMacAddress = mac
					logger.Info("Using MAC address from config for %s: %s", vmName, mac)
				}
			}

			config := vsphere.VMConfig{
				Name:                 vmName,
				Memory:               memory,
				VCPUs:                vcpus,
				DiskSize:             diskSize,
				OpenEBSSize:          openebsSize,
				Datastore:            datastore,
				Network:              network,
				ISO:                  isoPath,
				MacAddress:           vmMacAddress,
				PowerOn:              true, // Power on with auto unregister/re-register on failure
				EnableIOMMU:          true, // Enable IOMMU for Talos VMs
				ExposeCounters:       true, // Expose CPU performance counters
				ThinProvisioned:      true, // Use thin provisioned disks (matches manual VM)
				EnablePrecisionClock: true, // Add precision clock device
				EnableWatchdog:       true, // Add watchdog timer device
			}
			configs = append(configs, config)
		}
	}

	// Deploy VMs
	if len(configs) == 1 {
		// Single VM deployment
		logger.Info("Deploying VM: %s", configs[0].Name)
		logger.Info("Enhanced Configuration:")
		logger.Info("  Memory: %d MB", configs[0].Memory)
		logger.Info("  vCPUs: %d", configs[0].VCPUs)
		logger.Info("  Boot Disk: %d GB (thin provisioned: %v)", configs[0].DiskSize, configs[0].ThinProvisioned)
		logger.Info("  OpenEBS Disk: %d GB (thin provisioned: %v)", configs[0].OpenEBSSize, configs[0].ThinProvisioned)
		logger.Info("  Datastore: %s", configs[0].Datastore)
		logger.Info("  Network: %s (vmxnet3)", configs[0].Network)
		logger.Info("  ISO: %s", configs[0].ISO)
		if configs[0].MacAddress != "" {
			logger.Info("  MAC Address: %s", configs[0].MacAddress)
		}
		logger.Info("  IOMMU Enabled: %v", configs[0].EnableIOMMU)
		logger.Info("  CPU Counters Exposed: %v", configs[0].ExposeCounters)
		logger.Info("  Precision Clock: %v", configs[0].EnablePrecisionClock)
		logger.Info("  Watchdog Timer: %v", configs[0].EnableWatchdog)
		logger.Info("  EFI Firmware: enabled")
		logger.Info("  UEFI Secure Boot: disabled")
		logger.Info("  NVME Controllers: 2 (separate for each disk)")

		_, err := client.CreateVM(configs[0])
		if err != nil {
			return fmt.Errorf("failed to create VM: %w", err)
		}

		logger.Success("VM %s deployed successfully with enhanced configuration!", configs[0].Name)
	} else {
		// Parallel VM deployment
		logger.Info("Deploying %d VMs in parallel (max concurrent: %d)", len(configs), concurrent)
		logger.Info("Enhanced VM Configuration (for all VMs):")
		logger.Info("  Memory: %d MB", memory)
		logger.Info("  vCPUs: %d", vcpus)
		logger.Info("  Boot Disk: %d GB (thin provisioned)", diskSize)
		logger.Info("  OpenEBS Disk: %d GB (thin provisioned)", openebsSize)
		logger.Info("  Datastore: %s", datastore)
		logger.Info("  Network: %s (vmxnet3)", network)
		logger.Info("  ISO: %s", isoPath)
		logger.Info("  IOMMU Enabled: true")
		logger.Info("  CPU Counters Exposed: true")
		logger.Info("  Precision Clock: enabled")
		logger.Info("  Watchdog Timer: enabled")
		logger.Info("  EFI Firmware: enabled")
		logger.Info("  UEFI Secure Boot: disabled")
		logger.Info("  NVME Controllers: 2 (separate for each disk)")
		logger.Info("")
		logger.Info("VMs to deploy:")
		for _, config := range configs {
			if config.MacAddress != "" {
				logger.Info("  - %s (MAC: %s)", config.Name, config.MacAddress)
			} else {
				logger.Info("  - %s", config.Name)
			}
		}

		if err := client.DeployVMsConcurrently(configs); err != nil {
			return fmt.Errorf("parallel deployment failed: %w", err)
		}

		logger.Success("Successfully deployed %d VMs with enhanced configuration!", len(configs))
	}

	return nil
}

// deleteVMOnVSphere deletes a VM from vSphere/ESXi
func deleteVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Starting vSphere/ESXi VM deletion for: %s", vmName)

	// Get vSphere credentials from 1Password or environment
	host := get1PasswordSecret("op://Infrastructure/esxi/add more/host")
	if host == "" {
		host = os.Getenv("VSPHERE_HOST")
	}
	username := get1PasswordSecret("op://Infrastructure/esxi/username")
	if username == "" {
		username = os.Getenv("VSPHERE_USERNAME")
	}
	password := get1PasswordSecret("op://Infrastructure/esxi/password")
	if password == "" {
		password = os.Getenv("VSPHERE_PASSWORD")
	}

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("vSphere credentials not found. Please set VSPHERE_HOST, VSPHERE_USERNAME, and VSPHERE_PASSWORD environment variables or configure 1Password")
	}

	// Create vSphere client
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

	// Find the VM
	vm, err := client.FindVM(vmName)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", vmName, err)
	}

	logger.Info("Found VM: %s", vmName)

	// Delete the VM
	if err := client.DeleteVM(vm); err != nil {
		return fmt.Errorf("failed to delete VM %s: %w", vmName, err)
	}

	logger.Success("VM %s deleted successfully!", vmName)
	return nil
}

func newPowerOnVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "poweron",
		Short: "Power on a VM on TrueNAS or vSphere/ESXi",
		Long:  `Power on a VM on TrueNAS or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return powerOnVM(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func newPowerOffVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "poweroff",
		Short: "Power off a VM on TrueNAS or vSphere/ESXi",
		Long:  `Power off a VM on TrueNAS or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return powerOffVM(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "vsphere", "Virtualization provider: vsphere/esxi (default) or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

// powerOnVMOnVSphere powers on a VM on vSphere/ESXi
// powerOnVM powers on a VM on the specified provider with interactive selector
func powerOnVM(name, provider string) error {
	// If VM name is not provided, prompt for selection based on provider
	if name == "" {
		var vmNames []string
		var err error

		// Use appropriate VM listing based on provider
		if provider == "truenas" {
			vmNames, err = getTrueNASVMNames()
		} else {
			// Default to ESXi/vSphere for vsphere, esxi, or empty provider
			vmNames, err = getESXiVMNames()
		}

		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to power on:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Call appropriate power on function based on provider
	if provider == "truenas" {
		return startVM(name)
	}
	return powerOnVMOnVSphere(name)
}

// powerOffVM powers off a VM on the specified provider with interactive selector
func powerOffVM(name, provider string) error {
	// If VM name is not provided, prompt for selection based on provider
	if name == "" {
		var vmNames []string
		var err error

		// Use appropriate VM listing based on provider
		if provider == "truenas" {
			vmNames, err = getTrueNASVMNames()
		} else {
			// Default to ESXi/vSphere for vsphere, esxi, or empty provider
			vmNames, err = getESXiVMNames()
		}

		if err != nil {
			return err
		}

		selectedVM, err := ui.Choose("Select VM to power off:", vmNames)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("VM selection failed: %w", err)
		}
		name = selectedVM
	}

	// Call appropriate power off function based on provider
	if provider == "truenas" {
		return stopVM(name)
	}
	return powerOffVMOnVSphere(name)
}

func powerOnVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Powering on vSphere/ESXi VM: %s", vmName)

	// Get vSphere credentials from 1Password or environment
	host := get1PasswordSecret("op://Infrastructure/esxi/add more/host")
	if host == "" {
		host = os.Getenv("VSPHERE_HOST")
	}
	username := get1PasswordSecret("op://Infrastructure/esxi/username")
	if username == "" {
		username = os.Getenv("VSPHERE_USERNAME")
	}
	password := get1PasswordSecret("op://Infrastructure/esxi/password")
	if password == "" {
		password = os.Getenv("VSPHERE_PASSWORD")
	}

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("vSphere credentials not found. Please set VSPHERE_HOST, VSPHERE_USERNAME, and VSPHERE_PASSWORD environment variables or configure 1Password")
	}

	// Create vSphere client
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

	// Find the VM
	vm, err := client.FindVM(vmName)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", vmName, err)
	}

	// Power on the VM
	if err := client.PowerOnVM(vm); err != nil {
		return fmt.Errorf("failed to power on VM %s: %w", vmName, err)
	}

	logger.Success("VM %s powered on successfully!", vmName)
	return nil
}

// powerOffVMOnVSphere powers off a VM on vSphere/ESXi
func powerOffVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Powering off vSphere/ESXi VM: %s", vmName)

	// Get vSphere credentials from 1Password or environment
	host := get1PasswordSecret("op://Infrastructure/esxi/add more/host")
	if host == "" {
		host = os.Getenv("VSPHERE_HOST")
	}
	username := get1PasswordSecret("op://Infrastructure/esxi/username")
	if username == "" {
		username = os.Getenv("VSPHERE_USERNAME")
	}
	password := get1PasswordSecret("op://Infrastructure/esxi/password")
	if password == "" {
		password = os.Getenv("VSPHERE_PASSWORD")
	}

	if host == "" || username == "" || password == "" {
		return fmt.Errorf("vSphere credentials not found. Please set VSPHERE_HOST, VSPHERE_USERNAME, and VSPHERE_PASSWORD environment variables or configure 1Password")
	}

	// Create vSphere client
	client := vsphere.NewClient(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

	// Find the VM
	vm, err := client.FindVM(vmName)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", vmName, err)
	}

	// Power off the VM
	if err := client.PowerOffVM(vm); err != nil {
		return fmt.Errorf("failed to power off VM %s: %w", vmName, err)
	}

	logger.Success("VM %s powered off successfully!", vmName)
	return nil
}
