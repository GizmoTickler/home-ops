package workstation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
)

// NewCommand creates the workstation command
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workstation",
		Short: "Setup workstation tools and dependencies",
		Long:  `Commands for setting up workstation tools including Homebrew packages and Krew plugins`,
	}

	// Add subcommands
	cmd.AddCommand(
		newBrewCommand(),
		newKrewCommand(),
	)

	return cmd
}

// newBrewCommand creates the brew subcommand
func newBrewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "brew",
		Short: "Install Homebrew packages from Brewfile",
		Long:  `Install all packages defined in the Brewfile using Homebrew`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installBrewPackages()
		},
	}
}

// newKrewCommand creates the krew subcommand
func newKrewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "krew",
		Short: "Install kubectl plugins using Krew",
		Long:  `Install required kubectl plugins using the Krew plugin manager`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installKrewPlugins()
		},
	}
}

// installBrewPackages installs packages from Brewfile
func installBrewPackages() error {
	logger := common.NewColorLogger()
	logger.Info("Installing Homebrew packages from Brewfile...")

	// Check if Homebrew is installed
	if err := common.CheckCLI("brew"); err != nil {
		return fmt.Errorf("Homebrew is not installed. Please install Homebrew first: %w", err)
	}

	// Find Brewfile
	brewfilePath := "./Brewfile"
	if !common.FileExists(brewfilePath) {
		// Try alternative locations
		alternatives := []string{
			"../Brewfile",
			"../../Brewfile",
			"./.taskfiles/workstation/resources/Brewfile",
			"../.taskfiles/workstation/resources/Brewfile",
		}
		for _, alt := range alternatives {
			if common.FileExists(alt) {
				brewfilePath = alt
				break
			}
		}
		if !common.FileExists(brewfilePath) {
			return fmt.Errorf("Brewfile not found in current directory or common locations")
		}
	}

	logger.Info("Found Brewfile: %s", brewfilePath)

	// Get absolute path
	absPath, err := filepath.Abs(brewfilePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Run brew bundle install
	cmd := exec.Command("brew", "bundle", "install", "--file", absPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install Homebrew packages: %w", err)
	}

	logger.Success("Successfully installed Homebrew packages")
	return nil
}

// installKrewPlugins installs kubectl plugins using Krew
func installKrewPlugins() error {
	logger := common.NewColorLogger()
	logger.Info("Installing kubectl plugins using Krew...")

	// Check if kubectl is installed
	if err := common.CheckCLI("kubectl"); err != nil {
		return fmt.Errorf("kubectl is not installed. Please install kubectl first: %w", err)
	}

	// List of plugins to install (from the original Taskfile)
	plugins := []string{
		"ctx",
		"ns",
		"stern",
		"tail",
		"who-can",
	}

	// Check if krew is installed
	if !isKrewInstalled() {
		logger.Info("Krew not found, installing...")
		if err := installKrew(); err != nil {
			return fmt.Errorf("failed to install Krew: %w", err)
		}
	}

	// Update krew
	logger.Info("Updating Krew plugin index...")
	if err := runKrewCommand("update"); err != nil {
		logger.Warn("Failed to update Krew index: %v", err)
	}

	// Install each plugin
	for _, plugin := range plugins {
		logger.Info("Installing kubectl plugin: %s", plugin)
		if err := runKrewCommand("install", plugin); err != nil {
			logger.Warn("Failed to install plugin %s: %v", plugin, err)
			continue
		}
		logger.Success("✓ Installed plugin: %s", plugin)
	}

	logger.Success("Krew plugin installation completed")
	return nil
}

// isKrewInstalled checks if Krew is installed
func isKrewInstalled() bool {
	cmd := exec.Command("kubectl", "krew", "version")
	return cmd.Run() == nil
}

// installKrew installs the Krew plugin manager
func installKrew() error {
	// This is a simplified installation - in practice, you might want to
	// implement the full Krew installation script
	cmd := exec.Command("kubectl", "krew", "install", "krew")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runKrewCommand runs a kubectl krew command
func runKrewCommand(args ...string) error {
	cmdArgs := append([]string{"krew"}, args...)
	cmd := exec.Command("kubectl", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}