package vsphere

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"homeops-cli/internal/common"
)

type fakeVMClient struct {
	foundNames   []string
	findErr      error
	poweredOn    int
	poweredOff   int
	deleted      int
	listCount    int
	closeCalls   int
	infoResponse *mo.VirtualMachine
}

func (f *fakeVMClient) ListVMs() ([]*object.VirtualMachine, error) {
	f.listCount++
	return nil, nil
}

func (f *fakeVMClient) FindVM(name string) (*object.VirtualMachine, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	f.foundNames = append(f.foundNames, name)
	return nil, nil
}

func (f *fakeVMClient) GetVMInfo(*object.VirtualMachine) (*mo.VirtualMachine, error) {
	return f.infoResponse, nil
}

func (f *fakeVMClient) PowerOnVM(*object.VirtualMachine) error {
	f.poweredOn++
	return nil
}

func (f *fakeVMClient) PowerOffVM(*object.VirtualMachine) error {
	f.poweredOff++
	return nil
}

func (f *fakeVMClient) DeleteVM(*object.VirtualMachine) error {
	f.deleted++
	return nil
}

func (f *fakeVMClient) Close() error {
	f.closeCalls++
	return nil
}

func newTestVMManager(client vmClient) *VMManager {
	return &VMManager{client: client, logger: common.NewColorLogger()}
}

func TestVMManagerLifecycleComposition(t *testing.T) {
	client := &fakeVMClient{
		infoResponse: &mo.VirtualMachine{
			Runtime: types.VirtualMachineRuntimeInfo{PowerState: types.VirtualMachinePowerStatePoweredOn},
			Config: &types.VirtualMachineConfigInfo{
				GuestFullName: "Talos Linux",
				Uuid:          "vm-uuid",
				Hardware:      types.VirtualHardware{NumCPU: 4, MemoryMB: 8192},
			},
			Guest: &types.GuestInfo{IpAddress: "10.0.0.99"},
		},
	}
	manager := newTestVMManager(client)

	require.NoError(t, manager.ListVMs())
	require.NoError(t, manager.StartVM("esx-vm"))
	require.NoError(t, manager.StopVM("esx-vm", true))
	require.NoError(t, manager.DeleteVM("esx-vm"))
	require.NoError(t, manager.GetVMInfo("esx-vm"))
	require.NoError(t, manager.Close())

	assert.Equal(t, 1, client.listCount)
	assert.Equal(t, []string{"esx-vm", "esx-vm", "esx-vm", "esx-vm"}, client.foundNames)
	assert.Equal(t, 1, client.poweredOn)
	assert.Equal(t, 1, client.poweredOff)
	assert.Equal(t, 1, client.deleted)
	assert.Equal(t, 1, client.closeCalls)
}

func TestVMManagerFindErrorsAreWrapped(t *testing.T) {
	client := &fakeVMClient{findErr: errors.New("no such vm")}
	manager := newTestVMManager(client)

	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"start", func() error { return manager.StartVM("missing") }},
		{"stop", func() error { return manager.StopVM("missing", false) }},
		{"delete", func() error { return manager.DeleteVM("missing") }},
		{"info", func() error { return manager.GetVMInfo("missing") }},
	} {
		err := op.run()
		require.Error(t, err, op.name)
		assert.Contains(t, err.Error(), fmt.Sprintf("failed to find VM %s", "missing"), op.name)
		assert.Contains(t, err.Error(), "no such vm", op.name)
	}
}

func TestNewVMManagerConnectError(t *testing.T) {
	original := newClientWithConnectFn
	t.Cleanup(func() { newClientWithConnectFn = original })

	newClientWithConnectFn = func(host, username, password string, insecure bool) (*Client, error) {
		return nil, errors.New("dial failed")
	}

	_, err := NewVMManager("esxi.local", "root", "secret", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to vSphere")
}
