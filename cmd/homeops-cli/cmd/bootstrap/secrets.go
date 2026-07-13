package bootstrap

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/secrets"
)

// get1PasswordSecret retrieves a secret through the shared scheme resolver.
func get1PasswordSecret(reference string) (string, error) {
	return secrets.Resolve(reference)
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
	if remaining := secrets.ListReferences(resolved); len(remaining) > 0 {
		logger.Warn("Warning: Resolved content still contains secret references")
		for _, ref := range remaining {
			logger.Warn("Unresolved reference: %s", ref)
		}
	} else {
		logger.Debug("✅ No secret references remain in rendered configuration")
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
