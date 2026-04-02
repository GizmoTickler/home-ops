package proxmox

import (
	"context"
	"fmt"
	"testing"
	"time"

	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTaskHandle struct {
	waitErr error
	waits   int
}

func (f *fakeTaskHandle) Wait(context.Context, time.Duration, time.Duration) error {
	f.waits++
	return f.waitErr
}

type fakeVMHandle struct {
	name          string
	vmid          int
	status        string
	startTask     taskHandle
	startErr      error
	shutdownTask  taskHandle
	shutdownErr   error
	stopTask      taskHandle
	stopErr       error
	deleteTask    taskHandle
	deleteErr     error
	startCalls    int
	shutdownCalls int
	stopCalls     int
	deleteCalls   int
}

func (f *fakeVMHandle) Name() string   { return f.name }
func (f *fakeVMHandle) VMID() int      { return f.vmid }
func (f *fakeVMHandle) Status() string { return f.status }
func (f *fakeVMHandle) Start(context.Context) (taskHandle, error) {
	f.startCalls++
	return f.startTask, f.startErr
}
func (f *fakeVMHandle) Shutdown(context.Context) (taskHandle, error) {
	f.shutdownCalls++
	return f.shutdownTask, f.shutdownErr
}
func (f *fakeVMHandle) Stop(context.Context) (taskHandle, error) {
	f.stopCalls++
	return f.stopTask, f.stopErr
}
func (f *fakeVMHandle) Delete(context.Context) (taskHandle, error) {
	f.deleteCalls++
	return f.deleteTask, f.deleteErr
}

func TestVMManagerBuildVMOptions(t *testing.T) {
	manager := &VMManager{}
	options := manager.buildVMOptions(VMConfig{
		Name:           "k8s-0",
		Memory:         32768,
		Cores:          8,
		Sockets:        2,
		CPUType:        "host",
		CPUAffinity:    "0-7",
		NUMAEnabled:    true,
		NUMANode:       1,
		BootDiskSize:   200,
		BootStorage:    "nvme1",
		OpenEBSSize:    800,
		CephDiskByID:   "disk-by-id",
		ISOPath:        "local:iso/talos.iso",
		NetworkBridge:  "vmbr0",
		NetworkMTU:     9000,
		NetworkQueues:  8,
		VLANID:         999,
		MacAddress:     "00:11:22:33:44:55",
		SCSIController: "virtio-scsi-single",
		IOThread:       true,
		Discard:        true,
		BIOS:           "ovmf",
		WatchdogModel:  "i6300esb",
		WatchdogAction: "reset",
		AgentEnabled:   true,
		StartOnBoot:    true,
		EFIDiskStorage: "efi-store",
		OpenEBSStorage: "nvmeof-vmdata",
	})

	optionMap := make(map[string]interface{}, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt.Value
	}

	require.Equal(t, "k8s-0", optionMap["name"])
	assert.Equal(t, 32768, optionMap["memory"])
	assert.Equal(t, 8, optionMap["cores"])
	assert.Equal(t, 2, optionMap["sockets"])
	assert.Equal(t, "host", optionMap["cpu"])
	assert.Equal(t, "0-7", optionMap["affinity"])
	assert.Equal(t, 1, optionMap["numa"])
	assert.Contains(t, optionMap["numa0"], "hostnodes=1")
	assert.Equal(t, "ovmf", optionMap["bios"])
	assert.Equal(t, "efi-store:1,efitype=4m,pre-enrolled-keys=0", optionMap["efidisk0"])
	assert.Equal(t, "virtio-scsi-single", optionMap["scsihw"])
	assert.Equal(t, "nvme1:200,discard=on,iothread=1", optionMap["scsi0"])
	assert.Equal(t, "/dev/disk/by-id/disk-by-id,discard=on,iothread=1", optionMap["scsi2"])
	assert.Equal(t, "local:iso/talos.iso,media=cdrom", optionMap["ide2"])
	assert.Equal(t, "order=ide2", optionMap["boot"])
	assert.Contains(t, optionMap["net0"], "virtio=00:11:22:33:44:55,bridge=vmbr0")
	assert.Contains(t, optionMap["net0"], "mtu=9000")
	assert.Contains(t, optionMap["net0"], "queues=8")
	assert.Contains(t, optionMap["net0"], "tag=999")
	assert.Equal(t, "model=i6300esb,action=reset", optionMap["watchdog"])
	assert.Equal(t, "enabled=1", optionMap["agent"])
	assert.Equal(t, 1, optionMap["onboot"])
}

func TestVMManagerBuildVMOptionsMinimalNetworkFallback(t *testing.T) {
	manager := &VMManager{}
	options := manager.buildVMOptions(VMConfig{
		Name:          "demo",
		Memory:        4096,
		Cores:         2,
		Sockets:       1,
		BootDiskSize:  32,
		BootStorage:   "local",
		NetworkBridge: "vmbr1",
	})

	optionMap := make(map[string]interface{}, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt.Value
	}

	assert.Equal(t, "local:32", optionMap["scsi0"])
	assert.Equal(t, "virtio,bridge=vmbr1", optionMap["net0"])
	_, hasOpenEBS := optionMap["scsi1"]
	assert.False(t, hasOpenEBS)
	_, hasCeph := optionMap["scsi2"]
	assert.False(t, hasCeph)

	config, exists := GetTalosNodeConfig("k8s-0")
	require.True(t, exists)
	assert.Equal(t, 200, config.VMID)

	_, exists = GetTalosNodeConfig("unknown")
	assert.False(t, exists)
}

func TestNormalizeStorageConfig(t *testing.T) {
	t.Run("fills efi and openebs from boot storage", func(t *testing.T) {
		config, err := normalizeStorageConfig(VMConfig{
			Name:         "demo",
			BootDiskSize: 32,
			BootStorage:  "local-zfs",
			OpenEBSSize:  64,
			BIOS:         "ovmf",
		})
		require.NoError(t, err)
		assert.Equal(t, "local-zfs", config.EFIDiskStorage)
		assert.Equal(t, "local-zfs", config.OpenEBSStorage)
	})

	t.Run("falls back to openebs storage for boot when needed", func(t *testing.T) {
		config, err := normalizeStorageConfig(VMConfig{
			Name:           "demo",
			BootDiskSize:   32,
			OpenEBSSize:    64,
			OpenEBSStorage: "fast-data",
			BIOS:           "ovmf",
		})
		require.NoError(t, err)
		assert.Equal(t, "fast-data", config.BootStorage)
		assert.Equal(t, "fast-data", config.EFIDiskStorage)
	})

	t.Run("rejects missing boot storage", func(t *testing.T) {
		_, err := normalizeStorageConfig(VMConfig{
			Name:         "demo",
			BootDiskSize: 32,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boot storage is required")
	})
}

func TestVMManagerStopVMUsesCorrectMode(t *testing.T) {
	shutdownTask := &fakeTaskHandle{}
	stopTask := &fakeTaskHandle{}
	gracefulVM := &fakeVMHandle{name: "vm1", vmid: 101, shutdownTask: shutdownTask}
	forceVM := &fakeVMHandle{name: "vm1", vmid: 101, stopTask: stopTask}

	manager := &VMManager{
		client: &Client{},
		logger: common.NewColorLogger(),
	}

	manager.lookupVMHandle = func(name string) (vmHandle, error) {
		return gracefulVM, nil
	}
	require.NoError(t, manager.StopVM("vm1", false))
	assert.Equal(t, 1, gracefulVM.shutdownCalls)
	assert.Equal(t, 0, gracefulVM.stopCalls)
	assert.Equal(t, 1, shutdownTask.waits)

	manager.lookupVMHandle = func(name string) (vmHandle, error) {
		return forceVM, nil
	}
	require.NoError(t, manager.StopVM("vm1", true))
	assert.Equal(t, 1, forceVM.stopCalls)
	assert.Equal(t, 0, forceVM.shutdownCalls)
	assert.Equal(t, 1, stopTask.waits)
}

func TestVMManagerDeleteVMFailsIfStopFails(t *testing.T) {
	oldSleep := sleepForOperation
	sleepForOperation = func(time.Duration) {}
	t.Cleanup(func() {
		sleepForOperation = oldSleep
	})

	runningVM := &fakeVMHandle{
		name:    "vm1",
		vmid:    101,
		status:  "running",
		stopErr: fmt.Errorf("stop failed"),
	}

	manager := &VMManager{
		client: &Client{},
		logger: common.NewColorLogger(),
		lookupVMHandle: func(name string) (vmHandle, error) {
			return runningVM, nil
		},
	}

	err := manager.DeleteVM("vm1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to stop running VM before deletion")
	assert.Equal(t, 1, runningVM.stopCalls)
	assert.Equal(t, 0, runningVM.deleteCalls)
}

func TestVMManagerDeleteVMVerifiesRemoval(t *testing.T) {
	oldSleep := sleepForOperation
	sleepForOperation = func(time.Duration) {}
	t.Cleanup(func() {
		sleepForOperation = oldSleep
	})

	deleteTask := &fakeTaskHandle{}
	vmObj := &fakeVMHandle{
		name:       "vm1",
		vmid:       101,
		status:     "stopped",
		deleteTask: deleteTask,
	}

	lookupCalls := 0
	manager := &VMManager{
		client: &Client{},
		logger: common.NewColorLogger(),
		lookupVMHandle: func(name string) (vmHandle, error) {
			lookupCalls++
			switch lookupCalls {
			case 1:
				return vmObj, nil
			case 2:
				return vmObj, nil
			case 3:
				return nil, fmt.Errorf("%w: %s", ErrVMNotFound, name)
			default:
				return nil, fmt.Errorf("unexpected lookup")
			}
		},
	}

	require.NoError(t, manager.DeleteVM("vm1"))
	assert.Equal(t, 1, vmObj.deleteCalls)
	assert.Equal(t, 1, deleteTask.waits)
}

func TestVMManagerDeleteVMFailsIfStillPresentAfterRetry(t *testing.T) {
	oldSleep := sleepForOperation
	sleepForOperation = func(time.Duration) {}
	t.Cleanup(func() {
		sleepForOperation = oldSleep
	})

	deleteTask := &fakeTaskHandle{}
	vmObj := &fakeVMHandle{
		name:       "vm1",
		vmid:       101,
		status:     "stopped",
		deleteTask: deleteTask,
	}

	manager := &VMManager{
		client: &Client{},
		logger: common.NewColorLogger(),
		lookupVMHandle: func(name string) (vmHandle, error) {
			return vmObj, nil
		},
	}

	err := manager.DeleteVM("vm1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still exists after deletion request")
	assert.Equal(t, 1, vmObj.deleteCalls)
	assert.Equal(t, 1, deleteTask.waits)
}
