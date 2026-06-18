package vm

import (
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	versionconfig "homeops-cli/internal/config"
)

func stubVMNamesForCompletion(t *testing.T, fn func(provider string) ([]string, error)) {
	t.Helper()
	orig := vmNamesForCompletionFn
	vmNamesForCompletionFn = fn
	t.Cleanup(func() { vmNamesForCompletionFn = orig })
}

func TestVMNameCompletionUsesProviderFlag(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	var gotProvider string
	stubVMNamesForCompletion(t, func(provider string) ([]string, error) {
		gotProvider = provider
		return []string{"web0", "db0"}, nil
	})

	cmd := newRestartVMCommand()
	require.NoError(t, cmd.Flags().Set("provider", "truenas"))
	names, directive := vmNameCompletion(cmd, nil, "")
	assert.Equal(t, "truenas", gotProvider)
	assert.Equal(t, []string{"web0", "db0"}, names)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

func TestVMNameCompletionErrorsAndTimeouts(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	stubVMNamesForCompletion(t, func(string) ([]string, error) { return nil, errors.New("api down") })
	cmd := newRestartVMCommand()
	_, directive := vmNameCompletion(cmd, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveError, directive)

	origTimeout := vmNameCompletionTimeout
	vmNameCompletionTimeout = 10 * time.Millisecond
	t.Cleanup(func() { vmNameCompletionTimeout = origTimeout })
	stubVMNamesForCompletion(t, func(string) ([]string, error) {
		time.Sleep(200 * time.Millisecond)
		return []string{"late"}, nil
	})
	_, directive = vmNameCompletion(cmd, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveError, directive, "slow APIs must not hang the shell")
}

func TestEveryVMSubcommandHasNameCompletion(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	var check func(t *testing.T, cmd *cobra.Command)
	check = func(t *testing.T, cmd *cobra.Command) {
		for _, flagName := range []string{"name", "from-vm"} {
			if cmd.Flags().Lookup(flagName) == nil {
				continue
			}
			_, exists := cmd.GetFlagCompletionFunc(flagName)
			assert.True(t, exists, "command %q must complete --%s live", cmd.CommandPath(), flagName)
		}
		switch cmd.Name() {
		case "ip", "ssh", "console":
			assert.NotNil(t, cmd.ValidArgsFunction, "command %q must complete its positional VM name", cmd.CommandPath())
		}
		for _, sub := range cmd.Commands() {
			check(t, sub)
		}
	}
	for _, cmd := range vmLifecycleSubcommands() {
		check(t, cmd)
	}
}
