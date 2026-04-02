package truenas

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

func TestNewWorkingClientAndClose(t *testing.T) {
	client := NewWorkingClient("nas.local", "api-key", 443, true)
	assert.Equal(t, "nas.local", client.host)
	assert.Equal(t, "api-key", client.apiKey)
	assert.Equal(t, 443, client.port)
	assert.True(t, client.useSSL)
	require.NoError(t, client.Close())
}

func TestGetCredentialsFromEnvironment(t *testing.T) {
	stubUnavailable1PasswordCLI(t)
	t.Setenv(constants.EnvTrueNASHost, "nas.local")
	t.Setenv(constants.EnvTrueNASAPIKey, "api-key")

	host, apiKey, err := GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "nas.local", host)
	assert.Equal(t, "api-key", apiKey)
}

func TestGetCredentialsMissingValues(t *testing.T) {
	stubUnavailable1PasswordCLI(t)
	t.Setenv(constants.EnvTrueNASHost, "")
	t.Setenv(constants.EnvTrueNASAPIKey, "")

	_, _, err := GetCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TrueNAS credentials not found")
}
