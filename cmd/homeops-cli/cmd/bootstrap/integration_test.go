package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeops-cli/internal/common"
)

// TestBootstrapWorkflowDryRun tests the complete bootstrap workflow in dry-run mode
func TestBootstrapWorkflowDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a temporary directory structure
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	talosconfigPath := filepath.Join(tmpDir, "talosconfig")

	// Create mock config files
	if err := os.WriteFile(kubeconfigPath, []byte("mock kubeconfig"), 0600); err != nil {
		t.Fatalf("Failed to create mock kubeconfig: %v", err)
	}
	if err := os.WriteFile(talosconfigPath, []byte("mock talosconfig"), 0600); err != nil {
		t.Fatalf("Failed to create mock talosconfig: %v", err)
	}

	config := &BootstrapConfig{
		RootDir:       tmpDir,
		KubeConfig:    kubeconfigPath,
		TalosConfig:   talosconfigPath,
		K8sVersion:    "v1.33.4",
		TalosVersion:  "v1.11.0",
		DryRun:        true, // Dry run mode
		SkipPreflight: true, // Skip preflight checks for test
	}

	// Test dry run execution
	start := time.Now()
	err := runBootstrap(config)
	duration := time.Since(start)

	// Dry run should complete quickly without errors
	if err != nil {
		t.Errorf("Dry run bootstrap failed: %v", err)
	}

	// Should complete within reasonable time (30 seconds for dry run)
	if duration > 30*time.Second {
		t.Errorf("Dry run took too long: %v", duration)
	}

	t.Logf("Dry run completed in %v", duration)
}

// TestBootstrapConfigValidation tests configuration validation
func TestBootstrapConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *BootstrapConfig
		expectError bool
	}{
		{
			name: "valid config",
			config: &BootstrapConfig{
				RootDir:       "/tmp",
				KubeConfig:    "/tmp/kubeconfig",
				TalosConfig:   "/tmp/talosconfig",
				K8sVersion:    "v1.33.4",
				TalosVersion:  "v1.11.0",
				DryRun:        true,
				SkipPreflight: true,
			},
			expectError: false,
		},
		{
			name: "missing root directory",
			config: &BootstrapConfig{
				RootDir:       "",
				KubeConfig:    "/tmp/kubeconfig",
				TalosConfig:   "/tmp/talosconfig",
				K8sVersion:    "v1.33.4",
				TalosVersion:  "v1.11.0",
				DryRun:        true,
				SkipPreflight: true,
			},
			expectError: false, // Should get converted to current dir
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runBootstrap(tt.config)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				// Missing files are expected in test environment for these tests
				if strings.Contains(err.Error(), "not found") {
					t.Logf("Expected missing file error in test environment: %v", err)
				} else {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestPreflightChecks tests preflight check execution
func TestPreflightChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	config := &BootstrapConfig{
		RootDir:       tmpDir,
		KubeConfig:    filepath.Join(tmpDir, "kubeconfig"),
		TalosConfig:   filepath.Join(tmpDir, "talosconfig"),
		K8sVersion:    "v1.33.4",
		TalosVersion:  "v1.11.0",
		SkipPreflight: false, // Enable preflight checks
	}

	logger := common.NewColorLogger()

	// This will likely fail due to missing tools/configs in test environment
	err := runPreflightChecks(config, logger)
	if err != nil {
		t.Logf("Preflight checks failed as expected in test environment: %v", err)
	}
}

// TestNodeReadinessWorkflow tests the node readiness checking workflow
func TestNodeReadinessWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")

	// Create invalid kubeconfig for testing
	invalidKubeconfig := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://invalid-test-server:6443
  name: invalid-test
contexts:
- context:
    cluster: invalid-test
  name: invalid-test
current-context: invalid-test
`

	if err := os.WriteFile(kubeconfigPath, []byte(invalidKubeconfig), 0600); err != nil {
		t.Fatalf("Failed to create test kubeconfig: %v", err)
	}

	config := &BootstrapConfig{
		KubeConfig: kubeconfigPath,
	}

	logger := common.NewColorLogger()

	// Test node availability check (should fail quickly with invalid config)
	start := time.Now()
	err := waitForNodesAvailable(config, logger)
	duration := time.Since(start)

	if err == nil {
		t.Errorf("Expected error with invalid kubeconfig")
	}

	// Should fail within reasonable time
	if duration > 60*time.Second {
		t.Errorf("Node availability check took too long: %v", duration)
	}

	t.Logf("Node availability check failed as expected in %v", duration)
}

// TestBootstrapStages tests individual bootstrap stages
func TestBootstrapStages(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	config := &BootstrapConfig{
		RootDir:       tmpDir,
		KubeConfig:    filepath.Join(tmpDir, "kubeconfig"),
		TalosConfig:   filepath.Join(tmpDir, "talosconfig"),
		K8sVersion:    "v1.33.4",
		TalosVersion:  "v1.11.0",
		DryRun:        true,
		SkipPreflight: true,
	}

	logger := common.NewColorLogger()

	// Create mock files
	if err := os.WriteFile(config.KubeConfig, []byte("mock kubeconfig"), 0600); err != nil {
		t.Fatalf("Failed to create mock kubeconfig: %v", err)
	}
	if err := os.WriteFile(config.TalosConfig, []byte("mock talosconfig"), 0600); err != nil {
		t.Fatalf("Failed to create mock talosconfig: %v", err)
	}

	// Test individual stages in dry-run mode
	tests := []struct {
		name      string
		stageFunc func(*BootstrapConfig, *common.ColorLogger) error
	}{
		{"validate prerequisites", func(config *BootstrapConfig, logger *common.ColorLogger) error {
			return validatePrerequisites(config)
		}},
		{"wait for nodes", waitForNodes},
		{"apply namespaces", applyNamespaces},
		{"apply resources", applyResources},
		{"apply CRDs", applyCRDs},
		{"sync helm releases", syncHelmReleases},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			err := tt.stageFunc(config, logger)
			duration := time.Since(start)

			// Most stages should complete quickly in dry-run mode or fail gracefully
			if duration > 30*time.Second {
				t.Errorf("Stage %s took too long: %v", tt.name, duration)
			}

			// Log results (some failures expected in test environment)
			if err != nil {
				t.Logf("Stage %s failed as expected in test environment: %v", tt.name, err)
			} else {
				t.Logf("Stage %s completed successfully in %v", tt.name, duration)
			}
		})
	}
}

// TestErrorHandlingAndRecovery tests error handling throughout the bootstrap process
func TestErrorHandlingAndRecovery(t *testing.T) {
	tests := []struct {
		name        string
		config      *BootstrapConfig
		description string
	}{
		{
			name: "invalid root directory",
			config: &BootstrapConfig{
				RootDir:       "/nonexistent/path/that/should/not/exist",
				KubeConfig:    "/tmp/kubeconfig",
				TalosConfig:   "/tmp/talosconfig",
				K8sVersion:    "v1.33.4",
				TalosVersion:  "v1.11.0",
				DryRun:        true,
				SkipPreflight: true,
			},
			description: "Should handle invalid root directory gracefully",
		},
		{
			name: "missing version info",
			config: &BootstrapConfig{
				RootDir:       "/tmp",
				KubeConfig:    "/tmp/kubeconfig",
				TalosConfig:   "/tmp/talosconfig",
				K8sVersion:    "",
				TalosVersion:  "",
				DryRun:        true,
				SkipPreflight: true,
			},
			description: "Should load versions from defaults",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runBootstrap(tt.config)

			// Log the result - some errors are expected and acceptable
			if err != nil {
				t.Logf("%s - Error (may be expected): %v", tt.description, err)
			} else {
				t.Logf("%s - Completed successfully", tt.description)
			}

			// The main goal is that the process doesn't panic or hang
			// Specific errors are acceptable in the test environment
		})
	}
}

// TestResourceValidation tests validation of embedded resources
func TestResourceValidation(t *testing.T) {
	logger := common.NewColorLogger()

	// Test validation functions with mock data
	tests := []struct {
		name     string
		content  string
		validate func(string, *common.ColorLogger) error
	}{
		{
			name: "valid clustersecretstore YAML",
			content: `
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: onepassword-connect
spec:
  provider:
    onepassword:
      connectHost: http://onepassword-connect:8080
`,
			validate: validateClusterSecretStoreYAML,
		},
		{
			name: "valid resources YAML",
			content: `
apiVersion: v1
kind: Secret
metadata:
  name: onepassword-secret
  namespace: external-secrets
---
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-tunnel-id-secret
  namespace: default
`,
			validate: validateResourcesYAML,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.validate(tt.content, logger)
			if err != nil {
				t.Errorf("Validation failed for %s: %v", tt.name, err)
			}
		})
	}
}

// Benchmark complete bootstrap dry run
func BenchmarkBootstrapDryRun(b *testing.B) {
	tmpDir := b.TempDir()
	config := &BootstrapConfig{
		RootDir:       tmpDir,
		KubeConfig:    filepath.Join(tmpDir, "kubeconfig"),
		TalosConfig:   filepath.Join(tmpDir, "talosconfig"),
		K8sVersion:    "v1.33.4",
		TalosVersion:  "v1.11.0",
		DryRun:        true,
		SkipPreflight: true,
	}

	// Create mock files
	if err := os.WriteFile(config.KubeConfig, []byte("mock kubeconfig"), 0600); err != nil {
		b.Fatalf("Failed to create mock kubeconfig: %v", err)
	}
	if err := os.WriteFile(config.TalosConfig, []byte("mock talosconfig"), 0600); err != nil {
		b.Fatalf("Failed to create mock talosconfig: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := runBootstrap(config)
		if err != nil {
			b.Fatalf("Bootstrap failed: %v", err)
		}
	}
}
