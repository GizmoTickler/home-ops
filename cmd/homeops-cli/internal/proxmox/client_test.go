package proxmox

import (
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stubUnavailable1PasswordCLI(t *testing.T) {
	t.Helper()

	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNewClientAndUnconnectedMethods(t *testing.T) {
	client, err := NewClient("host", "token", "secret", true)
	require.NoError(t, err)
	assert.NotNil(t, client.Context())
	assert.Nil(t, client.Node())
	require.NoError(t, client.Close())

	_, err = client.CreateVM(100)
	require.Error(t, err)
	_, err = client.GetVM(100)
	require.Error(t, err)
	_, err = client.ListVMs()
	require.Error(t, err)
	_, err = client.GetStorage("local")
	require.Error(t, err)
}

func TestGetCredentialsFromEnvironment(t *testing.T) {
	stubUnavailable1PasswordCLI(t)
	t.Setenv(constants.EnvProxmoxHost, "pve.local")
	t.Setenv(constants.EnvProxmoxTokenID, "token-id")
	t.Setenv(constants.EnvProxmoxTokenSecret, "token-secret")
	t.Setenv(constants.EnvProxmoxNode, "pve-node")

	host, tokenID, secret, nodeName, err := GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "pve.local", host)
	assert.Equal(t, "token-id", tokenID)
	assert.Equal(t, "token-secret", secret)
	assert.Equal(t, "pve-node", nodeName)
}

func TestGetCredentialsDefaultsAndMissingValues(t *testing.T) {
	stubUnavailable1PasswordCLI(t)
	t.Setenv(constants.EnvProxmoxHost, "pve.local")
	t.Setenv(constants.EnvProxmoxTokenID, "token-id")
	t.Setenv(constants.EnvProxmoxTokenSecret, "token-secret")

	host, tokenID, secret, nodeName, err := GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "pve", nodeName)
	assert.Equal(t, "pve.local", host)
	assert.Equal(t, "token-id", tokenID)
	assert.Equal(t, "token-secret", secret)

	t.Setenv(constants.EnvProxmoxHost, "")
	t.Setenv(constants.EnvProxmoxTokenID, "")
	t.Setenv(constants.EnvProxmoxTokenSecret, "")
	_, _, _, _, err = GetCredentials()
	require.Error(t, err)
}
