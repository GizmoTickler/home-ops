package common

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCommandHelpers(t *testing.T) {
	restoreFactory := SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
		switch name {
		case "echo-out":
			return exec.Command("sh", "-c", "printf 'stdout'")
		case "echo-both":
			return exec.Command("sh", "-c", "printf 'stdout'; printf 'stderr' >&2")
		case "cat-stdin":
			return exec.Command("sh", "-c", "cat")
		default:
			return exec.Command("sh", "-c", "printf '%s' \"$0\"", name)
		}
	})
	defer restoreFactory()

	cmd := Command("custom-tool", "arg1")
	require.NotNil(t, cmd)
	assert.Equal(t, "sh", cmd.Args[0])

	output, err := Output("echo-out")
	require.NoError(t, err)
	assert.Equal(t, "stdout", string(output))

	combined, err := CombinedOutput("echo-both")
	require.NoError(t, err)
	assert.Contains(t, string(combined), "stdout")
	assert.Contains(t, string(combined), "stderr")

	var stdout bytes.Buffer
	require.NoError(t, RunInteractive(bytes.NewBufferString("from-stdin"), &stdout, io.Discard, "cat-stdin"))
	assert.Equal(t, "from-stdin", stdout.String())
}

func TestLookPathOverride(t *testing.T) {
	restore := SetLookPathFuncForTesting(func(file string) (string, error) {
		return "/tmp/fake-bin/" + file, nil
	})
	defer restore()

	path, err := LookPath("kubectl")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/fake-bin/kubectl", path)
}

func TestStructuredLoggerFilesystemAndSecretsHelpers(t *testing.T) {
	logger, err := NewStructuredLogger("debug")
	require.NoError(t, err)

	withFields := logger.WithFields(map[string]interface{}{"component": "test"})
	require.NotNil(t, withFields)

	withFields.Debug("debug", zap.String("key", "value"))
	withFields.Info("info", zap.String("key", "value"))
	withFields.Warn("warn", zap.String("key", "value"))
	withFields.Error("error", zap.String("key", "value"))
	withFields.Debugf("debugf %s", "value")
	withFields.Infof("infof %s", "value")
	withFields.Warnf("warnf %s", "value")
	withFields.Errorf("errorf %s", "value")
	_ = withFields.Sync()

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "file.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("content"), 0o644))

	assert.True(t, FileExists(tmpFile))
	assert.False(t, FileExists(filepath.Join(tmpDir, "missing.txt")))
	assert.True(t, DirExists(tmpDir))
	assert.False(t, DirExists(tmpFile))
	assert.False(t, DirExists(filepath.Join(tmpDir, "missing-dir")))
}

func TestSilentAndBatchSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tests not supported on Windows in this environment")
	}

	tmp := t.TempDir()
	script := "" +
		"#!/usr/bin/env bash\n" +
		"set -e\n" +
		"cmd=$1\n" +
		"shift || true\n" +
		"if [[ \"$cmd\" == \"read\" ]]; then\n" +
		"  ref=$1\n" +
		"  case \"$ref\" in\n" +
		"    op://Vault/Item/One) echo -n 'ONE'; exit 0 ;;\n" +
		"    op://Vault/Item/Two) echo -n 'TWO'; exit 0 ;;\n" +
		"    *) echo 'missing' >&2; exit 1 ;;\n" +
		"  esac\n" +
		"fi\n" +
		"echo 'unexpected command' >&2\n" +
		"exit 1\n"
	writeFakeOp(t, tmp, script)

	oldPath := os.Getenv("PATH")
	require.NoError(t, os.Setenv("PATH", tmp+":"+oldPath))
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	assert.Equal(t, "ONE", Get1PasswordSecretSilent("op://Vault/Item/One"))
	assert.Equal(t, "", Get1PasswordSecretSilent("op://Vault/Item/Missing"))

	results := Get1PasswordSecretsBatch([]string{
		"op://Vault/Item/One",
		"op://Vault/Item/Two",
		"op://Vault/Item/Missing",
	})
	assert.Equal(t, map[string]string{
		"op://Vault/Item/One": "ONE",
		"op://Vault/Item/Two": "TWO",
	}, results)
	assert.Empty(t, Get1PasswordSecretsBatch(nil))
}
