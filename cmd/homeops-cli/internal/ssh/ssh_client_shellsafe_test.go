package ssh

import (
	"context"
	"fmt"
	"testing"

	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSHCommandsAreShellSafe asserts that file-path / URL arguments interpolated
// into remote shell commands are single-quoted, so a value containing whitespace
// or a shell metacharacter cannot inject a command or operate on the wrong file.
func TestSSHCommandsAreShellSafe(t *testing.T) {
	const evil = "/tmp/iso dir/x;rm -rf $HOME`reboot`.iso"
	quoted := common.ShellQuote(evil)

	var captured []string
	restore := setCommandRunnerForTesting(func(_ context.Context, opts common.CommandOptions) (common.CommandResult, error) {
		// The remote command is the final positional arg after the ssh flags.
		captured = append(captured, opts.Args[len(opts.Args)-1])
		return common.CommandResult{Stdout: "FILE_NOT_FOUND"}, nil
	})
	defer restore()

	client := NewSSHClient(SSHConfig{Host: "h", Username: "u", Port: "22"})

	t.Run("RemoveFile", func(t *testing.T) {
		captured = nil
		require.NoError(t, client.RemoveFile(evil))
		require.Len(t, captured, 1)
		assert.Equal(t, "sudo rm -f "+quoted, captured[0])
	})

	t.Run("VerifyFile", func(t *testing.T) {
		captured = nil
		_, _, err := client.VerifyFile(evil)
		require.NoError(t, err)
		require.Len(t, captured, 1)
		want := fmt.Sprintf("stat -c '%%s' %s 2>/dev/null || echo 'FILE_NOT_FOUND'", quoted)
		assert.Equal(t, want, captured[0])
	})

	t.Run("DownloadISO", func(t *testing.T) {
		captured = nil
		require.NoError(t, client.DownloadISO("https://ex/x.iso", evil))
		require.Len(t, captured, 2) // mkdir then wget (no curl fallback on success)
		assert.Equal(t, "sudo mkdir -p "+common.ShellQuote("/tmp/iso dir"), captured[0])
		assert.Equal(t, "sudo wget -O "+quoted+" "+common.ShellQuote("https://ex/x.iso"), captured[1])
	})
}
