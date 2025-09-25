package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"homeops-cli/internal/common"
)

// Config represents the application configuration
type Config struct {
	TalosVersion      string `mapstructure:"talos_version"`
	KubernetesVersion string `mapstructure:"kubernetes_version"`
	OnePasswordVault  string `mapstructure:"onepassword_vault"`
	TrueNASHost       string `mapstructure:"truenas_host"`
	LogLevel          string `mapstructure:"log_level"`
	CacheDir          string `mapstructure:"cache_dir"`
	SecretCacheTTL    int    `mapstructure:"secret_cache_ttl"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	// Load versions dynamically from system-upgrade plans
	versions := GetVersions(common.GetWorkingDirectory())

	return &Config{
		TalosVersion:      versions.TalosVersion,
		KubernetesVersion: versions.KubernetesVersion,
		OnePasswordVault:  "homeops",
		LogLevel:          "info",
		CacheDir:          filepath.Join(os.TempDir(), "homeops-cli"),
		SecretCacheTTL:    300, // 5 minutes
	}
}

// LoadConfig loads configuration from file and environment variables
func LoadConfig() (*Config, error) {
	return LoadConfigFromPath("")
}

// LoadConfigFromPath loads configuration from a specific file path
func LoadConfigFromPath(configPath string) (*Config, error) {
	// Set defaults
	config := DefaultConfig()

	// Configure viper
	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		// Check for environment variable first
		if envPath := os.Getenv("HOMEOPS_CONFIG"); envPath != "" {
			viper.SetConfigFile(envPath)
		} else {
			viper.SetConfigName("homeops")
			viper.SetConfigType("yaml")
			viper.AddConfigPath(".")
			viper.AddConfigPath("$HOME/.config/homeops")
			viper.AddConfigPath("/etc/homeops")
		}
	}

	// Environment variable support
	viper.SetEnvPrefix("HOMEOPS")
	viper.AutomaticEnv()

	// Set defaults in viper
	viper.SetDefault("talos_version", config.TalosVersion)
	viper.SetDefault("kubernetes_version", config.KubernetesVersion)
	viper.SetDefault("onepassword_vault", config.OnePasswordVault)
	viper.SetDefault("log_level", config.LogLevel)
	viper.SetDefault("cache_dir", config.CacheDir)
	viper.SetDefault("secret_cache_ttl", config.SecretCacheTTL)

	// Try to read config file (it's okay if it doesn't exist)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
	}

	// Unmarshal into struct
	if err := viper.Unmarshal(config); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return config, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.LogLevel == "" {
		return fmt.Errorf("log_level cannot be empty")
	}

	validLogLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}

	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("invalid log_level: %s (must be one of: debug, info, warn, error)", c.LogLevel)
	}

	if c.SecretCacheTTL < 0 {
		return fmt.Errorf("secret_cache_ttl must be non-negative")
	}

	return nil
}

// GetConfigPath returns the path to the configuration file if it exists
func GetConfigPath() string {
	return viper.ConfigFileUsed()
}
