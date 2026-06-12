package vsphere

import (
	"homeops-cli/internal/provider"
)

// Clone adapts CloneVM to the provider contract. vSphere clones here are
// always full; options it cannot honour are rejected explicitly.
func (m *VMManager) Clone(name, newName string, opts provider.CloneOptions) error {
	if opts.VMID != 0 {
		return provider.Unsupported("vsphere", "vSphere has no numeric VMIDs; omit --vmid")
	}
	if opts.Linked {
		return provider.Unsupported("vsphere", "linked clones require a base snapshot and are not implemented; use a full clone")
	}
	return m.CloneVM(name, newName)
}

// Capabilities: the lifecycle surface is fully supported (guest IP and
// console need VMware Tools at runtime, which is a per-VM condition rather
// than a provider gap).
func (m *VMManager) Capabilities() provider.Capabilities {
	return provider.Capabilities{}
}
