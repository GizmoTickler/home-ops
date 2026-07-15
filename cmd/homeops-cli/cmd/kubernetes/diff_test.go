package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

const diffTestManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: changed
  namespace: media
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: added
  namespace: media
---
apiVersion: v1
kind: Namespace
metadata:
  name: unchanged
`

func exitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit "+strconv.Itoa(code))
	err := cmd.Run()
	require.Error(t, err)
	return err
}

func TestInterpretKubectlDiffResult(t *testing.T) {
	t.Run("exit zero means no differences", func(t *testing.T) {
		output, err := interpretKubectlDiffResult([]byte(""), nil)
		require.NoError(t, err)
		assert.Empty(t, output)
	})

	t.Run("exit one means differences found", func(t *testing.T) {
		output, err := interpretKubectlDiffResult([]byte("unified diff\n"), exitError(t, 1))
		require.NoError(t, err)
		assert.Equal(t, "unified diff\n", output)
	})

	t.Run("exit greater than one is a real error", func(t *testing.T) {
		output, err := interpretKubectlDiffResult([]byte("API unavailable"), exitError(t, 2))
		require.Error(t, err)
		assert.Empty(t, output)
		assert.Contains(t, err.Error(), "API unavailable")
	})

	t.Run("non exit errors are real errors", func(t *testing.T) {
		_, err := interpretKubectlDiffResult(nil, errors.New("start failed"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "start failed")
	})
}

func TestSummarizeKustomizationDiff(t *testing.T) {
	diff := `diff -u -N /tmp/LIVE/v1.ConfigMap.media.changed /tmp/MERGED/v1.ConfigMap.media.changed
--- /tmp/LIVE/v1.ConfigMap.media.changed
+++ /tmp/MERGED/v1.ConfigMap.media.changed
@@ -3,1 +3,1 @@
-  old
+  new
diff -u -N /tmp/LIVE/apps.v1.Deployment.media.added /tmp/MERGED/apps.v1.Deployment.media.added
--- /tmp/LIVE/apps.v1.Deployment.media.added
+++ /tmp/MERGED/apps.v1.Deployment.media.added
@@ -0,0 +1,5 @@
+apiVersion: apps/v1
`

	report, err := summarizeKustomizationDiff(diffTestManifest, diff)
	require.NoError(t, err)
	assert.Equal(t, []string{"ConfigMap/media/changed"}, report.Changed)
	assert.Equal(t, []string{"Deployment/media/added"}, report.Added)
	assert.Equal(t, 1, report.Unchanged)
	assert.Equal(t, diff, report.Diff)
}

func TestParseRenderedResourcesClusterScopedFilename(t *testing.T) {
	resources, err := parseRenderedResources(diffTestManifest)
	require.NoError(t, err)
	require.Len(t, resources, 3)
	assert.Equal(t, "v1.Namespace..unchanged", resources[2].DiffName)
}

func TestRunKustomizationDiffPlumbsRenderedManifest(t *testing.T) {
	testutil.Swap(t, &buildKustomizationManifestOnlineFn, func(path, name string) (*KustomizationInfo, string, error) {
		assert.Equal(t, "ks.yaml", path)
		assert.Equal(t, "media", name)
		return &KustomizationInfo{Name: name}, diffTestManifest, nil
	})
	testutil.Swap(t, &kubectlDiffManifestFn, func(ctx context.Context, manifest string) (string, error) {
		assert.Equal(t, diffTestManifest, manifest)
		return "", nil
	})

	var output strings.Builder
	require.NoError(t, runKustomizationDiff(context.Background(), "ks.yaml", "media", "table", &output))
	assert.Equal(t, "0 resources changed, 0 added, 3 unchanged\n", output.String())
}

func TestRunKustomizationDiffJSON(t *testing.T) {
	testutil.Swap(t, &buildKustomizationManifestOnlineFn, func(string, string) (*KustomizationInfo, string, error) {
		return &KustomizationInfo{}, diffTestManifest, nil
	})
	testutil.Swap(t, &kubectlDiffManifestFn, func(context.Context, string) (string, error) {
		return "", nil
	})

	var output strings.Builder
	require.NoError(t, runKustomizationDiff(context.Background(), "ks.yaml", "", "json", &output))
	var report kustomizationDiffReport
	require.NoError(t, json.Unmarshal([]byte(output.String()), &report))
	assert.Empty(t, report.Changed)
	assert.Empty(t, report.Added)
	assert.Equal(t, "", report.Diff)
}
