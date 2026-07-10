package proxmox

import (
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
	return vm.buildParameterizedVMOptions(config, vmOptionsProfile{
		staticCPU:  "host",
		staticSCSI: "virtio-scsi-single",
		earlyExtras: []proxmox.VirtualMachineOption{
			{Name: "agent", Value: "enabled=1"},
			{Name: "serial0", Value: "socket"},
			{Name: "vga", Value: "serial0"},
		},
		bootDisk:  (*VMManager).flatcarBootDiskOpts,
		bootOrder: cloudInitBootOrder,
		afterBoot: addCloudInitOptions(ci),
		network: vmNetworkOptionsProfile{
			usePositiveMTUAndVLAN: false,
		},
		includeOnBoot: true,
	})
}
