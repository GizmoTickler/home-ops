package common

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsure1PasswordAuth_SigninFlow(t *testing.T) {
	tmp := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Skip("unsupported on Windows in this environment")
	}
	state := filepath.Join(tmp, ".auth")
	script := "" +
		"#!/usr/bin/env bash\n" +
		"set -e\n" +
		"cmd=$1\n" +
		"shift || true\n" +
		"if [[ \"$cmd\" == \"whoami\" ]]; then\n" +
		"  if [[ -f '" + filepath.Base(state) + "' ]]; then\n" +
		"    echo '{\"user\":\"ok\"}'\n" +
		"    exit 0\n" +
		"  else\n" +
		"    echo 'not signed in' >&2; exit 1\n" +
		"  fi\n" +
		"elif [[ \"$cmd\" == \"signin\" ]]; then\n" +
		"  : > '" + filepath.Base(state) + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unknown' >&2; exit 1\n"
	// write script and state file path in tmp; script uses relative paths, so chdir into tmp
	writeFakeOp(t, tmp, script)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmp+":"+oldPath); err != nil {
		t.Fatalf("failed to set PATH: %v", err)
	}
	defer func() { _ = os.Setenv("PATH", oldPath) }()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir to tmp: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	if err := Ensure1PasswordAuth(); err != nil {
		t.Fatalf("Ensure1PasswordAuth failed: %v", err)
	}
}
