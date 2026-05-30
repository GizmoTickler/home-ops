package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidVersionFormat(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"valid version with v prefix", "v1.8.2", true},
		{"valid version with patch", "v1.31.1", true},
		{"invalid version without patch", "v2.0", false},
		{"invalid version without v prefix", "1.8.2", false},
		{"invalid version with extra parts", "v1.2.3.4", false},
		{"empty version", "", false},
		{"invalid format", "versionOne", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidVersionFormat(tt.version))
		})
	}
}

func TestGetDefaultVersions(t *testing.T) {
	versions := getDefaultVersions()
	require.NotNil(t, versions)
	assert.Equal(t, "v1.36.1", versions.KubernetesVersion)
	// Flatcar clusters use kubeadm, not Talos; the version loader returns
	// a zeroed TalosVersion (the Consumer-facing Config still carries a
	// TalosVersion field for backward compatibility).
	assert.Equal(t, "v0.0.0", versions.TalosVersion)
	assert.True(t, isValidVersionFormat(versions.KubernetesVersion))
	assert.NotEmpty(t, versions.FlatcarVersion)
	assert.NotEmpty(t, versions.KubeVipVersion)
	assert.NotEmpty(t, versions.PauseImage)
}

func TestLoadVersionsFromSystemUpgradeFlatcar(t *testing.T) {
	t.Run("valid kubeadm plan", func(t *testing.T) {
		tmpDir := t.TempDir()
		planDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "kubeadm-upgrade", "app")
		err := os.MkdirAll(planDir, 0755)
		require.NoError(t, err)

		planContent := `apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: kubeadm-control-plane
  namespace: system-upgrade
spec:
  version: v1.36.1
  concurrency: 1
  nodeSelector:
    matchExpressions:
      - key: node-role.kubernetes.io/control-plane
        operator: Exists
`
		err = os.WriteFile(filepath.Join(planDir, "plan.yaml"), []byte(planContent), 0644)
		require.NoError(t, err)

		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, "v1.36.1", versions.KubernetesVersion)
		assert.Empty(t, versions.TalosVersion, "TalosVersion should be empty for Flatcar")
		assert.NotEmpty(t, versions.FlatcarVersion)
		assert.NotEmpty(t, versions.KubeVipVersion)
		assert.NotEmpty(t, versions.PauseImage)
	})

	t.Run("missing plan file", func(t *testing.T) {
		tmpDir := t.TempDir()
		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		assert.Error(t, err)
		assert.Nil(t, versions)
	})

	t.Run("empty plan file", func(t *testing.T) {
		tmpDir := t.TempDir()
		planDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "kubeadm-upgrade", "app")
		err := os.MkdirAll(planDir, 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(planDir, "plan.yaml"), []byte(""), 0644)
		require.NoError(t, err)

		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		assert.Error(t, err)
		assert.Nil(t, versions)
	})

	t.Run("plan with quoted version", func(t *testing.T) {
		tmpDir := t.TempDir()
		planDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "kubeadm-upgrade", "app")
		err := os.MkdirAll(planDir, 0755)
		require.NoError(t, err)

		planContent := "spec:\n  version: \"v1.35.0\"\n"
		err = os.WriteFile(filepath.Join(planDir, "plan.yaml"), []byte(planContent), 0644)
		require.NoError(t, err)

		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, "v1.35.0", versions.KubernetesVersion)
	})
}

func TestApplyFlatcarDefaults(t *testing.T) {
	c := &VersionConfig{}
	applyFlatcarDefaults(c)
	assert.Equal(t, defaultFlatcarVersion, c.FlatcarVersion)
	assert.Equal(t, defaultKubeVipVersion, c.KubeVipVersion)
	assert.Equal(t, defaultPauseImage, c.PauseImage)

	// Existing values are not overridden.
	c2 := &VersionConfig{FlatcarVersion: "4152.2.0", KubeVipVersion: "v0.9.0", PauseImage: "custom"}
	applyFlatcarDefaults(c2)
	assert.Equal(t, "4152.2.0", c2.FlatcarVersion)
	assert.Equal(t, "v0.9.0", c2.KubeVipVersion)
	assert.Equal(t, "custom", c2.PauseImage)
}
