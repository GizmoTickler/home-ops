package proxmox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFlatcarNodeConfig(t *testing.T) {
	cfg, ok := GetFlatcarNodeConfig("k8s-1")
	require.True(t, ok)
	assert.Equal(t, 201, cfg.VMID)
	assert.Equal(t, "192.168.122.11", cfg.NodeIP)
	assert.Equal(t, "00:a0:98:1a:f3:72", cfg.MacAddress)
	assert.Equal(t, "16-23,48-55", cfg.CPUAffinity)
	assert.Equal(t, 1, cfg.NUMANode)

	_, ok = GetFlatcarNodeConfig("does-not-exist")
	assert.False(t, ok)
}

func TestFlatcarNodeConfigsMatchTalosSlots(t *testing.T) {
	// VMIDs/MACs/affinity must match the Talos slots they replace.
	for name, fc := range FlatcarNodeConfigs {
		tc, ok := TalosNodeConfigs[name]
		require.True(t, ok, "flatcar node %s has no matching talos slot", name)
		assert.Equal(t, tc.VMID, fc.VMID, "VMID mismatch for %s", name)
		assert.Equal(t, tc.MacAddress, fc.MacAddress, "MAC mismatch for %s", name)
		assert.Equal(t, tc.CPUAffinity, fc.CPUAffinity, "affinity mismatch for %s", name)
		assert.Equal(t, tc.NUMANode, fc.NUMANode, "NUMA mismatch for %s", name)
		// Boot storage INTENTIONALLY differs from Talos: Flatcar boots from the
		// nvme-mirror ZFS RAID1 built during the cutover, not per-node nvme1/nvme2.
		assert.Equal(t, "nvme-mirror", fc.BootStorage, "flatcar should boot from nvme-mirror for %s", name)
		assert.Equal(t, tc.CephDiskByID, fc.CephDiskByID, "ceph disk mismatch for %s", name)
	}
}

func TestBuildFlatcarVMOptionsImportPath(t *testing.T) {
	manager := &VMManager{}
	options := manager.buildFlatcarVMOptions(VMConfig{
		Name:           "k8s-0",
		Memory:         98304,
		Cores:          16,
		Sockets:        1,
		CPUType:        "host",
		CPUAffinity:    "0-7,32-39",
		NUMAEnabled:    true,
		NUMANode:       0,
		BootDiskSize:   200,
		BootStorage:    "nvme1",
		OpenEBSSize:    800,
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL",
		NetworkBridge:  "vmbr0",
		NetworkMTU:     9000,
		NetworkQueues:  8,
		VLANID:         999,
		MacAddress:     "00:a0:98:28:c8:83",
		SCSIController: "virtio-scsi-single",
		IOThread:       true,
		Discard:        true,
		BIOS:           "ovmf",
		WatchdogModel:  "i6300esb",
		WatchdogAction: "reset",
		AgentEnabled:   true,
		EFIDiskStorage: "nvme1",
		ImageDiskPath:  "/var/lib/vz/template/flatcar.img",
		IgnitionPath:   "/var/lib/vz/snippets/ignition-k8s-0.json",
	})

	optionMap := make(map[string]interface{}, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt.Value
	}

	// scsi0 imports the Flatcar image.
	assert.Equal(t, "nvme1:200,import-from=/var/lib/vz/template/flatcar.img,discard=on,iothread=1", optionMap["scsi0"])
	// OpenEBS + Ceph preserved.
	assert.Equal(t, "nvmeof-vmdata:800,discard=on,iothread=1", optionMap["scsi1"])
	assert.Equal(t, "/dev/disk/by-id/ata-INTEL,discard=on,iothread=1", optionMap["scsi2"])
	// Boots from disk, NOT from a CD-ROM. No ide2 set.
	assert.Equal(t, "order=scsi0", optionMap["boot"])
	_, hasISO := optionMap["ide2"]
	assert.False(t, hasISO, "flatcar VM must not attach an install ISO")
	// UEFI + network jumbo + MAC + vlan.
	assert.Equal(t, "ovmf", optionMap["bios"])
	assert.Equal(t, "nvme1:1,efitype=4m,pre-enrolled-keys=0", optionMap["efidisk0"])
	assert.Contains(t, optionMap["net0"], "virtio=00:a0:98:28:c8:83,bridge=vmbr0")
	assert.Contains(t, optionMap["net0"], "mtu=9000")
	assert.Contains(t, optionMap["net0"], "tag=999")
	// Ignition injected via fw_cfg.
	assert.Equal(t, "-fw_cfg name=opt/org.flatcar-linux/config,file=/var/lib/vz/snippets/ignition-k8s-0.json", optionMap["args"])
	// NUMA bind.
	assert.Contains(t, optionMap["numa0"], "hostnodes=0")
}

func TestBuildFlatcarVMOptionsExistingVolume(t *testing.T) {
	manager := &VMManager{}
	options := manager.buildFlatcarVMOptions(VMConfig{
		Name:        "k8s-2",
		Memory:      1024,
		Cores:       2,
		Sockets:     1,
		BootStorage: "nvme1",
		ImageVolume: "nvme1:vm-202-disk-0",
		IOThread:    true,
		Discard:     true,
	})
	optionMap := make(map[string]interface{}, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt.Value
	}
	assert.Equal(t, "nvme1:vm-202-disk-0,discard=on,iothread=1", optionMap["scsi0"])
}
