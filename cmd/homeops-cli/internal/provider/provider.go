// Package provider defines the shared contracts that every hypervisor
// provider (Proxmox, TrueNAS, vSphere/ESXi) implements, so command-layer
// code can dispatch VM operations without per-provider switch statements.
package provider

// VMLifecycle is the name-addressed VM lifecycle contract. Construction and
// provider-specific options (credentials, storage cleanup behaviour) live
// with each implementation; everything past construction is uniform.
//
// Semantics each implementation must honour:
//   - StopVM's force flag requests an ungraceful power-off where the
//     platform distinguishes (providers without the distinction may ignore it).
//   - DeleteVM removes the VM and any provider-default associated storage.
//   - ListVMs and GetVMInfo print human-readable output to stdout/logger.
type VMLifecycle interface {
	ListVMs() error
	StartVM(name string) error
	StopVM(name string, force bool) error
	DeleteVM(name string) error
	GetVMInfo(name string) error
	Close() error
}
