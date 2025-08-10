package completion

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewCommand creates the completion command
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate completion script",
		Long: `To load completions:

Bash:

  $ source <(homeops completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ homeops completion bash > /etc/bash_completion.d/homeops
  # macOS:
  $ homeops completion bash > $(brew --prefix)/etc/bash_completion.d/homeops

Zsh:

  # If shell completion is not already enabled in your environment,
  # you will need to enable it.  You can execute the following once:

  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ homeops completion zsh > "${fpath[1]}/_homeops"

  # You will need to start a new shell for this setup to take effect.

fish:

  $ homeops completion fish | source

  # To load completions for each session, execute once:
  $ homeops completion fish > ~/.config/fish/completions/homeops.fish

PowerShell:

  PS> homeops completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> homeops completion powershell > homeops.ps1
  # and source this file from your PowerShell profile.
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		Run: func(cmd *cobra.Command, args []string) {
			switch args[0] {
			case "bash":
				_ = cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				_ = cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				_ = cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				_ = cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				fmt.Fprintf(os.Stderr, "Unsupported shell type %q\n", args[0])
				os.Exit(1)
			}
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

// ValidNodeIPs provides completion for common node IP addresses
func ValidNodeIPs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Common node IPs from the project structure
	nodeIPs := []string{
		"192.168.122.10",
		"192.168.122.11", 
		"192.168.122.12",
	}
	return nodeIPs, cobra.ShellCompDirectiveNoFileComp
}

// ValidNamespaces provides completion for common Kubernetes namespaces
func ValidNamespaces(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	namespaces := []string{
		"default",
		"kube-system",
		"flux-system",
		"cert-manager",
		"external-secrets",
		"observability",
		"network",
		"rook-ceph",
		"openebs-system",
		"volsync-system",
		"system-upgrade",
		"actions-runner-system",
	}
	return namespaces, cobra.ShellCompDirectiveNoFileComp
}

// ValidApplications provides completion for common applications
func ValidApplications(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
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