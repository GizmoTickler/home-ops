#!/bin/bash

# HomeOps CLI Shell Completion Installation Script

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOMEOPS_CLI="${SCRIPT_DIR}/homeops-cli"

# Check if homeops-cli binary exists
if [[ ! -f "$HOMEOPS_CLI" ]]; then
    echo "Error: homeops-cli binary not found at $HOMEOPS_CLI"
    echo "Please build the binary first with: go build -o homeops-cli ."
    exit 1
fi

# Detect shell
SHELL_NAME=$(basename "$SHELL")

echo "Installing HomeOps CLI completion for $SHELL_NAME..."

case "$SHELL_NAME" in
    "bash")
        # Check if we're on macOS with Homebrew
        if [[ "$(uname)" == "Darwin" ]] && command -v brew >/dev/null 2>&1; then
            COMPLETION_DIR="$(brew --prefix)/etc/bash_completion.d"
            mkdir -p "$COMPLETION_DIR"
            "$HOMEOPS_CLI" completion bash > "$COMPLETION_DIR/homeops"
            echo "✅ Bash completion installed to $COMPLETION_DIR/homeops"
            echo "Please restart your terminal or run: source $COMPLETION_DIR/homeops"
        else
            # Linux or other systems
            COMPLETION_DIR="/etc/bash_completion.d"
            if [[ -w "$COMPLETION_DIR" ]]; then
                "$HOMEOPS_CLI" completion bash > "$COMPLETION_DIR/homeops"
                echo "✅ Bash completion installed to $COMPLETION_DIR/homeops"
            else
                echo "⚠️  Cannot write to $COMPLETION_DIR (permission denied)"
                echo "Please run with sudo or manually install:"
                echo "  $HOMEOPS_CLI completion bash | sudo tee $COMPLETION_DIR/homeops"
            fi
        fi
        ;;
    "zsh")
        # Find the first directory in fpath that we can write to
        ZSH_COMPLETION_DIR=""
        for dir in ${fpath[@]}; do
            if [[ -d "$dir" && -w "$dir" ]]; then
                ZSH_COMPLETION_DIR="$dir"
                break
            fi
        done
        
        if [[ -n "$ZSH_COMPLETION_DIR" ]]; then
            "$HOMEOPS_CLI" completion zsh > "$ZSH_COMPLETION_DIR/_homeops"
            echo "✅ Zsh completion installed to $ZSH_COMPLETION_DIR/_homeops"
            echo "Please restart your terminal or run: compinit"
        else
            # Create user-specific completion directory
            USER_COMPLETION_DIR="$HOME/.zsh/completions"
            mkdir -p "$USER_COMPLETION_DIR"
            # Generate completion and fix binary name association
            "$HOMEOPS_CLI" completion zsh > "$USER_COMPLETION_DIR/_homeops-temp"
            echo '#compdef homeops homeops-cli' > "$USER_COMPLETION_DIR/_homeops"
            echo 'compdef _homeops homeops homeops-cli' >> "$USER_COMPLETION_DIR/_homeops"
            tail -n +3 "$USER_COMPLETION_DIR/_homeops-temp" >> "$USER_COMPLETION_DIR/_homeops"
            rm "$USER_COMPLETION_DIR/_homeops-temp"
            echo "✅ Zsh completion installed to $USER_COMPLETION_DIR/_homeops"
            echo ""
            echo "📝 To enable completions, add this to your ~/.zshrc:"
            echo "    fpath=(\"$USER_COMPLETION_DIR\" \$fpath)"
            echo "    autoload -U compinit && compinit"
            echo ""
            echo "Then restart your terminal or run: source ~/.zshrc"
        fi
        ;;
    "fish")
        FISH_COMPLETION_DIR="$HOME/.config/fish/completions"
        mkdir -p "$FISH_COMPLETION_DIR"
        "$HOMEOPS_CLI" completion fish > "$FISH_COMPLETION_DIR/homeops.fish"
        echo "✅ Fish completion installed to $FISH_COMPLETION_DIR/homeops.fish"
        echo "Completion will be available in new fish sessions"
        ;;
    *)
        echo "⚠️  Unsupported shell: $SHELL_NAME"
        echo "Supported shells: bash, zsh, fish"
        echo "You can manually generate completion scripts:"
        echo "  $HOMEOPS_CLI completion bash   # for Bash"
        echo "  $HOMEOPS_CLI completion zsh    # for Zsh"
        echo "  $HOMEOPS_CLI completion fish   # for Fish"
        echo "  $HOMEOPS_CLI completion powershell # for PowerShell"
        exit 1
        ;;
esac

echo ""
echo "🎉 HomeOps CLI completion installation complete!"
echo "You can now use tab completion with the homeops command."
echo ""
echo "Examples:"
echo "  homeops <TAB>                    # Show available commands"
echo "  homeops talos apply-node --ip <TAB>  # Show available node IPs"
echo "  homeops k8s browse-pvc --namespace <TAB>  # Show available namespaces"