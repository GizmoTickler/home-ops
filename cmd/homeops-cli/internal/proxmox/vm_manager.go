package proxmox

import (
	"fmt"
	"strings"
	"time"

	"homeops-cli/internal/common"

	"github.com/luthermonson/go-proxmox"
)

// VMConfig represents the configuration for Proxmox VM deployment
type VMConfig struct {
	Name    string
	Memory  int // MB (default: 98304 = 96GB)
	Cores   int // CPU cores (default: 16)
	Sockets int // CPU sockets (default: 1)

	// CPU Configuration
	CPUType     string // e.g., "host,flags=+pdpe1gb;-spec-ctrl"
	CPUAffinity string // e.g., "0-7,32-39"

	// NUMA Configuration
	NUMAEnabled bool // Enable NUMA
	NUMANode    int  // Host NUMA node (0 or 1)

	// Storage configuration (multi-pool support)
	BootDiskSize   int    // Boot disk GB (default: 200)
	BootStorage    string // Storage pool for boot disk (e.g., "nvme1", "nvme2")
	OpenEBSSize    int    // OpenEBS disk GB (default: 800)
	OpenEBSStorage string // Storage pool for OpenEBS (e.g., "nvmeof-vmdata")

	// Disk Passthrough for Rook-Ceph SSDs (via /dev/disk/by-id/)
	CephDiskByID string // e.g., "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN"

	// Proxmox-specific configuration
	Node       string // Proxmox node name (default: "pve")
	ISOStorage string // Storage for ISOs (default: "local")
	ISOPath    string // Full ISO path (e.g., "local:iso/talos-1.12.2-no-multipath.iso")

	// Network Configuration
	NetworkBridge string // e.g., "vmbr0"
	NetworkMTU    int    // e.g., 9000 for jumbo frames
	NetworkQueues int    // e.g., 8
	VLANID        int    // e.g., 999
	MacAddress    string // Static MAC address

	// SCSI Configuration
	SCSIController string // e.g., "virtio-scsi-single"
	IOThread       bool   // Enable iothread per disk (default: true)
	Discard        bool   // Enable discard/TRIM (default: true)

	// UEFI Boot configuration (required for Talos)
	BIOS           string // "ovmf" for UEFI
	EFIDiskStorage string // Storage for EFI disk

	// Additional Features
	WatchdogModel  string // e.g., "i6300esb"
	WatchdogAction string // e.g., "reset"
	AgentEnabled   bool   // QEMU guest agent

	// Deployment options
	PowerOn     bool
	StartOnBoot bool

	// Talos-specific
	SchematicID  string
	TalosVersion string
	CustomISO    bool
}

// TalosNodeConfig defines per-node configuration matching actual deployment
type TalosNodeConfig struct {
	Name           string
	VMID           int    // Proxmox VMID (200, 201, 202)
	BootStorage    string // Storage pool for boot disk
	OpenEBSStorage string // Storage pool for OpenEBS disk
	CephDiskByID   string // Disk passthrough by-id path for Ceph SSD
	CPUAffinity    string // CPU core pinning
	NUMANode       int    // NUMA node (0 or 1)
	MacAddress     string // Static MAC address
}

// TalosNodeConfigs contains pre-defined Talos node configurations
var TalosNodeConfigs = map[string]TalosNodeConfig{
	"k8s-0": {
		Name:           "k8s-0",
		VMID:           200,
		BootStorage:    "nvme1",
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN",
		CPUAffinity:    "0-7,32-39",
		NUMANode:       0,
		MacAddress:     "00:a0:98:28:c8:83",
	},
	"k8s-1": {
		Name:           "k8s-1",
		VMID:           201,
		BootStorage:    "nvme2",
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101691P2DGN",
		CPUAffinity:    "16-23,48-55",
		NUMANode:       1,
		MacAddress:     "00:a0:98:1a:f3:72",
	},
	"k8s-2": {
		Name:           "k8s-2",
		VMID:           202,
		BootStorage:    "nvme1",
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101LU1P2DGN",
		CPUAffinity:    "8-15,40-47",
		NUMANode:       0,
		MacAddress:     "00:a0:98:3e:6c:22",
	},
}

// DefaultVMConfig provides default VM settings matching actual deployment
var DefaultVMConfig = VMConfig{
	Memory:         98304, // 96GB
	Cores:          16,
	Sockets:        1,
	CPUType:        "host,flags=+pdpe1gb;-spec-ctrl",
	NUMAEnabled:    true,
	BootDiskSize:   200,
	OpenEBSSize:    800,
	OpenEBSStorage: "nvmeof-vmdata",
	Node:           "pve",
	ISOStorage:     "local",
	NetworkBridge:  "vmbr0",
	NetworkMTU:     9000,
	NetworkQueues:  8,
	VLANID:         999,
	SCSIController: "virtio-scsi-single",
	IOThread:       true,
	Discard:        true,
	BIOS:           "ovmf",
	WatchdogModel:  "i6300esb",
	WatchdogAction: "reset",
	AgentEnabled:   true,
}

// GetTalosNodeConfig retrieves predefined config for a Talos node
func GetTalosNodeConfig(name string) (TalosNodeConfig, bool) {
	config, exists := TalosNodeConfigs[name]
	return config, exists
}

// VMManager handles high-level VM operations on Proxmox
type VMManager struct {
	client *Client
	logger *common.ColorLogger
}

// NewVMManager creates a new VM manager
func NewVMManager(host, tokenID, secret, nodeName string, insecure bool) (*VMManager, error) {
	client, err := NewClient(host, tokenID, secret, insecure)
	if err != nil {
		return nil, err
	}

	if err := client.Connect(nodeName); err != nil {
		return nil, err
	}

	return &VMManager{
		client: client,
		logger: common.NewColorLogger(),
	}, nil
}

// Close closes the connection
func (vm *VMManager) Close() error {
	return vm.client.Close()
}

// DeployVM deploys a new VM with the specified configuration
func (vm *VMManager) DeployVM(config VMConfig) error {
	vm.logger.Info("Starting Proxmox VM deployment: %s", config.Name)

	// Check if VM with same name already exists
	existingVMs, err := vm.client.ListVMs()
	if err != nil {
		return fmt.Errorf("failed to list existing VMs: %w", err)
	}

	for _, existingVM := range existingVMs {
		if existingVM.Name == config.Name {
			return fmt.Errorf("VM with name '%s' already exists (VMID: %d)", config.Name, existingVM.VMID)
		}
	}

	// Get next available VMID or use predefined one
	var vmid int
	if nodeConfig, exists := GetTalosNodeConfig(config.Name); exists {
		vmid = nodeConfig.VMID
		vm.logger.Info("Using predefined VMID: %d for %s", vmid, config.Name)
	} else {
		vmid, err = vm.client.GetNextVMID()
		if err != nil {
			return fmt.Errorf("failed to get next VMID: %w", err)
		}
		vm.logger.Info("Assigned VMID: %d", vmid)
	}

	// Build VM options for Talos
	options := vm.buildVMOptions(config)

	// Create the VM
	task, err := vm.client.CreateVM(vmid, options...)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// Wait for task completion
	vm.logger.Info("Waiting for VM creation task to complete...")
	if err := task.Wait(vm.client.Context(), time.Second, 120*time.Second); err != nil {
		return fmt.Errorf("VM creation task failed: %w", err)
	}

	vm.logger.Success("VM %s created successfully with VMID: %d", config.Name, vmid)

	// Power on if requested
	if config.PowerOn {
		vm.logger.Info("Powering on VM %s...", config.Name)
		vmObj, err := vm.client.GetVM(vmid)
		if err != nil {
			return fmt.Errorf("failed to get VM for power on: %w", err)
		}

		task, err := vmObj.Start(vm.client.Context())
		if err != nil {
			return fmt.Errorf("failed to start VM: %w", err)
		}

		if err := task.Wait(vm.client.Context(), time.Second, 60*time.Second); err != nil {
			return fmt.Errorf("power on task failed: %w", err)
		}
		vm.logger.Success("VM %s powered on", config.Name)
	}

	vm.logger.Success("Successfully deployed VM: %s (VMID: %d)", config.Name, vmid)
	return nil
}

// buildVMOptions builds the VirtualMachineOption slice for Talos
func (vm *VMManager) buildVMOptions(config VMConfig) []proxmox.VirtualMachineOption {
	options := []proxmox.VirtualMachineOption{
		{Name: "name", Value: config.Name},
		{Name: "memory", Value: config.Memory},
		{Name: "cores", Value: config.Cores},
		{Name: "sockets", Value: config.Sockets},
		{Name: "ostype", Value: "l26"}, // Linux 2.6+ kernel
	}

	// CPU configuration
	if config.CPUType != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "cpu", Value: config.CPUType})
	}

	// CPU affinity
	if config.CPUAffinity != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "affinity", Value: config.CPUAffinity})
	}

	// NUMA configuration
	if config.NUMAEnabled {
		options = append(options, proxmox.VirtualMachineOption{Name: "numa", Value: 1})
		numaConfig := fmt.Sprintf("cpus=0-%d,hostnodes=%d,memory=%d,policy=bind",
			config.Cores-1, config.NUMANode, config.Memory)
		options = append(options, proxmox.VirtualMachineOption{Name: "numa0", Value: numaConfig})
	}

	// UEFI boot configuration (required for Talos)
	if config.BIOS == "ovmf" {
		options = append(options, proxmox.VirtualMachineOption{Name: "bios", Value: "ovmf"})
		efiStorage := config.EFIDiskStorage
		if efiStorage == "" {
			efiStorage = config.BootStorage
		}
		efiDisk := fmt.Sprintf("%s:1,efitype=4m,pre-enrolled-keys=0", efiStorage)
		options = append(options, proxmox.VirtualMachineOption{Name: "efidisk0", Value: efiDisk})
	}

	// SCSI controller
	if config.SCSIController != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "scsihw", Value: config.SCSIController})
	}

	// Boot disk (scsi0)
	bootDiskOpts := fmt.Sprintf("%s:%d", config.BootStorage, config.BootDiskSize)
	if config.Discard {
		bootDiskOpts += ",discard=on"
	}
	if config.IOThread {
		bootDiskOpts += ",iothread=1"
	}
	options = append(options, proxmox.VirtualMachineOption{Name: "scsi0", Value: bootDiskOpts})

	// OpenEBS disk (scsi1)
	if config.OpenEBSSize > 0 && config.OpenEBSStorage != "" {
		openebsDiskOpts := fmt.Sprintf("%s:%d", config.OpenEBSStorage, config.OpenEBSSize)
		if config.Discard {
			openebsDiskOpts += ",discard=on"
		}
		if config.IOThread {
			openebsDiskOpts += ",iothread=1"
		}
		options = append(options, proxmox.VirtualMachineOption{Name: "scsi1", Value: openebsDiskOpts})
	}

	// Ceph SSD disk passthrough (scsi2)
	if config.CephDiskByID != "" {
		cephDiskPath := fmt.Sprintf("/dev/disk/by-id/%s", config.CephDiskByID)
		cephDiskOpts := cephDiskPath
		if config.Discard {
			cephDiskOpts += ",discard=on"
		}
		if config.IOThread {
			cephDiskOpts += ",iothread=1"
		}
		options = append(options, proxmox.VirtualMachineOption{Name: "scsi2", Value: cephDiskOpts})
	}

	// CD-ROM with ISO
	if config.ISOPath != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "ide2", Value: config.ISOPath + ",media=cdrom"})
	}

	// Boot order - boot from CD first for initial install
	options = append(options, proxmox.VirtualMachineOption{Name: "boot", Value: "order=ide2"})

	// Network configuration
	netConfig := fmt.Sprintf("virtio=%s,bridge=%s", config.MacAddress, config.NetworkBridge)
	if config.MacAddress == "" {
		netConfig = fmt.Sprintf("virtio,bridge=%s", config.NetworkBridge)
	}
	if config.NetworkMTU > 0 {
		netConfig += fmt.Sprintf(",mtu=%d", config.NetworkMTU)
	}
	if config.NetworkQueues > 0 {
		netConfig += fmt.Sprintf(",queues=%d", config.NetworkQueues)
	}
	if config.VLANID > 0 {
		netConfig += fmt.Sprintf(",tag=%d", config.VLANID)
	}
	options = append(options, proxmox.VirtualMachineOption{Name: "net0", Value: netConfig})

	// Watchdog
	if config.WatchdogModel != "" {
		watchdogOpts := fmt.Sprintf("model=%s", config.WatchdogModel)
		if config.WatchdogAction != "" {
			watchdogOpts += fmt.Sprintf(",action=%s", config.WatchdogAction)
		}
		options = append(options, proxmox.VirtualMachineOption{Name: "watchdog", Value: watchdogOpts})
	}

	// QEMU agent
	if config.AgentEnabled {
		options = append(options, proxmox.VirtualMachineOption{Name: "agent", Value: "enabled=1"})
	}

	// Auto-start configuration
	if config.StartOnBoot {
		options = append(options, proxmox.VirtualMachineOption{Name: "onboot", Value: 1})
	}

	return options
}

// ListVMs lists all VMs
func (vm *VMManager) ListVMs() error {
	vms, err := vm.client.ListVMs()
	if err != nil {
		return fmt.Errorf("failed to list VMs: %w", err)
	}

	if len(vms) == 0 {
		fmt.Println("No virtual machines found.")
		return nil
	}

	fmt.Printf("%-6s %-20s %-10s %-12s %-6s %-12s\n", "VMID", "Name", "Status", "Memory(MB)", "CPUs", "Uptime(s)")
	fmt.Println(strings.Repeat("-", 75))

	for _, vmItem := range vms {
		memMB := vmItem.MaxMem / (1024 * 1024)
		fmt.Printf("%-6d %-20s %-10s %-12d %-6d %-12d\n",
			vmItem.VMID, vmItem.Name, vmItem.Status, memMB, vmItem.CPUs, vmItem.Uptime)
	}

	return nil
}

// StartVM starts a VM by name
func (vm *VMManager) StartVM(name string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}

	vm.logger.Info("Starting VM: %s (VMID: %d)", name, vmObj.VMID)

	task, err := vmObj.Start(vm.client.Context())
	if err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	if err := task.Wait(vm.client.Context(), time.Second, 60*time.Second); err != nil {
		return fmt.Errorf("start task failed: %w", err)
	}

	vm.logger.Success("VM %s started successfully", name)
	return nil
}

// StopVM stops a VM by name (graceful shutdown or force)
func (vm *VMManager) StopVM(name string, force bool) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}

	action := "Stopping"
	if force {
		action = "Force stopping"
	}
	vm.logger.Info("%s VM: %s (VMID: %d)", action, name, vmObj.VMID)

	var task *proxmox.Task
	if force {
		task, err = vmObj.Stop(vm.client.Context())
	} else {
		task, err = vmObj.Shutdown(vm.client.Context())
	}
	if err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	if err := task.Wait(vm.client.Context(), time.Second, 120*time.Second); err != nil {
		return fmt.Errorf("stop task failed: %w", err)
	}

	vm.logger.Success("VM %s stopped successfully", name)
	return nil
}

// DeleteVM deletes a VM by name
func (vm *VMManager) DeleteVM(name string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}

	vm.logger.Info("Deleting VM: %s (VMID: %d)", name, vmObj.VMID)

	// Stop VM if running
	if vmObj.Status == proxmox.StatusVirtualMachineRunning {
		vm.logger.Info("Stopping running VM before deletion...")
		task, err := vmObj.Stop(vm.client.Context())
		if err == nil {
			_ = task.Wait(vm.client.Context(), time.Second, 60*time.Second)
			time.Sleep(2 * time.Second)
		}
	}

	// Delete VM
	task, err := vmObj.Delete(vm.client.Context())
	if err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	if err := task.Wait(vm.client.Context(), time.Second, 120*time.Second); err != nil {
		return fmt.Errorf("delete task failed: %w", err)
	}

	vm.logger.Success("VM %s deleted successfully", name)
	return nil
}

// GetVMInfo displays detailed information about a VM
func (vm *VMManager) GetVMInfo(name string) error {
	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return err
	}

	fmt.Printf("VM Information for: %s\n", name)
	fmt.Printf("VMID: %d\n", vmObj.VMID)
	fmt.Printf("Node: %s\n", vmObj.Node)
	fmt.Printf("Status: %s\n", vmObj.Status)
	fmt.Printf("Memory: %d MB (max: %d MB)\n", vmObj.Mem/(1024*1024), vmObj.MaxMem/(1024*1024))
	fmt.Printf("CPUs: %d\n", vmObj.CPUs)
	fmt.Printf("Uptime: %d seconds\n", vmObj.Uptime)

	// Get detailed config from the VirtualMachineConfig field
	if vmObj.VirtualMachineConfig != nil {
		config := vmObj.VirtualMachineConfig
		fmt.Printf("\nConfiguration:\n")
		fmt.Printf("  Name: %s\n", config.Name)
		fmt.Printf("  Cores: %d\n", config.Cores)
		fmt.Printf("  Sockets: %d\n", config.Sockets)
		fmt.Printf("  BIOS: %s\n", config.Bios)
		fmt.Printf("  SCSI HW: %s\n", config.SCSIHW)
	}

	return nil
}

// findVMByName finds a VM by name
func (vm *VMManager) findVMByName(name string) (*proxmox.VirtualMachine, error) {
	vms, err := vm.client.ListVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}

	for _, vmItem := range vms {
		if vmItem.Name == name {
			// Get full VM object - convert StringOrUint64 to int
			return vm.client.GetVM(int(vmItem.VMID))
		}
	}

	return nil, fmt.Errorf("VM '%s' not found", name)
}

// GetVMNames returns a list of VM names (for CLI completion)
func GetVMNames() ([]string, error) {
	host, tokenID, secret, nodeName, err := GetCredentials()
	if err != nil {
		return nil, err
	}

	manager, err := NewVMManager(host, tokenID, secret, nodeName, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = manager.Close() }()

	vms, err := manager.client.ListVMs()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(vms))
	for _, vmItem := range vms {
		names = append(names, vmItem.Name)
	}

	return names, nil
}

// UploadISOFromURL downloads an ISO from URL to Proxmox storage
func (vm *VMManager) UploadISOFromURL(isoURL, filename, storageName string) error {
	vm.logger.Info("Downloading ISO to Proxmox storage: %s", filename)

	task, err := vm.client.UploadISO(storageName, isoURL, filename)
	if err != nil {
		return fmt.Errorf("failed to initiate ISO download: %w", err)
	}

	// Wait for download to complete (may take several minutes)
	if err := task.Wait(vm.client.Context(), 5*time.Second, 600*time.Second); err != nil {
		return fmt.Errorf("ISO download failed: %w", err)
	}

	vm.logger.Success("ISO downloaded successfully: %s:iso/%s", storageName, filename)
	return nil
}

// GetISOPath returns the Proxmox-format ISO path
func GetISOPath(storageName, filename string) string {
	return fmt.Sprintf("%s:iso/%s", storageName, filename)
}
