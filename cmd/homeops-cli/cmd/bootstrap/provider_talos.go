package bootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/secrets"
	"homeops-cli/internal/templates"
)

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

	// Debug: Check if the resolved config still contains secret references
	if remaining := secrets.ListReferences(resolvedConfig); len(remaining) > 0 {
		logger.Warn("Warning: Resolved config still contains secret references")
		for _, ref := range remaining {
			logger.Warn("Unresolved secret reference: %s", ref)
		}
	}

	return []byte(resolvedConfig), nil
}

// mergeConfigsWithTalosctl merges base and patch configs using talosctl (onedr0p approach)
func mergeConfigsWithTalosctl(baseConfig, patchConfig []byte) ([]byte, error) {
	// The configs have 1Password references already resolved, so keep them in
	// a private 0700 directory for the duration of the merge instead of
	// loose files in the shared temp root.
	mergeDir, err := os.MkdirTemp(bootstrapTalosTempDir, "talos-merge-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create config merge temp dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(mergeDir) // Ignore cleanup errors
	}()

	baseFile, err := os.CreateTemp(mergeDir, "talos-base-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create base config temp file: %w", err)
	}
	defer func() {
		_ = baseFile.Close() // Ignore cleanup errors
	}()

	patchFile, err := os.CreateTemp(mergeDir, "talos-patch-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create patch config temp file: %w", err)
	}
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
