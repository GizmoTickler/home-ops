package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
)

const defaultSSHCommandTimeout = 2 * time.Minute

// connectRetrySleep is the backoff used between SSH connect probes (injectable in
// tests so they don't actually sleep).
var connectRetrySleep = time.Sleep

var runCommand = common.RunCommand

// SSHClient executes commands on a remote host via the system ssh binary.
// Authentication is delegated entirely to the ambient ssh-agent (a standard
// agent or the 1Password agent — the client doesn't care which).
type SSHClient struct {
	host     string
	username string
	port     string
	logger   *common.ColorLogger
}

// SSHConfig holds SSH connection configuration
type SSHConfig struct {
	Host     string
	Username string
	Port     string
}

// NewSSHClient creates a new SSH client instance
func NewSSHClient(config SSHConfig) *SSHClient {
	return &SSHClient{
		host:     config.Host,
		username: config.Username,
		port:     config.Port,
		logger:   common.NewColorLogger(),
	}
}

// Connect validates the SSH connection using the ambient ssh-agent
func (c *SSHClient) Connect() error {
	c.logger.Debug("Testing SSH connection to %s@%s:%s via ssh-agent", c.username, c.host, c.port)

	// Validate configuration first
	if c.host == "" {
		return fmt.Errorf("SSH host is required")
	}
	if c.username == "" {
		return fmt.Errorf("SSH username is required")
	}

	// Probe with an idempotent command, retrying transient failures: right after a
	// VM boots, sshd may not be listening yet (connection refused / timed out). ssh
	// reports that in stderr while the error itself is a generic "exit status 255",
	// so classify retryability on the command OUTPUT. ExecuteCommand is deliberately
	// NOT retried — it may run mutating commands that must not run twice.
	var result common.CommandResult
	err := common.Retry(common.RetryConfig{
		Attempts:  5,
		BaseDelay: 2 * time.Second,
		MaxDelay:  10 * time.Second,
		Sleep:     connectRetrySleep,
		Retryable: func(error) bool {
			return common.IsRetryableError(errors.New(result.Stdout + result.Stderr))
		},
	}, func() error {
		var e error
		result, e = c.runSSHCommand("echo", "connection_test")
		return e
	})
	if err != nil {
		return fmt.Errorf("failed to connect via SSH to %s@%s:%s: %w\nOutput: %s", c.username, c.host, c.port, err, combinedCommandOutput(result))
	}

	if !strings.Contains(result.Stdout+result.Stderr, "connection_test") {
		return fmt.Errorf("SSH connection test failed - expected 'connection_test' in output")
	}

	c.logger.Success("Successfully connected to %s via SSH", c.host)
	return nil
}

// Close is a no-op for SSH agent connections
func (c *SSHClient) Close() error {
	// No cleanup needed for SSH agent connections
	return nil
}

// ExecuteCommand executes a command on the remote server using SSH
func (c *SSHClient) ExecuteCommand(command string) (string, error) {
	// NOTE: never log the command body — Flatcar bootstrap routes kubeadm join
	// material (token/cert-key/CA hash) and base64 cluster PKI private keys
	// through here; logging the string would leak them at debug level.
	c.logger.Debug("Executing SSH command (%d bytes)", len(command))

	result, err := c.runSSHCommand(command)
	if err != nil {
		return "", fmt.Errorf("failed to execute command via SSH: %w\nOutput: %s", err, combinedCommandOutput(result))
	}

	return result.Stdout, nil
}

func (c *SSHClient) runSSHCommand(remoteArgs ...string) (common.CommandResult, error) {
	return runCommand(context.Background(), common.CommandOptions{
		Name:    "ssh",
		Args:    append(c.sshArgs(), remoteArgs...),
		Timeout: defaultSSHCommandTimeout,
	})
}

// UploadBytes streams content to remotePath on the remote host via ssh stdin
// (sudo tee), so binary payloads never touch argv or a temp file.
func (c *SSHClient) UploadBytes(content []byte, remotePath string) error {
	c.logger.Debug("Uploading %d bytes to %s", len(content), remotePath)
	mkdir := fmt.Sprintf("sudo mkdir -p %s", common.ShellQuote(filepath.Dir(remotePath)))
	if _, err := c.ExecuteCommand(mkdir); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", remotePath, err)
	}
	command := fmt.Sprintf("sudo tee %s > /dev/null", common.ShellQuote(remotePath))
	result, err := runCommand(context.Background(), common.CommandOptions{
		Name:    "ssh",
		Args:    append(c.sshArgs(), command),
		Timeout: defaultSSHCommandTimeout,
		Stdin:   bytes.NewReader(content),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to %s: %w\nOutput: %s", remotePath, err, combinedCommandOutput(result))
	}
	return nil
}

func (c *SSHClient) sshArgs() []string {
	return []string{
		// Trust-on-first-use: record the host key on first connect, then
		// refuse to connect if it ever changes (MITM protection).
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"-o", "NumberOfPasswordPrompts=0",
		"-p", c.port,
		fmt.Sprintf("%s@%s", c.username, c.host),
	}
}

func combinedCommandOutput(result common.CommandResult) string {
	return common.RedactCommandOutput(strings.TrimSpace(result.Stdout + result.Stderr))
}

// DownloadISO downloads an ISO from a URL to a specific path on TrueNAS
func (c *SSHClient) DownloadISO(isoURL, remotePath string) error {
	c.logger.Info("Downloading ISO from %s to %s", isoURL, remotePath)

	// Create the directory if it doesn't exist (using sudo for permissions).
	// remotePath/isoURL are single-quoted: they may contain spaces or shell
	// metacharacters, and an unquoted interpolation here is a command-injection
	// (and wrong-file) vector.
	dirPath := filepath.Dir(remotePath)
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", common.ShellQuote(dirPath))
	if _, err := c.ExecuteCommand(mkdirCmd); err != nil {
		c.logger.Warn("Failed to create directory (may already exist): %v", err)
	}

	// Download the ISO using wget or curl (using sudo for write permissions)
	downloadCmd := fmt.Sprintf("sudo wget -O %s %s", common.ShellQuote(remotePath), common.ShellQuote(isoURL))
	c.logger.Debug("Download command: %s", downloadCmd)

	_, err := c.ExecuteCommand(downloadCmd)
	if err != nil {
		// Try with curl as fallback (using sudo for write permissions)
		c.logger.Debug("wget failed, trying curl: %v", err)
		curlCmd := fmt.Sprintf("sudo curl -L -o %s %s", common.ShellQuote(remotePath), common.ShellQuote(isoURL))
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

	// Check if file exists and get its size. remotePath is single-quoted to
	// neutralize spaces and shell metacharacters.
	statCmd := fmt.Sprintf("stat -c '%%s' %s 2>/dev/null || echo 'FILE_NOT_FOUND'", common.ShellQuote(remotePath))
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

	// remotePath is single-quoted so a value containing whitespace or shell
	// metacharacters cannot expand into "rm -f" of the wrong target.
	removeCmd := fmt.Sprintf("sudo rm -f %s", common.ShellQuote(remotePath))
	_, err := c.ExecuteCommand(removeCmd)
	if err != nil {
		return fmt.Errorf("failed to remove file: %w", err)
	}

	c.logger.Debug("File removed successfully")
	return nil
}
