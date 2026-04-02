package ssh

import (
	"os"
	"testing"

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
