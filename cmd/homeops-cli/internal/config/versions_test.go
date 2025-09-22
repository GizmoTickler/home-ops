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
		{
			name:    "valid version with v prefix",
			version: "v1.8.2",
			want:    true,
		},
		{
			name:    "valid version with patch",
			version: "v1.31.1",
			want:    true,
		},
		{
			name:    "valid version without patch",
			version: "v2.0",
			want:    true,
		},
		{
			name:    "invalid version without v prefix",
			version: "1.8.2",
			want:    false,
		},
		{
			name:    "invalid version with extra parts",
			version: "v1.2.3.4",
			want:    false,
		},
		{
			name:    "empty version",
			version: "",
			want:    false,
		},
		{
			name:    "invalid format",
			version: "versionOne",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidVersionFormat(tt.version)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetDefaultVersions(t *testing.T) {
	versions := getDefaultVersions()

	assert.NotNil(t, versions)
	assert.NotEmpty(t, versions.TalosVersion)
	assert.NotEmpty(t, versions.KubernetesVersion)

	// Check format
	assert.True(t, isValidVersionFormat(versions.TalosVersion))
	assert.True(t, isValidVersionFormat(versions.KubernetesVersion))
}

func TestLoadVersionsFromSystemUpgrade(t *testing.T) {
	t.Run("valid plans directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create the full nested directory structure that the function expects
		plansDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "system-upgrade-controller", "plans")
		err := os.MkdirAll(plansDir, 0755)
		require.NoError(t, err)

		// Create talos plan file with expected component "installer"
		talosPlan := filepath.Join(plansDir, "talos.yaml")
		talosPlanContent := `
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: talos
spec:
  version: v1.9.0
  image: factory.talos.dev/installer/v1.9.0
`
		err = os.WriteFile(talosPlan, []byte(talosPlanContent), 0644)
		require.NoError(t, err)

		// Create kubernetes plan file with expected component "kubelet"
		k8sPlan := filepath.Join(plansDir, "kubernetes.yaml")
		k8sPlanContent := `
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: kubernetes
spec:
  version: v1.32.0
  image: registry.k8s.io/kube-apiserver:v1.32.0
  command:
    - kubelet
`
		err = os.WriteFile(k8sPlan, []byte(k8sPlanContent), 0644)
		require.NoError(t, err)

		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		require.NoError(t, err)
		assert.NotNil(t, versions)
		assert.Equal(t, "v1.9.0", versions.TalosVersion)
		assert.Equal(t, "v1.32.0", versions.KubernetesVersion)
	})

	t.Run("missing plans directory", func(t *testing.T) {
		versions, err := LoadVersionsFromSystemUpgrade("/non/existent/path")
		assert.Error(t, err)
		assert.Nil(t, versions)
	})

	t.Run("empty plans directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create empty nested directory structure
		plansDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "system-upgrade-controller", "plans")
		err := os.MkdirAll(plansDir, 0755)
		require.NoError(t, err)

		// The function doesn't actually check if directory is empty, it tries to load specific files
		// and will use fallback versions if they don't exist
		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		// This should actually succeed with fallback versions
		assert.NoError(t, err)
		assert.NotNil(t, versions)
		// Should use fallback versions
		assert.Equal(t, "v1.11.0", versions.TalosVersion)
		assert.Equal(t, "v1.34.0", versions.KubernetesVersion)
	})

	t.Run("invalid yaml in plan file", func(t *testing.T) {
		tmpDir := t.TempDir()
		plansDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "system-upgrade-controller", "plans")
		err := os.MkdirAll(plansDir, 0755)
		require.NoError(t, err)

		invalidPlan := filepath.Join(plansDir, "talos.yaml")
		invalidContent := `invalid: yaml: content:`
		err = os.WriteFile(invalidPlan, []byte(invalidContent), 0644)
		require.NoError(t, err)

		// Function will use fallback versions on error, not fail completely
		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		assert.NoError(t, err)
		assert.NotNil(t, versions)
		// Should use fallback version for talos due to invalid yaml
		assert.Equal(t, "v1.11.0", versions.TalosVersion)
	})

	t.Run("missing version field in plan", func(t *testing.T) {
		tmpDir := t.TempDir()
		plansDir := filepath.Join(tmpDir, "kubernetes", "apps", "system-upgrade", "system-upgrade-controller", "plans")
		err := os.MkdirAll(plansDir, 0755)
		require.NoError(t, err)

		planWithoutVersion := filepath.Join(plansDir, "talos.yaml")
		content := `
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: talos
spec:
  image: factory.talos.dev/installer/v1.9.0
  # version field missing
`
		err = os.WriteFile(planWithoutVersion, []byte(content), 0644)
		require.NoError(t, err)

		// Function will use fallback versions on error, not fail completely
		versions, err := LoadVersionsFromSystemUpgrade(tmpDir)
		assert.NoError(t, err)
		assert.NotNil(t, versions)
		// Should use fallback version due to missing version field
		assert.Equal(t, "v1.11.0", versions.TalosVersion)
	})
}

func TestLoadVersionFromPlan(t *testing.T) {
	t.Run("valid talos plan", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "talos-*.yaml")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpFile.Name()) }()

		content := `
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: talos
spec:
  version: v1.9.0
`
		_, err = tmpFile.WriteString(content)
		require.NoError(t, err)
		_ = tmpFile.Close()

		version, err := loadVersionFromPlan(tmpFile.Name(), "talos")
		require.NoError(t, err)
		assert.Equal(t, "v1.9.0", version)
	})

	t.Run("valid kubernetes plan", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "kubernetes-*.yaml")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpFile.Name()) }()

		content := `
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: kubernetes
spec:
  version: v1.32.0
`
		_, err = tmpFile.WriteString(content)
		require.NoError(t, err)
		_ = tmpFile.Close()

		version, err := loadVersionFromPlan(tmpFile.Name(), "kubernetes")
		require.NoError(t, err)
		assert.Equal(t, "v1.32.0", version)
	})

	t.Run("non-existent file", func(t *testing.T) {
		version, err := loadVersionFromPlan("/non/existent/file.yaml", "talos")
		assert.Error(t, err)
		assert.Empty(t, version)
	})
}

func TestGetVersions(t *testing.T) {
	t.Run("with valid system-upgrade plans", func(t *testing.T) {
		// This will use the actual filesystem
		// In a real test environment, you might want to mock this
		versions := GetVersions(".")
		assert.NotNil(t, versions)
		assert.NotEmpty(t, versions.TalosVersion)
		assert.NotEmpty(t, versions.KubernetesVersion)
	})

	t.Run("fallback to defaults", func(t *testing.T) {
		// When plans directory doesn't exist, should fallback to defaults
		versions := GetVersions("/non/existent/path")
		assert.NotNil(t, versions)
		assert.NotEmpty(t, versions.TalosVersion)
		assert.NotEmpty(t, versions.KubernetesVersion)

		// Should match default versions
		defaultVersions := getDefaultVersions()
		assert.Equal(t, defaultVersions.TalosVersion, versions.TalosVersion)
		assert.Equal(t, defaultVersions.KubernetesVersion, versions.KubernetesVersion)
	})
}

func TestVersionConfig(t *testing.T) {
	vc := &VersionConfig{
		TalosVersion:      "v1.8.2",
		KubernetesVersion: "v1.31.1",
	}

	assert.Equal(t, "v1.8.2", vc.TalosVersion)
	assert.Equal(t, "v1.31.1", vc.KubernetesVersion)
}

func TestSystemUpgradePlan(t *testing.T) {
	plan := &SystemUpgradePlan{
		Spec: struct {
			Version string `yaml:"version"`
		}{
			Version: "v1.9.0",
		},
	}

	assert.Equal(t, "v1.9.0", plan.Spec.Version)
}

// Benchmark tests
func BenchmarkIsValidVersionFormat(b *testing.B) {
	versions := []string{
		"v1.8.2",
		"1.8.2",
		"v1.31.1",
		"invalid",
		"",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range versions {
			_ = isValidVersionFormat(v)
		}
	}
}

func BenchmarkGetDefaultVersions(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = getDefaultVersions()
	}
}

func BenchmarkLoadVersionFromPlan(b *testing.B) {
	tmpFile, err := os.CreateTemp("", "plan-*.yaml")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	content := `
apiVersion: upgrade.cattle.io/v1
kind: Plan
spec:
  version: v1.9.0
`
	_, _ = tmpFile.WriteString(content)
	_ = tmpFile.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = loadVersionFromPlan(tmpFile.Name(), "talos")
	}
}
