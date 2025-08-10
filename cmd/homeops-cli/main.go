package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/bootstrap"
	"homeops-cli/cmd/completion"
	"homeops-cli/cmd/kubernetes"
	"homeops-cli/cmd/talos"
	"homeops-cli/cmd/volsync"
	"homeops-cli/cmd/workstation"
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

func setEnvironment() {
	// Set default environment variables if not already set
	envDefaults := map[string]string{
		"KUBECONFIG":            "./kubeconfig",
		"MINIJINJA_CONFIG_FILE": "./.minijinja.toml",
		"SOPS_AGE_KEY_FILE":     "./age.key",
		"TALOSCONFIG":           "./talosconfig",
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
