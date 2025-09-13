package onepassword

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"crypto/md5"
	"os"

	"go.uber.org/zap"
)

// OpRefRegex matches 1Password references of the form op://vault/item/field
var OpRefRegex = regexp.MustCompile(`op://([^/]+)/([^/]+)/([^\s"']+)`)

// GetSecret retrieves a secret from 1Password using the CLI with retry logic
func GetSecret(reference string) (string, error) {
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

// GetSecretSilent retrieves a secret from 1Password, returns empty string on failure
func GetSecretSilent(reference string) string {
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
		secret, err := GetSecret(fullMatch)
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

func ResolveReferencesInContent(content string, log *zap.SugaredLogger) (string, error) {
	// Debug: Show lines containing the problematic secret before resolution
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "secretboxEncryptionSecret") {
			log.Debugf("Line %d with secretboxEncryptionSecret: '%s'", i+1, line)
		}
	}

	// Fast path: if no references present, return as-is
	opRefs := ExtractReferences(content)
	if len(opRefs) == 0 {
		log.Info("No 1Password references found to resolve")
		return content, nil
	}
	log.Infof("Found %d 1Password references to resolve", len(opRefs))

	// Use the shared, collision-safe injector so we don’t corrupt secrets
	resolved, err := InjectSecrets(content)
	if err != nil {
		log.Warnf("Secret resolution reported an error: %v", err)
		// If we still have refs, list them to aid debugging
		remainingRefs := ExtractReferences(content)
		for _, ref := range remainingRefs {
			log.Warnf("Unresolved reference: %s", ref)
		}

		// If error indicates unauthenticated op CLI, attempt interactive signin and retry once
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not authenticated") || strings.Contains(errStr, "not signed in") || strings.Contains(errStr, "please run 'op signin'") {
			log.Info("Attempting 1Password CLI signin due to authentication error...")
			if authErr := Ensure1PasswordAuth(); authErr != nil {
				return "", fmt.Errorf("1Password signin failed: %w (original: %v)", authErr, err)
			}
			// Retry resolution once after successful signin
			if retryResolved, retryErr := InjectSecrets(content); retryErr == nil {
				resolved = retryResolved
				err = nil
			} else {
				return "", fmt.Errorf("secret resolution failed after signin: %w", retryErr)
			}
		} else {
			return "", err
		}
	}

	// Debug: Show the final result for secretboxEncryptionSecret after resolution
	finalLines := strings.Split(resolved, "\n")
	for i, line := range finalLines {
		if strings.Contains(line, "secretboxEncryptionSecret") {
			log.Debugf("Final line %d with secretboxEncryptionSecret: '%s'", i+1, line)
		}
	}

	// Optional validation message
	if strings.Contains(resolved, "op://") {
		log.Warn("Warning: Resolved content still contains 1Password references")
		remainingRefs := ExtractReferences(resolved)
		for _, ref := range remainingRefs {
			log.Warnf("Unresolved reference: %s", ref)
		}
	} else {
		log.Debug("✅ No 1Password references remain in rendered configuration")
	}

	// Save rendered configuration for validation if debug is enabled
	if os.Getenv("DEBUG") == "1" || os.Getenv("SAVE_RENDERED_CONFIG") == "1" {
		hash := fmt.Sprintf("%x", md5.Sum([]byte(resolved)))
		filename := fmt.Sprintf("rendered-config-%s.yaml", hash[:8])
		if err := saveRenderedConfig(resolved, filename, log); err != nil {
			log.Warnf("Failed to save rendered configuration: %v", err)
		}
	}

	return resolved, nil
}

func saveRenderedConfig(config, filename string, log *zap.SugaredLogger) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Warnf("Failed to close file: %v", err)
		}
	}()

	_, err = file.WriteString(config)
	if err != nil {
		return fmt.Errorf("failed to write to file %s: %w", filename, err)
	}

	log.Debugf("Saved rendered configuration to %s for validation", filename)
	return nil
}
