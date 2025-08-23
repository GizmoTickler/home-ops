package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"homeops-cli/internal/common"
)

// SSHClient represents an SSH client for TrueNAS operations
type SSHClient struct {
	host       string
	username   string
	port       string
	sshItemRef string // 1Password SSH item reference
	logger     *common.ColorLogger
}

// SSHConfig holds SSH connection configuration
type SSHConfig struct {
	Host           string
	Username       string
	Port           string
	SSHItemRef     string // 1Password SSH item reference
}

// NewSSHClient creates a new SSH client instance
func NewSSHClient(config SSHConfig) *SSHClient {
	return &SSHClient{
		host:       config.Host,
		username:   config.Username,
		port:       config.Port,
		sshItemRef: config.SSHItemRef,
		logger:     common.NewColorLogger(),
	}
}


// Connect validates the SSH connection using SSH with 1Password SSH agent
func (c *SSHClient) Connect() error {
	c.logger.Debug("Testing SSH connection to %s@%s:%s using 1Password SSH agent", c.username, c.host, c.port)
	
	// Validate configuration first
	if c.host == "" {
		return fmt.Errorf("SSH host is required")
	}
	if c.username == "" {
		return fmt.Errorf("SSH username is required")
	}
	
	// Test SSH connection using 1Password SSH agent with limited key attempts
	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "IdentitiesOnly=yes", "-o", "NumberOfPasswordPrompts=0", "-p", c.port, fmt.Sprintf("%s@%s", c.username, c.host), "echo", "connection_test")
	
	// Ensure SSH agent is available
	if sshAuthSock := os.Getenv("SSH_AUTH_SOCK"); sshAuthSock != "" {
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sshAuthSock)
	}
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to connect via SSH to %s@%s:%s: %w\nOutput: %s", c.username, c.host, c.port, err, string(output))
	}

	if !strings.Contains(string(output), "connection_test") {
		return fmt.Errorf("SSH connection test failed - expected 'connection_test' in output")
	}

	c.logger.Success("Successfully connected to TrueNAS via SSH")
	return nil
}

// Close is a no-op for SSH agent connections
func (c *SSHClient) Close() error {
	// No cleanup needed for SSH agent connections
	return nil
}

// ExecuteCommand executes a command on the remote server using SSH
func (c *SSHClient) ExecuteCommand(command string) (string, error) {
	c.logger.Debug("Executing command via SSH: %s", command)
	
	// Execute command using SSH with 1Password SSH agent
	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "IdentitiesOnly=yes", "-o", "NumberOfPasswordPrompts=0", "-p", c.port, fmt.Sprintf("%s@%s", c.username, c.host), command)
	
	// Ensure SSH agent is available
	if sshAuthSock := os.Getenv("SSH_AUTH_SOCK"); sshAuthSock != "" {
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sshAuthSock)
	}
	
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute command via SSH: %w", err)
	}

	return string(output), nil
}

// DownloadISO downloads an ISO from a URL to a specific path on TrueNAS
func (c *SSHClient) DownloadISO(isoURL, remotePath string) error {
	c.logger.Info("Downloading ISO from %s to %s", isoURL, remotePath)
	
	// Create the directory if it doesn't exist (using sudo for permissions)
	dirPath := filepath.Dir(remotePath)
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", dirPath)
	if _, err := c.ExecuteCommand(mkdirCmd); err != nil {
		c.logger.Warn("Failed to create directory (may already exist): %v", err)
	}

	// Download the ISO using wget or curl (using sudo for write permissions)
	downloadCmd := fmt.Sprintf("sudo wget -O %s %s", remotePath, isoURL)
	c.logger.Debug("Download command: %s", downloadCmd)
	
	_, err := c.ExecuteCommand(downloadCmd)
	if err != nil {
		// Try with curl as fallback (using sudo for write permissions)
		c.logger.Debug("wget failed, trying curl: %v", err)
		curlCmd := fmt.Sprintf("sudo curl -L -o %s %s", remotePath, isoURL)
		output, err := c.ExecuteCommand(curlCmd)
		if err != nil {
			return fmt.Errorf("failed to download ISO with both wget and curl: %w\nOutput: %s", err, output)
		}
	}

	c.logger.Success("ISO downloaded successfully to %s", remotePath)
	return nil
}

// VerifyFile checks if a file exists and optionally gets its size
func (c *SSHClient) VerifyFile(remotePath string) (bool, int64, error) {
	c.logger.Debug("Verifying file: %s", remotePath)
	
	// Check if file exists and get its size
	statCmd := fmt.Sprintf("stat -c '%%s' %s 2>/dev/null || echo 'FILE_NOT_FOUND'", remotePath)
	output, err := c.ExecuteCommand(statCmd)
	if err != nil {
		return false, 0, fmt.Errorf("failed to check file: %w", err)
	}

	output = strings.TrimSpace(output)
	if output == "FILE_NOT_FOUND" {
		return false, 0, nil
	}

	// Parse file size
	var size int64
	if _, err := fmt.Sscanf(output, "%d", &size); err != nil {
		return true, 0, fmt.Errorf("failed to parse file size: %w", err)
	}

	c.logger.Debug("File exists with size: %d bytes", size)
	return true, size, nil
}

// RemoveFile removes a file from the remote server
func (c *SSHClient) RemoveFile(remotePath string) error {
	c.logger.Debug("Removing file: %s", remotePath)
	
	removeCmd := fmt.Sprintf("sudo rm -f %s", remotePath)
	_, err := c.ExecuteCommand(removeCmd)
	if err != nil {
		return fmt.Errorf("failed to remove file: %w", err)
	}

	c.logger.Debug("File removed successfully")
	return nil
}