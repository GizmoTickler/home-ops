package kubernetes

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findK8sSubcommand(t *testing.T, name string) *cobra.Command {
	t.Helper()
	cmd, _, err := NewCommand().Find([]string{name})
	require.NoError(t, err)
	require.NotNil(t, cmd)
	require.Equal(t, name, cmd.Name())
	return cmd
}

func TestK8sNamespaceFlagsHaveShorthand(t *testing.T) {
	for _, name := range []string{"browse-pvc", "sync-secrets"} {
		cmd := findK8sSubcommand(t, name)
		flag := cmd.Flags().Lookup("namespace")
		require.NotNil(t, flag, name)
		assert.Equal(t, "n", flag.Shorthand, name)
	}
}

func TestRenderKsUsesOutputFileCanonicalFlag(t *testing.T) {
	cmd := findK8sSubcommand(t, "render-ks")

	require.NotNil(t, cmd.Flags().Lookup("output-file"))
	legacy := cmd.Flags().Lookup("output")
	require.NotNil(t, legacy)
	assert.True(t, legacy.Hidden)
	assert.NotEmpty(t, legacy.Deprecated)
	assert.Equal(t, "o", legacy.Shorthand)
}

func TestDayTwoCommandsRegistered(t *testing.T) {
	etcd := findK8sSubcommand(t, "etcd")
	for _, name := range []string{"backup", "status"} {
		child, _, err := etcd.Find([]string{name})
		require.NoError(t, err)
		assert.Equal(t, name, child.Name())
	}

	certs := findK8sSubcommand(t, "certs")
	for _, name := range []string{"warn-days", "fail-on-warn", "renew", "restart-control-plane", "output"} {
		assert.NotNil(t, certs.Flags().Lookup(name), name)
	}
}
