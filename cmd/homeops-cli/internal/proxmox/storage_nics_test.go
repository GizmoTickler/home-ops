package proxmox

import (
	"os"
	"path/filepath"
	"testing"

	homeopscfg "homeops-cli/internal/config"

	proxmoxlib "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFlatcarStorageNICOptionsExactAndStable(t *testing.T) {
	manager := &VMManager{}
	config := VMConfig{
		Name:          "k8s-0",
		Memory:        1024,
		Cores:         2,
		Sockets:       1,
		BootStorage:   "nvme-mirror",
		NetworkBridge: "vmbr0",
		NetworkMTU:    9000,
		NetworkQueues: 8,
		StorageNICs: []homeopscfg.StorageNIC{
			{VLAN: 1204, MAC: "BC:24:11:FB:16:76", IP: "192.168.204.20/24"},
			{VLAN: 1202, MAC: "BC:24:11:ED:F6:B6", IP: "192.168.202.20/24"},
			{VLAN: 1201, MAC: "BC:24:11:3B:E0:50", IP: "192.168.201.20/24"},
			{VLAN: 1203, MAC: "BC:24:11:FF:50:81", IP: "192.168.203.20/24"},
		},
	}

	first := manager.buildFlatcarVMOptions(config)
	options := make(map[string]any, len(first))
	for _, option := range first {
		options[option.Name] = option.Value
	}
	assert.Equal(t, "virtio=BC:24:11:3B:E0:50,bridge=vmbr0,tag=1201,mtu=9000,queues=8", options["net3"])
	assert.Equal(t, "virtio=BC:24:11:ED:F6:B6,bridge=vmbr0,tag=1202,mtu=9000,queues=8", options["net4"])
	assert.Equal(t, "virtio=BC:24:11:FF:50:81,bridge=vmbr0,tag=1203,mtu=9000,queues=8", options["net5"])
	assert.Equal(t, "virtio=BC:24:11:FB:16:76,bridge=vmbr0,tag=1204,mtu=9000,queues=8", options["net6"])

	for i := 0; i < 20; i++ {
		require.Equal(t, first, manager.buildFlatcarVMOptions(config), "render %d", i)
	}
}

func TestBuildFlatcarVMOptionsWithoutStorageNICsIsBackwardCompatible(t *testing.T) {
	options := (&VMManager{}).buildFlatcarVMOptions(VMConfig{
		Name: "legacy", Memory: 1024, Cores: 1, Sockets: 1,
		BootStorage: "local", NetworkBridge: "vmbr0",
	})

	for _, option := range options {
		assert.NotContains(t, []string{"net3", "net4", "net5", "net6"}, option.Name)
	}
}

func TestBuildFlatcarStorageNICOptionsOmitsNonPositiveMTUAndQueues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mtu    int
		queues int
	}{
		{name: "zero", mtu: 0, queues: 0},
		{name: "negative", mtu: -1, queues: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := storageNICs(VMConfig{
				NetworkBridge: "vmbr0",
				NetworkMTU:    tc.mtu,
				NetworkQueues: tc.queues,
				StorageNICs: []homeopscfg.StorageNIC{
					{VLAN: 1201, MAC: "02:00:00:00:12:01", IP: "192.168.201.20/24"},
				},
			})
			require.Len(t, options, 1)
			assert.Equal(t, "virtio=02:00:00:00:12:01,bridge=vmbr0,tag=1201", options[0].value)
		})
	}
}

func TestConfiguredBaseNICQueueOverrides(t *testing.T) {
	legacyConfig := VMConfig{
		Name: "legacy", Memory: 1024, Cores: 2, Sockets: 1,
		NetworkBridge: "vmbr0", NetworkMTU: 9000, NetworkQueues: 8, VLANID: 999,
		MacAddress: "02:00:00:00:00:10", MacAddressIoT: "02:00:00:00:00:11", MacAddressVPN: "02:00:00:00:00:12",
		VLANIDIoT: 20, VLANIDVPN: 90, SecondaryMTU: 1500,
	}
	legacyOptions := optionValues((&VMManager{}).buildFlatcarVMOptions(legacyConfig))
	assert.Equal(t, "virtio=02:00:00:00:00:10,bridge=vmbr0,mtu=9000,queues=8,tag=999", legacyOptions["net0"])
	assert.Equal(t, "virtio=02:00:00:00:00:11,bridge=vmbr0,mtu=1500,tag=20", legacyOptions["net1"])
	assert.Equal(t, "virtio=02:00:00:00:00:12,bridge=vmbr0,mtu=1500,tag=90", legacyOptions["net2"])

	path := filepath.Join(t.TempDir(), "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
hypervisors:
  proxmox:
    vm:
      network_queue_overrides:
        net0: 16
        net2: 8
`), 0o600))
	cfg, err := homeopscfg.LoadFile(path)
	require.NoError(t, err)
	restore := homeopscfg.SetForTesting(cfg)
	t.Cleanup(restore)

	vmConfig := GetDefaultVMConfig()
	vmConfig.Name = "k8s-0"
	vmConfig.Memory = 1024
	vmConfig.Cores = 2
	vmConfig.Sockets = 1
	vmConfig.MacAddress = "02:00:00:00:00:10"
	vmConfig.MacAddressIoT = "02:00:00:00:00:11"
	vmConfig.MacAddressVPN = "02:00:00:00:00:12"
	vmConfig.VLANIDIoT = 20
	vmConfig.VLANIDVPN = 90
	vmConfig.SecondaryMTU = 1500

	optionMap := optionValues((&VMManager{}).buildFlatcarVMOptions(vmConfig))
	assert.Equal(t, "virtio=02:00:00:00:00:10,bridge=vmbr0,mtu=9000,queues=16,tag=999", optionMap["net0"])
	assert.Equal(t, "virtio=02:00:00:00:00:11,bridge=vmbr0,mtu=1500,tag=20", optionMap["net1"])
	assert.Equal(t, "virtio=02:00:00:00:00:12,bridge=vmbr0,mtu=1500,queues=8,tag=90", optionMap["net2"])
}

func TestRepositoryBaseNICOptionsMatchLivePVE(t *testing.T) {
	cfg, err := homeopscfg.LoadFile(filepath.Join("..", "..", "..", "..", "homeops.yaml"))
	require.NoError(t, err)
	restore := homeopscfg.SetForTesting(cfg)
	t.Cleanup(restore)

	wantMACs := map[string][3]string{
		"k8s-0": {"00:a0:98:28:c8:83", "BC:24:11:B9:55:83", "BC:24:11:33:4C:37"},
		"k8s-1": {"00:a0:98:1a:f3:72", "BC:24:11:24:80:8B", "BC:24:11:C6:89:07"},
		"k8s-2": {"00:a0:98:3e:6c:22", "BC:24:11:60:19:38", "BC:24:11:B1:02:55"},
	}
	for name, macs := range wantMACs {
		t.Run(name, func(t *testing.T) {
			node, found := GetFlatcarNodeConfig(name)
			require.True(t, found)
			vmConfig := GetDefaultVMConfig()
			vmConfig.Name = name
			vmConfig.MacAddress = node.MacAddress
			vmConfig.MacAddressIoT = node.MacAddressIoT
			vmConfig.MacAddressVPN = node.MacAddressVPN
			vmConfig.VLANIDIoT = 20
			vmConfig.VLANIDVPN = 90
			vmConfig.SecondaryMTU = 1500

			options := optionValues((&VMManager{}).buildFlatcarVMOptions(vmConfig))
			assert.Equal(t, "virtio="+macs[0]+",bridge=vmbr0,mtu=9000,queues=16,tag=999", options["net0"])
			assert.Equal(t, "virtio="+macs[1]+",bridge=vmbr0,mtu=1500,tag=20", options["net1"])
			assert.Equal(t, "virtio="+macs[2]+",bridge=vmbr0,mtu=1500,queues=8,tag=90", options["net2"])
		})
	}
}

func optionValues(options []proxmoxlib.VirtualMachineOption) map[string]any {
	out := make(map[string]any, len(options))
	for _, option := range options {
		out[option.Name] = option.Value
	}
	return out
}
