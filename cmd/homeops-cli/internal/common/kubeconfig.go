package common

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// 1Password location for kubeconfig
	KubeconfigVault = "Infrastructure"
	KubeconfigItem  = "kubeconfig"
	KubeconfigField = "kubeconfig"
)

// SaveKubeconfigTo1Password saves kubeconfig content to 1Password as a file attachment
func SaveKubeconfigTo1Password(kubeconfigContent []byte, logger *ColorLogger) error {
	logger.Debug("Updating kubeconfig file in 1Password...")

	// Create a temporary file with the kubeconfig content
	tmpFile, err := os.CreateTemp("", "kubeconfig-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temporary kubeconfig file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	if _, err := tmpFile.Write(kubeconfigContent); err != nil {
		return fmt.Errorf("failed to write kubeconfig to temporary file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Update the existing kubeconfig item by replacing the file attachment
	cmd := exec.Command("op", "item", "edit", KubeconfigItem, "--vault", KubeconfigVault,
		fmt.Sprintf("%s[file]=%s", KubeconfigField, tmpFile.Name()))
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to update kubeconfig file in 1Password: %w (output: %s)", err, string(output))
	}

	logger.Debug("Kubeconfig file updated in 1Password")
	return nil
}

// PullKubeconfigFrom1Password retrieves kubeconfig from 1Password and saves to the specified path
func PullKubeconfigFrom1Password(destPath string, logger *ColorLogger) error {
	logger.Debug("Pulling kubeconfig from 1Password...")

	// Ensure destination directory exists
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Use op read to get the file attachment content
	ref := fmt.Sprintf("op://%s/%s/%s", KubeconfigVault, KubeconfigItem, KubeconfigField)
	cmd := exec.Command("op", "read", ref, "--out-file", destPath)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to pull kubeconfig from 1Password: %w (output: %s)", err, string(output))
	}

	// Set proper permissions (600 for kubeconfig)
	if err := os.Chmod(destPath, 0600); err != nil {
		logger.Warn("Failed to set kubeconfig permissions: %v", err)
	}

	logger.Debug("Kubeconfig pulled from 1Password to %s", destPath)
	return nil
}

// PushKubeconfigTo1Password reads kubeconfig from path and saves to 1Password
func PushKubeconfigTo1Password(sourcePath string, logger *ColorLogger) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig file: %w", err)
	}

	return SaveKubeconfigTo1Password(content, logger)
}
