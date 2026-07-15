package kubernetes

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func TestSupportBundleCollectorIsolationManifestAndTarStructure(t *testing.T) {
	fixed := time.Date(2026, 7, 15, 12, 30, 0, 0, time.UTC)
	testutil.Swap(t, &supportBundleNowFn, func() time.Time { return fixed })
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{
			{Name: "healthy", Filename: "healthy.json", Collect: func(context.Context) ([]byte, error) {
				return []byte(`{"status":"ok"}`), nil
			}},
			{Name: "broken", Filename: "broken.json", Collect: func(context.Context) ([]byte, error) {
				return nil, errors.New("probe unavailable")
			}},
			{Name: "malformed", Filename: "malformed.json", Collect: func(context.Context) ([]byte, error) {
				return []byte(`not-json`), nil
			}},
		}
	})
	output := filepath.Join(t.TempDir(), "bundle.tar.gz")
	result, err := runSupportBundle(context.Background(), output, false)
	require.NoError(t, err)
	assert.Equal(t, output, result.Path)
	assert.Positive(t, result.Size)
	require.Len(t, result.Collectors, 3)
	assert.Equal(t, "OK", result.Collectors[0].Status)
	assert.Equal(t, "ERROR", result.Collectors[1].Status)
	assert.Equal(t, "ERROR", result.Collectors[2].Status)

	entries := readSupportBundleArchive(t, output)
	assert.Equal(t, []string{"broken.error", "healthy.json", "malformed.error", "manifest.json"}, sortedMapKeys(entries))
	assert.Equal(t, "probe unavailable\n", string(entries["broken.error"]))
	assert.Contains(t, string(entries["malformed.error"]), "invalid JSON")
	var manifest supportBundleManifest
	require.NoError(t, json.Unmarshal(entries["manifest.json"], &manifest))
	assert.Equal(t, fixed.Format(time.RFC3339), manifest.CreatedAt)
	assert.NotEmpty(t, manifest.CLIVersion)
	assert.NotEmpty(t, manifest.Versions["go"])
	assert.Equal(t, []string{"broken.error", "healthy.json", "malformed.error", "manifest.json"}, manifest.Contents)
	require.Len(t, manifest.Collectors, 3)
	assert.Equal(t, "broken.error", manifest.Collectors[1].File)
	assert.Equal(t, "ERROR", manifest.Collectors[1].Status)
}

func TestSupportBundleCollectorRecoversPanic(t *testing.T) {
	_, err := runSupportBundleCollector(context.Background(), supportBundleCollector{Collect: func(context.Context) ([]byte, error) {
		panic("probe panic")
	}})
	require.Error(t, err)
	assert.ErrorContains(t, err, "collector panic")
}

func TestSupportBundleSecurityScanRefusesArchive(t *testing.T) {
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{{
			Name: "leaky", Filename: "leaky.txt", Collect: func(context.Context) ([]byte, error) {
				return []byte("-----BEGIN PRIVATE KEY-----\nknown-secret\n-----END PRIVATE KEY-----\n"), nil
			},
		}}
	})
	output := filepath.Join(t.TempDir(), "must-not-exist.tar.gz")
	_, err := runSupportBundle(context.Background(), output, false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "security scan")
	_, statErr := os.Stat(output)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSupportBundleNoSSHSkipsCollectors(t *testing.T) {
	called := false
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{{
			Name: "ssh", Filename: "ssh.json", SSH: true, Collect: func(context.Context) ([]byte, error) {
				called = true
				return []byte(`{}`), nil
			},
		}}
	})
	output := filepath.Join(t.TempDir(), "bundle.tar.gz")
	result, err := runSupportBundle(context.Background(), output, true)
	require.NoError(t, err)
	assert.False(t, called)
	require.Len(t, result.Collectors, 1)
	assert.Equal(t, "SKIPPED", result.Collectors[0].Status)
	entries := readSupportBundleArchive(t, output)
	assert.Equal(t, []string{"manifest.json"}, sortedMapKeys(entries))
}

func TestCollectSupportEventsKeepsNewestTwoHundred(t *testing.T) {
	items := make([]json.RawMessage, 205)
	for index := range items {
		items[index] = json.RawMessage(fmt.Sprintf(`{"index":%d}`, index))
	}
	raw, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "EventList", "items": items})
	require.NoError(t, err)
	testutil.Swap(t, &supportBundleKubectlOutputFn, func(context.Context, ...string) ([]byte, error) { return raw, nil })
	collected, err := collectSupportEvents(context.Background())
	require.NoError(t, err)
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(collected, &list))
	assert.Len(t, list.Items, 200)
}

func readSupportBundleArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	archive, err := os.Open(path) // #nosec G304 -- test opens its own temporary output path.
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()
	gzipReader, err := gzip.NewReader(archive)
	require.NoError(t, err)
	defer func() { require.NoError(t, gzipReader.Close()) }()
	tarReader := tar.NewReader(gzipReader)
	entries := map[string][]byte{}
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		require.NoError(t, nextErr)
		data, readErr := io.ReadAll(tarReader)
		require.NoError(t, readErr)
		entries[header.Name] = data
		assert.Equal(t, int64(0o600), header.Mode)
	}
	return entries
}

func sortedMapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
