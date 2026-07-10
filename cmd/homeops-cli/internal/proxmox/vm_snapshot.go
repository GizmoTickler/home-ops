package proxmox

import (
	"fmt"
	"time"

	"github.com/luthermonson/go-proxmox"

	"homeops-cli/internal/ui"
)

// SnapshotVM creates a snapshot of a VM.
func (vm *VMManager) SnapshotVM(name, snapName string) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	if err := requireSnapshotName(snapName); err != nil {
		return err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.NewSnapshot(vm.client.Context(), snapName)
	if err != nil {
		return fmt.Errorf("snapshot %s of VM %s: %w", snapName, name, err)
	}
	if err := vm.waitTask(task, 300*time.Second, "snapshot", name); err != nil {
		return err
	}
	vm.logger.Success("Snapshot %q created for VM %s", snapName, name)
	return nil
}

// ListVMSnapshots prints a VM's snapshots.
func (vm *VMManager) ListVMSnapshots(name string) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	snaps, err := vmObj.Snapshots(vm.client.Context())
	if err != nil {
		return fmt.Errorf("list snapshots of VM %s: %w", name, err)
	}
	if len(snaps) == 0 {
		fmt.Printf("No snapshots for VM %s\n", name)
		return nil
	}
	var rows [][]string
	for _, s := range snaps {
		if s.Name == "current" {
			continue
		}
		created := ""
		if s.Snaptime > 0 {
			created = time.Unix(s.Snaptime, 0).Format("2006-01-02 15:04:05")
		}
		rows = append(rows, []string{s.Name, created, s.Description})
	}
	ui.PrintTable([]string{"NAME", "CREATED", "DESCRIPTION"}, rows)
	return nil
}

// RollbackVM rolls a VM back to a snapshot. DESTRUCTIVE: current disk state
// after the snapshot is lost.
func (vm *VMManager) RollbackVM(name, snapName string) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	if err := requireSnapshotName(snapName); err != nil {
		return err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.Snapshot(snapName).Rollback(vm.client.Context())
	if err != nil {
		return fmt.Errorf("rollback VM %s to %s: %w", name, snapName, err)
	}
	if err := vm.waitTask(task, 600*time.Second, "rollback", name); err != nil {
		return err
	}
	vm.logger.Success("VM %s rolled back to snapshot %q", name, snapName)
	return nil
}

// DeleteVMSnapshot removes a snapshot.
func (vm *VMManager) DeleteVMSnapshot(name, snapName string) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	if err := requireSnapshotName(snapName); err != nil {
		return err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.Snapshot(snapName).Delete(vm.client.Context())
	if err != nil {
		return fmt.Errorf("delete snapshot %s of VM %s: %w", snapName, name, err)
	}
	if err := vm.waitTask(task, 300*time.Second, "delete-snapshot", name); err != nil {
		return err
	}
	vm.logger.Success("Snapshot %q of VM %s deleted", snapName, name)
	return nil
}

// CloneVM clones a VM to a new name (full clone). newVMID 0 = auto-assign.
func (vm *VMManager) CloneVM(name, newName string, newVMID int, full bool) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	if err := requireTargetVMName(newName); err != nil {
		return err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	if newVMID == 0 {
		newVMID, err = vm.nextVMID()
		if err != nil {
			return fmt.Errorf("assign VMID for clone: %w", err)
		}
	}
	opts := &proxmox.VirtualMachineCloneOptions{NewID: newVMID, Name: newName}
	if full {
		opts.Full = true
	}
	id, task, err := vmObj.Clone(vm.client.Context(), opts)
	if err != nil {
		return fmt.Errorf("clone VM %s -> %s: %w", name, newName, err)
	}
	if err := vm.waitTask(task, 1800*time.Second, "clone", newName); err != nil {
		return err
	}
	vm.logger.Success("VM %s cloned to %s (VMID %d)", name, newName, id)
	return nil
}
