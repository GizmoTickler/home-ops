package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"homeops-cli/cmd/bootstrap"
	"homeops-cli/cmd/completion"
	"homeops-cli/cmd/flatcar"
	"homeops-cli/cmd/kubernetes"
	"homeops-cli/cmd/talos"
	"homeops-cli/cmd/volsync"
	"homeops-cli/cmd/workstation"
	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/ui"

	"github.com/spf13/cobra"
)

var (
	version          = "dev"
	commit           = "none"
	date             = "unknown"
	logLevel         string
	chooseFn                   = ui.Choose
	signalNotifyFn             = signal.Notify
	executeRootCmdFn           = func(cmd *cobra.Command) error { return cmd.Execute() }
	stderrWriter     io.Writer = os.Stderr
)

func main() {
	sigChan := make(chan os.Signal, 1)
	if code := runApp(sigChan); code != 0 {
		os.Exit(code)
	}
}

func runApp(sigChan chan os.Signal) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalNotifyFn(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		_, _ = fmt.Fprintln(stderrWriter, "\nReceived interrupt signal, shutting down...")
		cancel()
	}()

	rootCmd := newRootCommand(ctx)
	if err := executeRootCmdFn(rootCmd); err != nil {
		_, _ = fmt.Fprintf(stderrWriter, "Error: %v\n", err)
		return 1
	}

	return 0
}

func newRootCommand(ctx context.Context) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "homeops-cli",
		Short: "HomeOps Infrastructure Management CLI",
		Long: `A comprehensive CLI tool for managing home infrastructure including
Flatcar/kubeadm (and legacy Talos) clusters, Kubernetes applications,
VolSync backups, and more.`,
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Set global log level from flag (if provided) before any command runs
			if logLevel != "" {
				common.SetGlobalLogLevel(logLevel)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no subcommand provided, show interactive menu
			return showInteractiveMenu(cmd)
		},
	}

	// Add global flags
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "Set log level (debug, info, warn, error)")

	// Set global environment variables
	setEnvironment()

	// Add subcommands
	rootCmd.AddCommand(
		bootstrap.NewCommand(),
		completion.NewCommand(),
		flatcar.NewCommand(),
		kubernetes.NewCommand(),
		talos.NewCommand(),
		volsync.NewCommand(),
		workstation.NewCommand(),
	)

	// Enable completion for all commands
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Set context on root command - subcommands can access via cmd.Context()
	rootCmd.SetContext(ctx)

	return rootCmd
}

func showInteractiveMenu(rootCmd *cobra.Command) error {
	for {
		// Build the menu from the live command tree so it never drifts from the
		// registered subcommands. Skip hidden + non-interactive helpers.
		var labels []string
		for _, c := range rootCmd.Commands() {
			if c.Hidden || c.Name() == "completion" || c.Name() == "help" {
				continue
			}
			label := c.Name()
			if c.Short != "" {
				label += " - " + c.Short
			}
			labels = append(labels, label)
		}
		labels = append(labels, "Exit - Exit the application")

		selected, err := chooseFn("Select a command to run:", labels)
		if err != nil {
			// User cancelled (Ctrl+C) - exit cleanly
			return nil
		}
		if strings.HasPrefix(selected, "Exit") {
			return nil
		}

		// Resolve the command name = the token before " - " (label format above).
		cmdName := selected
		if i := strings.Index(selected, " - "); i >= 0 {
			cmdName = selected[:i]
		}
		var target *cobra.Command
		for _, c := range rootCmd.Commands() {
			if c.Name() == cmdName {
				target = c
				break
			}
		}
		if target == nil {
			return rootCmd.Help()
		}

		switch {
		case target.HasSubCommands():
			if err := showSubcommandMenu(target); err != nil {
				return err
			}
		case target.RunE != nil:
			if err := target.RunE(target, []string{}); err != nil {
				return err
			}
		case target.Run != nil:
			target.Run(target, []string{})
		default:
			_ = target.Help()
		}
	}
}

func showSubcommandMenu(cmd *cobra.Command) error {
	for {
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

		// Add Back option
		subcommands = append(subcommands, "Back - Return to main menu")

		selected, err := chooseFn(fmt.Sprintf("Select a %s subcommand:", cmd.Name()), subcommands)
		if err != nil {
			// User cancelled (Ctrl+C) - exit cleanly
			return nil
		}

		// Check for Back option
		if strings.HasPrefix(selected, "Back") {
			return nil // Return to main menu
		}

		// Extract subcommand name from selection (everything before " - ")
		parts := strings.SplitN(selected, " - ", 2)
		if len(parts) == 0 {
			return cmd.Help()
		}
		subcmdName := parts[0]

		// Find and execute the selected subcommand
		for _, subcmd := range cmd.Commands() {
			if subcmd.Name() == subcmdName {
				// If this subcommand has its own subcommands, show another menu
				if subcmd.HasSubCommands() {
					if err := showSubcommandMenu(subcmd); err != nil {
						return err
					}
					continue // Return to this menu after subcommand menu returns
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
	}
}

func setEnvironment() {
	// Set default environment variables if not already set
	// KUBECONFIG and TALOSCONFIG should use global environment variables

	// Convert relative path to absolute for MINIJINJA_CONFIG_FILE
	minijinjaConfig := "./.minijinja.toml"
	if absPath, err := filepath.Abs(minijinjaConfig); err == nil {
		minijinjaConfig = absPath
	}

	envDefaults := map[string]string{
		constants.EnvMiniJinjaConfig: minijinjaConfig,
	}

	for key, defaultValue := range envDefaults {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, defaultValue); err != nil {
				// Log the error but continue execution
				// Note: We can't use logger here as it may not be initialized yet
				fmt.Fprintf(os.Stderr, "Warning: failed to set environment variable %s: %v\n", key, err)
			}
		}
	}
}
