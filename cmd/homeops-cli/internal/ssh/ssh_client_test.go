package ssh

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSSHClientAndClose(t *testing.T) {
	client := NewSSHClient(SSHConfig{
		Host:       "host",
		Username:   "user",
		Port:       "22",
		SSHItemRef: "op://vault/item/key",
	})

	assert.Equal(t, "host", client.host)
	assert.Equal(t, "user", client.username)
	assert.Equal(t, "22", client.port)
	assert.Equal(t, "op://vault/item/key", client.sshItemRef)
	require.NoError(t, client.Close())
}

func TestSSHClientConnectValidation(t *testing.T) {
	client := NewSSHClient(SSHConfig{})
	err := client.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSH host is required")

	client = NewSSHClient(SSHConfig{Host: "host"})
	err = client.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSH username is required")
}

func TestSSHClientExecuteCommandPropagatesFailure(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))

	client := NewSSHClient(SSHConfig{
		Host:     "127.0.0.1",
		Username: "nobody",
		Port:     "1",
	})

	_, err := client.ExecuteCommand("echo test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to execute command via SSH")
}

func TestSSHClientConnectRedactsCommandOutputOnFailure(t *testing.T) {
	restore := setCommandRunnerForTesting(func(ctx context.Context, opts common.CommandOptions) (common.CommandResult, error) {
		assert.Equal(t, defaultSSHCommandTimeout, opts.Timeout)
		assert.Equal(t, "ssh", opts.Name)
		assert.Equal(t, []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "IdentitiesOnly=yes",
			"-o", "NumberOfPasswordPrompts=0",
			"-p", "22",
			"admin@truenas.local",
			"echo", "connection_test",
		}, opts.Args)

		return common.CommandResult{
			Stdout: "password=CONNECT_TEST_VALUE\n",
			Stderr: "token: CONNECT_TOKEN_VALUE\n",
		}, errors.New("exit status 255")
	})
	defer restore()

	client := NewSSHClient(SSHConfig{
		Host:     "truenas.local",
		Username: "admin",
		Port:     "22",
	})

	err := client.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password=<redacted>")
	assert.Contains(t, err.Error(), "token: <redacted>")
	assert.NotContains(t, err.Error(), "CONNECT_TEST_VALUE")
	assert.NotContains(t, err.Error(), "CONNECT_TOKEN_VALUE")
}

func TestSSHClientExecuteCommandUsesTimeoutAndRedactsCommandOutputOnFailure(t *testing.T) {
	restore := setCommandRunnerForTesting(func(ctx context.Context, opts common.CommandOptions) (common.CommandResult, error) {
		assert.Equal(t, defaultSSHCommandTimeout, opts.Timeout)
		assert.Equal(t, "ssh", opts.Name)
		assert.Equal(t, []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "IdentitiesOnly=yes",
			"-o", "NumberOfPasswordPrompts=0",
			"-p", "2222",
			"admin@truenas.local",
			"sudo test-command",
		}, opts.Args)

		return common.CommandResult{
			Stdout: "api_key=EXEC_TEST_VALUE\n",
			Stderr: "client_secret: EXEC_SECRET_VALUE\n",
		}, context.DeadlineExceeded
	})
	defer restore()

	client := NewSSHClient(SSHConfig{
		Host:     "truenas.local",
		Username: "admin",
		Port:     "2222",
	})

	_, err := client.ExecuteCommand("sudo test-command")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to execute command via SSH")
	assert.Contains(t, err.Error(), context.DeadlineExceeded.Error())
	assert.Contains(t, err.Error(), "api_key=<redacted>")
	assert.Contains(t, err.Error(), "client_secret: <redacted>")
	assert.NotContains(t, err.Error(), "EXEC_TEST_VALUE")
	assert.NotContains(t, err.Error(), "EXEC_SECRET_VALUE")
}

func TestSSHClientExecuteCommandReturnsStdoutFromRunner(t *testing.T) {
	restore := setCommandRunnerForTesting(func(ctx context.Context, opts common.CommandOptions) (common.CommandResult, error) {
		return common.CommandResult{Stdout: "ran-command"}, nil
	})
	defer restore()

	client := NewSSHClient(SSHConfig{
		Host:     "truenas.local",
		Username: "admin",
		Port:     "22",
	})

	output, err := client.ExecuteCommand("echo test")
	require.NoError(t, err)
	assert.Equal(t, "ran-command", output)
}

func TestSSHCommandTimeoutIsConfigured(t *testing.T) {
	assert.Greater(t, defaultSSHCommandTimeout, time.Duration(0))
}

func setCommandRunnerForTesting(runner func(context.Context, common.CommandOptions) (common.CommandResult, error)) func() {
	old := runCommand
	runCommand = runner
	return func() {
		runCommand = old
	}
}
