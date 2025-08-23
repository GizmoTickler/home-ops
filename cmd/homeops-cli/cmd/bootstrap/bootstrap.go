package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
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

	// Convert RootDir to absolute path if it's relative
	if !filepath.IsAbs(config.RootDir) {
		absPath, err := filepath.Abs(config.RootDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for root directory: %w", err)
		}
		config.RootDir = absPath
	}

	// Load versions from system-upgrade plans if not provided via flags/env
	if config.K8sVersion == "" || config.TalosVersion == "" {
		versionConfig := versionconfig.GetVersions(config.RootDir)
		if config.K8sVersion == "" {
			config.K8sVersion = versionConfig.KubernetesVersion
		}
		if config.TalosVersion == "" {
			config.TalosVersion = versionConfig.TalosVersion
		}
		logger.Debug("Loaded versions from system-upgrade plans: K8s=%s, Talos=%s", config.K8sVersion, config.TalosVersion)
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


// validateClusterSecretStoreTemplate validates the clustersecretstore YAML file
func validateClusterSecretStoreTemplate(logger *common.ColorLogger) error {
	logger.Info("Validating clustersecretstore.yaml file")

	// Get the YAML file content
	yamlContent, err := templates.GetBootstrapFile("clustersecretstore.yaml")
	if err != nil {
		return fmt.Errorf("failed to get clustersecretstore YAML: %w", err)
	}

	// Validate YAML syntax
	if err := validateYAMLSyntax([]byte(yamlContent)); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Check for 1Password references
	opRefs := extractOnePasswordReferences(yamlContent)
	if len(opRefs) == 0 {
		logger.Warn("No 1Password references found in clustersecretstore YAML")
	} else {
		logger.Info("Found %d 1Password references in clustersecretstore", len(opRefs))
		for _, ref := range opRefs {
			if err := validate1PasswordReference(ref); err != nil {
				return fmt.Errorf("invalid 1Password reference '%s': %w", ref, err)
			}
		}
		logger.Info("All 1Password references in clustersecretstore are valid")
	}

	logger.Info("ClusterSecretStore YAML validation completed successfully")
	return nil
}

// extractOnePasswordReferences finds all 1Password references in the content
func extractOnePasswordReferences(content string) []string {
	var refs []string

	// Find all op:// references using a more comprehensive approach
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, "op://") {
			// Find all op:// references in this line
			for i := 0; i < len(line); i++ {
				if strings.HasPrefix(line[i:], "op://") {
					start := i
					// Find the end of the reference
					end := len(line)
					for j := start + 5; j < len(line); j++ {
						if line[j] == ' ' || line[j] == '"' || line[j] == '\'' || line[j] == '}' || line[j] == ',' || line[j] == '\n' || line[j] == '\t' {
							end = j
							break
						}
					}
					ref := line[start:end]
					if len(ref) > 5 { // Ensure we have more than just "op://"
						refs = append(refs, ref)
					}
					// Move past this reference
					i = end - 1
				}
			}
		}
	}

	return refs
}

// validate1PasswordReference validates the format of a 1Password reference
func validate1PasswordReference(ref string) error {
	if !strings.HasPrefix(ref, "op://") {
		return fmt.Errorf("reference must start with 'op://'")
	}

	// Remove the op:// prefix
	path := strings.TrimPrefix(ref, "op://")
	parts := strings.Split(path, "/")

	// Should have at least vault/item format
	if len(parts) < 2 {
		return fmt.Errorf("reference must have format 'op://vault/item' or 'op://vault/item/field'")
	}

	// Validate vault name
	if parts[0] == "" {
		return fmt.Errorf("vault name cannot be empty")
	}

	// Validate item name
	if parts[1] == "" {
		return fmt.Errorf("item name cannot be empty")
	}

	return nil
}


// validateYAMLSyntax validates YAML syntax by attempting to parse it
func validateYAMLSyntax(content []byte) error {
	var result interface{}
	return yamlv3.Unmarshal(content, &result)
}


// validateResourcesYAML validates the resources YAML content (replaces validateResourcesContent for non-template files)
func validateResourcesYAML(yamlContent string, logger *common.ColorLogger) error {
	logger.Info("Validating resources.yaml content")

	// Validate YAML syntax
	if err := validateYAMLSyntax([]byte(yamlContent)); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Check that expected secrets were created (no namespaces since resources.yaml only contains secrets)
	expectedSecrets := []string{"onepassword-secret", "sops-age", "cloudflare-tunnel-id-secret"}
	for _, secret := range expectedSecrets {
		if !strings.Contains(yamlContent, fmt.Sprintf("name: %s", secret)) {
			return fmt.Errorf("expected secret '%s' not found in YAML content", secret)
		}
	}
	logger.Debug("All expected secrets found in YAML content")

	// Verify that the content contains proper Kubernetes resources
	if !strings.Contains(yamlContent, "apiVersion: v1") {
		return fmt.Errorf("YAML content does not contain valid Kubernetes resources")
	}
	if !strings.Contains(yamlContent, "kind: Secret") {
		return fmt.Errorf("YAML content does not contain Secret resources")
	}

	return nil
}

// validateClusterSecretStoreYAML validates the cluster secret store YAML content (replaces validateClusterSecretStoreContent for non-template files)
func validateClusterSecretStoreYAML(yamlContent string, logger *common.ColorLogger) error {
	logger.Info("Validating clustersecretstore.yaml content")

	// Validate YAML syntax
	if err := validateYAMLSyntax([]byte(yamlContent)); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Check for ClusterSecretStore resource
	if !strings.Contains(yamlContent, "kind: ClusterSecretStore") {
		return fmt.Errorf("YAML content does not contain ClusterSecretStore resource")
	}

	// Check for expected ClusterSecretStore name
	if !strings.Contains(yamlContent, "name: onepassword") {
		return fmt.Errorf("expected ClusterSecretStore 'onepassword' not found in YAML content")
	}

	// Check for 1Password provider configuration
	if !strings.Contains(yamlContent, "onepassword:") {
		return fmt.Errorf("YAML content does not contain 1Password provider configuration")
	}

	logger.Debug("ClusterSecretStore YAML validation passed")
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
		switch result.Status {
		case "PASS":
			logger.Success("✓ %s: %s", result.Name, result.Message)
		case "WARN":
			logger.Warn("⚠ %s: %s", result.Name, result.Message)
		default:
			logger.Error("✗ %s: %s", result.Name, result.Message)
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
	// Validate versions are set (now using hardcoded defaults from templates)
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
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Warn("Failed to close response body: %v", closeErr)
		}
	}()

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
	// Use the unified machineconfig.yaml.j2 template for both controlplane and worker nodes
	baseTemplate := "machineconfig.yaml.j2"
	// Use a sample node template for patch testing
	patchTemplate := "nodes/192.168.122.10.yaml.j2"

	_, err := renderMachineConfigFromEmbedded(baseTemplate, patchTemplate, "controlplane")
	if err != nil {
		return &PreflightResult{
			Name:    "Machine Config Rendering",
			Status:  "FAIL",
			Message: fmt.Sprintf("Failed to render machine config: %v", err),
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

	logger.Info("Found %d Talos nodes to configure", len(nodes))

	// Apply configuration to each node
	var failures []string
	for _, node := range nodes {
		nodeTemplate := fmt.Sprintf("nodes/%s.yaml.j2", node)

		// Get machine type from embedded node template
		machineType, err := getMachineTypeFromEmbedded(nodeTemplate)
		if err != nil {
			logger.Error("Failed to determine machine type for %s: %v", node, err)
			failures = append(failures, node)
			continue
		}

		logger.Debug("Applying Talos configuration to %s (type: %s)", node, machineType)

		// Determine base template
		var baseTemplate string
		switch machineType {
		case "controlplane":
			baseTemplate = "machineconfig.yaml.j2"
		case "worker":
			baseTemplate = "machineconfig.yaml.j2"
		default:
			logger.Error("Unknown machine type for %s: %s", node, machineType)
			failures = append(failures, node)
			continue
		}

		// Render machine config using embedded templates
		renderedConfig, err := renderMachineConfigFromEmbedded(baseTemplate, nodeTemplate, machineType)
		if err != nil {
			logger.Error("Failed to render config for %s: %v", node, err)
			failures = append(failures, node)
			continue
		}

		if config.DryRun {
			logger.Info("[DRY RUN] Would apply config to %s (type: %s)", node, machineType)
			continue
		}

		// Apply the config with retry
		if err := applyNodeConfigWithRetry(node, renderedConfig, logger, 3); err != nil {
			// Check if node is already configured
			if strings.Contains(err.Error(), "certificate required") || strings.Contains(err.Error(), "already configured") {
				logger.Warn("Node %s is already configured, skipping", node)
				continue
			}
			logger.Error("Failed to apply config to %s after retries: %v", node, err)
			failures = append(failures, node)
			continue
		}

		logger.Success("Successfully applied configuration to %s", node)
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
		logger.Warn("Attempt %d/%d to get Talos nodes failed: %v", attempt, maxRetries, err)

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

		logger.Warn("Attempt %d/%d to apply config to %s failed: %v", attempt, maxRetries, node, err)

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

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate, machineType string) ([]byte, error) {
	// Get versions from system-upgrade plans or environment variables
	versionConfig := versionconfig.GetVersions(".")
	env := map[string]string{
		"KUBERNETES_VERSION": getEnvOrDefault("KUBERNETES_VERSION", versionConfig.KubernetesVersion),
		"TALOS_VERSION":      getEnvOrDefault("TALOS_VERSION", versionConfig.TalosVersion),
		"machinetype":        machineType, // Add machine type for Jinja2 conditionals
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

	mergedConfig, err := processor.MergeYAMLMultiDocument([]byte(baseConfig), []byte(patchConfig))
	if err != nil {
		return nil, err
	}

	// Resolve 1Password references in the merged configuration
	// Create a minimal logger for 1Password resolution
	logger := common.NewColorLogger()
	resolvedConfig, err := resolve1PasswordReferences(string(mergedConfig), logger)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in machine config: %w", err)
	}

	// Debug: Check if the resolved config still contains 1Password references
	if strings.Contains(resolvedConfig, "op://") {
		logger.Warn("Warning: Resolved config still contains 1Password references")
		// Find remaining references
		opRefs := extractOnePasswordReferences(resolvedConfig)
		for _, ref := range opRefs {
			logger.Warn("Unresolved 1Password reference: %s", ref)
		}
	}

	return []byte(resolvedConfig), nil
}

func bootstrapTalos(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get a random controller node
	controller, err := getRandomController()
	if err != nil {
		return err
	}

	logger.Debug("Bootstrapping Talos on controller %s", controller)

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

		logger.Info("Bootstrap in progress, waiting 10 seconds... (attempt %d/30)", attempts+1)
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
		logger.Debug("%s", string(output))

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

		logger.Info("Waiting for nodes to become available... (attempt %d/15)", attempts+1)
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
			logger.Debug("%s", string(statusOutput))
		}

		if attempts == 19 {
			return fmt.Errorf("nodes did not become ready after %d attempts (10 minutes)", attempts+1)
		}

		logger.Info("Waiting for nodes to be ready... (attempt %d/20)", attempts+1)
		time.Sleep(30 * time.Second)
	}

	return fmt.Errorf("unexpected error in waitForNodes")
}

func applyCRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Use the new helmfile-based CRD application method
	return applyCRDsFromHelmfile(config, logger)
}

func applyCRDsFromHelmfile(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("Applying CRDs from helmfile...")

	if config.DryRun {
		logger.Info("[DRY RUN] Would apply CRDs from crds/helmfile.yaml")
		return nil
	}

	// Create temporary directory for helmfile execution
	tempDir, err := os.MkdirTemp("", "homeops-crds-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			logger.Warn("Warning: failed to remove temp directory: %v", removeErr)
		}
	}()

	// Get embedded CRDs helmfile content
	crdsHelmfileTemplate, err := templates.GetBootstrapFile("crds/helmfile.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded CRDs helmfile: %w", err)
	}

	// Render the helmfile template with RootDir
	tmpl, err := template.New("crds-helmfile").Parse(crdsHelmfileTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse CRDs helmfile template: %w", err)
	}

	var helmfileContent bytes.Buffer
	templateData := struct {
		RootDir string
	}{
		RootDir: config.RootDir,
	}

	if err := tmpl.Execute(&helmfileContent, templateData); err != nil {
		return fmt.Errorf("failed to render CRDs helmfile template: %w", err)
	}

	// Create temporary file for CRDs helmfile
	crdsHelmfilePath := filepath.Join(tempDir, "crds-helmfile.yaml")
	if err := os.WriteFile(crdsHelmfilePath, helmfileContent.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write CRDs helmfile: %w", err)
	}

	logger.Info("Created CRDs helmfile for template processing")

	// Use helmfile template command to generate CRD manifests
	cmd := exec.Command("helmfile", "--file", crdsHelmfilePath, "template", "--include-crds")
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ROOT_DIR=%s", config.RootDir),
	)

	// Capture the templated output
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to template CRDs from helmfile: %w", err)
	}

	if len(output) == 0 {
		logger.Warn("No CRD manifests generated from helmfile template")
		return nil
	}

	// Apply the templated CRDs using kubectl
	applyCmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-", "--kubeconfig", config.KubeConfig)
	applyCmd.Stdin = bytes.NewReader(output)

	if applyOutput, err := applyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply CRDs: %w\nOutput: %s", err, string(applyOutput))
	}

	logger.Success("CRDs applied successfully from helmfile")
	return nil
}



func applyResources(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get resources from embedded YAML file (no longer a template)
	resources, err := templates.GetBootstrapFile("resources.yaml")
	if err != nil {
		return fmt.Errorf("failed to get resources: %w", err)
	}

	// Resolve 1Password references in the YAML content
	logger.Info("Resolving 1Password references in bootstrap resources...")
	resolvedResources, err := resolve1PasswordReferences(resources, logger)
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
	}

	if config.DryRun {
		// Validate YAML content and 1Password references
		if err := validateResourcesYAML(resolvedResources, logger); err != nil {
			return fmt.Errorf("resources validation failed: %w", err)
		}
		logger.Info("[DRY RUN] Resources validation passed - would apply resources")
		return nil
	}

	// Apply resources with force-conflicts to handle cert-manager managed fields
	cmd := exec.Command("kubectl", "apply", "--server-side", "--force-conflicts", "--filename", "-")
	cmd.Stdin = bytes.NewReader([]byte(resolvedResources))

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply resources: %w\n%s", err, output)
	}

	logger.Info("Resources applied successfully")
	return nil
}

// get1PasswordSecret retrieves a secret from 1Password using the CLI
func get1PasswordSecret(reference string) (string, error) {
	cmd := exec.Command("op", "read", reference)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to read 1Password secret %s: %w", reference, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// resolve1PasswordReferences resolves all 1Password references in the content
func resolve1PasswordReferences(content string, logger *common.ColorLogger) (string, error) {
	// Extract all 1Password references
	opRefs := extractOnePasswordReferences(content)
	if len(opRefs) == 0 {
		logger.Info("No 1Password references found to resolve")
		return content, nil
	}

	logger.Info("Found %d 1Password references to resolve", len(opRefs))
	result := content

	// Resolve each reference
	for _, ref := range opRefs {
		logger.Debug("Resolving 1Password reference: %s", ref)

		// Get the secret value
		secretValue, err := get1PasswordSecret(ref)
		if err != nil {
			return "", fmt.Errorf("failed to resolve 1Password reference '%s': %w", ref, err)
		}

		// Replace the reference with the actual value
		result = strings.ReplaceAll(result, ref, secretValue)
		logger.Debug("Successfully resolved 1Password reference: %s", ref)
	}

	logger.Info("All 1Password references resolved successfully")
	return result, nil
}

func applyClusterSecretStore(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get cluster secret store from embedded YAML file (no longer a template)
	clusterSecretStore, err := templates.GetBootstrapFile("clustersecretstore.yaml")
	if err != nil {
		return fmt.Errorf("failed to get cluster secret store: %w", err)
	}

	// Resolve 1Password references in the YAML content
	logger.Info("Resolving 1Password references in cluster secret store...")
	resolvedClusterSecretStore, err := resolve1PasswordReferences(clusterSecretStore, logger)
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
	}

	if config.DryRun {
		// Validate YAML content and 1Password references
		if err := validateClusterSecretStoreYAML(resolvedClusterSecretStore, logger); err != nil {
			return fmt.Errorf("cluster secret store validation failed: %w", err)
		}
		logger.Info("[DRY RUN] Cluster secret store validation passed - would apply cluster secret store")
		return nil
	}

	// Apply cluster secret store with force-conflicts to handle field management conflicts
	cmd := exec.Command("kubectl", "apply", "--namespace=external-secrets", "--server-side", "--force-conflicts", "--filename", "-", "--wait=true")
	cmd.Stdin = bytes.NewReader([]byte(resolvedClusterSecretStore))

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply cluster secret store: %w\n%s", err, output)
	}

	logger.Info("Cluster secret store applied successfully")
	return nil
}

func syncHelmReleases(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		// Validate clustersecretstore template that would be applied after helmfile
		if err := applyClusterSecretStore(config, logger); err != nil {
			return err
		}
		if err := validateClusterSecretStoreTemplate(logger); err != nil {
			return fmt.Errorf("clustersecretstore template validation failed: %w", err)
		}
		// Test dynamic values template rendering
		if err := testDynamicValuesTemplate(config, logger); err != nil {
			return fmt.Errorf("dynamic values template test failed: %w", err)
		}
		logger.Info("[DRY RUN] Template validation passed - would sync Helm releases")
		return nil
	}

	// Create temporary directory for helmfile execution
	tempDir, err := os.MkdirTemp("", "homeops-bootstrap-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			logger.Warn("Warning: failed to remove temp directory: %v", removeErr)
		}
	}()

	// Create values.yaml.gotmpl in temp directory
	valuesTemplate, err := templates.GetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}

	valuesPath := filepath.Join(tempDir, "values.yaml.gotmpl")
	if err := os.WriteFile(valuesPath, []byte(valuesTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	// Get embedded helmfile content
	helmfileTemplate, err := templates.GetBootstrapFile("helmfile.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded helmfile: %w", err)
	}

	// Render the helmfile template with RootDir and temp directory
	tmpl, err := template.New("helmfile").Parse(helmfileTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse helmfile template: %w", err)
	}

	var helmfileContent bytes.Buffer
	templateData := struct {
		RootDir string
	}{
		RootDir: config.RootDir,
	}

	if err := tmpl.Execute(&helmfileContent, templateData); err != nil {
		return fmt.Errorf("failed to render helmfile template: %w", err)
	}

	// Create temporary file for helmfile
	helmfilePath := filepath.Join(tempDir, "helmfile.yaml")
	if err := os.WriteFile(helmfilePath, helmfileContent.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write helmfile: %w", err)
	}

	logger.Info("Created dynamic helmfile with Go template support")
	logger.Info("Setting working directory to: %s", config.RootDir)

	cmd := exec.Command("helmfile", "--file", helmfilePath, "sync", "--hide-notes")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = tempDir // Set working directory to temp directory with templates

	// Set additional environment variables for helmfile
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("HELMFILE_TEMPLATE_DIR=%s", tempDir),
		fmt.Sprintf("ROOT_DIR=%s", config.RootDir),
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to sync Helm releases: %w", err)
	}

	logger.Info("Helm releases synced successfully")

	// Apply cluster secret store after external-secrets is deployed
	if err := applyClusterSecretStore(config, logger); err != nil {
		return fmt.Errorf("failed to apply cluster secret store: %w", err)
	}

	return nil
}

// testDynamicValuesTemplate tests the dynamic values template rendering
func testDynamicValuesTemplate(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("Testing dynamic values template rendering...")

	// Create metrics collector
	metricsCollector := metrics.NewPerformanceCollector()

	// Test releases to verify template works
	testReleases := []string{"cilium", "coredns", "spegel", "cert-manager", "external-secrets", "flux-operator", "flux-instance"}

	for _, release := range testReleases {
		logger.Debug("Testing values rendering for release: %s", release)

		values, err := templates.RenderHelmfileValues(release, config.RootDir, metricsCollector)
		if err != nil {
			return fmt.Errorf("failed to render values for %s: %w", release, err)
		}

		// Validate that we got some values back (not empty)
		if strings.TrimSpace(values) == "" {
			return fmt.Errorf("rendered values for %s are empty", release)
		}

		logger.Debug("Successfully rendered values for %s (%d characters)", release, len(values))
	}

	logger.Success("Dynamic values template rendering test passed")
	return nil
}
