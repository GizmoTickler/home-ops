package common

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/constants"

	"github.com/fatih/color"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextAndGitHelpers(t *testing.T) {
	cmd := CommandWithContext(context.Background(), "sh", "-c", "printf hello")
	require.NotNil(t, cmd)
	assert.Equal(t, "sh", cmd.Args[0])

	combined, err := RunCommandWithContext(context.Background(), "sh", "-c", "printf out && printf err >&2")
	require.NoError(t, err)
	assert.Contains(t, string(combined), "out")
	assert.Contains(t, string(combined), "err")

	redactedCombined, err := RunCommandWithContext(context.Background(), "sh", "-c", "printf 'token: synthetic-test-fixture' >&2; exit 1")
	require.Error(t, err)
	assert.Contains(t, string(redactedCombined), "token: <redacted>")
	assert.NotContains(t, string(redactedCombined), "synthetic-test-fixture")

	output, err := RunCommandWithContextOutput(context.Background(), "sh", "-c", "printf stdout-only")
	require.NoError(t, err)
	assert.Equal(t, "stdout-only", string(output))

	repoDir := t.TempDir()
	nestedDir := filepath.Join(repoDir, "a", "b", "c")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))

	gitRoot, err := FindGitRoot(nestedDir)
	require.NoError(t, err)
	assert.Equal(t, repoDir, gitRoot)

	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(nestedDir))
	expectedWD, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	actualWD, err := filepath.EvalSymlinks(GetWorkingDirectory())
	require.NoError(t, err)
	assert.Equal(t, expectedWD, actualWD)

	_, err = FindGitRoot(t.TempDir())
	require.Error(t, err)
}

func TestValidationAndLoggerHelpers(t *testing.T) {
	filtered := filterConnectEnvVars([]string{
		"PATH=/bin",
		"OP_CONNECT_HOST=https://connect",
		"OP_CONNECT_TOKEN=secret",
		"HOME=/tmp/home",
	})
	assert.Equal(t, []string{"PATH=/bin", "HOME=/tmp/home"}, filtered)

	t.Setenv("HOMEOPS_TEST_ENV", "set")
	require.NoError(t, CheckEnv("HOMEOPS_TEST_ENV"))
	require.Error(t, CheckEnv("HOMEOPS_TEST_ENV", "HOMEOPS_TEST_MISSING"))

	toolDir := t.TempDir()
	toolPath := filepath.Join(toolDir, "fake-tool")
	require.NoError(t, os.WriteFile(toolPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, CheckCLI(toolPath))
	require.Error(t, CheckCLI(filepath.Join(toolDir, "missing-tool")))

	SetGlobalLogLevel("debug")
	assert.Equal(t, "debug", GetGlobalLogLevel())
	logger := NewColorLogger()
	assert.Equal(t, DebugLevel, logger.Level)
	logger.SetQuiet(true)
	assert.True(t, logger.IsQuiet())
	logger.SetQuiet(false)
	assert.False(t, logger.IsQuiet())

	globalA := Logger()
	SetGlobalLogLevel("error")
	globalB := Logger()
	assert.Same(t, globalA, globalB)
	assert.Equal(t, ErrorLevel, globalB.Level)

	oldNoColor := color.NoColor
	oldOutput := color.Output
	oldError := color.Error
	color.NoColor = true
	buf := &bytes.Buffer{}
	color.Output = buf
	color.Error = buf
	defer func() {
		color.NoColor = oldNoColor
		color.Output = oldOutput
		color.Error = oldError
	}()

	logger.Level = DebugLevel
	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")
	logger.Success("success message")

	output := buf.String()
	assert.Contains(t, output, "DEBUG")
	assert.Contains(t, output, "INFO")
	assert.Contains(t, output, "WARN")
	assert.Contains(t, output, "ERROR")
	assert.Contains(t, output, "SUCCESS")

	t.Setenv(constants.EnvDebug, "1")
	assert.Equal(t, DebugLevel, NewColorLogger().Level)

	structured, err := NewStructuredLogger("warn")
	require.NoError(t, err)
	withFields := structured.WithFields(map[string]interface{}{"component": "test"})
	require.NotNil(t, withFields)
	require.NotNil(t, withFields.logger)
	require.NotNil(t, withFields.sugar)

	assert.Equal(t, parseLogLevel("debug").String(), "debug")
	assert.Equal(t, parseLogLevel("warn").String(), "warn")
	assert.Equal(t, parseLogLevel("error").String(), "error")
	assert.Equal(t, parseLogLevel("unknown").String(), "info")
}

func TestSaveKubeconfigTo1PasswordFiltersConnectEnv(t *testing.T) {
	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte(`#!/bin/sh
set -eu
if env | grep -q '^OP_CONNECT_HOST='; then
  echo "unexpected OP_CONNECT_HOST" >&2
  exit 97
fi
if env | grep -q '^OP_CONNECT_TOKEN='; then
  echo "unexpected OP_CONNECT_TOKEN" >&2
  exit 98
fi
if [ "$1" = "item" ] && [ "$2" = "edit" ]; then
  exit 0
fi
echo "unexpected command" >&2
exit 1
`), 0o755))

	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OP_CONNECT_HOST", "https://connect.local")
	t.Setenv("OP_CONNECT_TOKEN", "token")

	err := SaveKubeconfigTo1Password([]byte("apiVersion: v1\n"), NewColorLogger())
	require.NoError(t, err)
}

func TestPullPushAndLookupKubeconfigIn1Password(t *testing.T) {
	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte(`#!/bin/sh
set -eu
if [ "$1" = "item" ] && [ "$2" = "get" ]; then
  printf '{"files":[{"id":"file-123","name":"kubeconfig"},{"id":"other","name":"note"}]}'
  exit 0
fi
if [ "$1" = "read" ]; then
  [ "$2" = "op://Infrastructure/kubeconfig/file-123" ] || exit 1
  [ "$3" = "--out-file" ] || exit 1
  printf 'apiVersion: v1\nclusters: []\n' > "$4"
  exit 0
fi
if [ "$1" = "item" ] && [ "$2" = "edit" ]; then
  exit 0
fi
echo "unexpected command" >&2
exit 1
`), 0o755))

	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fileID, err := getKubeconfigFileID()
	require.NoError(t, err)
	assert.Equal(t, "file-123", fileID)

	destPath := filepath.Join(t.TempDir(), "config", "kubeconfig")
	require.NoError(t, PullKubeconfigFrom1Password(destPath, NewColorLogger()))
	content, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "apiVersion: v1")

	info, err := os.Stat(destPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, PushKubeconfigTo1Password(destPath, NewColorLogger()))
}

func TestGetKubeconfigFileIDErrors(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		scriptDir := t.TempDir()
		opPath := filepath.Join(scriptDir, "op")
		require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nprintf '{'\n"), 0o755))
		t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := getKubeconfigFileID()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse item")
	})

	t.Run("missing kubeconfig attachment", func(t *testing.T) {
		scriptDir := t.TempDir()
		opPath := filepath.Join(scriptDir, "op")
		require.NoError(t, os.WriteFile(opPath, []byte(`#!/bin/sh
printf '{"files":[{"id":"other","name":"notes"}]}'
`), 0o755))
		t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := getKubeconfigFileID()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no kubeconfig file found")
	})
}
