package kubernetes

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKustomizationFile(t *testing.T) {
	repoDir := t.TempDir()
	appDir := filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana", "app")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	instanceDir := filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana", "instance")
	require.NoError(t, os.MkdirAll(instanceDir, 0o755))

	ksDir := filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana")
	require.NoError(t, os.MkdirAll(ksDir, 0o755))

	ksContent := `---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: grafana
spec:
  path: ./kubernetes/apps/observability/grafana/app
  targetNamespace: observability
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: grafana-instance
spec:
  path: ./kubernetes/apps/observability/grafana/instance
  targetNamespace: observability
`
	ksPath := filepath.Join(ksDir, "ks.yaml")
	require.NoError(t, os.WriteFile(ksPath, []byte(ksContent), 0o644))

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(repoDir))

	_, err = parseKustomizationFile(ksPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple kustomizations found")

	info, err := parseKustomizationFileWithSelector(ksPath, "grafana")
	require.NoError(t, err)
	assert.Equal(t, "grafana", info.Name)
	assert.Equal(t, "observability", info.Namespace)
	assert.Equal(t, "kubernetes/apps/observability/grafana/app", info.Path)
	expectedFullPath, err := filepath.EvalSymlinks(filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana", "app"))
	require.NoError(t, err)
	actualFullPath, err := filepath.EvalSymlinks(info.FullPath)
	require.NoError(t, err)
	assert.Equal(t, expectedFullPath, actualFullPath)

	info, err = parseKustomizationFileWithSelector(ksDir, "grafana-instance")
	require.NoError(t, err)
	assert.Equal(t, ksPath, info.KsFile)
	assert.Equal(t, "grafana-instance", info.Name)
}

func TestParseKustomizationFileErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := parseKustomizationFile(filepath.Join(dir, "missing"))
	require.Error(t, err)

	ksPath := filepath.Join(dir, "ks.yaml")
	require.NoError(t, os.WriteFile(ksPath, []byte("kind: Kustomization\nmetadata:\n  name: broken\n"), 0o644))
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(dir))

	_, err = parseKustomizationFile(ksPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to extract")
}

func TestParseKustomizationFileWithSelectorErrors(t *testing.T) {
	repoDir := t.TempDir()
	appDir := filepath.Join(repoDir, "kubernetes", "apps", "network", "envoy", "app")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	ksPath := filepath.Join(repoDir, "kubernetes", "apps", "network", "envoy", "ks.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(ksPath), 0o755))
	require.NoError(t, os.WriteFile(ksPath, []byte(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: envoy
spec:
  path: ./kubernetes/apps/network/envoy/app
  targetNamespace: network
`), 0o644))

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(repoDir))

	_, err = parseKustomizationFileWithSelector(ksPath, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `kustomization "missing" not found`)
}

func TestFindKustomizationFiles(t *testing.T) {
	appsDir := filepath.Join(t.TempDir(), "kubernetes", "apps")
	require.NoError(t, os.MkdirAll(filepath.Join(appsDir, "observability", "grafana"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(appsDir, "network", "envoy"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appsDir, "observability", "grafana", "ks.yaml"), []byte("kind: Kustomization"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appsDir, "network", "envoy", "ks.yaml"), []byte("kind: Kustomization"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appsDir, "network", "envoy", "ignore.yaml"), []byte("kind: Kustomization"), 0o644))

	ksFiles, err := findKustomizationFiles(appsDir)
	require.NoError(t, err)
	require.Len(t, ksFiles, 2)
	assert.Equal(t, filepath.Join(appsDir, "network", "envoy", "ks.yaml"), ksFiles[0])
	assert.Equal(t, filepath.Join(appsDir, "observability", "grafana", "ks.yaml"), ksFiles[1])
}
