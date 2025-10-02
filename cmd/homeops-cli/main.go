package main

import (
	"fmt"
	"homeops-cli/cmd/bootstrap"
	"homeops-cli/cmd/completion"
	"homeops-cli/cmd/kubernetes"
	"homeops-cli/cmd/talos"
	"homeops-cli/cmd/volsync"
	"homeops-cli/cmd/workstation"
	"homeops-cli/internal/ui"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "homeops",
		Short: "HomeOps Infrastructure Management CLI",
		Long: `A comprehensive CLI tool for managing home infrastructure including
Talos clusters, Kubernetes applications, VolSync backups, and more.`,
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no subcommand provided, show interactive menu
			return showInteractiveMenu(cmd)
		},
	}

	// Set global environment variables
	setEnvironment()

	// Add subcommands
	rootCmd.AddCommand(
		bootstrap.NewCommand(),
		completion.NewCommand(),
		kubernetes.NewCommand(),
		talos.NewCommand(),
		volsync.NewCommand(),
		workstation.NewCommand(),
	)

	// Enable completion for all commands
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Execute
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func showInteractiveMenu(rootCmd *cobra.Command) error {
	// Build list of available commands
	commands := []string{
		"bootstrap - Bootstrap Talos nodes and cluster applications",
		"k8s - Kubernetes cluster management",
		"talos - Manage Talos Linux nodes and clusters",
		"volsync - Manage VolSync backup and restore operations",
		"workstation - Setup workstation tools",
	}

	selected, err := ui.Choose("Select a command to run:", commands)
	if err != nil {
		// User cancelled (Ctrl+C) - exit cleanly
		return nil
	}

	// Extract command name from selection
	var cmdName string
	switch {
	case selected[:9] == "bootstrap":
		cmdName = "bootstrap"
	case selected[:3] == "k8s":
		cmdName = "k8s"
	case selected[:5] == "talos":
		cmdName = "talos"
	case selected[:7] == "volsync":
		cmdName = "volsync"
	case selected[:11] == "workstation":
		cmdName = "workstation"
	default:
		return rootCmd.Help()
	}

	// Find the selected command
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == cmdName {
			// If command has subcommands, show interactive submenu
			if cmd.HasSubCommands() {
				return showSubcommandMenu(cmd)
			}
			// If no subcommands, call RunE directly
			if cmd.RunE != nil {
				return cmd.RunE(cmd, []string{})
			}
			if cmd.Run != nil {
				cmd.Run(cmd, []string{})
				return nil
			}
			// If neither exists, show help
			return cmd.Help()
		}
	}

	return rootCmd.Help()
}

func showSubcommandMenu(cmd *cobra.Command) error {
	// Build list of subcommands
	var subcommands []string
	for _, subcmd := range cmd.Commands() {
		if subcmd.Hidden {
			continue
		}
		subcommands = append(subcommands, fmt.Sprintf("%s - %s", subcmd.Name(), subcmd.Short))
	}

	if len(subcommands) == 0 {
		return cmd.Help()
	}

	selected, err := ui.Choose(fmt.Sprintf("Select a %s subcommand:", cmd.Name()), subcommands)
	if err != nil {
		// User cancelled (Ctrl+C) - exit cleanly
		return nil
	}

	// Extract subcommand name from selection (everything before " - ")
	parts := []rune(selected)
	var subcmdName string
	for i := range parts {
		if i < len(parts)-2 && string(parts[i:i+3]) == " - " {
			subcmdName = string(parts[:i])
			break
		}
	}

	if subcmdName == "" {
		return cmd.Help()
	}

	// Find and execute the selected subcommand
	for _, subcmd := range cmd.Commands() {
		if subcmd.Name() == subcmdName {
			// If this subcommand has its own subcommands, show another menu
			if subcmd.HasSubCommands() {
				return showSubcommandMenu(subcmd)
			}
			// Call the command's RunE function directly if it exists
			if subcmd.RunE != nil {
				return subcmd.RunE(subcmd, []string{})
			}
			// Fall back to Run if RunE doesn't exist
			if subcmd.Run != nil {
				subcmd.Run(subcmd, []string{})
				return nil
			}
			// If neither exists, show help
			return subcmd.Help()
		}
	}

	return cmd.Help()
}

func setEnvironment() {
	// Set default environment variables if not already set
	// KUBECONFIG, TALOSCONFIG, and SOPS_AGE_KEY_FILE should use global environment variables
	envDefaults := map[string]string{
		"MINIJINJA_CONFIG_FILE": "./.minijinja.toml",
	}

	for key, defaultValue := range envDefaults {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, defaultValue); err != nil {
				// Log the error but continue execution
				fmt.Fprintf(os.Stderr, "Warning: failed to set environment variable %s: %v\n", key, err)
			}
		}
	}
}
