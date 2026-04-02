package ui

import (
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicPromptFallbacks(t *testing.T) {
	t.Setenv("HOMEOPS_NO_INTERACTIVE", "1")

	withStdin(t, "y\n", func() {
		ok, err := Confirm("continue", false)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	withStdin(t, "2\n", func() {
		choice, err := Choose("pick", []string{"one", "two"})
		require.NoError(t, err)
		assert.Equal(t, "two", choice)
	})

	withStdin(t, "1,2\n", func() {
		choices, err := ChooseMulti("pick", []string{"one", "two"}, 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"one", "two"}, choices)
	})

	withStdin(t, "hello\n", func() {
		value, err := Input("prompt", "")
		require.NoError(t, err)
		assert.Equal(t, "hello", value)
	})
}

func TestSpinnerAndNamespaceHelpers(t *testing.T) {
	scriptDir := t.TempDir()
	kubectlPath := filepath.Join(scriptDir, "kubectl")
	require.NoError(t, os.WriteFile(kubectlPath, []byte(`#!/bin/sh
case "$*" in
  "get namespaces -o jsonpath={.items[*].metadata.name}") printf "default kube-system flux-system" ;;
  *) exit 1 ;;
esac
`), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOMEOPS_NO_INTERACTIVE", "1")

	withStdin(t, "2\n", func() {
		namespace, err := SelectNamespace("Select namespace:", false)
		require.NoError(t, err)
		assert.Equal(t, "kube-system", namespace)
	})

	namespaces, err := GetNamespaces()
	require.NoError(t, err)
	assert.Equal(t, []string{"default", "kube-system", "flux-system"}, namespaces)

	require.NoError(t, Spin("echo", "sh", "-c", "printf ok >/dev/null"))
	output, err := SpinWithOutput("echo", "sh", "-c", "printf output")
	require.NoError(t, err)
	assert.Equal(t, "output", output)

	_, _, err = testutil.CaptureOutput(func() {
		ResetTerminal()
	})
	require.NoError(t, err)
}

func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "stdin-*")
	require.NoError(t, err)
	_, err = tmpFile.WriteString(input)
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	file, err := os.Open(tmpFile.Name())
	require.NoError(t, err)
	defer func() {
		_ = file.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	oldStdin := os.Stdin
	os.Stdin = file
	defer func() { os.Stdin = oldStdin }()

	fn()
}
