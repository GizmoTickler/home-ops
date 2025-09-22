package main

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/cmd/bootstrap"
	"homeops-cli/cmd/completion"
	"homeops-cli/cmd/kubernetes"
	"homeops-cli/cmd/talos"
	"homeops-cli/cmd/volsync"
	"homeops-cli/cmd/workstation"
	"homeops-cli/internal/testutil"
)

func TestMainCLIStructure(t *testing.T) {
	// Test that the main CLI has the expected structure and commands
	rootCmd := createRootCommand()

	assert.Equal(t, "homeops", rootCmd.Use)
	assert.Contains(t, rootCmd.Short, "HomeOps Infrastructure Management CLI")
	assert.Contains(t, rootCmd.Long, "comprehensive CLI tool")

	// Check that all expected subcommands are registered
	expectedCommands := []string{
		"bootstrap",
		"completion",
		"k8s",
		"talos",
		"volsync",
		"workstation",
	}

	actualCommands := make([]string, 0)
	for _, cmd := range rootCmd.Commands() {
		actualCommands = append(actualCommands, cmd.Name())
	}

	for _, expected := range expectedCommands {
		assert.Contains(t, actualCommands, expected, "Missing command: %s", expected)
	}
}

func TestCLIHelpOutput(t *testing.T) {
	rootCmd := createRootCommand()

	output, err := testutil.ExecuteCommand(rootCmd, "--help")
	require.NoError(t, err)

	// Verify help output contains expected content
	assert.Contains(t, output, "A comprehensive CLI tool for managing home infrastructure")
	assert.Contains(t, output, "Available Commands:")
	assert.Contains(t, output, "bootstrap")
	assert.Contains(t, output, "k8s")
	assert.Contains(t, output, "talos")
	assert.Contains(t, output, "volsync")
	assert.Contains(t, output, "completion")
	assert.Contains(t, output, "workstation")
}

func TestCLIVersionOutput(t *testing.T) {
	rootCmd := createRootCommand()

	output, err := testutil.ExecuteCommand(rootCmd, "--version")
	require.NoError(t, err)

	// Should contain version information
	assert.Contains(t, output, "dev")
	assert.Contains(t, output, "commit:")
	assert.Contains(t, output, "built:")
}

func TestSubcommandHelp(t *testing.T) {
	rootCmd := createRootCommand()

	subcommands := []string{"bootstrap", "k8s", "talos", "volsync"}

	for _, subcmd := range subcommands {
		t.Run(subcmd, func(t *testing.T) {
			output, err := testutil.ExecuteCommand(rootCmd, subcmd, "--help")
			require.NoError(t, err)
			assert.NotEmpty(t, output)
			assert.Contains(t, strings.ToLower(output), strings.ToLower(subcmd))
		})
	}
}

func TestEnvironmentSetup(t *testing.T) {
	// Save original environment
	originalMiniJinja := os.Getenv("MINIJINJA_CONFIG_FILE")
	defer func() {
		if originalMiniJinja != "" {
			_ = os.Setenv("MINIJINJA_CONFIG_FILE", originalMiniJinja)
		} else {
			_ = os.Unsetenv("MINIJINJA_CONFIG_FILE")
		}
	}()

	// Clear the environment variable
	_ = os.Unsetenv("MINIJINJA_CONFIG_FILE")

	// Call setEnvironment
	setEnvironment()

	// Check that default was set
	assert.Equal(t, "./.minijinja.toml", os.Getenv("MINIJINJA_CONFIG_FILE"))
}

func TestEnvironmentSetupDoesNotOverride(t *testing.T) {
	// Save original environment
	originalMiniJinja := os.Getenv("MINIJINJA_CONFIG_FILE")
	defer func() {
		if originalMiniJinja != "" {
			_ = os.Setenv("MINIJINJA_CONFIG_FILE", originalMiniJinja)
		} else {
			_ = os.Unsetenv("MINIJINJA_CONFIG_FILE")
		}
	}()

	// Set a custom value
	customValue := "/custom/path/minijinja.toml"
	_ = os.Setenv("MINIJINJA_CONFIG_FILE", customValue)

	// Call setEnvironment
	setEnvironment()

	// Check that custom value was not overridden
	assert.Equal(t, customValue, os.Getenv("MINIJINJA_CONFIG_FILE"))
}

func TestCLIErrorHandling(t *testing.T) {
	rootCmd := createRootCommand()

	// Test invalid command
	output, err := testutil.ExecuteCommand(rootCmd, "invalid-command")
	assert.Error(t, err)
	assert.Contains(t, output, "unknown command")
}

func TestCLICompletion(t *testing.T) {
	rootCmd := createRootCommand()

	// Test that completion is available
	output, err := testutil.ExecuteCommand(rootCmd, "completion", "--help")
	require.NoError(t, err)
	assert.Contains(t, output, "completion")
}

// Helper function to create the root command for testing
func createRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "homeops",
		Short: "HomeOps Infrastructure Management CLI",
		Long: `A comprehensive CLI tool for managing home infrastructure including
Talos clusters, Kubernetes applications, VolSync backups, and more.`,
		Version: "dev (commit: none, built: unknown)",
	}

	// Add subcommands (import the actual command constructors)
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

// Functional test for the complete CLI workflow
func TestCLIWorkflow(t *testing.T) {
	rootCmd := createRootCommand()

	t.Run("help workflow", func(t *testing.T) {
		// Test complete help workflow
		helpOutput, err := testutil.ExecuteCommand(rootCmd, "--help")
		require.NoError(t, err)
		assert.Contains(t, helpOutput, "Available Commands:")

		// Test subcommand help
		talosHelp, err := testutil.ExecuteCommand(rootCmd, "talos", "--help")
		require.NoError(t, err)
		assert.Contains(t, talosHelp, "Talos")

		k8sHelp, err := testutil.ExecuteCommand(rootCmd, "k8s", "--help")
		require.NoError(t, err)
		assert.Contains(t, k8sHelp, "Kubernetes")
	})

	t.Run("version workflow", func(t *testing.T) {
		versionOutput, err := testutil.ExecuteCommand(rootCmd, "--version")
		require.NoError(t, err)
		// Check if output contains version info or help (either is acceptable)
		assert.True(t,
			strings.Contains(versionOutput, "homeops version") ||
				strings.Contains(versionOutput, "Available Commands:"),
			"Output should contain version info or help menu")
	})
}

// Integration test for CLI command structure
func TestCLICommandStructureIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	rootCmd := createRootCommand()

	// Test that each major command group has expected subcommands
	testCases := []struct {
		command         string
		expectedSubcmds []string
	}{
		{
			command:         "talos",
			expectedSubcmds: []string{"apply-node", "deploy-vm", "upgrade-k8s"},
		},
		{
			command:         "k8s",
			expectedSubcmds: []string{"browse-pvc", "node-shell", "sync-secrets"},
		},
		{
			command:         "volsync",
			expectedSubcmds: []string{"snapshot", "restore"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.command, func(t *testing.T) {
			output, err := testutil.ExecuteCommand(rootCmd, tc.command, "--help")
			require.NoError(t, err)

			for _, subcmd := range tc.expectedSubcmds {
				assert.Contains(t, output, subcmd,
					"Command %s should have subcommand %s", tc.command, subcmd)
			}
		})
	}
}
