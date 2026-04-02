package workstation

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/ui"
)

var (
	httpGetFunc         = http.Get
	krewDownloadBaseURL = "https://github.com/kubernetes-sigs/krew/releases/latest/download"
	runtimeGOOS         = runtime.GOOS
	runtimeGOARCH       = runtime.GOARCH
	checkCLIFunc        = common.CheckCLI
	getBrewfileFunc     = templates.GetBrewfile
	combinedOutputFunc  = common.CombinedOutput
	runInteractiveFunc  = common.RunInteractive
	spinWithFunc        = ui.SpinWithFunc
	isKrewInstalledFunc = isKrewInstalled
	installKrewFunc     = installKrew
	runKrewCommandFunc  = runKrewCommand
	listKrewPluginsFunc = listInstalledKrewPlugins
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

// installBrewPackages installs packages from embedded Brewfile
func installBrewPackages() error {
	logger := common.NewColorLogger()
	logger.Info("Installing Homebrew packages from Brewfile...")

	// Check if Homebrew is installed
	if err := checkCLIFunc("brew"); err != nil {
		return fmt.Errorf("homebrew is not installed. Please install Homebrew first: %w", err)
	}

	// Get Brewfile content from embedded templates
	brewfileContent, err := getBrewfileFunc()
	if err != nil {
		return fmt.Errorf("failed to get embedded Brewfile: %w", err)
	}

	// Create temporary Brewfile
	tempFile, err := os.CreateTemp("", "Brewfile")
	if err != nil {
		return fmt.Errorf("failed to create temporary Brewfile: %w", err)
	}
	defer func() {
		if closeErr := tempFile.Close(); closeErr != nil {
			logger.Warn("Failed to close temp file: %v", closeErr)
		}
		if removeErr := os.Remove(tempFile.Name()); removeErr != nil {
			logger.Warn("Failed to remove temp file: %v", removeErr)
		}
	}()

	// Write Brewfile content to temporary file
	if _, err := tempFile.WriteString(brewfileContent); err != nil {
		return fmt.Errorf("failed to write Brewfile content: %w", err)
	}

	logger.Info("Using embedded Brewfile")

	// Short-circuit when the Brewfile is already satisfied.
	checkOutput, checkErr := combinedOutputFunc("brew", "bundle", "check", "--file", tempFile.Name())
	if checkErr == nil {
		logger.Success("Homebrew packages already match the embedded Brewfile")
		return nil
	}
	logger.Debug("brew bundle check reported changes needed: %s", strings.TrimSpace(string(checkOutput)))

	// Run brew bundle install with spinner
	err = spinWithFunc("📦 Installing Homebrew packages", func() error {
		if err := runInteractiveFunc(nil, os.Stdout, os.Stderr, "brew", "bundle", "install", "--file", tempFile.Name()); err != nil {
			return fmt.Errorf("failed to install Homebrew packages: %w", err)
		}
		return nil
	})

	if err != nil {
		return err
	}

	logger.Success("Successfully installed Homebrew packages")
	return nil
}

// installKrewPlugins installs kubectl plugins using Krew
func installKrewPlugins() error {
	logger := common.NewColorLogger()
	logger.Info("Installing kubectl plugins using Krew...")

	// Check if kubectl is installed
	if err := checkCLIFunc("kubectl"); err != nil {
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
	if !isKrewInstalledFunc() {
		logger.Info("Krew not found, installing...")
		err := spinWithFunc("🔧 Installing Krew", func() error {
			return installKrewFunc()
		})
		if err != nil {
			return fmt.Errorf("failed to install Krew: %w", err)
		}
		logger.Success("Successfully installed Krew")
		logger.Info("Ensure kubectl krew is on your PATH if this is a fresh install: export PATH=\"$HOME/.krew/bin:$PATH\"")
	}

	// Update krew
	err := spinWithFunc("🔄 Updating Krew plugin index", func() error {
		return runKrewCommandFunc("update")
	})
	if err != nil {
		logger.Warn("Failed to update Krew index: %v", err)
	}

	installedPlugins, err := listKrewPluginsFunc()
	if err != nil {
		logger.Warn("Failed to read installed Krew plugins: %v", err)
		installedPlugins = nil
	}

	// Install each plugin
	for _, plugin := range plugins {
		if slices.Contains(installedPlugins, plugin) {
			logger.Info("Plugin already installed: %s", plugin)
			continue
		}

		err := spinWithFunc(fmt.Sprintf("  Installing plugin: %s", plugin), func() error {
			return runKrewCommandFunc("install", plugin)
		})
		if err != nil {
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
	cmd := common.Command("kubectl", "krew", "version")
	return cmd.Run() == nil
}

// installKrew installs the Krew plugin manager using the official installation method
func installKrew() error {
	tempDir, err := os.MkdirTemp("", "homeops-krew-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory for krew: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	krewName, archiveURL, err := krewDownloadInfo()
	if err != nil {
		return err
	}

	archivePath := filepath.Join(tempDir, krewName+".tar.gz")
	if err := downloadFile(archiveURL, archivePath); err != nil {
		return fmt.Errorf("failed to download krew archive: %w", err)
	}

	if err := extractTarGz(archivePath, tempDir); err != nil {
		return fmt.Errorf("failed to extract krew archive: %w", err)
	}

	binaryPath, err := findExecutable(tempDir, krewName)
	if err != nil {
		return err
	}

	cmd := common.Command(binaryPath, "install", "krew")
	cmd.Dir = tempDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runKrewCommand runs a kubectl krew command
func runKrewCommand(args ...string) error {
	cmdArgs := append([]string{"krew"}, args...)
	return common.RunInteractive(nil, os.Stdout, os.Stderr, "kubectl", cmdArgs...)
}

func listInstalledKrewPlugins() ([]string, error) {
	output, err := common.Output("kubectl", "krew", "list")
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(output), "\n")
	plugins := make([]string, 0, len(lines))
	for _, line := range lines {
		plugin := strings.TrimSpace(line)
		if plugin == "" || strings.EqualFold(plugin, "PLUGIN") {
			continue
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

func krewDownloadInfo() (string, string, error) {
	goos, err := normalizeKrewOS(runtimeGOOS)
	if err != nil {
		return "", "", err
	}
	goarch, err := normalizeKrewArch(runtimeGOARCH)
	if err != nil {
		return "", "", err
	}

	name := fmt.Sprintf("krew-%s_%s", goos, goarch)
	url := fmt.Sprintf("%s/%s.tar.gz", krewDownloadBaseURL, name)
	return name, url, nil
}

func normalizeKrewOS(goos string) (string, error) {
	switch goos {
	case "darwin", "linux", "windows":
		return goos, nil
	default:
		return "", fmt.Errorf("unsupported operating system for Krew: %s", goos)
	}
}

func normalizeKrewArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64":
		return goarch, nil
	case "386":
		return "x86", nil
	default:
		return "", fmt.Errorf("unsupported architecture for Krew: %s", goarch)
	}
}

func downloadFile(url, destination string) error {
	resp, err := httpGetFunc(url)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}

	return nil
}

func extractTarGz(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() {
		_ = gzr.Close()
	}()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(destination, header.Name)
		cleanDest := filepath.Clean(destination) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(targetPath), cleanDest) && filepath.Clean(targetPath) != filepath.Clean(destination) {
			return fmt.Errorf("archive entry escapes destination: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			fileMode := os.FileMode(header.Mode)
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func findExecutable(rootDir, executableName string) (string, error) {
	var matches []string
	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == executableName || d.Name() == executableName+".exe" {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("krew executable %s not found after extraction", executableName)
	}
	slices.Sort(matches)
	return matches[0], nil
}
