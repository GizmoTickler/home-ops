package fcos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadStreamFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "streams", "stable.json"))
	require.NoError(t, err)
	return data
}

func TestParseStreamQEMUImage(t *testing.T) {
	img, err := ParseStreamQEMUImage(loadStreamFixture(t))
	require.NoError(t, err)
	assert.Equal(t, "stable", img.Stream)
	assert.Equal(t, "44.20260829.3.1", img.Release)
	assert.Equal(t, "https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/44.20260829.3.1/x86_64/fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2.xz", img.Location)
	assert.Equal(t, "a0aa13c4c88519c9c3ee6a16101f84c9f4184fc4eb1e687779b31177e540c12e", img.SHA256)
	assert.Equal(t, "46d90f2b792b17ea3b9105326ee068c6a2756d804757ebb8e4ea3aaf9e7ac75c", img.UncompressedSHA256)
	assert.Equal(t, "fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2.xz", img.CompressedName())
	assert.Equal(t, "fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2", img.DiskName())
}

func TestParseStreamQEMUImageRejectsBadMetadata(t *testing.T) {
	valid := string(loadStreamFixture(t))
	cases := map[string]string{
		"not json":         "{",
		"foreign host":     strings.Replace(valid, "https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/44.20260829.3.1/x86_64/fedora-coreos-44.20260829.3.1-qemu", "https://evil.example.test/fedora-coreos-44.20260829.3.1-qemu", 1),
		"bad sha256":       strings.Replace(valid, "a0aa13c4c88519c9c3ee6a16101f84c9f4184fc4eb1e687779b31177e540c12e", "nothex", 1),
		"bad uncompressed": strings.Replace(valid, "46d90f2b792b17ea3b9105326ee068c6a2756d804757ebb8e4ea3aaf9e7ac75c", "", 1),
		"no qemu artifact": strings.Replace(valid, `"qemu": {`, `"qemu-gone": {`, 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseStreamQEMUImage([]byte(doc))
			require.Error(t, err)
		})
	}
}

func TestResolveQEMUImageFetchesNamedStream(t *testing.T) {
	var fetched string
	testutil.Swap(t, &fetchStreamFn, func(_ context.Context, url string) ([]byte, error) {
		fetched = url
		return loadStreamFixture(t), nil
	})
	img, err := ResolveQEMUImage(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "https://builds.coreos.fedoraproject.org/streams/stable.json", fetched)
	assert.Equal(t, "44.20260829.3.1", img.Release)

	// Metadata for a different stream than requested is refused.
	_, err = ResolveQEMUImage(context.Background(), "testing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `describes stream "stable"`)

	_, err = ResolveQEMUImage(context.Background(), "rawhide")
	require.Error(t, err)

	testutil.Swap(t, &fetchStreamFn, func(context.Context, string) ([]byte, error) {
		return nil, errors.New("offline")
	})
	_, err = ResolveQEMUImage(context.Background(), "stable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline")
}

func TestProxmoxStageCommandValidatesInputs(t *testing.T) {
	img, err := ParseStreamQEMUImage(loadStreamFixture(t))
	require.NoError(t, err)

	_, disk, err := ProxmoxStageCommand(img, "/var/lib/vz/template/cache/")
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/vz/template/cache/fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2", disk)

	_, _, err = ProxmoxStageCommand(img, "relative/dir")
	require.Error(t, err)
	_, _, err = ProxmoxStageCommand(img, "/tmp/a dir")
	require.Error(t, err)
	bad := img
	bad.SHA256 = "x"
	_, _, err = ProxmoxStageCommand(bad, "/var/lib/vz/template/cache")
	require.Error(t, err)
}

// TestProxmoxStageCommandVerifiesChecksums executes the generated staging
// command locally with a fake wget, proving it (1) stages and verifies a good
// image, (2) is a no-op once the verified disk exists, and (3) refuses a
// download whose digest does not match — leaving no disk behind.
func TestProxmoxStageCommandVerifiesChecksums(t *testing.T) {
	for _, tool := range []string{"sh", "xz", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	work := t.TempDir()
	disk := []byte("not really a qcow2 but good enough\n")
	xzPath := filepath.Join(work, "payload.qcow2.xz")
	cmd := exec.Command("xz", "-c") // #nosec G204 -- fixed test tool
	cmd.Stdin = bytes.NewReader(disk)
	compressed, err := cmd.Output()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(xzPath, compressed, 0o600))

	// Fake wget: `wget -q -O <dest> <url>` copies the local payload.
	bin := filepath.Join(work, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "wget"),
		[]byte("#!/bin/sh\ncp \"$FAKE_PAYLOAD\" \"$3\"\n"), 0o700)) // #nosec G306 -- test helper must be executable

	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	img := QEMUImage{
		Stream:             "stable",
		Release:            "44.20260829.3.1",
		Location:           "https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/44.20260829.3.1/x86_64/fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2.xz",
		SHA256:             sum(compressed),
		UncompressedSHA256: sum(disk),
	}
	cache := filepath.Join(work, "cache")
	run := func(img QEMUImage) (string, error) {
		command, diskPath, err := ProxmoxStageCommand(img, cache)
		require.NoError(t, err)
		c := exec.Command("sh", "-c", command) // #nosec G204 -- command built by the code under test
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_PAYLOAD="+xzPath)
		out, err := c.CombinedOutput()
		if err != nil {
			return diskPath, errors.New(string(out))
		}
		return diskPath, nil
	}

	diskPath, err := run(img)
	require.NoError(t, err)
	got, err := os.ReadFile(diskPath) // #nosec G304 -- temp test path
	require.NoError(t, err)
	assert.Equal(t, disk, got)
	_, err = os.Stat(filepath.Join(cache, img.CompressedName()))
	assert.True(t, os.IsNotExist(err), "the .xz is removed after a verified decompress")

	// Idempotent: with the verified disk present no download happens (a
	// missing payload would make the fake wget fail).
	require.NoError(t, os.Remove(xzPath))
	_, err = run(img)
	require.NoError(t, err)

	// Tampered download: rejected, nothing left for import-from.
	require.NoError(t, os.WriteFile(xzPath, compressed, 0o600))
	tampered := img
	tampered.Release = "44.20260915.3.0"
	tampered.Location = strings.ReplaceAll(img.Location, "44.20260829.3.1", "44.20260915.3.0")
	tampered.SHA256 = sum([]byte("something else"))
	diskPath, err = run(tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256 mismatch")
	_, statErr := os.Stat(diskPath)
	assert.True(t, os.IsNotExist(statErr), "a mismatched image must not be staged")
}
