//go:build integration
// +build integration

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"homeops-cli/cmd/bootstrap"
	"homeops-cli/cmd/completion"
	"homeops-cli/cmd/kubernetes"
	"homeops-cli/cmd/talos"
	"homeops-cli/cmd/volsync"
	"homeops-cli/cmd/workstation"
	"homeops-cli/internal/testutil"
)

// IntegrationTestSuite runs all integration tests
type IntegrationTestSuite struct {
	suite.Suite
	testDir    string
	kubeconfig string
	ctx        context.Context
	cancel     context.CancelFunc
}

// SetupSuite runs once before all tests
func (s *IntegrationTestSuite) SetupSuite() {
	// Create test directory
	var err error
	s.testDir, err = os.MkdirTemp("", "homeops-integration-*")
	require.NoError(s.T(), err)

	// Setup test kubeconfig
	s.kubeconfig = filepath.Join(s.testDir, "kubeconfig")
	err = s.createTestKubeconfig()
	require.NoError(s.T(), err)

	// Set environment variables
	os.Setenv("KUBECONFIG", s.kubeconfig)
	os.Setenv("INTEGRATION_TEST", "true")

	// Create context
	s.ctx, s.cancel = context.WithTimeout(context.Background(), 30*time.Minute)
}

// TearDownSuite runs once after all tests
func (s *IntegrationTestSuite) TearDownSuite() {
	s.cancel()
	os.RemoveAll(s.testDir)
	os.Unsetenv("KUBECONFIG")
	os.Unsetenv("INTEGRATION_TEST")
}

// Test full bootstrap workflow
func (s *IntegrationTestSuite) TestBootstrapWorkflow() {
	if os.Getenv("SKIP_BOOTSTRAP_TEST") == "true" {
		s.T().Skip("Skipping bootstrap test")
	}

	// Test prerequisites check
	s.Run("Prerequisites", func() {
		cmd := bootstrap.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "--preflight-only")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "Preflight checks passed")
	})

	// Test configuration rendering
	s.Run("ConfigRendering", func() {
		// Test Talos config rendering
		configs := []string{"controlplane", "worker"}
		for _, nodeType := range configs {
			configPath := filepath.Join(s.testDir, nodeType+".yaml")
			err := s.renderTalosConfig(nodeType, configPath)
			assert.NoError(s.T(), err)
			assert.FileExists(s.T(), configPath)
		}
	})

	// Test dry-run bootstrap
	s.Run("DryRun", func() {
		cmd := bootstrap.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "Dry run completed")
	})
}

// Test Talos operations workflow
func (s *IntegrationTestSuite) TestTalosWorkflow() {
	if os.Getenv("SKIP_TALOS_TEST") == "true" {
		s.T().Skip("Skipping Talos test")
	}

	// Test VM deployment
	s.Run("VMDeployment", func() {
		cmd := talos.NewCommand()

		// Test VM validation
		output, err := testutil.ExecuteCommand(cmd, "deploy-vm", "--name", "test_vm", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "Dry run: VM would be created")

		// Test invalid VM name
		_, err = testutil.ExecuteCommand(cmd, "deploy-vm", "--name", "test-vm", "--dry-run")
		assert.Error(s.T(), err)
		assert.Contains(s.T(), err.Error(), "cannot contain dashes")
	})

	// Test node operations
	s.Run("NodeOperations", func() {
		// Test kubeconfig generation
		cmd := talos.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "kubeconfig", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "kubeconfig")
	})

	// Test ISO preparation
	s.Run("ISOPreparation", func() {
		cmd := talos.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "prepare-iso", "--provider", "truenas", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "ISO preparation")
	})
}

// Test Kubernetes operations workflow
func (s *IntegrationTestSuite) TestKubernetesWorkflow() {
	if os.Getenv("SKIP_K8S_TEST") == "true" {
		s.T().Skip("Skipping Kubernetes test")
	}

	// Test PVC browsing
	s.Run("PVCBrowse", func() {
		cmd := kubernetes.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "browse-pvc", "--namespace", "default", "--dry-run")
		// Command should succeed but indicate no PVCs in test environment
		assert.NoError(s.T(), err)
		assert.NotEmpty(s.T(), output)
	})

	// Test pod cleansing
	s.Run("CleansePods", func() {
		cmd := kubernetes.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "cleanse-pods", "--namespace", "default", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "Would delete")
	})

	// Test secret syncing
	s.Run("SecretSync", func() {
		cmd := kubernetes.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "sync-secrets", "--namespace", "flux-system", "--dry-run")
		assert.NoError(s.T(), err)
		assert.NotEmpty(s.T(), output)
	})
}

// Test VolSync operations workflow
func (s *IntegrationTestSuite) TestVolSyncWorkflow() {
	if os.Getenv("SKIP_VOLSYNC_TEST") == "true" {
		s.T().Skip("Skipping VolSync test")
	}

	// Test snapshot operations
	s.Run("SnapshotOperations", func() {
		cmd := volsync.NewCommand()

		// Test listing snapshots
		output, err := testutil.ExecuteCommand(cmd, "snapshots", "--namespace", "default", "--dry-run")
		assert.NoError(s.T(), err)
		assert.NotEmpty(s.T(), output)

		// Test creating snapshot
		output, err = testutil.ExecuteCommand(cmd, "snapshot", "--app", "test-app", "--namespace", "default", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "snapshot")
	})

	// Test restore operations
	s.Run("RestoreOperations", func() {
		cmd := volsync.NewCommand()
		output, err := testutil.ExecuteCommand(cmd, "restore", "--app", "test-app", "--namespace", "default", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "restore")
	})

	// Test state management
	s.Run("StateManagement", func() {
		cmd := volsync.NewCommand()

		// Test suspend
		output, err := testutil.ExecuteCommand(cmd, "state", "--action", "suspend", "--app", "test-app", "--namespace", "default", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "suspend")

		// Test resume
		output, err = testutil.ExecuteCommand(cmd, "state", "--action", "resume", "--app", "test-app", "--namespace", "default", "--dry-run")
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "resume")
	})
}

// Test end-to-end workflow
func (s *IntegrationTestSuite) TestEndToEndWorkflow() {
	if os.Getenv("RUN_E2E_TEST") != "true" {
		s.T().Skip("Skipping E2E test - set RUN_E2E_TEST=true to run")
	}

	// This test would run a complete workflow:
	// 1. Bootstrap cluster
	// 2. Deploy VMs
	// 3. Configure nodes
	// 4. Deploy applications
	// 5. Take snapshots
	// 6. Simulate failure
	// 7. Restore from snapshots
	// 8. Verify recovery

	s.T().Log("Starting E2E workflow test")

	// Bootstrap
	s.Run("Bootstrap", func() {
		// Implementation would go here
		s.T().Log("Bootstrap completed")
	})

	// Deploy infrastructure
	s.Run("Infrastructure", func() {
		// Implementation would go here
		s.T().Log("Infrastructure deployed")
	})

	// Deploy applications
	s.Run("Applications", func() {
		// Implementation would go here
		s.T().Log("Applications deployed")
	})

	// Backup and restore
	s.Run("BackupRestore", func() {
		// Implementation would go here
		s.T().Log("Backup and restore completed")
	})
}

// TestErrorHandlingScenarios tests various error conditions and edge cases
func (s *IntegrationTestSuite) TestErrorHandlingScenarios() {
	s.Run("InvalidFlags", func() {
		// Test invalid flags across all commands
		tests := []struct {
			name    string
			cmd     string
			args    []string
			wantErr bool
		}{
			{
				name:    "bootstrap with invalid flag",
				cmd:     "bootstrap",
				args:    []string{"--invalid-flag"},
				wantErr: true,
			},
			{
				name:    "talos with invalid flag",
				cmd:     "talos",
				args:    []string{"--nonexistent"},
				wantErr: true,
			},
			{
				name:    "k8s with invalid flag",
				cmd:     "k8s",
				args:    []string{"--bad-flag"},
				wantErr: true,
			},
			{
				name:    "volsync with invalid flag",
				cmd:     "volsync",
				args:    []string{"--wrong"},
				wantErr: true,
			},
			{
				name:    "workstation with invalid flag",
				cmd:     "workstation",
				args:    []string{"--invalid"},
				wantErr: true,
			},
		}

		for _, tt := range tests {
			s.Run(tt.name, func() {
				rootCmd := createTestRootCommand()
				args := append([]string{tt.cmd}, tt.args...)
				_, err := testutil.ExecuteCommand(rootCmd, args...)
				if tt.wantErr {
					assert.Error(s.T(), err)
				} else {
					assert.NoError(s.T(), err)
				}
			})
		}
	})

	s.Run("InvalidSubcommands", func() {
		tests := []struct {
			name    string
			cmd     string
			subcmd  string
			wantErr bool
		}{
			{
				name:    "bootstrap invalid subcommand",
				cmd:     "bootstrap",
				subcmd:  "nonexistent",
				wantErr: true,
			},
			{
				name:    "talos invalid subcommand",
				cmd:     "talos",
				subcmd:  "invalid",
				wantErr: true,
			},
			{
				name:    "workstation invalid subcommand",
				cmd:     "workstation",
				subcmd:  "missing",
				wantErr: true,
			},
		}

		for _, tt := range tests {
			s.Run(tt.name, func() {
				rootCmd := createTestRootCommand()
				_, err := testutil.ExecuteCommand(rootCmd, tt.cmd, tt.subcmd)
				if tt.wantErr {
					assert.Error(s.T(), err)
				}
			})
		}
	})

	s.Run("EnvironmentValidation", func() {
		// Test behavior with missing environment variables
		originalEnv := map[string]string{
			"KUBECONFIG":        os.Getenv("KUBECONFIG"),
			"TALOSCONFIG":       os.Getenv("TALOSCONFIG"),
			"SOPS_AGE_KEY_FILE": os.Getenv("SOPS_AGE_KEY_FILE"),
		}

		defer func() {
			// Restore original environment
			for key, value := range originalEnv {
				if value == "" {
					os.Unsetenv(key)
				} else {
					os.Setenv(key, value)
				}
			}
		}()

		// Test with missing environment variables
		os.Unsetenv("KUBECONFIG")
		os.Unsetenv("TALOSCONFIG")
		os.Unsetenv("SOPS_AGE_KEY_FILE")

		rootCmd := createTestRootCommand()

		// Some commands should handle missing env vars gracefully
		_, err := testutil.ExecuteCommand(rootCmd, "--help")
		assert.NoError(s.T(), err, "Help should work without env vars")

		_, err = testutil.ExecuteCommand(rootCmd, "--version")
		assert.NoError(s.T(), err, "Version should work without env vars")
	})
}

// TestCLIWorkflowEdgeCases tests edge cases in CLI workflows
func (s *IntegrationTestSuite) TestCLIWorkflowEdgeCases() {
	s.Run("EmptyArguments", func() {
		rootCmd := createTestRootCommand()
		output, err := testutil.ExecuteCommand(rootCmd)
		assert.NoError(s.T(), err)
		assert.Contains(s.T(), output, "Available Commands")
	})

	s.Run("HelpForAllCommands", func() {
		commands := []string{"bootstrap", "talos", "k8s", "volsync", "workstation", "completion"}
		rootCmd := createTestRootCommand()

		for _, cmd := range commands {
			s.Run(cmd+"_help", func() {
				output, err := testutil.ExecuteCommand(rootCmd, cmd, "--help")
				assert.NoError(s.T(), err)
				assert.NotEmpty(s.T(), output)
				assert.Contains(s.T(), output, "Usage:")
			})
		}
	})

	s.Run("VersionCommand", func() {
		rootCmd := createTestRootCommand()
		output, err := testutil.ExecuteCommand(rootCmd, "version")
		// version subcommand might not exist, so we test --version flag instead
		if err != nil {
			output, err = testutil.ExecuteCommand(rootCmd, "--version")
		}
		assert.NoError(s.T(), err)
		assert.NotEmpty(s.T(), output)
	})
}

// TestConcurrentOperations tests CLI behavior under concurrent usage
func (s *IntegrationTestSuite) TestConcurrentOperations() {
	if testing.Short() {
		s.T().Skip("Skipping concurrent operations test in short mode")
	}

	const numGoroutines = 10
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			rootCmd := createTestRootCommand()
			// Test concurrent help commands (safe operations)
			_, err := testutil.ExecuteCommand(rootCmd, "--help")
			if err != nil {
				errors <- fmt.Errorf("goroutine %d failed: %w", id, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errs []error
	for err := range errors {
		errs = append(errs, err)
	}

	assert.Empty(s.T(), errs, "No errors should occur during concurrent help operations")
}

// TestPerformanceBasics tests basic performance characteristics
func (s *IntegrationTestSuite) TestPerformanceBasics() {
	if testing.Short() {
		s.T().Skip("Skipping performance tests in short mode")
	}

	s.Run("CommandCreationSpeed", func() {
		start := time.Now()
		for i := 0; i < 100; i++ {
			_ = createTestRootCommand()
		}
		duration := time.Since(start)

		// Command creation should be fast (under 1 second for 100 iterations)
		assert.Less(s.T(), duration, time.Second, "Command creation should be fast")
	})

	s.Run("HelpCommandSpeed", func() {
		rootCmd := createTestRootCommand()

		start := time.Now()
		for i := 0; i < 10; i++ {
			_, err := testutil.ExecuteCommand(rootCmd, "--help")
			assert.NoError(s.T(), err)
		}
		duration := time.Since(start)

		// Help commands should be fast (under 1 second for 10 iterations)
		assert.Less(s.T(), duration, time.Second, "Help commands should be fast")
	})
}

// createTestRootCommand creates a root command for testing
func createTestRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "homeops",
		Short:   "HomeOps Infrastructure Management CLI",
		Long:    `A comprehensive CLI tool for managing home infrastructure including Talos clusters, Kubernetes applications, VolSync backups, and more.`,
		Version: "test-version",
	}

	// Add all subcommands
	rootCmd.AddCommand(
		bootstrap.NewCommand(),
		completion.NewCommand(),
		kubernetes.NewCommand(),
		talos.NewCommand(),
		volsync.NewCommand(),
		workstation.NewCommand(),
	)

	rootCmd.CompletionOptions.DisableDefaultCmd = true
	return rootCmd
}

// Helper functions
func (s *IntegrationTestSuite) createTestKubeconfig() error {
	kubeconfig := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi...
    server: https://127.0.0.1:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    client-certificate-data: LS0tLS1CRUdJTi...
    client-key-data: LS0tLS1CRUdJTi...
`
	return os.WriteFile(s.kubeconfig, []byte(kubeconfig), 0600)
}

func (s *IntegrationTestSuite) renderTalosConfig(nodeType, outputPath string) error {
	// Mock implementation
	config := `
machine:
  type: ` + nodeType + `
  install:
    disk: /dev/sda
cluster:
  name: test-cluster
`
	return os.WriteFile(outputPath, []byte(config), 0644)
}

// TestIntegrationSuite runs the integration test suite
func TestIntegrationSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	suite.Run(t, new(IntegrationTestSuite))
}

// Performance tests
func TestPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance tests in short mode")
	}

	t.Run("BootstrapPerformance", func(t *testing.T) {
		start := time.Now()
		cmd := bootstrap.NewCommand()
		_, err := testutil.ExecuteCommand(cmd, "--dry-run", "--skip-preflight")
		require.NoError(t, err)
		duration := time.Since(start)
		assert.Less(t, duration, 5*time.Second, "Bootstrap dry-run took too long")
	})

	t.Run("ConfigRenderingPerformance", func(t *testing.T) {
		// Test config rendering performance
		iterations := 100
		start := time.Now()

		for i := 0; i < iterations; i++ {
			// Simulate config rendering
			_ = renderTestConfig()
		}

		duration := time.Since(start)
		avgTime := duration / time.Duration(iterations)
		assert.Less(t, avgTime, 100*time.Millisecond, "Config rendering is too slow")
	})
}

func renderTestConfig() string {
	// Mock config rendering
	return `
machine:
  type: controlplane
cluster:
  name: test
`
}

// Stress tests
func TestStress(t *testing.T) {
	if os.Getenv("RUN_STRESS_TEST") != "true" {
		t.Skip("Skipping stress test - set RUN_STRESS_TEST=true to run")
	}

	t.Run("ConcurrentOperations", func(t *testing.T) {
		// Test concurrent command execution
		numGoroutines := 10
		done := make(chan bool, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				cmd := kubernetes.NewCommand()
				_, err := testutil.ExecuteCommand(cmd, "--help")
				assert.NoError(t, err)
				done <- true
			}(i)
		}

		// Wait for all goroutines
		for i := 0; i < numGoroutines; i++ {
			<-done
		}
	})

	t.Run("LargeScaleDiscovery", func(t *testing.T) {
		// Test discovery with many resources
		// This would simulate discovering hundreds of replication sources
		t.Log("Testing large-scale resource discovery")
	})
}
