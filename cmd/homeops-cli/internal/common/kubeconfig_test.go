package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveKubeconfigTo1PasswordRedactsFailedOpOutput(t *testing.T) {
	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte(`#!/bin/sh
set -eu
if [ "$1" = "item" ] && [ "$2" = "edit" ] && [ "${5:-}" = "kubeconfig[delete]" ]; then
  exit 0
fi
if [ "$1" = "item" ] && [ "$2" = "edit" ]; then
  printf 'client_secret=SENTINEL_CLIENT_SECRET_VALUE\n'
  printf 'password: SENTINEL_PASSWORD_VALUE\n' >&2
  exit 42
fi
echo "unexpected command" >&2
exit 1
`), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := SaveKubeconfigTo1Password([]byte("apiVersion: v1\n"), NewColorLogger())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update kubeconfig file in 1Password")
	assert.Contains(t, err.Error(), "client_secret=<redacted>")
	assert.Contains(t, err.Error(), "password: <redacted>")
	assert.NotContains(t, err.Error(), "SENTINEL_CLIENT_SECRET_VALUE")
	assert.NotContains(t, err.Error(), "SENTINEL_PASSWORD_VALUE")
}

func TestPullKubeconfigFrom1PasswordRedactsFailedOpOutput(t *testing.T) {
	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte(`#!/bin/sh
set -eu
if [ "$1" = "item" ] && [ "$2" = "get" ]; then
  printf '{"files":[{"id":"file-123","name":"kubeconfig"}]}'
  exit 0
fi
if [ "$1" = "read" ]; then
  printf 'token=SENTINEL_TOKEN_VALUE\n'
  printf 'private key: SENTINEL_PRIVATE_KEY_VALUE\n' >&2
  exit 43
fi
echo "unexpected command" >&2
exit 1
`), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	destPath := filepath.Join(t.TempDir(), "config", "kubeconfig")
	err := PullKubeconfigFrom1Password(destPath, NewColorLogger())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to pull kubeconfig from 1Password")
	assert.Contains(t, err.Error(), "token=<redacted>")
	assert.Contains(t, err.Error(), "private key: <redacted>")
	assert.NotContains(t, err.Error(), "SENTINEL_TOKEN_VALUE")
	assert.NotContains(t, err.Error(), "SENTINEL_PRIVATE_KEY_VALUE")
}
