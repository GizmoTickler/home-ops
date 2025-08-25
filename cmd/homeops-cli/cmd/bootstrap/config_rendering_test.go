package bootstrap

import (
	"homeops-cli/internal/common"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderMachineConfigFromEmbedded tests machine config rendering
func TestRenderMachineConfigFromEmbedded(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip if talosctl is not available
	if _, err := exec.LookPath("talosctl"); err != nil {
		t.Skip("talosctl not found, skipping test")
	}

	tests := []struct {
		name          string
		baseTemplate  string
		patchTemplate string
		machineType   string
		expectError   bool
	}{
		{
			name:          "valid templates",
			baseTemplate:  "controlplane.yaml",
			patchTemplate: "nodes/192.168.122.10.yaml",
			machineType:   "controlplane",
			expectError:   false,
		},
		{
			name:          "non-existent base template",
			baseTemplate:  "nonexistent.yaml",
			patchTemplate: "nodes/192.168.122.10.yaml",
			machineType:   "controlplane",
			expectError:   true,
		},
		{
			name:          "non-existent patch template",
			baseTemplate:  "controlplane.yaml",
			patchTemplate: "nodes/nonexistent.yaml",
			machineType:   "controlplane",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := renderMachineConfigFromEmbedded(tt.baseTemplate, tt.patchTemplate, tt.machineType)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(result) == 0 {
				t.Errorf("Expected non-empty result")
			}

			// Basic validation that result looks like YAML
			resultStr := string(result)
			if !strings.Contains(resultStr, "version:") && !strings.Contains(resultStr, "apiVersion:") {
				t.Errorf("Result doesn't look like valid Talos/Kubernetes YAML")
			}
		})
	}
}

// TestMergeConfigsWithTalosctlErrorHandling tests error handling in config merging
func TestMergeConfigsWithTalosctlErrorHandling(t *testing.T) {
	if _, err := exec.LookPath("talosctl"); err != nil {
		t.Skip("talosctl not found, skipping test")
	}

	tests := []struct {
		name        string
		baseConfig  []byte
		patchConfig []byte
		expectError bool
	}{
		{
			name:        "invalid base YAML",
			baseConfig:  []byte("invalid: yaml: content: ["),
			patchConfig: []byte("machine:\n  install:\n    wipe: true"),
			expectError: true,
		},
		{
			name:        "invalid patch YAML",
			baseConfig:  []byte("version: v1alpha1\nmachine:\n  type: controlplane"),
			patchConfig: []byte("invalid: yaml: content: ["),
			expectError: true,
		},
		{
			name:        "empty configs",
			baseConfig:  []byte(""),
			patchConfig: []byte(""),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mergeConfigsWithTalosctl(tt.baseConfig, tt.patchConfig)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// TestTempFileCleanup tests that temporary files are properly cleaned up
func TestTempFileCleanup(t *testing.T) {
	if _, err := exec.LookPath("talosctl"); err != nil {
		t.Skip("talosctl not found, skipping test")
	}

	baseConfig := []byte("version: v1alpha1\nmachine:\n  type: controlplane")
	patchConfig := []byte("machine:\n  install:\n    wipe: true")

	// Get count of temp files before
	tmpDir := os.TempDir()
	beforeFiles, err := filepath.Glob(filepath.Join(tmpDir, "talos-*-*.yaml"))
	if err != nil {
		t.Fatalf("Failed to glob temp files: %v", err)
	}
	beforeCount := len(beforeFiles)

	// Run merge operation
	_, err = mergeConfigsWithTalosctl(baseConfig, patchConfig)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	// Check that temp files were cleaned up
	afterFiles, err := filepath.Glob(filepath.Join(tmpDir, "talos-*-*.yaml"))
	if err != nil {
		t.Fatalf("Failed to glob temp files: %v", err)
	}
	afterCount := len(afterFiles)

	if afterCount > beforeCount {
		t.Errorf("Temp files not cleaned up: before=%d, after=%d", beforeCount, afterCount)
		// List the remaining files for debugging
		for _, file := range afterFiles {
			t.Logf("Remaining temp file: %s", file)
		}
	}
}

// TestGetMachineTypeFromEmbedded tests machine type detection
func TestGetMachineTypeFromEmbedded(t *testing.T) {
	tests := []struct {
		name         string
		nodeTemplate string
		expectedType string
		expectError  bool
	}{
		{
			name:         "controlplane node",
			nodeTemplate: "nodes/192.168.122.10.yaml",
			expectedType: "controlplane",
			expectError:  false,
		},
		{
			name:         "non-existent template",
			nodeTemplate: "nodes/nonexistent.yaml",
			expectedType: "",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machineType, err := getMachineTypeFromEmbedded(tt.nodeTemplate)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if machineType != tt.expectedType {
				t.Errorf("Expected machine type %s, got %s", tt.expectedType, machineType)
			}
		})
	}
}

// TestApplyNodeConfigWithRetry tests node config application with retry logic
func TestApplyNodeConfigWithRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := common.NewColorLogger()
	config := []byte("test config")

	// This will fail because we don't have a real Talos node, but test the retry logic
	err := applyNodeConfigWithRetry("192.168.1.1", config, logger, 2)
	if err == nil {
		t.Errorf("Expected error when applying config to non-existent node")
	}

	// Should contain retry information
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("Expected error to mention retry attempts")
	}
}

// TestApplyNodeConfig tests individual node config application
func TestApplyNodeConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := []byte("test config")

	// This will fail because we don't have a real Talos node
	err := applyNodeConfig("192.168.1.1", config)
	if err == nil {
		t.Errorf("Expected error when applying config to non-existent node")
	}
}

// Integration test for the full rendering pipeline
func TestFullRenderingPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("talosctl"); err != nil {
		t.Skip("talosctl not found, skipping test")
	}

	// Test the complete flow: embed template -> merge -> resolve secrets
	result, err := renderMachineConfigFromEmbedded("controlplane.yaml", "nodes/192.168.122.10.yaml", "controlplane")
	if err != nil {
		t.Fatalf("Full rendering pipeline failed: %v", err)
	}

	resultStr := string(result)

	// Check that basic structure is present
	if !strings.Contains(resultStr, "version:") && !strings.Contains(resultStr, "apiVersion:") {
		t.Errorf("Result missing version information")
	}

	// Check that merge worked by looking for elements from both base and patch
	if strings.Contains(resultStr, "op://") {
		t.Logf("Result contains unresolved 1Password references (expected in test)")
	}

	// Validate YAML syntax
	if err := validateYAMLSyntax(result); err != nil {
		t.Errorf("Result is not valid YAML: %v", err)
	}
}

// Benchmark machine config rendering
func BenchmarkRenderMachineConfigFromEmbedded(b *testing.B) {
	if _, err := exec.LookPath("talosctl"); err != nil {
		b.Skip("talosctl not found, skipping benchmark")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := renderMachineConfigFromEmbedded("controlplane.yaml", "nodes/192.168.122.10.yaml", "controlplane")
		if err != nil {
			b.Fatalf("Rendering failed: %v", err)
		}
	}
}

// Test parallel rendering to ensure thread safety
func TestParallelRendering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("talosctl"); err != nil {
		t.Skip("talosctl not found, skipping test")
	}

	const numGoroutines = 5
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			_, err := renderMachineConfigFromEmbedded("controlplane.yaml", "nodes/192.168.122.10.yaml", "controlplane")
			errors <- err
		}()
	}

	for i := 0; i < numGoroutines; i++ {
		if err := <-errors; err != nil {
			t.Errorf("Parallel rendering failed: %v", err)
		}
	}
}
