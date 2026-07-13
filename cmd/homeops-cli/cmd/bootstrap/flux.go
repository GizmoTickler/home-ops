package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/metrics"
)

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
	defer metricsCollector.LogReport(logger)

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
