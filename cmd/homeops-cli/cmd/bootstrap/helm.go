package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
)

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
	if err := os.MkdirAll(templatesDir, 0o750); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}

	// Create values.yaml.gotmpl in templates subdirectory
	valuesTemplate, err := bootstrapGetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}

	valuesPath := filepath.Join(templatesDir, "values.yaml.gotmpl")
	if err := os.WriteFile(valuesPath, []byte(valuesTemplate), 0o600); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	// Get embedded apps helmfile content
	appsHelmfileTemplate, err := bootstrapGetBootstrapFile("helmfile.d/01-apps.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded apps helmfile: %w", err)
	}

	// The apps helmfile doesn't need templating, write it directly
	helmfilePath := filepath.Join(tempDir, "01-apps.yaml")
	if err := os.WriteFile(helmfilePath, []byte(appsHelmfileTemplate), 0o600); err != nil {
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
