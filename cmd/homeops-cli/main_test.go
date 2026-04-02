package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRootCommandAndEnvironment(t *testing.T) {
	t.Setenv("MINIJINJA_CONFIG_FILE", "")
	cmd := newRootCommand(context.Background())

	assert.Equal(t, "homeops-cli", cmd.Use)
	assert.True(t, cmd.CompletionOptions.DisableDefaultCmd)
	assert.Equal(t, context.Background(), cmd.Context())
	assert.NotNil(t, cmd.VersionTemplate())

	minijinjaConfig := os.Getenv("MINIJINJA_CONFIG_FILE")
	require.NotEmpty(t, minijinjaConfig)
	assert.True(t, filepath.IsAbs(minijinjaConfig))

	subcommands := map[string]bool{}
	for _, subcmd := range cmd.Commands() {
		subcommands[subcmd.Name()] = true
	}
	assert.True(t, subcommands["bootstrap"])
	assert.True(t, subcommands["completion"])
	assert.True(t, subcommands["k8s"])
	assert.True(t, subcommands["talos"])
	assert.True(t, subcommands["volsync"])
	assert.True(t, subcommands["workstation"])
}

func TestShowInteractiveMenu(t *testing.T) {
	originalChoose := chooseFn
	t.Cleanup(func() { chooseFn = originalChoose })

	t.Run("exit immediately", func(t *testing.T) {
		chooseFn = func(prompt string, options []string) (string, error) {
			assert.Contains(t, prompt, "Select a command")
			return "Exit - Exit the application", nil
		}
		require.NoError(t, showInteractiveMenu(&cobra.Command{Use: "homeops-cli"}))
	})

	t.Run("runs leaf command then exits", func(t *testing.T) {
		var chooseCalls int
		var ran bool
		chooseFn = func(prompt string, options []string) (string, error) {
			chooseCalls++
			if chooseCalls == 1 {
				return "workstation - Setup workstation tools", nil
			}
			return "Exit - Exit the application", nil
		}

		rootCmd := &cobra.Command{Use: "homeops-cli"}
		rootCmd.AddCommand(&cobra.Command{
			Use: "workstation",
			RunE: func(cmd *cobra.Command, args []string) error {
				ran = true
				return nil
			},
		})

		require.NoError(t, showInteractiveMenu(rootCmd))
		assert.True(t, ran)
	})

	t.Run("unknown selection falls back to help", func(t *testing.T) {
		chooseFn = func(prompt string, options []string) (string, error) {
			return "mystery command", nil
		}

		var helped bool
		rootCmd := &cobra.Command{Use: "homeops-cli"}
		rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
			helped = true
		})

		require.NoError(t, showInteractiveMenu(rootCmd))
		assert.True(t, helped)
	})

	t.Run("cancel exits cleanly", func(t *testing.T) {
		chooseFn = func(prompt string, options []string) (string, error) {
			return "", errors.New("cancelled")
		}
		require.NoError(t, showInteractiveMenu(&cobra.Command{Use: "homeops-cli"}))
	})
}

func TestShowSubcommandMenu(t *testing.T) {
	originalChoose := chooseFn
	t.Cleanup(func() { chooseFn = originalChoose })

	t.Run("back exits submenu", func(t *testing.T) {
		chooseFn = func(prompt string, options []string) (string, error) {
			assert.Contains(t, prompt, "Select a talos subcommand")
			return "Back - Return to main menu", nil
		}

		cmd := &cobra.Command{Use: "talos"}
		cmd.AddCommand(&cobra.Command{Use: "status", Short: "status"})
		require.NoError(t, showSubcommandMenu(cmd))
	})

	t.Run("runs subcommand", func(t *testing.T) {
		var ran bool
		chooseFn = func(prompt string, options []string) (string, error) {
			return "status - Show status", nil
		}

		cmd := &cobra.Command{Use: "talos"}
		cmd.AddCommand(&cobra.Command{
			Use:   "status",
			Short: "Show status",
			RunE: func(cmd *cobra.Command, args []string) error {
				ran = true
				return nil
			},
		})

		require.NoError(t, showSubcommandMenu(cmd))
		assert.True(t, ran)
	})

	t.Run("nested submenu returns to parent", func(t *testing.T) {
		choices := []string{
			"cluster - Cluster operations",
			"status - Show status",
			"Back - Return to main menu",
		}
		var idx int
		var ran bool
		chooseFn = func(prompt string, options []string) (string, error) {
			choice := choices[idx]
			idx++
			return choice, nil
		}

		cmd := &cobra.Command{Use: "talos"}
		clusterCmd := &cobra.Command{Use: "cluster", Short: "Cluster operations"}
		clusterCmd.AddCommand(&cobra.Command{
			Use:   "status",
			Short: "Show status",
			Run: func(cmd *cobra.Command, args []string) {
				ran = true
			},
		})
		cmd.AddCommand(clusterCmd)

		require.NoError(t, showSubcommandMenu(cmd))
		assert.True(t, ran)
	})

	t.Run("no visible subcommands shows help", func(t *testing.T) {
		chooseFn = func(prompt string, options []string) (string, error) {
			return "", nil
		}

		var helped bool
		cmd := &cobra.Command{Use: "talos"}
		cmd.AddCommand(&cobra.Command{Use: "hidden", Hidden: true})
		cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
			helped = true
		})

		require.NoError(t, showSubcommandMenu(cmd))
		assert.True(t, helped)
	})
}

func TestRunApp(t *testing.T) {
	originalNotify := signalNotifyFn
	originalExecute := executeRootCmdFn
	originalStderr := stderrWriter
	t.Cleanup(func() {
		signalNotifyFn = originalNotify
		executeRootCmdFn = originalExecute
		stderrWriter = originalStderr
	})

	t.Run("success and signal cancels context", func(t *testing.T) {
		var stderr bytes.Buffer
		stderrWriter = &stderr
		signalNotifyFn = func(c chan<- os.Signal, sig ...os.Signal) {}
		sigChan := make(chan os.Signal, 1)
		executeRootCmdFn = func(cmd *cobra.Command) error {
			go func() {
				time.Sleep(10 * time.Millisecond)
				sigChan <- os.Interrupt
			}()
			select {
			case <-cmd.Context().Done():
				return nil
			case <-time.After(time.Second):
				t.Fatal("expected command context to be cancelled")
				return nil
			}
		}

		code := runApp(sigChan)
		assert.Equal(t, 0, code)
		assert.Contains(t, stderr.String(), "Received interrupt signal")
	})

	t.Run("execute error returns non-zero and writes stderr", func(t *testing.T) {
		var stderr bytes.Buffer
		stderrWriter = &stderr
		signalNotifyFn = func(c chan<- os.Signal, sig ...os.Signal) {}
		executeRootCmdFn = func(cmd *cobra.Command) error {
			return errors.New("boom")
		}

		code := runApp(make(chan os.Signal, 1))
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "Error: boom")
	})
}
