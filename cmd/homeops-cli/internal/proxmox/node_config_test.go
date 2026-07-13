package proxmox

import (
	"testing"

	homeopscfg "homeops-cli/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeConfigAccessorsCharacterizeEmbeddedDefaults(t *testing.T) {
	homeopscfg.ResetForTesting()
	t.Cleanup(homeopscfg.ResetForTesting)

	expectedTalos := map[string]TalosNodeConfig{
		"k8s-0": {
			Name:           "k8s-0",
			VMID:           200,
			BootStorage:    "nvme1",
			OpenEBSStorage: "nvmeof-vmdata",
			CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN",
			CPUAffinity:    "0-7,32-39",
			NUMANode:       0,
			MacAddress:     "00:a0:98:28:c8:83",
		},
		"k8s-1": {
			Name:           "k8s-1",
			VMID:           201,
			BootStorage:    "nvme2",
			OpenEBSStorage: "nvmeof-vmdata",
			CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101691P2DGN",
			CPUAffinity:    "16-23,48-55",
			NUMANode:       1,
			MacAddress:     "00:a0:98:1a:f3:72",
		},
		"k8s-2": {
			Name:           "k8s-2",
			VMID:           202,
			BootStorage:    "nvme1",
			OpenEBSStorage: "nvmeof-vmdata",
			CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101LU1P2DGN",
			CPUAffinity:    "8-15,40-47",
			NUMANode:       0,
			MacAddress:     "00:a0:98:3e:6c:22",
		},
	}
	expectedFlatcar := map[string]FlatcarNodeConfig{
		"k8s-0": {
			Name:           "k8s-0",
			VMID:           200,
			NodeIP:         "192.168.122.10",
			BootStorage:    "nvme-mirror",
			OpenEBSStorage: "nvmeof-vmdata",
			CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN",
			CPUAffinity:    "0-7,32-39",
			NUMANode:       0,
			MacAddress:     "00:a0:98:28:c8:83",
		},
		"k8s-1": {
			Name:           "k8s-1",
			VMID:           201,
			NodeIP:         "192.168.122.11",
			BootStorage:    "nvme-mirror",
			OpenEBSStorage: "nvmeof-vmdata",
			CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101691P2DGN",
			CPUAffinity:    "16-23,48-55",
			NUMANode:       1,
			MacAddress:     "00:a0:98:1a:f3:72",
		},
		"k8s-2": {
			Name:           "k8s-2",
			VMID:           202,
			NodeIP:         "192.168.122.12",
			BootStorage:    "nvme-mirror",
			OpenEBSStorage: "nvmeof-vmdata",
			CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101LU1P2DGN",
			CPUAffinity:    "8-15,40-47",
			NUMANode:       0,
			MacAddress:     "00:a0:98:3e:6c:22",
		},
	}

	for name, want := range expectedTalos {
		got, ok := GetTalosNodeConfig(name)
		require.True(t, ok, "talos %s exists", name)
		assert.Equal(t, want, got)
	}
	for name, want := range expectedFlatcar {
		got, ok := GetFlatcarNodeConfig(name)
		require.True(t, ok, "flatcar %s exists", name)
		assert.Equal(t, want, got)
	}
}

func TestNodeConfigAccessorsApplySharedAndProviderSpecificConfig(t *testing.T) {
	numa := 1
	restore := homeopscfg.SetForTesting(&homeopscfg.Config{
		Cluster: homeopscfg.ClusterConfig{
			Nodes: []homeopscfg.Node{
				{
					Name: "k8s-0",
					IP:   "10.10.10.10",
					VM: homeopscfg.VMProfile{
						VMID:           250,
						Mac:            "02:00:00:00:00:50",
						BootStorage:    "shared-boot",
						OpenEBSStorage: "shared-openebs",
						CPUAffinity:    "1-2",
						NUMANode:       &numa,
						Ceph:           homeopscfg.CephDisk{Mode: "virtual", SizeGB: 123, Storage: "shared-ceph"},
						Providers: homeopscfg.ProviderVMProfiles{
							Talos:   homeopscfg.ProviderVMProfile{BootStorage: "talos-boot"},
							Flatcar: homeopscfg.ProviderVMProfile{BootStorage: "flatcar-boot"},
						},
					},
				},
				{
					Name: "k8s-9",
					IP:   "10.10.10.19",
					VM: homeopscfg.VMProfile{
						VMID: 209,
						Mac:  "02:00:00:00:00:09",
						Providers: homeopscfg.ProviderVMProfiles{
							Flatcar: homeopscfg.ProviderVMProfile{BootStorage: "edge-flatcar"},
						},
					},
				},
			},
		},
	})
	defer restore()

	talos, ok := GetTalosNodeConfig("k8s-0")
	require.True(t, ok)
	assert.Equal(t, 250, talos.VMID)
	assert.Equal(t, "talos-boot", talos.BootStorage)
	assert.Equal(t, "shared-openebs", talos.OpenEBSStorage)
	assert.Equal(t, "virtual", talos.CephMode)
	assert.Equal(t, 123, talos.CephDiskGB)
	assert.Equal(t, "shared-ceph", talos.CephStorage)
	assert.Equal(t, "02:00:00:00:00:50", talos.MacAddress)

	flatcar, ok := GetFlatcarNodeConfig("k8s-0")
	require.True(t, ok)
	assert.Equal(t, "10.10.10.10", flatcar.NodeIP)
	assert.Equal(t, "flatcar-boot", flatcar.BootStorage)
	assert.Equal(t, "shared-openebs", flatcar.OpenEBSStorage)

	newNode, ok := GetFlatcarNodeConfig("k8s-9")
	require.True(t, ok)
	assert.Equal(t, 209, newNode.VMID)
	assert.Equal(t, "10.10.10.19", newNode.NodeIP)
	assert.Equal(t, "edge-flatcar", newNode.BootStorage)
	assert.Equal(t, "02:00:00:00:00:09", newNode.MacAddress)
}

func TestNodeConfigAccessorsReturnFalseForUnknownNode(t *testing.T) {
	homeopscfg.ResetForTesting()
	t.Cleanup(homeopscfg.ResetForTesting)

	_, ok := GetTalosNodeConfig("does-not-exist")
	assert.False(t, ok)
	_, ok = GetFlatcarNodeConfig("does-not-exist")
	assert.False(t, ok)
}
