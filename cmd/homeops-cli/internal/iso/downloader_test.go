package iso

import (
	"errors"
	"path/filepath"
	"testing"

	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"

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
	commandOutput string
	commandErr    error
	verifyCalls   []string
	removeCalls   []string
	downloadCalls [][2]string
	commandCalls  []string
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

func (f *fakeSSHClient) ExecuteCommand(command string) (string, error) {
	f.commandCalls = append(f.commandCalls, command)
	return f.commandOutput, f.commandErr
}

func TestGetDefaultConfig(t *testing.T) {
	// The default config resolves TrueNAS connection details through the
	// portable env:// references.
	t.Setenv("TRUENAS_HOST", "nas.example.com")
	t.Setenv("TRUENAS_USERNAME", "admin")

	config := GetDefaultConfig()

	assert.Equal(t, "nas.example.com", config.TrueNASHost)
	assert.Equal(t, "admin", config.TrueNASUsername)
	assert.Equal(t, "22", config.TrueNASPort)
	assert.Equal(t, "/mnt/flashstor/ISO", config.ISOStoragePath)
	assert.Equal(t, "metal-amd64.iso", config.ISOFilename)
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
				ISOURL:         "https://example.com/test.iso",
				ISOStoragePath: "/mnt/tank/isos",
				ISOFilename:    "test.iso",
			},
			wantErr: "TrueNAS username is required",
		},
		{
			name: "missing ISO URL",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
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
				ISOURL:          "not-a-valid-url",
				ISOStoragePath:  "/mnt/tank/isos",
				ISOFilename:     "test.iso",
			},
			wantErr: "ISO URL must start with https://",
		},
		{
			name: "invalid filename suffix",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
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

func TestDownloaderVerifiesFlatcarSHA512Checksum(t *testing.T) {
	const expected = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	config := DownloadConfig{
		TrueNASHost:     "nas.local",
		TrueNASUsername: "root",
		TrueNASPort:     "22",
		ISOURL:          "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_iso_image.iso",
		ISOStoragePath:  "/mnt/tank/iso dir",
		ISOFilename:     "flatcar_production_iso_image.iso",
	}
	fake := &fakeSSHClient{
		verifyResults: []struct {
			exists bool
			size   int64
			err    error
		}{
			{exists: false, size: 0, err: nil},
			{exists: true, size: 1024, err: nil},
		},
		commandOutput: expected + "  " + filepath.Join(config.ISOStoragePath, config.ISOFilename) + "\n",
	}
	var fetchedURLs []string
	testutil.Swap(t, &newSSHClient, func(ssh.SSHConfig) sshClient { return fake })
	testutil.Swap(t, &fetchChecksumFileFn, func(url string) ([]byte, error) {
		fetchedURLs = append(fetchedURLs, url)
		return []byte(expected + "  flatcar_production_iso_image.iso\n"), nil
	})

	require.NoError(t, NewDownloader().DownloadCustomISO(config))

	assert.Equal(t, []string{config.ISOURL + ".sha512"}, fetchedURLs)
	require.Len(t, fake.commandCalls, 1)
	assert.Equal(t, "sha512sum '/mnt/tank/iso dir/flatcar_production_iso_image.iso' | awk '{print $1}'", fake.commandCalls[0])
}

func TestDownloaderFailsOnFlatcarSHA512Mismatch(t *testing.T) {
	const expected = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const actual = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	config := DownloadConfig{
		TrueNASHost:     "nas.local",
		TrueNASUsername: "root",
		TrueNASPort:     "22",
		ISOURL:          "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_iso_image.iso",
		ISOStoragePath:  "/mnt/tank/isos",
		ISOFilename:     "flatcar_production_iso_image.iso",
	}
	fake := &fakeSSHClient{
		verifyResults: []struct {
			exists bool
			size   int64
			err    error
		}{
			{exists: false, size: 0, err: nil},
			{exists: true, size: 1024, err: nil},
		},
		commandOutput: actual + "\n",
	}
	testutil.Swap(t, &newSSHClient, func(ssh.SSHConfig) sshClient { return fake })
	testutil.Swap(t, &fetchChecksumFileFn, func(string) ([]byte, error) {
		return []byte(expected + "  flatcar_production_iso_image.iso\n"), nil
	})

	err := NewDownloader().DownloadCustomISO(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHA512 checksum mismatch")
}

func TestDownloaderSkipsTalosFactoryChecksumFetch(t *testing.T) {
	config := DownloadConfig{
		TrueNASHost:     "nas.local",
		TrueNASUsername: "root",
		TrueNASPort:     "22",
		ISOURL:          "https://factory.talos.dev/image/schematic/v1.13.6/metal-amd64.iso",
		ISOStoragePath:  "/mnt/tank/isos",
		ISOFilename:     "metal-amd64.iso",
	}
	fake := &fakeSSHClient{
		verifyResults: []struct {
			exists bool
			size   int64
			err    error
		}{
			{exists: false, size: 0, err: nil},
			{exists: true, size: 1024, err: nil},
		},
	}
	fetched := false
	testutil.Swap(t, &newSSHClient, func(ssh.SSHConfig) sshClient { return fake })
	testutil.Swap(t, &fetchChecksumFileFn, func(string) ([]byte, error) {
		fetched = true
		return nil, nil
	})

	require.NoError(t, NewDownloader().DownloadCustomISO(config))
	assert.False(t, fetched)
	assert.Empty(t, fake.commandCalls)
}
