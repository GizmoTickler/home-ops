package completion

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

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

// getKubernetesNamespaces fetches namespaces from the cluster
func getKubernetesNamespaces() ([]string, error) {
	cmd := exec.Command("kubectl", "get", "namespaces", "-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	namespaces := strings.Fields(string(output))
	return namespaces, nil
}

// getKubernetesApplications fetches applications from volsync-enabled namespaces
func getKubernetesApplications(namespace string) ([]string, error) {
	var cmd *exec.Cmd
	if namespace != "" {
		cmd = exec.Command("kubectl", "get", "replicationsources", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
	} else {
		cmd = exec.Command("kubectl", "get", "replicationsources", "--all-namespaces", "-o", "jsonpath={.items[*].metadata.name}")
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	apps := strings.Fields(string(output))
	return apps, nil
}

// getKubernetesNodes fetches node IPs from the cluster
func getKubernetesNodes() ([]string, error) {
	cmd := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].status.addresses[?(@.type=='InternalIP')].address}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	nodeIPs := strings.Fields(string(output))
	return nodeIPs, nil
}

// getKubernetesNodeNames fetches node names from the cluster
func getKubernetesNodeNames() ([]string, error) {
	cmd := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}")
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
	
	// Fallback to static node names
	nodeNames := []string{
		"k8s-0",
		"k8s-1",
		"k8s-2",
	}
	return nodeNames, cobra.ShellCompDirectiveNoFileComp
}

// ValidNodeIPs provides completion for node IP addresses
func ValidNodeIPs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Try to get dynamic node IPs from cluster
	if nodeIPs, err := getKubernetesNodes(); err == nil && len(nodeIPs) > 0 {
		return nodeIPs, cobra.ShellCompDirectiveNoFileComp
	}
	
	// Fallback to static node IPs
	nodeIPs := []string{
		"192.168.122.10",
		"192.168.122.11", 
		"192.168.122.12",
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
	
	// Fallback to static VM names (common Talos node names)
	vmNames := []string{
		"k8s_0",
		"k8s_1",
		"k8s_2",
		"talos_master",
		"talos_worker",
	}
	return vmNames, cobra.ShellCompDirectiveNoFileComp
}