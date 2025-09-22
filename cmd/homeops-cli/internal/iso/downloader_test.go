package iso

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDefaultConfig(t *testing.T) {
	config := GetDefaultConfig()

	assert.NotNil(t, config)
	// Check that default values are set based on actual implementation
	assert.Equal(t, "/mnt/flashstor/ISO", config.ISOStoragePath)
	assert.Equal(t, "22", config.TrueNASPort)
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
		wantErr bool
		errMsg  string
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
			wantErr: false,
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
			wantErr: true,
			errMsg:  "TrueNAS host is required",
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
			wantErr: true,
			errMsg:  "SSH item reference is required",
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
			wantErr: true,
			errMsg:  "ISO URL is required",
		},
		{
			name: "missing ISO filename",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "https://example.com/test.iso",
				ISOStoragePath:  "/mnt/tank/isos",
			},
			wantErr: true,
			errMsg:  "ISO filename is required",
		},
		{
			name: "missing storage path",
			config: DownloadConfig{
				TrueNASHost:     "192.168.1.100",
				TrueNASUsername: "root",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "https://example.com/test.iso",
				ISOFilename:     "test.iso",
			},
			wantErr: true,
			errMsg:  "ISO storage path is required",
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
			wantErr: true,
			errMsg:  "ISO URL must start with http:// or https://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			downloader := NewDownloader()
			err := downloader.validateConfig(tt.config)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDownloaderDownloadCustomISO(t *testing.T) {
	// Create a test server to simulate ISO download
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "13")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "FAKE-ISO-DATA")
	}))
	defer server.Close()

	tests := []struct {
		name      string
		config    DownloadConfig
		setupMock func(t *testing.T) (cleanup func())
		wantErr   bool
		errMsg    string
	}{
		{
			name: "successful download simulation",
			config: DownloadConfig{
				TrueNASHost:     "localhost",
				TrueNASUsername: "test",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          server.URL + "/test.iso",
				ISOStoragePath:  "/tmp",
				ISOFilename:     "test.iso",
			},
			setupMock: func(t *testing.T) func() {
				// Skip SSH operations in test
				t.Skip("SSH operations require actual SSH server")
				return func() {}
			},
			wantErr: false,
		},
		{
			name: "missing SSH item reference",
			config: DownloadConfig{
				TrueNASHost:     "localhost",
				TrueNASUsername: "test",
				TrueNASPort:     "22",
				ISOURL:          "https://example.com/test.iso",
				ISOStoragePath:  "/tmp",
				ISOFilename:     "test.iso",
			},
			setupMock: func(t *testing.T) func() {
				return func() {}
			},
			wantErr: true,
			errMsg:  "SSH item reference is required",
		},
		{
			name: "invalid ISO URL format",
			config: DownloadConfig{
				TrueNASHost:     "localhost",
				TrueNASUsername: "test",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "not-a-valid-url",
				ISOStoragePath:  "/tmp",
				ISOFilename:     "test.iso",
			},
			setupMock: func(t *testing.T) func() {
				return func() {}
			},
			wantErr: true,
			errMsg:  "ISO URL must start with http:// or https://",
		},
		{
			name: "server returns 404",
			config: DownloadConfig{
				TrueNASHost:     "localhost",
				TrueNASUsername: "test",
				TrueNASPort:     "22",
				SSHItemRef:      "op://vault/truenas/ssh",
				ISOURL:          "http://httpstat.us/404",
				ISOStoragePath:  "/tmp",
				ISOFilename:     "test.iso",
			},
			setupMock: func(t *testing.T) func() {
				t.Skip("External service test")
				return func() {}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setupMock(t)
			defer cleanup()

			downloader := NewDownloader()
			err := downloader.DownloadCustomISO(tt.config)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				// In a real test with SSH, we'd verify the file exists
				require.NoError(t, err)
			}
		})
	}
}

func TestDownloadConfigFields(t *testing.T) {
	config := &DownloadConfig{
		TrueNASHost:     "192.168.1.100",
		TrueNASUsername: "root",
		TrueNASPort:     "22",
		SSHItemRef:      "op://vault/truenas/ssh",
		ISOURL:          "https://example.com/test.iso",
		ISOStoragePath:  "/mnt/tank/isos",
		ISOFilename:     "metal-amd64.iso",
	}

	assert.Equal(t, "192.168.1.100", config.TrueNASHost)
	assert.Equal(t, "root", config.TrueNASUsername)
	assert.Equal(t, "22", config.TrueNASPort)
	assert.Equal(t, "op://vault/truenas/ssh", config.SSHItemRef)
	assert.Equal(t, "https://example.com/test.iso", config.ISOURL)
	assert.Equal(t, "/mnt/tank/isos", config.ISOStoragePath)
	assert.Equal(t, "metal-amd64.iso", config.ISOFilename)
}

func TestDownloaderWithMockSSH(t *testing.T) {
	// This test would require mocking SSH operations
	// For now, we'll skip it as it requires dependency injection
	t.Skip("Requires SSH mocking infrastructure")
}

func TestDownloadHelperFunctions(t *testing.T) {
	t.Run("download progress tracking", func(t *testing.T) {
		// Test that would verify progress tracking
		// Requires implementation details
		t.Skip("Progress tracking test requires implementation access")
	})

	t.Run("retry logic", func(t *testing.T) {
		// Test retry logic for failed downloads
		t.Skip("Retry logic test requires implementation access")
	})
}

func TestErrorScenarios(t *testing.T) {
	downloader := NewDownloader()

	t.Run("nil config", func(t *testing.T) {
		var config DownloadConfig
		err := downloader.validateConfig(config)
		assert.Error(t, err)
	})

	t.Run("network timeout", func(t *testing.T) {
		// Create a server that never responds
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate a very slow server
			select {}
		}))
		defer server.Close()

		// This would timeout in a real scenario
		t.Skip("Timeout test requires actual implementation")
	})
}

// Integration test - would run against actual TrueNAS
func TestIntegrationDownloadISO(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "true" {
		t.Skip("Skipping integration test")
	}

	config := DownloadConfig{
		TrueNASHost:     os.Getenv("TRUENAS_HOST"),
		TrueNASUsername: os.Getenv("TRUENAS_USERNAME"),
		TrueNASPort:     "22",
		SSHItemRef:      os.Getenv("SSH_ITEM_REF"),
		ISOURL:          "https://releases.ubuntu.com/22.04/ubuntu-22.04-desktop-amd64.iso",
		ISOStoragePath:  "/mnt/tank/isos",
		ISOFilename:     "ubuntu-test.iso",
	}

	downloader := NewDownloader()
	err := downloader.DownloadCustomISO(config)
	assert.NoError(t, err)
}

// Benchmark tests
func BenchmarkNewDownloader(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewDownloader()
	}
}

func BenchmarkValidateConfig(b *testing.B) {
	config := DownloadConfig{
		TrueNASHost:     "192.168.1.100",
		TrueNASUsername: "root",
		TrueNASPort:     "22",
		ISOURL:          "https://example.com/test.iso",
		ISOStoragePath:  "/mnt/tank/isos",
		ISOFilename:     "test.iso",
	}
	downloader := NewDownloader()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = downloader.validateConfig(config)
	}
}

func BenchmarkGetDefaultConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = GetDefaultConfig()
	}
}
