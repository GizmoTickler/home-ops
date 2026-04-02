package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHClientCommandHelpersWithFakeSSH(t *testing.T) {
	scriptDir := t.TempDir()
	sshPath := filepath.Join(scriptDir, "ssh")
	require.NoError(t, os.WriteFile(sshPath, []byte(`#!/bin/sh
set -eu
last=""
for arg in "$@"; do
  last="$arg"
done

case "$last" in
  connection_test)
    printf 'connection_test'
    ;;
  "echo test")
    printf 'ran-command'
    ;;
  "sudo mkdir -p /remote")
    exit 0
    ;;
  "sudo wget -O /remote/file.iso https://example.com/file.iso")
    echo 'wget failed' >&2
    exit 1
    ;;
  "sudo curl -L -o /remote/file.iso https://example.com/file.iso")
    printf 'curl-ok'
    ;;
  "stat -c '%s' /remote/existing.iso 2>/dev/null || echo 'FILE_NOT_FOUND'")
    printf '1234'
    ;;
  "stat -c '%s' /remote/missing.iso 2>/dev/null || echo 'FILE_NOT_FOUND'")
    printf 'FILE_NOT_FOUND'
    ;;
  "stat -c '%s' /remote/bad.iso 2>/dev/null || echo 'FILE_NOT_FOUND'")
    printf 'not-a-number'
    ;;
  "sudo rm -f /remote/file.iso")
    printf 'removed'
    ;;
  *)
    echo "unexpected remote command: $last" >&2
    exit 1
    ;;
esac
`), 0o755))

	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_AUTH_SOCK", "/tmp/test-agent.sock")

	client := NewSSHClient(SSHConfig{
		Host:       "truenas.local",
		Username:   "admin",
		Port:       "22",
		SSHItemRef: "op://vault/item/key",
	})

	require.NoError(t, client.Connect())

	output, err := client.ExecuteCommand("echo test")
	require.NoError(t, err)
	assert.Equal(t, "ran-command", output)

	require.NoError(t, client.DownloadISO("https://example.com/file.iso", "/remote/file.iso"))

	exists, size, err := client.VerifyFile("/remote/existing.iso")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, int64(1234), size)

	exists, size, err = client.VerifyFile("/remote/missing.iso")
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Zero(t, size)

	_, _, err = client.VerifyFile("/remote/bad.iso")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse file size")

	require.NoError(t, client.RemoveFile("/remote/file.iso"))
}
