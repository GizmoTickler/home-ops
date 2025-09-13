package onepassword

import (
	"fmt"
	"os"
	"os/exec"

	"go.uber.org/zap"
)

// SaveKubeconfig saves the kubeconfig content to the existing 1Password item as a file attachment
func SaveKubeconfig(kubeconfigContent []byte, log *zap.SugaredLogger) error {
	log.Debug("Updating kubeconfig file in 1Password...")

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
	cmd := exec.Command("op", "item", "edit", "kubeconfig", "--vault", "Private", fmt.Sprintf("kubeconfig[file]=%s", tmpFile.Name()))
	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to update kubeconfig file in 1Password: %w (output: %s)", err, string(output))
	}

	log.Debug("Kubeconfig file updated in 1Password")
	return nil
}
