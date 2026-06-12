package proxmox

import (
	"fmt"
	"time"

	"github.com/luthermonson/go-proxmox"
)

// SnapshotVM creates a snapshot of a VM.
func (vm *VMManager) SnapshotVM(name, snapName string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.NewSnapshot(vm.client.Context(), snapName)
	if err != nil {
		return fmt.Errorf("snapshot %s of VM %s: %w", snapName, name, err)
	}
	if err := task.Wait(vm.client.Context(), time.Second, 300*time.Second); err != nil {
		return fmt.Errorf("snapshot task for %s: %w", name, err)
	}
	vm.logger.Success("Snapshot %q created for VM %s", snapName, name)
	return nil
}

// ListVMSnapshots prints a VM's snapshots.
func (vm *VMManager) ListVMSnapshots(name string) error {
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
	fmt.Printf("%-30s %-22s %s\n", "NAME", "CREATED", "DESCRIPTION")
	for _, s := range snaps {
		if s.Name == "current" {
			continue
		}
		created := ""
		if s.Snaptime > 0 {
			created = time.Unix(s.Snaptime, 0).Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-30s %-22s %s\n", s.Name, created, s.Description)
	}
	return nil
}

// RollbackVM rolls a VM back to a snapshot. DESTRUCTIVE: current disk state
// after the snapshot is lost.
func (vm *VMManager) RollbackVM(name, snapName string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.SnapshotRollback(vm.client.Context(), snapName)
	if err != nil {
		return fmt.Errorf("rollback VM %s to %s: %w", name, snapName, err)
	}
	if err := task.Wait(vm.client.Context(), time.Second, 600*time.Second); err != nil {
		return fmt.Errorf("rollback task for %s: %w", name, err)
	}
	vm.logger.Success("VM %s rolled back to snapshot %q", name, snapName)
	return nil
}

// DeleteVMSnapshot removes a snapshot.
func (vm *VMManager) DeleteVMSnapshot(name, snapName string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.DeleteSnapshot(vm.client.Context(), snapName)
	if err != nil {
		return fmt.Errorf("delete snapshot %s of VM %s: %w", snapName, name, err)
	}
	if err := task.Wait(vm.client.Context(), time.Second, 300*time.Second); err != nil {
		return fmt.Errorf("delete-snapshot task for %s: %w", name, err)
	}
	vm.logger.Success("Snapshot %q of VM %s deleted", snapName, name)
	return nil
}

// CloneVM clones a VM to a new name (full clone). newVMID 0 = auto-assign.
func (vm *VMManager) CloneVM(name, newName string, newVMID int, full bool) error {
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
		opts.Full = 1
	}
	id, task, err := vmObj.Clone(vm.client.Context(), opts)
	if err != nil {
		return fmt.Errorf("clone VM %s -> %s: %w", name, newName, err)
	}
	if err := task.Wait(vm.client.Context(), time.Second, 1800*time.Second); err != nil {
		return fmt.Errorf("clone task for %s: %w", newName, err)
	}
	vm.logger.Success("VM %s cloned to %s (VMID %d)", name, newName, id)
	return nil
}
