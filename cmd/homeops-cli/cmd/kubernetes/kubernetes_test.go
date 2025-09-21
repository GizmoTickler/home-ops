package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"homeops-cli/internal/testutil"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "kubernetes", cmd.Use)
	assert.Equal(t, "k8s", cmd.Aliases[0])
	assert.NotEmpty(t, cmd.Short)

	// Check that all subcommands are registered
	subCommands := []string{
		"browse-pvc",
		"node-shell",
		"sync-secrets",
		"cleanse-pods",
		"upgrade-arc",
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

func TestCommandHelp(t *testing.T) {
	cmd := NewCommand()
	output, err := testutil.ExecuteCommand(cmd, "--help")
	assert.NoError(t, err)
	assert.Contains(t, output, "Kubernetes cluster operations")
	assert.Contains(t, output, "browse-pvc")
	assert.Contains(t, output, "node-shell")
}

func TestSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	tests := []string{
		"browse-pvc",
		"node-shell",
		"sync-secrets",
		"cleanse-pods",
		"upgrade-arc",
	}

	for _, subCmd := range tests {
		t.Run(subCmd, func(t *testing.T) {
			output, err := testutil.ExecuteCommand(cmd, subCmd, "--help")
			assert.NoError(t, err)
			assert.NotEmpty(t, output)
		})
	}
}
