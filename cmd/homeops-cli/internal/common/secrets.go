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

	// Cache to avoid duplicate CLI calls when the same ref appears multiple times
	cache := make(map[string]string)

	// Replace using a single regex pass to avoid substring collisions
	result := opRegex.ReplaceAllStringFunc(content, func(fullMatch string) string {
		// If cached, return immediately
		if v, ok := cache[fullMatch]; ok {
			return v
		}
		// Fetch from 1Password
		secret, err := Get1PasswordSecret(fullMatch)
		if err != nil {
			// On error, keep the original reference so caller can detect unresolved refs
			// We cannot return an error from inside ReplaceAllStringFunc, so store a sentinel
			// value that the outer scope can detect by presence of the unresolved reference.
			// For robustness, leave it unchanged here.
			return fullMatch
		}
		cache[fullMatch] = secret
		return secret
	})

	// If any op:// references remain, surface an error so callers can handle/report it
	if strings.Contains(result, "op://") {
		// Find the first remaining reference for error context
		rem := opRegex.FindString(result)
		return "", fmt.Errorf("failed to resolve 1Password reference: %s", rem)
	}

	return result, nil
}
