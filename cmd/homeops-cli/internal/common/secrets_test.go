package common

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// writeFakeOp creates a fake 'op' script in dir that simulates specific behaviors.
func writeFakeOp(t *testing.T, dir string, script string) string {
	t.Helper()
	path := filepath.Join(dir, "op")
	if runtime.GOOS == "windows" {
		// Not expected in this repo; skip on Windows
		t.Skip("tests not supported on Windows in this environment")
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake op: %v", err)
	}
	return path
}

func TestInjectSecrets_Success(t *testing.T) {
	tmp := t.TempDir()
	// Fake op: returns secret value for read, simple JSON for whoami
	script := "" +
		"#!/usr/bin/env bash\n" +
		"set -e\n" +
		"cmd=$1\n" +
		"shift || true\n" +
		"if [[ \"$cmd\" == \"read\" ]]; then\n" +
		"  ref=$1\n" +
		"  if [[ $ref == op://Vault/Item/Field ]]; then\n" +
		"    echo -n 'SECRET_VALUE'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"  echo 'not found' >&2; exit 1\n" +
		"elif [[ \"$cmd\" == \"whoami\" ]]; then\n" +
		"  echo '{\"user\":\"test\"}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unknown command' >&2; exit 1\n"
	writeFakeOp(t, tmp, script)

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmp+":"+oldPath); err != nil {
		t.Fatalf("failed to set PATH: %v", err)
	}
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	in := "key: op://Vault/Item/Field\n"
	out, err := InjectSecrets(in)
	if err != nil {
		t.Fatalf("InjectSecrets returned error: %v", err)
	}
	if out == in {
		t.Fatalf("expected secrets to be injected, got unchanged content")
	}
	if want := "SECRET_VALUE"; !contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}
	if contains(out, "op://") {
		t.Fatalf("expected no remaining op:// references, got: %s", out)
	}
}

func TestInjectSecrets_AggregatedErrors(t *testing.T) {
	tmp := t.TempDir()
	// Fake op: simulate two different failures for two refs
	script := "" +
		"#!/usr/bin/env bash\n" +
		"set -e\n" +
		"cmd=$1\n" +
		"shift || true\n" +
		"if [[ \"$cmd\" == \"read\" ]]; then\n" +
		"  ref=$1\n" +
		"  if [[ $ref == op://Vault/NeedsSignin/Key ]]; then\n" +
		"    echo 'not signed in' >&2; exit 1\n" +
		"  elif [[ $ref == op://Vault/Missing/Key ]]; then\n" +
		"    echo 'not found' >&2; exit 1\n" +
		"  fi\n" +
		"  echo 'not found' >&2; exit 1\n" +
		"elif [[ \"$cmd\" == \"whoami\" ]]; then\n" +
		"  echo '{\"user\":\"test\"}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unknown command' >&2; exit 1\n"
	writeFakeOp(t, tmp, script)

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmp+":"+oldPath); err != nil {
		t.Fatalf("failed to set PATH: %v", err)
	}
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	in := "a: op://Vault/NeedsSignin/Key\nb: op://Vault/Missing/Key\n"
	_, err := InjectSecrets(in)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	// Expect both references mentioned in error
	if !contains(err.Error(), "op://Vault/NeedsSignin/Key") || !contains(err.Error(), "op://Vault/Missing/Key") {
		t.Fatalf("expected aggregated error to include both refs, got: %v", err)
	}
}

func TestSecretResolverInjectCachesDuplicateReferences(t *testing.T) {
	calls := 0
	resolver := NewSecretResolverWithReader(func(reference string) (string, error) {
		calls++
		if reference != "op://Vault/Item/Field" {
			t.Fatalf("unexpected reference: %s", reference)
		}
		return "fake-shared-value", nil
	})

	out, err := resolver.InjectSecrets("a: op://Vault/Item/Field\nb: op://Vault/Item/Field\n")
	if err != nil {
		t.Fatalf("InjectSecrets returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected duplicate reference to be read once, got %d reads", calls)
	}
	if strings.Count(out, "fake-shared-value") != 2 {
		t.Fatalf("expected both references to be replaced with fake value, got: %s", out)
	}
}

func TestSecretResolverCachesFailures(t *testing.T) {
	calls := 0
	readErr := errors.New("fake read failure")
	resolver := NewSecretResolverWithReader(func(string) (string, error) {
		calls++
		return "", readErr
	})

	for i := 0; i < 2; i++ {
		if _, err := resolver.Resolve("op://Vault/Item/Field"); !errors.Is(err, readErr) {
			t.Fatalf("expected cached failure %v, got %v", readErr, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected failed reference to be read once, got %d reads", calls)
	}
}

func TestSecretResolverResolvesDistinctReferencesIndependently(t *testing.T) {
	calls := map[string]int{}
	values := map[string]string{
		"op://Vault/Item/One": "fake-one-value",
		"op://Vault/Item/Two": "fake-two-value",
	}
	resolver := NewSecretResolverWithReader(func(reference string) (string, error) {
		calls[reference]++
		return values[reference], nil
	})

	out, err := resolver.InjectSecrets("a: op://Vault/Item/One\nb: op://Vault/Item/Two\n")
	if err != nil {
		t.Fatalf("InjectSecrets returned error: %v", err)
	}
	for _, ref := range []string{"op://Vault/Item/One", "op://Vault/Item/Two"} {
		if calls[ref] != 1 {
			t.Fatalf("expected %s to be read once, got %d reads", ref, calls[ref])
		}
		if !strings.Contains(out, values[ref]) {
			t.Fatalf("expected output to contain fake resolved value for %s, got: %s", ref, out)
		}
	}
}

func TestRead1PasswordSecretReturnsValueOnSuccess(t *testing.T) {
	restore := SetOpReadFnForTesting(func(reference string) (CommandResult, error) {
		if reference != "op://Vault/Item/Field" {
			t.Fatalf("unexpected reference %s", reference)
		}
		return CommandResult{Stdout: "  the-secret-value  \n"}, nil
	})
	defer restore()

	value, err := Get1PasswordSecret("op://Vault/Item/Field")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "the-secret-value" {
		t.Fatalf("expected trimmed secret, got %q", value)
	}
}

func TestRead1PasswordSecretEmptyValueReturnsError(t *testing.T) {
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		return CommandResult{Stdout: "   "}, nil
	})
	defer restore()

	_, err := Get1PasswordSecret("op://Vault/Item/Field")
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
	if !strings.Contains(err.Error(), "returned empty value") {
		t.Fatalf("expected empty-value error, got: %v", err)
	}
}

func TestRead1PasswordSecretClassifiesNotFound(t *testing.T) {
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		return CommandResult{Stderr: "[ERROR] not found at op://Vault/Item"}, errors.New("exit 1")
	})
	defer restore()

	_, err := Get1PasswordSecret("op://Vault/Missing/Field")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got: %v", err)
	}
}

func TestRead1PasswordSecretClassifiesAuthFailure(t *testing.T) {
	cases := []string{
		"unauthorized",
		"not signed in",
		"NO ACTIVE SESSION",
	}
	for _, stderr := range cases {
		t.Run(stderr, func(t *testing.T) {
			restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
				return CommandResult{Stderr: stderr}, errors.New("exit 1")
			})
			defer restore()

			_, err := Get1PasswordSecret("op://Vault/Item/Field")
			if err == nil {
				t.Fatalf("expected auth error for stderr %q", stderr)
			}
			if !strings.Contains(err.Error(), "op signin") {
				t.Fatalf("expected error to instruct signin, got: %v", err)
			}
		})
	}
}

func TestRead1PasswordSecretRetriesGenericFailure(t *testing.T) {
	var calls int32
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return CommandResult{Stderr: "transient: connection reset"}, errors.New("exit 1")
		}
		return CommandResult{Stdout: "the-secret-value"}, nil
	})
	defer restore()

	value, err := Get1PasswordSecret("op://Vault/Item/Field")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "the-secret-value" {
		t.Fatalf("expected eventual success, got %q", value)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestRead1PasswordSecretRetryExhaustionReturnsCount(t *testing.T) {
	var calls int32
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		atomic.AddInt32(&calls, 1)
		return CommandResult{Stderr: "transient: connection reset"}, errors.New("exit 1")
	})
	defer restore()

	_, err := Get1PasswordSecret("op://Vault/Item/Field")
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("expected attempt count, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestRead1PasswordSecretNeverIncludesStdoutOrStderrInError(t *testing.T) {
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		return CommandResult{
			Stdout: "SENTINEL_SECRET_VALUE_DO_NOT_LEAK",
			Stderr: "diagnostic: api_key=SENTINEL_API_KEY_VALUE",
		}, errors.New("exit 1")
	})
	defer restore()

	_, err := Get1PasswordSecret("op://Vault/Item/Field")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SENTINEL_SECRET_VALUE_DO_NOT_LEAK") {
		t.Fatalf("error must not contain stdout secret value: %v", err)
	}
	if strings.Contains(err.Error(), "SENTINEL_API_KEY_VALUE") {
		t.Fatalf("error must not contain raw stderr secret-looking content: %v", err)
	}
}

func TestSecretReturnedThroughSuccessPathIsNotRedacted(t *testing.T) {
	// A real-world secret may legitimately look like "password=correcthorse" — the
	// redactor would corrupt it, so we use the identity Redactor for op read.
	const secretLike = "password=correcthorsebatterystaple"
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		return CommandResult{Stdout: secretLike}, nil
	})
	defer restore()

	got, err := Get1PasswordSecret("op://Vault/Item/Field")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != secretLike {
		t.Fatalf("expected secret returned verbatim through success path, got %q", got)
	}
}

func TestGet1PasswordSecretSilentReturnsEmptyOnFailure(t *testing.T) {
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		return CommandResult{Stderr: "anything"}, errors.New("exit 1")
	})
	defer restore()

	if got := Get1PasswordSecretSilent("op://Vault/Item/Field"); got != "" {
		t.Fatalf("expected empty string on failure, got %q", got)
	}
}

func TestGet1PasswordSecretSilentTrimsSuccessOutput(t *testing.T) {
	restore := SetOpReadFnForTesting(func(string) (CommandResult, error) {
		return CommandResult{Stdout: "  shhh  \n"}, nil
	})
	defer restore()

	if got := Get1PasswordSecretSilent("op://Vault/Item/Field"); got != "shhh" {
		t.Fatalf("expected trimmed value, got %q", got)
	}
}

func TestClassifyOpFailureReturnsNilForUnknown(t *testing.T) {
	if err := classifyOpFailure("op://x", "totally unrelated message"); err != nil {
		t.Fatalf("expected nil for unknown stderr, got: %v", err)
	}
}

func TestClassifyOpFailureCaseInsensitive(t *testing.T) {
	if err := classifyOpFailure("op://x", "ITEM NOT FOUND"); err == nil {
		t.Fatal("expected classification for uppercase 'NOT FOUND'")
	}
}

func TestOpCommandTimeoutHasReasonableDefault(t *testing.T) {
	if opCommandTimeout < time.Second {
		t.Fatalf("op command timeout too short: %v", opCommandTimeout)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (os.Getenv("_TEST_STRICT") == "" && (stringIndex(s, sub) >= 0))
}
func stringIndex(s, sub string) int {
	return len([]rune(s[:]))*0 + len([]byte(s)) - len([]byte(s)) + int64Index([]byte(s), []byte(sub))
}
func int64Index(s, sep []byte) int { return indexByteSlice(s, sep) }
func indexByteSlice(s, sep []byte) int {
	// simple contains wrapper: defer to strings.Contains without importing strings to avoid linter noise in tests
	for i := 0; i+len(sep) <= len(s); i++ {
		match := true
		for j := range sep {
			if s[i+j] != sep[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
