package talos

import (
	"fmt"
	"os"
	"path/filepath"
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
		"cleanup-zvols",
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
			errMsg:  "VM name cannot contain dashes",
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
			wantErr: true,
			errMsg:  "VM name cannot contain spaces",
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
	tests := []struct {
		name        string
		envVars     map[string]string
		mockSecrets map[string]string
		wantErr     bool
		expectedURL string
		expectedAPI string
	}{
		{
			name: "credentials from environment",
			envVars: map[string]string{
				"TRUENAS_URL":     "https://truenas.local",
				"TRUENAS_API_KEY": "test-api-key",
			},
			wantErr:     false,
			expectedURL: "https://truenas.local",
			expectedAPI: "test-api-key",
		},
		{
			name: "credentials from 1Password",
			envVars: map[string]string{
				"TRUENAS_URL":                 "",
				"TRUENAS_API_KEY":             "",
				"ONEPASSWORD_CONNECT_TOKEN":   "test-token",
				"ONEPASSWORD_CONNECT_HOST":    "http://1password.local",
				"ONEPASSWORD_SERVICE_ACCOUNT": "test-account",
			},
			mockSecrets: map[string]string{
				"op://homelab/truenas/url":     "https://truenas.1p.local",
				"op://homelab/truenas/api_key": "1p-api-key",
			},
			wantErr:     false,
			expectedURL: "https://truenas.1p.local",
			expectedAPI: "1p-api-key",
		},
		{
			name:    "missing credentials",
			envVars: map[string]string{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := testutil.SetEnvs(t, tt.envVars)
			defer cleanup()

			// Mock 1Password client if needed
			if len(tt.mockSecrets) > 0 {
				// This would require dependency injection or interface
				// For now, we skip the 1Password test cases
				t.Skip("1Password mocking requires refactoring")
			}

			url, apiKey, err := getTrueNASCredentials()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedURL, url)
				assert.Equal(t, tt.expectedAPI, apiKey)
			}
		})
	}
}

func TestRenderMachineConfigFromEmbedded(t *testing.T) {
	tmpDir, cleanup := testutil.TempDir(t)
	defer cleanup()

	tests := []struct {
		name         string
		nodeIP       string
		nodeType     string
		envVars      map[string]string
		wantErr      bool
		validateFunc func(t *testing.T, config []byte)
	}{
		{
			name:     "render controlplane config",
			nodeIP:   "192.168.122.10",
			nodeType: "controlplane",
			envVars: map[string]string{
				"CLUSTER_NAME":        "test-cluster",
				"KUBERNETES_VERSION":  "v1.31.0",
				"TALOS_VERSION":       "v1.8.0",
				"CLUSTER_ENDPOINT_IP": "192.168.122.100",
			},
			wantErr: false,
			validateFunc: func(t *testing.T, config []byte) {
				assert.Contains(t, string(config), "192.168.122.10")
				assert.Contains(t, string(config), "controlplane")
			},
		},
		{
			name:     "render worker config",
			nodeIP:   "192.168.122.20",
			nodeType: "worker",
			envVars: map[string]string{
				"CLUSTER_NAME":        "test-cluster",
				"KUBERNETES_VERSION":  "v1.31.0",
				"TALOS_VERSION":       "v1.8.0",
				"CLUSTER_ENDPOINT_IP": "192.168.122.100",
			},
			wantErr: false,
			validateFunc: func(t *testing.T, config []byte) {
				assert.Contains(t, string(config), "192.168.122.20")
				assert.Contains(t, string(config), "worker")
			},
		},
		{
			name:     "missing required env vars",
			nodeIP:   "192.168.122.10",
			nodeType: "controlplane",
			envVars:  map[string]string{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := testutil.SetEnvs(t, tt.envVars)
			defer cleanup()

			// Set minijinja config
			miniJinjaConfig := filepath.Join(tmpDir, ".minijinja.toml")
			err := os.WriteFile(miniJinjaConfig, []byte(`
[config]
templates = "."
`), 0644)
			require.NoError(t, err)

			miniJinjaCleanup := testutil.SetEnv(t, "MINIJINJA_CONFIG_FILE", miniJinjaConfig)
			defer miniJinjaCleanup()

			config, err := renderMachineConfigFromEmbedded(tt.nodeIP, tt.nodeType)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, config)
				if tt.validateFunc != nil {
					tt.validateFunc(t, config)
				}
			}
		})
	}
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
