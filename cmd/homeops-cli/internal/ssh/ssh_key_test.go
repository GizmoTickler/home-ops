package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	cryptossh "golang.org/x/crypto/ssh"
	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPrivateKeySigner(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	path := writePrivateKey(t, t.TempDir(), privateKey, nil)

	signer, expandedPath, err := loadPrivateKeySigner(path)
	require.NoError(t, err)
	assert.Equal(t, path, expandedPath)
	wantPublicKey, err := cryptossh.NewPublicKey(publicKey)
	require.NoError(t, err)
	assert.Equal(t, wantPublicKey.Marshal(), signer.PublicKey().Marshal())
}

func TestLoadPrivateKeySignerExpandsTilde(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	home := t.TempDir()
	t.Setenv("HOME", home)
	keyDir := filepath.Join(home, ".ssh", "keys")
	path := writePrivateKey(t, keyDir, privateKey, nil)

	_, expandedPath, err := loadPrivateKeySigner("~/.ssh/keys/test-key")
	require.NoError(t, err)
	assert.Equal(t, path, expandedPath)
}

func TestLoadPrivateKeySignerRejectsEncryptedKeyClearly(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	path := writePrivateKey(t, t.TempDir(), privateKey, []byte("test-passphrase"))

	_, _, err = loadPrivateKeySigner(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is encrypted")
	assert.Contains(t, err.Error(), "passphrase-protected keys are not supported")
}

func TestSSHClientRejectsEncryptedKeyBeforeRunningOpenSSH(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	path := writePrivateKey(t, t.TempDir(), privateKey, []byte("test-passphrase"))
	called := false
	restore := setCommandRunnerForTesting(func(_ context.Context, _ common.CommandOptions) (common.CommandResult, error) {
		called = true
		return common.CommandResult{}, nil
	})
	defer restore()

	client := NewSSHClient(SSHConfig{Host: "nas", Username: "admin", Port: "22", KeyPath: path})
	err = client.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is encrypted")
	assert.False(t, called)
}

func TestSSHClientEmptyKeyPathPreservesAgentOnlyArguments(t *testing.T) {
	client := NewSSHClient(SSHConfig{Host: "nas", Username: "admin", Port: "22"})

	assert.NoError(t, client.keyLoadError)
	assert.Empty(t, client.keyPath)
	assert.Equal(t, []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"-o", "NumberOfPasswordPrompts=0",
		"-p", "22",
		"admin@nas",
	}, client.sshArgs())
}

func TestSSHClientConfiguredKeyIsOfferedBeforeAgentFallback(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	path := writePrivateKey(t, t.TempDir(), privateKey, nil)
	client := NewSSHClient(SSHConfig{Host: "nas", Username: "admin", Port: "22", KeyPath: path})

	require.NoError(t, client.keyLoadError)
	assert.Equal(t, []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-i", path,
		"-o", "IdentitiesOnly=no",
		"-o", "NumberOfPasswordPrompts=0",
		"-p", "22",
		"admin@nas",
	}, client.sshArgs())
}

func writePrivateKey(t *testing.T, dir string, privateKey ed25519.PrivateKey, passphrase []byte) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	var block *pem.Block
	var err error
	if len(passphrase) == 0 {
		block, err = cryptossh.MarshalPrivateKey(privateKey, "homeops-test")
	} else {
		block, err = cryptossh.MarshalPrivateKeyWithPassphrase(privateKey, "homeops-test", passphrase)
	}
	require.NoError(t, err)
	path := filepath.Join(dir, "test-key")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
	return path
}
