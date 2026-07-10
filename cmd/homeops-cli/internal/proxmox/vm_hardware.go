package proxmox

import (
	"fmt"
	"time"

	"github.com/luthermonson/go-proxmox"
)

// SetVMResources updates a VM's memory (MB) and/or cores (0 = leave as-is).
// Changes to a running VM apply on next reboot (Proxmox pending config).
func (vm *VMManager) SetVMResources(name string, memoryMB, cores int) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	if memoryMB < 0 {
		return fmt.Errorf("memory must not be negative (0 leaves it unchanged), got %d", memoryMB)
	}
	if cores < 0 {
		return fmt.Errorf("cores must not be negative (0 leaves it unchanged), got %d", cores)
	}
	if memoryMB == 0 && cores == 0 {
		return fmt.Errorf("nothing to change: pass --memory and/or --cores")
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	var options []proxmox.VirtualMachineOption
	if memoryMB > 0 {
		options = append(options, proxmox.VirtualMachineOption{Name: "memory", Value: memoryMB})
	}
	if cores > 0 {
		options = append(options, proxmox.VirtualMachineOption{Name: "cores", Value: cores})
	}
	task, err := vmObj.Config(vm.client.Context(), options...)
	if err != nil {
		return fmt.Errorf("update VM %s config: %w", name, err)
	}
	if err := vm.waitTask(task, 60*time.Second, "config update", name); err != nil {
		return err
	}
	if vmObj.Status == "running" {
		vm.logger.Warn("VM %s is running — new resources apply after a reboot ('homeops-cli vm restart --name %s')", name, name)
	}
	vm.logger.Success("VM %s resources updated (memory=%dMB cores=%d; 0 = unchanged)", name, memoryMB, cores)
	return nil
}

// ResizeVMDisk grows a disk (e.g. scsi0) by sizeSpec ("+20G") or to an
// absolute size ("100G"). Proxmox disks can only grow, never shrink.
// An empty or "boot" disk selector means the boot disk (scsi0).
func (vm *VMManager) ResizeVMDisk(name, disk, sizeSpec string) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	if sizeSpec == "" {
		return fmt.Errorf("disk size is required (e.g. +20G to grow, or 100G for an absolute size)")
	}
	if disk == "" || disk == "boot" {
		disk = "scsi0"
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.ResizeDisk(vm.client.Context(), disk, sizeSpec)
	if err != nil {
		return fmt.Errorf("resize %s on VM %s: %w", disk, name, err)
	}
	if err := vm.waitTask(task, 120*time.Second, "resize", name); err != nil {
		return err
	}
	vm.logger.Success("VM %s disk %s resized to %s (grow the filesystem inside the guest)", name, disk, sizeSpec)
	return nil
}

// RestartVM reboots a VM (guest-cooperative when the agent is present).
func (vm *VMManager) RestartVM(name string) error {
	if err := requireVMName(name); err != nil {
		return err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.Reboot(vm.client.Context())
	if err != nil {
		return fmt.Errorf("reboot VM %s: %w", name, err)
	}
	if err := vm.waitTask(task, 180*time.Second, "reboot", name); err != nil {
		return err
	}
	vm.logger.Success("VM %s rebooted", name)
	return nil
}
