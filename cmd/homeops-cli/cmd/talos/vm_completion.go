package talos

import (
	"time"

	"github.com/spf13/cobra"
)

// vmNameCompletionTimeout bounds how long shell completion may block on a
// live hypervisor API before giving up.
var vmNameCompletionTimeout = 4 * time.Second

// vmNamesForCompletionFn lists VM names for completion. Swappable for tests.
var vmNamesForCompletionFn = getVMNamesForProvider

// vmNameCompletion completes VM names live from the hypervisor selected by
// the command's --provider flag (or the configured default).
func vmNameCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	provider := defaultProviderName()
	if f := cmd.Flags().Lookup("provider"); f != nil && f.Value.String() != "" {
		provider = f.Value.String()
	}

	type result struct {
		names []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		names, err := vmNamesForCompletionFn(provider)
		ch <- result{names, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return r.names, cobra.ShellCompDirectiveNoFileComp
	case <-time.After(vmNameCompletionTimeout):
		return nil, cobra.ShellCompDirectiveError
	}
}

// registerVMNameCompletion wires live VM-name completion onto a vm
// subcommand tree: every --name / --from-vm flag and the positional <name>
// commands (ip, ssh, console). Already-registered flags are left alone.
func registerVMNameCompletion(cmd *cobra.Command) {
	for _, flagName := range []string{"name", "from-vm"} {
		if cmd.Flags().Lookup(flagName) == nil {
			continue
		}
		if _, exists := cmd.GetFlagCompletionFunc(flagName); !exists {
			_ = cmd.RegisterFlagCompletionFunc(flagName, vmNameCompletion)
		}
	}
	switch cmd.Name() {
	case "ip", "ssh", "console":
		if cmd.ValidArgsFunction == nil {
			cmd.ValidArgsFunction = vmNameCompletion
		}
	}
	for _, sub := range cmd.Commands() {
		registerVMNameCompletion(sub)
	}
}
