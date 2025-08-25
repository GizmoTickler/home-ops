package common

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Get1PasswordSecret retrieves a secret from 1Password using the CLI with retry logic
// This matches the sophisticated error handling from bootstrap.go
func Get1PasswordSecret(reference string) (string, error) {
	maxAttempts := 3
	for attempts := 0; attempts < maxAttempts; attempts++ {
		cmd := exec.Command("op", "read", reference)
		output, err := cmd.CombinedOutput()
		if err == nil {
			secretValue := strings.TrimSpace(string(output))
			if secretValue == "" {
				return "", fmt.Errorf("1Password secret %s returned empty value", reference)
			}
			return secretValue, nil
		}

		// Check for specific error types
		outputStr := string(output)
		if strings.Contains(outputStr, "not found") {
			return "", fmt.Errorf("1Password secret %s not found", reference)
		}
		if strings.Contains(outputStr, "unauthorized") || strings.Contains(outputStr, "not signed in") {
			return "", fmt.Errorf("1Password CLI not authenticated. Please run 'op signin'")
		}

		// If this isn't the last attempt, wait a bit before retrying
		if attempts < maxAttempts-1 {
			// Simple exponential backoff: 100ms, 200ms
			time.Sleep(time.Duration(100*(attempts+1)) * time.Millisecond)
		}
	}

	return "", fmt.Errorf("failed to read 1Password secret %s after %d attempts", reference, maxAttempts)
}

// Get1PasswordSecretSilent retrieves a secret from 1Password, returns empty string on failure
// This matches the existing pattern in talos.go for fallback scenarios
func Get1PasswordSecretSilent(reference string) string {
	cmd := exec.Command("op", "read", reference)
	output, err := cmd.Output()
	if err != nil {
		// Silently fail and return empty string to allow fallback to env vars
		return ""
	}
	return strings.TrimSpace(string(output))
}

// InjectSecrets replaces op:// references with actual secrets from 1Password
func InjectSecrets(content string) (string, error) {
	// Regex to match op://vault/item/field patterns
	opRegex := regexp.MustCompile(`op://([^/]+)/([^/]+)/([^\s"']+)`)
	result := content

	// Find all op:// references
	matches := opRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return result, nil // No secrets to inject
	}

	// Process each secret reference
	for _, match := range matches {
		fullMatch := match[0]

		// Get secret from 1Password
		secret, err := Get1PasswordSecret(fullMatch)
		if err != nil {
			return "", fmt.Errorf("failed to inject 1Password secret %s: %w", fullMatch, err)
		}

		// Replace the op:// reference with the actual secret
		result = strings.ReplaceAll(result, fullMatch, secret)
	}

	return result, nil
}
