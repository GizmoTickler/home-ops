package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
)

// VersionConfig holds version information extracted from system-upgrade plans
type VersionConfig struct {
	KubernetesVersion string
	TalosVersion      string
}

// SystemUpgradePlan represents the structure of system-upgrade controller plans
type SystemUpgradePlan struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Version string `yaml:"version"`
	} `yaml:"spec"`
}

// LoadVersionsFromSystemUpgrade extracts version information from system-upgrade controller plans
func LoadVersionsFromSystemUpgrade(rootDir string) (*VersionConfig, error) {
	logger := common.NewColorLogger()
	
	// Path to system-upgrade plans
	plansDir := filepath.Join(rootDir, "kubernetes", "apps", "system-upgrade", "system-upgrade-controller", "plans")
	
	if _, err := os.Stat(plansDir); os.IsNotExist(err) {
		logger.Debug("System upgrade plans directory not found: %s", plansDir)
		return getDefaultVersions(), nil
	}
	
	config := &VersionConfig{}
	
	// Load Kubernetes version from kubernetes.yaml
	if k8sVersion, err := loadVersionFromPlan(filepath.Join(plansDir, "kubernetes.yaml"), "kubelet"); err == nil {
		config.KubernetesVersion = k8sVersion
		logger.Debug("Loaded Kubernetes version from system-upgrade plan: %s", k8sVersion)
	} else {
		logger.Debug("Failed to load Kubernetes version: %v", err)
		config.KubernetesVersion = "v1.33.4" // fallback
	}
	
	// Load Talos version from talos.yaml
	if talosVersion, err := loadVersionFromPlan(filepath.Join(plansDir, "talos.yaml"), "installer"); err == nil {
		config.TalosVersion = talosVersion
		logger.Debug("Loaded Talos version from system-upgrade plan: %s", talosVersion)
	} else {
		logger.Debug("Failed to load Talos version: %v", err)
		config.TalosVersion = "v1.11.0-rc.0" // fallback
	}
	
	return config, nil
}

// loadVersionFromPlan loads version from a specific system-upgrade plan file
func loadVersionFromPlan(planPath, expectedComponent string) (string, error) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return "", fmt.Errorf("failed to read plan file %s: %w", planPath, err)
	}
	
	// Parse YAML
	var plan SystemUpgradePlan
	if err := yaml.Unmarshal(data, &plan); err != nil {
		return "", fmt.Errorf("failed to parse plan YAML: %w", err)
	}
	
	// Validate it's a system-upgrade plan
	if plan.APIVersion != "upgrade.cattle.io/v1" || plan.Kind != "Plan" {
		return "", fmt.Errorf("invalid system-upgrade plan format")
	}
	
	// Validate the component matches expected (basic sanity check)
	planContent := string(data)
	if !strings.Contains(planContent, expectedComponent) {
		return "", fmt.Errorf("plan does not contain expected component %s", expectedComponent)
	}
	
	// Validate version format
	version := plan.Spec.Version
	if !isValidVersionFormat(version) {
		return "", fmt.Errorf("invalid version format: %s", version)
	}
	
	return version, nil
}

// isValidVersionFormat validates that the version string looks like a semantic version
func isValidVersionFormat(version string) bool {
	// Match patterns like v1.33.4, v1.11.0-rc.0, etc.
	versionRegex := regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[\w\.]+)?$`)
	return versionRegex.MatchString(version)
}

// getDefaultVersions returns hardcoded fallback versions
func getDefaultVersions() *VersionConfig {
	return &VersionConfig{
		KubernetesVersion: "v1.33.4",
		TalosVersion:      "v1.11.0-rc.0",
	}
}

// GetVersions is a convenience function that attempts to load from system-upgrade plans
// with fallback to defaults. This is the main entry point for the CLI.
func GetVersions(rootDir string) *VersionConfig {
	if config, err := LoadVersionsFromSystemUpgrade(rootDir); err == nil {
		return config
	}
	
	// Fallback to defaults if loading fails
	return getDefaultVersions()
}