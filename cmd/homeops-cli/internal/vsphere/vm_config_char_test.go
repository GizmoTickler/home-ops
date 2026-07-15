package vsphere

// Characterization test: captures the fully-resolved vSphere VM configuration
// for the two scenarios that matter for the config-single-source refactor:
//
//   default: no homeops.yaml (built-in defaults only)
//   repo:    this repository's homeops.yaml topology (shared VM overrides)
//
// The golden files under testdata/ are byte-for-byte snapshots of the CURRENT
// behavior. The refactor MUST keep them identical. Regenerate intentionally
// with UPDATE_GOLDEN=1 go test ./internal/vsphere/ -run Characterize.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	homeopscfg "homeops-cli/internal/config"

	"github.com/stretchr/testify/require"
)

func vsphereCharNames() []string { return []string{"k8s-0", "k8s-1", "k8s-2", "k8s-3", "demo"} }

func dumpVSphereConfigs() string {
	var b strings.Builder
	fmt.Fprintf(&b, "DefaultISOPath=%q\n", DefaultISOPath())
	fmt.Fprintf(&b, "BuildISOPath(datastore1,vmware-amd64.iso)=%q\n", BuildISOPath("datastore1", "vmware-amd64.iso"))
	for _, name := range vsphereCharNames() {
		fmt.Fprintf(&b, "\n== %s ==\n", name)
		if nc, ok := GetK8sNodeConfig(name); ok {
			fmt.Fprintf(&b, "NodeConfig: rdm=%q pci=%q pciHex=%q mac=%q affinity=%q boot=%q\n",
				nc.RDMPath, nc.PCIDevice, nc.PCIDeviceHex, nc.MacAddress, nc.CPUAffinity, nc.BootDatastore)
		} else {
			fmt.Fprintf(&b, "NodeConfig: <none>\n")
		}
		def := GetDefaultVMConfig(name)
		fmt.Fprintf(&b, "Default: mem=%d vcpu=%d disk=%d openebs=%d boot=%q openebsDS=%q isoDS=%q net=%q coresPerSocket=%d\n",
			def.Memory, def.VCPUs, def.DiskSize, def.OpenEBSSize, def.BootDatastore, def.OpenEBSDatastore, def.ISODatastore, def.Network, def.CoresPerSocket)
		k := GetK8sVMConfig(name)
		fmt.Fprintf(&b, "K8s: mem=%d vcpu=%d disk=%d openebs=%d boot=%q openebsDS=%q isoDS=%q iso=%q net=%q mac=%q affinity=%q rdm=%q pci=%q pciHex=%q coresPerSocket=%d powerOn=%t sriov=%t\n",
			k.Memory, k.VCPUs, k.DiskSize, k.OpenEBSSize, k.BootDatastore, k.OpenEBSDatastore, k.ISODatastore, k.ISO, k.Network, k.MacAddress, k.CPUAffinity, k.RDMPath, k.PCIDevice, k.PCIDeviceHex, k.CoresPerSocket, k.PowerOn, k.EnableSRIOV)
	}
	return b.String()
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- test-local golden path
	require.NoError(t, err)
	require.Equal(t, string(want), got, "golden %s drifted; behavior is NOT preserved", name)
}

// repoScenarioConfig builds a Config equivalent to this repository's
// homeops.yaml cluster topology + hypervisor settings (secrets omitted; they
// are irrelevant to VM composition). SetForTesting runs applyDefaults so the
// embedded defaults merge exactly as a real load would.
func repoScenarioConfig() *homeopscfg.Config {
	numa0, numa1 := 0, 1
	node := func(name, ip string, vmid int, mac, affinity string, numa *int, disk string) homeopscfg.Node {
		return homeopscfg.Node{
			Name: name,
			IP:   ip,
			VM: homeopscfg.VMProfile{
				VMID:           vmid,
				Mac:            mac,
				BootStorage:    "nvme-mirror",
				OpenEBSStorage: "nvmeof-vmdata",
				CPUAffinity:    affinity,
				NUMANode:       numa,
				Ceph:           homeopscfg.CephDisk{Mode: "passthrough", DiskByID: disk},
			},
		}
	}
	return &homeopscfg.Config{
		Cluster: homeopscfg.ClusterConfig{
			Name:            "home-kubernetes",
			ControlPlaneVIP: "192.168.123.253",
			NodeInterface:   "eth0",
			Nodes: []homeopscfg.Node{
				node("k8s-0", "192.168.122.10", 200, "00:a0:98:28:c8:83", "0-7,32-39", &numa0, "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN"),
				node("k8s-1", "192.168.122.11", 201, "00:a0:98:1a:f3:72", "16-23,48-55", &numa1, "ata-INTEL_SSDSC2BB012T7_PHDV650101691P2DGN"),
				node("k8s-2", "192.168.122.12", 202, "00:a0:98:3e:6c:22", "8-15,40-47", &numa0, "ata-INTEL_SSDSC2BB012T7_PHDV650101LU1P2DGN"),
			},
		},
		Hypervisors: homeopscfg.HypervisorsConfig{
			Default: "proxmox",
			Proxmox: homeopscfg.ProxmoxConfig{
				SnippetsDir: "/var/lib/vz/snippets",
				VM: homeopscfg.VMDefaults{
					MemoryMB: 98304, Cores: 16, BootDiskGB: 100, OpenEBSDiskGB: 800,
					NetworkBridge: "vmbr0", NetworkMTU: 9000, VLANID: 999,
				},
			},
			TrueNAS: homeopscfg.TrueNASConfig{
				ISODir: "/mnt/flashstor/ISO", ISOFile: "metal-amd64.iso", SpiceHost: "192.168.120.10",
			},
		},
	}
}

func TestVSphereVMConfigCharacterizeDefault(t *testing.T) {
	homeopscfg.ResetForTesting()
	t.Cleanup(homeopscfg.ResetForTesting)
	assertGolden(t, "vsphere_char_default.txt", dumpVSphereConfigs())
}

func TestVSphereVMConfigCharacterizeRepo(t *testing.T) {
	restore := homeopscfg.SetForTesting(repoScenarioConfig())
	t.Cleanup(restore)
	assertGolden(t, "vsphere_char_repo.txt", dumpVSphereConfigs())
}
