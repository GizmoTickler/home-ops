package volsync

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findVolsyncSubcommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := NewCommand().Find(args)
	require.NoError(t, err)
	require.NotNil(t, cmd)
	require.Equal(t, args[len(args)-1], cmd.Name())
	return cmd
}

func TestVolsyncNamespaceFlagsHaveShorthand(t *testing.T) {
	for _, args := range [][]string{
		{"suspend"},
		{"resume"},
		{"snapshot"},
		{"snapshot-all"},
		{"restore"},
		{"restore-all"},
		{"status"},
	} {
		cmd := findVolsyncSubcommand(t, args...)
		flag := cmd.Flags().Lookup("namespace")
		require.NotNil(t, flag, args)
		assert.Equal(t, "n", flag.Shorthand, args)
	}
}

func TestVolsyncStateHasOutputFormatFlag(t *testing.T) {
	cmd := findVolsyncSubcommand(t, "state")
	flag := cmd.Flags().Lookup("output")
	require.NotNil(t, flag)
	assert.Equal(t, "o", flag.Shorthand)
	assert.Equal(t, "table", flag.DefValue)
}
