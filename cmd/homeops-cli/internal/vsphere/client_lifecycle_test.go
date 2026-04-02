package vsphere

import (
	"context"
	"fmt"
	"testing"

	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

type fakeLifecycleTask struct {
	waitErr error
	waits   int
}

func (f *fakeLifecycleTask) Wait(context.Context) error {
	f.waits++
	return f.waitErr
}

type fakeVMLifecycle struct {
	powerState      types.VirtualMachinePowerState
	propertiesErr   error
	powerOnTask     lifecycleTask
	powerOnErr      error
	powerOffTask    lifecycleTask
	powerOffErr     error
	destroyTask     lifecycleTask
	destroyErr      error
	powerOnCalls    int
	powerOffCalls   int
	destroyCalls    int
	propertiesCalls int
}

func (f *fakeVMLifecycle) PowerOn(context.Context) (lifecycleTask, error) {
	f.powerOnCalls++
	return f.powerOnTask, f.powerOnErr
}

func (f *fakeVMLifecycle) PowerOff(context.Context) (lifecycleTask, error) {
	f.powerOffCalls++
	return f.powerOffTask, f.powerOffErr
}

func (f *fakeVMLifecycle) Destroy(context.Context) (lifecycleTask, error) {
	f.destroyCalls++
	return f.destroyTask, f.destroyErr
}

func (f *fakeVMLifecycle) Properties(_ context.Context, _ types.ManagedObjectReference, _ []string, dst interface{}) error {
	f.propertiesCalls++
	if f.propertiesErr != nil {
		return f.propertiesErr
	}
	if dst, ok := dst.(*mo.VirtualMachine); ok {
		dst.Runtime.PowerState = f.powerState
	}
	return nil
}

func (f *fakeVMLifecycle) Reference() types.ManagedObjectReference {
	return types.ManagedObjectReference{}
}

func TestClientLifecycleHelpers(t *testing.T) {
	client := &Client{
		ctx:    context.Background(),
		logger: common.NewColorLogger(),
	}

	t.Run("power on waits for task", func(t *testing.T) {
		task := &fakeLifecycleTask{}
		vm := &fakeVMLifecycle{powerOnTask: task}
		require.NoError(t, client.powerOnVM(vm))
		assert.Equal(t, 1, vm.powerOnCalls)
		assert.Equal(t, 1, task.waits)
	})

	t.Run("power off waits for task", func(t *testing.T) {
		task := &fakeLifecycleTask{}
		vm := &fakeVMLifecycle{powerOffTask: task}
		require.NoError(t, client.powerOffVM(vm))
		assert.Equal(t, 1, vm.powerOffCalls)
		assert.Equal(t, 1, task.waits)
	})

	t.Run("delete powered off VM", func(t *testing.T) {
		destroyTask := &fakeLifecycleTask{}
		vm := &fakeVMLifecycle{
			powerState:  types.VirtualMachinePowerStatePoweredOff,
			destroyTask: destroyTask,
		}
		require.NoError(t, client.deleteVM(vm))
		assert.Equal(t, 0, vm.powerOffCalls)
		assert.Equal(t, 1, vm.destroyCalls)
		assert.Equal(t, 1, destroyTask.waits)
	})

	t.Run("delete powers off running VM first", func(t *testing.T) {
		powerOffTask := &fakeLifecycleTask{}
		destroyTask := &fakeLifecycleTask{}
		vm := &fakeVMLifecycle{
			powerState:   types.VirtualMachinePowerStatePoweredOn,
			powerOffTask: powerOffTask,
			destroyTask:  destroyTask,
		}
		require.NoError(t, client.deleteVM(vm))
		assert.Equal(t, 1, vm.powerOffCalls)
		assert.Equal(t, 1, powerOffTask.waits)
		assert.Equal(t, 1, vm.destroyCalls)
		assert.Equal(t, 1, destroyTask.waits)
	})

	t.Run("delete fails if pre-delete power off fails", func(t *testing.T) {
		vm := &fakeVMLifecycle{
			powerState:  types.VirtualMachinePowerStatePoweredOn,
			powerOffErr: fmt.Errorf("power off refused"),
			destroyTask: &fakeLifecycleTask{},
		}
		err := client.deleteVM(vm)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to power off running VM before deletion")
		assert.Equal(t, 1, vm.powerOffCalls)
		assert.Equal(t, 0, vm.destroyCalls)
	})
}
