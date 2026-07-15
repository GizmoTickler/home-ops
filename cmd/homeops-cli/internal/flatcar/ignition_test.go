package flatcar

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/config"
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

func TestRenderIgnitionUsesNTPServersAndNetworkMTU(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Cluster: config.ClusterConfig{
			NTPServers: []string{"10.0.0.1", "10.0.0.2"},
		},
		Hypervisors: config.HypervisorsConfig{
			Proxmox: config.ProxmoxConfig{VM: config.VMDefaults{NetworkMTU: 1400}},
		},
	})
	defer restore()

	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	assert.Contains(t, ignitionFileContent(t, ign, "/etc/systemd/timesyncd.conf"), "NTP=10.0.0.1 10.0.0.2")
	assert.Contains(t, ignitionFileContent(t, ign, "/etc/systemd/network/10-k8s.network"), "MTUBytes=1400")
}

func ignitionFileContent(t *testing.T, ign []byte, path string) string {
	t.Helper()
	var doc struct {
		Storage struct {
			Files []struct {
				Path     string `json:"path"`
				Contents struct {
					Source string `json:"source"`
				} `json:"contents"`
			} `json:"files"`
		} `json:"storage"`
	}
	require.NoError(t, json.Unmarshal(ign, &doc))
	for _, f := range doc.Storage.Files {
		if f.Path != path {
			continue
		}
		source := f.Contents.Source
		if strings.HasPrefix(source, "data:;base64,") {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(source, "data:;base64,"))
			require.NoError(t, err)
			return string(decoded)
		}
		require.True(t, strings.HasPrefix(source, "data:,"), "unsupported data URL %q", source)
		decoded, err := url.PathUnescape(strings.TrimPrefix(source, "data:,"))
		require.NoError(t, err)
		return decoded
	}
	t.Fatalf("Ignition file %s not found", path)
	return ""
}

func TestRenderIgnitionDeterministicAndNodeSpecific(t *testing.T) {
	restoreConfig := config.SetForTesting(&config.Config{})
	defer restoreConfig()

	env := sampleEnv()
	first, err := RenderIgnition(env)
	require.NoError(t, err)
	require.NotEmpty(t, first)

	for i := 0; i < 20; i++ {
		rendered, err := RenderIgnition(env)
		require.NoError(t, err, "render %d", i)
		if !bytes.Equal(first, rendered) {
			t.Fatalf("RenderIgnition is nondeterministic on render %d", i)
		}
	}

	other := sampleEnv()
	other.NodeName = "k8s-1"
	other.NodeIP = "192.168.122.11"
	other.NodeMAC = "00:a0:98:1a:f3:72"
	differentNode, err := RenderIgnition(other)
	require.NoError(t, err)
	assert.NotEqual(t, first, differentNode, "different nodes must not render identical Ignition")
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

func TestRenderKubeadmInitConfigUsesClusterEnvironmentFields(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Cluster: config.ClusterConfig{
			Name:          "custom-cluster",
			PodCIDR:       "10.244.0.0/16",
			ServiceCIDR:   "10.96.0.0/12",
			DNSDomain:     "corp.local",
			ExtraCertSANs: []string{"10.0.0.100", "api.internal"},
			Kubelet: config.KubeletConfig{
				MaxPods:            111,
				ImageGCHighPercent: 70,
				ImageGCLowPercent:  55,
			},
		},
	})
	defer restore()

	out, err := RenderKubeadmInitConfig(sampleEnv())
	require.NoError(t, err)
	assert.Contains(t, out, "clusterName: custom-cluster")
	assert.Contains(t, out, "podSubnet: 10.244.0.0/16")
	assert.Contains(t, out, "serviceSubnet: 10.96.0.0/12")
	assert.Contains(t, out, "dnsDomain: corp.local")
	assert.Contains(t, out, "clusterDomain: corp.local")
	assert.Contains(t, out, "- 10.96.0.10")
	assert.Contains(t, out, "- 10.0.0.100")
	assert.Contains(t, out, "- api.internal")
	assert.Contains(t, out, "maxPods: 111")
	assert.Contains(t, out, "imageGCHighThresholdPercent: 70")
	assert.Contains(t, out, "imageGCLowThresholdPercent: 55")
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
