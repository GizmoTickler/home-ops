package iso

import (
	"errors"
	"path/filepath"
	"testing"

	"homeops-cli/internal/constants"
	"homeops-cli/internal/ssh"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSSHClient struct {
	connectErr    error
	verifyResults []struct {
		exists bool
		size   int64
		err    error
	}
	removeErr     error
	downloadErr   error
	verifyCalls   []string
	removeCalls   []string
	downloadCalls [][2]string
	connectCalls  int
	closeCalls    int
}

func (f *fakeSSHClient) Connect() error {
	f.connectCalls++
	return f.connectErr
}

func (f *fakeSSHClient) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeSSHClient) VerifyFile(path string) (bool, int64, error) {
	f.verifyCalls = append(f.verifyCalls, path)
	if len(f.verifyResults) == 0 {
		return false, 0, nil
	}
	result := f.verifyResults[0]
	f.verifyResults = f.verifyResults[1:]
	return result.exists, result.size, result.err
}

func (f *fakeSSHClient) RemoveFile(path string) error {
	f.removeCalls = append(f.removeCalls, path)
	return f.removeErr
}

func (f *fakeSSHClient) DownloadISO(url, path string) error {
	f.downloadCalls = append(f.downloadCalls, [2]string{url, path})
	return f.downloadErr
}

func TestGetDefaultConfig(t *testing.T) {
	config := GetDefaultConfig()

	assert.Equal(t, constants.OpTrueNASHost, config.TrueNASHost)
	assert.Equal(t, constants.OpTrueNASUsername, config.TrueNASUsername)
	assert.Equal(t, "22", config.TrueNASPort)
	assert.Equal(t, "/mnt/flashstor/ISO", config.ISOStoragePath)
	assert.Equal(t, "metal-amd64.iso", config.ISOFilename)
	assert.Equal(t, constants.OpTrueNASSSHPrivateKey, config.SSHItemRef)
}

func TestNewDownloader(t *testing.T) {
	downloader := NewDownloader()
	assert.NotNil(t, downloader)
	assert.NotNil(t, downloader.logger)
}

func TestDownloaderValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  DownloadConfig
		wantErr string
	}{
		{
			name: "valid config",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "https://example.com/test.iso",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.iso",
			},
		},
		{
			name: "missing TrueNAS host",
			config: DownloadConfig{
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "https://example.com/test.iso",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.iso",
			},
			wantErr: "TrueNAS host is required",
		},
		{
			name: "missing username",
			config: DownloadConfig{
				TrueNASHost:    "192.168.1.100",
				TrueNASPort:    "22",
				SSHItemRef:     "op://vault/truenas/ssh",
				ISOURL:         "https://example.com/test.iso",
				ISOStoragePath: "/mnt/tank/isos",
				ISOFilename:    "test.iso",
			},
			wantErr: "TrueNAS username is required",
		},
		{
			name: "missing SSH item reference",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				ISOURL:          "https://example.com/test.iso",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.iso",
			},
			wantErr: "SSH item reference is required",
		},
		{
			name: "missing ISO URL",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.iso",
			},
			wantErr: "ISO URL is required",
		},
		{
			name: "invalid URL format",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "not-a-valid-url",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.iso",
			},
			wantErr: "ISO URL must start with http:// or https://",
		},
		{
			name: "invalid filename suffix",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "https://example.com/test.img",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.img",
			},
			wantErr: "ISO filename must end with .iso",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewDownloader().validateConfig(tt.config)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDownloaderDownloadCustomISO(t *testing.T) {
	baseConfig := DownloadConfig{
		TrueNASHost:     "nas.local",
		TrueNASUsername: "root",
		TrueNASPort:     "22",
		SSHItemRef:      "op://vault/truenas/ssh",
		ISOURL:          "https://example.com/test.iso",
		ISOStoragePath:  "/mnt/tank/isos",
		ISOFilename:     "test.iso",
	}

	t.Run("successful download removes existing file first", func(t *testing.T) {
		fake := &fakeSSHClient{
			verifyResults: []struct {
				exists bool
				size   int64
				err    error
			}{
				{exists: true, size: 64, err: nil},
				{exists: true, size: 1024, err: nil},
			},
		}

		oldNewSSHClient := newSSHClient
		t.Cleanup(func() { newSSHClient = oldNewSSHClient })
		newSSHClient = func(config ssh.SSHConfig) sshClient {
			assert.Equal(t, "nas.local", config.Host)
			assert.Equal(t, "root", config.Username)
			assert.Equal(t, "22", config.Port)
			assert.Equal(t, "op://vault/truenas/ssh", config.SSHItemRef)
			return fake
		}

		err := NewDownloader().DownloadCustomISO(baseConfig)
		require.NoError(t, err)
		fullPath := filepath.Join(baseConfig.ISOStoragePath, baseConfig.ISOFilename)
		assert.Equal(t, []string{fullPath, fullPath}, fake.verifyCalls)
		assert.Equal(t, []string{fullPath}, fake.removeCalls)
		assert.Equal(t, [][2]string{{baseConfig.ISOURL, fullPath}}, fake.downloadCalls)
		assert.Equal(t, 1, fake.connectCalls)
		assert.Equal(t, 1, fake.closeCalls)
	})

	t.Run("connect failure", func(t *testing.T) {
		fake := &fakeSSHClient{connectErr: errors.New("ssh unavailable")}
		oldNewSSHClient := newSSHClient
		t.Cleanup(func() { newSSHClient = oldNewSSHClient })
		newSSHClient = func(config ssh.SSHConfig) sshClient { return fake }

		err := NewDownloader().DownloadCustomISO(baseConfig)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to connect to TrueNAS")
	})

	t.Run("download failure", func(t *testing.T) {
		fake := &fakeSSHClient{
			verifyResults: []struct {
				exists bool
				size   int64
				err    error
			}{
				{exists: false, size: 0, err: nil},
			},
			downloadErr: errors.New("wget failed"),
		}
		oldNewSSHClient := newSSHClient
		t.Cleanup(func() { newSSHClient = oldNewSSHClient })
		newSSHClient = func(config ssh.SSHConfig) sshClient { return fake }

		err := NewDownloader().DownloadCustomISO(baseConfig)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to download ISO")
	})

	t.Run("verify after download returns missing file", func(t *testing.T) {
		fake := &fakeSSHClient{
			verifyResults: []struct {
				exists bool
				size   int64
				err    error
			}{
				{exists: false, size: 0, err: nil},
				{exists: false, size: 0, err: nil},
			},
		}
		oldNewSSHClient := newSSHClient
		t.Cleanup(func() { newSSHClient = oldNewSSHClient })
		newSSHClient = func(config ssh.SSHConfig) sshClient { return fake }

		err := NewDownloader().DownloadCustomISO(baseConfig)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ISO file not found after download")
	})

	t.Run("verify after download returns empty file", func(t *testing.T) {
		fake := &fakeSSHClient{
			verifyResults: []struct {
				exists bool
				size   int64
				err    error
			}{
				{exists: false, size: 0, err: nil},
				{exists: true, size: 0, err: nil},
			},
		}
		oldNewSSHClient := newSSHClient
		t.Cleanup(func() { newSSHClient = oldNewSSHClient })
		newSSHClient = func(config ssh.SSHConfig) sshClient { return fake }

		err := NewDownloader().DownloadCustomISO(baseConfig)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "downloaded ISO file is empty")
	})

	t.Run("validation error returns early", func(t *testing.T) {
		fake := &fakeSSHClient{}
		oldNewSSHClient := newSSHClient
		t.Cleanup(func() { newSSHClient = oldNewSSHClient })
		newSSHClient = func(config ssh.SSHConfig) sshClient { return fake }

		err := NewDownloader().DownloadCustomISO(DownloadConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid configuration")
		assert.Equal(t, 0, fake.connectCalls)
	})
}
