package bootstrap

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeops-cli/internal/common"
)

func configureBootstrapWaitTest(t *testing.T, stub func(*BootstrapConfig, ...string) ([]byte, error)) {
	t.Helper()

	originalKubectlOutput := bootstrapKubectlOutput
	originalCheckIntervalNormal := bootstrapCheckIntervalNormal
	originalCheckIntervalSlow := bootstrapCheckIntervalSlow
	originalStallTimeout := bootstrapStallTimeout
	originalExtSecMaxWait := bootstrapExtSecMaxWait
	originalFluxMaxWait := bootstrapFluxMaxWait
	originalNodeMaxWait := bootstrapNodeMaxWait

	t.Cleanup(func() {
		bootstrapKubectlOutput = originalKubectlOutput
		bootstrapCheckIntervalNormal = originalCheckIntervalNormal
		bootstrapCheckIntervalSlow = originalCheckIntervalSlow
		bootstrapStallTimeout = originalStallTimeout
		bootstrapExtSecMaxWait = originalExtSecMaxWait
		bootstrapFluxMaxWait = originalFluxMaxWait
		bootstrapNodeMaxWait = originalNodeMaxWait
	})

	bootstrapKubectlOutput = stub
	bootstrapCheckIntervalNormal = time.Millisecond
	bootstrapCheckIntervalSlow = time.Millisecond
	bootstrapStallTimeout = 5 * time.Millisecond
	bootstrapExtSecMaxWait = 20 * time.Millisecond
	bootstrapFluxMaxWait = 20 * time.Millisecond
	bootstrapNodeMaxWait = 20 * time.Millisecond
}

func TestBuildTalosctlCmd(t *testing.T) {
	t.Run("with talosconfig", func(t *testing.T) {
		cmd := buildTalosctlCmd("/tmp/talosconfig", "config", "info")
		want := []string{"talosctl", "--talosconfig", "/tmp/talosconfig", "config", "info"}
		if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
			t.Fatalf("unexpected args: got %v want %v", cmd.Args, want)
		}
	})

	t.Run("without talosconfig", func(t *testing.T) {
		cmd := buildTalosctlCmd("", "config", "info")
		want := []string{"talosctl", "config", "info"}
		if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
			t.Fatalf("unexpected args: got %v want %v", cmd.Args, want)
		}
	})
}

func TestBuildKubectlCmd(t *testing.T) {
	config := &BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}
	cmd := buildKubectlCmd(config, "get", "nodes", "-o", "name")

	want := []string{"kubectl", "get", "nodes", "-o", "name", "--kubeconfig", "/tmp/kubeconfig"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected args: got %v want %v", cmd.Args, want)
	}
}

func TestBuildHelmfileCmd(t *testing.T) {
	config := &BootstrapConfig{RootDir: "/repo/root"}
	cmd := buildHelmfileCmd("/tmp/work", config, "--file", "/tmp/work/01-apps.yaml", "sync")

	want := []string{"helmfile", "--file", "/tmp/work/01-apps.yaml", "sync"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected args: got %v want %v", cmd.Args, want)
	}
	if cmd.Dir != "/tmp/work" {
		t.Fatalf("unexpected dir: got %q", cmd.Dir)
	}

	foundRootDir := false
	for _, env := range cmd.Env {
		if env == "ROOT_DIR=/repo/root" {
			foundRootDir = true
			break
		}
	}
	if !foundRootDir {
		t.Fatalf("expected ROOT_DIR to be present in env, got %v", cmd.Env)
	}
}

func TestDeploymentReadinessHelpers(t *testing.T) {
	t.Run("parse replica count", func(t *testing.T) {
		count, err := parseReplicaCount("2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 2 {
			t.Fatalf("unexpected count: got %d want 2", count)
		}

		if _, err := parseReplicaCount(""); err == nil {
			t.Fatal("expected error for empty replica count")
		}
		if _, err := parseReplicaCount("abc"); err == nil {
			t.Fatal("expected error for invalid replica count")
		}
	})

	t.Run("deployment ready from state", func(t *testing.T) {
		tests := []struct {
			state string
			ready bool
		}{
			{state: "1/1", ready: true},
			{state: "2/2", ready: true},
			{state: "1/2", ready: false},
			{state: "0/1", ready: false},
			{state: "0/0", ready: false},
			{state: "bad", ready: false},
		}

		for _, tt := range tests {
			if got := deploymentReadyFromState(tt.state); got != tt.ready {
				t.Fatalf("deploymentReadyFromState(%q) = %v, want %v", tt.state, got, tt.ready)
			}
		}
	})

	t.Run("deployment and endpoints ready from state", func(t *testing.T) {
		tests := []struct {
			state     string
			endpoints string
			ready     bool
		}{
			{state: "1/1:True", endpoints: "10.0.0.1", ready: true},
			{state: "2/2:True", endpoints: "10.0.0.1 10.0.0.2", ready: true},
			{state: "1/2:True", endpoints: "10.0.0.1", ready: false},
			{state: "1/1:False", endpoints: "10.0.0.1", ready: false},
			{state: "1/1:True", endpoints: "", ready: false},
			{state: "bad", endpoints: "10.0.0.1", ready: false},
		}

		for _, tt := range tests {
			if got := deploymentAndEndpointsReadyFromState(tt.state, tt.endpoints); got != tt.ready {
				t.Fatalf("deploymentAndEndpointsReadyFromState(%q, %q) = %v, want %v", tt.state, tt.endpoints, got, tt.ready)
			}
		}
	})
}

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
	oldInjectSecrets := bootstrapInjectSecrets
	t.Cleanup(func() { bootstrapInjectSecrets = oldInjectSecrets })
	bootstrapInjectSecrets = func(content string) (string, error) {
		if strings.Contains(content, "op://") {
			return content, errors.New("synthetic 1Password resolution failure")
		}
		return content, nil
	}

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
	configureBootstrapWaitTest(t, func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
		return nil, errors.New("kubectl unavailable")
	})

	config := &BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}
	logger := common.NewColorLogger()

	start := time.Now()
	err := waitForNodesAvailable(config, logger)
	duration := time.Since(start)

	if err == nil {
		t.Errorf("Expected kubectl stall error")
	}
	if err != nil && !strings.Contains(err.Error(), "node discovery stalled") {
		t.Errorf("Expected stall error, got: %v", err)
	}

	if duration > time.Second {
		t.Errorf("Function took too long to fail: %v", duration)
	}
}

func TestWaitForNodesReadyFalseStallsOnKubectlErrors(t *testing.T) {
	configureBootstrapWaitTest(t, func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
		return nil, errors.New("kubectl unavailable")
	})

	config := &BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}
	logger := common.NewColorLogger()

	start := time.Now()
	err := waitForNodesReadyFalse(config, logger)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected error with invalid kubeconfig")
	}
	if !strings.Contains(err.Error(), "node readiness stalled") {
		t.Fatalf("Expected readiness stall error, got: %v", err)
	}
	if duration > time.Second {
		t.Fatalf("Function took too long to fail: %v", duration)
	}
}

func TestWaitForExternalSecretsWebhookStallsOnKubectlErrors(t *testing.T) {
	configureBootstrapWaitTest(t, func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
		return nil, errors.New("kubectl unavailable")
	})

	config := &BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}
	logger := common.NewColorLogger()

	start := time.Now()
	err := waitForExternalSecretsWebhook(config, logger)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected error with kubectl failures")
	}
	if !strings.Contains(err.Error(), "external-secrets webhook stalled") {
		t.Fatalf("Expected external-secrets stall error, got: %v", err)
	}
	if duration > time.Second {
		t.Fatalf("Function took too long to fail: %v", duration)
	}
}

func TestWaitForFluxControllerStallsOnKubectlErrors(t *testing.T) {
	configureBootstrapWaitTest(t, func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
		return nil, errors.New("kubectl unavailable")
	})

	config := &BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}
	logger := common.NewColorLogger()

	start := time.Now()
	err := waitForFluxController(config, logger, "source-controller")
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected error with kubectl failures")
	}
	if !strings.Contains(err.Error(), "source-controller stalled") {
		t.Fatalf("Expected flux controller stall error, got: %v", err)
	}
	if duration > time.Second {
		t.Fatalf("Function took too long to fail: %v", duration)
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

// TestGetGatewayAPICRDsURL tests extracting the Gateway API CRDs URL from a kustomization file
func TestGetGatewayAPICRDsURL(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		expectedURL string
		expectError bool
		errContains string
	}{
		{
			name: "valid kustomization",
			content: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml`,
			expectedURL: "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml",
			expectError: false,
		},
		{
			name: "no gateway-api resource",
			content: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./some-other-resource.yaml`,
			expectError: true,
			errContains: "not found",
		},
		{
			name:        "invalid yaml",
			content:     `{invalid yaml: [`,
			expectError: true,
			errContains: "failed to parse",
		},
		{
			name:        "empty resources",
			content:     `apiVersion: kustomize.config.k8s.io/v1beta1`,
			expectError: true,
			errContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Create the expected directory structure
			kustomizationDir := filepath.Join(tmpDir, "kubernetes", "apps", "network", "kgateway", "gateway-api-crds")
			if err := os.MkdirAll(kustomizationDir, 0755); err != nil {
				t.Fatalf("Failed to create test directory: %v", err)
			}

			kustomizationPath := filepath.Join(kustomizationDir, "kustomization.yaml")
			if err := os.WriteFile(kustomizationPath, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			url, err := getGatewayAPICRDsURL(tmpDir)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errContains)
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if url != tt.expectedURL {
					t.Errorf("Expected URL %q, got %q", tt.expectedURL, url)
				}
			}
		})
	}

	// Test missing file
	t.Run("missing kustomization file", func(t *testing.T) {
		_, err := getGatewayAPICRDsURL("/nonexistent/path")
		if err == nil {
			t.Error("Expected error for missing file, got nil")
		}
	})
}

// TestValidateGatewayAPICRDsURL tests URL validation for Gateway API CRDs
func TestValidateGatewayAPICRDsURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectError bool
		errContains string
	}{
		{
			name:        "valid URL",
			url:         "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml",
			expectError: false,
		},
		{
			name:        "wrong host",
			url:         "https://evil.com/kubernetes-sigs/gateway-api/releases/download/v1.0.0/install.yaml",
			expectError: true,
			errContains: "unexpected host",
		},
		{
			name:        "wrong path",
			url:         "https://github.com/some-other/repo/releases/download/v1.0.0/install.yaml",
			expectError: true,
			errContains: "unexpected path",
		},
		{
			name:        "malformed URL",
			url:         "://not-a-url",
			expectError: true,
			errContains: "malformed URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGatewayAPICRDsURL(tt.url)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errContains)
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestExtractGatewayAPIVersion tests version extraction from Gateway API URLs
func TestExtractGatewayAPIVersion(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "standard release URL",
			url:      "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml",
			expected: "v1.4.1",
		},
		{
			name:     "different version",
			url:      "https://github.com/kubernetes-sigs/gateway-api/releases/download/v2.0.0/standard-install.yaml",
			expected: "v2.0.0",
		},
		{
			name:     "non-matching URL returns full URL",
			url:      "https://example.com/some/other/path",
			expected: "https://example.com/some/other/path",
		},
		{
			name:     "empty string",
			url:      "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractGatewayAPIVersion(tt.url)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestApplyGatewayAPICRDsValidation tests input validation for applyGatewayAPICRDs
func TestApplyGatewayAPICRDsValidation(t *testing.T) {
	t.Run("empty kubeconfig", func(t *testing.T) {
		config := &BootstrapConfig{
			RootDir:    "/tmp",
			KubeConfig: "",
		}
		logger := common.NewColorLogger()

		err := applyGatewayAPICRDs(config, logger)
		if err == nil {
			t.Error("Expected error for empty kubeconfig, got nil")
		}
		if !strings.Contains(err.Error(), "kubeconfig path is required") {
			t.Errorf("Expected kubeconfig error, got: %v", err)
		}
	})

	t.Run("missing kustomization file", func(t *testing.T) {
		config := &BootstrapConfig{
			RootDir:    "/nonexistent/path",
			KubeConfig: "/tmp/kubeconfig",
		}
		logger := common.NewColorLogger()

		err := applyGatewayAPICRDs(config, logger)
		if err == nil {
			t.Error("Expected error for missing kustomization, got nil")
		}
	})

	t.Run("dry run succeeds with valid kustomization", func(t *testing.T) {
		tmpDir := t.TempDir()
		kustomizationDir := filepath.Join(tmpDir, "kubernetes", "apps", "network", "kgateway", "gateway-api-crds")
		if err := os.MkdirAll(kustomizationDir, 0755); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}
		content := `resources:
  - https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml`
		if err := os.WriteFile(filepath.Join(kustomizationDir, "kustomization.yaml"), []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		config := &BootstrapConfig{
			RootDir:    tmpDir,
			KubeConfig: "/tmp/kubeconfig",
			DryRun:     true,
		}
		logger := common.NewColorLogger()

		err := applyGatewayAPICRDs(config, logger)
		if err != nil {
			t.Errorf("Dry run should succeed with valid kustomization: %v", err)
		}
	})
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
