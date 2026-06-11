package proxmox

import (
	"fmt"
	"net/url"

	"github.com/luthermonson/go-proxmox"
)

// CloudInitConfig describes a general-purpose cloud-image VM (vm create):
// the boot disk is imported from a cloud image (qcow2) and first-boot
// configuration is delivered via a Proxmox cloud-init drive.
type CloudInitConfig struct {
	User       string // login user (ciuser)
	SSHKeys    string // authorized public keys, newline separated
	IPConfig   string // ipconfig0, e.g. "ip=dhcp" or "ip=10.0.0.5/24,gw=10.0.0.1"
	Nameserver string // optional DNS server(s)
	SearchDom  string // optional DNS search domain
}

// buildCloudInitVMOptions assembles options for a cloud-image VM: imported
// boot disk, cloud-init drive on ide2, serial console (cloud images default
// their console there), and qemu agent.
func (vm *VMManager) buildCloudInitVMOptions(config VMConfig, ci CloudInitConfig) []proxmox.VirtualMachineOption {
	options := []proxmox.VirtualMachineOption{
		{Name: "name", Value: config.Name},
		{Name: "memory", Value: config.Memory},
		{Name: "cores", Value: config.Cores},
		{Name: "sockets", Value: config.Sockets},
		{Name: "ostype", Value: "l26"},
		{Name: "cpu", Value: "host"},
		{Name: "scsihw", Value: "virtio-scsi-single"},
		{Name: "agent", Value: "enabled=1"},
		{Name: "serial0", Value: "socket"},
		{Name: "vga", Value: "serial0"},
	}

	// Boot disk: import the cloud image into the target storage.
	options = append(options, proxmox.VirtualMachineOption{Name: "scsi0", Value: vm.flatcarBootDiskOpts(config)})
	options = append(options, proxmox.VirtualMachineOption{Name: "boot", Value: "order=scsi0"})

	// Cloud-init drive
	options = append(options, proxmox.VirtualMachineOption{Name: "ide2", Value: fmt.Sprintf("%s:cloudinit", config.BootStorage)})
	if ci.User != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "ciuser", Value: ci.User})
	}
	if ci.SSHKeys != "" {
		// The API requires the keys URL-encoded.
		options = append(options, proxmox.VirtualMachineOption{Name: "sshkeys", Value: url.QueryEscape(ci.SSHKeys)})
	}
	ipcfg := ci.IPConfig
	if ipcfg == "" {
		ipcfg = "ip=dhcp"
	}
	options = append(options, proxmox.VirtualMachineOption{Name: "ipconfig0", Value: ipcfg})
	if ci.Nameserver != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "nameserver", Value: ci.Nameserver})
	}
	if ci.SearchDom != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "searchdomain", Value: ci.SearchDom})
	}

	// Network
	netConfig := fmt.Sprintf("virtio,bridge=%s", config.NetworkBridge)
	if config.MacAddress != "" {
		netConfig = fmt.Sprintf("virtio=%s,bridge=%s", config.MacAddress, config.NetworkBridge)
	}
	if config.NetworkMTU != 0 {
		netConfig += fmt.Sprintf(",mtu=%d", config.NetworkMTU)
	}
	if config.VLANID != 0 {
		netConfig += fmt.Sprintf(",tag=%d", config.VLANID)
	}
	options = append(options, proxmox.VirtualMachineOption{Name: "net0", Value: netConfig})

	if config.StartOnBoot {
		options = append(options, proxmox.VirtualMachineOption{Name: "onboot", Value: 1})
	}
	return options
}
