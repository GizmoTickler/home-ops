package common

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OpRefRegex matches 1Password references of the form op://vault/item/field
var OpRefRegex = regexp.MustCompile(`op://([^/]+)/([^/]+)/([^\s"']+)`)

// SecretReader resolves a single 1Password reference.
type SecretReader func(reference string) (string, error)

type secretResolution struct {
	value string
	err   error
	ready chan struct{}
}

// SecretResolver caches 1Password reads for the lifetime of a single operation.
// Values stay in memory only and are never persisted by the resolver.
type SecretResolver struct {
	mu     sync.Mutex
	reader SecretReader
	cache  map[string]*secretResolution
}

// NewSecretResolver creates an operation-scoped resolver backed by the 1Password CLI.
func NewSecretResolver() *SecretResolver {
	return NewSecretResolverWithReader(read1PasswordSecret)
}

// NewSecretResolverWithReader creates an operation-scoped resolver with a custom reader.
func NewSecretResolverWithReader(reader SecretReader) *SecretResolver {
	if reader == nil {
		reader = read1PasswordSecret
	}
	return &SecretResolver{
		reader: reader,
		cache:  make(map[string]*secretResolution),
	}
}

// Resolve returns the value for a 1Password reference, caching successes and failures.
func (r *SecretResolver) Resolve(reference string) (string, error) {
	if r == nil {
		return read1PasswordSecret(reference)
	}

	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]*secretResolution)
	}
	if cached, ok := r.cache[reference]; ok {
		r.mu.Unlock()
		<-cached.ready
		return cached.value, cached.err
	}
	reader := r.reader
	if reader == nil {
		reader = read1PasswordSecret
	}
	resolution := &secretResolution{ready: make(chan struct{})}
	r.cache[reference] = resolution
	r.mu.Unlock()

	value, err := reader(reference)

	r.mu.Lock()
	resolution.value = value
	resolution.err = err
	close(resolution.ready)
	r.mu.Unlock()

	return value, err
}

// InjectSecrets replaces op:// references with secrets resolved through this resolver.
func (r *SecretResolver) InjectSecrets(content string) (string, error) {
	opRefs := OpRefRegex.FindAllString(content, -1)
	if len(opRefs) == 0 {
		return content, nil
	}

	seen := make(map[string]struct{}, len(opRefs))
	orderedRefs := make([]string, 0, len(opRefs))
	errCache := make(map[string]error)
	for _, ref := range opRefs {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		orderedRefs = append(orderedRefs, ref)
		if _, err := r.Resolve(ref); err != nil {
			errCache[ref] = err
		}
	}

	if len(errCache) > 0 {
		var b strings.Builder
		b.WriteString("failed to resolve 1Password references:\n")
		for _, ref := range orderedRefs {
			err, ok := errCache[ref]
			if !ok {
				continue
			}
			b.WriteString(" - ")
			b.WriteString(ref)
			b.WriteString(": ")
			b.WriteString(err.Error())
			b.WriteByte('\n')
		}
		msg := strings.TrimRight(b.String(), "\n")
		return "", fmt.Errorf("%s", msg)
	}

	result := OpRefRegex.ReplaceAllStringFunc(content, func(fullMatch string) string {
		secret, err := r.Resolve(fullMatch)
		if err != nil {
			return fullMatch
		}
		return secret
	})

	if strings.Contains(result, "op://") {
		rem := OpRefRegex.FindString(result)
		return "", fmt.Errorf("unresolved 1Password reference remains: %s", rem)
	}

	return result, nil
}

// read1PasswordSecret retrieves a secret from 1Password using the CLI with retry logic.
func read1PasswordSecret(reference string) (string, error) {
	maxAttempts := 3
	for attempts := 0; attempts < maxAttempts; attempts++ {
		cmd := Command("op", "read", reference)
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

// Get1PasswordSecret retrieves a secret from 1Password using the CLI with retry logic.
func Get1PasswordSecret(reference string) (string, error) {
	return read1PasswordSecret(reference)
}

// Get1PasswordSecretSilent retrieves a secret from 1Password, returns empty string on failure
// This matches the existing pattern in talos.go for fallback scenarios
func Get1PasswordSecretSilent(reference string) string {
	cmd := Command("op", "read", reference)
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
	resolver := NewSecretResolverWithReader(func(reference string) (string, error) {
		secret := Get1PasswordSecretSilent(reference)
		if secret == "" {
			return "", fmt.Errorf("1Password secret %s returned empty value", reference)
		}
		return secret, nil
	})

	for _, ref := range references {
		wg.Add(1)
		go func(reference string) {
			defer wg.Done()
			secret, err := resolver.Resolve(reference)
			if err == nil && secret != "" {
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
	return NewSecretResolver().InjectSecrets(content)
}
