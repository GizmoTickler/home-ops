package proxmox

import (
	"fmt"
	"net/url"
	"sort"

	homeopscfg "homeops-cli/internal/config"

	"github.com/luthermonson/go-proxmox"
)

type vmOptionsProfile struct {
	staticCPU        string
	useConfigCPU     bool
	useAffinity      bool
	useNUMA          bool
	useUEFI          bool
	staticSCSI       string
	useConfigSCSI    bool
	earlyExtras      []proxmox.VirtualMachineOption
	bootDisk         func(*VMManager, VMConfig) string
	includeOpenEBS   bool
	includeLegacyOSD bool
	includeISO       bool
	bootOrder        func(VMConfig) string
	afterBoot        func([]proxmox.VirtualMachineOption, VMConfig) []proxmox.VirtualMachineOption
	network          vmNetworkOptionsProfile
	includeWatchdog  bool
	useConfigAgent   bool
	includeOnBoot    bool
	afterOnBoot      func([]proxmox.VirtualMachineOption, VMConfig) []proxmox.VirtualMachineOption
}

type vmNetworkOptionsProfile struct {
	includeQueues         bool
	usePositiveMTUAndVLAN bool
}

func (vm *VMManager) buildParameterizedVMOptions(config VMConfig, profile vmOptionsProfile) []proxmox.VirtualMachineOption {
	options := []proxmox.VirtualMachineOption{
		{Name: "name", Value: config.Name},
		{Name: "memory", Value: config.Memory},
		{Name: "cores", Value: config.Cores},
		{Name: "sockets", Value: config.Sockets},
		{Name: "ostype", Value: "l26"},
	}

	if profile.staticCPU != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "cpu", Value: profile.staticCPU})
	}
	if profile.useConfigCPU && config.CPUType != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "cpu", Value: config.CPUType})
	}
	if profile.useAffinity && config.CPUAffinity != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "affinity", Value: config.CPUAffinity})
	}
	if profile.useNUMA && config.NUMAEnabled {
		options = append(options, proxmox.VirtualMachineOption{Name: "numa", Value: 1})
		numaConfig := fmt.Sprintf("cpus=0-%d,hostnodes=%d,memory=%d,policy=bind",
			config.Cores-1, config.NUMANode, config.Memory)
		options = append(options, proxmox.VirtualMachineOption{Name: "numa0", Value: numaConfig})
	}
	if profile.useUEFI && config.BIOS == "ovmf" {
		options = append(options, proxmox.VirtualMachineOption{Name: "bios", Value: "ovmf"})
		efiStorage := config.EFIDiskStorage
		if efiStorage == "" {
			efiStorage = config.BootStorage
		}
		efiDisk := fmt.Sprintf("%s:1,efitype=4m,pre-enrolled-keys=0", efiStorage)
		options = append(options, proxmox.VirtualMachineOption{Name: "efidisk0", Value: efiDisk})
	}
	if profile.staticSCSI != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "scsihw", Value: profile.staticSCSI})
	}
	if profile.useConfigSCSI && config.SCSIController != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "scsihw", Value: config.SCSIController})
	}
	options = append(options, profile.earlyExtras...)

	if profile.bootDisk != nil {
		options = append(options, proxmox.VirtualMachineOption{Name: "scsi0", Value: profile.bootDisk(vm, config)})
	}
	if profile.includeOpenEBS {
		options = appendOpenEBSDiskOptions(options, config)
		options = appendScratchDiskOptions(options, config)
	}
	if profile.includeLegacyOSD {
		options = appendLegacyOSDDiskOptions(options, config)
	}
	if profile.includeISO && config.ISOPath != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "ide2", Value: config.ISOPath + ",media=cdrom"})
	}
	if profile.bootOrder != nil {
		options = append(options, proxmox.VirtualMachineOption{Name: "boot", Value: profile.bootOrder(config)})
	}
	if profile.afterBoot != nil {
		options = profile.afterBoot(options, config)
	}

	options = append(options, proxmox.VirtualMachineOption{Name: "net0", Value: buildNetworkConfig(config, profile.network)})
	for _, nic := range secondaryNICs(config) {
		options = append(options, proxmox.VirtualMachineOption{Name: nic.name, Value: nic.value})
	}
	for _, nic := range storageNICs(config) {
		options = append(options, proxmox.VirtualMachineOption{Name: nic.name, Value: nic.value})
	}

	if profile.includeWatchdog && config.WatchdogModel != "" {
		watchdogOpts := fmt.Sprintf("model=%s", config.WatchdogModel)
		if config.WatchdogAction != "" {
			watchdogOpts += fmt.Sprintf(",action=%s", config.WatchdogAction)
		}
		options = append(options, proxmox.VirtualMachineOption{Name: "watchdog", Value: watchdogOpts})
	}
	if profile.useConfigAgent && config.AgentEnabled {
		options = append(options, proxmox.VirtualMachineOption{Name: "agent", Value: "enabled=1"})
	}
	if profile.includeOnBoot && config.StartOnBoot {
		options = append(options, proxmox.VirtualMachineOption{Name: "onboot", Value: 1})
	}
	if profile.afterOnBoot != nil {
		options = profile.afterOnBoot(options, config)
	}

	return options
}

func talosBootDiskOpts(_ *VMManager, config VMConfig) string {
	return appendDiskPerformanceOptions(fmt.Sprintf("%s:%d", config.BootStorage, config.BootDiskSize), config)
}

func appendOpenEBSDiskOptions(options []proxmox.VirtualMachineOption, config VMConfig) []proxmox.VirtualMachineOption {
	if config.OpenEBSSize <= 0 || config.OpenEBSStorage == "" {
		return options
	}
	openebsSlot := config.OpenEBSSlot
	if openebsSlot == "" {
		openebsSlot = "scsi1"
	}
	openebsDiskOpts := appendDiskPerformanceOptions(fmt.Sprintf("%s:%d", config.OpenEBSStorage, config.OpenEBSSize), config)
	if config.OpenEBSSSD {
		openebsDiskOpts += ",ssd=1"
	}
	return append(options, proxmox.VirtualMachineOption{Name: openebsSlot, Value: openebsDiskOpts})
}

// appendScratchDiskOptions attaches the node-local NVMe download-scratch disk.
// Opt-in: skipped entirely unless both a size and a pool are configured.
func appendScratchDiskOptions(options []proxmox.VirtualMachineOption, config VMConfig) []proxmox.VirtualMachineOption {
	if config.ScratchSize <= 0 || config.ScratchStorage == "" {
		return options
	}
	scratchSlot := config.ScratchSlot
	if scratchSlot == "" {
		scratchSlot = "scsi4"
	}
	scratchDiskOpts := appendDiskPerformanceOptions(fmt.Sprintf("%s:%d", config.ScratchStorage, config.ScratchSize), config)
	if config.ScratchSSD {
		scratchDiskOpts += ",ssd=1"
	}
	return append(options, proxmox.VirtualMachineOption{Name: scratchSlot, Value: scratchDiskOpts})
}

// appendLegacyOSDDiskOptions implements the retained nodes[].vm.ceph disk
// compatibility path. mode=none deliberately leaves scsi2 unattached.
func appendLegacyOSDDiskOptions(options []proxmox.VirtualMachineOption, config VMConfig) []proxmox.VirtualMachineOption {
	usePassthrough := config.CephMode == "passthrough" ||
		(config.CephMode == "" && config.CephDiskByID != "")
	useVirtual := config.CephMode == "virtual" ||
		(config.CephMode == "" && config.CephDiskByID == "" && config.CephDiskSize > 0)
	if !usePassthrough && !useVirtual {
		return options
	}

	var legacyOSDDiskOpts string
	if usePassthrough {
		legacyOSDDiskOpts = fmt.Sprintf("/dev/disk/by-id/%s", config.CephDiskByID)
	} else {
		legacyOSDStorage := config.CephStorage
		if legacyOSDStorage == "" {
			legacyOSDStorage = config.BootStorage
		}
		legacyOSDDiskOpts = fmt.Sprintf("%s:%d", legacyOSDStorage, config.CephDiskSize)
	}
	legacyOSDDiskOpts = appendDiskPerformanceOptions(legacyOSDDiskOpts, config)
	return append(options, proxmox.VirtualMachineOption{Name: "scsi2", Value: legacyOSDDiskOpts})
}

func appendDiskPerformanceOptions(opts string, config VMConfig) string {
	if config.Discard {
		opts += ",discard=on"
	}
	if config.IOThread {
		opts += ",iothread=1"
	}
	return opts
}

func buildNetworkConfig(config VMConfig, profile vmNetworkOptionsProfile) string {
	netConfig := fmt.Sprintf("virtio=%s,bridge=%s", config.MacAddress, config.NetworkBridge)
	if config.MacAddress == "" {
		netConfig = fmt.Sprintf("virtio,bridge=%s", config.NetworkBridge)
	}
	if profile.usePositiveMTUAndVLAN {
		if config.NetworkMTU > 0 {
			netConfig += fmt.Sprintf(",mtu=%d", config.NetworkMTU)
		}
	} else if config.NetworkMTU != 0 {
		netConfig += fmt.Sprintf(",mtu=%d", config.NetworkMTU)
	}
	queues := config.NetworkQueues
	if config.NetworkQueueOverrides.Net0 > 0 {
		queues = config.NetworkQueueOverrides.Net0
	}
	if profile.includeQueues && queues > 0 {
		netConfig += fmt.Sprintf(",queues=%d", queues)
	}
	if profile.usePositiveMTUAndVLAN {
		if config.VLANID > 0 {
			netConfig += fmt.Sprintf(",tag=%d", config.VLANID)
		}
	} else if config.VLANID != 0 {
		netConfig += fmt.Sprintf(",tag=%d", config.VLANID)
	}
	return netConfig
}

func talosBootOrder(VMConfig) string {
	return "order=ide2"
}

func flatcarBootOrder(config VMConfig) string {
	bootOrder := config.BootMode
	if bootOrder == "" {
		bootOrder = "order=scsi0"
	}
	return bootOrder
}

func cloudInitBootOrder(VMConfig) string {
	return "order=scsi0"
}

func addCloudInitOptions(ci CloudInitConfig) func([]proxmox.VirtualMachineOption, VMConfig) []proxmox.VirtualMachineOption {
	return func(options []proxmox.VirtualMachineOption, config VMConfig) []proxmox.VirtualMachineOption {
		options = append(options, proxmox.VirtualMachineOption{Name: "ide2", Value: fmt.Sprintf("%s:cloudinit", config.BootStorage)})
		if ci.User != "" {
			options = append(options, proxmox.VirtualMachineOption{Name: "ciuser", Value: ci.User})
		}
		if ci.SSHKeys != "" {
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
		return options
	}
}

func addFlatcarIgnitionArgs(options []proxmox.VirtualMachineOption, config VMConfig) []proxmox.VirtualMachineOption {
	if config.IgnitionPath == "" {
		return options
	}
	args := fmt.Sprintf("-fw_cfg name=opt/org.flatcar-linux/config,file=%s", config.IgnitionPath)
	return append(options, proxmox.VirtualMachineOption{Name: "args", Value: args})
}

// secondaryNIC is one extra vNIC attached purely as a Multus macvlan master.
type secondaryNIC struct {
	name  string
	value string
}

// secondaryNICs builds net1/net2 for the Multus macvlan masters. A NIC is
// emitted only when its MAC is set, so nodes that predate these interfaces (or
// non-Flatcar profiles) are provisioned exactly as before.
//
// The MAC is REQUIRED rather than auto-generated: the Butane config pins the
// interface name by MAC (10-eth1.link / 10-eth2.link) so the NAD's
// `master: eth1` / `master: eth2` keeps resolving across a machine-type change.
func secondaryNICs(config VMConfig) []secondaryNIC {
	mtu := config.SecondaryMTU
	if mtu == 0 {
		mtu = 1500
	}
	var out []secondaryNIC
	add := func(name, mac string, vlan, queues int) {
		if mac == "" || vlan == 0 {
			return
		}
		value := fmt.Sprintf("virtio=%s,bridge=%s,mtu=%d", mac, config.NetworkBridge, mtu)
		if queues > 0 {
			value += fmt.Sprintf(",queues=%d", queues)
		}
		value += fmt.Sprintf(",tag=%d", vlan)
		out = append(out, secondaryNIC{
			name:  name,
			value: value,
		})
	}
	add("net1", config.MacAddressIoT, config.VLANIDIoT, config.NetworkQueueOverrides.Net1)
	add("net2", config.MacAddressVPN, config.VLANIDVPN, config.NetworkQueueOverrides.Net2)
	return out
}

// storageNICs builds the optional NVMe-oF fabric as net3-net6. Sorting a copy
// makes slot assignment stable regardless of the order used in homeops.yaml:
// VLAN 1201 -> net3 through VLAN 1204 -> net6.
func storageNICs(config VMConfig) []secondaryNIC {
	if len(config.StorageNICs) == 0 {
		return nil
	}

	nics := append([]homeopscfg.StorageNIC(nil), config.StorageNICs...)
	sort.Slice(nics, func(i, j int) bool { return nics[i].VLAN < nics[j].VLAN })
	out := make([]secondaryNIC, 0, len(nics))
	for index, nic := range nics {
		value := fmt.Sprintf("virtio=%s,bridge=%s,tag=%d", nic.MAC, config.NetworkBridge, nic.VLAN)
		if config.NetworkMTU > 0 {
			value += fmt.Sprintf(",mtu=%d", config.NetworkMTU)
		}
		if config.NetworkQueues > 0 {
			value += fmt.Sprintf(",queues=%d", config.NetworkQueues)
		}
		out = append(out, secondaryNIC{
			name:  fmt.Sprintf("net%d", index+3),
			value: value,
		})
	}
	return out
}
