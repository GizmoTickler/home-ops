// Package provider defines the shared contracts that every hypervisor
// provider (Proxmox, TrueNAS, vSphere/ESXi) implements, so command-layer
// code can dispatch VM operations without per-provider switch statements.
package provider

import (
	"errors"
	"fmt"
)

// Feature names a VM capability a provider may or may not support. Features
// map 1:1 onto `homeops-cli vm` subcommands or flags.
type Feature string

const (
	FeatureCreate         Feature = "create"
	FeatureSet            Feature = "set"
	FeatureResizeDisk     Feature = "resize-disk"
	FeatureRestart        Feature = "restart"
	FeatureSnapshot       Feature = "snapshot"
	FeatureClone          Feature = "clone"
	FeatureIP             Feature = "ip"
	FeatureConsole        Feature = "console"
	FeatureTemplateImport Feature = "template import"
)

// Capabilities maps the features a provider CANNOT perform to the reason
// why. A feature absent from the map is supported. This keeps "unsupported"
// an explicit, reasoned state instead of a silent no-op or ad-hoc switch.
type Capabilities map[Feature]string

// UnsupportedError reports an operation a provider genuinely cannot perform.
// Its message is the uniform "not supported on <provider>: <reason>".
type UnsupportedError struct {
	Provider string
	Reason   string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("not supported on %s: %s", e.Provider, e.Reason)
}

// Unsupported builds the uniform unsupported-operation error.
func Unsupported(provider, reason string) error {
	return &UnsupportedError{Provider: provider, Reason: reason}
}

// IsUnsupported reports whether err is (or wraps) an UnsupportedError.
func IsUnsupported(err error) bool {
	var u *UnsupportedError
	return errors.As(err, &u)
}

// CloneOptions carries provider-specific clone knobs. Providers reject
// options they cannot honour with an UnsupportedError (never silently).
type CloneOptions struct {
	// VMID pins the clone's numeric ID (Proxmox only; 0 = auto).
	VMID int
	// Linked requests a linked clone instead of a full copy. TrueNAS clones
	// are always ZFS snapshot clones, so the flag is a no-op there by design;
	// vSphere rejects it (full clones only).
	Linked bool
}

// VMSummary is one VM in a provider's inventory, shaped for both table
// rendering and machine-readable output (vm list --output json).
type VMSummary struct {
	Name     string            `json:"name" yaml:"name"`
	ID       string            `json:"id,omitempty" yaml:"id,omitempty"`
	Status   string            `json:"status" yaml:"status"`
	MemoryMB int               `json:"memory_mb,omitempty" yaml:"memory_mb,omitempty"`
	CPUs     int               `json:"cpus,omitempty" yaml:"cpus,omitempty"`
	Details  map[string]string `json:"details,omitempty" yaml:"details,omitempty"`
}

// VMLifecycle is the name-addressed VM lifecycle contract. Construction and
// provider-specific options (credentials, storage cleanup behaviour) live
// with each implementation; everything past construction is uniform.
//
// Semantics each implementation must honour:
//   - StopVM's force flag requests an ungraceful power-off where the
//     platform distinguishes (providers without the distinction may ignore it).
//   - DeleteVM removes the VM and any provider-default associated storage.
//   - ListVMs, GetVMInfo, and ListVMSnapshots print human-readable output.
//   - Snapshot operations address snapshots by name; on TrueNAS a "VM
//     snapshot" spans every zvol backing the VM under one snapshot name.
//   - Operations a provider cannot perform return an UnsupportedError with
//     the uniform "not supported on <provider>: <reason>" message — never a
//     silent no-op. Capabilities() advertises those gaps up front.
type VMLifecycle interface {
	ListVMs() error
	VMSummaries() ([]VMSummary, error)
	StartVM(name string) error
	StopVM(name string, force bool) error
	RestartVM(name string) error
	DeleteVM(name string) error
	GetVMInfo(name string) error
	SetVMResources(name string, memoryMB, cores int) error
	ResizeVMDisk(name, disk, sizeSpec string) error
	SnapshotVM(name, snap string) error
	ListVMSnapshots(name string) error
	RollbackVM(name, snap string) error
	DeleteVMSnapshot(name, snap string) error
	Clone(name, newName string, opts CloneOptions) error
	VMIPAddresses(name string) ([]string, error)
	ConsoleURL(name string) (string, error)
	Capabilities() Capabilities
	Close() error
}
