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
	configcmd "homeops-cli/cmd/config"
	"homeops-cli/cmd/flatcar"
	"homeops-cli/cmd/kubernetes"
	"homeops-cli/cmd/talos"
	"homeops-cli/cmd/volsync"
	"homeops-cli/cmd/workstation"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/ui"

	"github.com/spf13/cobra"
)

var (
	version          = "dev"
	commit           = "none"
	date             = "unknown"
	logLevel         string
	assumeYes        bool
	configPath       string
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
VolSync backups, and more.

Configuration lives in homeops.yaml (cluster topology, hypervisors, and
secret-backend references) — run 'homeops-cli config init' to scaffold one
and 'homeops-cli config doctor' to validate your setup.

Environment:
  HOMEOPS_CONFIG          path to the config file (same as --config)
  HOMEOPS_NO_INTERACTIVE  set to 1 to disable interactive prompts (CI mode)`,
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Set global log level from flag (if provided) before any command runs
			if logLevel != "" {
				common.SetGlobalLogLevel(logLevel)
			}
			ui.SetAssumeYes(assumeYes)
			// Record --config before any command loads the configuration.
			if configPath != "" {
				config.SetExplicitPath(configPath)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Only launch the interactive menu on a real terminal; when stdin is
			// piped or redirected (scripts, CI), print help instead of blocking.
			if !stdinIsTerminal() {
				return cmd.Help()
			}
			return showInteractiveMenu(cmd)
		},
	}

	// Add global flags
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "Set log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().BoolVarP(&assumeYes, "yes", "y", false, "Assume yes for all confirmation prompts (non-interactive)")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to the homeops config file (default: ./homeops.yaml, <git root>/homeops.yaml, or ~/.config/homeops/config.yaml)")

	// Set global environment variables
	setEnvironment()

	// Add subcommands
	rootCmd.AddCommand(
		bootstrap.NewCommand(),
		completion.NewCommand(),
		configcmd.NewCommand(),
		flatcar.NewCommand(),
		kubernetes.NewCommand(),
		talos.NewCommand(),
		volsync.NewCommand(),
		workstation.NewCommand(),
		newVersionCommand(),
	)

	// Enable completion for all commands
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Set context on root command - subcommands can access via cmd.Context()
	rootCmd.SetContext(ctx)

	return rootCmd
}

// stdinIsTerminal reports whether stdin is attached to a terminal (vs a pipe
// or redirect), so interactive prompts are only offered where a user can type.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
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

// newVersionCommand exposes the build info as `homeops-cli version` so
// scripts don't have to parse --help output.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and build date",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("homeops-cli %s\ncommit: %s\nbuilt:  %s\n", version, commit, date)
		},
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
