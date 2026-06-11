package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
)

func opStoreConfig() config.StoreConfig {
	return config.StoreConfig{
		Backend: "op",
		Op:      config.OpLocation{Vault: "Infrastructure", Item: "kubeconfig", Field: "kubeconfig"},
	}
}

func TestFileKubeconfigStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewKubeconfigStore(config.StoreConfig{Backend: "file", Path: filepath.Join(dir, "state", "kubeconfig")})
	logger := common.NewColorLogger()

	require.NoError(t, store.Save([]byte("apiVersion: v1\n"), logger))

	dest := filepath.Join(dir, "out", "kubeconfig")
	require.NoError(t, store.Pull(dest, logger))

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "apiVersion: v1\n", string(content))

	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Contains(t, store.Describe(), "file ")
}

func TestFileKubeconfigStorePullMissing(t *testing.T) {
	store := NewKubeconfigStore(config.StoreConfig{Backend: "file", Path: filepath.Join(t.TempDir(), "absent")})
	err := store.Pull(filepath.Join(t.TempDir(), "kubeconfig"), common.NewColorLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no persisted kubeconfig")
}

func TestFilePKIStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	store := NewPKIStore(config.StoreConfig{Backend: "file", Path: dir})

	require.NoError(t, store.Save(map[string]string{"ca_crt": "QUJD", "ca_key": "REVG"}))
	assert.Equal(t, "QUJD", store.GetField("ca_crt"))
	assert.Equal(t, "REVG", store.GetField("ca_key"))
	assert.Equal(t, "", store.GetField("missing"))

	info, err := os.Stat(filepath.Join(dir, "ca_key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestOpKubeconfigStoreSaveFiltersConnectEnv(t *testing.T) {
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

	store := NewKubeconfigStore(opStoreConfig())
	require.NoError(t, store.Save([]byte("apiVersion: v1\n"), common.NewColorLogger()))
}

func TestOpKubeconfigStorePull(t *testing.T) {
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
echo "unexpected command" >&2
exit 1
`), 0o755))

	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	store := NewKubeconfigStore(opStoreConfig())
	destPath := filepath.Join(t.TempDir(), "config", "kubeconfig")
	require.NoError(t, store.Pull(destPath, common.NewColorLogger()))

	content, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "apiVersion: v1")

	info, err := os.Stat(destPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestOpKubeconfigStoreFileIDErrors(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		scriptDir := t.TempDir()
		opPath := filepath.Join(scriptDir, "op")
		require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nprintf '{'\n"), 0o755))
		t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		err := NewKubeconfigStore(opStoreConfig()).Pull(filepath.Join(t.TempDir(), "kc"), common.NewColorLogger())
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

		err := NewKubeconfigStore(opStoreConfig()).Pull(filepath.Join(t.TempDir(), "kc"), common.NewColorLogger())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no kubeconfig file found")
	})
}

func TestOpPKIStoreSave(t *testing.T) {
	var deleteArgs []string
	var createArgs []string
	var createStdin []byte
	restore := SetRunOpFnsForTesting(
		func(args ...string) error { deleteArgs = args; return nil },
		func(stdin []byte, args ...string) error { createStdin = stdin; createArgs = args; return nil },
	)
	defer restore()

	store := NewPKIStore(config.StoreConfig{
		Backend: "op",
		Op:      config.OpLocation{Vault: "Infrastructure", Item: "kubernetes-pki"},
	})
	require.NoError(t, store.Save(map[string]string{"ca_crt": "QUJD", "ca_key": "REVG"}))

	assert.Equal(t, []string{"item", "delete", "kubernetes-pki", "--vault", "Infrastructure"}, deleteArgs)
	assert.Equal(t, []string{"item", "create", "--vault", "Infrastructure"}, createArgs)

	var tmpl struct {
		Title    string `json:"title"`
		Category string `json:"category"`
		Fields   []struct {
			Label string `json:"label"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(createStdin, &tmpl))
	assert.Equal(t, "kubernetes-pki", tmpl.Title)
	assert.Equal(t, "SECURE_NOTE", tmpl.Category)

	types := map[string]string{}
	for _, f := range tmpl.Fields {
		if f.Label != "" {
			types[f.Label] = f.Type
		}
	}
	assert.Equal(t, "STRING", types["ca_crt"])
	assert.Equal(t, "CONCEALED", types["ca_key"])
}
