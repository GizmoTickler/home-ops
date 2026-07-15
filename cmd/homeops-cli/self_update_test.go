package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"
)

func TestSelectSelfUpdateAssetsAllReleasePlatforms(t *testing.T) {
	assets := []githubReleaseAsset{{Name: constants.SelfUpdateChecksums, BrowserDownloadURL: "http://127.0.0.1/checksums.txt"}}
	for _, goos := range []string{"darwin", "linux"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			name := fmt.Sprintf("homeops-cli_1.2.3_%s_%s.tar.gz", goos, goarch)
			assets = append(assets, githubReleaseAsset{Name: name, BrowserDownloadURL: "http://127.0.0.1/" + name})
		}
	}
	release := githubRelease{TagName: "1.2.3", Assets: assets}
	for _, platform := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		t.Run(platform[0]+"_"+platform[1], func(t *testing.T) {
			asset, checksums, err := selectSelfUpdateAssets(release, platform[0], platform[1])
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("homeops-cli_1.2.3_%s_%s.tar.gz", platform[0], platform[1]), asset.Name)
			assert.Equal(t, constants.SelfUpdateChecksums, checksums.Name)
		})
	}
}

func TestVerifySelfUpdateChecksumSuccessAndMismatch(t *testing.T) {
	dir := t.TempDir()
	assetName := "homeops-cli_1.2.3_darwin_arm64.tar.gz"
	path := filepath.Join(dir, assetName)
	require.NoError(t, os.WriteFile(path, []byte("verified archive"), 0o600))
	digest := sha256.Sum256([]byte("verified archive"))
	checksums := []byte(hex.EncodeToString(digest[:]) + "  " + assetName + "\n")
	require.NoError(t, verifySelfUpdateChecksum(path, assetName, checksums))

	err := verifySelfUpdateChecksum(path, assetName, []byte(fmt.Sprintf("%064x  %s\n", 1, assetName)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestSelfUpdateRefusesDevelopmentBuildWithoutForce(t *testing.T) {
	testutil.Swap(t, &selfUpdateAPIURL, "http://127.0.0.1:1/should-not-be-called")
	report, err := runSelfUpdate(context.Background(), "dev (abc12345)", selfUpdateOptions{Check: true})
	require.Error(t, err)
	assert.Empty(t, report.Latest)
	assert.Contains(t, err.Error(), "--force")
}

func TestSelfUpdateCheckUsesLatestAPIWithoutDownloading(t *testing.T) {
	var apiCalls, downloadCalls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			apiCalls++
			release := githubRelease{TagName: "1.2.3", Assets: []githubReleaseAsset{
				{Name: "homeops-cli_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: "http://" + r.Host + "/asset"},
				{Name: constants.SelfUpdateChecksums, BrowserDownloadURL: "http://" + r.Host + "/checksums"},
			}}
			_ = json.NewEncoder(w).Encode(release)
		default:
			downloadCalls++
			http.Error(w, "check must not download", http.StatusInternalServerError)
		}
	})
	endpoint, client := selfUpdateHTTPTestEndpoint(t, handler)
	testutil.Swap(t, &selfUpdateHTTPClientFn, func() *http.Client { return client })
	testutil.Swap(t, &selfUpdateAPIURL, endpoint+"/latest")
	testutil.Swap(t, &selfUpdateGOOS, "linux")
	testutil.Swap(t, &selfUpdateGOARCH, "amd64")
	confirmed := false
	testutil.Swap(t, &selfUpdateConfirmFn, func(string, bool) (bool, error) {
		confirmed = true
		return true, nil
	})

	report, err := runSelfUpdate(context.Background(), "1.0.0", selfUpdateOptions{Check: true})
	require.NoError(t, err)
	assert.Equal(t, 1, apiCalls)
	assert.Zero(t, downloadCalls)
	assert.False(t, confirmed)
	assert.True(t, report.UpdateAvailable)
	assert.Equal(t, "update-available", report.Status)
}

func TestSelfUpdateSendsGitHubToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "1.0.0", Assets: []githubReleaseAsset{
			{Name: "homeops-cli_1.0.0_linux_amd64.tar.gz", BrowserDownloadURL: "https://127.0.0.1/asset"},
			{Name: constants.SelfUpdateChecksums, BrowserDownloadURL: "https://127.0.0.1/checksums"},
		}})
	})
	testutil.Swap(t, &selfUpdateHTTPClientFn, func() *http.Client { return recorderHTTPClient(handler) })
	testutil.Swap(t, &selfUpdateAPIURL, "https://127.0.0.1/latest")
	testutil.Swap(t, &selfUpdateGOOS, "linux")
	testutil.Swap(t, &selfUpdateGOARCH, "amd64")
	t.Cleanup(testutil.SetEnv(t, "GITHUB_TOKEN", "test-token"))

	_, err := runSelfUpdate(context.Background(), "1.0.0", selfUpdateOptions{Check: true})
	require.NoError(t, err)
}

func TestSelfUpdateDownloadsVerifiesExtractsAndInstalls(t *testing.T) {
	binaryName := "homeops-cli_1.2.3_linux_amd64"
	assetName := binaryName + ".tar.gz"
	binary := []byte("verified homeops binary")
	archive := selfUpdateTestArchive(t, binaryName, binary)
	digest := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(digest[:]) + "  " + assetName + "\n")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(githubRelease{TagName: "1.2.3", Assets: []githubReleaseAsset{
				{Name: assetName, BrowserDownloadURL: "https://127.0.0.1/asset"},
				{Name: constants.SelfUpdateChecksums, BrowserDownloadURL: "https://127.0.0.1/checksums"},
			}})
		case "/asset":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = w.Write(checksums)
		default:
			http.NotFound(w, r)
		}
	})
	testutil.Swap(t, &selfUpdateHTTPClientFn, func() *http.Client { return recorderHTTPClient(handler) })
	testutil.Swap(t, &selfUpdateAPIURL, "https://127.0.0.1/latest")
	testutil.Swap(t, &selfUpdateGOOS, "linux")
	testutil.Swap(t, &selfUpdateGOARCH, "amd64")
	testutil.Swap(t, &selfUpdateConfirmFn, func(string, bool) (bool, error) { return true, nil })
	installed := false
	testutil.Swap(t, &selfUpdateInstallFn, func(path string) error {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, binary, got)
		installed = true
		return nil
	})

	report, err := runSelfUpdate(context.Background(), "1.0.0", selfUpdateOptions{})
	require.NoError(t, err)
	assert.True(t, installed)
	assert.Equal(t, "updated", report.Status)
}

func TestInstallSelfUpdateBinaryAtAtomicallyReplacesTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "verified")
	target := filepath.Join(dir, "homeops-cli")
	require.NoError(t, os.WriteFile(source, []byte("new binary"), 0o700))
	require.NoError(t, os.WriteFile(target, []byte("old binary"), 0o755))

	require.NoError(t, installSelfUpdateBinaryAt(source, target))
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, []byte("new binary"), got)
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestRenderSelfUpdateReport(t *testing.T) {
	report := selfUpdateReport{Current: "1.0.0", Latest: "1.2.3", UpdateAvailable: true, Status: "update-available"}
	table, err := renderSelfUpdateReport(report, "table")
	require.NoError(t, err)
	assert.Contains(t, table, "CURRENT")
	assert.Contains(t, table, "1.2.3")
	jsonOutput, err := renderSelfUpdateReport(report, "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"current":"1.0.0","latest":"1.2.3","update_available":true,"status":"update-available"}`, jsonOutput)
	_, err = renderSelfUpdateReport(report, "yaml")
	require.Error(t, err)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func recorderHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Request = req
		return response, nil
	})}
}

// selfUpdateHTTPTestEndpoint uses a real httptest server when the environment
// permits loopback listeners. Restricted sandboxes fall back to an in-memory
// httptest recorder while exercising the same net/http request path.
func selfUpdateHTTPTestEndpoint(t *testing.T, handler http.Handler) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "http://127.0.0.1", recorderHTTPClient(handler)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server.URL, server.Client()
}

func selfUpdateTestArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	require.NoError(t, tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}))
	_, err := tarWriter.Write(content)
	require.NoError(t, err)
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())
	return buffer.Bytes()
}
