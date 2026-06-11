package proxmox

import (
	"fmt"
	"time"

	"github.com/luthermonson/go-proxmox"
)

// SetVMResources updates a VM's memory (MB) and/or cores (0 = leave as-is).
// Changes to a running VM apply on next reboot (Proxmox pending config).
func (vm *VMManager) SetVMResources(name string, memoryMB, cores int) error {
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
	if err := task.Wait(vm.client.Context(), time.Second, 60*time.Second); err != nil {
		return fmt.Errorf("config update task for %s: %w", name, err)
	}
	if vmObj.Status == "running" {
		vm.logger.Warn("VM %s is running — new resources apply after a reboot ('homeops-cli vm restart --name %s')", name, name)
	}
	vm.logger.Success("VM %s resources updated (memory=%dMB cores=%d; 0 = unchanged)", name, memoryMB, cores)
	return nil
}

// ResizeVMDisk grows a disk (e.g. scsi0) by sizeSpec ("+20G") or to an
// absolute size ("100G"). Proxmox disks can only grow, never shrink.
func (vm *VMManager) ResizeVMDisk(name, disk, sizeSpec string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.ResizeDisk(vm.client.Context(), disk, sizeSpec)
	if err != nil {
		return fmt.Errorf("resize %s on VM %s: %w", disk, name, err)
	}
	if task != nil {
		if err := task.Wait(vm.client.Context(), time.Second, 120*time.Second); err != nil {
			return fmt.Errorf("resize task for %s: %w", name, err)
		}
	}
	vm.logger.Success("VM %s disk %s resized to %s (grow the filesystem inside the guest)", name, disk, sizeSpec)
	return nil
}

// RestartVM reboots a VM (guest-cooperative when the agent is present).
func (vm *VMManager) RestartVM(name string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.Reboot(vm.client.Context())
	if err != nil {
		return fmt.Errorf("reboot VM %s: %w", name, err)
	}
	if err := task.Wait(vm.client.Context(), time.Second, 180*time.Second); err != nil {
		return fmt.Errorf("reboot task for %s: %w", name, err)
	}
	vm.logger.Success("VM %s rebooted", name)
	return nil
}
