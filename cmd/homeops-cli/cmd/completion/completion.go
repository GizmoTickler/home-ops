package completion

import (
	"fmt"
	"os"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"

	"github.com/spf13/cobra"
)

// NewCommand creates the completion command
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate completion script",
		Long: `To load completions:

Bash:

  $ source <(homeops-cli completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ homeops-cli completion bash > /etc/bash_completion.d/homeops-cli
  # macOS:
  $ homeops-cli completion bash > $(brew --prefix)/etc/bash_completion.d/homeops-cli

Zsh:

  # If shell completion is not already enabled in your environment,
  # you will need to enable it.  You can execute the following once:

  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ homeops-cli completion zsh > "${fpath[1]}/_homeops-cli"

  # You will need to start a new shell for this setup to take effect.

fish:

  $ homeops-cli completion fish | source

  # To load completions for each session, execute once:
  $ homeops-cli completion fish > ~/.config/fish/completions/homeops-cli.fish

PowerShell:

  PS> homeops-cli completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> homeops-cli completion powershell > homeops-cli.ps1
  # and source this file from your PowerShell profile.
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			switch args[0] {
			case "bash":
				err = cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				err = cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				err = cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				err = cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell type %q", args[0])
			}
			if err != nil {
				return fmt.Errorf("failed to generate %s completion: %w", args[0], err)
			}
			return nil
		},
	}

	return cmd
}

// ValidConfigFiles provides completion for configuration files
func ValidConfigFiles(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return []string{"yaml", "yml", "json"}, cobra.ShellCompDirectiveFilterFileExt
}

// ValidKubeconfigs provides completion for kubeconfig files
func ValidKubeconfigs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return []string{"kubeconfig", "config"}, cobra.ShellCompDirectiveFilterFileExt
}

// ValidTalosconfigs provides completion for talosconfig files
func ValidTalosconfigs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return []string{"talosconfig"}, cobra.ShellCompDirectiveFilterFileExt
}

// getKubernetesNamespaces fetches namespaces from the cluster
func getKubernetesNamespaces() ([]string, error) {
	cmd := common.Command("kubectl", "get", "namespaces", "-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	namespaces := strings.Fields(string(output))
	return namespaces, nil
}

// getKubernetesApplications fetches applications from volsync-enabled namespaces
func getKubernetesApplications(namespace string) ([]string, error) {
	var output []byte
	var err error
	if namespace != "" {
		output, err = common.Output("kubectl", "get", "replicationsources", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
	} else {
		output, err = common.Output("kubectl", "get", "replicationsources", "--all-namespaces", "-o", "jsonpath={.items[*].metadata.name}")
	}
	if err != nil {
		return nil, err
	}
	apps := strings.Fields(string(output))
	return apps, nil
}

// getKubernetesNodes fetches node IPs from the cluster
func getKubernetesNodes() ([]string, error) {
	cmd := common.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].status.addresses[?(@.type=='InternalIP')].address}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	nodeIPs := strings.Fields(string(output))
	return nodeIPs, nil
}

// getKubernetesNodeNames fetches node names from the cluster
func getKubernetesNodeNames() ([]string, error) {
	cmd := common.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	nodeNames := strings.Fields(string(output))
	return nodeNames, nil
}

// ValidNodeNames provides completion for node names
func ValidNodeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Try to get dynamic node names from cluster
	if nodeNames, err := getKubernetesNodeNames(); err == nil && len(nodeNames) > 0 {
		return nodeNames, cobra.ShellCompDirectiveNoFileComp
	}

	// Fallback to the configured cluster topology
	return config.Get().NodeNames(), cobra.ShellCompDirectiveNoFileComp
}

// ValidNodeIPs provides completion for node IP addresses
func ValidNodeIPs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Try to get dynamic node IPs from cluster
	if nodeIPs, err := getKubernetesNodes(); err == nil && len(nodeIPs) > 0 {
		return nodeIPs, cobra.ShellCompDirectiveNoFileComp
	}

	// Fallback to the configured cluster topology
	nodes := config.Get().Cluster.Nodes
	nodeIPs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		nodeIPs = append(nodeIPs, n.IP)
	}
	return nodeIPs, cobra.ShellCompDirectiveNoFileComp
}

// ValidNamespaces provides completion for Kubernetes namespaces
func ValidNamespaces(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Try to get dynamic namespaces from cluster
	if namespaces, err := getKubernetesNamespaces(); err == nil && len(namespaces) > 0 {
		return namespaces, cobra.ShellCompDirectiveNoFileComp
	}

	// Fallback to static namespaces
	namespaces := []string{
		"default",
		constants.NSKubeSystem,
		constants.NSFluxSystem,
		constants.NSCertManager,
		constants.NSExternalSecret,
		constants.NSObservability,
		constants.NSNetwork,
		constants.NSRookCeph,
		constants.NSOpenEBSSystem,
		constants.NSVolsyncSystem,
		constants.NSSystemUpgrade,
		constants.NSActionsRunner,
	}
	return namespaces, cobra.ShellCompDirectiveNoFileComp
}

// ValidApplications provides completion for applications
func ValidApplications(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Try to get dynamic applications from cluster (volsync replication sources)
	if apps, err := getKubernetesApplications(""); err == nil && len(apps) > 0 {
		return apps, cobra.ShellCompDirectiveNoFileComp
	}

	// Fallback to static applications
	applications := []string{
		"cert-manager",
		"external-secrets",
		"flux",
		"grafana",
		"prometheus",
		"rook-ceph",
		"openebs",
		"volsync",
		"gateway",
		"keda",
	}
	return applications, cobra.ShellCompDirectiveNoFileComp
}

// getTrueNASVMNames fetches VM names from TrueNAS
func getTrueNASVMNames() ([]string, error) {
	// This would require TrueNAS API integration
	// For now, return empty slice to fall back to static names
	return []string{}, nil
}

// ValidVMNames provides completion for TrueNAS VM names
func ValidVMNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Try to get dynamic VM names from TrueNAS
	if vmNames, err := getTrueNASVMNames(); err == nil && len(vmNames) > 0 {
		return vmNames, cobra.ShellCompDirectiveNoFileComp
	}

	// Fallback to the configured cluster node names.
	return config.Get().NodeNames(), cobra.ShellCompDirectiveNoFileComp
}
