package vsphere

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// diskInfo builds a mo.VirtualMachine with the given virtual disks.
func vmInfoWithDisks(capacitiesKB ...int64) *mo.VirtualMachine {
	var devices []types.BaseVirtualDevice
	for i, kb := range capacitiesKB {
		devices = append(devices, &types.VirtualDisk{
			VirtualDevice: types.VirtualDevice{
				DeviceInfo: &types.Description{Label: labelForDisk(i)},
			},
			CapacityInKB: kb,
		})
	}
	return &mo.VirtualMachine{
		Config: &types.VirtualMachineConfigInfo{
			Hardware: types.VirtualHardware{Device: devices},
		},
	}
}

func labelForDisk(i int) string {
	return "Hard disk " + string(rune('1'+i))
}

func TestVSphereSetVMResources(t *testing.T) {
	t.Run("memory and cpus", func(t *testing.T) {
		client := &fakeVMClient{}
		manager := newTestVMManager(client)
		require.NoError(t, manager.SetVMResources("vm0", 8192, 4))
		require.Len(t, client.reconfigSpecs, 1)
		assert.Equal(t, int64(8192), client.reconfigSpecs[0].MemoryMB)
		assert.Equal(t, int32(4), client.reconfigSpecs[0].NumCPUs)
	})

	t.Run("nothing to change", func(t *testing.T) {
		manager := newTestVMManager(&fakeVMClient{})
		require.ErrorContains(t, manager.SetVMResources("vm0", 0, 0), "nothing to change")
	})
}

func TestSelectVirtualDisk(t *testing.T) {
	disks := virtualDisks(vmInfoWithDisks(40<<20, 100<<20))

	got, err := selectVirtualDisk(disks, "")
	require.NoError(t, err)
	assert.Equal(t, int64(40<<20), got.CapacityInKB)

	got, err = selectVirtualDisk(disks, "boot")
	require.NoError(t, err)
	assert.Equal(t, int64(40<<20), got.CapacityInKB)

	got, err = selectVirtualDisk(disks, "scsi1")
	require.NoError(t, err)
	assert.Equal(t, int64(100<<20), got.CapacityInKB)

	got, err = selectVirtualDisk(disks, "Hard disk 2")
	require.NoError(t, err)
	assert.Equal(t, int64(100<<20), got.CapacityInKB)

	_, err = selectVirtualDisk(disks, "scsi5")
	require.ErrorContains(t, err, "out of range")

	_, err = selectVirtualDisk(disks, "floppy")
	require.ErrorContains(t, err, "no disk matching")

	_, err = selectVirtualDisk(nil, "")
	require.ErrorContains(t, err, "no virtual disks")
}

func TestVSphereResizeVMDisk(t *testing.T) {
	t.Run("grow by delta", func(t *testing.T) {
		client := &fakeVMClient{infoResponse: vmInfoWithDisks(40 << 20)}
		manager := newTestVMManager(client)
		require.NoError(t, manager.ResizeVMDisk("vm0", "boot", "+20G"))
		require.Len(t, client.reconfigSpecs, 1)
		change := client.reconfigSpecs[0].DeviceChange[0].GetVirtualDeviceConfigSpec()
		assert.Equal(t, types.VirtualDeviceConfigSpecOperationEdit, change.Operation)
		disk := change.Device.(*types.VirtualDisk)
		assert.Equal(t, int64(60<<20), disk.CapacityInKB)
	})

	t.Run("shrink refused", func(t *testing.T) {
		client := &fakeVMClient{infoResponse: vmInfoWithDisks(40 << 20)}
		manager := newTestVMManager(client)
		require.ErrorContains(t, manager.ResizeVMDisk("vm0", "boot", "20G"), "only grow")
		assert.Empty(t, client.reconfigSpecs)
	})
}

func TestVSphereSnapshotOps(t *testing.T) {
	client := &fakeVMClient{}
	manager := newTestVMManager(client)

	require.NoError(t, manager.SnapshotVM("vm0", "pre"))
	require.NoError(t, manager.RollbackVM("vm0", "pre"))
	require.NoError(t, manager.DeleteVMSnapshot("vm0", "pre"))

	assert.Equal(t, []string{"pre"}, client.snapsCreated)
	assert.Equal(t, []string{"pre"}, client.snapsReverted)
	assert.Equal(t, []string{"pre"}, client.snapsRemoved)
}

func TestVSphereListVMSnapshots(t *testing.T) {
	t.Run("no snapshots", func(t *testing.T) {
		manager := newTestVMManager(&fakeVMClient{infoResponse: &mo.VirtualMachine{}})
		require.NoError(t, manager.ListVMSnapshots("vm0"))
	})

	t.Run("tree", func(t *testing.T) {
		manager := newTestVMManager(&fakeVMClient{infoResponse: &mo.VirtualMachine{
			Snapshot: &types.VirtualMachineSnapshotInfo{
				RootSnapshotList: []types.VirtualMachineSnapshotTree{{
					Name:       "base",
					CreateTime: time.Now(),
					ChildSnapshotList: []types.VirtualMachineSnapshotTree{{
						Name: "child", CreateTime: time.Now(),
					}},
				}},
			},
		}})
		require.NoError(t, manager.ListVMSnapshots("vm0"))
	})
}

func TestVSphereCloneVM(t *testing.T) {
	client := &fakeVMClient{}
	manager := newTestVMManager(client)
	require.NoError(t, manager.CloneVM("vm0", "vm1"))
	assert.Equal(t, []string{"vm1"}, client.clonedTo)
}

func TestVSphereRestartVM(t *testing.T) {
	client := &fakeVMClient{}
	manager := newTestVMManager(client)
	require.NoError(t, manager.RestartVM("vm0"))
	assert.Equal(t, 1, client.rebooted)
}

func TestVSphereVMIPAddresses(t *testing.T) {
	t.Run("collects unique IPs", func(t *testing.T) {
		manager := newTestVMManager(&fakeVMClient{infoResponse: &mo.VirtualMachine{
			Guest: &types.GuestInfo{
				IpAddress: "10.0.0.5",
				Net: []types.GuestNicInfo{
					{IpAddress: []string{"10.0.0.5", "fe80::1"}},
					{IpAddress: []string{"192.168.1.7"}},
				},
			},
		}})
		ips, err := manager.VMIPAddresses("vm0")
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.5", "fe80::1", "192.168.1.7"}, ips)
	})

	t.Run("no tools", func(t *testing.T) {
		manager := newTestVMManager(&fakeVMClient{infoResponse: &mo.VirtualMachine{}})
		_, err := manager.VMIPAddresses("vm0")
		require.ErrorContains(t, err, "no guest info")
	})

	t.Run("no IPs", func(t *testing.T) {
		manager := newTestVMManager(&fakeVMClient{infoResponse: &mo.VirtualMachine{Guest: &types.GuestInfo{}}})
		_, err := manager.VMIPAddresses("vm0")
		require.ErrorContains(t, err, "no reported IPs")
	})
}

func TestVSphereConsoleURL(t *testing.T) {
	manager := newTestVMManager(&fakeVMClient{consoleURL: "wss://esx:443/ticket/abc"})
	url, err := manager.ConsoleURL("vm0")
	require.NoError(t, err)
	assert.Equal(t, "wss://esx:443/ticket/abc", url)
}

func TestVSphereMarkVMAsTemplate(t *testing.T) {
	client := &fakeVMClient{}
	manager := newTestVMManager(client)
	require.NoError(t, manager.MarkVMAsTemplate("golden"))
	assert.Equal(t, 1, client.templated)
}

func TestCreateCloudInitVM(t *testing.T) {
	t.Run("clone, guestinfo, power on", func(t *testing.T) {
		client := &fakeVMClient{infoResponse: vmInfoWithDisks(40 << 20)}
		manager := newTestVMManager(client)
		err := manager.CreateCloudInitVM(CloudInitVMConfig{
			TemplateName: "ubuntu-tpl",
			Name:         "dev0",
			MemoryMB:     4096,
			Cores:        2,
			DiskGB:       60,
			Userdata:     "#cloud-config\nhostname: dev0\n",
			PowerOn:      true,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"dev0"}, client.clonedTo)
		assert.Equal(t, 1, client.poweredOn)
		// first reconfigure: sizing + guestinfo; second: disk grow to 60G
		require.Len(t, client.reconfigSpecs, 2)
		spec := client.reconfigSpecs[0]
		assert.Equal(t, int64(4096), spec.MemoryMB)
		assert.Equal(t, int32(2), spec.NumCPUs)

		extra := map[string]string{}
		for _, opt := range spec.ExtraConfig {
			ov := opt.GetOptionValue()
			extra[ov.Key] = ov.Value.(string)
		}
		userdata, err := base64.StdEncoding.DecodeString(extra["guestinfo.userdata"])
		require.NoError(t, err)
		assert.Contains(t, string(userdata), "hostname: dev0")
		assert.Equal(t, "base64", extra["guestinfo.userdata.encoding"])
		metadata, err := base64.StdEncoding.DecodeString(extra["guestinfo.metadata"])
		require.NoError(t, err)
		assert.Contains(t, string(metadata), `"local-hostname":"dev0"`)
	})

	t.Run("missing template", func(t *testing.T) {
		client := &fakeVMClient{findErr: assert.AnError}
		manager := newTestVMManager(client)
		err := manager.CreateCloudInitVM(CloudInitVMConfig{TemplateName: "ghost", Name: "dev0"})
		require.ErrorContains(t, err, "failed to find template")
	})

	t.Run("same-size disk is fine", func(t *testing.T) {
		client := &fakeVMClient{infoResponse: vmInfoWithDisks(60 << 20)}
		manager := newTestVMManager(client)
		err := manager.CreateCloudInitVM(CloudInitVMConfig{
			TemplateName: "tpl", Name: "dev1", DiskGB: 60,
		})
		require.NoError(t, err)
		require.Len(t, client.reconfigSpecs, 1) // no disk-grow reconfigure
	})
}
