package truenas

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"homeops-cli/internal/common"
)

// This file holds the day-2 VM operations (resources, disks, snapshots,
// clone, restart, console) layered on the same JSON-RPC client as the
// deploy/lifecycle paths in vm_manager.go.
//
// TrueNAS has no VM-level snapshot primitive: a "VM snapshot" here is a
// point-in-time ZFS snapshot of every zvol backing the VM, all sharing one
// snapshot name. Rollback/delete operate on that same set.

// callResult invokes an RPC method and decodes the JSON-RPC "result" field
// into out (pass nil to discard).
func (c *WorkingClient) callResult(method string, params interface{}, timeoutSeconds int64, out interface{}) error {
	raw, err := c.Call(method, params, timeoutSeconds)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}
	if envelope.Result == nil {
		return fmt.Errorf("no result field in response")
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("failed to unmarshal %s result: %w", method, err)
	}
	return nil
}

// UpdateVM applies vm.update fields (e.g. memory, vcpus) to a VM by ID.
func (c *WorkingClient) UpdateVM(vmID int, updates map[string]interface{}) error {
	if err := c.callResult("vm.update", []interface{}{vmID, updates}, 60, nil); err != nil {
		return fmt.Errorf("failed to update VM %d: %w", vmID, err)
	}
	return nil
}

// RestartVM restarts a VM by ID (graceful stop + start in the middleware).
func (c *WorkingClient) RestartVM(vmID int) error {
	if err := c.callResult("vm.restart", []interface{}{vmID}, 180, nil); err != nil {
		return fmt.Errorf("failed to restart VM %d: %w", vmID, err)
	}
	return nil
}

// CloneVM clones a VM (and its zvols, as ZFS clones) to a new name.
func (c *WorkingClient) CloneVM(vmID int, newName string) error {
	if err := c.callResult("vm.clone", []interface{}{vmID, newName}, 600, nil); err != nil {
		return fmt.Errorf("failed to clone VM %d to %s: %w", vmID, newName, err)
	}
	return nil
}

// UpdateDataset applies pool.dataset.update fields (e.g. volsize) to a
// dataset/zvol by its ID (the full dataset path).
func (c *WorkingClient) UpdateDataset(id string, updates map[string]interface{}) error {
	if err := c.callResult("pool.dataset.update", []interface{}{id, updates}, 120, nil); err != nil {
		return fmt.Errorf("failed to update dataset %s: %w", id, err)
	}
	return nil
}

// zvolSizeEntry is the slice of pool.dataset.query output needed to read a
// zvol's current volsize.
type zvolSizeEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Volsize struct {
		Parsed json.Number `json:"parsed"`
	} `json:"volsize"`
}

// GetZvolSize returns a zvol's current volsize in bytes.
func (c *WorkingClient) GetZvolSize(id string) (int64, error) {
	params := []interface{}{[]interface{}{[]interface{}{"id", "=", id}}}
	var entries []zvolSizeEntry
	if err := c.callResult("pool.dataset.query", params, 30, &entries); err != nil {
		return 0, fmt.Errorf("failed to query zvol %s: %w", id, err)
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("zvol %s not found", id)
	}
	if entries[0].Type != "VOLUME" {
		return 0, fmt.Errorf("dataset %s is not a VOLUME (got %s)", id, entries[0].Type)
	}
	size, err := entries[0].Volsize.Parsed.Int64()
	if err != nil {
		return 0, fmt.Errorf("failed to parse volsize of %s: %w", id, err)
	}
	return size, nil
}

// ZFSSnapshot is a ZFS snapshot as returned by zfs.snapshot.query.
type ZFSSnapshot struct {
	ID           string                 `json:"id"` // dataset@name
	Dataset      string                 `json:"dataset"`
	SnapshotName string                 `json:"snapshot_name"`
	Properties   map[string]interface{} `json:"properties"`
}

// Created returns the snapshot's creation time as a display string ("" when
// the middleware did not include it).
func (s ZFSSnapshot) Created() string {
	prop, ok := s.Properties["creation"].(map[string]interface{})
	if !ok {
		return ""
	}
	if v, ok := prop["value"].(string); ok {
		return v
	}
	if v, ok := prop["rawvalue"].(string); ok {
		return v
	}
	return ""
}

// CreateZFSSnapshot snapshots one dataset/zvol under the given snapshot name.
func (c *WorkingClient) CreateZFSSnapshot(dataset, name string) error {
	params := []interface{}{map[string]interface{}{"dataset": dataset, "name": name}}
	if err := c.callResult("zfs.snapshot.create", params, 60, nil); err != nil {
		return fmt.Errorf("failed to snapshot %s@%s: %w", dataset, name, err)
	}
	return nil
}

// QueryZFSSnapshots lists snapshots of one dataset/zvol.
func (c *WorkingClient) QueryZFSSnapshots(dataset string) ([]ZFSSnapshot, error) {
	params := []interface{}{[]interface{}{[]interface{}{"dataset", "=", dataset}}}
	var snaps []ZFSSnapshot
	if err := c.callResult("zfs.snapshot.query", params, 30, &snaps); err != nil {
		return nil, fmt.Errorf("failed to query snapshots of %s: %w", dataset, err)
	}
	return snaps, nil
}

// DeleteZFSSnapshot removes one snapshot by ID ("dataset@name").
func (c *WorkingClient) DeleteZFSSnapshot(id string) error {
	if err := c.callResult("zfs.snapshot.delete", []interface{}{id}, 60, nil); err != nil {
		return fmt.Errorf("failed to delete snapshot %s: %w", id, err)
	}
	return nil
}

// RollbackZFSSnapshot rolls a dataset back to a snapshot ("dataset@name").
// force unmounts/destroys anything newer that blocks the rollback.
func (c *WorkingClient) RollbackZFSSnapshot(id string, force bool) error {
	params := []interface{}{id, map[string]interface{}{"force": force}}
	if err := c.callResult("zfs.snapshot.rollback", params, 300, nil); err != nil {
		return fmt.Errorf("failed to roll back to snapshot %s: %w", id, err)
	}
	return nil
}

// --- VMManager operations ---

// vmIsRunning reports whether the middleware considers the VM running.
func vmIsRunning(vmItem *VM) bool {
	if vmItem.Status == nil {
		return false
	}
	state, _ := vmItem.Status["state"].(string)
	return strings.EqualFold(state, "RUNNING")
}

// vmZVolDatasets resolves the dataset paths of every zvol backing the VM.
func (vm *VMManager) vmZVolDatasets(vmItem *VM) ([]string, error) {
	zvols, err := vm.discoverVMZVols(vmItem)
	if err != nil {
		return nil, err
	}
	if len(zvols) == 0 {
		return nil, fmt.Errorf("VM %s has no zvol-backed disks", vmItem.Name)
	}
	return zvols, nil
}

// SetVMResources updates a VM's memory (MB) and/or vCPU count (0 = leave
// as-is). Changes to a running VM apply on its next restart.
func (vm *VMManager) SetVMResources(name string, memoryMB, vcpus int) error {
	if memoryMB == 0 && vcpus == 0 {
		return fmt.Errorf("nothing to change: pass --memory and/or --cores")
	}
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{}
	if memoryMB > 0 {
		updates["memory"] = memoryMB
	}
	if vcpus > 0 {
		updates["vcpus"] = vcpus
	}
	if err := vm.client.UpdateVM(vmItem.ID, updates); err != nil {
		return err
	}
	if vmIsRunning(vmItem) {
		vm.logger.Warn("VM %s is running — new resources apply after a restart ('homeops-cli vm restart --name %s')", name, name)
	}
	vm.logger.Success("VM %s resources updated (memory=%dMB vcpus=%d; 0 = unchanged)", name, memoryMB, vcpus)
	return nil
}

// resolveVMDisk picks the zvol a disk selector refers to. Selector forms:
// "" or "boot" (the boot zvol), a suffix like "openebs", or a full dataset
// path.
func resolveVMDisk(zvols []string, disk string) (string, error) {
	selector := strings.TrimSpace(disk)
	if selector == "" {
		selector = "boot"
	}
	var matches []string
	for _, z := range zvols {
		if z == selector || strings.HasSuffix(z, "-"+selector) {
			matches = append(matches, z)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		// "boot" with a single-disk VM: that disk IS the boot disk.
		if selector == "boot" && len(zvols) == 1 {
			return zvols[0], nil
		}
		return "", fmt.Errorf("no disk matching %q on this VM (disks: %s)", selector, strings.Join(zvols, ", "))
	default:
		return "", fmt.Errorf("disk selector %q is ambiguous (matches: %s)", selector, strings.Join(matches, ", "))
	}
}

// ResizeVMDisk grows a VM disk (zvol). disk selects which zvol ("boot" by
// default, "openebs", or a full dataset path); sizeSpec is "+20G" (grow by)
// or "200G" (grow to). ZFS volumes can only grow, never shrink.
func (vm *VMManager) ResizeVMDisk(name, disk, sizeSpec string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	zvols, err := vm.vmZVolDatasets(vmItem)
	if err != nil {
		return err
	}
	target, err := resolveVMDisk(zvols, disk)
	if err != nil {
		return err
	}
	specBytes, relative, err := common.ParseSizeSpec(sizeSpec)
	if err != nil {
		return err
	}
	current, err := vm.client.GetZvolSize(target)
	if err != nil {
		return err
	}
	newSize := specBytes
	if relative {
		newSize = current + specBytes
	}
	if newSize <= current {
		return fmt.Errorf("new size %dGiB is not larger than current %dGiB — zvols can only grow", newSize>>30, current>>30)
	}
	if err := vm.client.UpdateDataset(target, map[string]interface{}{"volsize": newSize}); err != nil {
		return err
	}
	vm.logger.Success("VM %s disk %s resized to %dGiB (grow the filesystem inside the guest)", name, target, newSize>>30)
	return nil
}

// RestartVM restarts a VM through the middleware (graceful stop + start).
func (vm *VMManager) RestartVM(name string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	vm.logger.Info("Restarting VM: %s (ID: %d)", vmItem.Name, vmItem.ID)
	if err := vm.client.RestartVM(vmItem.ID); err != nil {
		return err
	}
	vm.logger.Success("VM %s restarted", name)
	return nil
}

// SnapshotVM creates a consistent-by-name ZFS snapshot of every zvol backing
// the VM.
func (vm *VMManager) SnapshotVM(name, snapName string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	zvols, err := vm.vmZVolDatasets(vmItem)
	if err != nil {
		return err
	}
	if vmIsRunning(vmItem) {
		vm.logger.Warn("VM %s is running — the snapshot is crash-consistent (like a power loss)", name)
	}
	for _, ds := range zvols {
		if err := vm.client.CreateZFSSnapshot(ds, snapName); err != nil {
			return err
		}
		vm.logger.Debug("Snapshotted %s@%s", ds, snapName)
	}
	vm.logger.Success("Snapshot %q created for VM %s (%d zvols)", snapName, name, len(zvols))
	return nil
}

// ListVMSnapshots prints the ZFS snapshots of the VM's zvols.
func (vm *VMManager) ListVMSnapshots(name string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	zvols, err := vm.vmZVolDatasets(vmItem)
	if err != nil {
		return err
	}
	var all []ZFSSnapshot
	for _, ds := range zvols {
		snaps, err := vm.client.QueryZFSSnapshots(ds)
		if err != nil {
			return err
		}
		all = append(all, snaps...)
	}
	if len(all) == 0 {
		fmt.Printf("No snapshots for VM %s\n", name)
		return nil
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].SnapshotName != all[j].SnapshotName {
			return all[i].SnapshotName < all[j].SnapshotName
		}
		return all[i].Dataset < all[j].Dataset
	})
	fmt.Printf("%-30s %-40s %s\n", "NAME", "DATASET", "CREATED")
	for _, s := range all {
		fmt.Printf("%-30s %-40s %s\n", s.SnapshotName, s.Dataset, s.Created())
	}
	return nil
}

// RollbackVM rolls every zvol backing the VM back to the named snapshot.
// DESTRUCTIVE: disk state after the snapshot is lost. The VM must be stopped.
func (vm *VMManager) RollbackVM(name, snapName string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	if vmIsRunning(vmItem) {
		return fmt.Errorf("VM %s is running — stop it before rolling back ('homeops-cli vm stop --provider truenas --name %s')", name, name)
	}
	zvols, err := vm.vmZVolDatasets(vmItem)
	if err != nil {
		return err
	}
	for _, ds := range zvols {
		if err := vm.client.RollbackZFSSnapshot(ds+"@"+snapName, true); err != nil {
			return err
		}
		vm.logger.Debug("Rolled back %s@%s", ds, snapName)
	}
	vm.logger.Success("VM %s rolled back to snapshot %q (%d zvols)", name, snapName, len(zvols))
	return nil
}

// DeleteVMSnapshot removes the named snapshot from every zvol backing the VM.
func (vm *VMManager) DeleteVMSnapshot(name, snapName string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	zvols, err := vm.vmZVolDatasets(vmItem)
	if err != nil {
		return err
	}
	deleted := 0
	var failures []string
	for _, ds := range zvols {
		snaps, err := vm.client.QueryZFSSnapshots(ds)
		if err != nil {
			return err
		}
		found := false
		for _, s := range snaps {
			if s.SnapshotName == snapName {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if err := vm.client.DeleteZFSSnapshot(ds + "@" + snapName); err != nil {
			failures = append(failures, fmt.Sprintf("%s@%s: %v", ds, snapName, err))
			continue
		}
		deleted++
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to delete snapshot on %d zvols: %s", len(failures), strings.Join(failures, "; "))
	}
	if deleted == 0 {
		return fmt.Errorf("snapshot %q not found on any zvol of VM %s", snapName, name)
	}
	vm.logger.Success("Snapshot %q of VM %s deleted (%d zvols)", snapName, name, deleted)
	return nil
}

// CloneVM clones a VM to a new name via the middleware (zvols become ZFS
// clones of an automatic snapshot).
func (vm *VMManager) CloneVM(name, newName string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}
	if _, err := vm.getVMByName(newName); err == nil {
		return fmt.Errorf("VM with name '%s' already exists", newName)
	}
	vm.logger.Info("Cloning VM %s (ID: %d) to %s", name, vmItem.ID, newName)
	if err := vm.client.CloneVM(vmItem.ID, newName); err != nil {
		return err
	}
	vm.logger.Success("VM %s cloned to %s", name, newName)
	return nil
}

// DisplayInfo describes a VM's display (SPICE) device endpoints.
type DisplayInfo struct {
	Type    string // e.g. SPICE
	Bind    string // bind address configured on the device
	Port    int    // native protocol port
	WebPort int    // web (HTML5) console port, 0 when web access is disabled
}

// VMDisplayInfo returns the display device endpoints of a VM, for building
// console URLs. Errors when the VM has no display device.
func (vm *VMManager) VMDisplayInfo(name string) (*DisplayInfo, error) {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return nil, err
	}
	devices, err := vm.client.QueryVMDevices(vmItem.ID)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		attributes, ok := device["attributes"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected device shape for VM %s", name)
		}
		if dtype, _ := attributes["dtype"].(string); dtype != "DISPLAY" {
			continue
		}
		info := &DisplayInfo{}
		if t, ok := attributes["type"].(string); ok {
			info.Type = t
		}
		if b, ok := attributes["bind"].(string); ok {
			info.Bind = b
		}
		info.Port = intAttr(attributes, "port")
		if web, _ := attributes["web"].(bool); web {
			info.WebPort = intAttr(attributes, "web_port")
		}
		return info, nil
	}
	return nil, fmt.Errorf("VM %s has no display device", name)
}

// intAttr reads a numeric device attribute that JSON may deliver as float64.
func intAttr(attributes map[string]interface{}, key string) int {
	switch v := attributes[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}
