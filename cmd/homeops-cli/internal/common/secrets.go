package common

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// opCommandTimeout caps how long a single `op read` call is allowed to run
// before being cancelled. Each retry gets its own timeout window.
const opCommandTimeout = 30 * time.Second

// opReadFn invokes `op read` and is overridable in tests. The default routes
// through RunCommand with an identity Redactor so secret values returned via
// stdout are not corrupted by output redaction.
var opReadFn = defaultOpRead

// identityRedactor preserves command output verbatim. We use it for `op read`
// because the secret value IS the stdout payload — applying the standard
// secret-label redactor would corrupt valid secrets that happen to look like
// `password=...` or similar. Stdout is therefore handled with extra care: it
// must never appear in error messages, and the returned value is forwarded
// only via the SecretReader return path.
func identityRedactor(s string) string { return s }

func defaultOpRead(reference string) (CommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opCommandTimeout)
	defer cancel()
	return RunCommand(ctx, CommandOptions{
		Name:     "op",
		Args:     []string{"read", reference},
		Redactor: identityRedactor,
	})
}

// SetOpReadFnForTesting overrides the underlying op-read implementation for
// the duration of a test. Returns a restore function the caller should defer.
func SetOpReadFnForTesting(fn func(reference string) (CommandResult, error)) func() {
	old := opReadFn
	opReadFn = fn
	return func() { opReadFn = old }
}

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
//
// The secret value is returned to callers via the success return path only. It is
// never included in any error message. Error classification is based on stderr —
// stdout is treated as the (possibly partial) secret payload and must not leak
// into diagnostic output.
func read1PasswordSecret(reference string) (string, error) {
	const maxAttempts = 3
	for attempts := 0; attempts < maxAttempts; attempts++ {
		result, err := opReadFn(reference)
		if err == nil {
			secretValue := strings.TrimSpace(result.Stdout)
			if secretValue == "" {
				return "", fmt.Errorf("1Password secret %s returned empty value", reference)
			}
			return secretValue, nil
		}

		if classified := classifyOpFailure(reference, result.Stderr); classified != nil {
			return "", classified
		}

		// Generic failure that may be transient — retry with backoff. Do not include
		// stdout/stderr in any error path; both could carry secret-looking content.
		if attempts < maxAttempts-1 {
			time.Sleep(time.Duration(100*(attempts+1)) * time.Millisecond)
		}
	}

	return "", fmt.Errorf("failed to read 1Password secret %s after %d attempts", reference, maxAttempts)
}

// classifyOpFailure returns a redaction-safe error for known op-CLI failure
// modes. Returns nil if the failure is not recognised, leaving the caller to
// retry or surface a generic message. Only stderr is inspected — stdout would
// contain the (partial) secret payload on success and is therefore treated as
// untrusted for error-message purposes.
func classifyOpFailure(reference, stderr string) error {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "not found"):
		return fmt.Errorf("1Password secret %s not found", reference)
	case strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "not signed in"),
		strings.Contains(lower, "no active session"):
		return fmt.Errorf("1Password CLI not authenticated. Please run 'op signin'")
	}
	return nil
}

// Get1PasswordSecret retrieves a secret from 1Password using the CLI with retry logic.
func Get1PasswordSecret(reference string) (string, error) {
	return read1PasswordSecret(reference)
}

// Get1PasswordSecretSilent retrieves a secret from 1Password, returns empty string on failure
// This matches the existing pattern in talos.go for fallback scenarios.
// Routes through the shared op-read executor so timeouts and the no-output
// guarantee for errors are consistent with read1PasswordSecret.
func Get1PasswordSecretSilent(reference string) string {
	result, err := opReadFn(reference)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
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
