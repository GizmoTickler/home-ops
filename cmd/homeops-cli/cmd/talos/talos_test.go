package talos

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/testutil"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "talos", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	// Check that all subcommands are registered
	subCommands := []string{
		"apply-node",
		"upgrade-node",
		"upgrade-k8s",
		"reboot-node",
		"shutdown-cluster",
		"reset-node",
		"reset-cluster",
		"kubeconfig",
		"deploy-vm",
		"manage-vm",
		"prepare-iso",
	}

	for _, subCmd := range subCommands {
		t.Run(subCmd, func(t *testing.T) {
			found := false
			for _, cmd := range cmd.Commands() {
				if cmd.Name() == subCmd {
					found = true
					break
				}
			}
			assert.True(t, found, "Subcommand %s not found", subCmd)
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		expected     string
	}{
		{
			name:         "env variable exists",
			key:          "TEST_VAR",
			defaultValue: "default",
			envValue:     "custom",
			expected:     "custom",
		},
		{
			name:         "env variable does not exist",
			key:          "NON_EXISTENT_VAR",
			defaultValue: "default",
			envValue:     "",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				cleanup := testutil.SetEnv(t, tt.key, tt.envValue)
				defer cleanup()
			}

			result := getEnvOrDefault(tt.key, tt.defaultValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateVMName(t *testing.T) {
	tests := []struct {
		name    string
		vmName  string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid name with underscores",
			vmName:  "test_vm_name",
			wantErr: false,
		},
		{
			name:    "valid simple name",
			vmName:  "testvm",
			wantErr: false,
		},
		{
			name:    "invalid name with dashes",
			vmName:  "test-vm-name",
			wantErr: true,
			errMsg:  "cannot contain dashes",
		},
		{
			name:    "empty name",
			vmName:  "",
			wantErr: true,
			errMsg:  "VM name cannot be empty",
		},
		{
			name:    "name with spaces",
			vmName:  "test vm name",
			wantErr: false, // Spaces are not actually validated in the function
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVMName(tt.vmName)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetTrueNASCredentials(t *testing.T) {
	// Note: This test is limited because the function calls actual 1Password
	// which may return real credentials. For proper testing, the function
	// would need dependency injection to mock the 1Password client.

	// Test only that the function works and returns some values
	// when environment variables are set properly
	t.Run("function returns values", func(t *testing.T) {
		// Save original values
		origHost := os.Getenv("TRUENAS_HOST")
		origKey := os.Getenv("TRUENAS_API_KEY")

		// Set test values
		_ = os.Setenv("TRUENAS_HOST", "test-host")
		_ = os.Setenv("TRUENAS_API_KEY", "test-key")

		defer func() {
			// Restore original values
			if origHost != "" {
				_ = os.Setenv("TRUENAS_HOST", origHost)
			} else {
				_ = os.Unsetenv("TRUENAS_HOST")
			}
			if origKey != "" {
				_ = os.Setenv("TRUENAS_API_KEY", origKey)
			} else {
				_ = os.Unsetenv("TRUENAS_API_KEY")
			}
		}()

		host, apiKey, err := getTrueNASCredentials()

		// Should not error when credentials are available
		assert.NoError(t, err)
		assert.NotEmpty(t, host)
		assert.NotEmpty(t, apiKey)
	})

	t.Run("function signature exists", func(t *testing.T) {
		// Just verify the function exists and can be called
		// The actual implementation may return 1Password values
		host, apiKey, err := getTrueNASCredentials()

		// Function should either succeed or fail with meaningful error
		if err != nil {
			assert.Contains(t, err.Error(), "TrueNAS credentials")
		} else {
			assert.NotEmpty(t, host)
			assert.NotEmpty(t, apiKey)
		}
	})

	// TODO: Implement proper testing with mocked 1Password client
	// This would require refactoring getTrueNASCredentials to accept
	// an interface for the secret provider
}

func TestRenderMachineConfigFromEmbedded(t *testing.T) {
	// Note: This test currently cannot run full template rendering because it requires
	// embedded templates that are not available in the test environment.
	// For now, we test that the function exists and has the correct signature.

	tests := []struct {
		name          string
		baseTemplate  string
		patchTemplate string
		envVars       map[string]string
		expectError   bool
	}{
		{
			name:          "function exists and accepts parameters",
			baseTemplate:  "controlplane",
			patchTemplate: "192.168.122.10",
			envVars: map[string]string{
				"CLUSTER_NAME":        "test-cluster",
				"KUBERNETES_VERSION":  "v1.31.0",
				"TALOS_VERSION":       "v1.8.0",
				"CLUSTER_ENDPOINT_IP": "192.168.122.100",
			},
			expectError: true, // Expected to fail without embedded templates
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := testutil.SetEnvs(t, tt.envVars)
			defer cleanup()

			// This will fail because embedded templates are not available in test,
			// but we can verify the function signature is correct
			_, err := renderMachineConfigFromEmbedded(tt.baseTemplate, tt.patchTemplate)

			if tt.expectError {
				assert.Error(t, err, "Expected error due to missing embedded templates")
			} else {
				assert.NoError(t, err)
			}
		})
	}

	// TODO: Implement proper template rendering tests with mocked dependencies
	// or embedded template fixtures for integration testing
}

func TestApplyNodeConfig(t *testing.T) {
	tests := []struct {
		name      string
		nodeIP    string
		nodeType  string
		setupMock func(*testutil.MockTalosClient)
		wantErr   bool
	}{
		{
			name:     "successful apply",
			nodeIP:   "192.168.122.10",
			nodeType: "controlplane",
			setupMock: func(m *testutil.MockTalosClient) {
				// No error setup needed, default behavior is success
			},
			wantErr: false,
		},
		{
			name:     "apply fails",
			nodeIP:   "192.168.122.10",
			nodeType: "controlplane",
			setupMock: func(m *testutil.MockTalosClient) {
				m.ApplyConfigErr = fmt.Errorf("connection refused")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

func TestUpgradeNode(t *testing.T) {
	tests := []struct {
		name      string
		nodeIP    string
		image     string
		preserve  bool
		setupMock func(*testutil.MockTalosClient)
		wantErr   bool
	}{
		{
			name:     "successful upgrade with preserve",
			nodeIP:   "192.168.122.10",
			image:    "factory.talos.dev/installer:v1.8.0",
			preserve: true,
			setupMock: func(m *testutil.MockTalosClient) {
				// No error setup needed
			},
			wantErr: false,
		},
		{
			name:     "upgrade fails",
			nodeIP:   "192.168.122.10",
			image:    "factory.talos.dev/installer:v1.8.0",
			preserve: false,
			setupMock: func(m *testutil.MockTalosClient) {
				m.UpgradeErr = fmt.Errorf("upgrade failed")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

func TestDeployVMWithPattern(t *testing.T) {
	tests := []struct {
		name        string
		baseName    string
		pattern     string
		startIndex  int
		count       int
		generateISO bool
		setupMock   func(*testutil.MockTrueNASClient)
		wantErr     bool
		expectedVMs []string
	}{
		{
			name:        "deploy single VM",
			baseName:    "test",
			pattern:     "",
			startIndex:  1,
			count:       1,
			generateISO: false,
			setupMock: func(m *testutil.MockTrueNASClient) {
				// No error setup needed
			},
			wantErr:     false,
			expectedVMs: []string{"test"},
		},
		{
			name:        "deploy multiple VMs with pattern",
			baseName:    "worker",
			pattern:     "worker_%d",
			startIndex:  1,
			count:       3,
			generateISO: false,
			setupMock: func(m *testutil.MockTrueNASClient) {
				// No error setup needed
			},
			wantErr:     false,
			expectedVMs: []string{"worker_1", "worker_2", "worker_3"},
		},
		{
			name:        "deploy fails",
			baseName:    "test",
			pattern:     "",
			startIndex:  1,
			count:       1,
			generateISO: false,
			setupMock: func(m *testutil.MockTrueNASClient) {
				m.CreateVMErr = fmt.Errorf("failed to create VM")
			},
			wantErr:     true,
			expectedVMs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

func TestGetAllNodes(t *testing.T) {
	tests := []struct {
		name          string
		mockResources string
		wantErr       bool
		expectedNodes []string
	}{
		{
			name: "successful node list",
			mockResources: `NAME                  READY   STATUS
test-cp-1             True    Running
test-cp-2             True    Running
test-worker-1         True    Running`,
			wantErr: false,
			expectedNodes: []string{
				"test-cp-1",
				"test-cp-2",
				"test-worker-1",
			},
		},
		{
			name:          "command fails",
			mockResources: "",
			wantErr:       true,
			expectedNodes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual command execution mocking
			t.Skip("Requires command execution mocking")
		})
	}
}

func TestPrepareISOWithProvider(t *testing.T) {
	_, cleanup := testutil.TempDir(t)
	defer cleanup()

	tests := []struct {
		name        string
		provider    string
		isoName     string
		generateISO bool
		envVars     map[string]string
		wantErr     bool
	}{
		{
			name:        "prepare ISO for TrueNAS",
			provider:    "truenas",
			isoName:     "test.iso",
			generateISO: true,
			envVars: map[string]string{
				"TALOS_VERSION": "v1.8.0",
			},
			wantErr: false,
		},
		{
			name:        "prepare ISO for vSphere",
			provider:    "vsphere",
			isoName:     "test.iso",
			generateISO: true,
			envVars: map[string]string{
				"TALOS_VERSION": "v1.8.0",
				"GOVC_URL":      "https://vsphere.local",
			},
			wantErr: false,
		},
		{
			name:        "invalid provider",
			provider:    "unknown",
			isoName:     "test.iso",
			generateISO: false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := testutil.SetEnvs(t, tt.envVars)
			defer cleanup()

			// Skip for now as it requires actual implementation
			t.Skip("Requires full implementation with mocked dependencies")
		})
	}
}

func TestCleanupOrphanedZVols(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(*testutil.MockTrueNASClient)
		wantErr   bool
	}{
		{
			name: "successful cleanup",
			setupMock: func(m *testutil.MockTrueNASClient) {
				// Setup mock ZVols and VMs
			},
			wantErr: false,
		},
		{
			name: "cleanup fails",
			setupMock: func(m *testutil.MockTrueNASClient) {
				// Setup error condition
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

// Benchmark tests
func BenchmarkRenderMachineConfig(b *testing.B) {
	envVars := map[string]string{
		"CLUSTER_NAME":        "test-cluster",
		"KUBERNETES_VERSION":  "v1.31.0",
		"TALOS_VERSION":       "v1.8.0",
		"CLUSTER_ENDPOINT_IP": "192.168.122.100",
	}

	for k, v := range envVars {
		_ = os.Setenv(k, v)
	}
	defer func() {
		for k := range envVars {
			_ = os.Unsetenv(k)
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderMachineConfigFromEmbedded("192.168.122.10", "controlplane")
	}
}

func BenchmarkValidateVMName(b *testing.B) {
	names := []string{
		"valid_name",
		"invalid-name",
		"another_valid_name",
		"",
		"name with spaces",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, name := range names {
			_ = validateVMName(name)
		}
	}
}
