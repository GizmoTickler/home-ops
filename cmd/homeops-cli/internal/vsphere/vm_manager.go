package vsphere

import (
	"fmt"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"homeops-cli/internal/common"
	"homeops-cli/internal/provider"
	"homeops-cli/internal/ui"
)

// vmClient is the slice of Client that VMManager composes over; narrow so
// tests can fake it.
type vmClient interface {
	ListVMs() ([]*object.VirtualMachine, error)
	FindVM(name string) (*object.VirtualMachine, error)
	GetVMInfo(vm *object.VirtualMachine) (*mo.VirtualMachine, error)
	PowerOnVM(vm *object.VirtualMachine) error
	PowerOffVM(vm *object.VirtualMachine) error
	DeleteVM(vm *object.VirtualMachine) error
	ReconfigureVM(vm *object.VirtualMachine, spec types.VirtualMachineConfigSpec) error
	CreateVMSnapshot(vm *object.VirtualMachine, name string) error
	RevertVMSnapshot(vm *object.VirtualMachine, name string) error
	RemoveVMSnapshot(vm *object.VirtualMachine, name string) error
	CloneVMTo(vm *object.VirtualMachine, newName string) error
	RebootVM(vm *object.VirtualMachine) error
	AcquireConsoleURL(vm *object.VirtualMachine) (string, error)
	MarkVMAsTemplate(vm *object.VirtualMachine) error
	Close() error
}

// VMManager is the name-addressed lifecycle wrapper around Client. It owns
// the find-by-name + act composition that command code previously repeated
// per operation, and implements provider.VMLifecycle like the Proxmox and
// TrueNAS managers do.
type VMManager struct {
	client vmClient
	logger *common.ColorLogger
}

var _ provider.VMLifecycle = (*VMManager)(nil)

// NewVMManager connects to vSphere/ESXi and returns a name-addressed manager.
func NewVMManager(host, username, password string, insecure bool) (*VMManager, error) {
	client, err := newClientWithConnectFn(host, username, password, insecure)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	return &VMManager{client: client, logger: common.NewColorLogger()}, nil
}

// Close logs out of the vSphere session.
func (m *VMManager) Close() error {
	return m.client.Close()
}

// ListVMs prints the inventory of VMs.
func (m *VMManager) ListVMs() error {
	summaries, err := m.VMSummaries()
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		rows = append(rows, []string{s.Name, s.Status, fmt.Sprintf("%d", s.MemoryMB), fmt.Sprintf("%d", s.CPUs)})
	}
	ui.PrintTable([]string{"NAME", "STATUS", "MEMORY(MB)", "CPUS"}, rows)
	fmt.Printf("Total: %d VMs\n", len(summaries))
	return nil
}

// VMSummaries returns the inventory in the provider-neutral shape. Hardware
// and power state need a per-VM property fetch; failures degrade to a bare
// name entry rather than failing the whole listing.
func (m *VMManager) VMSummaries() ([]provider.VMSummary, error) {
	vms, err := m.client.ListVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	summaries := make([]provider.VMSummary, 0, len(vms))
	for _, vm := range vms {
		summary := provider.VMSummary{Name: "", Status: "unknown"}
		if vm != nil {
			summary.Name = vm.Name()
		}
		if info, err := m.client.GetVMInfo(vm); err == nil && info != nil {
			summary.Status = string(info.Runtime.PowerState)
			if info.Config != nil {
				summary.MemoryMB = int(info.Config.Hardware.MemoryMB)
				summary.CPUs = int(info.Config.Hardware.NumCPU)
				summary.ID = info.Config.Uuid
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// StartVM powers on the named VM.
func (m *VMManager) StartVM(name string) error {
	m.logger.Info("Powering on vSphere/ESXi VM: %s", name)
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.PowerOnVM(vm); err != nil {
		return fmt.Errorf("failed to power on VM %s: %w", name, err)
	}
	m.logger.Success("VM %s powered on successfully!", name)
	return nil
}

// StopVM powers off the named VM. vSphere's PowerOffVM is already a hard
// power-off, so the force flag carries no extra meaning here.
func (m *VMManager) StopVM(name string, _ bool) error {
	m.logger.Info("Powering off vSphere/ESXi VM: %s", name)
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.PowerOffVM(vm); err != nil {
		return fmt.Errorf("failed to power off VM %s: %w", name, err)
	}
	m.logger.Success("VM %s powered off successfully!", name)
	return nil
}

// DeleteVM destroys the named VM.
func (m *VMManager) DeleteVM(name string) error {
	m.logger.Info("Starting vSphere/ESXi VM deletion for: %s", name)
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	m.logger.Info("Found VM: %s", name)
	if err := m.client.DeleteVM(vm); err != nil {
		return fmt.Errorf("failed to delete VM %s: %w", name, err)
	}
	m.logger.Success("VM %s deleted successfully!", name)
	return nil
}

// GetVMInfo prints detailed information for the named VM.
func (m *VMManager) GetVMInfo(name string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	vmInfo, err := m.client.GetVMInfo(vm)
	if err != nil {
		return fmt.Errorf("failed to get VM info for %s: %w", name, err)
	}

	m.logger.Info("VM Information for: %s", name)
	m.logger.Info("  Power State: %s", vmInfo.Runtime.PowerState)
	m.logger.Info("  Guest OS: %s", vmInfo.Config.GuestFullName)
	m.logger.Info("  CPUs: %d", vmInfo.Config.Hardware.NumCPU)
	m.logger.Info("  Memory: %d MB", vmInfo.Config.Hardware.MemoryMB)
	m.logger.Info("  UUID: %s", vmInfo.Config.Uuid)

	if vmInfo.Guest != nil && vmInfo.Guest.IpAddress != "" {
		m.logger.Info("  IP Address: %s", vmInfo.Guest.IpAddress)
	}
	return nil
}
