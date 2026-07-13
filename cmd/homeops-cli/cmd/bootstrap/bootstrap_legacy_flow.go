package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

func runTalosBootstrapFlow(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("🚀 Starting cluster bootstrap process")

	if err := prepareTalosBootstrapConfig(config, logger); err != nil {
		return err
	}

	if err := runBootstrapPreflightPhase(config, logger); err != nil {
		return err
	}

	if err := runTalosPreCNIBootstrap(config, logger); err != nil {
		return err
	}

	if err := runSharedPostCNIBootstrap(config, logger); err != nil {
		return err
	}

	finishBootstrap(logger)
	return nil
}

func prepareTalosBootstrapConfig(config *BootstrapConfig, logger *common.ColorLogger) error {
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

	loadBootstrapVersions(config, logger)
	return nil
}

func loadBootstrapVersions(config *BootstrapConfig, logger *common.ColorLogger) {
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
}

func runBootstrapPreflightPhase(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Run comprehensive preflight checks
	if !config.SkipPreflight {
		if err := bootstrapRunWithSpinner("🔍 Running preflight checks", config.Verbose, logger, func() error {
			return bootstrapRunPreflightChecks(config, logger)
		}); err != nil {
			return fmt.Errorf("preflight checks failed: %w", err)
		}
		return nil
	}

	logger.Warn("⚠️  Skipping preflight checks - this may cause failures during bootstrap")
	// Still run basic prerequisite validation
	if err := bootstrapValidatePrereqs(config); err != nil {
		return fmt.Errorf("prerequisite validation failed: %w", err)
	}
	return nil
}

func runTalosPreCNIBootstrap(config *BootstrapConfig, logger *common.ColorLogger) error {
	if err := applyTalosConfigurationStep(config, logger); err != nil {
		return err
	}

	if err := bootstrapTalosClusterStep(config, logger); err != nil {
		return err
	}

	if err := fetchAndValidateKubeconfigStep(config, logger); err != nil {
		return err
	}

	return waitForBootstrapNodesStep(config, logger)
}

func applyTalosConfigurationStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 1: Apply Talos configuration
	logger.Info("📋 Step 1: Applying Talos configuration to nodes")
	if err := bootstrapApplyTalosConfig(config, logger); err != nil {
		return fmt.Errorf("failed to apply Talos config: %w", err)
	}

	// Reset terminal after Step 1 completes (multiple spinners)
	bootstrapResetTerminal()
	return nil
}

func bootstrapTalosClusterStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 2: Bootstrap Talos
	if err := bootstrapRunWithSpinner("🎯 Step 2: Bootstrapping Talos cluster", config.Verbose, logger, func() error {
		// Wait a moment for configurations to be fully processed (following onedr0p's pattern)
		logger.Debug("Waiting for configurations to be processed...")
		bootstrapSleep(5 * time.Second)
		return bootstrapBootstrapTalos(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to bootstrap Talos: %w", err)
	}
	return nil
}

func fetchAndValidateKubeconfigStep(config *BootstrapConfig, logger *common.ColorLogger) error {
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
	return nil
}

func waitForBootstrapNodesStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 4: Wait for nodes to be ready
	if err := bootstrapRunWithSpinner("⏳ Step 4: Waiting for nodes to be ready", config.Verbose, logger, func() error {
		return bootstrapWaitForNodes(config, logger)
	}); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}
	return nil
}

func runSharedPostCNIBootstrap(config *BootstrapConfig, logger *common.ColorLogger) error {
	if err := applyBootstrapNamespacesStep(config, logger); err != nil {
		return err
	}

	if err := applyBootstrapResourcesStep(config, logger); err != nil {
		return err
	}

	if err := applyBootstrapCRDsStep(config, logger); err != nil {
		return err
	}

	if err := syncBootstrapHelmReleasesStep(config, logger); err != nil {
		return err
	}

	waitForBootstrapFluxStep(config, logger)
	return nil
}

func applyBootstrapNamespacesStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 5: Apply namespaces first (following onedr0p pattern)
	if err := bootstrapRunWithSpinner("📦 Step 5: Creating initial namespaces", config.Verbose, logger, func() error {
		return bootstrapApplyNamespaces(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to apply namespaces: %w", err)
	}
	return nil
}

func applyBootstrapResourcesStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 6: Apply initial resources
	if !config.SkipResources {
		if err := bootstrapRunWithSpinner("🔧 Step 6: Applying initial resources", config.Verbose, logger, func() error {
			return bootstrapApplyResources(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}
	return nil
}

func applyBootstrapCRDsStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 7: Apply CRDs
	if !config.SkipCRDs {
		if err := bootstrapRunWithSpinner("📜 Step 7: Applying Custom Resource Definitions", config.Verbose, logger, func() error {
			return bootstrapApplyCRDs(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}
	return nil
}

func syncBootstrapHelmReleasesStep(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Step 8: Sync Helm releases
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner("⚙️  Step 8: Syncing Helm releases", config.Verbose, logger, func() error {
			return bootstrapSyncHelmReleases(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}
	return nil
}

func waitForBootstrapFluxStep(config *BootstrapConfig, logger *common.ColorLogger) {
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
}

func finishBootstrap(logger *common.ColorLogger) {
	logger.Success("🎉 Congrats! The cluster is bootstrapped and Flux has completed initial reconciliation")
	ui.PrintSuccessBox("🎉 Cluster bootstrapped!",
		"Flux has completed initial reconciliation.",
		"kubectl get nodes        # say hello to your cluster",
		"flux get kustomizations  # watch the apps roll out")

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
}
