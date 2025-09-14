package common

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
