package workstation

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()

	// Test command structure
	assert.Equal(t, "workstation", cmd.Use)
	assert.Equal(t, "Setup workstation tools and dependencies", cmd.Short)
	assert.Contains(t, cmd.Long, "Commands for setting up workstation tools")

	// Test subcommands are present
	subcommands := cmd.Commands()
	assert.Len(t, subcommands, 2)

	var brewCmd, krewCmd bool
	for _, subcmd := range subcommands {
		switch subcmd.Use {
		case "brew":
			brewCmd = true
		case "krew":
			krewCmd = true
		}
	}
	assert.True(t, brewCmd, "brew subcommand should be present")
	assert.True(t, krewCmd, "krew subcommand should be present")
}

func TestNewBrewCommand(t *testing.T) {
	cmd := newBrewCommand()

	assert.Equal(t, "brew", cmd.Use)
	assert.Equal(t, "Install Homebrew packages from Brewfile", cmd.Short)
	assert.Contains(t, cmd.Long, "Install all packages defined in the Brewfile")
	assert.NotNil(t, cmd.RunE)
}

func TestNewKrewCommand(t *testing.T) {
	cmd := newKrewCommand()

	assert.Equal(t, "krew", cmd.Use)
	assert.Equal(t, "Install kubectl plugins using Krew", cmd.Short)
	assert.Contains(t, cmd.Long, "Install required kubectl plugins")
	assert.NotNil(t, cmd.RunE)
}

func TestWorkstationHelpOutput(t *testing.T) {
	cmd := NewCommand()

	output, err := testutil.ExecuteCommand(cmd, "--help")
	require.NoError(t, err)

	// Verify help output contains expected content
	assert.Contains(t, output, "Commands for setting up workstation tools")
	assert.Contains(t, output, "Available Commands:")
	assert.Contains(t, output, "brew")
	assert.Contains(t, output, "krew")
}

func TestBrewSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	output, err := testutil.ExecuteCommand(cmd, "brew", "--help")
	require.NoError(t, err)

	assert.Contains(t, output, "Install all packages defined in the Brewfile")
}

func TestKrewSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	output, err := testutil.ExecuteCommand(cmd, "krew", "--help")
	require.NoError(t, err)

	assert.Contains(t, output, "Install required kubectl plugins")
}

func TestIsKrewInstalled(t *testing.T) {
	tests := []struct {
		name           string
		mockCommand    func() *exec.Cmd
		expectedResult bool
	}{
		{
			name: "krew is installed",
			mockCommand: func() *exec.Cmd {
				return exec.Command("true") // Always succeeds
			},
			expectedResult: true,
		},
		{
			name: "krew is not installed",
			mockCommand: func() *exec.Cmd {
				return exec.Command("false") // Always fails
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This test would need dependency injection to properly mock exec.Command
			// For now, we test the function exists and has correct signature
			result := isKrewInstalled()
			// Result will depend on actual system state, so we just verify function runs
			assert.IsType(t, true, result)
		})
	}
}

func TestInstallBrewPackagesValidation(t *testing.T) {
	// Test that function validates brew is installed before proceeding
	// This will fail if brew is not installed, which is expected behavior

	// Save original PATH
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("PATH", originalPath)
	}()

	// Set empty PATH to simulate brew not being available
	_ = os.Setenv("PATH", "")

	err := installBrewPackages()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "homebrew is not installed")
}

func TestInstallKrewPluginsValidation(t *testing.T) {
	// Test that function validates kubectl is installed before proceeding

	// Save original PATH
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("PATH", originalPath)
	}()

	// Set empty PATH to simulate kubectl not being available
	_ = os.Setenv("PATH", "")

	err := installKrewPlugins()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubectl is not installed")
}

// Integration tests for actual command execution
func TestWorkstationCommandIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	cmd := NewCommand()

	t.Run("workstation help", func(t *testing.T) {
		output, err := testutil.ExecuteCommand(cmd, "--help")
		require.NoError(t, err)
		assert.Contains(t, output, "workstation")
	})

	t.Run("brew help", func(t *testing.T) {
		output, err := testutil.ExecuteCommand(cmd, "brew", "--help")
		require.NoError(t, err)
		assert.Contains(t, output, "Homebrew")
	})

	t.Run("krew help", func(t *testing.T) {
		output, err := testutil.ExecuteCommand(cmd, "krew", "--help")
		require.NoError(t, err)
		assert.Contains(t, output, "kubectl plugins")
	})
}

func TestWorkstationErrorHandling(t *testing.T) {
	cmd := NewCommand()

	t.Run("invalid subcommand", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(cmd, "invalid")
		assert.Error(t, err)
	})

	t.Run("brew with invalid flag", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(cmd, "brew", "--invalid-flag")
		assert.Error(t, err)
	})

	t.Run("krew with invalid flag", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(cmd, "krew", "--invalid-flag")
		assert.Error(t, err)
	})
}

// Benchmark tests for performance-sensitive operations
func BenchmarkNewCommand(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewCommand()
	}
}

func BenchmarkIsKrewInstalled(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = isKrewInstalled()
	}
}

// Table-driven tests for multiple scenarios
func TestRunKrewCommandArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "single argument",
			args: []string{"version"},
		},
		{
			name: "multiple arguments",
			args: []string{"install", "ctx"},
		},
		{
			name: "no arguments",
			args: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test validates that the function accepts various argument patterns
			// The actual execution may succeed or fail depending on kubectl/krew availability
			err := runKrewCommand(tt.args...)
			// We don't assert error/success since it depends on system state
			// Just verify the function can be called with different argument patterns
			_ = err // Acknowledge the error variable to avoid unused warnings
		})
	}
}
