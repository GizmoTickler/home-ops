package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"homeops-cli/internal/common"
)

// VersionConfig holds version information extracted from the kubeadm
// System Upgrade Controller Plan (Flatcar/kubeadm clusters).
// TalosVersion is zeroed — Flatcar clusters use kubeadm, not Talos.
// Flatcar/kube-vip/pause defaults are applied on top of the loaded
// Kubernetes version. Callers that need Talos version resolution
// should continue to use the tuppr path (which is separate).
type VersionConfig struct {
	KubernetesVersion string
	TalosVersion      string

	// Flatcar/kubeadm migration knobs.
	FlatcarVersion string // Flatcar stable release version (e.g. "current" or "4152.2.0")
	KubeVipVersion string // kube-vip image tag (e.g. "v0.8.9")
	PauseImage     string // sandbox/pause image (e.g. "registry.k8s.io/pause:3.10")
}

const (
	defaultFlatcarVersion = "current"
	defaultKubeVipVersion = "v0.8.9"
	defaultPauseImage     = "registry.k8s.io/pause:3.10"
	// defaultTalosVersion is the version used by the LEGACY `--provider talos`
	// path (bootstrap preflight + Talos ISO generation). Flatcar ignores it.
	// Tracks the install.image tag in internal/templates/talos/controlplane.yaml.
	defaultTalosVersion = "v1.13.3"
)

// LoadVersionsFromSystemUpgrade loads the Kubernetes target version from
// the kubeadm System Upgrade Controller Plan (kubernetes/apps/system-upgrade/
// kubeadm-upgrade/app/plan.yaml) and applies Flatcar defaults.
func LoadVersionsFromSystemUpgrade(rootDir string) (*VersionConfig, error) {
	logger := common.NewColorLogger()

	k8sVersion, err := loadKubernetesVersionFromPlan(rootDir)
	if err != nil {
		logger.Debug("Failed to load Kubernetes version from kubeadm Plan: %v", err)
		return nil, fmt.Errorf("kubeadm Plan version load failed: %w", err)
	}

	config := &VersionConfig{
		KubernetesVersion: k8sVersion,
		// Flatcar ignores TalosVersion; populate the legacy-talos default so a
		// `--provider talos` bootstrap (preflight + ISO naming) still resolves.
		TalosVersion: defaultTalosVersion,
	}
	applyFlatcarDefaults(config)

	logger.Debug("Loaded versions from kubeadm Plan:")
	logger.Debug("  - Kubernetes: %s", config.KubernetesVersion)
	logger.Debug("  - Flatcar: %s  kube-vip: %s  pause: %s",
		config.FlatcarVersion, config.KubeVipVersion, config.PauseImage)

	return config, nil
}

// loadKubernetesVersionFromPlan reads the kubeadm Plan and extracts
// spec.version. Uses minimal string scraping rather than YAML
// unmarshalling so it survives apiVersion/kind drift over time.
func loadKubernetesVersionFromPlan(rootDir string) (string, error) {
	planPath := filepath.Join(rootDir, "kubernetes", "apps", "system-upgrade",
		"kubeadm-upgrade", "app", "plan.yaml")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return "", fmt.Errorf("failed to read kubeadm upgrade plan %s: %w", planPath, err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "version:") {
			ver := strings.TrimSpace(strings.TrimPrefix(trimmed, "version:"))
			ver = strings.Trim(ver, "\"'")
			if !isValidVersionFormat(ver) {
				return "", fmt.Errorf("kubeadm plan version %q does not look like a Kubernetes version", ver)
			}
			return ver, nil
		}
	}
	return "", fmt.Errorf("kubeadm plan %s does not contain a 'version:' field", planPath)
}

// isValidVersionFormat validates that the version string looks like a semantic version.
func isValidVersionFormat(version string) bool {
	versionRegex := regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[\w\.]+)?$`)
	return versionRegex.MatchString(version)
}

// applyFlatcarDefaults fills in any unset Flatcar/kubeadm knobs with their defaults.
func applyFlatcarDefaults(c *VersionConfig) {
	if c.FlatcarVersion == "" {
		c.FlatcarVersion = defaultFlatcarVersion
	}
	if c.KubeVipVersion == "" {
		c.KubeVipVersion = defaultKubeVipVersion
	}
	if c.PauseImage == "" {
		c.PauseImage = defaultPauseImage
	}
}

// getDefaultVersions returns hardcoded fallback versions.
func getDefaultVersions() *VersionConfig {
	c := &VersionConfig{
		KubernetesVersion: "v1.36.1",
		// Flatcar ignores TalosVersion; use the legacy-talos default (not a
		// v0.0.0 stub) so the `--provider talos` path resolves a real version.
		TalosVersion: defaultTalosVersion,
	}
	applyFlatcarDefaults(c)
	return c
}

// GetVersions loads versions from the kubeadm Plan (primary source).
// This function automatically finds the git repository root regardless
// of where it's called from.
func GetVersions(rootDir string) *VersionConfig {
	logger := common.NewColorLogger()

	actualRootDir := rootDir
	if rootDir == "." {
		gitRoot, err := common.FindGitRoot(".")
		if err != nil {
			logger.Debug("Could not find git root, using current directory: %v", err)
		} else {
			actualRootDir = gitRoot
			logger.Debug("Found git repository root: %s", gitRoot)
		}
	}

	config, err := LoadVersionsFromSystemUpgrade(actualRootDir)
	if err != nil {
		logger.Warn("Failed to load versions from kubeadm Plan: %v", err)
		logger.Warn("Using hardcoded fallback versions — this should not happen in production")
		return getDefaultVersions()
	}

	logger.Debug("Loaded versions from kubeadm Plan:")
	logger.Debug("  - Kubernetes: %s", config.KubernetesVersion)

	return config
}
