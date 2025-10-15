package bootstrap

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/ui"
)

type BootstrapConfig struct {
	RootDir       string
	KubeConfig    string
	TalosConfig   string
	K8sVersion    string
	TalosVersion  string
	DryRun        bool
	SkipCRDs      bool
	SkipResources bool
	SkipHelmfile  bool
	SkipPreflight bool
	Verbose       bool
}

type PreflightResult struct {
	Name    string
	Status  string
	Message string
	Error   error
}

// runWithSpinner runs a function with a spinner if verbose mode is disabled,
// otherwise runs it directly with full logger output
func runWithSpinner(title string, verbose bool, logger *common.ColorLogger, fn func() error) error {
	if verbose {
		// In verbose mode, show the title and run without spinner
		logger.Info("%s", title)
		return fn()
	}
	// In normal mode, use spinner and suppress logger output
	return ui.SpinWithFunc(title, func() error {
		logger.Quiet = true
		defer func() { logger.Quiet = false }()
		return fn()
	})
}

// buildTalosctlCmd builds a talosctl command with optional talosconfig
func buildTalosctlCmd(talosConfig string, args ...string) *exec.Cmd {
	if talosConfig != "" {
		cmdArgs := append([]string{"--talosconfig", talosConfig}, args...)
		return exec.Command("talosctl", cmdArgs...)
	}
	return exec.Command("talosctl", args...)
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

	// Add flags - default root-dir to git repository root
	defaultRootDir := common.GetWorkingDirectory()
	cmd.Flags().StringVar(&config.RootDir, "root-dir", defaultRootDir, "Root directory of the project")
	cmd.Flags().StringVar(&config.KubeConfig, "kubeconfig", os.Getenv("KUBECONFIG"), "Path to kubeconfig file")
	cmd.Flags().StringVar(&config.TalosConfig, "talosconfig", os.Getenv("TALOSCONFIG"), "Path to talosconfig file")
	cmd.Flags().StringVar(&config.K8sVersion, "k8s-version", os.Getenv("KUBERNETES_VERSION"), "Kubernetes version")
	cmd.Flags().StringVar(&config.TalosVersion, "talos-version", os.Getenv("TALOS_VERSION"), "Talos version")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Perform a dry run without making changes")
	cmd.Flags().BoolVar(&config.SkipCRDs, "skip-crds", false, "Skip CRD installation")
	cmd.Flags().BoolVar(&config.SkipResources, "skip-resources", false, "Skip resource creation")
	cmd.Flags().BoolVar(&config.SkipHelmfile, "skip-helmfile", false, "Skip Helmfile sync")
	cmd.Flags().BoolVar(&config.SkipPreflight, "skip-preflight", false, "Skip preflight checks (not recommended)")
	cmd.Flags().BoolVarP(&config.Verbose, "verbose", "v", false, "Enable verbose output (shows all logs, disables spinners)")

	return cmd
}

func promptBootstrapOptions(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 1: Ask if this is a dry-run
	dryRunOptions := []string{
		"Real Bootstrap - Actually perform the bootstrap",
		"Dry-Run - Preview what would be done without making changes",
	}

	selectedMode, err := ui.Choose("Select bootstrap mode:", dryRunOptions)
	if err != nil {
		// User cancelled
		return fmt.Errorf("bootstrap cancelled")
	}

	if strings.HasPrefix(selectedMode, "Dry-Run") {
		config.DryRun = true
		logger.Info("🔍 Dry-run mode enabled - no changes will be made")
	}

	// Step 2: Ask what to include/skip (multi-select)
	// Show options regardless of dry-run or real
	skipOptions := []string{
		"Skip Preflight Checks",
		"Skip CRDs",
		"Skip Resources",
		"Skip Helmfile",
		"Enable Verbose Mode",
	}

	selectedOptions, err := ui.ChooseMulti("Select options to customize (use 'x' to toggle, Enter to confirm - or just press Enter for full bootstrap):", skipOptions, 0)
	if err != nil {
		// User cancelled or error
		return fmt.Errorf("options selection cancelled")
	}

	// Apply selected options
	for _, option := range selectedOptions {
		switch {
		case strings.HasPrefix(option, "Skip Preflight"):
			config.SkipPreflight = true
			logger.Warn("⚠️  Skipping preflight checks")
		case strings.HasPrefix(option, "Skip CRDs"):
			config.SkipCRDs = true
			logger.Info("📋 Skipping CRD installation")
		case strings.HasPrefix(option, "Skip Resources"):
			config.SkipResources = true
			logger.Info("📦 Skipping resource creation")
		case strings.HasPrefix(option, "Skip Helmfile"):
			config.SkipHelmfile = true
			logger.Info("⚙️  Skipping Helmfile sync")
		case strings.HasPrefix(option, "Enable Verbose"):
			config.Verbose = true
			logger.Info("📢 Verbose mode enabled")
		}
	}

	// Show summary of what will be done
	if config.DryRun {
		logger.Info("📋 Summary: Dry-run mode with selected skips")
	} else if len(selectedOptions) == 0 {
		logger.Info("🚀 Summary: Full bootstrap - all steps will be performed")
	} else {
		logger.Info("🎯 Summary: Real bootstrap with %d step(s) skipped", len(selectedOptions))
	}

	return nil
}

// resetTerminal resets terminal state to prevent escape code leakage
func resetTerminal() {
	// Run stty sane to fully restore terminal
	resetCmd := exec.Command("stty", "sane")
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		resetCmd.Stdin = tty
		resetCmd.Stdout = tty
		resetCmd.Stderr = tty
		_ = resetCmd.Run()

		// Also send ANSI reset codes
		_, _ = tty.WriteString("\033[0m\033[?25h\r")
		_ = tty.Sync()
		_ = tty.Close()
	}

	// Small delay to let terminal settle
	time.Sleep(50 * time.Millisecond)
}

func runBootstrap(config *BootstrapConfig) error {
	// Initialize logger with colors
	logger := common.NewColorLogger()

	// If no flags were set, show interactive menus
	if !config.DryRun && !config.SkipCRDs && !config.SkipResources && !config.SkipHelmfile && !config.SkipPreflight && !config.Verbose {
		if err := promptBootstrapOptions(config, logger); err != nil {
			return err
		}
	}

	logger.Info("🚀 Starting cluster bootstrap process")

	// Convert RootDir to absolute path if it's relative
	if !filepath.IsAbs(config.RootDir) {
		absPath, err := filepath.Abs(config.RootDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for root directory: %w", err)
		}
		config.RootDir = absPath
	}

	// Kubeconfig and talosconfig are now expected to be absolute paths from environment variables
	// No need to resolve them relative to root directory

	logger.Debug("Using kubeconfig: %s", config.KubeConfig)
	logger.Debug("Using talosconfig: %s", config.TalosConfig)

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
		if err := runWithSpinner("🔍 Running preflight checks", config.Verbose, logger, func() error {
			return runPreflightChecks(config, logger)
		}); err != nil {
			return fmt.Errorf("preflight checks failed: %w", err)
		}
	} else {
		logger.Warn("⚠️  Skipping preflight checks - this may cause failures during bootstrap")
		// Still run basic prerequisite validation
		if err := validatePrerequisites(config); err != nil {
			return fmt.Errorf("prerequisite validation failed: %w", err)
		}
	}

	// Step 1: Apply Talos configuration
	logger.Info("📋 Step 1: Applying Talos configuration to nodes")
	if err := applyTalosConfig(config, logger); err != nil {
		return fmt.Errorf("failed to apply Talos config: %w", err)
	}

	// Reset terminal after Step 1 completes (multiple spinners)
	resetTerminal()

	// Step 2: Bootstrap Talos
	if err := runWithSpinner("🎯 Step 2: Bootstrapping Talos cluster", config.Verbose, logger, func() error {
		// Wait a moment for configurations to be fully processed (following onedr0p's pattern)
		logger.Debug("Waiting for configurations to be processed...")
		time.Sleep(5 * time.Second)
		return bootstrapTalos(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to bootstrap Talos: %w", err)
	}

	// Step 3: Fetch kubeconfig
	if err := runWithSpinner("🔑 Step 3: Fetching and validating kubeconfig", config.Verbose, logger, func() error {
		if err := fetchKubeconfig(config, logger); err != nil {
			return fmt.Errorf("failed to fetch kubeconfig: %w", err)
		}
		// Validate kubeconfig is working
		if err := validateKubeconfig(config, logger); err != nil {
			return fmt.Errorf("kubeconfig validation failed: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	// Step 4: Wait for nodes to be ready
	if err := runWithSpinner("⏳ Step 4: Waiting for nodes to be ready", config.Verbose, logger, func() error {
		return waitForNodes(config, logger)
	}); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}

	// Step 5: Apply namespaces first (following onedr0p pattern)
	if err := runWithSpinner("📦 Step 5: Creating initial namespaces", config.Verbose, logger, func() error {
		return applyNamespaces(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to apply namespaces: %w", err)
	}

	// Step 6: Apply initial resources
	if !config.SkipResources {
		if err := runWithSpinner("🔧 Step 6: Applying initial resources", config.Verbose, logger, func() error {
			return applyResources(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}

	// Step 7: Apply CRDs
	if !config.SkipCRDs {
		if err := runWithSpinner("📜 Step 7: Applying Custom Resource Definitions", config.Verbose, logger, func() error {
			return applyCRDs(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}

	// Step 8: Sync Helm releases
	if !config.SkipHelmfile {
		if err := runWithSpinner("⚙️  Step 8: Syncing Helm releases", config.Verbose, logger, func() error {
			return syncHelmReleases(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}

	logger.Success("🎉 Congrats! The cluster is bootstrapped and Flux is syncing the Git repository")

	// Explicitly reset terminal state to prevent escape code leakage
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err == nil {
		// Send ANSI reset sequences directly to TTY
		// \033[0m - Reset all attributes
		// \033[?25h - Show cursor
		// \r\n - Clear line and move to next
		_, _ = tty.WriteString("\033[0m\033[?25h\r\n")
		_ = tty.Close()
	} else {
		// Fallback if we can't open TTY
		fmt.Println()
	}

	return nil
}

// validateKubeconfig validates that the kubeconfig is working and cluster is accessible
func validateKubeconfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would validate kubeconfig")
		return nil
	}

	logger.Info("Waiting for cluster API server to be ready...")

	maxAttempts := 12 // 2 minutes total with exponential backoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		logger.Debug("Kubeconfig validation attempt %d/%d", attempt, maxAttempts)

		// Test cluster connectivity with timeout
		cmd := exec.Command("kubectl", "cluster-info", "--kubeconfig", config.KubeConfig, "--request-timeout=10s")
		if err := cmd.Run(); err == nil {
			// If cluster-info succeeds, test node accessibility
			cmd = exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig, "--request-timeout=10s")
			if err := cmd.Run(); err == nil {
				logger.Debug("Kubeconfig validation passed - cluster is accessible")
				return nil
			}
			logger.Debug("Cluster connectivity OK, but nodes not ready yet")
		} else {
			logger.Debug("Cluster connectivity not ready yet")
		}

		// Don't wait after the last attempt
		if attempt < maxAttempts {
			waitTime := time.Duration(attempt*5) * time.Second // 5s, 10s, 15s, etc.
			logger.Debug("Waiting %v before next attempt...", waitTime)
			time.Sleep(waitTime)
		}
	}

	return fmt.Errorf("cluster did not become ready after %d attempts over 2+ minutes", maxAttempts)
}

// applyNamespaces creates the initial namespaces required for bootstrap
func applyNamespaces(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would apply initial namespaces")
		return nil
	}

	// Define all namespaces used in the cluster
	// This ensures all namespaces exist before any resources are applied
	namespaces := []string{
		"actions-runner-system",
		"cert-manager",
		"external-secrets",
		"flux-system",
		"kube-system", // Usually exists but we'll ensure it's there
		"longhorn-system",
		"network",
		"observability",
		"openebs-system",
		"system",
		"system-upgrade",
		"volsync-system",
	}

	for _, ns := range namespaces {
		logger.Debug("Creating namespace: %s", ns)

		cmd := exec.Command("kubectl", "create", "namespace", ns,
			"--kubeconfig", config.KubeConfig, "--dry-run=client", "-o", "yaml")
		manifestOutput, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to generate namespace manifest for %s: %w", ns, err)
		}

		applyCmd := exec.Command("kubectl", "apply", "--kubeconfig", config.KubeConfig,
			"--filename", "-")
		applyCmd.Stdin = bytes.NewReader(manifestOutput)

		if output, err := applyCmd.CombinedOutput(); err != nil {
			// Check if namespace already exists - that's okay
			if strings.Contains(string(output), "AlreadyExists") || strings.Contains(string(output), "unchanged") {
				logger.Debug("Namespace %s already exists", ns)
				continue
			}
			return fmt.Errorf("failed to create namespace %s: %w\nOutput: %s", ns, err, string(output))
		}

		logger.Debug("Successfully created namespace: %s", ns)
	}

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
						if line[j] == ' ' || line[j] == '"' || line[j] == '\'' || line[j] == '}' || line[j] == ',' || line[j] == '\n' || line[j] == '\t' || line[j] == ':' {
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

	// Deduplicate references
	seen := make(map[string]bool)
	var uniqueRefs []string
	for _, ref := range refs {
		if !seen[ref] {
			seen[ref] = true
			uniqueRefs = append(uniqueRefs, ref)
		}
	}

	return uniqueRefs
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

	// If field is specified, validate it's not empty
	if len(parts) >= 3 && parts[2] == "" {
		return fmt.Errorf("field name cannot be empty")
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
	// Skip empty talosconfig path (uses default)
	if config.TalosConfig != "" {
		if _, err := os.Stat(config.TalosConfig); os.IsNotExist(err) {
			return fmt.Errorf("required file '%s' not found", config.TalosConfig)
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

	// Check talosconfig file (only if specified)
	if config.TalosConfig != "" {
		if _, err := os.Stat(config.TalosConfig); os.IsNotExist(err) {
			return &PreflightResult{
				Name:    "Environment Files",
				Status:  "FAIL",
				Message: fmt.Sprintf("talosconfig file not found: %s", config.TalosConfig),
			}
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
	if err := common.Ensure1PasswordAuth(); err != nil {
		return &PreflightResult{
			Name:    "1Password Authentication",
			Status:  "FAIL",
			Message: fmt.Sprintf("1Password authentication failed: %v", err),
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
	// Use a sample node template for patch testing
	patchTemplate := "nodes/192.168.122.10.yaml"

	_, err := renderMachineConfigFromEmbedded("controlplane.yaml", patchTemplate, "controlplane", logger)
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
	nodes, err := getTalosNodes(config.TalosConfig)
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

// Removed local check1PasswordAuth in favor of common.Ensure1PasswordAuth

func applyTalosConfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get list of nodes from talosctl config with retry
	nodes, err := getTalosNodesWithRetry(config.TalosConfig, logger, 3)
	if err != nil {
		return err
	}

	logger.Info("Found %d Talos nodes to configure", len(nodes))

	// Apply configuration to each node
	var failures []string
	for _, node := range nodes {
		nodeTemplate := fmt.Sprintf("nodes/%s.yaml", node)

		// Get machine type from embedded node template - do this outside spinner for better error messages
		machineType, err := getMachineTypeFromEmbedded(nodeTemplate)
		if err != nil {
			logger.Error("Failed to determine machine type for %s: %v", node, err)
			failures = append(failures, node)
			continue
		}

		// Determine base template
		var baseTemplate string
		switch machineType {
		case "controlplane":
			baseTemplate = "controlplane.yaml"
		case "worker":
			baseTemplate = "worker.yaml"
		default:
			logger.Error("Unknown machine type for %s: %s", node, machineType)
			failures = append(failures, node)
			continue
		}

		// Apply config with spinner showing the node being configured
		spinnerTitle := fmt.Sprintf("  Applying config to %s (%s)", node, machineType)
		err = runWithSpinner(spinnerTitle, config.Verbose, logger, func() error {
			// Render machine config using embedded templates
			renderedConfig, err := renderMachineConfigFromEmbedded(baseTemplate, nodeTemplate, machineType, logger)
			if err != nil {
				return fmt.Errorf("failed to render config: %w", err)
			}

			if config.DryRun {
				// For dry-run, just simulate a brief delay so spinner is visible
				time.Sleep(500 * time.Millisecond)
				return nil
			}

			// Apply the config with retry
			if err := applyNodeConfigWithRetry(node, renderedConfig, logger, 3); err != nil {
				// Check if node is already configured
				if strings.Contains(err.Error(), "certificate required") || strings.Contains(err.Error(), "already configured") {
					return nil // Silent skip for already configured nodes
				}
				return fmt.Errorf("failed to apply config after retries: %w", err)
			}

			return nil
		})

		if err != nil {
			logger.Error("Failed to configure %s: %v", node, err)
			failures = append(failures, node)
			continue
		}

		if config.DryRun {
			logger.Info("[DRY RUN] Would apply config to %s (type: %s)", node, machineType)
		} else {
			logger.Success("Successfully applied configuration to %s", node)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("failed to configure nodes: %s", strings.Join(failures, ", "))
	}

	return nil
}

func getTalosNodes(talosConfig string) ([]string, error) {
	cmd := buildTalosctlCmd(talosConfig, "config", "info", "--output", "json")
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

func getTalosNodesWithRetry(talosConfig string, logger *common.ColorLogger, maxRetries int) ([]string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		nodes, err := getTalosNodes(talosConfig)
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

func getMachineTypeFromEmbedded(nodeTemplate string) (string, error) {
	// Get the node template content with proper talos/ prefix
	fullTemplatePath := fmt.Sprintf("talos/%s", nodeTemplate)
	content, err := templates.GetTalosTemplate(fullTemplatePath)
	if err != nil {
		return "", fmt.Errorf("failed to get node template: %w", err)
	}

	// Parse machine type from template content
	// Check for explicit type field
	if strings.Contains(content, "type: worker") {
		return "worker", nil
	}

	// If node has VIP configuration, it's a controlplane node
	if strings.Contains(content, "vip:") {
		return "controlplane", nil
	}

	// Default to controlplane since all nodes in this cluster are controlplane
	// (can be changed to "worker" if worker nodes are added later)
	return "controlplane", nil
}

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate, machineType string, logger *common.ColorLogger) ([]byte, error) {
	// Get base config from embedded YAML file with proper talos/ prefix
	fullBaseTemplatePath := fmt.Sprintf("talos/%s", baseTemplate)
	baseConfig, err := templates.GetTalosTemplate(fullBaseTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get base config: %w", err)
	}

	// Get patch config from embedded YAML file with proper talos/ prefix
	fullPatchTemplatePath := fmt.Sprintf("talos/%s", patchTemplate)
	patchConfig, err := templates.GetTalosTemplate(fullPatchTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get patch config: %w", err)
	}

	// Trim leading document separator if present
	patchConfigTrimmed := strings.TrimPrefix(patchConfig, "---\n")
	patchConfigTrimmed = strings.TrimPrefix(patchConfigTrimmed, "---\r\n")

	// Now split by document separators
	var patchParts []string
	if strings.Contains(patchConfigTrimmed, "\n---\n") {
		patchParts = strings.Split(patchConfigTrimmed, "\n---\n")
	} else if strings.Contains(patchConfigTrimmed, "\n---") {
		patchParts = strings.Split(patchConfigTrimmed, "\n---")
	} else {
		patchParts = []string{patchConfigTrimmed}
	}

	// The first part should be the machine config
	machineConfigPatch := strings.TrimSpace(patchParts[0])

	// Ensure the machine config patch starts with proper YAML
	if !strings.HasPrefix(machineConfigPatch, "machine:") && !strings.HasPrefix(machineConfigPatch, "version:") {
		return nil, fmt.Errorf("machine config patch does not start with valid Talos config")
	}

	// Ensure the patch has proper Talos config structure
	if !strings.Contains(machineConfigPatch, "version:") {
		machineConfigPatch = "version: v1alpha1\n" + machineConfigPatch
	}

	// Resolve 1Password references in both configs BEFORE merging
	// Use the logger passed in (which can be in quiet mode during spinners)

	resolvedBaseConfig, err := resolve1PasswordReferences(baseConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in base config: %w", err)
	}

	resolvedPatchConfig, err := resolve1PasswordReferences(machineConfigPatch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in patch config: %w", err)
	}

	// Use talosctl for merging resolved configs (following proven patterns)
	mergedConfig, err := mergeConfigsWithTalosctl([]byte(resolvedBaseConfig), []byte(resolvedPatchConfig))
	if err != nil {
		return nil, fmt.Errorf("failed to merge configs with talosctl: %w", err)
	}

	// If there are additional YAML documents in the patch (UserVolumeConfig, etc.), append them
	var resolvedConfig string
	if len(patchParts) > 1 {
		// Collect additional documents (skip the first one which is the machine config)
		additionalDocs := patchParts[1:]
		additionalParts := strings.Join(additionalDocs, "\n---\n")

		// Resolve 1Password references in additional parts too
		resolvedAdditionalParts, err := resolve1PasswordReferences(additionalParts, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve 1Password references in additional config parts: %w", err)
		}
		resolvedConfig = string(mergedConfig) + "\n---\n" + resolvedAdditionalParts
	} else {
		resolvedConfig = string(mergedConfig)
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

// mergeConfigsWithTalosctl merges base and patch configs using talosctl (onedr0p approach)
func mergeConfigsWithTalosctl(baseConfig, patchConfig []byte) ([]byte, error) {
	// Create temporary files for talosctl processing
	baseFile, err := os.CreateTemp("", "talos-base-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create base config temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(baseFile.Name()) // Ignore cleanup errors
	}()
	defer func() {
		_ = baseFile.Close() // Ignore cleanup errors
	}()

	patchFile, err := os.CreateTemp("", "talos-patch-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create patch config temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(patchFile.Name()) // Ignore cleanup errors
	}()
	defer func() {
		_ = patchFile.Close() // Ignore cleanup errors
	}()

	// Write configs to temp files
	if _, err := baseFile.Write(baseConfig); err != nil {
		return nil, fmt.Errorf("failed to write base config: %w", err)
	}
	if _, err := patchFile.Write(patchConfig); err != nil {
		return nil, fmt.Errorf("failed to write patch config: %w", err)
	}

	// Close files so talosctl can read them
	if err := baseFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close base file: %w", err)
	}
	if err := patchFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close patch file: %w", err)
	}

	// Use talosctl to merge configurations
	cmd := exec.Command("talosctl", "machineconfig", "patch", baseFile.Name(), "--patch", "@"+patchFile.Name())

	mergedConfig, err := cmd.Output()
	if err != nil {
		// Get stderr for better error reporting
		if exitError, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("talosctl config merge failed: %w\nStderr: %s", err, string(exitError.Stderr))
		}
		return nil, fmt.Errorf("talosctl config merge failed: %w", err)
	}

	return mergedConfig, nil
}

// validateEtcdRunning validates that etcd is actually running after bootstrap
func validateEtcdRunning(talosConfig, controller string, logger *common.ColorLogger) error {
	maxAttempts := 6 // 1 minute total with 10s intervals
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		logger.Debug("Etcd validation attempt %d/%d", attempt, maxAttempts)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "talosctl", "--talosconfig", talosConfig, "--nodes", controller, "etcd", "status")
		_, err := cmd.CombinedOutput()

		if err == nil {
			logger.Debug("Etcd is running and responding")
			return nil
		}

		// Check if etcd service is actually running (not just waiting)
		serviceCmd := exec.Command("talosctl", "--talosconfig", talosConfig, "--nodes", controller, "service", "etcd")
		serviceOutput, serviceErr := serviceCmd.Output()
		if serviceErr == nil && strings.Contains(string(serviceOutput), "STATE    Running") {
			logger.Debug("Etcd service is running")
			return nil
		}

		logger.Debug("Etcd validation attempt %d failed: %v", attempt, err)
		if attempt < maxAttempts {
			time.Sleep(10 * time.Second)
		}
	}

	return fmt.Errorf("etcd failed to start after bootstrap")
}

func bootstrapTalos(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get a random controller node
	controller, err := getRandomController(config.TalosConfig)
	if err != nil {
		return fmt.Errorf("failed to get controller node for bootstrap: %w", err)
	}

	logger.Debug("Bootstrapping Talos on controller %s", controller)

	if config.DryRun {
		logger.Info("[DRY RUN] Would bootstrap Talos on controller %s", controller)
		return nil
	}

	// Check if cluster is already bootstrapped first with timeout
	logger.Debug("Checking if cluster is already bootstrapped...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	checkCmd := exec.CommandContext(ctx, "talosctl", "--talosconfig", config.TalosConfig, "--nodes", controller, "etcd", "status")
	if checkOutput, checkErr := checkCmd.CombinedOutput(); checkErr == nil {
		logger.Info("Talos cluster is already bootstrapped (etcd is running)")
		return nil
	} else {
		logger.Debug("Cluster not bootstrapped yet: %s", string(checkOutput))
	}

	// Bootstrap with retry logic similar to onedr0p's implementation
	maxAttempts := 10
	for attempts := 0; attempts < maxAttempts; attempts++ {
		logger.Debug("Bootstrap attempt %d/%d on controller %s", attempts+1, maxAttempts, controller)

		cmd := buildTalosctlCmd(config.TalosConfig, "--nodes", controller, "bootstrap")
		output, err := cmd.CombinedOutput()
		outputStr := string(output)

		// Success cases (following onedr0p's logic)
		if err == nil {
			// Validate that etcd actually started after bootstrap
			logger.Debug("Bootstrap command succeeded, validating etcd started...")
			if err := validateEtcdRunning(config.TalosConfig, controller, logger); err != nil {
				logger.Debug("Bootstrap succeeded but etcd validation failed: %v", err)
				if attempts < maxAttempts-1 {
					logger.Debug("Waiting 10 seconds before next bootstrap attempt")
					time.Sleep(10 * time.Second)
					continue
				}
				return fmt.Errorf("bootstrap command succeeded but etcd failed to start: %w", err)
			}
			logger.Success("Talos cluster bootstrapped successfully")
			return nil
		}

		// Handle "AlreadyExists" as success (like onedr0p does)
		if strings.Contains(outputStr, "AlreadyExists") ||
			strings.Contains(outputStr, "already exists") ||
			strings.Contains(outputStr, "cluster is already initialized") {
			logger.Info("Bootstrap already exists - cluster is already initialized")
			return nil
		}

		logger.Debug("Bootstrap attempt %d failed: %v", attempts+1, err)
		logger.Debug("Bootstrap output: %s", outputStr)

		// Wait 5 seconds between attempts (matching onedr0p's timing)
		if attempts < maxAttempts-1 {
			logger.Debug("Waiting 5 seconds before next bootstrap attempt")
			time.Sleep(5 * time.Second)
		}

	}

	return fmt.Errorf("failed to bootstrap controller after %d attempts", maxAttempts)
}

func getRandomController(talosConfig string) (string, error) {
	cmd := buildTalosctlCmd(talosConfig, "config", "info", "--output", "json")
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
	controller, err := getRandomController(config.TalosConfig)
	if err != nil {
		return fmt.Errorf("failed to get controller node for kubeconfig: %w", err)
	}

	if config.DryRun {
		logger.Info("[DRY RUN] Would fetch kubeconfig from controller %s", controller)
		return nil
	}

	logger.Debug("Fetching kubeconfig from controller %s", controller)

	// Ensure directory exists for kubeconfig
	kubeconfigDir := filepath.Dir(config.KubeConfig)
	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		return fmt.Errorf("failed to create kubeconfig directory %s: %w", kubeconfigDir, err)
	}

	// Try to fetch kubeconfig with retry logic
	maxAttempts := 10
	for attempts := 0; attempts < maxAttempts; attempts++ {
		logger.Debug("Kubeconfig fetch attempt %d/%d", attempts+1, maxAttempts)

		cmd := exec.Command("talosctl", "--talosconfig", config.TalosConfig, "kubeconfig", "--nodes", controller,
			"--force", "--force-context-name", "home-ops-cluster", config.KubeConfig)

		output, err := cmd.CombinedOutput()
		if err == nil {
			logger.Debug("Kubeconfig fetched successfully")

			// Verify the kubeconfig file was created and is readable
			if _, statErr := os.Stat(config.KubeConfig); statErr != nil {
				return fmt.Errorf("kubeconfig file was not created at %s: %w", config.KubeConfig, statErr)
			}

			// Quick validation that the kubeconfig contains expected content
			kubeconfigContent, readErr := os.ReadFile(config.KubeConfig)
			if readErr != nil {
				return fmt.Errorf("failed to read kubeconfig file: %w", readErr)
			}

			if !strings.Contains(string(kubeconfigContent), "apiVersion: v1") ||
				!strings.Contains(string(kubeconfigContent), "kind: Config") {
				return fmt.Errorf("kubeconfig file does not contain valid Kubernetes configuration")
			}

			// Save kubeconfig to 1Password for chezmoi
			if err := saveKubeconfigTo1Password(kubeconfigContent, logger); err != nil {
				logger.Warn("Failed to save kubeconfig to 1Password: %v", err)
				logger.Warn("Continuing with bootstrap - kubeconfig is available locally")
			} else {
				logger.Success("Kubeconfig saved to 1Password for chezmoi")
			}

			logger.Success("Kubeconfig fetched and validated successfully")
			return nil
		}

		outputStr := string(output)
		if strings.Contains(outputStr, "connection refused") {
			logger.Warn("Kubeconfig fetch attempt %d/%d: Controller not ready", attempts+1, maxAttempts)
		} else if strings.Contains(outputStr, "timeout") {
			logger.Warn("Kubeconfig fetch attempt %d/%d: Timeout", attempts+1, maxAttempts)
		} else {
			logger.Warn("Kubeconfig fetch attempt %d/%d failed: %s", attempts+1, maxAttempts, strings.TrimSpace(outputStr))
		}

		if attempts == maxAttempts-1 {
			return fmt.Errorf("failed to fetch kubeconfig after %d attempts. Last error: %s", maxAttempts, outputStr)
		}

		// Wait before retry
		waitTime := time.Duration(attempts+1) * 5 * time.Second
		logger.Debug("Waiting %v before next kubeconfig fetch attempt...", waitTime)
		time.Sleep(waitTime)
	}

	return fmt.Errorf("unexpected error in kubeconfig fetch loop")
}

func waitForNodes(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would wait for nodes to be ready")
		return nil
	}

	// First, wait for nodes to appear
	logger.Info("Waiting for nodes to become available...")
	if err := waitForNodesAvailable(config, logger); err != nil {
		return err
	}

	// Check if nodes are already ready (re-bootstrap scenario)
	if ready, err := checkIfNodesReady(config, logger); err != nil {
		return fmt.Errorf("failed to check node readiness: %w", err)
	} else if ready {
		logger.Success("Nodes are already ready (CNI likely already installed)")
		return nil
	}

	// Wait for nodes to be in Ready=False state (fresh bootstrap sequence)
	logger.Info("Waiting for nodes to be in 'Ready=False' state...")
	if err := waitForNodesReadyFalse(config, logger); err != nil {
		return err
	}

	return nil
}

// checkIfNodesReady checks if nodes are already in Ready=True state
func checkIfNodesReady(config *BootstrapConfig, logger *common.ColorLogger) (bool, error) {
	cmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig,
		"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type=='Ready')].status}{\"\\n\"}{end}")

	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check node ready status: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	allReady := true
	readyCount := 0
	totalNodes := 0

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) == 2 {
			totalNodes++
			nodeName := parts[0]
			readyStatus := parts[1]
			if readyStatus == "True" {
				readyCount++
				logger.Debug("Node %s is Ready=True", nodeName)
			} else {
				logger.Debug("Node %s is Ready=%s", nodeName, readyStatus)
				allReady = false
			}
		}
	}

	if allReady && readyCount > 0 {
		logger.Info("All %d nodes are already Ready=True", readyCount)
		return true, nil
	}

	logger.Debug("Nodes not all ready yet: %d/%d ready", readyCount, totalNodes)
	return false, nil
}

func waitForNodesAvailable(config *BootstrapConfig, logger *common.ColorLogger) error {
	maxAttempts := 30 // 10 minutes
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig,
			"--output=jsonpath={.items[*].metadata.name}", "--no-headers")

		output, err := cmd.Output()
		if err != nil {
			if attempt%3 == 0 { // Log every minute
				logger.Debug("Attempt %d/%d: Nodes not yet available", attempt, maxAttempts)
			}
			if attempt == maxAttempts {
				return fmt.Errorf("nodes not available after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(20 * time.Second)
			continue
		}

		nodeNames := strings.Fields(strings.TrimSpace(string(output)))
		if len(nodeNames) > 0 {
			logger.Success("Found %d nodes: %v", len(nodeNames), nodeNames)
			return nil
		}

		if attempt%3 == 0 { // Log every minute
			logger.Debug("Attempt %d/%d: No nodes found yet", attempt, maxAttempts)
		}
		time.Sleep(20 * time.Second)
	}

	return fmt.Errorf("no nodes found after %d attempts", maxAttempts)
}

func waitForNodesReadyFalse(config *BootstrapConfig, logger *common.ColorLogger) error {
	maxAttempts := 60 // 20 minutes
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("kubectl", "get", "nodes", "--kubeconfig", config.KubeConfig,
			"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type==\"Ready\")].status}{\"\\n\"}{end}")

		output, err := cmd.Output()
		if err != nil {
			if attempt%3 == 0 { // Log every minute
				logger.Debug("Attempt %d/%d: Failed to check node ready status", attempt, maxAttempts)
			}
			if attempt == maxAttempts {
				return fmt.Errorf("failed to check node ready status after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(20 * time.Second)
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		allReadyFalse := true
		readyFalseCount := 0
		totalNodes := 0

		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				totalNodes++
				nodeName := parts[0]
				readyStatus := parts[1]
				if readyStatus == "False" {
					readyFalseCount++
					logger.Debug("Node %s is Ready=False", nodeName)
				} else {
					logger.Debug("Node %s is Ready=%s", nodeName, readyStatus)
					allReadyFalse = false
				}
			}
		}

		if allReadyFalse && readyFalseCount > 0 {
			logger.Success("All %d nodes are in Ready=False state", readyFalseCount)
			return nil
		}

		if attempt%3 == 0 { // Log every minute
			logger.Info("Attempt %d/%d: %d/%d nodes Ready=False, waiting for all nodes...",
				attempt, maxAttempts, readyFalseCount, totalNodes)
		}
		time.Sleep(20 * time.Second)
	}

	return fmt.Errorf("nodes did not reach Ready=False state after %d attempts", maxAttempts)
}

func applyCRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Use the new helmfile-based CRD application method
	return applyCRDsFromHelmfile(config, logger)
}

func applyCRDsFromHelmfile(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("Applying CRDs from dedicated helmfile...")

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
	crdsHelmfileTemplate, err := templates.GetBootstrapFile("helmfile.d/00-crds.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded CRDs helmfile: %w", err)
	}

	// The CRDs helmfile doesn't need templating, write it directly
	crdsHelmfilePath := filepath.Join(tempDir, "00-crds.yaml")
	if err := os.WriteFile(crdsHelmfilePath, []byte(crdsHelmfileTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write CRDs helmfile: %w", err)
	}

	logger.Info("Using dedicated CRDs helmfile to extract CRDs only")

	// Use helmfile template to generate CRDs only (the helmfile has --include-crds but we filter to CRDs)
	cmd := exec.Command("helmfile", "--file", crdsHelmfilePath, "template")
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
		logger.Warn("No manifests generated from CRDs helmfile template")
		return nil
	}

	// Extract only the CRDs from the output
	crdManifests, otherManifests, err := separateCRDsFromManifests(string(output))
	if err != nil {
		return fmt.Errorf("failed to separate CRDs from manifests: %w", err)
	}

	if len(otherManifests) > 0 {
		logger.Debug("Found %d non-CRD resources in CRDs helmfile output, ignoring them", len(otherManifests))
	}

	// Apply only the CRDs
	if len(crdManifests) > 0 {
		logger.Info("Applying %d CRDs...", len(crdManifests))
		crdYaml := strings.Join(crdManifests, "\n---\n")

		applyCmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-", "--kubeconfig", config.KubeConfig)
		applyCmd.Stdin = bytes.NewReader([]byte(crdYaml))

		if applyOutput, err := applyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w\nOutput: %s", err, string(applyOutput))
		}

		logger.Info("CRDs applied, waiting for them to be established...")

		// Wait for CRDs to be established
		if err := waitForCRDsEstablished(config, logger); err != nil {
			return fmt.Errorf("CRDs failed to be established: %w", err)
		}
	} else {
		logger.Warn("No CRDs found in helmfile template output")
	}

	logger.Success("CRDs applied and established successfully")
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

// get1PasswordSecret retrieves a secret from 1Password using the shared common function
func get1PasswordSecret(reference string) (string, error) {
	return common.Get1PasswordSecret(reference)
}

// resolve1PasswordReferences resolves all 1Password references in the content
func resolve1PasswordReferences(content string, logger *common.ColorLogger) (string, error) {
	// Debug: Show lines containing the problematic secret before resolution
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "secretboxEncryptionSecret") {
			logger.Debug("Line %d with secretboxEncryptionSecret: '%s'", i+1, line)
		}
	}

	// Fast path: if no references present, return as-is
	opRefs := extractOnePasswordReferences(content)
	if len(opRefs) == 0 {
		logger.Info("No 1Password references found to resolve")
		return content, nil
	}
	logger.Info("Found %d 1Password references to resolve", len(opRefs))

	// Use the shared, collision-safe injector so we don’t corrupt secrets
	resolved, err := common.InjectSecrets(content)
	if err != nil {
		logger.Warn("Secret resolution reported an error: %v", err)
		// If we still have refs, list them to aid debugging
		remainingRefs := extractOnePasswordReferences(content)
		for _, ref := range remainingRefs {
			logger.Warn("Unresolved reference: %s", ref)
		}

		// If error indicates unauthenticated op CLI, attempt interactive signin and retry once
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not authenticated") || strings.Contains(errStr, "not signed in") || strings.Contains(errStr, "please run 'op signin'") {
			logger.Info("Attempting 1Password CLI signin due to authentication error...")
			if authErr := common.Ensure1PasswordAuth(); authErr != nil {
				return "", fmt.Errorf("1Password signin failed: %w (original: %v)", authErr, err)
			}
			// Retry resolution once after successful signin
			if retryResolved, retryErr := common.InjectSecrets(content); retryErr == nil {
				resolved = retryResolved
				err = nil
			} else {
				return "", fmt.Errorf("secret resolution failed after signin: %w", retryErr)
			}
		} else {
			return "", err
		}
	}

	// Debug: Show the final result for secretboxEncryptionSecret after resolution
	finalLines := strings.Split(resolved, "\n")
	for i, line := range finalLines {
		if strings.Contains(line, "secretboxEncryptionSecret") {
			logger.Debug("Final line %d with secretboxEncryptionSecret: '%s'", i+1, line)
		}
	}

	// Optional validation message
	if strings.Contains(resolved, "op://") {
		logger.Warn("Warning: Resolved content still contains 1Password references")
		remainingRefs := extractOnePasswordReferences(resolved)
		for _, ref := range remainingRefs {
			logger.Warn("Unresolved reference: %s", ref)
		}
	} else {
		logger.Debug("✅ No 1Password references remain in rendered configuration")
	}

	// Save rendered configuration for validation if debug is enabled
	if os.Getenv("DEBUG") == "1" || os.Getenv("SAVE_RENDERED_CONFIG") == "1" {
		hash := fmt.Sprintf("%x", md5.Sum([]byte(resolved)))
		filename := fmt.Sprintf("rendered-config-%s.yaml", hash[:8])
		if err := saveRenderedConfig(resolved, filename, logger); err != nil {
			logger.Warn("Failed to save rendered configuration: %v", err)
		}
	}

	return resolved, nil
}

// saveRenderedConfig saves the rendered configuration to a file for inspection
func saveRenderedConfig(config, filename string, logger *common.ColorLogger) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			logger.Warn("Failed to close file: %v", err)
		}
	}()

	_, err = file.WriteString(config)
	if err != nil {
		return fmt.Errorf("failed to write to file %s: %w", filename, err)
	}

	logger.Debug("Saved rendered configuration to %s for validation", filename)
	return nil
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

	// Fix any existing CRDs that may lack proper Helm ownership metadata
	// This prevents Helm from failing when trying to adopt pre-existing CRDs
	if err := fixExistingCRDMetadata(config, logger); err != nil {
		logger.Warn("Failed to fix existing CRD metadata (continuing anyway): %v", err)
	}

	// Retry helmfile sync with exponential backoff
	maxAttempts := 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			logger.Info("Helm sync attempt %d/%d", attempt, maxAttempts)
		}

		err := executeHelmfileSync(config, logger)
		if err == nil {
			logger.Info("Helm releases synced successfully")

			// Check if external-secrets was installed by Helmfile before waiting for it
			logger.Info("Checking if external-secrets is ready...")
			if isExternalSecretsInstalled(config, logger) {
				logger.Info("External-secrets found, waiting for webhook to be ready...")
				if err := waitForExternalSecretsWebhook(config, logger); err != nil {
					logger.Warn("External-secrets webhook not ready after waiting: %v", err)
					logger.Info("ClusterSecretStore will be applied by Flux when external-secrets becomes ready")
				} else {
					// Apply cluster secret store only if webhook is ready
					if err := applyClusterSecretStore(config, logger); err != nil {
						logger.Warn("Failed to apply ClusterSecretStore: %v", err)
						logger.Info("ClusterSecretStore will be retried by Flux")
					} else {
						logger.Success("ClusterSecretStore applied successfully")
					}
				}
			} else {
				logger.Info("External-secrets not installed in bootstrap phase - will be installed by Flux")
				logger.Info("ClusterSecretStore will be applied by Flux after external-secrets is ready")
			}

			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableHelmError(err) {
			logger.Error("Non-retryable Helm error encountered")
			return fmt.Errorf("helmfile sync failed: %w", err)
		}

		logger.Warn("Helm sync attempt %d/%d failed with retryable error: %v", attempt, maxAttempts, err)

		if attempt < maxAttempts {
			waitTime := time.Duration(attempt*30) * time.Second // 30s, 60s
			logger.Info("Waiting %v before retry...", waitTime)
			time.Sleep(waitTime)

			// Re-verify API server health before retry
			logger.Info("Re-checking API server connectivity before retry...")
			if err := testAPIServerConnectivity(config, logger); err != nil {
				logger.Warn("API server connectivity check failed: %v", err)
				logger.Info("Continuing with retry anyway...")
			}
		}
	}

	return fmt.Errorf("helmfile sync failed after %d attempts: %w", maxAttempts, lastErr)
}

// separateCRDsFromManifests separates CRD manifests from other manifests
func separateCRDsFromManifests(manifestsYaml string) ([]string, []string, error) {
	var crdManifests []string
	var otherManifests []string

	// Split by YAML document separator
	documents := strings.Split(manifestsYaml, "\n---\n")

	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" || doc == "---" {
			continue
		}

		// Check if this is a CRD by looking for "kind: CustomResourceDefinition"
		if strings.Contains(doc, "kind: CustomResourceDefinition") {
			crdManifests = append(crdManifests, doc)
		} else {
			otherManifests = append(otherManifests, doc)
		}
	}

	return crdManifests, otherManifests, nil
}

// waitForCRDsEstablished waits for all CRDs to be established
func waitForCRDsEstablished(config *BootstrapConfig, logger *common.ColorLogger) error {
	maxAttempts := 30 // 2 minutes with 4-second intervals
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("kubectl", "get", "crd",
			"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type=='Established')].status}{\"\\n\"}{end}",
			"--kubeconfig", config.KubeConfig)

		output, err := cmd.Output()
		if err != nil {
			logger.Debug("Attempt %d/%d: Failed to check CRD status", attempt, maxAttempts)
			if attempt == maxAttempts {
				return fmt.Errorf("failed to check CRD status after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(4 * time.Second)
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		allEstablished := true
		establishedCount := 0
		totalCRDs := 0

		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				totalCRDs++
				crdName := parts[0]
				status := parts[1]
				if status == "True" {
					establishedCount++
					logger.Debug("CRD %s is established", crdName)
				} else {
					logger.Debug("CRD %s is not established (status: %s)", crdName, status)
					allEstablished = false
				}
			}
		}

		if allEstablished && establishedCount > 0 {
			logger.Success("All %d CRDs are established", establishedCount)
			return nil
		}

		if attempt%5 == 0 { // Log every 20 seconds
			logger.Info("Waiting for CRDs to be established: %d/%d ready", establishedCount, totalCRDs)
		}
		time.Sleep(4 * time.Second)
	}

	return fmt.Errorf("CRDs did not become established after %d attempts", maxAttempts)
}

// executeHelmfileSync executes the helmfile sync operation
func executeHelmfileSync(config *BootstrapConfig, logger *common.ColorLogger) error {
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

	// Create templates subdirectory to match the new helmfile path references
	templatesDir := filepath.Join(tempDir, "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}

	// Create values.yaml.gotmpl in templates subdirectory
	valuesTemplate, err := templates.GetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}

	valuesPath := filepath.Join(templatesDir, "values.yaml.gotmpl")
	if err := os.WriteFile(valuesPath, []byte(valuesTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	// Get embedded apps helmfile content
	appsHelmfileTemplate, err := templates.GetBootstrapFile("helmfile.d/01-apps.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded apps helmfile: %w", err)
	}

	// The apps helmfile doesn't need templating, write it directly
	helmfilePath := filepath.Join(tempDir, "01-apps.yaml")
	if err := os.WriteFile(helmfilePath, []byte(appsHelmfileTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write apps helmfile: %w", err)
	}

	logger.Debug("Created dynamic helmfile with Go template support")
	logger.Debug("Setting working directory to: %s", config.RootDir)

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
		return fmt.Errorf("helmfile sync failed: %w", err)
	}

	return nil
}

// isRetryableHelmError checks if a Helm error is retryable
func isRetryableHelmError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	retryablePatterns := []string{
		"connection lost",
		"connection refused",
		"timeout",
		"tls handshake timeout",
		"i/o timeout",
		"context deadline exceeded",
		"temporary failure",
		"server is currently unable",
		"http2: client connection lost",
		"client connection lost",
		"dial tcp",
		"connection reset by peer",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// fixExistingCRDMetadata adds Helm ownership metadata to existing CRDs that lack it
// This allows Helm to adopt CRDs that were previously installed manually or by other means
func fixExistingCRDMetadata(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("Checking for CRDs that need Helm ownership metadata...")

	// Define known CRD groups and their Helm release ownership
	crdGroups := map[string]struct {
		releaseName      string
		releaseNamespace string
	}{
		"external-secrets.io":       {releaseName: "external-secrets", releaseNamespace: "external-secrets"},
		"cert-manager.io":           {releaseName: "cert-manager", releaseNamespace: "cert-manager"},
		"gateway.networking.k8s.io": {releaseName: "cilium", releaseNamespace: "kube-system"},
	}

	// Get all CRDs
	cmd := exec.Command("kubectl", "get", "crds", "-o", "json", "--kubeconfig", config.KubeConfig)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get CRDs: %w", err)
	}

	// Parse the JSON output
	var crdList struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}

	if err := json.Unmarshal(output, &crdList); err != nil {
		return fmt.Errorf("failed to parse CRD list: %w", err)
	}

	fixedCount := 0
	for _, crd := range crdList.Items {
		// Check if CRD already has Helm ownership metadata
		if crd.Metadata.Labels["app.kubernetes.io/managed-by"] == "Helm" &&
			crd.Metadata.Annotations["meta.helm.sh/release-name"] != "" {
			continue
		}

		// Determine which Helm release should own this CRD based on its group
		var owner *struct {
			releaseName      string
			releaseNamespace string
		}

		for groupSuffix, groupOwner := range crdGroups {
			if strings.HasSuffix(crd.Metadata.Name, groupSuffix) {
				owner = &groupOwner
				break
			}
		}

		// If we don't know the owner, skip this CRD
		if owner == nil {
			continue
		}

		logger.Debug("Adding Helm metadata to CRD: %s (owner: %s/%s)",
			crd.Metadata.Name, owner.releaseNamespace, owner.releaseName)

		// Patch the CRD with Helm ownership metadata
		patchCmd := exec.Command("kubectl", "annotate", "crd", crd.Metadata.Name,
			fmt.Sprintf("meta.helm.sh/release-name=%s", owner.releaseName),
			fmt.Sprintf("meta.helm.sh/release-namespace=%s", owner.releaseNamespace),
			"--overwrite",
			"--kubeconfig", config.KubeConfig)

		if output, err := patchCmd.CombinedOutput(); err != nil {
			logger.Warn("Failed to add annotations to CRD %s: %v\nOutput: %s",
				crd.Metadata.Name, err, string(output))
			continue
		}

		labelCmd := exec.Command("kubectl", "label", "crd", crd.Metadata.Name,
			"app.kubernetes.io/managed-by=Helm",
			"--overwrite",
			"--kubeconfig", config.KubeConfig)

		if output, err := labelCmd.CombinedOutput(); err != nil {
			logger.Warn("Failed to add labels to CRD %s: %v\nOutput: %s",
				crd.Metadata.Name, err, string(output))
			continue
		}

		fixedCount++
	}

	if fixedCount > 0 {
		logger.Success("Added Helm ownership metadata to %d CRDs", fixedCount)
	} else {
		logger.Info("All CRDs already have proper Helm ownership metadata")
	}

	return nil
}

// testAPIServerConnectivity is a simpler version for retry checks
func testAPIServerConnectivity(config *BootstrapConfig, logger *common.ColorLogger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "cluster-info", "--kubeconfig", config.KubeConfig)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debug("API server connectivity check output: %s", string(output))
		return fmt.Errorf("cluster-info failed: %w", err)
	}
	return nil
}

// isExternalSecretsInstalled checks if external-secrets is installed in the cluster
func isExternalSecretsInstalled(config *BootstrapConfig, logger *common.ColorLogger) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "external-secrets-webhook",
		"-n", "external-secrets", "--kubeconfig", config.KubeConfig)
	err := cmd.Run()

	if err != nil {
		logger.Debug("External-secrets not found: %v", err)
		return false
	}
	return true
}

// waitForExternalSecretsWebhook waits for the external-secrets webhook to be ready
func waitForExternalSecretsWebhook(config *BootstrapConfig, logger *common.ColorLogger) error {
	maxAttempts := 30 // Reduced from 60 to 30 (2.5 minutes instead of 5)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check if the webhook deployment is ready
		cmd := exec.Command("kubectl", "get", "deployment", "external-secrets-webhook",
			"-n", "external-secrets",
			"--output=jsonpath={.status.readyReplicas}",
			"--kubeconfig", config.KubeConfig)

		output, err := cmd.Output()
		if err != nil {
			logger.Debug("Attempt %d/%d: Failed to check webhook deployment status", attempt, maxAttempts)

			// Add diagnostic info on failure
			if attempt%6 == 0 {
				logger.Info("Still waiting for external-secrets webhook (attempt %d/%d)", attempt, maxAttempts)
				if podOutput, podErr := exec.Command("kubectl", "get", "pods", "-n", "external-secrets",
					"--kubeconfig", config.KubeConfig).Output(); podErr == nil {
					logger.Debug("External-secrets pods:\n%s", string(podOutput))
				}
			}

			if attempt == maxAttempts {
				return fmt.Errorf("webhook deployment not found after %d attempts (2.5 minutes): %w", maxAttempts, err)
			}
			time.Sleep(5 * time.Second)
			continue
		}

		readyReplicas := strings.TrimSpace(string(output))
		if readyReplicas != "" && readyReplicas != "0" {
			// Also check if the webhook service has endpoints
			endpointsCmd := exec.Command("kubectl", "get", "endpoints", "external-secrets-webhook",
				"-n", "external-secrets",
				"--output=jsonpath={.subsets[*].addresses[*].ip}",
				"--kubeconfig", config.KubeConfig)

			endpointsOutput, endpointsErr := endpointsCmd.Output()
			if endpointsErr == nil {
				endpoints := strings.TrimSpace(string(endpointsOutput))
				if endpoints != "" {
					logger.Success("External-secrets webhook is ready with %s ready replicas", readyReplicas)
					return nil
				}
			}
			logger.Debug("Webhook deployment ready but no endpoints available yet")
		} else {
			logger.Debug("Webhook deployment not ready yet (ready replicas: %s)", readyReplicas)
		}

		if attempt%6 == 0 { // Log every 30 seconds
			logger.Info("Still waiting for external-secrets webhook to be ready (attempt %d/%d)", attempt, maxAttempts)
			// Show pod status for debugging
			if podOutput, podErr := exec.Command("kubectl", "get", "pods", "-n", "external-secrets", "-o", "wide",
				"--kubeconfig", config.KubeConfig).Output(); podErr == nil {
				logger.Debug("External-secrets pods:\n%s", string(podOutput))
			}
		}
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("external-secrets webhook did not become ready after %d attempts (2.5 minutes)", maxAttempts)
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

// saveKubeconfigTo1Password saves the kubeconfig content to the existing 1Password item as a file attachment
func saveKubeconfigTo1Password(kubeconfigContent []byte, logger *common.ColorLogger) error {
	logger.Debug("Updating kubeconfig file in 1Password...")

	// Create a temporary file with the kubeconfig content
	tmpFile, err := os.CreateTemp("", "kubeconfig-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temporary kubeconfig file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	if _, err := tmpFile.Write(kubeconfigContent); err != nil {
		return fmt.Errorf("failed to write kubeconfig to temporary file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Update the existing kubeconfig item by replacing the file attachment
	cmd := exec.Command("op", "item", "edit", "kubeconfig", "--vault", "Private", fmt.Sprintf("kubeconfig[file]=%s", tmpFile.Name()))
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to update kubeconfig file in 1Password: %w (output: %s)", err, string(output))
	}

	logger.Debug("Kubeconfig file updated in 1Password")
	return nil
}
