package proxmox

import (
	"homeops-cli/internal/provider"
)

// Proxmox implements the full provider.VMLifecycle contract.
var _ provider.VMLifecycle = (*VMManager)(nil)

// Clone adapts CloneVM to the provider contract (full clone unless Linked).
func (vm *VMManager) Clone(name, newName string, opts provider.CloneOptions) error {
	return vm.CloneVM(name, newName, opts.VMID, !opts.Linked)
}

// Capabilities: Proxmox supports every vm feature.
func (vm *VMManager) Capabilities() provider.Capabilities {
	return provider.Capabilities{}
}
