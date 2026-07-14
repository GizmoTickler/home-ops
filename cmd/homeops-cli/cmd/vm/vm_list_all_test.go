package vm

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/vmlifecycle"
)

type fakeListLifecycle struct {
	provider string
	closed   *int
	err      error
}

func (f *fakeListLifecycle) ListVMs() error { return nil }
func (f *fakeListLifecycle) VMSummaries() ([]vmprov.VMSummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []vmprov.VMSummary{{Name: f.provider + "-vm", ID: f.provider + "-1", Status: "running", MemoryMB: 1024, CPUs: 1}}, nil
}
func (f *fakeListLifecycle) StartVM(string) error                            { return nil }
func (f *fakeListLifecycle) StopVM(string, bool) error                       { return nil }
func (f *fakeListLifecycle) RestartVM(string) error                          { return nil }
func (f *fakeListLifecycle) DeleteVM(string) error                           { return nil }
func (f *fakeListLifecycle) GetVMInfo(string) error                          { return nil }
func (f *fakeListLifecycle) SetVMResources(string, int, int) error           { return nil }
func (f *fakeListLifecycle) ResizeVMDisk(string, string, string) error       { return nil }
func (f *fakeListLifecycle) SnapshotVM(string, string) error                 { return nil }
func (f *fakeListLifecycle) ListVMSnapshots(string) error                    { return nil }
func (f *fakeListLifecycle) RollbackVM(string, string) error                 { return nil }
func (f *fakeListLifecycle) DeleteVMSnapshot(string, string) error           { return nil }
func (f *fakeListLifecycle) Clone(string, string, vmprov.CloneOptions) error { return nil }
func (f *fakeListLifecycle) VMIPAddresses(string) ([]string, error)          { return nil, nil }
func (f *fakeListLifecycle) ConsoleURL(string) (string, error)               { return "", nil }
func (f *fakeListLifecycle) Capabilities() vmprov.Capabilities               { return vmprov.Capabilities{} }
func (f *fakeListLifecycle) Close() error {
	if f.closed != nil {
		*f.closed++
	}
	return nil
}

func TestListAllProvidersContinuesAfterProviderError(t *testing.T) {
	oldFactory := vmlifecycle.NewVMLifecycleFn
	t.Cleanup(func() { vmlifecycle.NewVMLifecycleFn = oldFactory })
	closed := 0
	vmlifecycle.NewVMLifecycleFn = func(provider string) (vmprov.VMLifecycle, error) {
		if provider == "vsphere" {
			return nil, errors.New("not configured")
		}
		return &fakeListLifecycle{provider: provider, closed: &closed}, nil
	}

	inventory, err := collectAllProviderVMs()
	require.NoError(t, err)
	assert.Len(t, inventory.VMs, 2)
	assert.Len(t, inventory.ProviderErrors, 1)
	assert.Equal(t, "vsphere", inventory.ProviderErrors[0].Provider)
	assert.Contains(t, inventory.ProviderErrors[0].Error, "not configured")
	assert.Equal(t, 2, closed)
}

func TestRenderAllProviderInventoryIncludesProviderColumnAndNotes(t *testing.T) {
	inventory := allProviderVMInventory{
		VMs: []providerVMSummary{
			{Provider: "proxmox", VMSummary: vmprov.VMSummary{Name: "px", ID: "100", Status: "running"}},
			{Provider: "truenas", VMSummary: vmprov.VMSummary{Name: "tn", ID: "1", Status: "stopped"}},
		},
		ProviderErrors: []providerError{{Provider: "vsphere", Error: "not configured"}},
	}
	out, err := renderAllProviderVMInventory(inventory, "table")
	require.NoError(t, err)
	assert.Contains(t, out, "PROVIDER")
	assert.Contains(t, out, "proxmox")
	assert.Contains(t, out, "truenas")
	assert.Contains(t, out, "Provider notes")
	assert.Contains(t, out, "vsphere")
}

func TestRenderAllProviderInventoryJSON(t *testing.T) {
	inventory := allProviderVMInventory{
		VMs: []providerVMSummary{{Provider: "proxmox", VMSummary: vmprov.VMSummary{Name: "px", Status: "running"}}},
	}
	out, err := renderAllProviderVMInventory(inventory, "json")
	require.NoError(t, err)
	assert.Contains(t, out, `"provider": "proxmox"`)
	assert.Contains(t, out, `"name": "px"`)
}

func TestVMCommandExposesHiddenListAllProvidersLeafForInteractiveMenu(t *testing.T) {
	cmd := NewVMCommand()

	var listAll *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "list-all" {
			listAll = sub
			break
		}
	}

	require.NotNil(t, listAll)
	assert.True(t, listAll.Hidden)
	assert.False(t, listAll.HasSubCommands())
	assert.NotNil(t, listAll.RunE)
	assert.Equal(t, "table", listAll.Flags().Lookup("output").DefValue)
}

func TestListAllCommandUsesAllProviderInventory(t *testing.T) {
	oldFactory := vmlifecycle.NewVMLifecycleFn
	t.Cleanup(func() { vmlifecycle.NewVMLifecycleFn = oldFactory })
	vmlifecycle.NewVMLifecycleFn = func(provider string) (vmprov.VMLifecycle, error) {
		return &fakeListLifecycle{provider: provider}, nil
	}

	cmd := newListAllVMsCommand()
	cmd.SetArgs([]string{"--output", "json"})
	err := cmd.Execute()

	require.NoError(t, err)
}
