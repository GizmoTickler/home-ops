package bootstrap

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/ui"

	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"
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

var (
	bootstrapNow              = time.Now
	bootstrapSleep            = time.Sleep
	bootstrapChoose           = ui.Choose
	bootstrapChooseMulti      = ui.ChooseMulti
	bootstrapRunWithSpinner   = ui.RunWithSpinner
	bootstrapResetTerminal    = ui.ResetTerminal
	bootstrapWorkingDirectory = common.GetWorkingDirectory
	bootstrapGetVersions      = versionconfig.GetVersions
	bootstrapLookPath         = exec.LookPath
	bootstrapEnsureOPAuth     = common.Ensure1PasswordAuth
	bootstrapHTTPDo           = func(req *http.Request) (*http.Response, error) {
		client := &http.Client{Timeout: 10 * time.Second}
		return client.Do(req)
	}
	bootstrapLookupHost = func(ctx context.Context, host string) ([]string, error) {
		return (&net.Resolver{}).LookupHost(ctx, host)
	}
	bootstrapKubectlRun        = kubectlRun
	bootstrapKubectlOutput     = kubectlOutput
	bootstrapKubectlCombined   = kubectlCombinedOutput
	bootstrapKubectlCombinedIn = kubectlCombinedOutputWithInput
	bootstrapTalosctlOutput    = func(talosConfig string, args ...string) ([]byte, error) {
		return buildTalosctlCmd(talosConfig, args...).Output()
	}
	bootstrapTalosctlCombined = func(talosConfig string, args ...string) ([]byte, error) {
		return buildTalosctlCmd(talosConfig, args...).CombinedOutput()
	}
	bootstrapGetBootstrapFile       = templates.GetBootstrapFile
	bootstrapGetBootstrapTemplate   = templates.GetBootstrapTemplate
	bootstrapGetTalosTemplate       = templates.GetTalosTemplate
	bootstrapInjectSecrets          = common.InjectSecrets
	bootstrapResolveSecrets         = resolve1PasswordReferences
	bootstrapRenderMachineConfig    = renderMachineConfigFromEmbedded
	bootstrapGetMachineType         = getMachineTypeFromEmbedded
	bootstrapMergeTalosConfigs      = mergeConfigsWithTalosctl
	bootstrapGetTalosNodes          = getTalosNodes
	bootstrapApplyNodeConfig        = applyNodeConfig
	bootstrapApplyNodeConfigTry     = applyNodeConfigWithRetry
	bootstrapValidateEtcd           = validateEtcdRunning
	bootstrapSaveKubeconfig         = common.SaveKubeconfigTo1Password
	bootstrapPatchKubeconfig        = patchKubeconfigForBootstrap
	bootstrapGetRandomController    = getRandomController
	bootstrapRunPreflightChecks     = runPreflightChecks
	bootstrapValidatePrereqs        = validatePrerequisites
	bootstrapApplyTalosConfig       = applyTalosConfig
	bootstrapBootstrapTalos         = bootstrapTalos
	bootstrapFetchKubeconfig        = fetchKubeconfig
	bootstrapValidateKubeconfig     = validateKubeconfig
	bootstrapWaitForNodes           = waitForNodes
	bootstrapWaitNodesAvailable     = waitForNodesAvailable
	bootstrapCheckNodesReady        = checkIfNodesReady
	bootstrapWaitNodesReadyFalse    = waitForNodesReadyFalse
	bootstrapApplyNamespaces        = applyNamespaces
	bootstrapApplyResources         = applyResources
	bootstrapApplyCRDs              = applyCRDs
	bootstrapApplyGatewayCRDs       = applyGatewayAPICRDs
	bootstrapApplyCRDsHelmfile      = applyCRDsFromHelmfile
	bootstrapSyncHelmReleases       = syncHelmReleases
	bootstrapWaitForFlux            = waitForFluxReconciliation
	bootstrapWaitFluxController     = waitForFluxController
	bootstrapWaitGitRepository      = waitForGitRepositoryReady
	bootstrapWaitFluxKS             = waitForFluxKustomizationReady
	bootstrapWaitCRDs               = waitForCRDsEstablished
	bootstrapApplySecretStore       = applyClusterSecretStore
	bootstrapValidateSecretStore    = validateClusterSecretStoreTemplate
	bootstrapTestDynamicValues      = testDynamicValuesTemplate
	bootstrapFixCRDMetadata         = fixExistingCRDMetadata
	bootstrapExecuteHelmfileSync    = executeHelmfileSync
	bootstrapHelmfileTemplateOutput = func(tempDir string, config *BootstrapConfig, helmfilePath string) ([]byte, error) {
		cmd := buildHelmfileCmd(tempDir, config, "--file", helmfilePath, "template")
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				redacted := common.RedactCommandOutput(string(exitErr.Stderr))
				return nil, fmt.Errorf("helmfile template failed: %w\nStderr: %s", err, redacted)
			}
			return nil, fmt.Errorf("helmfile template failed: %w", err)
		}
		return out, nil
	}
	bootstrapRunHelmfileSyncCmd = func(tempDir, helmfilePath string, config *BootstrapConfig) error {
		cmd := buildHelmfileCmd(tempDir, config, "--file", helmfilePath, "sync", "--hide-notes")
		cmd.Stdout = bootstrapHelmfileStdout
		cmd.Stderr = bootstrapHelmfileStderr
		cmd.Env = append(cmd.Env, fmt.Sprintf("HELMFILE_TEMPLATE_DIR=%s", tempDir))
		return cmd.Run()
	}
	// bootstrapHelmfileStdout/Stderr default to os.Stdout/Stderr but can be replaced
	// in tests so we can verify the sync command wires its streams correctly.
	bootstrapHelmfileStdout      io.Writer = os.Stdout
	bootstrapHelmfileStderr      io.Writer = os.Stderr
	bootstrapExternalSecretsUp             = isExternalSecretsInstalled
	bootstrapWaitExternalSecrets           = waitForExternalSecretsWebhook
	bootstrapTestAPIConnectivity           = testAPIServerConnectivity
	bootstrapRenderHelmValues              = templates.RenderHelmfileValues
	bootstrapCheckIntervalNormal           = time.Duration(constants.BootstrapCheckIntervalNormal) * time.Second
	bootstrapCheckIntervalFast             = time.Duration(constants.BootstrapCheckIntervalFast) * time.Second
	bootstrapCheckIntervalSlow             = time.Duration(constants.BootstrapCheckIntervalSlow) * time.Second
	bootstrapStallTimeout                  = time.Duration(constants.BootstrapStallTimeout) * time.Second
	bootstrapExtSecMaxWait                 = time.Duration(constants.BootstrapExtSecMaxWait) * time.Second
	bootstrapFluxMaxWait                   = time.Duration(constants.BootstrapFluxMaxWait) * time.Second
	bootstrapNodeMaxWait                   = time.Duration(constants.BootstrapNodeMaxWait) * time.Second
	bootstrapKubeconfigMaxWait             = time.Duration(constants.BootstrapKubeconfigMaxWait) * time.Second
	bootstrapCRDMaxWait                    = time.Duration(constants.BootstrapCRDMaxWait) * time.Second
	bootstrapTalosTempDir        string
	bootstrapPreflightChecks     = []func(*BootstrapConfig, *common.ColorLogger) *PreflightResult{
		checkToolAvailability,
		checkEnvironmentFiles,
		checkNetworkConnectivity,
		checkDNSResolution,
		check1PasswordAuthPreflight,
		checkMachineConfigRendering,
		checkTalosNodes,
	}
)

func parseReplicaCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("replica count is empty")
	}
	var count int
	if _, err := fmt.Sscanf(value, "%d", &count); err != nil {
		return 0, fmt.Errorf("invalid replica count %q: %w", value, err)
	}
	return count, nil
}

func deploymentReadyFromState(state string) bool {
	parts := strings.Split(strings.TrimSpace(state), "/")
	if len(parts) != 2 {
		return false
	}

	readyReplicas, err := parseReplicaCount(parts[0])
	if err != nil {
		return false
	}
	totalReplicas, err := parseReplicaCount(parts[1])
	if err != nil {
		return false
	}

	return totalReplicas > 0 && readyReplicas == totalReplicas
}

func deploymentAndEndpointsReadyFromState(state, endpoints string) bool {
	parts := strings.Split(strings.TrimSpace(state), ":")
	if len(parts) < 2 {
		return false
	}
	if strings.TrimSpace(parts[1]) != "True" {
		return false
	}
	if !deploymentReadyFromState(parts[0]) {
		return false
	}
	return strings.TrimSpace(endpoints) != ""
}

// buildTalosctlCmd builds a talosctl command with optional talosconfig.
func buildTalosctlCmd(talosConfig string, args ...string) *exec.Cmd {
	if talosConfig != "" {
		cmdArgs := append([]string{"--talosconfig", talosConfig}, args...)
		return common.Command("talosctl", cmdArgs...)
	}
	return common.Command("talosctl", args...)
}

func buildTalosctlCmdContext(ctx context.Context, talosConfig string, args ...string) *exec.Cmd {
	if talosConfig != "" {
		cmdArgs := append([]string{"--talosconfig", talosConfig}, args...)
		return exec.CommandContext(ctx, "talosctl", cmdArgs...)
	}
	return exec.CommandContext(ctx, "talosctl", args...)
}

func buildKubectlCmd(config *BootstrapConfig, args ...string) *exec.Cmd {
	cmdArgs := append(append([]string{}, args...), "--kubeconfig", config.KubeConfig)
	return common.Command("kubectl", cmdArgs...)
}

func buildKubectlCmdContext(ctx context.Context, config *BootstrapConfig, args ...string) *exec.Cmd {
	cmdArgs := append(append([]string{}, args...), "--kubeconfig", config.KubeConfig)
	return exec.CommandContext(ctx, "kubectl", cmdArgs...)
}

func kubectlOutput(config *BootstrapConfig, args ...string) ([]byte, error) {
	return buildKubectlCmd(config, args...).Output()
}

func kubectlRun(config *BootstrapConfig, args ...string) error {
	return buildKubectlCmd(config, args...).Run()
}

func kubectlCombinedOutput(config *BootstrapConfig, args ...string) ([]byte, error) {
	return buildKubectlCmd(config, args...).CombinedOutput()
}

func kubectlCombinedOutputWithInput(config *BootstrapConfig, input io.Reader, args ...string) ([]byte, error) {
	cmd := buildKubectlCmd(config, args...)
	cmd.Stdin = input
	return cmd.CombinedOutput()
}

// runKubectlContext executes kubectl with a context-bound timeout/cancellation
// signal. Output is redacted before being returned via the error path.
func runKubectlContext(ctx context.Context, config *BootstrapConfig, args ...string) (common.CommandResult, error) {
	cmdArgs := append(append([]string{}, args...), "--kubeconfig", config.KubeConfig)
	return common.RunCommand(ctx, common.CommandOptions{
		Name: "kubectl",
		Args: cmdArgs,
	})
}

// runTalosctlContext executes talosctl with a context-bound timeout/cancellation
// signal. Output is redacted before being returned via the error path.
func runTalosctlContext(ctx context.Context, talosConfig string, args ...string) (common.CommandResult, error) {
	cmdArgs := args
	if talosConfig != "" {
		cmdArgs = append([]string{"--talosconfig", talosConfig}, args...)
	}
	return common.RunCommand(ctx, common.CommandOptions{
		Name: "talosctl",
		Args: cmdArgs,
	})
}

func redactCommandOutput(output []byte) string {
	return common.RedactCommandOutput(string(output))
}

func buildHelmfileCmd(tempDir string, config *BootstrapConfig, args ...string) *exec.Cmd {
	cmd := common.Command("helmfile", args...)
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("ROOT_DIR=%s", config.RootDir))
	return cmd
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
	defaultRootDir := bootstrapWorkingDirectory()
	cmd.Flags().StringVar(&config.RootDir, "root-dir", defaultRootDir, "Root directory of the project")
	cmd.Flags().StringVar(&config.KubeConfig, "kubeconfig", os.Getenv(constants.EnvKubeconfig), "Path to kubeconfig file")
	cmd.Flags().StringVar(&config.TalosConfig, "talosconfig", os.Getenv(constants.EnvTalosconfig), "Path to talosconfig file")
	cmd.Flags().StringVar(&config.K8sVersion, "k8s-version", os.Getenv(constants.EnvKubernetesVersion), "Kubernetes version")
	cmd.Flags().StringVar(&config.TalosVersion, "talos-version", os.Getenv(constants.EnvTalosVersion), "Talos version")
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

	selectedMode, err := bootstrapChoose("Select bootstrap mode:", dryRunOptions)
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

	selectedOptions, err := bootstrapChooseMulti("Select options to customize (use 'x' to toggle, Enter to confirm - or just press Enter for full bootstrap):", skipOptions, 0)
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
		versionConfig := bootstrapGetVersions(config.RootDir)
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
		if err := bootstrapRunWithSpinner("🔍 Running preflight checks", config.Verbose, logger, func() error {
			return bootstrapRunPreflightChecks(config, logger)
		}); err != nil {
			return fmt.Errorf("preflight checks failed: %w", err)
		}
	} else {
		logger.Warn("⚠️  Skipping preflight checks - this may cause failures during bootstrap")
		// Still run basic prerequisite validation
		if err := bootstrapValidatePrereqs(config); err != nil {
			return fmt.Errorf("prerequisite validation failed: %w", err)
		}
	}

	// Step 1: Apply Talos configuration
	logger.Info("📋 Step 1: Applying Talos configuration to nodes")
	if err := bootstrapApplyTalosConfig(config, logger); err != nil {
		return fmt.Errorf("failed to apply Talos config: %w", err)
	}

	// Reset terminal after Step 1 completes (multiple spinners)
	bootstrapResetTerminal()

	// Step 2: Bootstrap Talos
	if err := bootstrapRunWithSpinner("🎯 Step 2: Bootstrapping Talos cluster", config.Verbose, logger, func() error {
		// Wait a moment for configurations to be fully processed (following onedr0p's pattern)
		logger.Debug("Waiting for configurations to be processed...")
		bootstrapSleep(5 * time.Second)
		return bootstrapBootstrapTalos(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to bootstrap Talos: %w", err)
	}

	// Step 3: Fetch kubeconfig
	if err := bootstrapRunWithSpinner("🔑 Step 3: Fetching and validating kubeconfig", config.Verbose, logger, func() error {
		if err := bootstrapFetchKubeconfig(config, logger); err != nil {
			return fmt.Errorf("failed to fetch kubeconfig: %w", err)
		}
		// Validate kubeconfig is working
		if err := bootstrapValidateKubeconfig(config, logger); err != nil {
			return fmt.Errorf("kubeconfig validation failed: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	// Step 4: Wait for nodes to be ready
	if err := bootstrapRunWithSpinner("⏳ Step 4: Waiting for nodes to be ready", config.Verbose, logger, func() error {
		return bootstrapWaitForNodes(config, logger)
	}); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}

	// Step 5: Apply namespaces first (following onedr0p pattern)
	if err := bootstrapRunWithSpinner("📦 Step 5: Creating initial namespaces", config.Verbose, logger, func() error {
		return bootstrapApplyNamespaces(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to apply namespaces: %w", err)
	}

	// Step 6: Apply initial resources
	if !config.SkipResources {
		if err := bootstrapRunWithSpinner("🔧 Step 6: Applying initial resources", config.Verbose, logger, func() error {
			return bootstrapApplyResources(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}

	// Step 7: Apply CRDs
	if !config.SkipCRDs {
		if err := bootstrapRunWithSpinner("📜 Step 7: Applying Custom Resource Definitions", config.Verbose, logger, func() error {
			return bootstrapApplyCRDs(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}

	// Step 8: Sync Helm releases
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner("⚙️  Step 8: Syncing Helm releases", config.Verbose, logger, func() error {
			return bootstrapSyncHelmReleases(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}

	// Step 9: Wait for Flux initial reconciliation
	// This is critical - without it, bootstrap declares success before Flux has actually reconciled
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner("🔄 Step 9: Waiting for Flux initial reconciliation", config.Verbose, logger, func() error {
			return bootstrapWaitForFlux(config, logger)
		}); err != nil {
			// Not fatal - cluster is functional, just not fully reconciled yet
			logger.Warn("Flux reconciliation wait completed with warnings: %v", err)
			logger.Info("Cluster is functional but Flux may still be reconciling in the background")
		}
	}

	logger.Success("🎉 Congrats! The cluster is bootstrapped and Flux has completed initial reconciliation")

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

	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapKubeconfigMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastState := ""

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("cluster did not become ready after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		// Test cluster connectivity with timeout
		currentState := "no-connection"
		if err := bootstrapKubectlRun(config, "cluster-info", "--request-timeout=10s"); err == nil {
			// If cluster-info succeeds, test node accessibility
			if err := bootstrapKubectlRun(config, "get", "nodes", "--request-timeout=10s"); err == nil {
				logger.Debug("Kubeconfig validation passed - cluster is accessible (took %v)", elapsed.Round(time.Second))
				return nil
			}
			currentState = "api-reachable-no-nodes"
		}

		// Check for progress
		if currentState != lastState {
			logger.Debug("Cluster state: %s", currentState)
			lastProgressTime = bootstrapNow()
			lastState = currentState
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("cluster connectivity stalled: no progress for %v (state: %s)", stallDuration.Round(time.Second), currentState)
		}

		if int(elapsed.Seconds())%30 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for API server: state=%s, %v elapsed", currentState, elapsed.Round(time.Second))
		}

		bootstrapSleep(checkInterval)
	}
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
		constants.NSAuth,
		constants.NSAutomation,
		constants.NSCertManager,
		constants.NSDatabase,
		constants.NSDownloads,
		constants.NSExternalSecret,
		constants.NSFluxSystem,
		constants.NSKubeSystem, // Usually exists but we'll ensure it's there
		constants.NSMedia,
		constants.NSNetwork,
		constants.NSObservability,
		constants.NSOpenEBSSystem,
		constants.NSRookCeph,
		constants.NSSelfHosted,
		constants.NSSystem,
		constants.NSSystemUpgrade,
		constants.NSVolsyncSystem,
	}

	for _, ns := range namespaces {
		logger.Debug("Creating namespace: %s", ns)

		manifestOutput, err := bootstrapKubectlOutput(config, "create", "namespace", ns, "--dry-run=client", "-o", "yaml")
		if err != nil {
			return fmt.Errorf("failed to generate namespace manifest for %s: %w", ns, err)
		}

		if output, err := bootstrapKubectlCombinedIn(config, bytes.NewReader(manifestOutput), "apply", "--filename", "-"); err != nil {
			// Check if namespace already exists - that's okay
			if strings.Contains(string(output), "AlreadyExists") || strings.Contains(string(output), "unchanged") {
				logger.Debug("Namespace %s already exists", ns)
				continue
			}
			return fmt.Errorf("failed to create namespace %s: %w\nOutput: %s", ns, err, redactCommandOutput(output))
		}

		logger.Debug("Successfully created namespace: %s", ns)
	}

	return nil
}

// validateClusterSecretStoreTemplate validates the clustersecretstore YAML file
func validateClusterSecretStoreTemplate(logger *common.ColorLogger) error {
	logger.Info("Validating clustersecretstore.yaml file")

	// Get the YAML file content
	yamlContent, err := bootstrapGetBootstrapFile("clustersecretstore.yaml")
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
	var result any
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
	expectedSecrets := []string{"onepassword-secret", "cloudflare-tunnel-id-secret"}
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
		if _, err := bootstrapLookPath(bin); err != nil {
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
	var failures []string
	for _, check := range bootstrapPreflightChecks {
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
		if _, err := bootstrapLookPath(bin); err != nil {
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

	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://github.com", nil)
	resp, err := bootstrapHTTPDo(req)
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

	_, err := bootstrapLookupHost(ctx, "github.com")
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
	if err := bootstrapEnsureOPAuth(); err != nil {
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

	_, err := bootstrapRenderMachineConfig("controlplane.yaml", patchTemplate, "controlplane", logger)
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
	nodes, err := bootstrapGetTalosNodes(config.TalosConfig)
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
		machineType, err := bootstrapGetMachineType(nodeTemplate)
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
		err = bootstrapRunWithSpinner(spinnerTitle, config.Verbose, logger, func() error {
			// Render machine config using embedded templates
			renderedConfig, err := bootstrapRenderMachineConfig(baseTemplate, nodeTemplate, machineType, logger)
			if err != nil {
				return fmt.Errorf("failed to render config: %w", err)
			}

			if config.DryRun {
				// For dry-run, just simulate a brief delay so spinner is visible
				bootstrapSleep(500 * time.Millisecond)
				return nil
			}

			// Apply the config with retry
			if err := bootstrapApplyNodeConfigTry(node, renderedConfig, logger, 3); err != nil {
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
	output, err := bootstrapTalosctlOutput(talosConfig, "config", "info", "--output", "json")
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
		nodes, err := bootstrapGetTalosNodes(talosConfig)
		if err == nil {
			return nodes, nil
		}

		lastErr = err
		logger.Warn("Attempt %d/%d to get Talos nodes failed: %v", attempt, maxRetries, err)

		if attempt < maxRetries {
			bootstrapSleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	return nil, fmt.Errorf("failed to get Talos nodes after %d attempts: %w", maxRetries, lastErr)
}

func applyNodeConfigWithRetry(node string, config []byte, logger *common.ColorLogger, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := bootstrapApplyNodeConfig(node, config)
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
			bootstrapSleep(time.Duration(attempt) * 3 * time.Second)
		}
	}

	return fmt.Errorf("failed to apply config to %s after %d attempts: %w", node, maxRetries, lastErr)
}

// applyTalosPatch function removed - now using Go YAML processor in renderMachineConfig

func applyNodeConfig(node string, config []byte) error {
	cmd := common.Command("talosctl", "--nodes", node, "apply-config", "--insecure", "--file", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(config)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Redact before surfacing — talosctl error output sometimes echoes secret-laden
		// machineconfig fragments. Keep error wording stable for upstream pattern matching.
		redacted := common.RedactCommandOutput(string(output))
		return fmt.Errorf("%s: %s", err, redacted)
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

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate, _ string, logger *common.ColorLogger) ([]byte, error) {
	// Get base config from embedded YAML file with proper talos/ prefix
	fullBaseTemplatePath := fmt.Sprintf("talos/%s", baseTemplate)
	baseConfig, err := bootstrapGetTalosTemplate(fullBaseTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get base config: %w", err)
	}

	// Get patch config from embedded YAML file with proper talos/ prefix
	fullPatchTemplatePath := fmt.Sprintf("talos/%s", patchTemplate)
	patchConfig, err := bootstrapGetTalosTemplate(fullPatchTemplatePath)
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

	resolvedBaseConfig, err := bootstrapResolveSecrets(baseConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in base config: %w", err)
	}

	resolvedPatchConfig, err := bootstrapResolveSecrets(machineConfigPatch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in patch config: %w", err)
	}

	// Use talosctl for merging resolved configs (following proven patterns)
	mergedConfig, err := bootstrapMergeTalosConfigs([]byte(resolvedBaseConfig), []byte(resolvedPatchConfig))
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
		resolvedAdditionalParts, err := bootstrapResolveSecrets(additionalParts, logger)
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
	baseFile, err := os.CreateTemp(bootstrapTalosTempDir, "talos-base-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create base config temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(baseFile.Name()) // Ignore cleanup errors
	}()
	defer func() {
		_ = baseFile.Close() // Ignore cleanup errors
	}()

	patchFile, err := os.CreateTemp(bootstrapTalosTempDir, "talos-patch-*.yaml")
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
	cmd := common.Command("talosctl", "machineconfig", "patch", baseFile.Name(), "--patch", "@"+patchFile.Name())

	mergedConfig, err := cmd.Output()
	if err != nil {
		// Get stderr for better error reporting; redact before surfacing.
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			redacted := common.RedactCommandOutput(string(exitError.Stderr))
			return nil, fmt.Errorf("talosctl config merge failed: %w\nStderr: %s", err, redacted)
		}
		return nil, fmt.Errorf("talosctl config merge failed: %w", err)
	}

	return mergedConfig, nil
}

// validateEtcdRunning validates that etcd is actually running after bootstrap
// Uses progress-based detection: continues as long as progress is being made
func validateEtcdRunning(talosConfig, controller string, logger *common.ColorLogger) error {
	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastState := ""
	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapExtSecMaxWait // 5 minutes max for etcd

	for {
		elapsed := bootstrapNow().Sub(startTime)

		// Check if we've exceeded maximum wait time (safety net)
		if elapsed > maxWait {
			return fmt.Errorf("etcd failed to start after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		// Check for stall - no progress for stall timeout period
		if bootstrapNow().Sub(lastProgressTime) > stallTimeout {
			return fmt.Errorf("etcd startup stalled - no progress for %v (last state: %s)", stallTimeout, lastState)
		}

		_, err := bootstrapTalosctlCombined(talosConfig, "--nodes", controller, "etcd", "status")
		if err == nil {
			logger.Debug("Etcd is running and responding")
			return nil
		}

		// Check if etcd service is actually running (not just waiting)
		serviceOutput, serviceErr := bootstrapTalosctlOutput(talosConfig, "--nodes", controller, "service", "etcd")
		if serviceErr == nil && strings.Contains(string(serviceOutput), "STATE    Running") {
			logger.Debug("Etcd service is running")
			return nil
		}

		// Track state changes for progress detection
		currentState := "waiting"
		if serviceErr == nil {
			// Extract service state from output for progress tracking
			outputStr := string(serviceOutput)
			if strings.Contains(outputStr, "Starting") {
				currentState = "starting"
			} else if strings.Contains(outputStr, "Preparing") {
				currentState = "preparing"
			} else if strings.Contains(outputStr, "Waiting") {
				currentState = "waiting"
			}
		}

		// Update progress if state changed
		if currentState != lastState {
			logger.Debug("Etcd state: %s -> %s (elapsed: %v)", lastState, currentState, elapsed.Round(time.Second))
			lastProgressTime = bootstrapNow()
			lastState = currentState
		}

		logger.Debug("Waiting for etcd to start (state: %s, elapsed: %v)", currentState, elapsed.Round(time.Second))
		bootstrapSleep(checkInterval)
	}
}

func bootstrapTalos(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get a random controller node
	controller, err := bootstrapGetRandomController(config.TalosConfig)
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
	if checkOutput, checkErr := bootstrapTalosctlCombined(config.TalosConfig, "--nodes", controller, "etcd", "status"); checkErr == nil {
		logger.Info("Talos cluster is already bootstrapped (etcd is running)")
		return nil
	} else {
		logger.Debug("Cluster not bootstrapped yet: %s", redactCommandOutput(checkOutput))
	}

	// Bootstrap with retry logic similar to onedr0p's implementation
	maxAttempts := 10
	var lastErr error
	var lastOutput string
	for attempts := range maxAttempts {
		logger.Debug("Bootstrap attempt %d/%d on controller %s", attempts+1, maxAttempts, controller)

		output, err := bootstrapTalosctlCombined(config.TalosConfig, "--nodes", controller, "bootstrap")
		outputStr := string(output)

		// Success cases (following onedr0p's logic)
		if err == nil {
			// Validate that etcd actually started after bootstrap
			logger.Debug("Bootstrap command succeeded, validating etcd started...")
			if err := bootstrapValidateEtcd(config.TalosConfig, controller, logger); err != nil {
				logger.Debug("Bootstrap succeeded but etcd validation failed: %v", err)
				if attempts < maxAttempts-1 {
					logger.Debug("Waiting 10 seconds before next bootstrap attempt")
					bootstrapSleep(10 * time.Second)
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

		// Preserve last error and output for final error message
		lastErr = err
		lastOutput = common.RedactCommandOutput(strings.TrimSpace(outputStr))

		logger.Debug("Bootstrap attempt %d failed: %v", attempts+1, err)
		logger.Debug("Bootstrap output: %s", common.RedactCommandOutput(outputStr))

		// Wait 5 seconds between attempts (matching onedr0p's timing)
		if attempts < maxAttempts-1 {
			logger.Debug("Waiting 5 seconds before next bootstrap attempt")
			bootstrapSleep(5 * time.Second)
		}

	}

	// Include the last error and output in the final error message for better diagnostics
	if lastOutput != "" {
		return fmt.Errorf("failed to bootstrap controller %s after %d attempts: %w (output: %s)", controller, maxAttempts, lastErr, lastOutput)
	}
	return fmt.Errorf("failed to bootstrap controller %s after %d attempts: %w", controller, maxAttempts, lastErr)
}

func getRandomController(talosConfig string) (string, error) {
	output, err := bootstrapTalosctlOutput(talosConfig, "config", "info", "--output", "json")
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

// fetchKubeconfig fetches the kubeconfig from the cluster
// Uses progress-based detection: continues as long as progress is being made
func fetchKubeconfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	controller, err := bootstrapGetRandomController(config.TalosConfig)
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

	// Progress-based waiting
	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastErrorType := ""
	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapKubeconfigMaxWait

	for {
		elapsed := bootstrapNow().Sub(startTime)

		// Check if we've exceeded maximum wait time (safety net)
		if elapsed > maxWait {
			return fmt.Errorf("failed to fetch kubeconfig after %v (max wait exceeded, last error: %s)", elapsed.Round(time.Second), lastErrorType)
		}

		// Check for stall - no progress for stall timeout period
		if bootstrapNow().Sub(lastProgressTime) > stallTimeout {
			return fmt.Errorf("kubeconfig fetch stalled - no progress for %v (last error: %s)", stallTimeout, lastErrorType)
		}

		logger.Debug("Kubeconfig fetch attempt (elapsed: %v)", elapsed.Round(time.Second))

		output, err := bootstrapTalosctlCombined(config.TalosConfig, "kubeconfig", "--nodes", controller,
			"--force", "--force-context-name", "home-ops-cluster", config.KubeConfig)
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
			if err := bootstrapSaveKubeconfig(kubeconfigContent, logger); err != nil {
				logger.Warn("Failed to save kubeconfig to 1Password: %v", err)
				logger.Warn("Continuing with bootstrap - kubeconfig is available locally")
			} else {
				logger.Success("Kubeconfig saved to 1Password for chezmoi")
			}

			// Patch kubeconfig to use direct node IP for bootstrap
			// The VIP won't work until Cilium BGP is up
			if err := bootstrapPatchKubeconfig(config.KubeConfig, controller, logger); err != nil {
				logger.Warn("Failed to patch kubeconfig for bootstrap: %v", err)
				logger.Warn("Bootstrap may fail if VIP is not accessible")
			}

			logger.Success("Kubeconfig fetched and validated successfully")
			return nil
		}

		// Categorize error type for progress tracking
		outputStr := string(output)
		currentErrorType := "unknown"
		if strings.Contains(outputStr, "connection refused") {
			currentErrorType = "connection_refused"
			logger.Debug("Kubeconfig fetch: Controller not ready yet (elapsed: %v)", elapsed.Round(time.Second))
		} else if strings.Contains(outputStr, "timeout") {
			currentErrorType = "timeout"
			logger.Debug("Kubeconfig fetch: Timeout (elapsed: %v)", elapsed.Round(time.Second))
		} else if strings.Contains(outputStr, "certificate") {
			currentErrorType = "certificate"
			logger.Debug("Kubeconfig fetch: Certificate issue (elapsed: %v)", elapsed.Round(time.Second))
		} else {
			logger.Debug("Kubeconfig fetch failed: %s (elapsed: %v)", common.RedactCommandOutput(strings.TrimSpace(outputStr)), elapsed.Round(time.Second))
		}

		// Update progress if error type changed (indicates different stage)
		if currentErrorType != lastErrorType {
			logger.Debug("Kubeconfig fetch error type changed: %s -> %s", lastErrorType, currentErrorType)
			lastProgressTime = bootstrapNow()
			lastErrorType = currentErrorType
		}

		bootstrapSleep(checkInterval)
	}
}

// patchKubeconfigForBootstrap modifies the kubeconfig to use a direct node IP
// instead of the VIP, since the VIP won't work until Cilium BGP is up
func patchKubeconfigForBootstrap(kubeconfigPath, nodeIP string, logger *common.ColorLogger) error {
	content, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	// Parse kubeconfig as YAML
	var kubeconfig map[string]any
	if err := yamlv3.Unmarshal(content, &kubeconfig); err != nil {
		return fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Find and update the server URL in clusters
	clusters, ok := kubeconfig["clusters"].([]any)
	if !ok {
		return fmt.Errorf("invalid kubeconfig: clusters not found")
	}

	modified := false
	for _, c := range clusters {
		cluster, ok := c.(map[string]any)
		if !ok {
			continue
		}
		clusterData, ok := cluster["cluster"].(map[string]any)
		if !ok {
			continue
		}
		if server, ok := clusterData["server"].(string); ok {
			// Replace the hostname/VIP with the direct node IP
			// Keep the port (usually 6443)
			newServer := fmt.Sprintf("https://%s:6443", nodeIP)
			if server != newServer {
				logger.Debug("Patching kubeconfig server: %s -> %s", server, newServer)
				clusterData["server"] = newServer
				modified = true
			}
		}
	}

	if !modified {
		logger.Debug("Kubeconfig already using direct node IP, no patch needed")
		return nil
	}

	// Write back the modified kubeconfig
	newContent, err := yamlv3.Marshal(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to marshal kubeconfig: %w", err)
	}

	if err := os.WriteFile(kubeconfigPath, newContent, 0600); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	logger.Info("Kubeconfig patched to use direct node IP %s for bootstrap", nodeIP)
	return nil
}

func waitForNodes(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would wait for nodes to be ready")
		return nil
	}

	// First, wait for nodes to appear
	logger.Info("Waiting for nodes to become available...")
	if err := bootstrapWaitNodesAvailable(config, logger); err != nil {
		return err
	}

	// Check if nodes are already ready (re-bootstrap scenario)
	if ready, err := bootstrapCheckNodesReady(config, logger); err != nil {
		return fmt.Errorf("failed to check node readiness: %w", err)
	} else if ready {
		logger.Success("Nodes are already ready (CNI likely already installed)")
		return nil
	}

	// Wait for nodes to be in Ready=False state (fresh bootstrap sequence)
	logger.Info("Waiting for nodes to be in 'Ready=False' state...")
	if err := bootstrapWaitNodesReadyFalse(config, logger); err != nil {
		return err
	}

	return nil
}

// checkIfNodesReady checks if nodes are already in Ready=True state
func checkIfNodesReady(config *BootstrapConfig, logger *common.ColorLogger) (bool, error) {
	output, err := bootstrapKubectlOutput(config, "get", "nodes",
		"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type=='Ready')].status}{\"\\n\"}{end}")
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
	checkInterval := bootstrapCheckIntervalSlow
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapNodeMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastNodeCount := 0

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("nodes not available after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "nodes",
			"--output=jsonpath={.items[*].metadata.name}", "--no-headers")
		if err != nil {
			stallDuration := bootstrapNow().Sub(lastProgressTime)
			if stallDuration > stallTimeout {
				return fmt.Errorf("node discovery stalled: kubectl get nodes failed for %v: %w",
					stallDuration.Round(time.Second), err)
			}
			bootstrapSleep(checkInterval)
			continue
		}

		nodeNames := strings.Fields(strings.TrimSpace(string(output)))
		nodeCount := len(nodeNames)

		if nodeCount > 0 {
			logger.Success("Found %d nodes: %v (took %v)", nodeCount, nodeNames, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress (node count change)
		if nodeCount != lastNodeCount {
			lastProgressTime = bootstrapNow()
			lastNodeCount = nodeCount
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("node discovery stalled: no progress for %v", stallDuration.Round(time.Second))
		}

		if int(elapsed.Seconds())%60 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for nodes to appear: %v elapsed", elapsed.Round(time.Second))
		}

		bootstrapSleep(checkInterval)
	}
}

func waitForNodesReadyFalse(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalSlow
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapNodeMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastReadyFalseCount := 0

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("nodes did not reach Ready=False state after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "nodes",
			"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type==\"Ready\")].status}{\"\\n\"}{end}")
		if err != nil {
			stallDuration := bootstrapNow().Sub(lastProgressTime)
			if stallDuration > stallTimeout {
				return fmt.Errorf("node readiness stalled: kubectl get nodes failed for %v: %w",
					stallDuration.Round(time.Second), err)
			}
			bootstrapSleep(checkInterval)
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
				readyStatus := parts[1]
				if readyStatus == "False" {
					readyFalseCount++
				} else {
					allReadyFalse = false
				}
			}
		}

		// Success: all nodes Ready=False
		if allReadyFalse && readyFalseCount > 0 {
			logger.Success("All %d nodes are in Ready=False state (took %v)", readyFalseCount, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress
		if readyFalseCount > lastReadyFalseCount {
			logger.Debug("Progress: %d/%d nodes Ready=False (+%d)", readyFalseCount, totalNodes, readyFalseCount-lastReadyFalseCount)
			lastProgressTime = bootstrapNow()
			lastReadyFalseCount = readyFalseCount
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("node readiness stalled: no progress for %v (stuck at %d/%d Ready=False)",
				stallDuration.Round(time.Second), readyFalseCount, totalNodes)
		}

		if int(elapsed.Seconds())%60 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for nodes: %d/%d Ready=False, %v elapsed", readyFalseCount, totalNodes, elapsed.Round(time.Second))
		}

		bootstrapSleep(checkInterval)
	}
}

// gatewayAPICRDsKustomizationPath is the path (relative to repo root) to the
// kustomization that installs Gateway API CRDs. The version is extracted from
// this file so Renovate manages it in a single place.
const gatewayAPICRDsKustomizationPath = "kubernetes/apps/network/kgateway/gateway-api-crds/kustomization.yaml"

func applyCRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Apply Gateway API CRDs from official kubernetes-sigs release
	// These are not in a Helm chart so they must be applied separately
	if err := bootstrapApplyGatewayCRDs(config, logger); err != nil {
		return fmt.Errorf("failed to apply Gateway API CRDs: %w", err)
	}

	// Apply remaining CRDs from Helm charts via helmfile
	return bootstrapApplyCRDsHelmfile(config, logger)
}

// expectedGatewayAPICRDsHost is the only allowed host for Gateway API CRD URLs.
const expectedGatewayAPICRDsHost = "github.com"

// expectedGatewayAPICRDsPathPrefix is the expected URL path prefix for validation.
const expectedGatewayAPICRDsPathPrefix = "/kubernetes-sigs/gateway-api/releases/download/"

// getGatewayAPICRDsURL reads the Gateway API CRDs install URL from the
// kustomization file so the version is managed by Renovate in one place.
func getGatewayAPICRDsURL(rootDir string) (string, error) {
	kustomizationPath := filepath.Join(rootDir, gatewayAPICRDsKustomizationPath)
	content, err := os.ReadFile(kustomizationPath)
	if err != nil {
		return "", fmt.Errorf("failed to read gateway-api-crds kustomization at %s: %w", kustomizationPath, err)
	}

	// Parse the kustomization to extract the GitHub release URL from resources
	var kustomization struct {
		Resources []string `yaml:"resources"`
	}
	if err := yamlv3.Unmarshal(content, &kustomization); err != nil {
		return "", fmt.Errorf("failed to parse gateway-api-crds kustomization: %w", err)
	}

	for _, resource := range kustomization.Resources {
		if strings.Contains(resource, "kubernetes-sigs/gateway-api") {
			if err := validateGatewayAPICRDsURL(resource); err != nil {
				return "", fmt.Errorf("invalid Gateway API CRDs URL in %s: %w", kustomizationPath, err)
			}
			return resource, nil
		}
	}

	return "", fmt.Errorf("gateway-api release URL not found in %s", kustomizationPath)
}

// validateGatewayAPICRDsURL validates that the URL points to the expected
// kubernetes-sigs/gateway-api GitHub release location.
func validateGatewayAPICRDsURL(rawURL string) error {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("malformed URL %q: %w", rawURL, err)
	}
	if parsed.Host != expectedGatewayAPICRDsHost {
		return fmt.Errorf("unexpected host %q, expected %q", parsed.Host, expectedGatewayAPICRDsHost)
	}
	if !strings.HasPrefix(parsed.Path, expectedGatewayAPICRDsPathPrefix) {
		return fmt.Errorf("unexpected path %q, expected prefix %q", parsed.Path, expectedGatewayAPICRDsPathPrefix)
	}
	return nil
}

// extractGatewayAPIVersion extracts the version tag (e.g. "v1.4.1") from a
// validated Gateway API CRDs URL. Returns the full URL as fallback.
func extractGatewayAPIVersion(rawURL string) string {
	rest := strings.TrimPrefix(rawURL, "https://"+expectedGatewayAPICRDsHost+expectedGatewayAPICRDsPathPrefix)
	if rest == rawURL {
		return rawURL
	}
	if idx := strings.Index(rest, "/"); idx > 0 {
		return rest[:idx]
	}
	return rawURL
}

// applyGatewayAPICRDs installs the standard Gateway API CRDs from the official
// kubernetes-sigs/gateway-api GitHub release. These are applied via Kustomize in
// the GitOps flow (kubernetes/apps/network/kgateway/gateway-api-crds/) but need
// to be present before Flux starts reconciling.
func applyGatewayAPICRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.KubeConfig == "" {
		return fmt.Errorf("kubeconfig path is required for Gateway API CRD installation - ensure KUBECONFIG environment variable is set")
	}

	url, err := getGatewayAPICRDsURL(config.RootDir)
	if err != nil {
		return err
	}

	version := extractGatewayAPIVersion(url)
	logger.Info("Applying Gateway API CRDs %s from %s", version, url)

	if config.DryRun {
		logger.Info("[DRY RUN] Would apply Gateway API CRDs %s from %s", version, url)
		return nil
	}

	if output, err := kubectlCombinedOutput(config, "apply", "--server-side", "--filename", url); err != nil {
		return fmt.Errorf("failed to apply Gateway API CRDs %s from %s: %w\nKubectl output: %s", version, url, err, redactCommandOutput(output))
	}

	logger.Success("Gateway API CRDs %s applied successfully", version)
	return nil
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
	crdsHelmfileTemplate, err := bootstrapGetBootstrapFile("helmfile.d/00-crds.yaml")
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
	output, err := bootstrapHelmfileTemplateOutput(tempDir, config, crdsHelmfilePath)
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

		if applyOutput, err := bootstrapKubectlCombinedIn(config, bytes.NewReader([]byte(crdYaml)), "apply", "--server-side", "--filename", "-"); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w\nOutput: %s", err, redactCommandOutput(applyOutput))
		}

		logger.Info("CRDs applied, waiting for them to be established...")

		// Wait for CRDs to be established
		if err := bootstrapWaitCRDs(config, logger); err != nil {
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
	resources, err := bootstrapGetBootstrapFile("resources.yaml")
	if err != nil {
		return fmt.Errorf("failed to get resources: %w", err)
	}

	// Resolve 1Password references in the YAML content
	logger.Info("Resolving 1Password references in bootstrap resources...")
	resolvedResources, err := bootstrapResolveSecrets(resources, logger)
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
	if output, err := bootstrapKubectlCombinedIn(config, bytes.NewReader([]byte(resolvedResources)), "apply", "--server-side", "--force-conflicts", "--filename", "-"); err != nil {
		return fmt.Errorf("failed to apply resources: %w\n%s", err, redactCommandOutput(output))
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
	// Fast path: if no references present, return as-is
	opRefs := extractOnePasswordReferences(content)
	if len(opRefs) == 0 {
		logger.Info("No 1Password references found to resolve")
		return content, nil
	}
	logger.Info("Found %d 1Password references to resolve", len(opRefs))

	// Use the shared, collision-safe injector so we don’t corrupt secrets
	resolved, err := bootstrapInjectSecrets(content)
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
			if authErr := bootstrapEnsureOPAuth(); authErr != nil {
				return "", fmt.Errorf("1Password signin failed: %w (original: %v)", authErr, err)
			}
			// Retry resolution once after successful signin
			if retryResolved, retryErr := bootstrapInjectSecrets(content); retryErr == nil {
				resolved = retryResolved
				err = nil
			} else {
				return "", fmt.Errorf("secret resolution failed after signin: %w", retryErr)
			}
		} else {
			return "", err
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
	if os.Getenv(constants.EnvDebug) == "1" || os.Getenv("SAVE_RENDERED_CONFIG") == "1" {
		redacted := redactResolved1PasswordValues(content, resolved)
		hash := fmt.Sprintf("%x", md5.Sum([]byte(redacted)))
		debugDir, err := renderedConfigDebugDir()
		if err != nil {
			logger.Warn("Failed to determine rendered configuration debug directory: %v", err)
			return resolved, nil
		}
		filename := filepath.Join(debugDir, fmt.Sprintf("rendered-config-%s.yaml", hash[:8]))
		if err := saveRenderedConfig(redacted, filename, logger); err != nil {
			logger.Warn("Failed to save rendered configuration: %v", err)
		}
	}

	return resolved, nil
}

func renderedConfigDebugDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "homeops-cli", "rendered-configs"), nil
}

func redactResolved1PasswordValues(original, resolved string) string {
	opRefs := extractOnePasswordReferences(original)
	if len(opRefs) == 0 {
		return resolved
	}

	redacted := original
	for _, ref := range opRefs {
		redacted = strings.ReplaceAll(redacted, ref, "<redacted:1password>")
	}
	return redacted
}

// saveRenderedConfig saves the rendered configuration to a file for inspection
func saveRenderedConfig(config, filename string, logger *common.ColorLogger) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", filename, err)
	}

	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("failed to set permissions on %s: %w", filename, err)
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
	clusterSecretStore, err := bootstrapGetBootstrapFile("clustersecretstore.yaml")
	if err != nil {
		return fmt.Errorf("failed to get cluster secret store: %w", err)
	}

	// Resolve 1Password references in the YAML content
	logger.Info("Resolving 1Password references in cluster secret store...")
	resolvedClusterSecretStore, err := bootstrapResolveSecrets(clusterSecretStore, logger)
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
	if output, err := bootstrapKubectlCombinedIn(config, bytes.NewReader([]byte(resolvedClusterSecretStore)),
		"apply", "--namespace", constants.NSExternalSecret, "--server-side", "--force-conflicts", "--filename", "-", "--wait=true"); err != nil {
		return fmt.Errorf("failed to apply cluster secret store: %w\n%s", err, redactCommandOutput(output))
	}

	logger.Info("Cluster secret store applied successfully")
	return nil
}

func syncHelmReleases(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		// Validate clustersecretstore template that would be applied after helmfile
		if err := bootstrapApplySecretStore(config, logger); err != nil {
			return err
		}
		if err := bootstrapValidateSecretStore(logger); err != nil {
			return fmt.Errorf("clustersecretstore template validation failed: %w", err)
		}
		// Test dynamic values template rendering
		if err := bootstrapTestDynamicValues(config, logger); err != nil {
			return fmt.Errorf("dynamic values template test failed: %w", err)
		}
		logger.Info("[DRY RUN] Template validation passed - would sync Helm releases")
		return nil
	}

	// Fix any existing CRDs that may lack proper Helm ownership metadata
	// This prevents Helm from failing when trying to adopt pre-existing CRDs
	if err := bootstrapFixCRDMetadata(config, logger); err != nil {
		logger.Warn("Failed to fix existing CRD metadata (continuing anyway): %v", err)
	}

	// Retry helmfile sync with exponential backoff
	maxAttempts := 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			logger.Info("Helm sync attempt %d/%d", attempt, maxAttempts)
		}

		err := bootstrapExecuteHelmfileSync(config, logger)
		if err == nil {
			logger.Info("Helm releases synced successfully")

			// Check if external-secrets was installed by Helmfile before waiting for it
			logger.Info("Checking if external-secrets is ready...")
			if bootstrapExternalSecretsUp(config, logger) {
				logger.Info("External-secrets found, waiting for webhook to be ready...")
				if err := bootstrapWaitExternalSecrets(config, logger); err != nil {
					logger.Warn("External-secrets webhook not ready after waiting: %v", err)
					logger.Info("ClusterSecretStore will be applied by Flux when external-secrets becomes ready")
				} else {
					// Apply cluster secret store only if webhook is ready
					if err := bootstrapApplySecretStore(config, logger); err != nil {
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
			bootstrapSleep(waitTime)

			// Re-verify API server health before retry
			logger.Info("Re-checking API server connectivity before retry...")
			if err := bootstrapTestAPIConnectivity(config, logger); err != nil {
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

// waitForCRDsEstablished waits for all CRDs to be established using progress-based detection
// It keeps waiting as long as progress is being made, only failing if stuck for too long
func waitForCRDsEstablished(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalFast
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapCRDMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastEstablishedCount := 0

	for {
		elapsed := bootstrapNow().Sub(startTime)

		// Safety net: fail if we've been waiting too long overall
		if elapsed > maxWait {
			return fmt.Errorf("CRDs did not become established after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "crd",
			"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type=='Established')].status}{\"\\n\"}{end}")
		if err != nil {
			logger.Debug("Failed to check CRD status: %v", err)
			bootstrapSleep(checkInterval)
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		allEstablished := true
		establishedCount := 0
		totalCRDs := 0
		var pendingCRDs []string

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
				} else {
					allEstablished = false
					pendingCRDs = append(pendingCRDs, crdName)
				}
			}
		}

		// Success: all CRDs established
		if allEstablished && establishedCount > 0 {
			logger.Success("All %d CRDs are established (took %v)", establishedCount, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress
		if establishedCount > lastEstablishedCount {
			logger.Debug("Progress: %d/%d CRDs established (+%d)", establishedCount, totalCRDs, establishedCount-lastEstablishedCount)
			lastProgressTime = bootstrapNow()
			lastEstablishedCount = establishedCount
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("CRD establishment stalled: no progress for %v (stuck at %d/%d). Pending: %v",
				stallDuration.Round(time.Second), establishedCount, totalCRDs, pendingCRDs)
		}

		// Periodic status update
		if int(elapsed.Seconds())%20 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for CRDs: %d/%d established, %v elapsed", establishedCount, totalCRDs, elapsed.Round(time.Second))
			if len(pendingCRDs) > 0 && len(pendingCRDs) <= 5 {
				logger.Debug("Pending CRDs: %v", pendingCRDs)
			}
		}

		bootstrapSleep(checkInterval)
	}
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
	valuesTemplate, err := bootstrapGetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}

	valuesPath := filepath.Join(templatesDir, "values.yaml.gotmpl")
	if err := os.WriteFile(valuesPath, []byte(valuesTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	// Get embedded apps helmfile content
	appsHelmfileTemplate, err := bootstrapGetBootstrapFile("helmfile.d/01-apps.yaml")
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

	if err := bootstrapRunHelmfileSyncCmd(tempDir, helmfilePath, config); err != nil {
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
	// Only include CRD groups that are actually managed by Helm releases.
	// Gateway API CRDs (gateway.networking.k8s.io) are installed via Kustomize
	// from the official kubernetes-sigs/gateway-api GitHub release, not Helm.
	crdGroups := map[string]struct {
		releaseName      string
		releaseNamespace string
	}{
		"external-secrets.io": {releaseName: "external-secrets", releaseNamespace: constants.NSExternalSecret},
		"cert-manager.io":     {releaseName: "cert-manager", releaseNamespace: constants.NSCertManager},
	}

	// Get all CRDs
	output, err := bootstrapKubectlOutput(config, "get", "crds", "-o", "json")
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
		if output, err := bootstrapKubectlCombined(config, "annotate", "crd", crd.Metadata.Name,
			fmt.Sprintf("meta.helm.sh/release-name=%s", owner.releaseName),
			fmt.Sprintf("meta.helm.sh/release-namespace=%s", owner.releaseNamespace),
			"--overwrite"); err != nil {
			logger.Warn("Failed to add annotations to CRD %s: %v\nOutput: %s",
				crd.Metadata.Name, err, redactCommandOutput(output))
			continue
		}

		if output, err := bootstrapKubectlCombined(config, "label", "crd", crd.Metadata.Name,
			"app.kubernetes.io/managed-by=Helm",
			"--overwrite"); err != nil {
			logger.Warn("Failed to add labels to CRD %s: %v\nOutput: %s",
				crd.Metadata.Name, err, redactCommandOutput(output))
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
	output, err := bootstrapKubectlCombined(config, "cluster-info", "--request-timeout=10s")
	if err != nil {
		logger.Debug("API server connectivity check output: %s", redactCommandOutput(output))
		return fmt.Errorf("cluster-info failed: %w", err)
	}
	return nil
}

// isExternalSecretsInstalled checks if external-secrets is installed in the cluster
// Uses retry logic since the deployment may still be creating after helmfile sync
func isExternalSecretsInstalled(config *BootstrapConfig, logger *common.ColorLogger) bool {
	maxAttempts := constants.BootstrapExtSecInstallAttempts // 1 minute total with 5-second intervals
	checkInterval := bootstrapCheckIntervalNormal

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := bootstrapKubectlRun(config, "get", "deployment", "external-secrets-webhook",
			"-n", constants.NSExternalSecret, "--request-timeout=5s")

		if err == nil {
			logger.Debug("External-secrets deployment found on attempt %d", attempt)
			return true
		}

		if attempt < maxAttempts {
			logger.Debug("External-secrets check attempt %d/%d: not found yet, retrying...", attempt, maxAttempts)
			bootstrapSleep(checkInterval)
		}
	}

	logger.Debug("External-secrets not found after %d attempts", maxAttempts)
	return false
}

// waitForExternalSecretsWebhook waits for the external-secrets webhook to be ready using progress-based detection
func waitForExternalSecretsWebhook(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapExtSecMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastState := ""

	for {
		elapsed := bootstrapNow().Sub(startTime)

		// Safety net: fail if we've been waiting too long overall
		if elapsed > maxWait {
			return fmt.Errorf("external-secrets webhook did not become ready after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		// Check deployment status
		output, err := bootstrapKubectlOutput(config, "get", "deployment", "external-secrets-webhook",
			"-n", constants.NSExternalSecret,
			"--output=jsonpath={.status.readyReplicas}/{.status.replicas}:{.status.conditions[?(@.type=='Available')].status}")
		var currentState string

		if err != nil {
			stallDuration := bootstrapNow().Sub(lastProgressTime)
			if stallDuration > stallTimeout {
				return fmt.Errorf("external-secrets webhook stalled: kubectl get deployment failed for %v: %w",
					stallDuration.Round(time.Second), err)
			}
			bootstrapSleep(checkInterval)
			continue
		}

		currentState = strings.TrimSpace(string(output))

		// Also check if the webhook service has endpoints.
		endpointsOutput, endpointsErr := bootstrapKubectlOutput(config, "get", "endpoints", "external-secrets-webhook",
			"-n", constants.NSExternalSecret,
			"--output=jsonpath={.subsets[*].addresses[*].ip}")
		if endpointsErr == nil && deploymentAndEndpointsReadyFromState(currentState, string(endpointsOutput)) {
			logger.Success("External-secrets webhook is ready (took %v)", elapsed.Round(time.Second))
			return nil
		}
		logger.Debug("Webhook deployment not fully ready yet: %s", currentState)

		// Check for progress (state change)
		if currentState != lastState {
			logger.Debug("External-secrets state change: %s -> %s", lastState, currentState)
			lastProgressTime = bootstrapNow()
			lastState = currentState
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			// Get detailed pod info for debugging
			podOutput, _ := bootstrapKubectlOutput(config, "get", "pods", "-n", constants.NSExternalSecret, "-o", "wide")
			return fmt.Errorf("external-secrets webhook stalled: no progress for %v (state: %s)\nPods:\n%s",
				stallDuration.Round(time.Second), currentState, redactCommandOutput(podOutput))
		}

		// Periodic status update
		if int(elapsed.Seconds())%30 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for external-secrets webhook: state=%s, %v elapsed", currentState, elapsed.Round(time.Second))
			// Show pod status for debugging
			if podOutput, podErr := bootstrapKubectlOutput(config, "get", "pods", "-n", constants.NSExternalSecret, "-o", "wide"); podErr == nil {
				logger.Debug("External-secrets pods:\n%s", redactCommandOutput(podOutput))
			}
		}

		bootstrapSleep(checkInterval)
	}
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

		values, err := bootstrapRenderHelmValues(release, config.RootDir, metricsCollector)
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

// waitForFluxReconciliation waits for Flux to complete its initial reconciliation
// This ensures the cluster is actually ready before bootstrap declares success
func waitForFluxReconciliation(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would wait for Flux reconciliation")
		return nil
	}

	logger.Info("Waiting for Flux controllers to be ready...")

	// Step 1: Wait for Flux source-controller to be running
	if err := bootstrapWaitFluxController(config, logger, "source-controller"); err != nil {
		return fmt.Errorf("source-controller not ready: %w", err)
	}

	// Step 2: Wait for Flux kustomize-controller to be running
	if err := bootstrapWaitFluxController(config, logger, "kustomize-controller"); err != nil {
		return fmt.Errorf("kustomize-controller not ready: %w", err)
	}

	// Step 3: Wait for Flux helm-controller to be running
	if err := bootstrapWaitFluxController(config, logger, "helm-controller"); err != nil {
		return fmt.Errorf("helm-controller not ready: %w", err)
	}

	logger.Info("Flux controllers are ready, waiting for initial reconciliation...")

	// Step 4: Wait for the GitRepository to be ready
	if err := bootstrapWaitGitRepository(config, logger); err != nil {
		// Not fatal - Flux may still be cloning
		logger.Warn("GitRepository not ready yet: %v", err)
		logger.Info("Flux is still syncing - cluster is functional but reconciliation is in progress")
		return nil
	}

	// Step 5: Wait for the flux-system Kustomization to reconcile
	if err := bootstrapWaitFluxKS(config, logger, "cluster"); err != nil {
		// Not fatal - initial reconcile can take time
		logger.Warn("Flux Kustomization 'cluster' not ready yet: %v", err)
		logger.Info("Flux is still reconciling - cluster is functional")
		return nil
	}

	logger.Success("Flux initial reconciliation complete")
	return nil
}

// waitForFluxController waits for a specific Flux controller deployment to be ready using progress-based detection
func waitForFluxController(config *BootstrapConfig, logger *common.ColorLogger, controllerName string) error {
	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapFluxMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastState := ""

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("%s did not become ready after %v (max wait exceeded)", controllerName, elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "deployment", controllerName,
			"-n", constants.NSFluxSystem,
			"--output=jsonpath={.status.readyReplicas}/{.status.replicas}")
		var currentState string

		if err != nil {
			stallDuration := bootstrapNow().Sub(lastProgressTime)
			if stallDuration > stallTimeout {
				return fmt.Errorf("%s stalled: kubectl get deployment failed for %v: %w",
					controllerName, stallDuration.Round(time.Second), err)
			}
			bootstrapSleep(checkInterval)
			continue
		}

		currentState = strings.TrimSpace(string(output))
		if deploymentReadyFromState(currentState) {
			logger.Debug("Flux %s is ready (took %v)", controllerName, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress
		if currentState != lastState {
			logger.Debug("Flux %s state: %s", controllerName, currentState)
			lastProgressTime = bootstrapNow()
			lastState = currentState
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("%s stalled: no progress for %v (state: %s)", controllerName, stallDuration.Round(time.Second), currentState)
		}

		if int(elapsed.Seconds())%30 == 0 && elapsed.Seconds() > 0 {
			logger.Debug("Waiting for Flux %s: state=%s, %v elapsed", controllerName, currentState, elapsed.Round(time.Second))
		}
		bootstrapSleep(checkInterval)
	}
}

// waitForGitRepositoryReady waits for the flux-system GitRepository to be ready using progress-based detection
func waitForGitRepositoryReady(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapFluxMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastState := ""

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("GitRepository did not become ready after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		// Check GitRepository status with more detail
		output, err := bootstrapKubectlOutput(config, "get", "gitrepository", "flux-system",
			"-n", constants.NSFluxSystem,
			"--output=jsonpath={.status.conditions[?(@.type=='Ready')].status}:{.status.conditions[?(@.type=='Ready')].reason}:{.status.artifact.revision}")
		currentState := "not-found"

		if err == nil {
			currentState = strings.TrimSpace(string(output))
			parts := strings.Split(currentState, ":")
			if len(parts) >= 1 && parts[0] == "True" {
				logger.Debug("GitRepository flux-system is ready (took %v)", elapsed.Round(time.Second))
				return nil
			}
		}

		// Check for progress (state change means something is happening)
		if currentState != lastState {
			logger.Debug("GitRepository state: %s", currentState)
			lastProgressTime = bootstrapNow()
			lastState = currentState
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			// Get diagnostic info
			diagOutput, _ := bootstrapKubectlOutput(config, "get", "gitrepository", "-n", constants.NSFluxSystem, "-o", "wide")
			return fmt.Errorf("GitRepository stalled: no progress for %v (state: %s)\n%s",
				stallDuration.Round(time.Second), currentState, redactCommandOutput(diagOutput))
		}

		if int(elapsed.Seconds())%30 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for GitRepository: state=%s, %v elapsed", currentState, elapsed.Round(time.Second))
		}
		bootstrapSleep(checkInterval)
	}
}

// waitForFluxKustomizationReady waits for a specific Flux Kustomization to be ready using progress-based detection
func waitForFluxKustomizationReady(config *BootstrapConfig, logger *common.ColorLogger, ksName string) error {
	checkInterval := bootstrapCheckIntervalNormal
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapFluxMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastState := ""

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("kustomization %s did not become ready after %v (max wait exceeded)", ksName, elapsed.Round(time.Second))
		}

		// Check Kustomization status with more detail
		output, err := bootstrapKubectlOutput(config, "get", "kustomization", ksName,
			"-n", constants.NSFluxSystem,
			"--output=jsonpath={.status.conditions[?(@.type=='Ready')].status}:{.status.conditions[?(@.type=='Ready')].reason}:{.status.lastAppliedRevision}")
		currentState := "not-found"

		if err == nil {
			currentState = strings.TrimSpace(string(output))
			parts := strings.Split(currentState, ":")
			if len(parts) >= 1 && parts[0] == "True" {
				logger.Debug("Kustomization %s is ready (took %v)", ksName, elapsed.Round(time.Second))
				return nil
			}
		}

		// Check for progress
		if currentState != lastState {
			logger.Debug("Kustomization %s state: %s", ksName, currentState)
			lastProgressTime = bootstrapNow()
			lastState = currentState
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			// Get diagnostic info
			diagOutput, _ := bootstrapKubectlOutput(config, "get", "kustomization", "-n", constants.NSFluxSystem, "-o", "wide")
			return fmt.Errorf("kustomization %s stalled: no progress for %v (state: %s)\n%s",
				ksName, stallDuration.Round(time.Second), currentState, redactCommandOutput(diagOutput))
		}

		if int(elapsed.Seconds())%30 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for Kustomization '%s': state=%s, %v elapsed", ksName, currentState, elapsed.Round(time.Second))
		}
		bootstrapSleep(checkInterval)
	}
}
