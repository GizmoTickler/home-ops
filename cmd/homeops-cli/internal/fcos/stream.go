package fcos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"homeops-cli/internal/common"
)

// DefaultStream is the FCOS update stream nodes are provisioned from.
const DefaultStream = "stable"

// streamMetadataURL is the published stream metadata for a stream name.
const streamMetadataURL = "https://builds.coreos.fedoraproject.org/streams/%s.json"

// imageHostPrefix is where every artifact location in the official stream
// metadata lives; anything else is rejected rather than downloaded onto the
// hypervisor.
const imageHostPrefix = "https://builds.coreos.fedoraproject.org/"

const (
	streamFetchTimeout = 30 * time.Second
	streamMaxBytes     = 8 << 20
)

var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// knownStreams are the production FCOS streams.
var knownStreams = map[string]bool{"stable": true, "testing": true, "next": true}

// QEMUImage is the x86_64 qemu qcow2.xz disk artifact of an FCOS release, as
// published in the stream metadata
// (architectures.x86_64.artifacts.qemu.formats["qcow2.xz"].disk).
type QEMUImage struct {
	Stream             string
	Release            string // e.g. 44.20260829.3.1
	Location           string // https URL of the .qcow2.xz
	SHA256             string // digest of the compressed .qcow2.xz
	UncompressedSHA256 string // digest of the decompressed .qcow2
}

// CompressedName is the artifact's file name (…-qemu.x86_64.qcow2.xz).
func (i QEMUImage) CompressedName() string {
	return path.Base(i.Location)
}

// DiskName is the decompressed disk image file name (…-qemu.x86_64.qcow2) —
// what Proxmox imports with import-from=.
func (i QEMUImage) DiskName() string {
	return strings.TrimSuffix(i.CompressedName(), ".xz")
}

// fetchStreamFn downloads stream metadata. Swappable for tests.
var fetchStreamFn = func(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, streamFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	return io.ReadAll(io.LimitReader(resp.Body, streamMaxBytes))
}

// ResolveQEMUImage fetches the stream metadata for stream (default "stable")
// and returns its current x86_64 qemu qcow2.xz disk artifact.
func ResolveQEMUImage(ctx context.Context, stream string) (QEMUImage, error) {
	stream = strings.ToLower(strings.TrimSpace(stream))
	if stream == "" {
		stream = DefaultStream
	}
	if !knownStreams[stream] {
		return QEMUImage{}, fmt.Errorf("unknown FCOS stream %q (valid: stable, testing, next)", stream)
	}
	url := fmt.Sprintf(streamMetadataURL, stream)
	data, err := fetchStreamFn(ctx, url)
	if err != nil {
		return QEMUImage{}, fmt.Errorf("fetch FCOS stream metadata %s: %w", url, err)
	}
	img, err := ParseStreamQEMUImage(data)
	if err != nil {
		return QEMUImage{}, fmt.Errorf("parse FCOS stream metadata %s: %w", url, err)
	}
	if img.Stream != stream {
		return QEMUImage{}, fmt.Errorf("stream metadata %s describes stream %q, want %q", url, img.Stream, stream)
	}
	return img, nil
}

// ParseStreamQEMUImage extracts and validates the x86_64 qemu qcow2.xz disk
// artifact from FCOS stream metadata JSON.
func ParseStreamQEMUImage(data []byte) (QEMUImage, error) {
	var doc struct {
		Stream        string `json:"stream"`
		Architectures map[string]struct {
			Artifacts map[string]struct {
				Release string `json:"release"`
				Formats map[string]map[string]struct {
					Location           string `json:"location"`
					SHA256             string `json:"sha256"`
					UncompressedSHA256 string `json:"uncompressed-sha256"`
				} `json:"formats"`
			} `json:"artifacts"`
		} `json:"architectures"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return QEMUImage{}, fmt.Errorf("invalid JSON: %w", err)
	}
	arch, ok := doc.Architectures["x86_64"]
	if !ok {
		return QEMUImage{}, fmt.Errorf("no x86_64 architecture")
	}
	qemu, ok := arch.Artifacts["qemu"]
	if !ok {
		return QEMUImage{}, fmt.Errorf("no x86_64 qemu artifact")
	}
	disk, ok := qemu.Formats["qcow2.xz"]["disk"]
	if !ok {
		return QEMUImage{}, fmt.Errorf("no x86_64 qemu qcow2.xz disk")
	}
	img := QEMUImage{
		Stream:             doc.Stream,
		Release:            qemu.Release,
		Location:           disk.Location,
		SHA256:             strings.ToLower(disk.SHA256),
		UncompressedSHA256: strings.ToLower(disk.UncompressedSHA256),
	}
	if err := img.validate(); err != nil {
		return QEMUImage{}, err
	}
	return img, nil
}

func (i QEMUImage) validate() error {
	if i.Release == "" {
		return fmt.Errorf("qemu artifact has no release")
	}
	if !strings.HasPrefix(i.Location, imageHostPrefix) {
		return fmt.Errorf("qemu disk location %q is not on %s", i.Location, imageHostPrefix)
	}
	if !strings.HasSuffix(i.Location, ".qcow2.xz") {
		return fmt.Errorf("qemu disk location %q is not a .qcow2.xz", i.Location)
	}
	if !strings.Contains(i.CompressedName(), i.Release) {
		return fmt.Errorf("qemu disk %q does not match release %s", i.CompressedName(), i.Release)
	}
	if !sha256HexRe.MatchString(i.SHA256) {
		return fmt.Errorf("qemu disk sha256 %q is not a sha256 digest", i.SHA256)
	}
	if !sha256HexRe.MatchString(i.UncompressedSHA256) {
		return fmt.Errorf("qemu disk uncompressed-sha256 %q is not a sha256 digest", i.UncompressedSHA256)
	}
	// Everything below is interpolated into a remote shell command and a
	// Proxmox import-from= option.
	return common.ValidateProxmoxOptValue("FCOS image name", i.CompressedName())
}

// ProxmoxStageCommand returns the remote shell command that stages img into
// cacheDir on the Proxmox host, and the path of the decompressed disk image to
// import-from. The command is idempotent and fails closed:
//
//   - an existing .qcow2 is reused only if it matches uncompressed-sha256;
//   - the .qcow2.xz is downloaded to a .part file and verified against sha256
//     before it is renamed into place;
//   - it is decompressed to a .part file, verified against uncompressed-sha256
//     and only then renamed to the final name (so import-from never sees a
//     truncated or tampered disk); the .xz is removed afterwards.
func ProxmoxStageCommand(img QEMUImage, cacheDir string) (command, diskPath string, err error) {
	if err := img.validate(); err != nil {
		return "", "", err
	}
	cacheDir = strings.TrimRight(cacheDir, "/")
	if cacheDir == "" || !strings.HasPrefix(cacheDir, "/") {
		return "", "", fmt.Errorf("image cache dir %q must be an absolute path", cacheDir)
	}
	if err := common.ValidateProxmoxOptValue("image cache dir", cacheDir); err != nil {
		return "", "", err
	}
	diskPath = cacheDir + "/" + img.DiskName()
	xzPath := cacheDir + "/" + img.CompressedName()
	q := common.ShellQuote
	command = strings.Join([]string{
		"set -eu",
		"mkdir -p " + q(cacheDir),
		fmt.Sprintf("if [ -s %s ] && echo %s | sha256sum --check --status; then exit 0; fi",
			q(diskPath), q(img.UncompressedSHA256+"  "+diskPath)),
		fmt.Sprintf("rm -f %s %s", q(xzPath+".part"), q(diskPath+".part")),
		fmt.Sprintf("wget -q -O %s %s", q(xzPath+".part"), q(img.Location)),
		fmt.Sprintf("echo %s | sha256sum --check --status || { rm -f %s; echo 'FCOS image sha256 mismatch' >&2; exit 1; }",
			q(img.SHA256+"  "+xzPath+".part"), q(xzPath+".part")),
		fmt.Sprintf("mv %s %s", q(xzPath+".part"), q(xzPath)),
		fmt.Sprintf("xz -dc %s > %s", q(xzPath), q(diskPath+".part")),
		fmt.Sprintf("echo %s | sha256sum --check --status || { rm -f %s; echo 'FCOS image uncompressed-sha256 mismatch' >&2; exit 1; }",
			q(img.UncompressedSHA256+"  "+diskPath+".part"), q(diskPath+".part")),
		fmt.Sprintf("mv %s %s", q(diskPath+".part"), q(diskPath)),
		fmt.Sprintf("rm -f %s", q(xzPath)),
	}, "; ")
	return "sh -c " + q(command), diskPath, nil
}
