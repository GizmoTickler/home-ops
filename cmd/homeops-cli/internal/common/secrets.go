package common

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OpRefRegex matches 1Password references of the form op://vault/item/field
var OpRefRegex = regexp.MustCompile(`op://([^/]+)/([^/]+)/([^\s"']+)`)

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

// Get1PasswordSecretsBatch retrieves multiple secrets from 1Password in parallel
// Returns a map of reference -> secret value. Failed lookups are omitted from the map.
func Get1PasswordSecretsBatch(references []string) map[string]string {
	if len(references) == 0 {
		return make(map[string]string)
	}

	results := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, ref := range references {
		wg.Add(1)
		go func(reference string) {
			defer wg.Done()
			secret := Get1PasswordSecretSilent(reference)
			if secret != "" {
				mu.Lock()
				results[reference] = secret
				mu.Unlock()
			}
		}(ref)
	}

	wg.Wait()
	return results
}

// InjectSecrets replaces op:// references with actual secrets from 1Password
func InjectSecrets(content string) (string, error) {
	// Use shared regex for matching op:// references
	opRegex := OpRefRegex

	// Caches for successes and failures to avoid repeated CLI calls
	cache := make(map[string]string)
	errCache := make(map[string]error)

	// Replace using a single regex pass to avoid substring collisions
	result := opRegex.ReplaceAllStringFunc(content, func(fullMatch string) string {
		if v, ok := cache[fullMatch]; ok {
			return v
		}
		if _, failed := errCache[fullMatch]; failed {
			return fullMatch
		}
		secret, err := Get1PasswordSecret(fullMatch)
		if err != nil {
			errCache[fullMatch] = err
			return fullMatch
		}
		cache[fullMatch] = secret
		return secret
	})

	// Aggregate detailed errors if any
	if len(errCache) > 0 {
		// Build a descriptive error including each reference and cause
		var b strings.Builder
		b.WriteString("failed to resolve 1Password references:\n")
		for ref, err := range errCache {
			b.WriteString(" - ")
			b.WriteString(ref)
			b.WriteString(": ")
			b.WriteString(err.Error())
			b.WriteByte('\n')
		}
		// Wrap the aggregated message in a single error
		msg := strings.TrimRight(b.String(), "\n")
		return "", fmt.Errorf("%s", msg)
	}

	// Extra safety: if any references still remain (e.g., in comments), report the first
	if strings.Contains(result, "op://") {
		rem := opRegex.FindString(result)
		return "", fmt.Errorf("unresolved 1Password reference remains: %s", rem)
	}

	return result, nil
}
