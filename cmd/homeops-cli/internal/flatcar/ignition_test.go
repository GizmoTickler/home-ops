package flatcar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleEnv() NodeEnv {
	return NodeEnv{
		NodeName:          "k8s-0",
		NodeIP:            "192.168.122.10",
		Node0IP:           "192.168.122.10",
		Node1IP:           "192.168.122.11",
		Node2IP:           "192.168.122.12",
		KubernetesVersion: "v1.36.1",
		KubernetesMinor:   "v1.36",
		ControlPlaneVIP:   "192.168.123.253",
		PauseImage:        "registry.k8s.io/pause:3.10",
		KubeVipVersion:    "v0.8.9",
		NodeInterface:     "eth0",
		NodeMAC:           "00:a0:98:28:c8:83",
		K8sEndpoint:       "k8s.example.test",
		SSHAuthorizedKey:  "ssh-ed25519 AAAATESTKEY",
	}
}

func TestRenderIgnitionRejectsUnresolvedPlaceholder(t *testing.T) {
	// A silent 1Password miss leaves SSHAuthorizedKey empty; envMap() omits it, so
	// the Butane keeps a literal {{ ENV.SSH_AUTHORIZED_KEY }}. Rendering MUST fail
	// loudly rather than bake a garbage SSH key into Ignition (unreachable node).
	env := sampleEnv()
	env.SSHAuthorizedKey = ""
	_, err := RenderIgnition(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved placeholder")
	assert.Contains(t, err.Error(), "SSH_AUTHORIZED_KEY")
}

func TestRenderIgnitionProducesValidJSON(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	require.NotEmpty(t, ign)

	// Must be valid JSON.
	var doc map[string]any
	require.NoError(t, json.Unmarshal(ign, &doc), "ignition output must be valid JSON")

	// Ignition documents carry an "ignition" key with a version.
	_, ok := doc["ignition"]
	assert.True(t, ok, "ignition output must contain an 'ignition' section")

	s := string(ign)
	// No unresolved {{ ENV.NAME }} placeholders should survive (descriptive
	// "{{ ENV.* }}" comments are stripped during transpile, not flagged).
	assert.NotContains(t, s, "{{ ENV.NODE_NAME }}")
	assert.NotContains(t, s, "{{ ENV.KUBERNETES_VERSION }}")
	// The sysext source (un-compressed) carries the substituted k8s version.
	assert.Contains(t, s, "kubernetes-v1.36.1-x86-64.raw")
	// The hostname file is an inline data URL: data:,k8s-0 (NODE_NAME substituted).
	assert.Contains(t, s, "data:,k8s-0")
}

func TestRenderIgnitionSubstitutesLocalFiles(t *testing.T) {
	// Capture which files were rendered and that substitution reached them.
	var renderedFiles []string
	origRender := renderFlatcarTemplateFn
	defer func() { renderFlatcarTemplateFn = origRender }()
	renderFlatcarTemplateFn = func(name string, env map[string]string) (string, error) {
		renderedFiles = append(renderedFiles, name)
		return origRender(name, env)
	}

	_, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)

	assert.Contains(t, renderedFiles, "butane/controlplane.bu")
	assert.Contains(t, renderedFiles, "files/containerd-config.toml")
	assert.Contains(t, renderedFiles, "manifests/kube-vip.yaml")
}

func TestMaterializeFlatcarSubdirWritesRenderedFiles0600(t *testing.T) {
	testutil.Swap(t, &listFlatcarFilesFn, func(subdir string) ([]string, error) {
		assert.Equal(t, "files", subdir)
		return []string{"files/secret-ish.toml"}, nil
	})
	testutil.Swap(t, &renderFlatcarTemplateFn, func(name string, env map[string]string) (string, error) {
		assert.Equal(t, "files/secret-ish.toml", name)
		assert.Equal(t, "abc", env["TOKEN"])
		return "bootstrap-token=abc", nil
	})

	baseDir := t.TempDir()
	require.NoError(t, materializeFlatcarSubdir(baseDir, "files", map[string]string{"TOKEN": "abc"}))

	info, err := os.Stat(filepath.Join(baseDir, "files", "secret-ish.toml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRenderIgnitionTranspileError(t *testing.T) {
	orig := translateButaneFn
	defer func() { translateButaneFn = orig }()
	translateButaneFn = func(input []byte, dir string) ([]byte, error) {
		return nil, assertErr{}
	}
	_, err := RenderIgnition(sampleEnv())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transpile")
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }

func TestRenderKubeadmInitConfig(t *testing.T) {
	out, err := RenderKubeadmInitConfig(sampleEnv())
	require.NoError(t, err)
	assert.Contains(t, out, "kind: InitConfiguration")
	assert.Contains(t, out, "advertiseAddress: \"192.168.122.10\"")
	assert.Contains(t, out, "kubernetesVersion: \"v1.36.1\"")
	assert.Contains(t, out, "192.168.123.253")
	// No real {{ ENV.NAME }} placeholders remain (the descriptive "{{ ENV.* }}"
	// comment is allowed and not treated as unresolved).
	assert.NotRegexp(t, `{{ ENV\.[A-Z0-9_]+ }}`, out)
}

func TestRenderKubeadmJoinConfig(t *testing.T) {
	env := sampleEnv()
	env.NodeName = "k8s-1"
	env.NodeIP = "192.168.122.11"
	env.CertificateKey = "deadbeef"
	env.BootstrapToken = "abcdef.0123456789abcdef"
	env.CACertHash = "sha256:" + strings.Repeat("a", 64)

	out, err := RenderKubeadmJoinConfig(env)
	require.NoError(t, err)
	assert.Contains(t, out, "kind: JoinConfiguration")
	assert.Contains(t, out, "certificateKey: \"deadbeef\"")
	assert.Contains(t, out, "token: \"abcdef.0123456789abcdef\"")
	assert.Contains(t, out, "sha256:")
	assert.NotRegexp(t, `{{ ENV\.[A-Z0-9_]+ }}`, out)
}

func TestRenderKubeadmJoinConfigMissingMaterial(t *testing.T) {
	// Without cert key/token/hash, placeholders remain and we must error.
	env := sampleEnv()
	_, err := RenderKubeadmJoinConfig(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved")
}
