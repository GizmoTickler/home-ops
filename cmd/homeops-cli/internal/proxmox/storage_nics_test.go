package proxmox

import (
	"testing"

	homeopscfg "homeops-cli/internal/config"

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
