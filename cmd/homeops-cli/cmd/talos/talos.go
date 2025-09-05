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

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/iso"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/talos"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vsphere"
	localyaml "homeops-cli/internal/yaml"
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
	// Try 1Password first
	host = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_HOST")
	apiKey = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_API")

	// Fall back to environment variables if 1Password fails
	if host == "" {
		host = os.Getenv("TRUENAS_HOST")
	}
	if apiKey == "" {
		apiKey = os.Getenv("TRUENAS_API_KEY")
	}

	// Check if we have both credentials
	if host == "" || apiKey == "" {
		return "", "", fmt.Errorf("TrueNAS credentials not found. Please set TRUENAS_HOST and TRUENAS_API_KEY environment variables or configure 1Password with 'op://Infrastructure/talosdeploy/TRUENAS_HOST' and 'op://Infrastructure/talosdeploy/TRUENAS_API'")
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
	password := get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS")
	if password != "" {
		return password
	}
	// Fall back to environment variable
	return os.Getenv("SPICE_PASSWORD")
}

func newApplyNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "apply-node",
		Short: "Apply Talos config to a node",
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyNodeConfig(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().StringVar(&mode, "mode", "auto", "Apply mode (auto, interactive, etc.)")
	_ = cmd.MarkFlagRequired("ip")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func applyNodeConfig(nodeIP, mode string) error {
	logger := common.NewColorLogger()

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

	// Resolve 1Password references in the rendered config (following bootstrap pattern)
	logger.Info("Resolving 1Password references in Talos configuration...")
	resolvedConfig, err := common.InjectSecrets(string(renderedConfig))
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
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
	renderer := templates.NewTemplateRenderer(".", logger, metricsCollector)

	// Prepare environment variables for template rendering
	env := make(map[string]string)
	env["SCHEMATIC_ID"] = schematicID

	// Add other common environment variables
	versionConfig := versionconfig.GetVersions(".")
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
		RunE: func(cmd *cobra.Command, args []string) error {
			return upgradeNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")
	_ = cmd.MarkFlagRequired("ip")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func upgradeNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()

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

	// Perform upgrade
	cmd := exec.Command("talosctl", "--nodes", nodeIP, "upgrade",
		"--image", factoryImage,
		"--reboot-mode", mode,
		"--timeout", "10m")

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
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

	versionConfig := versionconfig.GetVersions(".")
	k8sVersion := getEnvOrDefault("KUBERNETES_VERSION", versionConfig.KubernetesVersion)
	if k8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}

	logger.Info("Upgrading Kubernetes to version %s via node %s", k8sVersion, node)

	cmd := exec.Command("talosctl", "--nodes", node, "upgrade-k8s", "--to", k8sVersion)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
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
		RunE: func(cmd *cobra.Command, args []string) error {
			return rebootNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")
	_ = cmd.MarkFlagRequired("ip")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func rebootNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()
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
				fmt.Print("Shutdown the Talos cluster ... continue? (y/N): ")
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
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
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Printf("Reset Talos node '%s' ... continue? (y/N): ", nodeIP)
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("reset cancelled")
				}
			}
			return resetNode(nodeIP)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")
	_ = cmd.MarkFlagRequired("ip")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func resetNode(nodeIP string) error {
	logger := common.NewColorLogger()
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
				fmt.Print("Reset the Talos cluster ... continue? (y/N): ")
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
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

	rootDir := "."
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

func newDeployVMCommand() *cobra.Command {
	var (
		name           string
		memory         int
		vcpus          int
		diskSize       int
		openebsSize    int
		rookSize       int
		macAddress     string
		pool           string
		skipZVolCreate bool
		generateISO    bool
		provider       string
		// vSphere specific flags
		datastore  string
		network    string
		concurrent int
		nodeCount  int
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Talos VM on TrueNAS or vSphere/ESXi",
		Long: `Deploy a new Talos VM on TrueNAS or vSphere/ESXi. Use --provider to select the virtualization platform.

For TrueNAS: Uses proper ZVol naming convention and SPICE console.
For vSphere/ESXi: Deploys to specified datastore with iSCSI storage.

Use --generate-iso to create a custom ISO using the schematic.yaml configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "vsphere" || provider == "esxi" {
				return deployVMOnVSphere(name, memory, vcpus, diskSize, openebsSize, rookSize, macAddress, datastore, network, generateISO, concurrent, nodeCount)
			}
			return deployVMWithPattern(name, pool, memory, vcpus, diskSize, openebsSize, rookSize, macAddress, skipZVolCreate, generateISO)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "truenas", "Virtualization provider: truenas or vsphere/esxi")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required for single VM, base name for multiple VMs)")
	cmd.Flags().StringVar(&pool, "pool", "flashstor/VM", "Storage pool (TrueNAS only)")
	cmd.Flags().IntVar(&memory, "memory", 48*1024, "Memory in MB (default: 48GB)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 8, "Number of vCPUs (default: 8)")
	cmd.Flags().IntVar(&diskSize, "disk-size", 100, "Boot disk size in GB")
	cmd.Flags().IntVar(&openebsSize, "openebs-size", 800, "OpenEBS disk size in GB")
	cmd.Flags().IntVar(&rookSize, "rook-size", 600, "Rook disk size in GB")
	cmd.Flags().StringVar(&macAddress, "mac-address", "", "MAC address (optional)")
	cmd.Flags().BoolVar(&skipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation (TrueNAS only)")
	cmd.Flags().BoolVar(&generateISO, "generate-iso", false, "Generate custom ISO using schematic.yaml")

	// vSphere specific flags
	cmd.Flags().StringVar(&datastore, "datastore", "truenas", "Datastore name (vSphere only)")
	cmd.Flags().StringVar(&network, "network", "vl999", "Network port group name (vSphere only)")
	cmd.Flags().IntVar(&concurrent, "concurrent", 3, "Number of concurrent VM deployments (vSphere only)")
	cmd.Flags().IntVar(&nodeCount, "node-count", 1, "Number of VMs to deploy (vSphere only)")

	_ = cmd.MarkFlagRequired("name")

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

func deployVMWithPattern(name, pool string, memory, vcpus, diskSize, openebsSize, rookSize int, macAddress string, skipZVolCreate, generateISO bool) error {
	logger := common.NewColorLogger()
	logger.Info("Starting VM deployment: %s", name)
	logger.Debug("VM Configuration: pool=%s, memory=%dMB, vcpus=%d, diskSize=%dGB, openebsSize=%dGB, rookSize=%dGB, macAddress=%s, skipZVolCreate=%t, generateISO=%t",
		pool, memory, vcpus, diskSize, openebsSize, rookSize, macAddress, skipZVolCreate, generateISO)

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
		return fmt.Errorf("OpenEBS size cannot be negative, got %d", openebsSize)
	}
	if rookSize < 0 {
		return fmt.Errorf("rook size cannot be negative, got %d", rookSize)
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
		versionConfig := versionconfig.GetVersions(".")
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
		versionConfig := versionconfig.GetVersions(".")
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
		RookSize:      rookSize,
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
	logger.Debug("Calling vmManager.DeployVM with configuration")

	if err := vmManager.DeployVM(config); err != nil {
		logger.Error("VM deployment failed: %v", err)
		return fmt.Errorf("VM deployment failed: %w", err)
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
	logger.Info("  Boot disk:    %s/%s-boot (%dGB)", pool, name, diskSize)
	if openebsSize > 0 {
		logger.Info("  OpenEBS disk: %s/%s-ebs (%dGB)", pool, name, openebsSize)
	}
	if rookSize > 0 {
		logger.Info("  Rook disk:    %s/%s-rook (%dGB)", pool, name, rookSize)
	}

	logger.Debug("VM deployment function completed successfully")
	return nil
}

func newManageVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manage-vm",
		Short: "Manage VMs on TrueNAS",
		Long:  `Commands for managing VMs on TrueNAS Scale`,
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
	return &cobra.Command{
		Use:   "list",
		Short: "List all VMs on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listVMs()
		},
	}
}

func listVMs() error {
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
			fmt.Fprintf(os.Stderr, "Warning: failed to close VM manager: %v\n", closeErr)
		}
	}()

	return vmManager.ListVMs()
}

func newStartVMCommand() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a VM on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return startVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	_ = cmd.MarkFlagRequired("name")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func startVM(name string) error {
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
			fmt.Fprintf(os.Stderr, "Warning: failed to close VM manager: %v\n", closeErr)
		}
	}()

	return vmManager.StartVM(name)
}

func newStopVMCommand() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a VM on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	_ = cmd.MarkFlagRequired("name")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func stopVM(name string) error {
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
			fmt.Fprintf(os.Stderr, "Warning: failed to close VM manager: %v\n", closeErr)
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
		Long:  `Delete a VM on TrueNAS (with ZVols) or vSphere/ESXi. Use --provider to select the virtualization platform.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				if provider == "vsphere" || provider == "esxi" {
					fmt.Printf("Delete VM '%s' on vSphere/ESXi ... continue? (y/N): ", name)
				} else {
					fmt.Printf("Delete VM '%s' and all its ZVols on TrueNAS ... continue? (y/N): ", name)
				}
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("deletion cancelled")
				}
			}

			if provider == "vsphere" || provider == "esxi" {
				return deleteVMOnVSphere(name)
			}
			return deleteVM(name)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "truenas", "Virtualization provider: truenas or vsphere/esxi")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")
	_ = cmd.MarkFlagRequired("name")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func deleteVM(name string) error {
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
			fmt.Fprintf(os.Stderr, "Warning: failed to close VM manager: %v\n", closeErr)
		}
	}()

	// Delete VM and ZVols
	// Use the most common storage pool path where VMs are deployed
	// The VM manager will try multiple patterns to find the actual ZVols
	storagePool := getEnvOrDefault("STORAGE_POOL", "flashstor")
	return vmManager.DeleteVM(name, true, storagePool)
}

func newInfoVMCommand() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Get detailed information about a VM on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return infoVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	_ = cmd.MarkFlagRequired("name")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func infoVM(name string) error {
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
			fmt.Fprintf(os.Stderr, "Warning: failed to close VM manager: %v\n", closeErr)
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

	cmd.Flags().StringVar(&provider, "provider", "truenas", "Storage provider: truenas or vsphere")

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
	versionConfig := versionconfig.GetVersions(".")
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
	logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=metal", versionConfig.TalosVersion)

	isoInfo, err := factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "metal")
	if err != nil {
		return fmt.Errorf("ISO generation failed: %w", err)
	}

	if isoInfo == nil {
		return fmt.Errorf("ISO generation returned nil result")
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

	if err := downloader.DownloadCustomISO(downloadConfig); err != nil {
		return fmt.Errorf("failed to upload custom ISO to TrueNAS: %w", err)
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
	versionConfig := versionconfig.GetVersions(".")
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
	logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=vmware", versionConfig.TalosVersion)

	isoInfo, err := factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "vmware")
	if err != nil {
		return fmt.Errorf("ISO generation failed: %w", err)
	}

	if isoInfo == nil {
		return fmt.Errorf("ISO generation returned nil result")
	}

	logger.Success("Custom ISO generated successfully")
	logger.Info("ISO Details:")
	logger.Info("  URL: %s", isoInfo.URL)
	logger.Info("  Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  Talos Version: %s", isoInfo.TalosVersion)

	// Upload ISO to vSphere datastore
	logger.Info("STEP 3: Uploading ISO to vSphere datastore...")
	if err := uploadISOToVSphere(isoInfo.URL); err != nil {
		return fmt.Errorf("failed to upload custom ISO to vSphere: %w", err)
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
	templateFile := "internal/templates/talos/controlplane.yaml"
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
				fmt.Printf("Delete orphaned ZVols for VM '%s' ... continue? (y/N): ", vmName)
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
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

// getPhysicalFunction returns the appropriate SR-IOV physical function for load balancing
// Alternates between two physical functions: 0000:04:00.0 and 0000:04:00.1
func getPhysicalFunction(vmIndex int) string {
	physicalFunctions := []string{
		"0000:04:00.0", // First physical function
		"0000:04:00.1", // Second physical function
	}
	return physicalFunctions[vmIndex%len(physicalFunctions)]
}

func deployVMOnVSphere(baseName string, memory, vcpus, diskSize, openebsSize, rookSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount int) error {
	logger := common.NewColorLogger()
	logger.Info("Starting vSphere/ESXi VM deployment")

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

	// Prepare VM configurations
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
			Name:             baseName,
			Memory:           memory,
			VCPUs:            vcpus,
			DiskSize:         diskSize,
			OpenEBSSize:      openebsSize,
			RookSize:         rookSize,
			Datastore:        datastore,
			Network:          network,
			ISO:              isoPath,
			MacAddress:       vmMacAddress,
			PhysicalFunction: getPhysicalFunction(0), // Single VM gets first PF
			PowerOn:          false,                  // Don't power on by default
		}
		configs = append(configs, config)
	} else {
		// Multiple VM deployment with numbering
		for i := 1; i <= nodeCount; i++ {
			vmName := fmt.Sprintf("%s-%02d", baseName, i)

			// For k8s nodes, use index-based naming (k8s-0, k8s-1, k8s-2)
			if baseName == "k8s" {
				vmName = fmt.Sprintf("%s-%d", baseName, i-1)
			}

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
				Name:             vmName,
				Memory:           memory,
				VCPUs:            vcpus,
				DiskSize:         diskSize,
				OpenEBSSize:      openebsSize,
				RookSize:         rookSize,
				Datastore:        datastore,
				Network:          network,
				ISO:              isoPath,
				MacAddress:       vmMacAddress,
				PhysicalFunction: getPhysicalFunction(i - 1), // Alternate PFs based on VM index
				PowerOn:          false,                      // Don't power on by default
			}
			configs = append(configs, config)
		}
	}

	// Deploy VMs
	if len(configs) == 1 {
		// Single VM deployment
		logger.Info("Deploying VM: %s", configs[0].Name)
		logger.Info("Configuration:")
		logger.Info("  Memory: %d MB", configs[0].Memory)
		logger.Info("  vCPUs: %d", configs[0].VCPUs)
		logger.Info("  Boot Disk: %d GB", configs[0].DiskSize)
		logger.Info("  OpenEBS Disk: %d GB", configs[0].OpenEBSSize)
		logger.Info("  Rook Disk: %d GB", configs[0].RookSize)
		logger.Info("  Datastore: %s", configs[0].Datastore)
		logger.Info("  Network: %s", configs[0].Network)
		logger.Info("  ISO: %s", configs[0].ISO)

		vm, err := client.CreateVM(configs[0])
		if err != nil {
			return fmt.Errorf("failed to create VM: %w", err)
		}

		if configs[0].PowerOn {
			if err := client.PowerOnVM(vm); err != nil {
				return fmt.Errorf("failed to power on VM: %w", err)
			}
		}

		logger.Success("VM %s deployed successfully!", configs[0].Name)
	} else {
		// Parallel VM deployment
		logger.Info("Deploying %d VMs in parallel (max concurrent: %d)", len(configs), concurrent)
		logger.Info("VM Configuration (for all VMs):")
		logger.Info("  Memory: %d MB", memory)
		logger.Info("  vCPUs: %d", vcpus)
		logger.Info("  Boot Disk: %d GB", diskSize)
		logger.Info("  OpenEBS Disk: %d GB", openebsSize)
		logger.Info("  Rook Disk: %d GB", rookSize)
		logger.Info("  Datastore: %s", datastore)
		logger.Info("  Network: %s", network)
		logger.Info("  ISO: %s", isoPath)
		logger.Info("")
		logger.Info("VMs to deploy:")
		for _, config := range configs {
			if config.MacAddress != "" {
				logger.Info("  - %s (MAC: %s, PF: %s)", config.Name, config.MacAddress, config.PhysicalFunction)
			} else {
				logger.Info("  - %s (PF: %s)", config.Name, config.PhysicalFunction)
			}
		}

		if err := client.DeployVMsConcurrently(configs); err != nil {
			return fmt.Errorf("parallel deployment failed: %w", err)
		}

		logger.Success("Successfully deployed %d VMs!", len(configs))
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
		Long:  `Power on a VM on TrueNAS or vSphere/ESXi. Use --provider to select the virtualization platform.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "vsphere" || provider == "esxi" {
				return powerOnVMOnVSphere(name)
			}
			return startVM(name)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "truenas", "Virtualization provider: truenas or vsphere/esxi")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	_ = cmd.MarkFlagRequired("name")

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
		Long:  `Power off a VM on TrueNAS or vSphere/ESXi. Use --provider to select the virtualization platform.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "vsphere" || provider == "esxi" {
				return powerOffVMOnVSphere(name)
			}
			return stopVM(name)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "truenas", "Virtualization provider: truenas or vsphere/esxi")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

// powerOnVMOnVSphere powers on a VM on vSphere/ESXi
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
