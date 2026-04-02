package completion

import (
	"testing"

	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCommandGeneratesShellCompletions(t *testing.T) {
	root := &cobra.Command{Use: "homeops-cli"}
	root.AddCommand(NewCommand())

	shells := []string{"bash", "zsh", "fish", "powershell"}
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			stdout, _, err := testutil.CaptureOutput(func() {
				root.SetArgs([]string{"completion", shell})
				require.NoError(t, root.Execute())
			})
			require.NoError(t, err)
			assert.Contains(t, stdout, "homeops-cli")
		})
	}
}

func TestCompletionFallbacks(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	files, directive := ValidConfigFiles(nil, nil, "")
	assert.Equal(t, []string{"yaml", "yml", "json"}, files)
	assert.Equal(t, cobra.ShellCompDirectiveFilterFileExt, directive)

	kubeconfigs, directive := ValidKubeconfigs(nil, nil, "")
	assert.Equal(t, []string{"kubeconfig", "config"}, kubeconfigs)
	assert.Equal(t, cobra.ShellCompDirectiveFilterFileExt, directive)

	talosconfigs, directive := ValidTalosconfigs(nil, nil, "")
	assert.Equal(t, []string{"talosconfig"}, talosconfigs)
	assert.Equal(t, cobra.ShellCompDirectiveFilterFileExt, directive)

	nodeNames, directive := ValidNodeNames(nil, nil, "")
	assert.Contains(t, nodeNames, "k8s-0")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	nodeIPs, directive := ValidNodeIPs(nil, nil, "")
	assert.Contains(t, nodeIPs, "192.168.122.10")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	namespaces, directive := ValidNamespaces(nil, nil, "")
	assert.Contains(t, namespaces, constants.NSFluxSystem)
	assert.Contains(t, namespaces, "default")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	apps, directive := ValidApplications(nil, nil, "")
	assert.Contains(t, apps, "grafana")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	vms, directive := ValidVMNames(nil, nil, "")
	assert.Contains(t, vms, "k8s_0")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}
