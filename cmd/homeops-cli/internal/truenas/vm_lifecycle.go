package truenas

import (
	"fmt"

	homeopscfg "homeops-cli/internal/config"
	"homeops-cli/internal/provider"
)

// trueNASIPReason explains the one lifecycle gap: the middleware has no
// guest-agent integration, so it cannot report VM IPs.
const trueNASIPReason = "TrueNAS middleware does not expose guest IPs (no guest agent); cluster nodes from homeops.yaml still resolve by name"

// Clone adapts CloneVM to the provider contract. TrueNAS clones are always
// ZFS snapshot clones (storage-linked), so Linked changes nothing by design.
func (vm *VMManager) Clone(name, newName string, opts provider.CloneOptions) error {
	if opts.VMID != 0 {
		return provider.Unsupported("truenas", "TrueNAS assigns VM IDs automatically; omit --vmid")
	}
	return vm.CloneVM(name, newName)
}

// VMIPAddresses cannot be answered by the middleware; callers fall back to
// configured cluster nodes (homeops.yaml).
func (vm *VMManager) VMIPAddresses(name string) ([]string, error) {
	return nil, provider.Unsupported("truenas", trueNASIPReason)
}

// ConsoleURL returns the VM's display endpoint: the web (HTML5) console when
// the display device has one, otherwise the native SPICE URL.
func (vm *VMManager) ConsoleURL(name string) (string, error) {
	info, err := vm.VMDisplayInfo(name)
	if err != nil {
		return "", err
	}
	host := homeopscfg.Get().Hypervisors.TrueNAS.SpiceHost
	if host == "" && info.Bind != "" && info.Bind != "0.0.0.0" && info.Bind != "::" {
		host = info.Bind
	}
	if host == "" {
		host = vm.client.host
	}
	if info.WebPort > 0 {
		return fmt.Sprintf("https://%s:%d", host, info.WebPort), nil
	}
	if info.Port > 0 {
		return fmt.Sprintf("spice://%s:%d", host, info.Port), nil
	}
	return "", fmt.Errorf("VM %s has a display device but no assigned ports (is the VM running?)", name)
}

// Capabilities: everything except guest-IP discovery is supported.
func (vm *VMManager) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		provider.FeatureIP: trueNASIPReason,
	}
}
