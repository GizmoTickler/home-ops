package talos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/iso"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/talos"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/yaml"
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

// get1PasswordSecret retrieves a secret from 1Password using the op CLI
func get1PasswordSecret(reference string) string {
	cmd := exec.Command("op", "read", reference)
	output, err := cmd.Output()
	if err != nil {
		// Silently fail and return empty string to allow fallback to env vars
		return ""
	}
	return strings.TrimSpace(string(output))
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
	machineConfigTemplate := fmt.Sprintf("%s.yaml.j2", machineType)
	nodeConfigTemplate := fmt.Sprintf("nodes/%s.yaml.j2", nodeIP)

	// Render the configuration
	renderedConfig, err := renderMachineConfigFromEmbedded(machineConfigTemplate, nodeConfigTemplate)
	if err != nil {
		return fmt.Errorf("failed to render config: %w", err)
	}

	// Apply the configuration
	cmd := exec.Command("talosctl", "--nodes", nodeIP, "apply-config", "--mode", mode, "--file", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(renderedConfig)
	
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
	// Get environment variables for template rendering
	env := map[string]string{
		"KUBERNETES_VERSION": getEnvOrDefault("KUBERNETES_VERSION", "v1.29.0"),
		"TALOS_VERSION":      getEnvOrDefault("TALOS_VERSION", "v1.6.0"),
	}
	
	// Add schematic ID if provided
	if schematicID != "" {
		env["SCHEMATIC_ID"] = schematicID
	} else if envSchematicID := os.Getenv("SCHEMATIC_ID"); envSchematicID != "" {
		// Use schematic ID from environment variable if available
		env["SCHEMATIC_ID"] = envSchematicID
	} else {
		// Use default schematic ID if none provided
		env["SCHEMATIC_ID"] = "89b50c59f01a5ec3946078c1e4474c958b6f7fe9064654e15385ad1ad73f536c"
	}
	
	// Render base config from embedded template
	baseConfig, err := templates.RenderTalosTemplate(baseTemplate, env)
	if err != nil {
		return nil, fmt.Errorf("failed to render base config: %w", err)
	}
	
	// Render patch config from embedded template
	patchConfig, err := templates.RenderTalosTemplate(patchTemplate, env)
	if err != nil {
		return nil, fmt.Errorf("failed to render patch config: %w", err)
	}
	
	// Use Go YAML processor to merge
	metrics := metrics.NewPerformanceCollector()
	processor := yaml.NewProcessor(nil, metrics)
	
	return processor.MergeYAML([]byte(baseConfig), []byte(patchConfig))
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

	// Get factory image from node config
	nodeFile := filepath.Join("./talos/nodes", fmt.Sprintf("%s.yaml.j2", nodeIP))
	if !common.FileExists(nodeFile) {
		return fmt.Errorf("node config not found: %s", nodeFile)
	}

	// Get node config from embedded templates
	nodeTemplate := fmt.Sprintf("talos/nodes/%s.yaml.j2", nodeIP)
	configOutput, err := templates.GetTalosTemplate(nodeTemplate)
	if err != nil {
		return fmt.Errorf("failed to get node config: %w", err)
	}

	// Extract factory image using Go YAML processor
	metrics := metrics.NewPerformanceCollector()
	processor := yaml.NewProcessor(nil, metrics)
	
	// Parse YAML content into a map
	configData, err := processor.ParseString(string(configOutput))
	if err != nil {
		return fmt.Errorf("failed to parse node config: %w", err)
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

	k8sVersion := os.Getenv("KUBERNETES_VERSION")
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
		"--force", "--force-context-name", "main", rootDir)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to generate kubeconfig: %w\n%s", err, output)
	}

	logger.Success("Kubeconfig generated successfully")
	return nil
}

func newDeployVMCommand() *cobra.Command {
	var (
		name         string
		memory       int
		vcpus        int
		diskSize     int
		openebsSize  int
		rookSize     int
		macAddress   string
		pool         string
		skipZVolCreate bool
		generateISO  bool
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Talos VM on TrueNAS with auto-generated ZVol paths",
		Long:  `Deploy a new Talos VM on TrueNAS with proper ZVol naming convention. Use --generate-iso to create a custom ISO using the schematic.yaml configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return deployVMWithPattern(name, pool, memory, vcpus, diskSize, openebsSize, rookSize, macAddress, skipZVolCreate, generateISO)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().StringVar(&pool, "pool", "flashstor/VM", "Storage pool (default: flashstor/VM)")
	cmd.Flags().IntVar(&memory, "memory", 4096, "Memory in MB")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "Number of vCPUs")
	cmd.Flags().IntVar(&diskSize, "disk-size", 250, "Boot disk size in GB")
	cmd.Flags().IntVar(&openebsSize, "openebs-size", 1024, "OpenEBS disk size in GB")
	cmd.Flags().IntVar(&rookSize, "rook-size", 800, "Rook disk size in GB")
	cmd.Flags().StringVar(&macAddress, "mac-address", "", "MAC address (optional)")
	cmd.Flags().BoolVar(&skipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation")
	cmd.Flags().BoolVar(&generateISO, "generate-iso", false, "Generate custom ISO using schematic.yaml")
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
		return fmt.Errorf("Rook size cannot be negative, got %d", rookSize)
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
		logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=metal", talos.DefaultTalosVersion)
		isoInfo, err := factoryClient.GenerateISOFromSchematic(schematic, talos.DefaultTalosVersion, "amd64", "metal")
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
		// Schema-based ISO generation is required for VM deployment
		return fmt.Errorf("schema-based ISO generation is required for VM deployment. Please use the --generate-iso flag to create a custom Talos ISO with the required schematic configuration. Default ISOs are not supported as they lack the necessary customizations for this deployment workflow")
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
		Name:           name,
		Memory:         memory,
		VCPUs:          vcpus,
		DiskSize:       diskSize,
		OpenEBSSize:    openebsSize,
		RookSize:       rookSize,
		TrueNASHost:    host,
		TrueNASAPIKey:  apiKey,
		TrueNASPort:    443,
		NoSSL:          false,
		TalosISO:       isoURL,
		NetworkBridge:  networkBridge,
		StoragePool:    pool,
		MacAddress:     macAddress,
		// Let getZVolPaths handle path construction to avoid duplication
		// BootZVol, OpenEBSZVol, RookZVol will be auto-generated
		SkipZVolCreate: skipZVolCreate,
		SpicePassword:  spicePassword,
		UseSpice:       true, // Always use SPICE as per working scripts
		// Schematic configuration fields
		SchematicID:    schematicID,
		TalosVersion:   talosVersion,
		CustomISO:      customISO,
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
		newDeleteVMCommand(),
		newInfoVMCommand(),
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
		name  string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a VM and all associated ZVols on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Printf("Delete VM '%s' and all its ZVols ... continue? (y/N): ", name)
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("deletion cancelled")
				}
			}
			return deleteVM(name)
		},
	}

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
	storagePool := getEnvOrDefault("STORAGE_POOL", "tank")
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
