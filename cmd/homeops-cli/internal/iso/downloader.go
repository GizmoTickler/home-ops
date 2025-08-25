package iso

import (
	"fmt"
	"path/filepath"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/ssh"
)

// DownloadConfig holds configuration for ISO download
type DownloadConfig struct {
	// TrueNAS SSH connection details
	TrueNASHost     string
	TrueNASUsername string
	TrueNASPort     string
	SSHItemRef      string // 1Password SSH item reference for op CLI

	// ISO details
	ISOURL         string
	ISOStoragePath string // Path on TrueNAS where ISO should be stored
	ISOFilename    string // Filename for the ISO (e.g., "metal-amd64.iso")
}

// Downloader handles ISO download operations
type Downloader struct {
	logger *common.ColorLogger
}

// NewDownloader creates a new ISO downloader
func NewDownloader() *Downloader {
	return &Downloader{
		logger: common.NewColorLogger(),
	}
}

// DownloadCustomISO downloads a custom ISO to TrueNAS storage
func (d *Downloader) DownloadCustomISO(config DownloadConfig) error {
	d.logger.Info("Starting custom ISO download to TrueNAS")
	d.logger.Debug("Config: Host=%s, Username=%s, Port=%s, URL=%s, Path=%s",
		config.TrueNASHost, config.TrueNASUsername, config.TrueNASPort,
		config.ISOURL, config.ISOStoragePath)

	// Validate configuration
	if err := d.validateConfig(config); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Create SSH client
	sshConfig := ssh.SSHConfig{
		Host:       config.TrueNASHost,
		Username:   config.TrueNASUsername,
		Port:       config.TrueNASPort,
		SSHItemRef: config.SSHItemRef,
	}

	sshClient := ssh.NewSSHClient(sshConfig)

	// Connect to TrueNAS
	d.logger.Debug("Connecting to TrueNAS via op SSH")
	if err := sshClient.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := sshClient.Close(); closeErr != nil {
			d.logger.Warn("Failed to close SSH connection: %v", closeErr)
		}
	}()

	// Construct full ISO path
	fullISOPath := filepath.Join(config.ISOStoragePath, config.ISOFilename)
	d.logger.Debug("Full ISO path: %s", fullISOPath)

	// Check if ISO already exists and remove it
	exists, size, err := sshClient.VerifyFile(fullISOPath)
	if err != nil {
		d.logger.Warn("Failed to check existing ISO file: %v", err)
	} else if exists {
		d.logger.Info("Existing ISO found (size: %d bytes), removing it", size)
		if err := sshClient.RemoveFile(fullISOPath); err != nil {
			d.logger.Warn("Failed to remove existing ISO: %v", err)
		}
	}

	// Download the new ISO
	d.logger.Info("Downloading ISO from %s", config.ISOURL)
	if err := sshClient.DownloadISO(config.ISOURL, fullISOPath); err != nil {
		return fmt.Errorf("failed to download ISO: %w", err)
	}

	// Verify the downloaded ISO
	exists, size, err = sshClient.VerifyFile(fullISOPath)
	if err != nil {
		return fmt.Errorf("failed to verify downloaded ISO: %w", err)
	}
	if !exists {
		return fmt.Errorf("ISO file not found after download")
	}
	if size == 0 {
		return fmt.Errorf("downloaded ISO file is empty")
	}

	d.logger.Success("Custom ISO downloaded successfully to %s (size: %d bytes)", fullISOPath, size)
	return nil
}

// validateConfig validates the download configuration
func (d *Downloader) validateConfig(config DownloadConfig) error {
	if config.TrueNASHost == "" {
		return fmt.Errorf("TrueNAS host is required")
	}
	if config.TrueNASUsername == "" {
		return fmt.Errorf("TrueNAS username is required")
	}
	if config.TrueNASPort == "" {
		return fmt.Errorf("TrueNAS SSH port is required")
	}
	if config.SSHItemRef == "" {
		return fmt.Errorf("SSH item reference is required")
	}
	if config.ISOURL == "" {
		return fmt.Errorf("ISO URL is required")
	}
	if config.ISOStoragePath == "" {
		return fmt.Errorf("ISO storage path is required")
	}
	if config.ISOFilename == "" {
		return fmt.Errorf("ISO filename is required")
	}

	// Validate URL format
	if !strings.HasPrefix(config.ISOURL, "http://") && !strings.HasPrefix(config.ISOURL, "https://") {
		return fmt.Errorf("ISO URL must start with http:// or https://")
	}

	// Validate filename
	if !strings.HasSuffix(config.ISOFilename, ".iso") {
		return fmt.Errorf("ISO filename must end with .iso")
	}

	return nil
}

// GetDefaultConfig returns a default configuration for ISO download
func GetDefaultConfig() DownloadConfig {
	return DownloadConfig{
		TrueNASHost:     "op://Infrastructure/talosdeploy/TRUENAS_HOST",
		TrueNASUsername: "op://Infrastructure/talosdeploy/TRUENAS_USERNAME",
		TrueNASPort:     "22",
		ISOStoragePath:  "/mnt/flashstor/ISO",
		ISOFilename:     "metal-amd64.iso",
		SSHItemRef:      "op://Infrastructure/NAS01/private key",
	}
}
