package fcos

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/config"
	"homeops-cli/internal/flatcar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// charNodes / charStorageNICs / charEnv are the SAME fixture the Flatcar
// characterization goldens use (internal/flatcar/characterization_test.go), so
// the FCOS and Flatcar goldens describe the same three nodes and can be diffed
// to review exactly what the OS migration changes.
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

func charEnv(name, ip, mac string) NodeEnv {
	env := flatcar.NodeEnv{
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
	return NodeEnv{NodeEnv: env}
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

func TestFCOSRenderCharacterization(t *testing.T) {
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
}

// TestFCOSGoldenStorageFabricMatchesFlatcar pins, per node, that the FCOS
// Ignition configures the four NVMe-oF storage NICs with EXACTLY the addresses
// and MTU the Flatcar Ignition gives the same node — as NetworkManager
// keyfiles (0600, static, no gateway, IPv6 off), never systemd-networkd files.
func TestFCOSGoldenStorageFabricMatchesFlatcar(t *testing.T) {
	restore := config.SetForTesting(&config.Config{Cluster: config.ClusterConfig{Name: "home-ops-cluster"}})
	defer restore()

	for _, n := range charNodes {
		t.Run(n.name, func(t *testing.T) {
			env := charEnv(n.name, n.ip, n.mac)
			fcosIgn, err := RenderIgnition(env)
			require.NoError(t, err)
			flatcarIgn, err := flatcar.RenderIgnition(env.NodeEnv)
			require.NoError(t, err)
			fcosFiles := ignitionFiles(t, fcosIgn)
			flatcarFiles := ignitionFiles(t, flatcarIgnDecompressed(t, flatcarIgn))

			require.Len(t, env.StorageNICs, 4)
			for _, nic := range env.StorageNICs {
				networkd := flatcarFiles[fmt.Sprintf("/etc/systemd/network/30-stor%d.network", nic.VLAN)].Contents
				require.Contains(t, networkd, "Address="+nic.IP+"\n", "Flatcar reference render")
				require.Contains(t, networkd, "MTUBytes=9000\n")

				keyfile, ok := fcosFiles[fmt.Sprintf("/etc/NetworkManager/system-connections/30-stor%d.nmconnection", nic.VLAN)]
				require.True(t, ok, "missing storage keyfile for VLAN %d", nic.VLAN)
				assert.Equal(t, 0o600, keyfile.Mode)
				assert.Contains(t, keyfile.Contents, "mac-address="+nic.MAC+"\n")
				assert.Contains(t, keyfile.Contents, "mtu=9000\n")
				assert.Contains(t, keyfile.Contents, "method=manual\naddress1="+nic.IP+"\n")
				assert.Contains(t, keyfile.Contents, "never-default=true\n")
				assert.Contains(t, keyfile.Contents, "[ipv6]\nmethod=disabled\n")
				assert.NotContains(t, keyfile.Contents, "gateway")
				assert.NotContains(t, keyfile.Contents, ",192.168.", "address1 must carry no gateway")
			}
			for path := range fcosFiles {
				assert.False(t, strings.HasSuffix(path, ".network"), path)
			}
		})
	}
}

// TestFCOSGoldenKeepsScratchDiskFilesystem pins the node-local NVMe scratch
// disk handling to the Flatcar template byte-for-byte: the disk carries live
// OpenEBS hostpath PVs across a boot-disk rebuild, so it must never be wiped,
// and it must be mounted at /var/mnt/nvme-hostpath on every boot by the
// explicit mount unit (the Flatcar template does not use with_mount_unit).
func TestFCOSGoldenKeepsScratchDiskFilesystem(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	defer restore()
	env := charEnv("k8s-0", "192.168.122.10", "00:a0:98:28:c8:83")
	fcosIgn, err := RenderIgnition(env)
	require.NoError(t, err)
	flatcarIgn, err := flatcar.RenderIgnition(env.NodeEnv)
	require.NoError(t, err)

	type storageDoc struct {
		Storage struct {
			Filesystems []map[string]any `json:"filesystems"`
			Directories []map[string]any `json:"directories"`
		} `json:"storage"`
	}
	var fcosDoc, flatcarDoc storageDoc
	require.NoError(t, json.Unmarshal(fcosIgn, &fcosDoc))
	require.NoError(t, json.Unmarshal(flatcarIgn, &flatcarDoc))
	assert.Equal(t, flatcarDoc.Storage.Filesystems, fcosDoc.Storage.Filesystems)
	assert.Equal(t, flatcarDoc.Storage.Directories, fcosDoc.Storage.Directories)
	require.Len(t, fcosDoc.Storage.Filesystems, 1)
	fs := fcosDoc.Storage.Filesystems[0]
	assert.Equal(t, "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi4", fs["device"])
	assert.Equal(t, "ext4", fs["format"])
	assert.Equal(t, "openebs-nvme", fs["label"])
	assert.Equal(t, false, fs["wipeFilesystem"], "the scratch disk must never be reformatted")

	mount := ignitionSystemdUnits(t, fcosIgn)[`var-mnt-nvme\x2dhostpath.mount`]
	require.NotNil(t, mount.Enabled)
	assert.True(t, *mount.Enabled)
	assert.Equal(t, ignitionSystemdUnits(t, flatcarIgn)[`var-mnt-nvme\x2dhostpath.mount`].Contents, mount.Contents)
	assert.Contains(t, mount.Contents, "What=/dev/disk/by-label/openebs-nvme\n")
	assert.Contains(t, mount.Contents, "Where=/var/mnt/nvme-hostpath\n")
	assert.Contains(t, mount.Contents, "WantedBy=local-fs.target\n")
	assert.Equal(t, "k8s-0", ignitionFileContent(t, fcosIgn, "/etc/hostname"), "node names are unchanged")
}

// flatcarIgnDecompressed re-encodes a (possibly gzip-compressed) Flatcar
// Ignition so ignitionFiles can read it; the Flatcar renderer compresses.
func flatcarIgnDecompressed(t *testing.T, ign []byte) []byte {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(ign, &doc))
	storage := doc["storage"].(map[string]any)
	for _, raw := range storage["files"].([]any) {
		file := raw.(map[string]any)
		contents := file["contents"].(map[string]any)
		if contents["compression"] != "gzip" {
			continue
		}
		source := contents["source"].(string)
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(source, "data:;base64,"))
		require.NoError(t, err)
		reader, err := gzip.NewReader(bytes.NewReader(data))
		require.NoError(t, err)
		plain, err := io.ReadAll(reader)
		require.NoError(t, err)
		contents["compression"] = ""
		contents["source"] = "data:;base64," + base64.StdEncoding.EncodeToString(plain)
	}
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return out
}
