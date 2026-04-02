# HomeOps CLI Shell Completion

The HomeOps CLI includes comprehensive shell completion support for Bash, Zsh, Fish, and PowerShell.

## Features

### Smart Completions

- **Node IPs**: Auto-complete common node IP addresses (192.168.122.10-12)
- **Namespaces**: Complete Kubernetes namespace names
- **Applications**: Complete common application names
- **File Extensions**: Complete configuration files (.yaml, .yml, .json)
- **Commands & Flags**: Complete all commands, subcommands, and flags

### Supported Shells

- **Bash** (Linux/macOS)
- **Zsh** (macOS default, Linux)
- **Fish** (Cross-platform)
- **PowerShell** (Windows/Cross-platform)

## Quick Installation

### Automatic Installation

Run the provided installation script:

```bash
./install-completion.sh
```

This script will:
- Detect your current shell
- Install completion to the appropriate location
- Provide instructions for activation

### Manual Installation

#### Bash

**macOS (with Homebrew):**
```bash
homeops-cli completion bash > $(brew --prefix)/etc/bash_completion.d/homeops-cli
```

**Linux:**
```bash
sudo homeops-cli completion bash > /etc/bash_completion.d/homeops-cli
```

**Per-session (any system):**
```bash
source <(homeops-cli completion bash)
```

#### Zsh

**Enable completion system (if not already enabled):**
```zsh
echo "autoload -U compinit; compinit" >> ~/.zshrc
```

**Install completion:**
```zsh
homeops-cli completion zsh > "${fpath[1]}/_homeops-cli"
```

**Per-session:**
```zsh
source <(homeops-cli completion zsh)
```

#### Fish

**Install completion:**
```fish
homeops-cli completion fish > ~/.config/fish/completions/homeops-cli.fish
```

**Per-session:**
```fish
homeops-cli completion fish | source
```

#### PowerShell

**Per-session:**
```powershell
homeops-cli completion powershell | Out-String | Invoke-Expression
```

**Persistent (add to profile):**
```powershell
homeops-cli completion powershell > homeops-cli.ps1
# Add to your PowerShell profile
```

## Usage Examples

Once installed, you can use tab completion with any HomeOps command:

### Basic Command Completion
```bash
homeops-cli <TAB>                    # Shows: bootstrap, completion, k8s, talos, volsync, workstation
homeops-cli talos <TAB>              # Shows: apply-node, deploy-vm, kubeconfig, etc.
```

### Smart Parameter Completion
```bash
# Node IP completion
homeops-cli talos apply-node --ip <TAB>
# Shows: 192.168.122.10, 192.168.122.11, 192.168.122.12

# Namespace completion
homeops-cli k8s browse-pvc --namespace <TAB>
# Shows: default, kube-system, flux-system, cert-manager, etc.

# Application completion
homeops-cli bootstrap --app <TAB>
# Shows: cert-manager, external-secrets, flux, grafana, etc.
```

### File Path Completion
```bash
# Configuration files
homeops-cli talos apply-node --config <TAB>
# Shows .yaml, .yml, .json files

# Kubeconfig files
homeops-cli k8s --kubeconfig <TAB>
# Shows kubeconfig, config files
```

## Completion Functions

The completion system includes several specialized completion functions:

- `ValidNodeIPs`: Common node IP addresses from the infrastructure
- `ValidNamespaces`: Kubernetes namespaces used in the home lab
- `ValidApplications`: Common applications deployed in the cluster
- `ValidConfigFiles`: Configuration file extensions
- `ValidKubeconfigs`: Kubeconfig file patterns
- `ValidTalosconfigs`: Talosconfig file patterns

## Troubleshooting

### Completion Not Working

1. **Verify installation:**
   ```bash
   homeops-cli completion bash --help  # Should show help
   ```

2. **Check shell configuration:**
   - Bash: Ensure bash-completion is installed
   - Zsh: Ensure `compinit` is called in `.zshrc`
   - Fish: Completion should work automatically

3. **Reload shell:**
   ```bash
   exec $SHELL  # Restart current shell
   ```

### Permission Issues

If you get permission errors during installation:

```bash
# Use sudo for system-wide installation
sudo ./install-completion.sh

# Or install per-user
mkdir -p ~/.local/share/bash-completion/completions
homeops-cli completion bash > ~/.local/share/bash-completion/completions/homeops-cli
```

### Custom Completion Directory

For custom installation locations:

```bash
# Set custom directory
export COMPLETION_DIR="/path/to/completions"
homeops-cli completion bash > "$COMPLETION_DIR/homeops-cli"
```

## Development

To add new completion functions:

1. Add function to `cmd/completion/completion.go`
2. Register with command using `RegisterFlagCompletionFunc`
3. Test with `homeops-cli completion bash | grep -A 10 "function_name"`

### Example

```go
// In completion.go
func ValidClusters(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
    clusters := []string{"prod", "staging", "dev"}
    return clusters, cobra.ShellCompDirectiveNoFileComp
}

// In command file
cmd.RegisterFlagCompletionFunc("cluster", completion.ValidClusters)
```

## See Also

- [Cobra Completion Documentation](https://github.com/spf13/cobra/blob/main/shell_completions.md)
- [Bash Completion Guide](https://github.com/scop/bash-completion)
- [Zsh Completion System](http://zsh.sourceforge.net/Doc/Release/Completion-System.html)
