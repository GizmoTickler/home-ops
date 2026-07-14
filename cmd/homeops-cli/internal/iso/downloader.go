package iso

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
)

const checksumFetchTimeout = 20 * time.Second

var sha512HexRe = regexp.MustCompile(`(?i)\b([0-9a-f]{128})\b`)

// DownloadConfig holds configuration for ISO download
type DownloadConfig struct {
	// TrueNAS SSH connection details (auth is the ambient ssh-agent)
	TrueNASHost     string
	TrueNASUsername string
	TrueNASPort     string

	// ISO details
	ISOURL         string
	ISOStoragePath string // Path on TrueNAS where ISO should be stored
	ISOFilename    string // Filename for the ISO (e.g., "metal-amd64.iso")
}

// Downloader handles ISO download operations
type Downloader struct {
	logger *common.ColorLogger
}

type sshClient interface {
	Connect() error
	Close() error
	ExecuteCommand(string) (string, error)
	VerifyFile(string) (bool, int64, error)
	RemoveFile(string) error
	DownloadISO(string, string) error
}

var newSSHClient = func(config ssh.SSHConfig) sshClient {
	return ssh.NewSSHClient(config)
}

var fetchChecksumFileFn = fetchChecksumFile

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
		Host:     config.TrueNASHost,
		Username: config.TrueNASUsername,
		Port:     config.TrueNASPort,
	}

	sshClient := newSSHClient(sshConfig)

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
	if err := d.verifyChecksumIfAvailable(config.ISOURL, fullISOPath, sshClient); err != nil {
		return err
	}

	d.logger.Success("Custom ISO downloaded successfully to %s (size: %d bytes)", fullISOPath, size)
	return nil
}

func (d *Downloader) verifyChecksumIfAvailable(isoURL, remotePath string, sshClient sshClient) error {
	checksumURL, reason, ok := checksumURLForISO(isoURL)
	if !ok {
		d.logger.Warn("No vendor checksum verification for ISO %s: %s", isoURL, reason)
		return nil
	}
	doc, err := fetchChecksumFileFn(checksumURL)
	if err != nil {
		d.logger.Warn("Unable to fetch vendor checksum %s: %v; ISO integrity not verified", checksumURL, err)
		return nil
	}
	expected, err := parseSHA512Checksum(doc)
	if err != nil {
		d.logger.Warn("Unable to parse vendor checksum %s: %v; ISO integrity not verified", checksumURL, err)
		return nil
	}
	actual, err := remoteSHA512(sshClient, remotePath)
	if err != nil {
		return fmt.Errorf("verify ISO SHA512 checksum for %s: %w", remotePath, err)
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("SHA512 checksum mismatch for %s", remotePath)
	}
	d.logger.Success("Verified ISO SHA512 checksum using %s", checksumURL)
	return nil
}

func checksumURLForISO(isoURL string) (checksumURL, reason string, ok bool) {
	switch {
	case strings.Contains(isoURL, "release.flatcar-linux.net/") && strings.HasSuffix(isoURL, ".iso"):
		return isoURL + ".sha512", "", true
	case strings.Contains(isoURL, "factory.talos.dev/"):
		// Talos factory URLs are generated from a schematic ID; that ID is the
		// content-address of the submitted schematic, so the factory image is tied
		// to the requested machine definition even though this endpoint does not
		// publish a sibling checksum file for the ISO URL we consume here.
		return "", "Talos factory image URL is schematic-addressed and has no sibling checksum file", false
	default:
		return "", "no known vendor checksum URL pattern", false
	}
}

func parseSHA512Checksum(doc []byte) (string, error) {
	m := sha512HexRe.FindSubmatch(doc)
	if len(m) != 2 {
		return "", fmt.Errorf("no SHA512 hex digest found")
	}
	return string(m[1]), nil
}

func remoteSHA512(sshClient sshClient, remotePath string) (string, error) {
	cmd := fmt.Sprintf("sha512sum %s | awk '{print $1}'", common.ShellQuote(remotePath))
	out, err := sshClient.ExecuteCommand(cmd)
	if err != nil {
		return "", err
	}
	if m := sha512HexRe.FindString(out); m != "" {
		return m, nil
	}
	return "", fmt.Errorf("remote sha512sum output did not contain a SHA512 digest")
}

func fetchChecksumFile(checksumURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checksumFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
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
	if config.ISOURL == "" {
		return fmt.Errorf("ISO URL is required")
	}
	if config.ISOStoragePath == "" {
		return fmt.Errorf("ISO storage path is required")
	}
	if config.ISOFilename == "" {
		return fmt.Errorf("ISO filename is required")
	}

	// Require HTTPS — an ISO is booted as a node image, so an unencrypted fetch is
	// a tamper vector. (Talos Factory + Flatcar release URLs are HTTPS.)
	if !strings.HasPrefix(config.ISOURL, "https://") {
		return fmt.Errorf("ISO URL must start with https:// (got %q)", config.ISOURL)
	}

	// Validate filename
	if !strings.HasSuffix(config.ISOFilename, ".iso") {
		return fmt.Errorf("ISO filename must end with .iso")
	}

	return nil
}

// GetDefaultConfig returns a default configuration for ISO download with the
// TrueNAS connection details resolved through the configured secret
// references and the ISO location taken from the hypervisors.truenas config.
func GetDefaultConfig() DownloadConfig {
	cfg := config.Get()
	return DownloadConfig{
		TrueNASHost:     cfg.ResolveSecretSilent(config.KeyTrueNASHost),
		TrueNASUsername: cfg.ResolveSecretSilent(config.KeyTrueNASUsername),
		TrueNASPort:     "22",
		ISOStoragePath:  cfg.Hypervisors.TrueNAS.ISODir,
		ISOFilename:     cfg.Hypervisors.TrueNAS.ISOFile,
	}
}
