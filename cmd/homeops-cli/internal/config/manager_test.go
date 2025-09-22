package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Check that values are set (don't hardcode versions as they're dynamic)
	assert.NotEmpty(t, cfg.TalosVersion)
	assert.NotEmpty(t, cfg.KubernetesVersion)
	assert.Equal(t, "homeops", cfg.OnePasswordVault)
	assert.Equal(t, "", cfg.TrueNASHost)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.NotEmpty(t, cfg.CacheDir)
	assert.Equal(t, 300, cfg.SecretCacheTTL)

	// Verify version format
	assert.Regexp(t, `^v\d+\.\d+\.\d+$`, cfg.TalosVersion)
	assert.Regexp(t, `^v\d+\.\d+\.\d+$`, cfg.KubernetesVersion)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &Config{
				TalosVersion:      "v1.8.2",
				KubernetesVersion: "v1.31.1",
				OnePasswordVault:  "vault",
				LogLevel:          "info",
				CacheDir:          ".cache",
				SecretCacheTTL:    300,
			},
			wantErr: false,
		},
		{
			name: "empty talos version",
			config: &Config{
				TalosVersion:      "",
				KubernetesVersion: "v1.31.1",
				OnePasswordVault:  "vault",
				LogLevel:          "info",
				CacheDir:          ".cache",
				SecretCacheTTL:    300,
			},
			wantErr: true,
			errMsg:  "talos_version is required",
		},
		{
			name: "empty kubernetes version",
			config: &Config{
				TalosVersion:      "v1.8.2",
				KubernetesVersion: "",
				OnePasswordVault:  "vault",
				LogLevel:          "info",
				CacheDir:          ".cache",
				SecretCacheTTL:    300,
			},
			wantErr: true,
			errMsg:  "kubernetes_version is required",
		},
		{
			name: "invalid log level",
			config: &Config{
				TalosVersion:      "v1.8.2",
				KubernetesVersion: "v1.31.1",
				OnePasswordVault:  "vault",
				LogLevel:          "invalid",
				CacheDir:          ".cache",
				SecretCacheTTL:    300,
			},
			wantErr: true,
			errMsg:  "invalid log_level",
		},
		{
			name: "negative cache TTL",
			config: &Config{
				TalosVersion:      "v1.8.2",
				KubernetesVersion: "v1.31.1",
				OnePasswordVault:  "vault",
				LogLevel:          "info",
				CacheDir:          ".cache",
				SecretCacheTTL:    -1,
			},
			wantErr: true,
			errMsg:  "secret_cache_ttl must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	// Create a temporary directory for test
	tmpDir := t.TempDir()

	// Test with no config file (should use defaults)
	t.Run("no config file", func(t *testing.T) {
		// Set HOME to a temp dir with no config
		oldHome := os.Getenv("HOME")
		_ = os.Setenv("HOME", tmpDir)
		defer func() { _ = os.Setenv("HOME", oldHome) }()

		cfg, err := LoadConfig()
		require.NoError(t, err)
		assert.NotNil(t, cfg)

		// Should have default values
		assert.Equal(t, "v1.8.2", cfg.TalosVersion)
		assert.Equal(t, "v1.31.1", cfg.KubernetesVersion)
	})

	// Test with config file
	t.Run("with config file", func(t *testing.T) {
		configDir := filepath.Join(tmpDir, ".config", "homeops")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		configFile := filepath.Join(configDir, "config.yaml")
		configContent := `
talos_version: v1.9.0
kubernetes_version: v1.32.0
onepassword_vault: testvault
log_level: debug
cache_dir: /tmp/cache
secret_cache_ttl: 600
`
		err = os.WriteFile(configFile, []byte(configContent), 0644)
		require.NoError(t, err)

		cfg, err := LoadConfigFromPath(configFile)
		require.NoError(t, err)
		assert.NotNil(t, cfg)

		assert.Equal(t, "v1.9.0", cfg.TalosVersion)
		assert.Equal(t, "v1.32.0", cfg.KubernetesVersion)
		assert.Equal(t, "testvault", cfg.OnePasswordVault)
		assert.Equal(t, "debug", cfg.LogLevel)
		assert.Equal(t, "/tmp/cache", cfg.CacheDir)
		assert.Equal(t, 600, cfg.SecretCacheTTL)
	})

	// Test with environment variable overrides
	t.Run("with env overrides", func(t *testing.T) {
		configDir := filepath.Join(tmpDir, ".config", "homeops")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		configFile := filepath.Join(configDir, "config.yaml")
		configContent := `
talos_version: v1.9.0
kubernetes_version: v1.32.0
`
		err = os.WriteFile(configFile, []byte(configContent), 0644)
		require.NoError(t, err)

		// Set environment variables
		_ = os.Setenv("HOMEOPS_TALOS_VERSION", "v2.0.0")
		_ = os.Setenv("HOMEOPS_LOG_LEVEL", "trace")
		defer func() {
			_ = os.Unsetenv("HOMEOPS_TALOS_VERSION")
			_ = os.Unsetenv("HOMEOPS_LOG_LEVEL")
		}()

		cfg, err := LoadConfigFromPath(configFile)
		require.NoError(t, err)
		assert.NotNil(t, cfg)

		// Environment variables should override file config
		assert.Equal(t, "v2.0.0", cfg.TalosVersion)
		assert.Equal(t, "trace", cfg.LogLevel)
		assert.Equal(t, "v1.32.0", cfg.KubernetesVersion) // From file
	})
}

func TestGetConfigPath(t *testing.T) {
	tests := []struct {
		name     string
		homeVar  string
		expected string
	}{
		{
			name:     "with HOME set",
			homeVar:  "/home/testuser",
			expected: "/home/testuser/.config/homeops/config.yaml",
		},
		{
			name:     "with HOME unset",
			homeVar:  "",
			expected: filepath.Join(".", ".config", "homeops", "config.yaml"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldHome := os.Getenv("HOME")
			if tt.homeVar != "" {
				_ = os.Setenv("HOME", tt.homeVar)
			} else {
				_ = os.Unsetenv("HOME")
			}
			defer func() { _ = os.Setenv("HOME", oldHome) }()

			path := GetConfigPath()
			assert.Equal(t, tt.expected, path)
		})
	}
}

func TestLoadConfigFromPath(t *testing.T) {
	t.Run("invalid yaml", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "config-*.yaml")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpFile.Name()) }()

		// Write invalid YAML
		_, err = tmpFile.WriteString("invalid: yaml: content:")
		require.NoError(t, err)
		_ = tmpFile.Close()

		cfg, err := LoadConfigFromPath(tmpFile.Name())
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("non-existent file", func(t *testing.T) {
		cfg, err := LoadConfigFromPath("/non/existent/file.yaml")
		// Should return default config when file doesn't exist
		assert.NoError(t, err)
		assert.NotNil(t, cfg)
		assert.Equal(t, DefaultConfig(), cfg)
	})

	t.Run("partial config", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "config-*.yaml")
		require.NoError(t, err)
		defer func() { _ = os.Remove(tmpFile.Name()) }()

		// Write partial config
		configContent := `
talos_version: v1.9.5
# other fields not specified
`
		_, err = tmpFile.WriteString(configContent)
		require.NoError(t, err)
		_ = tmpFile.Close()

		cfg, err := LoadConfigFromPath(tmpFile.Name())
		require.NoError(t, err)
		assert.NotNil(t, cfg)

		// Specified field should be from file
		assert.Equal(t, "v1.9.5", cfg.TalosVersion)
		// Unspecified fields should have defaults
		assert.Equal(t, "v1.31.1", cfg.KubernetesVersion)
		assert.Equal(t, "info", cfg.LogLevel)
		assert.Equal(t, 300, cfg.SecretCacheTTL)
	})
}

// Benchmark tests
func BenchmarkDefaultConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = DefaultConfig()
	}
}

func BenchmarkConfigValidate(b *testing.B) {
	cfg := DefaultConfig()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cfg.Validate()
	}
}

func BenchmarkLoadConfig(b *testing.B) {
	// Create a test config file
	tmpDir := b.TempDir()
	configDir := filepath.Join(tmpDir, ".config", "homeops")
	_ = os.MkdirAll(configDir, 0755)

	configFile := filepath.Join(configDir, "config.yaml")
	configContent := `
talos_version: v1.9.0
kubernetes_version: v1.32.0
onepassword_vault: testvault
log_level: debug
`
	_ = os.WriteFile(configFile, []byte(configContent), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = LoadConfigFromPath(configFile)
	}
}
