package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"homeops-cli/internal/common"
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
// only via the resolve return path.
func identityRedactor(s string) string { return s }

func defaultOpRead(reference string) (common.CommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opCommandTimeout)
	defer cancel()
	return common.RunCommand(ctx, common.CommandOptions{
		Name:     "op",
		Args:     []string{"read", reference},
		Redactor: identityRedactor,
	})
}

// SetOpReadFnForTesting overrides the underlying op-read implementation for
// the duration of a test. Returns a restore function the caller should defer.
func SetOpReadFnForTesting(fn func(reference string) (common.CommandResult, error)) func() {
	old := opReadFn
	opReadFn = fn
	return func() { opReadFn = old }
}

// resolveOp retrieves a secret from 1Password using the CLI with retry logic.
//
// The secret value is returned to callers via the success return path only. It
// is never included in any error message. Error classification is based on
// stderr — stdout is treated as the (possibly partial) secret payload and must
// not leak into diagnostic output.
func resolveOp(reference string) (string, error) {
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
	case strings.Contains(lower, "executable file not found"):
		return fmt.Errorf("the 1Password CLI ('op') is not installed but %s requires it — install it (https://developer.1password.com/docs/cli/get-started/) or point this secret at another backend (env://, file://, cmd://) in your homeops config", reference)
	case strings.Contains(lower, "not found"):
		return fmt.Errorf("1Password secret %s not found", reference)
	case strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "not signed in"),
		strings.Contains(lower, "no active session"):
		return fmt.Errorf("1Password CLI not authenticated. Please run 'op signin'")
	}
	return nil
}

// EnsureOpAuth verifies 1Password CLI authentication and performs an
// interactive signin if necessary. It returns an error if authentication
// cannot be confirmed. Callers should only invoke this when op:// references
// are actually in play (see config.UsesOpReferences).
func EnsureOpAuth() error {
	// Check if already authenticated
	cmd := common.Command("op", "whoami", "--format=json")
	output, err := cmd.Output()
	if err == nil {
		var result map[string]interface{}
		if jsonErr := json.Unmarshal(output, &result); jsonErr == nil {
			return nil // Already authenticated
		}
	}

	// Not authenticated, attempt interactive signin
	signin := common.Command("op", "signin")
	signin.Stdin = os.Stdin
	signin.Stdout = os.Stdout
	signin.Stderr = os.Stderr
	if err := signin.Run(); err != nil {
		return fmt.Errorf("failed to sign in to 1Password: %w — install the op CLI (https://developer.1password.com/docs/cli/get-started/) or switch your homeops config secrets to env://, file://, or cmd:// references", err)
	}

	// Verify authentication after signin
	verify := common.Command("op", "whoami", "--format=json")
	verifyOutput, err := verify.Output()
	if err != nil {
		return fmt.Errorf("authentication verification failed: %w", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(verifyOutput, &result); err != nil {
		return fmt.Errorf("invalid 1Password response after signin: %w", err)
	}

	return nil
}
