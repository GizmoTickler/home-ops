package flatcar

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
		NodeMACIoT:        "bc:24:11:b9:55:83",
		NodeMACVPN:        "bc:24:11:33:4c:37",
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

func TestRenderIgnitionPersistsNFSTrunking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  nfs_trunk:
    export: /mnt/flashstor/data
    vlans: [1202, 1203, 1204]
`), 0o600))
	cfg, err := config.LoadFile(path)
	require.NoError(t, err)
	restore := config.SetForTesting(cfg)
	defer restore()

	env := sampleEnv()
	env.StorageNICs = []config.StorageNIC{
		{VLAN: 1201, MAC: "02:00:00:00:10:01", IP: "192.168.201.20/24"},
		{VLAN: 1202, MAC: "02:00:00:00:10:02", IP: "192.168.202.20/24"},
		{VLAN: 1203, MAC: "02:00:00:00:10:03", IP: "192.168.203.20/24"},
		{VLAN: 1204, MAC: "02:00:00:00:10:04", IP: "192.168.204.20/24"},
	}
	ign, err := RenderIgnition(env)
	require.NoError(t, err)

	assert.Equal(t, `# /etc/nfsmount.conf — port of Talos machine.files /etc/nfsmount.conf (overwrite).
# Tuned NFS defaults for the TrueNAS NFS mounts (VolSync Kopia repo, media, OpenEBS).
[ NFSMount_Global_Options ]
nfsvers=4.2
hard=True
# Worst-case budget: 16 is the NFS_MAX_TRANSPORTS ceiling; nconnect=4 leaves room for up to 4 server addresses.
nconnect=4
max_connect=16
noatime=True
rsize=1048576
wsize=1048576
`, ignitionFileContent(t, ign, "/etc/nfsmount.conf"))

	units := ignitionSystemdUnits(t, ign)
	want := map[string]string{
		`var-mnt-stor\x2dtrunk\x2d202.mount`: "[Unit]\nDescription=NFS 4.1 trunk anchor via VLAN 202 (adds transports to the nas01 session)\nAfter=network-online.target\nWants=network-online.target\n[Mount]\nWhat=192.168.202.10:/mnt/flashstor/data\nWhere=/var/mnt/stor-trunk-202\nType=nfs4\nOptions=vers=4.2,ro,noatime,nconnect=4,max_connect=16\n[Install]\nWantedBy=multi-user.target\n",
		`var-mnt-stor\x2dtrunk\x2d203.mount`: "[Unit]\nDescription=NFS 4.1 trunk anchor via VLAN 203 (adds transports to the nas01 session)\nAfter=network-online.target\nWants=network-online.target\n[Mount]\nWhat=192.168.203.10:/mnt/flashstor/data\nWhere=/var/mnt/stor-trunk-203\nType=nfs4\nOptions=vers=4.2,ro,noatime,nconnect=4,max_connect=16\n[Install]\nWantedBy=multi-user.target\n",
		`var-mnt-stor\x2dtrunk\x2d204.mount`: "[Unit]\nDescription=NFS 4.1 trunk anchor via VLAN 204 (adds transports to the nas01 session)\nAfter=network-online.target\nWants=network-online.target\n[Mount]\nWhat=192.168.204.10:/mnt/flashstor/data\nWhere=/var/mnt/stor-trunk-204\nType=nfs4\nOptions=vers=4.2,ro,noatime,nconnect=4,max_connect=16\n[Install]\nWantedBy=multi-user.target\n",
	}
	for name, contents := range want {
		unit, ok := units[name]
		require.True(t, ok, "missing NFS trunk anchor %s", name)
		assert.True(t, unit.Enabled, name)
		assert.Equal(t, contents, unit.Contents, name)
	}
}

func TestRenderIgnitionWithoutNFSTrunkConfigKeepsAnchorUnitsAbsent(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	defer restore()
	env := sampleEnv()
	env.StorageNICs = []config.StorageNIC{{VLAN: 1202, MAC: "02:00:00:00:10:02", IP: "192.168.202.20/24"}}

	ign, err := RenderIgnition(env)
	require.NoError(t, err)
	for name := range ignitionSystemdUnits(t, ign) {
		assert.NotContains(t, name, `stor\x2dtrunk`)
	}
}

func TestRepositoryConfigRendersThreeNFSTrunkAnchorsOnEveryStorageNode(t *testing.T) {
	cfg, err := config.LoadFile(filepath.Join("..", "..", "..", "..", "homeops.yaml"))
	require.NoError(t, err)
	restore := config.SetForTesting(cfg)
	defer restore()

	for _, node := range cfg.Cluster.Nodes {
		t.Run(node.Name, func(t *testing.T) {
			env := sampleEnv()
			env.NodeName = node.Name
			env.NodeIP = node.IP
			env.StorageNICs = append([]config.StorageNIC(nil), node.VM.StorageNICs...)

			ign, err := RenderIgnition(env)
			require.NoError(t, err)
			anchors := make(map[string]ignitionSystemdUnit)
			for name, unit := range ignitionSystemdUnits(t, ign) {
				if strings.Contains(name, `stor\x2dtrunk`) {
					anchors[name] = unit
				}
			}
			require.Len(t, anchors, 3)
			for _, storageVLAN := range cfg.Cluster.NFSTrunk.VLANs {
				vlan := storageVLAN - 1000
				name := fmt.Sprintf(`var-mnt-stor\x2dtrunk\x2d%d.mount`, vlan)
				unit, ok := anchors[name]
				require.True(t, ok, "missing NFS trunk anchor %s", name)
				assert.True(t, unit.Enabled, unit.Name)
				assert.Contains(t, unit.Contents, fmt.Sprintf("What=192.168.%d.10:%s\n", vlan, cfg.Cluster.NFSTrunk.Export), unit.Name)
				assert.Contains(t, unit.Contents, "Options=vers=4.2,ro,noatime,nconnect=4,max_connect=16\n", unit.Name)
			}
		})
	}
}

func TestRenderIgnitionIncludesStorageNetworkdUnits(t *testing.T) {
	cases := []struct {
		name        string
		hostOctet   string
		storageNICs []config.StorageNIC
	}{
		{name: "k8s-0", hostOctet: "20", storageNICs: []config.StorageNIC{
			{VLAN: 1201, MAC: "BC:24:11:3B:E0:50", IP: "192.168.201.20/24"},
			{VLAN: 1202, MAC: "BC:24:11:ED:F6:B6", IP: "192.168.202.20/24"},
			{VLAN: 1203, MAC: "BC:24:11:FF:50:81", IP: "192.168.203.20/24"},
			{VLAN: 1204, MAC: "BC:24:11:FB:16:76", IP: "192.168.204.20/24"},
		}},
		{name: "k8s-1", hostOctet: "21", storageNICs: []config.StorageNIC{
			{VLAN: 1201, MAC: "BC:24:11:6B:64:25", IP: "192.168.201.21/24"},
			{VLAN: 1202, MAC: "BC:24:11:A4:6E:42", IP: "192.168.202.21/24"},
			{VLAN: 1203, MAC: "BC:24:11:8C:C8:43", IP: "192.168.203.21/24"},
			{VLAN: 1204, MAC: "BC:24:11:D1:C4:BE", IP: "192.168.204.21/24"},
		}},
		{name: "k8s-2", hostOctet: "22", storageNICs: []config.StorageNIC{
			{VLAN: 1201, MAC: "BC:24:11:B3:CD:67", IP: "192.168.201.22/24"},
			{VLAN: 1202, MAC: "BC:24:11:41:2D:40", IP: "192.168.202.22/24"},
			{VLAN: 1203, MAC: "BC:24:11:F6:D9:1D", IP: "192.168.203.22/24"},
			{VLAN: 1204, MAC: "BC:24:11:63:50:11", IP: "192.168.204.22/24"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := sampleEnv()
			env.NodeName = tc.name
			env.StorageNICs = tc.storageNICs

			first, err := RenderIgnition(env)
			require.NoError(t, err)
			second, err := RenderIgnition(env)
			require.NoError(t, err)
			assert.Equal(t, first, second, "re-rendering the same node must be byte-stable")

			for _, nic := range env.StorageNICs {
				path := "/etc/systemd/network/30-stor" + fmt.Sprint(nic.VLAN) + ".network"
				want := "[Match]\nMACAddress=" + nic.MAC + "\n\n[Link]\nMTUBytes=9000\n\n[Network]\nAddress=" + nic.IP + "\nLinkLocalAddressing=no\nLLDP=no\n"
				assert.Equal(t, want, ignitionFileContent(t, first, path), path)
				assert.Contains(t, nic.IP, "."+tc.hostOctet+"/24")
			}
		})
	}
}

func TestRenderIgnitionWithoutStorageNICsKeepsUnitsAbsent(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	assert.NotContains(t, string(ign), "30-stor")
}

func ignitionFileContent(t *testing.T, ign []byte, path string) string {
	t.Helper()
	var doc struct {
		Storage struct {
			Files []struct {
				Path     string `json:"path"`
				Contents struct {
					Compression string `json:"compression"`
					Source      string `json:"source"`
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
			if f.Contents.Compression == "gzip" {
				reader, err := gzip.NewReader(bytes.NewReader(decoded))
				require.NoError(t, err)
				decoded, err = io.ReadAll(reader)
				require.NoError(t, err)
				require.NoError(t, reader.Close())
			}
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

type ignitionSystemdUnit struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Contents string `json:"contents"`
}

func ignitionSystemdUnits(t *testing.T, ign []byte) map[string]ignitionSystemdUnit {
	t.Helper()
	var doc struct {
		Systemd struct {
			Units []ignitionSystemdUnit `json:"units"`
		} `json:"systemd"`
	}
	require.NoError(t, json.Unmarshal(ign, &doc))
	units := make(map[string]ignitionSystemdUnit, len(doc.Systemd.Units))
	for _, unit := range doc.Systemd.Units {
		units[unit.Name] = unit
	}
	return units
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

// TestRenderIgnitionPinsMacvlanMasters asserts the secondary NICs land in the
// rendered Ignition as ADDRESS-LESS, MAC-pinned links.
//
// Both properties are load-bearing and were regressions in production:
//   - the name pin exists because the NetworkAttachmentDefinitions reference
//     `master: eth1` / `master: eth2`, which a machine-type rename would break;
//   - DHCP=no exists because Flatcar's catch-all eth* rule DHCP'd eth1 on first
//     hotplug and installed a SECOND default route via the IoT gateway at the
//     same metric as eth0's, ECMP-balancing control-plane egress onto VLAN 20.
func TestRenderIgnitionPinsMacvlanMasters(t *testing.T) {
	env := sampleEnv()
	out, err := RenderIgnition(env)
	if err != nil {
		t.Fatalf("RenderIgnition: %v", err)
	}
	got := string(out)

	// Paths appear literally in the Ignition JSON.
	for _, want := range []string{
		"10-eth1.link", "10-eth2.link",
		"15-eth1-iot.network", "15-eth2-vpn.network",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered ignition missing path %q", want)
		}
	}

	// File CONTENTS are data-URL encoded by butane, so decode before asserting.
	decoded := decodeIgnitionContents(t, got)
	for _, want := range []string{env.NodeMACIoT, env.NodeMACVPN} {
		if !strings.Contains(decoded, want) {
			t.Errorf("decoded ignition contents missing MAC %q", want)
		}
	}
	// Exactly two DHCP=no stanzas: eth1 and eth2. eth0 must stay DHCP=yes, or
	// the node loses its primary address.
	if n := strings.Count(decoded, "DHCP=no"); n != 2 {
		t.Errorf("expected exactly 2 DHCP=no stanzas (eth1, eth2), got %d", n)
	}
	if !strings.Contains(decoded, "DHCP=yes") {
		t.Error("eth0 must still use DHCP")
	}
}

// decodeIgnitionContents concatenates every decoded data: URL in an Ignition
// config so tests can assert on real file bodies rather than base64.
func decodeIgnitionContents(t *testing.T, ign string) string {
	t.Helper()
	var sb strings.Builder
	for _, m := range regexp.MustCompile(`data:[^"]*`).FindAllString(ign, -1) {
		if i := strings.Index(m, "base64,"); i >= 0 {
			if b, err := base64.StdEncoding.DecodeString(m[i+len("base64,"):]); err == nil {
				sb.Write(b)
				continue
			}
		}
		if i := strings.Index(m, ","); i >= 0 {
			if u, err := url.PathUnescape(m[i+1:]); err == nil {
				sb.WriteString(u)
			}
		}
	}
	return sb.String()
}
