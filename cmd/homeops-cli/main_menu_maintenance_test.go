package main

import (
	"testing"

	vmcmd "homeops-cli/cmd/vm"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMenuExcludesNamespaceRequiredMaintenanceCommands guards the interactive
// menu integration: k8s suspend/resume hard-require --namespace, which the
// menu cannot collect, so they are skipped like volsync suspend/resume.
func TestMenuExcludesNamespaceRequiredMaintenanceCommands(t *testing.T) {
	for _, path := range []string{
		"homeops-cli k8s suspend",
		"homeops-cli k8s resume",
		"homeops-cli volsync suspend",
		"homeops-cli volsync resume",
	} {
		assert.Truef(t, menuUnsupportedCommands[path], "%s should be excluded from the interactive menu", path)
	}
}

func TestSubcommandMenuSkipsUnsupportedCommands(t *testing.T) {
	originalChoose := chooseFn
	t.Cleanup(func() { chooseFn = originalChoose })

	var offered []string
	chooseFn = func(prompt string, options []string) (string, error) {
		offered = options
		return "Back - Return to main menu", nil
	}

	k8s := &cobra.Command{Use: "k8s"}
	suspend := &cobra.Command{Use: "suspend <app>", Short: "Suspend an app", Args: cobra.ExactArgs(1)}
	resume := &cobra.Command{Use: "resume <app>", Short: "Resume an app", Args: cobra.ExactArgs(1)}
	doctor := &cobra.Command{Use: "doctor", Short: "Triage", RunE: func(*cobra.Command, []string) error { return nil }}
	k8s.AddCommand(suspend, resume, doctor)

	// menuUnsupportedCommands is keyed by CommandPath(); root Use must be
	// "homeops-cli" for suspend/resume to match "homeops-cli k8s suspend".
	root := &cobra.Command{Use: "homeops-cli"}
	root.AddCommand(k8s)

	require.NoError(t, showSubcommandMenu(k8s))

	joined := ""
	for _, option := range offered {
		joined += option + "\n"
	}
	assert.NotContains(t, joined, "suspend")
	assert.NotContains(t, joined, "resume")
	assert.Contains(t, joined, "doctor")
}

func TestSubcommandMenuSurfacesVMListAllSyntheticEntry(t *testing.T) {
	originalChoose := chooseFn
	t.Cleanup(func() { chooseFn = originalChoose })

	var offered []string
	chooseFn = func(prompt string, options []string) (string, error) {
		offered = options
		return "Back - Return to main menu", nil
	}

	root := &cobra.Command{Use: "homeops-cli"}
	vm := vmcmd.NewVMCommand()
	root.AddCommand(vm)

	require.NoError(t, showSubcommandMenu(vm))

	joined := ""
	for _, option := range offered {
		joined += option + "\n"
	}
	assert.Contains(t, joined, "list-all - List VMs from all configured providers")
}
