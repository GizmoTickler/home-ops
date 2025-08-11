package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/yaml"
)

type BootstrapConfig struct {
	RootDir        string
	KubeConfig     string
	TalosConfig    string
	K8sVersion     string
	TalosVersion   string
	DryRun         bool
	SkipCRDs       bool
	SkipResources  bool
	SkipHelmfile   bool
	SkipPreflight  bool
}

type PreflightResult struct {
	Name    string
	Status  string
	Message string
	Error   error
}

func NewCommand() *cobra.Command {
	var config BootstrapConfig

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap Talos nodes and Cluster applications",
		Long: `Bootstrap a complete Talos cluster including:
- Applying Talos configuration to all nodes
- Bootstrapping the cluster
- Installing CRDs and resources
- Syncing Helm releases`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(&config)
		},
	}

	// Add flags
	cmd.Flags().StringVar(&config.RootDir, "root-dir", ".", "Root directory of the project")
	cmd.Flags().StringVar(&config.KubeConfig, "kubeconfig", "./kubeconfig", "Path to kubeconfig file")
	cmd.Flags().StringVar(&config.TalosConfig, "talosconfig", "talosconfig", "Path to talosconfig file")
	cmd.Flags().StringVar(&config.K8sVersion, "k8s-version", os.Getenv("KUBERNETES_VERSION"), "Kubernetes version")
	cmd.Flags().StringVar(&config.TalosVersion, "talos-version", os.Getenv("TALOS_VERSION"), "Talos version")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Perform a dry run without making changes")
	cmd.Flags().BoolVar(&config.SkipCRDs, "skip-crds", false, "Skip CRD installation")
	cmd.Flags().BoolVar(&config.SkipResources, "skip-resources", false, "Skip resource creation")
	cmd.Flags().BoolVar(&config.SkipHelmfile, "skip-helmfile", false, "Skip Helmfile sync")
	cmd.Flags().BoolVar(&config.SkipPreflight, "skip-preflight", false, "Skip preflight checks (not recommended)")

	return cmd
}

func runBootstrap(config *BootstrapConfig) error {
	// Initialize logger with colors
	logger := common.NewColorLogger()

	logger.Info("Starting cluster bootstrap process")

	// Load versions from versions.env file if not provided
	if err := loadVersionsFromFile(config); err != nil {
		return fmt.Errorf("failed to load versions: %w", err)
	}

	// Run comprehensive preflight checks
	if !config.SkipPreflight {
		logger.Info("Running preflight checks...")
		if err := runPreflightChecks(config, logger); err != nil {
			return fmt.Errorf("preflight checks failed: %w", err)
		}
		logger.Success("All preflight checks passed")
	} else {
		logger.Warn("Skipping preflight checks - this may cause failures during bootstrap")
		// Still run basic prerequisite validation
		if err := validatePrerequisites(config); err != nil {
			return fmt.Errorf("prerequisite validation failed: %w", err)
		}
	}

	// Apply Talos configuration
	logger.Debug("Applying Talos configuration to nodes")
	if err := applyTalosConfig(config, logger); err != nil {
		return fmt.Errorf("failed to apply Talos config: %w", err)
	}

	// Bootstrap Talos
	logger.Debug("Bootstrapping Talos cluster")
	if err := bootstrapTalos(config, logger); err != nil {
		return fmt.Errorf("failed to bootstrap Talos: %w", err)
	}

	// Fetch kubeconfig
	logger.Debug("Fetching kubeconfig")
	if err := fetchKubeconfig(config, logger); err != nil {
		return fmt.Errorf("failed to fetch kubeconfig: %w", err)
	}

	// Wait for nodes to be ready
	logger.Debug("Waiting for nodes to be available")
	if err := waitForNodes(config, logger); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}

	// Apply CRDs
	if !config.SkipCRDs {
		logger.Debug("Applying CRDs")
		if err := applyCRDs(config, logger); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}

	// Apply resources
	if !config.SkipResources {
		logger.Debug("Applying resources")
		if err := applyResources(config, logger); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}

	// Sync Helm releases
	if !config.SkipHelmfile {
		logger.Debug("Syncing Helm releases")
		if err := syncHelmReleases(config, logger); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}

	logger.Success("Congrats! The cluster is bootstrapped and Flux is syncing the Git repository")
	return nil
}

func validatePrerequisites(config *BootstrapConfig) error {
	// Check for required binaries
	requiredBins := []string{"talosctl", "kubectl", "kustomize", "op", "helmfile"}
	for _, bin := range requiredBins {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary '%s' not found in PATH", bin)
		}
	}

	// Check for required environment variables
	if config.K8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}
	if config.TalosVersion == "" {
		return fmt.Errorf("TALOS_VERSION environment variable not set")
	}

	// Check for required files (only talosconfig since templates are now embedded)
	requiredFiles := []string{
		config.TalosConfig,
	}
	for _, file := range requiredFiles {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			return fmt.Errorf("required file '%s' not found", file)
		}
	}

	return nil
}

func runPreflightChecks(config *BootstrapConfig, logger *common.ColorLogger) error {
	checks := []func(*BootstrapConfig, *common.ColorLogger) *PreflightResult{
		checkToolAvailability,
		checkEnvironmentFiles,
		checkNetworkConnectivity,
		checkDNSResolution,
		check1PasswordAuthPreflight,
		checkMachineConfigRendering,
		checkTalosNodes,
	}

	var failures []string
	for _, check := range checks {
		result := check(config, logger)
		if result.Status == "PASS" {
			logger.Success(fmt.Sprintf("✓ %s: %s", result.Name, result.Message))
		} else if result.Status == "WARN" {
			logger.Warn(fmt.Sprintf("⚠ %s: %s", result.Name, result.Message))
		} else {
			logger.Error(fmt.Sprintf("✗ %s: %s", result.Name, result.Message))
			failures = append(failures, result.Name)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("preflight checks failed: %s", strings.Join(failures, ", "))
	}
	return nil
}

func checkToolAvailability(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	requiredBins := []string{"talosctl", "kubectl", "kustomize", "op", "helmfile"}
	var missing []string

	for _, bin := range requiredBins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}

	if len(missing) > 0 {
		return &PreflightResult{
			Name:    "Tool Availability",
			Status:  "FAIL",
			Message: fmt.Sprintf("Missing required tools: %s", strings.Join(missing, ", ")),
		}
	}

	return &PreflightResult{
		Name:    "Tool Availability",
		Status:  "PASS",
		Message: "All required tools are available",
	}
}

func checkEnvironmentFiles(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	// Check versions.env file in multiple possible locations
	possiblePaths := []string{
		filepath.Join(config.RootDir, "versions.env"),
		filepath.Join(config.RootDir, "kubernetes", "apps", "system-upgrade", "versions", "versions.env"),
	}

	var versionsFile string
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			versionsFile = path
			break
		}
	}

	if versionsFile == "" {
		return &PreflightResult{
			Name:    "Environment Files",
			Status:  "FAIL",
			Message: "versions.env file not found in any expected location",
		}
	}

	// Validate versions are set
	if config.K8sVersion == "" {
		return &PreflightResult{
			Name:    "Environment Files",
			Status:  "FAIL",
			Message: "KUBERNETES_VERSION not set",
		}
	}

	if config.TalosVersion == "" {
		return &PreflightResult{
			Name:    "Environment Files",
			Status:  "FAIL",
			Message: "TALOS_VERSION not set",
		}
	}

	// Check talosconfig file
	if _, err := os.Stat(config.TalosConfig); os.IsNotExist(err) {
		return &PreflightResult{
			Name:    "Environment Files",
			Status:  "FAIL",
			Message: fmt.Sprintf("talosconfig file not found: %s", config.TalosConfig),
		}
	}

	return &PreflightResult{
		Name:    "Environment Files",
		Status:  "PASS",
		Message: fmt.Sprintf("Environment files valid (K8s: %s, Talos: %s)", config.K8sVersion, config.TalosVersion),
	}
}

func checkNetworkConnectivity(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	// Test connectivity to GitHub (for CRDs)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://github.com", nil)
	resp, err := client.Do(req)
	if err != nil {
		return &PreflightResult{
			Name:    "Network Connectivity",
			Status:  "FAIL",
			Message: fmt.Sprintf("Cannot reach GitHub: %v", err),
		}
	}
	resp.Body.Close()

	return &PreflightResult{
		Name:    "Network Connectivity",
		Status:  "PASS",
		Message: "Network connectivity verified",
	}
}

func checkDNSResolution(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	// Test DNS resolution
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resolver := &net.Resolver{}
	_, err := resolver.LookupHost(ctx, "github.com")
	if err != nil {
		return &PreflightResult{
			Name:    "DNS Resolution",
			Status:  "FAIL",
			Message: fmt.Sprintf("DNS resolution failed: %v", err),
		}
	}

	return &PreflightResult{
		Name:    "DNS Resolution",
		Status:  "PASS",
		Message: "DNS resolution working",
	}
}

func check1PasswordAuthPreflight(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	// Check if 1Password CLI is authenticated
	cmd := exec.Command("op", "whoami", "--format=json")
	output, err := cmd.Output()
	if err != nil {
		// Not authenticated, attempt to sign in
		logger.Info("1Password CLI not authenticated. Attempting to sign in...")
		if err := check1PasswordAuth(); err != nil {
			return &PreflightResult{
				Name:    "1Password Authentication",
				Status:  "FAIL",
				Message: fmt.Sprintf("Failed to authenticate with 1Password: %v", err),
			}
		}
		// Authentication successful, continue with verification
		cmd = exec.Command("op", "whoami", "--format=json")
		output, err = cmd.Output()
		if err != nil {
			return &PreflightResult{
				Name:    "1Password Authentication",
				Status:  "FAIL",
				Message: "Authentication verification failed after signin",
			}
		}
	}

	// Verify we got valid JSON response
	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		return &PreflightResult{
			Name:    "1Password Authentication",
			Status:  "FAIL",
			Message: "Invalid 1Password authentication response",
		}
	}

	return &PreflightResult{
		Name:    "1Password Authentication",
		Status:  "PASS",
		Message: "1Password CLI authenticated",
	}
}

func checkMachineConfigRendering(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	// Test rendering of machine configurations
	// Only test controlplane template since this cluster setup only has controlplane nodes
	nodeTemplates := []string{"controlplane"}

	for _, nodeType := range nodeTemplates {
		baseTemplate := fmt.Sprintf("%s.yaml.j2", nodeType)
		// Use a sample node template for patch testing
		patchTemplate := "nodes/192.168.122.10.yaml.j2"

		_, err := renderMachineConfigFromEmbedded(baseTemplate, patchTemplate)
		if err != nil {
			return &PreflightResult{
				Name:    "Machine Config Rendering",
				Status:  "FAIL",
				Message: fmt.Sprintf("Failed to render %s config: %v", nodeType, err),
			}
		}
	}

	return &PreflightResult{
		Name:    "Machine Config Rendering",
		Status:  "PASS",
		Message: "Machine configurations render successfully",
	}
}

func checkTalosNodes(config *BootstrapConfig, logger *common.ColorLogger) *PreflightResult {
	// Check if we can get Talos nodes
	nodes, err := getTalosNodes()
	if err != nil {
		return &PreflightResult{
			Name:    "Talos Nodes",
			Status:  "FAIL",
			Message: fmt.Sprintf("Cannot retrieve Talos nodes: %v", err),
		}
	}

	if len(nodes) == 0 {
		return &PreflightResult{
			Name:    "Talos Nodes",
			Status:  "WARN",
			Message: "No Talos nodes found in configuration",
		}
	}

	return &PreflightResult{
		Name:    "Talos Nodes",
		Status:  "PASS",
		Message: fmt.Sprintf("Found %d Talos nodes", len(nodes)),
	}
}

func check1PasswordAuth() error {
	// First, check if already authenticated
	cmd := exec.Command("op", "whoami", "--format=json")
	output, err := cmd.Output()
	if err == nil {
		// Verify we got valid JSON response
		var result map[string]interface{}
		if err := json.Unmarshal(output, &result); err == nil {
			return nil // Already authenticated
		}
	}

	// Not authenticated, attempt to sign in
	fmt.Println("1Password CLI not authenticated. Attempting to sign in...")
	signinCmd := exec.Command("op", "signin")
	signinCmd.Stdin = os.Stdin
	signinCmd.Stdout = os.Stdout
	signinCmd.Stderr = os.Stderr

	if err := signinCmd.Run(); err != nil {
		return fmt.Errorf("failed to sign in to 1Password: %w", err)
	}

	// Verify authentication after signin
	verifyCmd := exec.Command("op", "whoami", "--format=json")
	verifyOutput, err := verifyCmd.Output()
	if err != nil {
		return fmt.Errorf("authentication verification failed: %w", err)
	}

	// Verify we got valid JSON response
	var result map[string]interface{}
	if err := json.Unmarshal(verifyOutput, &result); err != nil {
		return fmt.Errorf("invalid 1Password response after signin: %w", err)
	}

	fmt.Println("Successfully authenticated with 1Password CLI")
	return nil
}

func applyTalosConfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get list of nodes from talosctl config with retry
	nodes, err := getTalosNodesWithRetry(logger, 3)
	if err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("Found %d Talos nodes to configure", len(nodes)))

	// Apply configuration to each node
	var failures []string
	for _, node := range nodes {
		nodeTemplate := fmt.Sprintf("nodes/%s.yaml.j2", node)
		
		// Get machine type from embedded node template
		machineType, err := getMachineTypeFromEmbedded(nodeTemplate)
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to determine machine type for %s: %v", node, err))
			failures = append(failures, node)
			continue
		}

		logger.Debug(fmt.Sprintf("Applying Talos configuration to %s (type: %s)", node, machineType))

		// Determine base template
		var baseTemplate string
		switch machineType {
		case "controlplane":
			baseTemplate = "controlplane.yaml.j2"
		case "worker":
			baseTemplate = "worker.yaml.j2"
		default:
			logger.Error(fmt.Sprintf("Unknown machine type for %s: %s", node, machineType))
			failures = append(failures, node)
			continue
		}

		// Render machine config using embedded templates
		renderedConfig, err := renderMachineConfigFromEmbedded(baseTemplate, nodeTemplate)
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to render config for %s: %v", node, err))
			failures = append(failures, node)
			continue
		}

		if config.DryRun {
			logger.Info(fmt.Sprintf("[DRY RUN] Would apply config to %s (type: %s)", node, machineType))
			continue
		}

		// Apply the config with retry
		if err := applyNodeConfigWithRetry(node, renderedConfig, logger, 3); err != nil {
			// Check if node is already configured
			if strings.Contains(err.Error(), "certificate required") || strings.Contains(err.Error(), "already configured") {
				logger.Warn(fmt.Sprintf("Node %s is already configured, skipping", node))
				continue
			}
			logger.Error(fmt.Sprintf("Failed to apply config to %s after retries: %v", node, err))
			failures = append(failures, node)
			continue
		}

		logger.Success(fmt.Sprintf("Successfully applied configuration to %s", node))
	}

	if len(failures) > 0 {
		return fmt.Errorf("failed to configure nodes: %s", strings.Join(failures, ", "))
	}

	return nil
}

func getTalosNodes() ([]string, error) {
	cmd := exec.Command("talosctl", "config", "info", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get Talos nodes: %w", err)
	}

	var configInfo struct {
		Nodes []string `json:"nodes"`
	}
	if err := json.Unmarshal(output, &configInfo); err != nil {
		return nil, fmt.Errorf("failed to parse Talos config: %w", err)
	}

	if len(configInfo.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes found in Talos configuration")
	}

	return configInfo.Nodes, nil
}

func getTalosNodesWithRetry(logger *common.ColorLogger, maxRetries int) ([]string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		nodes, err := getTalosNodes()
		if err == nil {
			return nodes, nil
		}

		lastErr = err
		logger.Warn(fmt.Sprintf("Attempt %d/%d to get Talos nodes failed: %v", attempt, maxRetries, err))

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	return nil, fmt.Errorf("failed to get Talos nodes after %d attempts: %w", maxRetries, lastErr)
}

func applyNodeConfigWithRetry(node string, config []byte, logger *common.ColorLogger, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := applyNodeConfig(node, config)
		if err == nil {
			return nil
		}

		lastErr = err
		// Don't retry if node is already configured
		if strings.Contains(err.Error(), "certificate required") || strings.Contains(err.Error(), "already configured") {
			return err
		}

		logger.Warn(fmt.Sprintf("Attempt %d/%d to apply config to %s failed: %v", attempt, maxRetries, node, err))

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * 3 * time.Second)
		}
	}

	return fmt.Errorf("failed to apply config to %s after %d attempts: %w", node, maxRetries, lastErr)
}





// applyTalosPatch function removed - now using Go YAML processor in renderMachineConfig

func applyNodeConfig(node string, config []byte) error {
	cmd := exec.Command("talosctl", "--nodes", node, "apply-config", "--insecure", "--file", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(config)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, output)
	}

	return nil
}

func loadVersionsFromFile(config *BootstrapConfig) error {
	// Try multiple possible locations for versions.env
	possiblePaths := []string{
		filepath.Join(config.RootDir, "versions.env"),
		filepath.Join(config.RootDir, "kubernetes", "apps", "system-upgrade", "versions", "versions.env"),
	}

	var versionsFile string
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			versionsFile = path
			break
		}
	}

	if versionsFile == "" {
		return fmt.Errorf("versions.env file not found in any expected location")
	}

	file, err := os.Open(versionsFile)
	if err != nil {
		return fmt.Errorf("failed to open versions.env: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Only set if not already provided via environment or flags
		switch key {
		case "KUBERNETES_VERSION":
			if config.K8sVersion == "" {
				config.K8sVersion = value
			}
		case "TALOS_VERSION":
			if config.TalosVersion == "" {
				config.TalosVersion = value
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading versions.env: %w", err)
	}

	// Validate that required versions are now set
	if config.K8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION not found in versions.env or environment")
	}
	if config.TalosVersion == "" {
		return fmt.Errorf("TALOS_VERSION not found in versions.env or environment")
	}

	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getMachineTypeFromEmbedded(nodeTemplate string) (string, error) {
	// Get the node template content with proper talos/ prefix
	fullTemplatePath := fmt.Sprintf("talos/%s", nodeTemplate)
	content, err := templates.GetTalosTemplate(fullTemplatePath)
	if err != nil {
		return "", fmt.Errorf("failed to get node template: %w", err)
	}
	
	// Parse machine type from template content
	if strings.Contains(content, "type: controlplane") {
		return "controlplane", nil
	}
	return "worker", nil
}

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate string) ([]byte, error) {
	// Get environment variables for template rendering
	env := map[string]string{
		"KUBERNETES_VERSION": getEnvOrDefault("KUBERNETES_VERSION", "v1.29.0"),
		"TALOS_VERSION":      getEnvOrDefault("TALOS_VERSION", "v1.6.0"),
	}
	
	// Render base config from embedded template with proper talos/ prefix
	fullBaseTemplatePath := fmt.Sprintf("talos/%s", baseTemplate)
	baseConfig, err := templates.RenderTalosTemplate(fullBaseTemplatePath, env)
	if err != nil {
		return nil, fmt.Errorf("failed to render base config: %w", err)
	}
	
	// Render patch config from embedded template with proper talos/ prefix
	fullPatchTemplatePath := fmt.Sprintf("talos/%s", patchTemplate)
	patchConfig, err := templates.RenderTalosTemplate(fullPatchTemplatePath, env)
	if err != nil {
		return nil, fmt.Errorf("failed to render patch config: %w", err)
	}
	
	// Use Go YAML processor to merge
	metrics := metrics.NewPerformanceCollector()
	processor := yaml.NewProcessor(nil, metrics)
	
	return processor.MergeYAML([]byte(baseConfig), []byte(patchConfig))
}

func bootstrapTalos(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get a random controller node
	controller, err := getRandomController()
	if err != nil {
		return err
	}

	logger.Debug(fmt.Sprintf("Bootstrapping Talos on controller %s", controller))

	if config.DryRun {
		logger.Info("[DRY RUN] Would bootstrap Talos")
		return nil
	}

	// Try to bootstrap, checking if already bootstrapped
	for attempts := 0; attempts < 30; attempts++ {
		cmd := exec.Command("talosctl", "--nodes", controller, "bootstrap")
		output, err := cmd.CombinedOutput()
		
		if err == nil {
			logger.Info("Talos cluster bootstrapped successfully")
			return nil
		}

		outputStr := string(output)
		if strings.Contains(outputStr, "AlreadyExists") {
			logger.Info("Talos cluster is already bootstrapped")
			return nil
		}

		logger.Info(fmt.Sprintf("Bootstrap in progress, waiting 10 seconds... (attempt %d/30)", attempts+1))
		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("failed to bootstrap Talos after 30 attempts")
}

func getRandomController() (string, error) {
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
		return "", fmt.Errorf("no controllers found")
	}

	// Simple selection of first endpoint (you could randomize if needed)
	return configInfo.Endpoints[0], nil
}

func fetchKubeconfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	controller, err := getRandomController()
	if err != nil {
		return err
	}

	if config.DryRun {
		logger.Info("[DRY RUN] Would fetch kubeconfig")
		return nil
	}

	cmd := exec.Command("talosctl", "kubeconfig", "--nodes", controller, 
		"--force", "--force-context-name", "main", filepath.Base(config.KubeConfig))
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to fetch kubeconfig: %w", err)
	}

	logger.Info("Kubeconfig fetched successfully")
	return nil
}

func waitForNodes(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would wait for nodes to be ready")
		return nil
	}

	logger.Info("Waiting for Kubernetes nodes to become ready...")

	// First check if nodes are already ready
	cmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig, "-o", "wide")
	output, err := cmd.Output()
	if err == nil {
		logger.Debug("Current node status:")
		logger.Debug(string(output))
		
		// Check if all nodes are ready
		readyCmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig, "-o", "jsonpath={.items[*].status.conditions[?(@.type=='Ready')].status}")
		readyOutput, readyErr := readyCmd.Output()
		if readyErr == nil && !strings.Contains(string(readyOutput), "False") && strings.Contains(string(readyOutput), "True") {
			logger.Success("All nodes are already ready")
			return nil
		}
	}

	// Wait for nodes to become available with enhanced retry logic
	logger.Info("Waiting for nodes to become available...")
	for attempts := 0; attempts < 15; attempts++ {
		cmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig)
		if err := cmd.Run(); err == nil {
			logger.Success("Nodes are now available")
			break
		}
		
		if attempts == 14 {
			return fmt.Errorf("nodes did not become available after %d attempts", attempts+1)
		}
		
		logger.Info(fmt.Sprintf("Waiting for nodes to become available... (attempt %d/15)", attempts+1))
		time.Sleep(20 * time.Second)
	}

	// Wait for nodes to be ready with progress tracking
	logger.Info("Waiting for all nodes to be ready...")
	for attempts := 0; attempts < 20; attempts++ {
		cmd := exec.Command("kubectl", "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=30s", "--kubeconfig", config.KubeConfig)
		if err := cmd.Run(); err == nil {
			logger.Success("All nodes are ready")
			return nil
		}

		// Show current node status for debugging
		statusCmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig, "-o", "custom-columns=NAME:.metadata.name,STATUS:.status.conditions[?(@.type=='Ready')].status,REASON:.status.conditions[?(@.type=='Ready')].reason")
		statusOutput, statusErr := statusCmd.Output()
		if statusErr == nil {
			logger.Debug("Current node readiness status:")
			logger.Debug(string(statusOutput))
		}

		if attempts == 19 {
			return fmt.Errorf("nodes did not become ready after %d attempts (10 minutes)", attempts+1)
		}

		logger.Info(fmt.Sprintf("Waiting for nodes to be ready... (attempt %d/20)", attempts+1))
		time.Sleep(30 * time.Second)
	}

	return fmt.Errorf("unexpected error in waitForNodes")
}

func applyCRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	crds := []struct {
		name string
		url  string
	}{
		{
			name: "external-dns",
			url:  "https://raw.githubusercontent.com/kubernetes-sigs/external-dns/refs/tags/v0.18.0/config/crd/standard/dnsendpoints.externaldns.k8s.io.yaml",
		},
		{
			name: "gateway-api",
			url:  "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/experimental-install.yaml",
		},
		{
			name: "prometheus-operator",
			url:  "https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.84.1/stripped-down-crds.yaml",
		},
	}

	logger.Info(fmt.Sprintf("Applying %d CRDs...", len(crds)))
	var failures []string

	for _, crd := range crds {
		logger.Debug(fmt.Sprintf("Processing CRD: %s", crd.name))

		if config.DryRun {
			logger.Info(fmt.Sprintf("[DRY RUN] Would apply CRD: %s from %s", crd.name, crd.url))
			continue
		}

		// Apply CRD with retry mechanism
		if err := applyCRDWithRetry(crd.name, crd.url, config, logger, 3); err != nil {
			logger.Error(fmt.Sprintf("Failed to apply CRD %s: %v", crd.name, err))
			failures = append(failures, crd.name)
			continue
		}

		logger.Success(fmt.Sprintf("Successfully applied CRD: %s", crd.name))
	}

	if len(failures) > 0 {
		return fmt.Errorf("failed to apply CRDs: %s", strings.Join(failures, ", "))
	}

	logger.Success("All CRDs applied successfully")
	return nil
}

func applyCRDWithRetry(name, url string, config *BootstrapConfig, logger *common.ColorLogger, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := applySingleCRD(name, url, config, logger)
		if err == nil {
			return nil
		}

		lastErr = err
		logger.Warn(fmt.Sprintf("Attempt %d/%d to apply CRD %s failed: %v", attempt, maxRetries, name, err))

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * 5 * time.Second)
		}
	}

	return fmt.Errorf("failed to apply CRD %s after %d attempts: %w", name, maxRetries, lastErr)
}

func applySingleCRD(name, url string, config *BootstrapConfig, logger *common.ColorLogger) error {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Download CRD with context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download CRD: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Warn(fmt.Sprintf("Warning: failed to close response body: %v", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: failed to download CRD from %s", resp.StatusCode, url)
	}

	crdContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read CRD content: %w", err)
	}

	if len(crdContent) == 0 {
		return fmt.Errorf("downloaded CRD content is empty")
	}

	// Apply CRD with enhanced error handling
	cmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-", "--kubeconfig", config.KubeConfig)
	cmd.Stdin = bytes.NewReader(crdContent)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's already applied
		if strings.Contains(string(output), "unchanged") || strings.Contains(string(output), "configured") {
			logger.Debug(fmt.Sprintf("CRD %s already exists or was updated", name))
			return nil
		}
		return fmt.Errorf("kubectl apply failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

func applyResources(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would apply resources")
		return nil
	}

	// Render resources from embedded template
	env := map[string]string{
		"KUBERNETES_VERSION": getEnvOrDefault("KUBERNETES_VERSION", "v1.29.0"),
		"TALOS_VERSION":      getEnvOrDefault("TALOS_VERSION", "v1.6.0"),
	}
	
	resources, err := templates.RenderBootstrapTemplate("resources.yaml.j2", env)
	if err != nil {
		return fmt.Errorf("failed to render resources: %w", err)
	}

	// Apply resources
	cmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-")
	cmd.Stdin = bytes.NewReader([]byte(resources))
	
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply resources: %w\n%s", err, output)
	}

	logger.Info("Resources applied successfully")
	return nil
}

func syncHelmReleases(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would sync Helm releases")
		return nil
	}

	// Get embedded helmfile content
	helmfileContent, err := templates.GetBootstrapFile("helmfile.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded helmfile: %w", err)
	}

	// Create temporary file for helmfile
	tempFile, err := os.CreateTemp("", "helmfile-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(tempFile.Name()); removeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove temp file: %v\n", removeErr)
		}
	}()
	defer func() {
		if closeErr := tempFile.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close temp file: %v\n", closeErr)
		}
	}()

	// Write helmfile content to temp file
	if _, err := tempFile.WriteString(helmfileContent); err != nil {
		return fmt.Errorf("failed to write helmfile content: %w", err)
	}
	// Close the file explicitly before using it
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	cmd := exec.Command("helmfile", "--file", tempFile.Name(), "sync", "--hide-notes")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to sync Helm releases: %w", err)
	}

	logger.Info("Helm releases synced successfully")
	return nil
}
