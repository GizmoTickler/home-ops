package vsphere

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

// This file holds the day-2 VM operations (resources, disks, snapshots,
// clone, restart, guest IPs, console, cloud-init create) on top of the
// govmomi client. Every govmomi call goes through a function var so tests
// can run without a vCenter/ESXi.

var (
	reconfigureVMFn = func(ctx context.Context, vm *object.VirtualMachine, spec types.VirtualMachineConfigSpec) (lifecycleTask, error) {
		task, err := vm.Reconfigure(ctx, spec)
		if err != nil {
			return nil, err
		}
		return objectTaskLifecycle{task: task}, nil
	}
	createSnapshotFn = func(ctx context.Context, vm *object.VirtualMachine, name string) (lifecycleTask, error) {
		task, err := vm.CreateSnapshot(ctx, name, "", false, false)
		if err != nil {
			return nil, err
		}
		return objectTaskLifecycle{task: task}, nil
	}
	revertSnapshotFn = func(ctx context.Context, vm *object.VirtualMachine, name string) (lifecycleTask, error) {
		task, err := vm.RevertToSnapshot(ctx, name, false)
		if err != nil {
			return nil, err
		}
		return objectTaskLifecycle{task: task}, nil
	}
	removeSnapshotFn = func(ctx context.Context, vm *object.VirtualMachine, name string) (lifecycleTask, error) {
		task, err := vm.RemoveSnapshot(ctx, name, false, nil)
		if err != nil {
			return nil, err
		}
		return objectTaskLifecycle{task: task}, nil
	}
	cloneVMFn = func(ctx context.Context, vm *object.VirtualMachine, folder *object.Folder, name string, spec types.VirtualMachineCloneSpec) (lifecycleTask, error) {
		task, err := vm.Clone(ctx, folder, name, spec)
		if err != nil {
			return nil, err
		}
		return objectTaskLifecycle{task: task}, nil
	}
	rebootGuestFn = func(ctx context.Context, vm *object.VirtualMachine) error { return vm.RebootGuest(ctx) }
	resetVMFn     = func(ctx context.Context, vm *object.VirtualMachine) (lifecycleTask, error) {
		task, err := vm.Reset(ctx)
		if err != nil {
			return nil, err
		}
		return objectTaskLifecycle{task: task}, nil
	}
	acquireTicketFn = func(ctx context.Context, vm *object.VirtualMachine) (*types.VirtualMachineTicket, error) {
		return vm.AcquireTicket(ctx, string(types.VirtualMachineTicketTypeWebmks))
	}
	markAsTemplateFn    = func(ctx context.Context, vm *object.VirtualMachine) error { return vm.MarkAsTemplate(ctx) }
	datacenterFoldersFn = func(ctx context.Context, dc *object.Datacenter) (*object.DatacenterFolders, error) {
		return dc.Folders(ctx)
	}
	defaultResourcePoolFn = func(ctx context.Context, finder *find.Finder) (*object.ResourcePool, error) {
		return finder.DefaultResourcePool(ctx)
	}
)

// ReconfigureVM applies a config spec (memory, CPUs, device edits,
// extraConfig) and waits for the task.
func (c *Client) ReconfigureVM(vm *object.VirtualMachine, spec types.VirtualMachineConfigSpec) error {
	task, err := reconfigureVMFn(c.ctx, vm, spec)
	if err != nil {
		return fmt.Errorf("reconfigure VM: %w", err)
	}
	if err := task.Wait(c.ctx); err != nil {
		return fmt.Errorf("reconfigure task: %w", err)
	}
	return nil
}

// CreateVMSnapshot snapshots the VM (disk-only, no memory state).
func (c *Client) CreateVMSnapshot(vm *object.VirtualMachine, name string) error {
	task, err := createSnapshotFn(c.ctx, vm, name)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	return task.Wait(c.ctx)
}

// RevertVMSnapshot reverts the VM to the named snapshot.
func (c *Client) RevertVMSnapshot(vm *object.VirtualMachine, name string) error {
	task, err := revertSnapshotFn(c.ctx, vm, name)
	if err != nil {
		return fmt.Errorf("revert to snapshot: %w", err)
	}
	return task.Wait(c.ctx)
}

// RemoveVMSnapshot deletes the named snapshot (children are kept).
func (c *Client) RemoveVMSnapshot(vm *object.VirtualMachine, name string) error {
	task, err := removeSnapshotFn(c.ctx, vm, name)
	if err != nil {
		return fmt.Errorf("remove snapshot: %w", err)
	}
	return task.Wait(c.ctx)
}

// CloneVMTo full-clones the VM into the datacenter's default VM folder and
// resource pool. The clone is left powered off.
func (c *Client) CloneVMTo(vm *object.VirtualMachine, newName string) error {
	folders, err := datacenterFoldersFn(c.ctx, c.datacenter)
	if err != nil {
		return fmt.Errorf("resolve VM folder: %w", err)
	}
	pool, err := defaultResourcePoolFn(c.ctx, c.finder)
	if err != nil {
		return fmt.Errorf("resolve resource pool: %w", err)
	}
	poolRef := pool.Reference()
	spec := types.VirtualMachineCloneSpec{
		Location: types.VirtualMachineRelocateSpec{Pool: &poolRef},
	}
	task, err := cloneVMFn(c.ctx, vm, folders.VmFolder, newName, spec)
	if err != nil {
		return fmt.Errorf("clone VM: %w", err)
	}
	return task.Wait(c.ctx)
}

// RebootVM asks the guest to reboot (VMware Tools); without tools it falls
// back to a hard reset.
func (c *Client) RebootVM(vm *object.VirtualMachine) error {
	if err := rebootGuestFn(c.ctx, vm); err == nil {
		return nil
	} else {
		c.logger.Warn("Guest reboot unavailable (no VMware Tools?): %v — falling back to hard reset", err)
	}
	task, err := resetVMFn(c.ctx, vm)
	if err != nil {
		return fmt.Errorf("reset VM: %w", err)
	}
	return task.Wait(c.ctx)
}

// AcquireConsoleURL returns a WebMKS console URL for the VM (short-lived
// one-time ticket).
func (c *Client) AcquireConsoleURL(vm *object.VirtualMachine) (string, error) {
	ticket, err := acquireTicketFn(c.ctx, vm)
	if err != nil {
		return "", fmt.Errorf("acquire console ticket: %w", err)
	}
	host := ticket.Host
	if host == "" && c.vim != nil {
		host = c.vim.URL().Hostname()
	}
	port := ticket.Port
	if port == 0 {
		port = 443
	}
	return fmt.Sprintf("wss://%s:%d/ticket/%s", host, port, ticket.Ticket), nil
}

// MarkVMAsTemplate converts a powered-off VM into a template.
func (c *Client) MarkVMAsTemplate(vm *object.VirtualMachine) error {
	if err := markAsTemplateFn(c.ctx, vm); err != nil {
		return fmt.Errorf("mark as template: %w", err)
	}
	return nil
}

// --- VMManager operations ---

// SetVMResources updates memory (MB) and/or CPU count (0 = leave as-is).
// vSphere rejects shrinking a powered-on VM; the API error is surfaced.
func (m *VMManager) SetVMResources(name string, memoryMB, cores int) error {
	if memoryMB == 0 && cores == 0 {
		return fmt.Errorf("nothing to change: pass --memory and/or --cores")
	}
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	spec := types.VirtualMachineConfigSpec{}
	if memoryMB > 0 {
		spec.MemoryMB = int64(memoryMB)
	}
	if cores > 0 {
		numCPUs, ok := common.SafeIntToInt32(cores)
		if !ok {
			return fmt.Errorf("CPU count %d is outside vSphere int32 range", cores)
		}
		spec.NumCPUs = numCPUs
	}
	if err := m.client.ReconfigureVM(vm, spec); err != nil {
		return fmt.Errorf("failed to update VM %s resources: %w", name, err)
	}
	m.logger.Success("VM %s resources updated (memory=%dMB cpus=%d; 0 = unchanged)", name, memoryMB, cores)
	return nil
}

// virtualDisks extracts the VM's virtual disks in device order.
func virtualDisks(info *mo.VirtualMachine) []*types.VirtualDisk {
	if info == nil || info.Config == nil {
		return nil
	}
	var disks []*types.VirtualDisk
	for _, dev := range info.Config.Hardware.Device {
		if disk, ok := dev.(*types.VirtualDisk); ok {
			disks = append(disks, disk)
		}
	}
	return disks
}

// selectVirtualDisk picks a disk by selector: "" or "boot" (first disk),
// "scsiN" (Nth disk), a numeric index, or the vSphere device label
// ("Hard disk 2").
func selectVirtualDisk(disks []*types.VirtualDisk, selector string) (*types.VirtualDisk, error) {
	if len(disks) == 0 {
		return nil, fmt.Errorf("VM has no virtual disks")
	}
	s := strings.TrimSpace(selector)
	if s == "" || strings.EqualFold(s, "boot") {
		return disks[0], nil
	}
	idx := -1
	if n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(s), "scsi")); err == nil {
		idx = n
	}
	if idx >= 0 {
		if idx >= len(disks) {
			return nil, fmt.Errorf("disk index %d out of range (VM has %d disks)", idx, len(disks))
		}
		return disks[idx], nil
	}
	for _, disk := range disks {
		if desc := disk.DeviceInfo.GetDescription(); desc != nil && strings.EqualFold(desc.Label, s) {
			return disk, nil
		}
	}
	return nil, fmt.Errorf("no disk matching %q on this VM", selector)
}

// ResizeVMDisk grows a VM disk. disk selects which one ("boot"/""/"scsiN"/
// label); sizeSpec is "+20G" (grow by) or "200G" (grow to). vSphere disks can
// only grow, never shrink.
func (m *VMManager) ResizeVMDisk(name, disk, sizeSpec string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	info, err := m.client.GetVMInfo(vm)
	if err != nil {
		return fmt.Errorf("failed to read VM %s hardware: %w", name, err)
	}
	target, err := selectVirtualDisk(virtualDisks(info), disk)
	if err != nil {
		return err
	}
	specBytes, relative, err := common.ParseSizeSpec(sizeSpec)
	if err != nil {
		return err
	}
	currentKB := target.CapacityInKB
	newKB := specBytes / 1024
	if relative {
		newKB = currentKB + specBytes/1024
	}
	if newKB <= currentKB {
		return fmt.Errorf("new size %dGiB is not larger than current %dGiB — vSphere disks can only grow", newKB>>20, currentKB>>20)
	}
	target.CapacityInKB = newKB
	configSpec := types.VirtualMachineConfigSpec{
		DeviceChange: []types.BaseVirtualDeviceConfigSpec{
			&types.VirtualDeviceConfigSpec{
				Operation: types.VirtualDeviceConfigSpecOperationEdit,
				Device:    target,
			},
		},
	}
	if err := m.client.ReconfigureVM(vm, configSpec); err != nil {
		return fmt.Errorf("failed to resize disk on VM %s: %w", name, err)
	}
	m.logger.Success("VM %s disk resized to %dGiB (grow the filesystem inside the guest)", name, newKB>>20)
	return nil
}

// RestartVM reboots the named VM (guest-cooperative when tools are present).
func (m *VMManager) RestartVM(name string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.RebootVM(vm); err != nil {
		return fmt.Errorf("failed to restart VM %s: %w", name, err)
	}
	m.logger.Success("VM %s restarted", name)
	return nil
}

// SnapshotVM creates a disk-only snapshot.
func (m *VMManager) SnapshotVM(name, snapName string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.CreateVMSnapshot(vm, snapName); err != nil {
		return fmt.Errorf("failed to snapshot VM %s: %w", name, err)
	}
	m.logger.Success("Snapshot %q created for VM %s", snapName, name)
	return nil
}

// ListVMSnapshots prints the VM's snapshot tree.
func (m *VMManager) ListVMSnapshots(name string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	info, err := m.client.GetVMInfo(vm)
	if err != nil {
		return fmt.Errorf("failed to read VM %s snapshots: %w", name, err)
	}
	if info == nil || info.Snapshot == nil || len(info.Snapshot.RootSnapshotList) == 0 {
		fmt.Printf("No snapshots for VM %s\n", name)
		return nil
	}
	var rows [][]string
	var walk func(snaps []types.VirtualMachineSnapshotTree, depth int)
	walk = func(snaps []types.VirtualMachineSnapshotTree, depth int) {
		for _, s := range snaps {
			indent := strings.Repeat("  ", depth)
			rows = append(rows, []string{indent + s.Name, s.CreateTime.Format("2006-01-02 15:04:05"), s.Description})
			walk(s.ChildSnapshotList, depth+1)
		}
	}
	walk(info.Snapshot.RootSnapshotList, 0)
	ui.PrintTable([]string{"NAME", "CREATED", "DESCRIPTION"}, rows)
	return nil
}

// RollbackVM reverts the VM to a snapshot. DESTRUCTIVE: disk state after the
// snapshot is lost.
func (m *VMManager) RollbackVM(name, snapName string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.RevertVMSnapshot(vm, snapName); err != nil {
		return fmt.Errorf("failed to roll back VM %s to %s: %w", name, snapName, err)
	}
	m.logger.Success("VM %s rolled back to snapshot %q", name, snapName)
	return nil
}

// DeleteVMSnapshot removes the named snapshot.
func (m *VMManager) DeleteVMSnapshot(name, snapName string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.RemoveVMSnapshot(vm, snapName); err != nil {
		return fmt.Errorf("failed to delete snapshot %s of VM %s: %w", snapName, name, err)
	}
	m.logger.Success("Snapshot %q of VM %s deleted", snapName, name)
	return nil
}

// CloneVM full-clones a VM to a new name (left powered off).
func (m *VMManager) CloneVM(name, newName string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.CloneVMTo(vm, newName); err != nil {
		return fmt.Errorf("failed to clone VM %s to %s: %w", name, newName, err)
	}
	m.logger.Success("VM %s cloned to %s", name, newName)
	return nil
}

// VMIPAddresses returns the guest's IPs as reported by VMware Tools.
func (m *VMManager) VMIPAddresses(name string) ([]string, error) {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return nil, fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	info, err := m.client.GetVMInfo(vm)
	if err != nil {
		return nil, fmt.Errorf("failed to read VM %s guest info: %w", name, err)
	}
	if info == nil || info.Guest == nil {
		return nil, fmt.Errorf("VM %s reports no guest info (VMware Tools not running?)", name)
	}
	var ips []string
	seen := map[string]bool{}
	add := func(ip string) {
		if ip != "" && !seen[ip] {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	add(info.Guest.IpAddress)
	for _, nic := range info.Guest.Net {
		for _, ip := range nic.IpAddress {
			add(ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("VM %s has no reported IPs (VMware Tools not running?)", name)
	}
	return ips, nil
}

// ConsoleURL returns a WebMKS console URL for the named VM.
func (m *VMManager) ConsoleURL(name string) (string, error) {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return "", fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	url, err := m.client.AcquireConsoleURL(vm)
	if err != nil {
		return "", fmt.Errorf("failed to get console for VM %s: %w", name, err)
	}
	return url, nil
}

// CloudInitVMConfig describes a VM cloned from a template with cloud-init
// delivered via guestinfo (the VMware datasource in cloud images).
type CloudInitVMConfig struct {
	TemplateName string
	Name         string
	MemoryMB     int
	Cores        int
	DiskGB       int    // grow the boot disk to this size (0 = template size)
	Userdata     string // cloud-config YAML ("" = none)
	Metadata     string // cloud-init metadata ("" = minimal instance-id/hostname)
	PowerOn      bool
}

// CreateCloudInitVM clones TemplateName to Name, sizes it, injects cloud-init
// data through guestinfo, and optionally powers it on.
func (m *VMManager) CreateCloudInitVM(cfg CloudInitVMConfig) error {
	template, err := m.client.FindVM(cfg.TemplateName)
	if err != nil {
		return fmt.Errorf("failed to find template %s: %w (import or build one first)", cfg.TemplateName, err)
	}
	if err := m.client.CloneVMTo(template, cfg.Name); err != nil {
		return fmt.Errorf("failed to clone template %s to %s: %w", cfg.TemplateName, cfg.Name, err)
	}
	vm, err := m.client.FindVM(cfg.Name)
	if err != nil {
		return fmt.Errorf("clone succeeded but VM %s not found: %w", cfg.Name, err)
	}

	metadata := cfg.Metadata
	if metadata == "" {
		raw, err := json.Marshal(map[string]string{"instance-id": cfg.Name, "local-hostname": cfg.Name})
		if err != nil {
			return fmt.Errorf("build cloud-init metadata: %w", err)
		}
		metadata = string(raw)
	}
	spec := types.VirtualMachineConfigSpec{
		ExtraConfig: []types.BaseOptionValue{
			&types.OptionValue{Key: "guestinfo.metadata", Value: base64.StdEncoding.EncodeToString([]byte(metadata))},
			&types.OptionValue{Key: "guestinfo.metadata.encoding", Value: "base64"},
		},
	}
	if cfg.Userdata != "" {
		spec.ExtraConfig = append(spec.ExtraConfig,
			&types.OptionValue{Key: "guestinfo.userdata", Value: base64.StdEncoding.EncodeToString([]byte(cfg.Userdata))},
			&types.OptionValue{Key: "guestinfo.userdata.encoding", Value: "base64"},
		)
	}
	if cfg.MemoryMB > 0 {
		spec.MemoryMB = int64(cfg.MemoryMB)
	}
	if cfg.Cores > 0 {
		numCPUs, ok := common.SafeIntToInt32(cfg.Cores)
		if !ok {
			return fmt.Errorf("CPU count %d is outside vSphere int32 range", cfg.Cores)
		}
		spec.NumCPUs = numCPUs
	}
	if err := m.client.ReconfigureVM(vm, spec); err != nil {
		return fmt.Errorf("failed to configure VM %s: %w", cfg.Name, err)
	}

	if cfg.DiskGB > 0 {
		// Growing past the template's disk is best-effort sizing sugar; a
		// same-size spec is fine (the template already matches).
		if err := m.ResizeVMDisk(cfg.Name, "boot", fmt.Sprintf("%dG", cfg.DiskGB)); err != nil &&
			!strings.Contains(err.Error(), "not larger than current") {
			return err
		}
	}

	if cfg.PowerOn {
		if err := m.client.PowerOnVM(vm); err != nil {
			return fmt.Errorf("failed to power on VM %s: %w", cfg.Name, err)
		}
	}
	m.logger.Success("VM %s created from template %s", cfg.Name, cfg.TemplateName)
	return nil
}

// MarkVMAsTemplate converts the named (powered-off) VM into a template.
func (m *VMManager) MarkVMAsTemplate(name string) error {
	vm, err := m.client.FindVM(name)
	if err != nil {
		return fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	if err := m.client.MarkVMAsTemplate(vm); err != nil {
		return fmt.Errorf("failed to convert VM %s to a template: %w", name, err)
	}
	m.logger.Success("VM %s is now a template", name)
	return nil
}
