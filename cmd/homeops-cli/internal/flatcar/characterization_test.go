package flatcar

import (
	"bytes"
	"encoding/json"
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

var charStorageNICs = map[string][]config.StorageNIC{
	"k8s-0": {
		{VLAN: 1201, MAC: "BC:24:11:3B:E0:50", IP: "192.168.201.20/24"},
		{VLAN: 1202, MAC: "BC:24:11:ED:F6:B6", IP: "192.168.202.20/24"},
		{VLAN: 1203, MAC: "BC:24:11:FF:50:81", IP: "192.168.203.20/24"},
		{VLAN: 1204, MAC: "BC:24:11:FB:16:76", IP: "192.168.204.20/24"},
	},
	"k8s-1": {
		{VLAN: 1201, MAC: "BC:24:11:6B:64:25", IP: "192.168.201.21/24"},
		{VLAN: 1202, MAC: "BC:24:11:A4:6E:42", IP: "192.168.202.21/24"},
		{VLAN: 1203, MAC: "BC:24:11:8C:C8:43", IP: "192.168.203.21/24"},
		{VLAN: 1204, MAC: "BC:24:11:D1:C4:BE", IP: "192.168.204.21/24"},
	},
	"k8s-2": {
		{VLAN: 1201, MAC: "BC:24:11:B3:CD:67", IP: "192.168.201.22/24"},
		{VLAN: 1202, MAC: "BC:24:11:41:2D:40", IP: "192.168.202.22/24"},
		{VLAN: 1203, MAC: "BC:24:11:F6:D9:1D", IP: "192.168.203.22/24"},
		{VLAN: 1204, MAC: "BC:24:11:63:50:11", IP: "192.168.204.22/24"},
	},
}

// charEnv builds a fully-populated NodeEnv identical to what the production
// builders assemble from a repo-mirror config (cluster.name: home-ops-cluster
// plus built-in defaults). This is the characterization fixture: its rendered
// output must stay byte-identical across the config-threading refactor.
func charEnv(name, ip, mac string) NodeEnv {
	env := NodeEnv{
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
		NodeMACIoT:        "bc:24:11:b9:55:83",
		NodeMACVPN:        "bc:24:11:33:4c:37",
		K8sEndpoint:       "k8s.example.test",
		SSHAuthorizedKey:  "ssh-ed25519 AAAATESTKEY",
		CertificateKey:    "deadbeef",
		BootstrapToken:    "abcdef.0123456789abcdef",
		CACertHash:        "sha256:" + strings.Repeat("a", 64),
	}
	env.StorageNICs = append([]config.StorageNIC(nil), charStorageNICs[name]...)
	return env
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

func goldenWithoutNFSTrunkAnchors(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "golden", rel)
	want, err := os.ReadFile(path) // #nosec G304 -- fixed testdata path
	require.NoError(t, err, "missing golden %s", path)

	var document struct {
		Systemd struct {
			Units []json.RawMessage `json:"units"`
		} `json:"systemd"`
	}
	require.NoError(t, json.Unmarshal(want, &document))
	stripped := append([]byte(nil), want...)
	removed := 0
	for _, raw := range document.Systemd.Units {
		var unit struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(raw, &unit))
		name := unit.Name
		if strings.Contains(name, `stor\x2dtrunk`) {
			start := bytes.Index(stripped, raw)
			require.NotEqual(t, -1, start, "golden %s is missing raw unit %s", rel, name)
			end := start + len(raw)
			switch {
			case start > 0 && stripped[start-1] == ',':
				start--
			case end < len(stripped) && stripped[end] == ',':
				end++
			default:
				t.Fatalf("golden %s cannot remove unit %s without changing surrounding bytes", rel, name)
			}
			stripped = append(stripped[:start], stripped[end:]...)
			removed++
		}
	}
	require.Equal(t, 3, removed, "golden %s must contain exactly three NFS trunk anchors", rel)
	return stripped
}

func TestFlatcarRenderCharacterization(t *testing.T) {
	restore := config.SetForTesting(&config.Config{Cluster: config.ClusterConfig{
		Name: "home-ops-cluster",
		NFSTrunk: config.NFSTrunkConfig{
			Export: "/mnt/flashstor/data",
			VLANs:  []int{1202, 1203, 1204},
		},
	}})
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
}

func TestFlatcarRenderCharacterizationWithoutConfig(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	defer restore()

	for _, n := range charNodes {
		ign, err := RenderIgnition(charEnv(n.name, n.ip, n.mac))
		require.NoError(t, err)
		require.Equal(t, goldenWithoutNFSTrunkAnchors(t, "ignition/"+n.name+".ign"), ign)
	}

	initCfg, err := RenderKubeadmInitConfig(charEnv("k8s-0", "192.168.122.10", "00:a0:98:28:c8:83"))
	require.NoError(t, err)
	goldenCompare(t, "kubeadm/init-config.yaml", []byte(initCfg))

	for _, n := range charNodes[1:] {
		joinCfg, err := RenderKubeadmJoinConfig(charEnv(n.name, n.ip, n.mac))
		require.NoError(t, err)
		goldenCompare(t, "kubeadm/join-"+n.name+".yaml", []byte(joinCfg))
	}
}
