package flatcar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/config"

	"github.com/stretchr/testify/require"
)

// charNodes mirrors the repo's three control-plane nodes.
var charNodes = []struct{ name, ip, mac string }{
	{"k8s-0", "192.168.122.10", "00:a0:98:28:c8:83"},
	{"k8s-1", "192.168.122.11", "00:a0:98:1a:f3:72"},
	{"k8s-2", "192.168.122.12", "00:a0:98:3e:6c:22"},
}

// charEnv builds a fully-populated NodeEnv identical to what the production
// builders assemble from a repo-mirror config (cluster.name: home-ops-cluster
// plus built-in defaults). This is the characterization fixture: its rendered
// output must stay byte-identical across the config-threading refactor.
func charEnv(name, ip, mac string) NodeEnv {
	return NodeEnv{
		NodeName:          name,
		NodeIP:            ip,
		Node0IP:           "192.168.122.10",
		Node1IP:           "192.168.122.11",
		Node2IP:           "192.168.122.12",
		KubernetesVersion: "v1.36.1",
		KubernetesMinor:   "v1.36",
		ControlPlaneVIP:   "192.168.123.253",
		PauseImage:        "registry.k8s.io/pause:3.10.2",
		KubeVipVersion:    "v1.2.0",
		NodeInterface:     "eth0",
		NodeMAC:           mac,
		K8sEndpoint:       "k8s.example.test",
		SSHAuthorizedKey:  "ssh-ed25519 AAAATESTKEY",
		CertificateKey:    "deadbeef",
		BootstrapToken:    "abcdef.0123456789abcdef",
		CACertHash:        "sha256:" + strings.Repeat("a", 64),
	}
}

func goldenCompare(t *testing.T, rel string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", rel)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- fixed testdata path
	require.NoError(t, err, "missing golden %s (run with UPDATE_GOLDEN=1)", path)
	if string(got) != string(want) {
		t.Fatalf("rendered output for %s drifted from golden (byte mismatch)", rel)
	}
}

func TestFlatcarRenderCharacterization(t *testing.T) {
	// Both a repo-mirror config (cluster.name explicit) and the no-config-file
	// default (cluster.name empty -> ClusterNameWithDefault) must render the
	// exact pre-refactor bytes.
	configs := map[string]*config.Config{
		"repo-mirror": {Cluster: config.ClusterConfig{Name: "home-ops-cluster"}},
		"no-config":   {},
	}
	for label, cfg := range configs {
		t.Run(label, func(t *testing.T) {
			restore := config.SetForTesting(cfg)
			defer restore()

			for _, n := range charNodes {
				ign, err := RenderIgnition(charEnv(n.name, n.ip, n.mac))
				require.NoError(t, err)
				goldenCompare(t, "ignition/"+n.name+".ign", ign)
			}

			initCfg, err := RenderKubeadmInitConfig(charEnv("k8s-0", "192.168.122.10", "00:a0:98:28:c8:83"))
			require.NoError(t, err)
			goldenCompare(t, "kubeadm/init-config.yaml", []byte(initCfg))

			for _, n := range charNodes[1:] {
				joinCfg, err := RenderKubeadmJoinConfig(charEnv(n.name, n.ip, n.mac))
				require.NoError(t, err)
				goldenCompare(t, "kubeadm/join-"+n.name+".yaml", []byte(joinCfg))
			}
		})
	}
}
