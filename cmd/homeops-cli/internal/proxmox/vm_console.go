package proxmox

import (
	"fmt"
	"net/url"
	"time"
)

// ConsoleURLs returns the Proxmox web console URLs for a VM: the noVNC
// graphical console and the xterm.js serial console (the latter requires a
// serial device on the VM). Authentication happens in the browser session.
func (vm *VMManager) ConsoleURLs(name string) (novnc, xtermjs string, err error) {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return "", "", err
	}
	query := fmt.Sprintf("vmid=%d&vmname=%s&node=%s",
		vmObj.VMID, url.QueryEscape(name), url.QueryEscape(vm.nodeName))
	base := fmt.Sprintf("https://%s:8006/?console=kvm", vm.host)
	return base + "&novnc=1&" + query, base + "&xtermjs=1&" + query, nil
}

// ConsoleURL returns the primary (noVNC) web console URL for a VM.
func (vm *VMManager) ConsoleURL(name string) (string, error) {
	novnc, _, err := vm.ConsoleURLs(name)
	return novnc, err
}

// ImportTemplate deploys a cloud image as a VM (boot disk imported from the
// image, cloud-init drive attached) and converts it into a reusable Proxmox
// template for 'vm clone'.
func (vm *VMManager) ImportTemplate(config VMConfig) error {
	config.PowerOn = false // templates are never started
	if err := vm.DeployVM(config); err != nil {
		return err
	}
	if err := vm.convertToTemplate(config.Name); err != nil {
		return err
	}
	vm.logger.Success("Template %s ready — clone it with 'homeops-cli vm clone --name %s --to <new-vm>'", config.Name, config.Name)
	return nil
}

// convertToTemplate flips the Proxmox template flag on an existing VM.
func (vm *VMManager) convertToTemplate(name string) error {
	if vm.convertToTemplateFn != nil {
		return vm.convertToTemplateFn(name)
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}
	task, err := vmObj.ConvertToTemplate(vm.client.Context())
	if err != nil {
		return fmt.Errorf("convert VM %s to template: %w", name, err)
	}
	if task != nil {
		if err := task.Wait(vm.client.Context(), time.Second, 120*time.Second); err != nil {
			return fmt.Errorf("template conversion task for %s: %w", name, err)
		}
	}
	return nil
}
