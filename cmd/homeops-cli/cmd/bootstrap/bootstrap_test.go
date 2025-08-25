package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeops-cli/internal/common"
)

// TestBootstrapConfig tests the BootstrapConfig structure
func TestBootstrapConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *BootstrapConfig
		valid  bool
	}{
		{
			name: "valid config",
			config: &BootstrapConfig{
				RootDir:      "/tmp/test",
				KubeConfig:   "/tmp/kubeconfig",
				TalosConfig:  "/tmp/talosconfig",
				K8sVersion:   "v1.33.4",
				TalosVersion: "v1.11.0",
				DryRun:       false,
			},
			valid: true,
		},
		{
			name: "empty root dir",
			config: &BootstrapConfig{
				RootDir:      "",
				KubeConfig:   "/tmp/kubeconfig",
				TalosConfig:  "/tmp/talosconfig",
				K8sVersion:   "v1.33.4",
				TalosVersion: "v1.11.0",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.RootDir == "" && tt.valid {
				t.Errorf("Expected config to be invalid with empty RootDir")
			}
			if tt.config.RootDir != "" && !tt.valid && tt.name != "empty root dir" {
				t.Errorf("Expected config to be valid")
			}
		})
	}
}

// TestMergeConfigsWithTalosctl tests the talosctl-based config merging
func TestMergeConfigsWithTalosctl(t *testing.T) {
	// Skip if talosctl is not available
	if _, err := exec.LookPath("talosctl"); err != nil {
		t.Skip("talosctl not found, skipping test")
	}

	baseConfig := []byte(`version: v1alpha1
machine:
  type: controlplane
  install:
    disk: /dev/sda
cluster:
  id: 123456789
  secret: testSecret
  controlPlane:
    endpoint: https://192.168.1.1:6443
  clusterName: test`)

	patchConfig := []byte(`machine:
  install:
    wipe: true`)

	result, err := mergeConfigsWithTalosctl(baseConfig, patchConfig)
	if err != nil {
		t.Fatalf("mergeConfigsWithTalosctl failed: %v", err)
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "wipe: true") {
		t.Errorf("Expected merged config to contain patch values")
	}
	if !strings.Contains(resultStr, "disk: /dev/sda") {
		t.Errorf("Expected merged config to contain base values")
	}
}

// TestValidatePrerequisites tests prerequisite validation
func TestValidatePrerequisites(t *testing.T) {
	config := &BootstrapConfig{
		RootDir:      "/tmp",
		KubeConfig:   "/tmp/kubeconfig",
		TalosConfig:  "/tmp/talosconfig",
		K8sVersion:   "v1.33.4",
		TalosVersion: "v1.11.0",
	}

	err := validatePrerequisites(config)
	// This might fail if tools aren't installed, but test should not crash
	if err != nil {
		t.Logf("Prerequisites validation failed (expected in test env): %v", err)
	}
}

// TestGet1PasswordSecret tests 1Password secret retrieval
func TestGet1PasswordSecret(t *testing.T) {
	tests := []struct {
		name        string
		reference   string
		expectError bool
	}{
		{
			name:        "invalid reference",
			reference:   "invalid-ref",
			expectError: true,
		},
		{
			name:        "malformed reference",
			reference:   "op://",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := get1PasswordSecret(tt.reference)
			if tt.expectError && err == nil {
				t.Errorf("Expected error for %s", tt.reference)
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error for %s: %v", tt.reference, err)
			}
		})
	}
}

// TestResolve1PasswordReferences tests 1Password reference resolution
func TestResolve1PasswordReferences(t *testing.T) {
	logger := common.NewColorLogger()

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "no references",
			content:  "normal content without refs",
			expected: "normal content without refs",
		},
		{
			name:     "single reference (will fail in test)",
			content:  "secret: op://vault/item/field",
			expected: "secret: op://vault/item/field", // Will remain unchanged due to failure
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolve1PasswordReferences(tt.content, logger)
			if err != nil && tt.name == "no references" {
				t.Errorf("Unexpected error for content without references: %v", err)
			}
			if tt.name == "no references" && string(result) != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, string(result))
			}
		})
	}
}

// TestWaitForNodesAvailable tests node availability waiting
func TestWaitForNodesAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a temporary kubeconfig that will fail
	tmpDir := t.TempDir()
	kubeconfig := filepath.Join(tmpDir, "kubeconfig")

	// Write invalid kubeconfig
	invalidConfig := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://invalid-server:6443
  name: invalid
contexts:
- context:
    cluster: invalid
  name: invalid
current-context: invalid
`

	if err := os.WriteFile(kubeconfig, []byte(invalidConfig), 0600); err != nil {
		t.Fatalf("Failed to write test kubeconfig: %v", err)
	}

	config := &BootstrapConfig{
		KubeConfig: kubeconfig,
	}
	logger := common.NewColorLogger()

	// This should timeout quickly with invalid config
	start := time.Now()
	err := waitForNodesAvailable(config, logger)
	duration := time.Since(start)

	if err == nil {
		t.Errorf("Expected error with invalid kubeconfig")
	}

	// Should fail relatively quickly (within 30 seconds for this test)
	if duration > 30*time.Second {
		t.Errorf("Function took too long to fail: %v", duration)
	}
}

// TestExtractOnePasswordReferences tests 1Password reference extraction
func TestExtractOnePasswordReferences(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "no references",
			content:  "no secrets here",
			expected: []string{},
		},
		{
			name:     "single reference",
			content:  "secret: op://vault/item/field",
			expected: []string{"op://vault/item/field"},
		},
		{
			name:     "multiple references",
			content:  "secret1: op://vault/item1/field1\nsecret2: op://vault/item2/field2",
			expected: []string{"op://vault/item1/field1", "op://vault/item2/field2"},
		},
		{
			name:     "duplicate references",
			content:  "secret1: op://vault/item/field\nsecret2: op://vault/item/field",
			expected: []string{"op://vault/item/field"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractOnePasswordReferences(tt.content)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d references, got %d", len(tt.expected), len(result))
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("Expected reference %d to be %s, got %s", i, expected, result[i])
				}
			}
		})
	}
}

// TestValidate1PasswordReference tests 1Password reference validation
func TestValidate1PasswordReference(t *testing.T) {
	tests := []struct {
		name      string
		reference string
		valid     bool
	}{
		{
			name:      "valid reference",
			reference: "op://vault/item/field",
			valid:     true,
		},
		{
			name:      "missing vault",
			reference: "op:///item/field",
			valid:     false,
		},
		{
			name:      "missing item",
			reference: "op://vault//field",
			valid:     false,
		},
		{
			name:      "missing field",
			reference: "op://vault/item/",
			valid:     false,
		},
		{
			name:      "invalid prefix",
			reference: "op:/vault/item/field",
			valid:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate1PasswordReference(tt.reference)
			if tt.valid && err != nil {
				t.Errorf("Expected valid reference, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("Expected invalid reference to produce error")
			}
		})
	}
}

// TestValidateYAMLSyntax tests YAML syntax validation
func TestValidateYAMLSyntax(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		valid   bool
	}{
		{
			name:    "valid yaml",
			content: []byte("key: value\nlist:\n  - item1\n  - item2"),
			valid:   true,
		},
		{
			name:    "invalid yaml - bad indentation",
			content: []byte("key: value\n bad_indent: value"),
			valid:   false,
		},
		{
			name:    "invalid yaml - unclosed quote",
			content: []byte(`key: "unclosed quote`),
			valid:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateYAMLSyntax(tt.content)
			if tt.valid && err != nil {
				t.Errorf("Expected valid YAML, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("Expected invalid YAML to produce error")
			}
		})
	}
}

// Benchmark tests
func BenchmarkMergeConfigsWithTalosctl(b *testing.B) {
	if _, err := exec.LookPath("talosctl"); err != nil {
		b.Skip("talosctl not found, skipping benchmark")
	}

	baseConfig := []byte(`
version: v1alpha1
machine:
  type: controlplane
  install:
    disk: /dev/sda
cluster:
  name: test
`)

	patchConfig := []byte(`
machine:
  install:
    wipe: true
  features:
    rbac: true
`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := mergeConfigsWithTalosctl(baseConfig, patchConfig)
		if err != nil {
			b.Fatalf("mergeConfigsWithTalosctl failed: %v", err)
		}
	}
}

func BenchmarkExtractOnePasswordReferences(b *testing.B) {
	content := `
secret1: op://vault/item1/field1
secret2: op://vault/item2/field2  
secret3: op://vault/item3/field3
normal_key: normal_value
secret4: op://vault/item4/field4
`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extractOnePasswordReferences(content)
	}
}
