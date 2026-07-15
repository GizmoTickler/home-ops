package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/secrets"
	"homeops-cli/internal/state"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/ui"

	"github.com/spf13/cobra"
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
	// SkipKubeadm (flatcar provider) skips kubeadm init/join (steps 1+3) and runs
	// only the post-CNI bootstrap (Cilium/helmfile/Flux) against an already-built
	// control plane; the kubeconfig is still fetched from node0 (step 2).
	SkipKubeadm bool
	Verbose     bool
	// FreshPKI (flatcar provider) skips restoring the persisted cluster PKI from
	// 1Password before `kubeadm init`, so kubeadm mints a NEW cluster CA. Default
	// (false) reuses the persisted PKI for a stable identity across rebuilds.
	FreshPKI bool
	// Provider selects the node-provisioning path: "flatcar" (default,
	// kubeadm-over-SSH) or "talos" (legacy, retained for rollback). Only the
	// pre-CNI steps differ; the generic post-CNI steps are shared.
	Provider string
	// Plan prints a complete, config-derived bootstrap plan and returns before
	// confirmations, preflight checks, secret resolution, or infrastructure I/O.
	Plan         bool
	CheckSecrets bool
	Output       string
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
	bootstrapConfirm          = ui.Confirm
	bootstrapInteractive      = ui.IsInteractive
	runBootstrapFn            = runBootstrap
	bootstrapRunWithSpinner   = ui.RunWithSpinner
	bootstrapResetTerminal    = ui.ResetTerminal
	bootstrapWorkingDirectory = common.GetWorkingDirectory
	bootstrapGetVersions      = versionconfig.GetVersions
	bootstrapLookPath         = exec.LookPath
	bootstrapEnsureOPAuth     = secrets.EnsureOpAuth
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
	bootstrapGetBootstrapFile     = templates.GetBootstrapFile
	bootstrapGetBootstrapTemplate = templates.GetBootstrapTemplate
	bootstrapGetTalosTemplate     = templates.GetTalosTemplate
	bootstrapGetFlatcarTemplate   = templates.GetFlatcarTemplate
	bootstrapInjectSecrets        = secrets.Inject
	bootstrapResolveSecrets       = resolve1PasswordReferences
	bootstrapRenderMachineConfig  = renderMachineConfigFromEmbedded
	bootstrapGetMachineType       = getMachineTypeFromEmbedded
	bootstrapMergeTalosConfigs    = mergeConfigsWithTalosctl
	bootstrapGetTalosNodes        = getTalosNodes
	bootstrapApplyNodeConfig      = applyNodeConfig
	bootstrapApplyNodeConfigTry   = applyNodeConfigWithRetry
	bootstrapValidateEtcd         = validateEtcdRunning
	bootstrapSaveKubeconfig       = func(content []byte, logger *common.ColorLogger) error {
		return state.NewKubeconfigStore(versionconfig.Get().State.Kubeconfig).Save(content, logger)
	}
	bootstrapPatchKubeconfig        = patchKubeconfigForBootstrap
	bootstrapGetRandomController    = getRandomController
	bootstrapRunPreflightChecks     = runPreflightChecks
	bootstrapValidatePrereqs        = validatePrerequisites
	bootstrapApplyTalosConfig       = applyTalosConfig
	bootstrapBootstrapTalos         = bootstrapTalos
	bootstrapRunFlatcar             = runBootstrapFlatcar
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
	bootstrapPreflightChecks     = []preflightCheck{
		{fn: checkToolAvailability},
		{fn: checkEnvironmentFiles},
		{fn: checkNetworkConnectivity},
		{fn: checkDNSResolution},
		// Serial: may launch an interactive `op signin`.
		{fn: check1PasswordAuthPreflight, serial: true},
		// Serial: resolves op:// references, so it needs the auth check first.
		{fn: checkMachineConfigRendering, serial: true},
		{fn: checkTalosNodes},
	}
)

// preflightCheck pairs a check with its scheduling constraint: independent
// checks run concurrently, serial ones run in order afterwards.
type preflightCheck struct {
	fn     func(*BootstrapConfig, *common.ColorLogger) *PreflightResult
	serial bool
}

func NewCommand() *cobra.Command {
	var config BootstrapConfig

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap the cluster (Flatcar/kubeadm) and cluster applications",
		Long: `Bootstrap a complete cluster. Defaults to the Flatcar Container Linux +
kubeadm provider:
- Delivering Ignition + running kubeadm init/join on all nodes
- Installing the Cilium CNI and waiting for nodes to be Ready
- Installing CRDs and resources
- Syncing Helm releases

Pass --provider talos to run the legacy Talos path (apply machine config,
talosctl bootstrap) instead.`,
		Example: `  # Print the complete config-derived plan without touching infrastructure
  homeops-cli bootstrap --plan

  # Include availability-only secret checks; values/references are never printed
  homeops-cli bootstrap --plan --check-secrets --output json

  # Exercise operational dry-run branches
  homeops-cli bootstrap --dry-run

  # Full Flatcar/kubeadm bootstrap
  homeops-cli bootstrap

  # Re-run only the post-CNI phase against an existing control plane
  homeops-cli bootstrap --skip-kubeadm

  # Legacy Talos path
  homeops-cli bootstrap --provider talos`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if config.CheckSecrets && !config.Plan {
				return fmt.Errorf("--check-secrets requires --plan")
			}
			if config.Output != "table" && config.Output != "json" {
				return fmt.Errorf("unsupported output format %q (table, json)", config.Output)
			}
			if !config.Plan && cmd.Flags().Changed("output") {
				return fmt.Errorf("--output requires --plan")
			}
			if config.Plan {
				plan, err := bootstrapBuildPlanFn(config)
				if err != nil {
					return err
				}
				rendered, err := renderBootstrapPlan(plan, config.Output)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
				return nil
			}
			// Bare invocation on a terminal (CLI or interactive menu): offer
			// the mode (dry-run vs real) and skip options first, so dry runs
			// are one choice away instead of flag-only knowledge.
			if cmd.Flags().NFlag() == 0 && bootstrapInteractive() {
				if err := promptBootstrapOptions(&config, common.NewColorLogger()); err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return err
				}
			}
			// Bootstrapping a cluster deserves an explicit yes (--yes/-y
			// skips the prompt, dry runs never mutate anything).
			if !config.DryRun {
				ok, err := bootstrapConfirm("Bootstrap the cluster now? (preflight, PKI, kubeadm, CRDs, helmfile)", false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return err
				}
				if !ok {
					common.NewColorLogger().Info("Bootstrap cancelled")
					return nil
				}
			}
			return runBootstrapFn(&config)
		},
	}

	// Add flags - default root-dir to git repository root
	defaultRootDir := bootstrapWorkingDirectory()
	cmd.Flags().StringVar(&config.RootDir, "root-dir", defaultRootDir, "Root directory of the project")
	cmd.Flags().StringVar(&config.KubeConfig, "kubeconfig", os.Getenv(constants.EnvKubeconfig), "Path to kubeconfig file")
	cmd.Flags().StringVar(&config.TalosConfig, "talosconfig", os.Getenv(constants.EnvTalosconfig), "Path to talosconfig file (legacy --provider talos only; ignored for Flatcar)")
	cmd.Flags().StringVar(&config.K8sVersion, "k8s-version", os.Getenv(constants.EnvKubernetesVersion), "Kubernetes version")
	cmd.Flags().StringVar(&config.TalosVersion, "talos-version", os.Getenv(constants.EnvTalosVersion), "Talos version (legacy --provider talos only; ignored for Flatcar)")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Perform a dry run without making changes")
	cmd.Flags().BoolVar(&config.SkipCRDs, "skip-crds", false, "Skip CRD installation")
	cmd.Flags().BoolVar(&config.SkipResources, "skip-resources", false, "Skip resource creation")
	cmd.Flags().BoolVar(&config.SkipHelmfile, "skip-helmfile", false, "Skip Helmfile sync")
	cmd.Flags().BoolVar(&config.SkipPreflight, "skip-preflight", false, "Skip preflight checks (not recommended)")
	cmd.Flags().BoolVar(&config.SkipKubeadm, "skip-kubeadm", false, "Flatcar: skip kubeadm init/join; run only post-CNI bootstrap against an existing control plane")
	cmd.Flags().BoolVar(&config.FreshPKI, "fresh-pki", false, "Flatcar: mint a NEW cluster CA instead of restoring the persisted PKI from 1Password (breaks existing kubeconfigs)")
	cmd.Flags().BoolVar(&config.Plan, "plan", false, "print the complete ordered bootstrap plan and exit without making changes")
	cmd.Flags().BoolVar(&config.CheckSecrets, "check-secrets", false, "with --plan, check whether listed secret references currently resolve without printing values")
	cmd.Flags().StringVarP(&config.Output, "output", "o", "table", "plan output format: table or json")
	cmd.Flags().BoolVarP(&config.Verbose, "verbose", "v", false, "Enable verbose output (shows all logs, disables spinners)")
	cmd.Flags().StringVar(&config.Provider, "provider", "flatcar", "Node provisioning provider: flatcar (kubeadm, default) or talos (legacy)")
	_ = cmd.RegisterFlagCompletionFunc("provider", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"flatcar", "talos"}, cobra.ShellCompDirectiveNoFileComp
	})

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

	// A dry run's whole output IS the plan: log every step directly instead
	// of hiding the "[DRY RUN] would ..." lines behind spinners.
	if config.DryRun {
		config.Verbose = true
	}

	// Provider dispatch: Flatcar/kubeadm is the default for the CLI (the
	// `--provider` flag defaults to "flatcar", so a bare `homeops-cli bootstrap`
	// runs this path). It replaces the Talos-specific pre-CNI steps but reuses
	// the generic post-CNI steps. The legacy Talos path is reached via an
	// explicit `--provider talos` — and, for tests that build a config struct
	// directly, by leaving Provider empty.
	if strings.EqualFold(config.Provider, "flatcar") {
		return bootstrapRunFlatcar(config)
	}

	return runTalosBootstrapFlow(config, logger)
}
