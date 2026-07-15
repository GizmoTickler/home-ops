package cmdutil

import (
	"time"

	"github.com/spf13/cobra"
)

// ResolveStringFlagDefault assigns defaultFn() to target only when name exists
// on cmd and the user did not explicitly set the flag. Commands register
// config-derived flags with an empty sentinel default, then call this helper in
// RunE after --config / HOMEOPS_CONFIG has been resolved.
func ResolveStringFlagDefault(cmd *cobra.Command, name string, target *string, defaultFn func() string) {
	if cmd == nil || target == nil || defaultFn == nil {
		return
	}
	flag := cmd.Flags().Lookup(name)
	if flag == nil || flag.Changed {
		return
	}
	*target = defaultFn()
}

// ResolveIntFlagDefault assigns defaultFn() to target only when name exists on
// cmd and the user did not explicitly set the flag. A zero flag value therefore
// remains a useful sentinel without preventing callers from explicitly passing
// --flag 0.
func ResolveIntFlagDefault(cmd *cobra.Command, name string, target *int, defaultFn func() int) {
	if cmd == nil || target == nil || defaultFn == nil {
		return
	}
	flag := cmd.Flags().Lookup(name)
	if flag == nil || flag.Changed {
		return
	}
	*target = defaultFn()
}

// ResolveDurationFlagDefault assigns a config-derived duration after the root
// command has resolved --config. Duration flags use zero as their sentinel.
func ResolveDurationFlagDefault(cmd *cobra.Command, name string, target *time.Duration, defaultFn func() time.Duration) {
	if cmd == nil || target == nil || defaultFn == nil {
		return
	}
	flag := cmd.Flags().Lookup(name)
	if flag == nil || flag.Changed {
		return
	}
	*target = defaultFn()
}
