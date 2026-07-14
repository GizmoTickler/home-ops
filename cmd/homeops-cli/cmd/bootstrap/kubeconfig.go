package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"

	yamlv3 "gopkg.in/yaml.v3"
)

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

	// Ensure directory exists for kubeconfig (owner-only: holds credentials)
	kubeconfigDir := filepath.Dir(config.KubeConfig)
	if err := os.MkdirAll(kubeconfigDir, 0700); err != nil {
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
	content, err := os.ReadFile(kubeconfigPath) // #nosec G304 -- kubeconfig path is an explicit local bootstrap artifact path
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
