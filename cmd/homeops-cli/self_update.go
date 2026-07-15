package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/ui"
)

const (
	selfUpdateOwner       = "GizmoTickler"
	selfUpdateRepository  = "home-ops"
	selfUpdateChecksums   = "checksums.txt"
	selfUpdateMaxDownload = 256 << 20
)

var (
	selfUpdateAPIURL       = "https://api.github.com/repos/" + selfUpdateOwner + "/" + selfUpdateRepository + "/releases/latest"
	selfUpdateGOOS         = runtime.GOOS
	selfUpdateGOARCH       = runtime.GOARCH
	selfUpdateHTTPClientFn = func() *http.Client {
		return &http.Client{
			Timeout: 2 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				if !selfUpdateSecureURL(req.URL) {
					return fmt.Errorf("refusing redirect to non-HTTPS URL %s", req.URL.Redacted())
				}
				return nil
			},
		}
	}
	selfUpdateConfirmFn = ui.Confirm
	selfUpdateInstallFn = installSelfUpdateBinary
)

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type selfUpdateOptions struct {
	Check  bool
	Force  bool
	Output string
}

type selfUpdateReport struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	Asset           string `json:"asset,omitempty"`
	Status          string `json:"status"`
}

func newSelfUpdateCommand() *cobra.Command {
	opts := selfUpdateOptions{}
	cmd := &cobra.Command{
		Use:          "self-update",
		Short:        "Update homeops-cli from the latest GitHub release",
		SilenceUsage: true,
		Example: `  homeops-cli self-update --check
  homeops-cli self-update --yes
  homeops-cli self-update --force --output json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := runSelfUpdate(cmd.Context(), version, opts)
			if report.Latest != "" {
				rendered, renderErr := renderSelfUpdateReport(report, opts.Output)
				if renderErr != nil {
					return renderErr
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			}
			if err == nil && report.Status == "updated" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Updated homeops-cli %s -> %s. Re-run `homeops-cli version` to verify.\n", report.Current, report.Latest)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.Check, "check", false, "only report whether an update is available")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "allow updates from a development build")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "table", "output format: table or json")
	return cmd
}

func runSelfUpdate(ctx context.Context, current string, opts selfUpdateOptions) (selfUpdateReport, error) {
	report := selfUpdateReport{Current: current}
	if opts.Output != "" && opts.Output != "table" && opts.Output != "json" {
		return report, fmt.Errorf("unsupported output format %q (table, json)", opts.Output)
	}
	if isDevelopmentVersion(current) && !opts.Force {
		return report, fmt.Errorf("self-update is disabled for development build %q; rebuild from a release or pass --force", current)
	}
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return report, err
	}
	report.Latest = release.TagName
	report.UpdateAvailable = normalizeReleaseVersion(current) != normalizeReleaseVersion(release.TagName)
	asset, checksums, err := selectSelfUpdateAssets(release, selfUpdateGOOS, selfUpdateGOARCH)
	if err != nil {
		return report, err
	}
	report.Asset = asset.Name
	if !report.UpdateAvailable {
		report.Status = "up-to-date"
		return report, nil
	}
	if opts.Check {
		report.Status = "update-available"
		return report, nil
	}
	confirmed, err := selfUpdateConfirmFn(fmt.Sprintf("Update homeops-cli %s -> %s?", current, release.TagName), false)
	if err != nil {
		return report, fmt.Errorf("confirm self-update: %w", err)
	}
	if !confirmed {
		report.Status = "cancelled"
		return report, nil
	}

	tempDir, err := os.MkdirTemp("", "homeops-self-update-*")
	if err != nil {
		return report, fmt.Errorf("create update temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	archivePath := filepath.Join(tempDir, asset.Name)
	checksumsPath := filepath.Join(tempDir, checksums.Name)
	if err := downloadSelfUpdateAsset(ctx, asset.BrowserDownloadURL, archivePath); err != nil {
		return report, err
	}
	if err := downloadSelfUpdateAsset(ctx, checksums.BrowserDownloadURL, checksumsPath); err != nil {
		return report, err
	}
	// #nosec G304 -- checksumsPath is constructed inside our private MkdirTemp directory.
	checksumsRaw, err := os.ReadFile(checksumsPath)
	if err != nil {
		return report, fmt.Errorf("read checksums: %w", err)
	}
	if err := verifySelfUpdateChecksum(archivePath, asset.Name, checksumsRaw); err != nil {
		return report, err
	}
	binaryPath, err := extractSelfUpdateArchive(archivePath, tempDir, strings.TrimSuffix(asset.Name, ".tar.gz"))
	if err != nil {
		return report, err
	}
	if err := selfUpdateInstallFn(binaryPath); err != nil {
		return report, err
	}
	report.Status = "updated"
	return report, nil
}

func fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	var release githubRelease
	apiURL, err := url.Parse(selfUpdateAPIURL)
	if err != nil || !selfUpdateSecureURL(apiURL) {
		return release, fmt.Errorf("self-update API URL must use HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL.String(), nil)
	if err != nil {
		return release, fmt.Errorf("create GitHub release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "homeops-cli-self-update")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// #nosec G107 -- the production URL is a fixed HTTPS GitHub API endpoint;
	// tests may swap it only for a loopback httptest server.
	resp, err := selfUpdateHTTPClientFn().Do(req)
	if err != nil {
		return release, fmt.Errorf("query latest GitHub release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return release, fmt.Errorf("query latest GitHub release: HTTP %s", resp.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := decoder.Decode(&release); err != nil {
		return release, fmt.Errorf("decode latest GitHub release: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return release, fmt.Errorf("latest GitHub release has no tag")
	}
	return release, nil
}

func selectSelfUpdateAssets(release githubRelease, goos, goarch string) (githubReleaseAsset, githubReleaseAsset, error) {
	wanted := fmt.Sprintf("homeops-cli_%s_%s_%s.tar.gz", release.TagName, goos, goarch)
	var binaryAsset, checksums githubReleaseAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case wanted:
			binaryAsset = asset
		case selfUpdateChecksums:
			checksums = asset
		}
	}
	if binaryAsset.Name == "" {
		return binaryAsset, checksums, fmt.Errorf("release %s has no asset for %s/%s (expected %s)", release.TagName, goos, goarch, wanted)
	}
	if checksums.Name == "" {
		return binaryAsset, checksums, fmt.Errorf("release %s has no %s asset", release.TagName, selfUpdateChecksums)
	}
	for _, asset := range []githubReleaseAsset{binaryAsset, checksums} {
		parsed, err := url.Parse(asset.BrowserDownloadURL)
		if err != nil || !selfUpdateSecureURL(parsed) {
			return binaryAsset, checksums, fmt.Errorf("release asset %s has a non-HTTPS download URL", asset.Name)
		}
	}
	return binaryAsset, checksums, nil
}

func downloadSelfUpdateAsset(ctx context.Context, sourceURL, destination string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil || !selfUpdateSecureURL(parsed) {
		return fmt.Errorf("refusing non-HTTPS update asset URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("create update download request: %w", err)
	}
	req.Header.Set("User-Agent", "homeops-cli-self-update")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// #nosec G107 -- release URLs are rejected unless HTTPS (or loopback-only in tests).
	resp, err := selfUpdateHTTPClientFn().Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", filepath.Base(destination), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", filepath.Base(destination), resp.Status)
	}
	if resp.ContentLength > selfUpdateMaxDownload {
		return fmt.Errorf("download %s exceeds %d bytes", filepath.Base(destination), selfUpdateMaxDownload)
	}
	// #nosec G304 -- destination is a fixed asset basename inside our private MkdirTemp directory.
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create download %s: %w", filepath.Base(destination), err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, selfUpdateMaxDownload+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download %s: %w", filepath.Base(destination), copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close download %s: %w", filepath.Base(destination), closeErr)
	}
	if written > selfUpdateMaxDownload {
		return fmt.Errorf("download %s exceeds %d bytes", filepath.Base(destination), selfUpdateMaxDownload)
	}
	return nil
}

func verifySelfUpdateChecksum(archivePath, assetName string, checksums []byte) error {
	wanted := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == assetName || filepath.Base(name) == assetName {
			wanted = strings.ToLower(fields[0])
			break
		}
	}
	if len(wanted) != sha256.Size*2 {
		return fmt.Errorf("checksums file has no valid SHA256 for %s", assetName)
	}
	if _, err := hex.DecodeString(wanted); err != nil {
		return fmt.Errorf("checksums file has invalid SHA256 for %s", assetName)
	}
	// #nosec G304 -- archivePath is the just-downloaded file in our private MkdirTemp directory.
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open downloaded asset: %w", err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash downloaded asset: %w", err)
	}
	got := hex.EncodeToString(digest.Sum(nil))
	if got != wanted {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", assetName, wanted, got)
	}
	return nil
}

func extractSelfUpdateArchive(archivePath, destinationDir, expectedName string) (string, error) {
	// #nosec G304 -- archivePath is the checksum-verified file in our private MkdirTemp directory.
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open update archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open update gzip archive: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read update archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Name != expectedName || filepath.Base(header.Name) != header.Name {
			continue
		}
		path := filepath.Join(destinationDir, "next-homeops-cli")
		// #nosec G304,G302 -- path is fixed inside MkdirTemp and must be executable after extraction.
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			return "", fmt.Errorf("create extracted update: %w", err)
		}
		written, copyErr := io.Copy(out, io.LimitReader(tarReader, selfUpdateMaxDownload+1))
		closeErr := out.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract update binary: %w", copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close extracted update: %w", closeErr)
		}
		if written > selfUpdateMaxDownload {
			_ = os.Remove(path)
			return "", fmt.Errorf("extracted update binary exceeds %d bytes", selfUpdateMaxDownload)
		}
		return path, nil
	}
	return "", fmt.Errorf("update archive does not contain %s", expectedName)
}

func installSelfUpdateBinary(source string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	return installSelfUpdateBinaryAt(source, target)
}

func installSelfUpdateBinaryAt(source, target string) error {
	// #nosec G304 -- source is the checksum-verified extraction path created by this process.
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open verified update binary: %w", err)
	}
	defer func() { _ = sourceFile.Close() }()
	tempFile, err := os.CreateTemp(filepath.Dir(target), ".homeops-cli-update-*")
	if err != nil {
		return fmt.Errorf("create update next to %s: %w", target, err)
	}
	tempPath := tempFile.Name()
	installed := false
	defer func() {
		_ = tempFile.Close()
		if !installed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := io.Copy(tempFile, sourceFile); err != nil {
		return fmt.Errorf("stage update binary: %w", err)
	}
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("sync update binary: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close staged update binary: %w", err)
	}
	// #nosec G302 -- the installed CLI must be executable by its owner and normal users.
	if err := os.Chmod(tempPath, 0o755); err != nil {
		return fmt.Errorf("mark update binary executable: %w", err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return fmt.Errorf("atomically replace %s: %w", target, err)
	}
	installed = true
	return nil
}

func selfUpdateSecureURL(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return true
	}
	// Go's httptest.Server is HTTP-only. This exception is limited to literal
	// loopback hosts and cannot admit a production redirect to a remote server.
	host := parsed.Hostname()
	return strings.EqualFold(parsed.Scheme, "http") && (host == "127.0.0.1" || host == "::1" || host == "localhost")
}

func isDevelopmentVersion(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "" || normalized == "dev" || strings.HasPrefix(normalized, "dev ") || strings.Contains(normalized, "(devel)")
}

func normalizeReleaseVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func renderSelfUpdateReport(report selfUpdateReport, output string) (string, error) {
	if output == "json" {
		raw, err := json.MarshalIndent(report, "", "  ")
		return string(raw), err
	}
	if output != "" && output != "table" {
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
	rows := [][]string{{report.Current, report.Latest, fmt.Sprintf("%t", report.UpdateAvailable), report.Status}}
	return ui.Table([]string{"CURRENT", "LATEST", "AVAILABLE", "STATUS"}, rows), nil
}
