package common

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// filterConnectEnvVars removes OP_CONNECT_HOST and OP_CONNECT_TOKEN from env vars
// because "op item edit" doesn't work with 1Password Connect
func filterConnectEnvVars(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "OP_CONNECT_HOST=") && !strings.HasPrefix(e, "OP_CONNECT_TOKEN=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

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

	// Get filtered env vars (op item edit doesn't work with Connect)
	filteredEnv := filterConnectEnvVars(os.Environ())

	// First, delete any existing kubeconfig file attachments to avoid duplicates
	// We use "delete" to remove the field, then re-add it with the new file
	deleteCmd := exec.Command("op", "item", "edit", KubeconfigItem, "--vault", KubeconfigVault,
		fmt.Sprintf("%s[delete]", KubeconfigField))
	deleteCmd.Env = filteredEnv
	// Ignore errors - field might not exist
	_ = deleteCmd.Run()

	// Add the new kubeconfig file attachment
	cmd := exec.Command("op", "item", "edit", KubeconfigItem, "--vault", KubeconfigVault,
		fmt.Sprintf("%s[file]=%s", KubeconfigField, tmpFile.Name()))
	cmd.Env = filteredEnv
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

	// Get the file ID to handle potential duplicates
	fileID, err := getKubeconfigFileID()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig file ID: %w", err)
	}

	// Use op read with the file ID to avoid ambiguity
	ref := fmt.Sprintf("op://%s/%s/%s", KubeconfigVault, KubeconfigItem, fileID)
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

// getKubeconfigFileID retrieves the file ID for the kubeconfig attachment
func getKubeconfigFileID() (string, error) {
	cmd := exec.Command("op", "item", "get", KubeconfigItem, "--vault", KubeconfigVault, "--format=json")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get item: %w", err)
	}

	var item struct {
		Files []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(output, &item); err != nil {
		return "", fmt.Errorf("failed to parse item: %w", err)
	}

	// Find the first file named "kubeconfig"
	for _, f := range item.Files {
		if f.Name == KubeconfigField {
			return f.ID, nil
		}
	}

	return "", fmt.Errorf("no kubeconfig file found in item")
}

// PushKubeconfigTo1Password reads kubeconfig from path and saves to 1Password
func PushKubeconfigTo1Password(sourcePath string, logger *ColorLogger) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig file: %w", err)
	}

	return SaveKubeconfigTo1Password(content, logger)
}
