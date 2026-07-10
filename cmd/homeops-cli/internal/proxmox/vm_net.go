package proxmox

import (
	"fmt"
	"strings"
)

// VMIPAddresses returns the VM's non-loopback IPv4 addresses via the QEMU
// guest agent (requires the agent running in the guest).
func (vm *VMManager) VMIPAddresses(name string) ([]string, error) {
	if err := requireVMName(name); err != nil {
		return nil, err
	}
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return nil, err
	}
	ifaces, err := vmObj.AgentGetNetworkIFaces(vm.client.Context())
	if err != nil {
		return nil, fmt.Errorf("query guest agent on VM %s (is qemu-guest-agent running?): %w", name, err)
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.IPAddressType != "ipv4" {
				continue
			}
			ip := addr.IPAddress
			if strings.HasPrefix(ip, "127.") || strings.HasPrefix(ip, "169.254.") {
				continue
			}
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("guest agent on VM %s reported no usable IPv4 addresses", name)
	}
	return ips, nil
}
