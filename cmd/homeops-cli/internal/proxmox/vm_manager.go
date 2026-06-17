package proxmox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"homeops-cli/internal/common"
	homeopscfg "homeops-cli/internal/config"
	"homeops-cli/internal/provider"
	"homeops-cli/internal/ui"

	"github.com/luthermonson/go-proxmox"
)

var (
	sleepForOperation = time.Sleep
	ErrVMNotFound     = errors.New("VM not found")
	getCredentialsFn  = GetCredentials
	newVMManagerFn    = NewVMManager
)

// VMManager satisfies the shared lifecycle contract used by command dispatch.
var _ provider.VMLifecycle = (*VMManager)(nil)

type taskHandle interface {
	Wait(context.Context, time.Duration, time.Duration) error
}

type vmHandle interface {
	Name() string
	VMID() int
	Status() string
	Start(context.Context) (taskHandle, error)
	Shutdown(context.Context) (taskHandle, error)
	Stop(context.Context) (taskHandle, error)
	Delete(context.Context) (taskHandle, error)
}

type proxmoxTaskHandle struct {
	task *proxmox.Task
}

func (t proxmoxTaskHandle) Wait(ctx context.Context, poll, timeout time.Duration) error {
	return t.task.Wait(ctx, poll, timeout)
}

type proxmoxVMHandle struct {
	vm *proxmox.VirtualMachine
}

func (h proxmoxVMHandle) Name() string {
	return h.vm.Name
}

func (h proxmoxVMHandle) VMID() int {
	return int(h.vm.VMID)
}

func (h proxmoxVMHandle) Status() string {
	return h.vm.Status
}

func (h proxmoxVMHandle) Start(ctx context.Context) (taskHandle, error) {
	task, err := h.vm.Start(ctx)
	if err != nil {
		return nil, err
	}
	return proxmoxTaskHandle{task: task}, nil
}

func (h proxmoxVMHandle) Shutdown(ctx context.Context) (taskHandle, error) {
	task, err := h.vm.Shutdown(ctx)
	if err != nil {
		return nil, err
	}
	return proxmoxTaskHandle{task: task}, nil
}

func (h proxmoxVMHandle) Stop(ctx context.Context) (taskHandle, error) {
	task, err := h.vm.Stop(ctx)
	if err != nil {
		return nil, err
	}
	return proxmoxTaskHandle{task: task}, nil
}

func (h proxmoxVMHandle) Delete(ctx context.Context) (taskHandle, error) {
	task, err := h.vm.Delete(ctx)
	if err != nil {
		return nil, err
	}
	return proxmoxTaskHandle{task: task}, nil
}

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

	// Rook-Ceph OSD disk: either a physical SSD passthrough (CephDiskByID,
	// via /dev/disk/by-id/) or a plain virtual disk (CephDiskSize GB on
	// CephStorage). Passthrough wins when both are set.
	CephMode     string // "", "passthrough", "virtual", or "none" (explicit selector)
	CephDiskByID string // e.g., "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN"
	CephDiskSize int    // virtual Ceph disk size in GB (used when CephDiskByID is empty)
	CephStorage  string // storage pool for the virtual Ceph disk (default: BootStorage)

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

	// Flatcar/kubeadm-specific. When IgnitionConfig is set the VM is treated as a
	// Flatcar node: it boots from a pre-staged Flatcar image disk (ImageDiskPath /
	// import-from) instead of an install ISO, and the rendered Ignition JSON is
	// injected via fw_cfg (qemu args). BootMode lets callers force a boot order.
	IgnitionConfig string // rendered Ignition JSON (controls Flatcar branch in buildVMOptions)
	IgnitionPath   string // Proxmox snippets path the Ignition was written to (for fw_cfg attach)
	ImageDiskPath  string // path/volume to import the Flatcar disk image from (import-from=)
	ImageVolume    string // pre-existing storage volume to use directly as scsi0 (alternative to import)

	// CloudInit, when set, deploys a general-purpose cloud-image VM (vm
	// create): imported boot disk + cloud-init drive (see cloudinit.go).
	CloudInit *CloudInitConfig
	BootMode  string // override boot order (e.g. "order=scsi0"); empty = sensible default
}

// TalosNodeConfig defines per-node configuration matching actual deployment
type TalosNodeConfig struct {
	Name           string
	VMID           int    // Proxmox VMID (200, 201, 202)
	BootStorage    string // Storage pool for boot disk
	OpenEBSStorage string // Storage pool for OpenEBS disk
	CephMode       string // "", "passthrough", "virtual", or "none"
	CephDiskByID   string // Disk passthrough by-id path for Ceph SSD
	CephDiskGB     int    // virtual Ceph disk size GB (alternative to passthrough)
	CephStorage    string // storage pool for the virtual Ceph disk
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

// FlatcarNodeConfig defines per-node configuration for Flatcar/kubeadm nodes.
// It mirrors TalosNodeConfig (same VMIDs/MACs/storage/affinity/NUMA) and adds the
// node IP + NODE_NAME used to render kubeadm/Ignition configs.
type FlatcarNodeConfig struct {
	Name           string
	VMID           int    // Proxmox VMID (same as Talos: 200, 201, 202)
	NodeIP         string // primary node IP (advertiseAddress / node-ip)
	BootStorage    string // Storage pool for boot disk
	OpenEBSStorage string // Storage pool for OpenEBS disk
	CephMode       string // "", "passthrough", "virtual", or "none"
	CephDiskByID   string // Disk passthrough by-id path for Ceph SSD
	CephDiskGB     int    // virtual Ceph disk size GB (alternative to passthrough)
	CephStorage    string // storage pool for the virtual Ceph disk
	CPUAffinity    string // CPU core pinning
	NUMANode       int    // NUMA node (0 or 1)
	MacAddress     string // Static MAC address
}

// FlatcarNodeConfigs contains pre-defined Flatcar node configurations. VMIDs, MACs,
// storage pools, CPU affinity and NUMA nodes are intentionally identical to
// TalosNodeConfigs so the migration reuses the same Proxmox slots.
var FlatcarNodeConfigs = map[string]FlatcarNodeConfig{
	"k8s-0": {
		Name:           "k8s-0",
		VMID:           200,
		NodeIP:         "192.168.122.10",
		BootStorage:    "nvme-mirror",
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN",
		CPUAffinity:    "0-7,32-39",
		NUMANode:       0,
		MacAddress:     "00:a0:98:28:c8:83",
	},
	"k8s-1": {
		Name:           "k8s-1",
		VMID:           201,
		NodeIP:         "192.168.122.11",
		BootStorage:    "nvme-mirror",
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101691P2DGN",
		CPUAffinity:    "16-23,48-55",
		NUMANode:       1,
		MacAddress:     "00:a0:98:1a:f3:72",
	},
	"k8s-2": {
		Name:           "k8s-2",
		VMID:           202,
		NodeIP:         "192.168.122.12",
		BootStorage:    "nvme-mirror",
		OpenEBSStorage: "nvmeof-vmdata",
		CephDiskByID:   "ata-INTEL_SSDSC2BB012T7_PHDV650101LU1P2DGN",
		CPUAffinity:    "8-15,40-47",
		NUMANode:       0,
		MacAddress:     "00:a0:98:3e:6c:22",
	},
}

// GetFlatcarNodeConfig retrieves the config for a Flatcar node. The hardware
// profile (VMID, storage, MAC, pinning) comes from the predefined map; the
// node IP is overridden by cluster.nodes in homeops.yaml when present, and a
// node defined only in homeops.yaml gets a minimal profile so config-driven
// topologies work without editing this file.
func GetFlatcarNodeConfig(name string) (FlatcarNodeConfig, bool) {
	nodeCfg, exists := FlatcarNodeConfigs[name]
	if homeopsNode, found := homeopscfg.Get().NodeByName(name); found {
		if !exists {
			nodeCfg = FlatcarNodeConfig{Name: name}
			exists = true
		}
		if homeopsNode.IP != "" {
			nodeCfg.NodeIP = homeopsNode.IP
		}
		applyNodeVMProfile(&nodeCfg.VMID, &nodeCfg.MacAddress, &nodeCfg.BootStorage,
			&nodeCfg.OpenEBSStorage, &nodeCfg.CephDiskByID, &nodeCfg.CPUAffinity, &nodeCfg.NUMANode,
			&nodeCfg.CephMode, &nodeCfg.CephDiskGB, &nodeCfg.CephStorage, homeopsNode.VM)
	}
	return nodeCfg, exists
}

// applyNodeVMProfile overlays a homeops.yaml per-node VM profile onto a
// predefined node hardware config; unset profile fields keep the defaults.
func applyNodeVMProfile(vmid *int, mac, bootStorage, openEBSStorage, cephDisk, cpuAffinity *string, numaNode *int, cephMode *string, cephDiskGB *int, cephStorage *string, p homeopscfg.VMProfile) {
	if p.VMID != 0 {
		*vmid = p.VMID
	}
	if p.Mac != "" {
		*mac = p.Mac
	}
	if p.BootStorage != "" {
		*bootStorage = p.BootStorage
	}
	if p.OpenEBSStorage != "" {
		*openEBSStorage = p.OpenEBSStorage
	}
	if p.Ceph.Mode != "" {
		*cephMode = p.Ceph.Mode
	}
	if p.Ceph.DiskByID != "" {
		*cephDisk = p.Ceph.DiskByID
	}
	if p.CPUAffinity != "" {
		*cpuAffinity = p.CPUAffinity
	}
	if p.NUMANode != nil {
		*numaNode = *p.NUMANode
	}
	if p.Ceph.SizeGB != 0 {
		*cephDiskGB = p.Ceph.SizeGB
	}
	if p.Ceph.Storage != "" {
		*cephStorage = p.Ceph.Storage
	}
}

// GetDefaultVMConfig returns DefaultVMConfig with the hypervisors.proxmox.vm
// overrides from homeops.yaml applied (sizing, disk backends, network).
func GetDefaultVMConfig() VMConfig {
	cfg := DefaultVMConfig
	vm := homeopscfg.Get().Hypervisors.Proxmox.VM
	if vm.MemoryMB != 0 {
		cfg.Memory = vm.MemoryMB
	}
	if vm.Cores != 0 {
		cfg.Cores = vm.Cores
	}
	if vm.BootDiskGB != 0 {
		cfg.BootDiskSize = vm.BootDiskGB
	}
	if vm.OpenEBSDiskGB != 0 {
		cfg.OpenEBSSize = vm.OpenEBSDiskGB
	}
	if vm.BootStorage != "" {
		cfg.BootStorage = vm.BootStorage
	}
	if vm.OpenEBSStorage != "" {
		cfg.OpenEBSStorage = vm.OpenEBSStorage
	}
	if vm.NetworkBridge != "" {
		cfg.NetworkBridge = vm.NetworkBridge
	}
	if vm.NetworkMTU != 0 {
		cfg.NetworkMTU = vm.NetworkMTU
	}
	if vm.VLANID != 0 {
		cfg.VLANID = vm.VLANID
	}
	if vm.Ceph.Mode != "" {
		cfg.CephMode = vm.Ceph.Mode
	}
	if vm.Ceph.DiskByID != "" {
		cfg.CephDiskByID = vm.Ceph.DiskByID
	}
	if vm.Ceph.SizeGB != 0 {
		cfg.CephDiskSize = vm.Ceph.SizeGB
	}
	if vm.Ceph.Storage != "" {
		cfg.CephStorage = vm.Ceph.Storage
	}
	return cfg
}

// DefaultVMConfig provides default VM settings matching actual deployment
var DefaultVMConfig = VMConfig{
	Memory:         98304, // 96GB
	Cores:          16,
	Sockets:        1,
	CPUType:        "host,flags=+pdpe1gb;-spec-ctrl",
	NUMAEnabled:    true,
	BootDiskSize:   100,
	BootStorage:    "nvme1",
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

func normalizeStorageConfig(config VMConfig) (VMConfig, error) {
	if config.BootStorage == "" {
		switch {
		case config.EFIDiskStorage != "":
			config.BootStorage = config.EFIDiskStorage
		case config.OpenEBSStorage != "":
			config.BootStorage = config.OpenEBSStorage
		}
	}

	if config.EFIDiskStorage == "" {
		config.EFIDiskStorage = config.BootStorage
	}

	if config.OpenEBSSize > 0 && config.OpenEBSStorage == "" {
		config.OpenEBSStorage = config.BootStorage
	}

	if config.BootDiskSize > 0 && config.BootStorage == "" {
		return config, fmt.Errorf("boot storage is required when boot disk size is set")
	}

	if config.BIOS == "ovmf" && config.EFIDiskStorage == "" {
		return config, fmt.Errorf("efi disk storage is required when BIOS is ovmf")
	}

	if config.OpenEBSSize > 0 && config.OpenEBSStorage == "" {
		return config, fmt.Errorf("openebs storage is required when OpenEBS disk size is set")
	}

	return config, nil
}

// GetTalosNodeConfig retrieves the config for a Talos node, with the
// homeops.yaml per-node VM profile overlaid (same semantics as the Flatcar
// accessor).
func GetTalosNodeConfig(name string) (TalosNodeConfig, bool) {
	nodeCfg, exists := TalosNodeConfigs[name]
	if homeopsNode, found := homeopscfg.Get().NodeByName(name); found {
		if !exists {
			nodeCfg = TalosNodeConfig{Name: name}
			exists = true
		}
		applyNodeVMProfile(&nodeCfg.VMID, &nodeCfg.MacAddress, &nodeCfg.BootStorage,
			&nodeCfg.OpenEBSStorage, &nodeCfg.CephDiskByID, &nodeCfg.CPUAffinity, &nodeCfg.NUMANode,
			&nodeCfg.CephMode, &nodeCfg.CephDiskGB, &nodeCfg.CephStorage, homeopsNode.VM)
	}
	return nodeCfg, exists
}

// VMManager handles high-level VM operations on Proxmox
type VMManager struct {
	client          *Client
	logger          *common.ColorLogger
	host            string // API host, for building web console URLs
	nodeName        string // Proxmox node, for building web console URLs
	lookupVMHandle  func(string) (vmHandle, error)
	listVMsFn       func() (proxmox.VirtualMachines, error)
	getNextVMIDFn   func() (int, error)
	createVMTaskFn  func(int, ...proxmox.VirtualMachineOption) (taskHandle, error)
	getVMHandleFn   func(int) (vmHandle, error)
	findVMByNameFn  func(string) (*proxmox.VirtualMachine, error)
	uploadISOTaskFn func(string, string, string) (taskHandle, error)
	verifyStorageFn func(string) error
	// convertToTemplateFn overrides the template-flag conversion (tests).
	convertToTemplateFn func(string) error
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
		client:   client,
		logger:   common.NewColorLogger(),
		host:     host,
		nodeName: nodeName,
	}, nil
}

// Close closes the connection
func (vm *VMManager) Close() error {
	if vm.client == nil {
		return nil
	}
	return vm.client.Close()
}

// DeployVM deploys a new VM with the specified configuration
func (vm *VMManager) DeployVM(config VMConfig) error {
	vm.logger.Info("Starting Proxmox VM deployment: %s", config.Name)

	normalizedConfig, err := normalizeStorageConfig(config)
	if err != nil {
		return fmt.Errorf("invalid VM storage configuration: %w", err)
	}
	config = normalizedConfig

	// Check if VM with same name already exists
	existingVMs, err := vm.listVMs()
	if err != nil {
		return fmt.Errorf("failed to list existing VMs: %w", err)
	}

	for _, existingVM := range existingVMs {
		if existingVM.Name == config.Name {
			return fmt.Errorf("VM with name '%s' already exists (VMID: %d)", config.Name, existingVM.VMID)
		}
	}

	// Get next available VMID or use predefined one (Talos and Flatcar share slots)
	var vmid int
	if nodeConfig, exists := GetTalosNodeConfig(config.Name); exists {
		vmid = nodeConfig.VMID
		vm.logger.Info("Using predefined VMID: %d for %s", vmid, config.Name)
	} else if flatcarConfig, exists := GetFlatcarNodeConfig(config.Name); exists {
		vmid = flatcarConfig.VMID
		vm.logger.Info("Using predefined Flatcar VMID: %d for %s", vmid, config.Name)
	} else {
		vmid, err = vm.nextVMID()
		if err != nil {
			return fmt.Errorf("failed to get next VMID: %w", err)
		}
		vm.logger.Info("Assigned VMID: %d", vmid)
	}

	// Fail fast if the target VMID is already taken by a *different* VM. The name
	// check above misses this (e.g. a leftover VM from a prior deploy, or a
	// Talos<->Flatcar slot reused under a different name); without this guard
	// createVMTask would fail partway and leave inconsistent state.
	for _, existingVM := range existingVMs {
		if int(existingVM.VMID) == vmid {
			return fmt.Errorf("VMID %d is already in use by VM '%s'; delete it or free the VMID before deploying %s",
				vmid, existingVM.Name, config.Name)
		}
	}

	// Preflight: confirm the storage pools resolve on the node before we mutate.
	if err := vm.preflightStorage(config); err != nil {
		return err
	}

	// Build VM options. Flatcar nodes (IgnitionConfig set) use the Ignition/fw_cfg
	// boot path; everything else uses the Talos ISO path. Talos behavior unchanged.
	var options []proxmox.VirtualMachineOption
	switch {
	case config.CloudInit != nil:
		options = vm.buildCloudInitVMOptions(config, *config.CloudInit)
	case config.IgnitionConfig != "" || config.ImageDiskPath != "" || config.ImageVolume != "":
		options = vm.buildFlatcarVMOptions(config)
	default:
		options = vm.buildVMOptions(config)
	}

	// Create the VM
	task, err := vm.createVMTask(vmid, options...)
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
		vmObj, err := vm.vmHandleByID(vmid)
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

	// Rook-Ceph OSD disk (scsi2). CephMode selects explicitly: "passthrough"
	// (physical disk via /dev/disk/by-id), "virtual" (plain virtual disk —
	// no dedicated SSD needed), "none" (no Ceph disk, even if a built-in
	// node profile sets one). Empty mode keeps the legacy inference:
	// passthrough when a disk id is set, else virtual when a size is set.
	usePassthrough := config.CephMode == "passthrough" ||
		(config.CephMode == "" && config.CephDiskByID != "")
	useVirtual := config.CephMode == "virtual" ||
		(config.CephMode == "" && config.CephDiskByID == "" && config.CephDiskSize > 0)
	if usePassthrough || useVirtual {
		var cephDiskOpts string
		if usePassthrough {
			cephDiskOpts = fmt.Sprintf("/dev/disk/by-id/%s", config.CephDiskByID)
		} else {
			cephStorage := config.CephStorage
			if cephStorage == "" {
				cephStorage = config.BootStorage
			}
			cephDiskOpts = fmt.Sprintf("%s:%d", cephStorage, config.CephDiskSize)
		}
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

// buildFlatcarVMOptions builds the VirtualMachineOption slice for a Flatcar/kubeadm
// node. It mirrors buildVMOptions (CPU/NUMA/UEFI/network/watchdog/agent) but:
//   - boots scsi0 from a pre-staged Flatcar disk image (import-from=<path>) or an
//     existing volume (ImageVolume) instead of installing from an ISO,
//   - keeps scsi1 = OpenEBS and scsi2 = Ceph passthrough,
//   - injects the rendered Ignition via fw_cfg:
//     args = -fw_cfg name=opt/org.flatcar-linux/config,file=<IgnitionPath>
//
// This does not alter the Talos buildVMOptions path.
func (vm *VMManager) buildFlatcarVMOptions(config VMConfig) []proxmox.VirtualMachineOption {
	options := []proxmox.VirtualMachineOption{
		{Name: "name", Value: config.Name},
		{Name: "memory", Value: config.Memory},
		{Name: "cores", Value: config.Cores},
		{Name: "sockets", Value: config.Sockets},
		{Name: "ostype", Value: "l26"},
	}

	// CPU configuration
	if config.CPUType != "" {
		options = append(options, proxmox.VirtualMachineOption{Name: "cpu", Value: config.CPUType})
	}
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

	// UEFI boot configuration (Flatcar amd64-usr images are UEFI-capable)
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

	// Boot disk (scsi0): import the Flatcar image, or attach an existing volume.
	bootDiskOpts := vm.flatcarBootDiskOpts(config)
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

	// Rook-Ceph OSD disk (scsi2). CephMode selects explicitly: "passthrough"
	// (physical disk via /dev/disk/by-id), "virtual" (plain virtual disk —
	// no dedicated SSD needed), "none" (no Ceph disk, even if a built-in
	// node profile sets one). Empty mode keeps the legacy inference:
	// passthrough when a disk id is set, else virtual when a size is set.
	usePassthrough := config.CephMode == "passthrough" ||
		(config.CephMode == "" && config.CephDiskByID != "")
	useVirtual := config.CephMode == "virtual" ||
		(config.CephMode == "" && config.CephDiskByID == "" && config.CephDiskSize > 0)
	if usePassthrough || useVirtual {
		var cephDiskOpts string
		if usePassthrough {
			cephDiskOpts = fmt.Sprintf("/dev/disk/by-id/%s", config.CephDiskByID)
		} else {
			cephStorage := config.CephStorage
			if cephStorage == "" {
				cephStorage = config.BootStorage
			}
			cephDiskOpts = fmt.Sprintf("%s:%d", cephStorage, config.CephDiskSize)
		}
		if config.Discard {
			cephDiskOpts += ",discard=on"
		}
		if config.IOThread {
			cephDiskOpts += ",iothread=1"
		}
		options = append(options, proxmox.VirtualMachineOption{Name: "scsi2", Value: cephDiskOpts})
	}

	// Boot order: Flatcar boots straight from the imported disk (no install ISO).
	bootOrder := config.BootMode
	if bootOrder == "" {
		bootOrder = "order=scsi0"
	}
	options = append(options, proxmox.VirtualMachineOption{Name: "boot", Value: bootOrder})

	// Network configuration (identical to Talos: MAC + jumbo MTU + queues + VLAN)
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

	// Ignition injection via fw_cfg. Flatcar reads the Ignition config from the
	// qemu fw_cfg blob opt/org.flatcar-linux/config. The file must live somewhere
	// the Proxmox node can read (e.g. the snippets dir); IgnitionPath carries that
	// absolute path on the Proxmox host.
	if config.IgnitionPath != "" {
		args := fmt.Sprintf("-fw_cfg name=opt/org.flatcar-linux/config,file=%s", config.IgnitionPath)
		options = append(options, proxmox.VirtualMachineOption{Name: "args", Value: args})
	}

	return options
}

// flatcarBootDiskOpts builds the scsi0 value for a Flatcar node. Preference order:
//  1. ImageVolume: attach an existing storage volume directly.
//  2. ImageDiskPath: import a disk image into BootStorage (import-from=).
//  3. fallback: a blank boot disk of BootDiskSize on BootStorage.
func (vm *VMManager) flatcarBootDiskOpts(config VMConfig) string {
	var opts string
	switch {
	case config.ImageVolume != "":
		opts = config.ImageVolume
	case config.ImageDiskPath != "":
		size := config.BootDiskSize
		if size <= 0 {
			size = 200
		}
		opts = fmt.Sprintf("%s:%d,import-from=%s", config.BootStorage, size, config.ImageDiskPath)
	default:
		size := config.BootDiskSize
		if size <= 0 {
			size = 200
		}
		opts = fmt.Sprintf("%s:%d", config.BootStorage, size)
	}
	if config.Discard {
		opts += ",discard=on"
	}
	if config.IOThread {
		opts += ",iothread=1"
	}
	return opts
}

// ListVMs lists all VMs
func (vm *VMManager) ListVMs() error {
	vms, err := vm.listVMs()
	if err != nil {
		return fmt.Errorf("failed to list VMs: %w", err)
	}

	fmt.Print(formatVMList(vms))
	return nil
}

// StartVM starts a VM by name
func (vm *VMManager) StartVM(name string) error {
	vmObj, err := vm.getVMHandle(name)
	if err != nil {
		return err
	}

	vm.logger.Info("Starting VM: %s (VMID: %d)", name, vmObj.VMID())

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
	vmObj, err := vm.getVMHandle(name)
	if err != nil {
		return err
	}

	action := "Stopping"
	if force {
		action = "Force stopping"
	}
	vm.logger.Info("%s VM: %s (VMID: %d)", action, name, vmObj.VMID())

	var task taskHandle
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
	vmObj, err := vm.getVMHandle(name)
	if err != nil {
		return err
	}

	vm.logger.Info("Deleting VM: %s (VMID: %d)", name, vmObj.VMID())

	// Stop VM if running
	if vmObj.Status() == string(proxmox.StatusVirtualMachineRunning) {
		vm.logger.Info("Stopping running VM before deletion...")
		task, err := vmObj.Stop(vm.client.Context())
		if err != nil {
			return fmt.Errorf("failed to stop running VM before deletion: %w", err)
		}
		if err := task.Wait(vm.client.Context(), time.Second, 60*time.Second); err != nil {
			return fmt.Errorf("stop task failed before deletion: %w", err)
		}
		sleepForOperation(2 * time.Second)
	}

	// Delete VM
	task, err := vmObj.Delete(vm.client.Context())
	if err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	if err := task.Wait(vm.client.Context(), time.Second, 120*time.Second); err != nil {
		return fmt.Errorf("delete task failed: %w", err)
	}

	if err := vm.verifyVMDeletion(name); err != nil {
		return err
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

	fmt.Print(formatVMInfo(name, vmObj))
	return nil
}

// findVMByName finds a VM by name
func (vm *VMManager) findVMByName(name string) (*proxmox.VirtualMachine, error) {
	if vm.findVMByNameFn != nil {
		return vm.findVMByNameFn(name)
	}

	vms, err := vm.listVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}

	for _, vmItem := range vms {
		if vmItem.Name == name {
			return vm.client.GetVM(int(vmItem.VMID))
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrVMNotFound, name)
}

func (vm *VMManager) listVMs() (proxmox.VirtualMachines, error) {
	if vm.listVMsFn != nil {
		return vm.listVMsFn()
	}
	return vm.client.ListVMs()
}

// verifyStorage confirms a storage pool resolves on the node. Swappable for tests.
func (vm *VMManager) verifyStorage(name string) error {
	if vm.verifyStorageFn != nil {
		return vm.verifyStorageFn(name)
	}
	_, err := vm.client.GetStorage(name)
	return err
}

// preflightStorage confirms every storage pool the deploy will use actually exists
// on the node BEFORE any VM is created, turning an opaque mid-create go-proxmox
// failure into a clear up-front error. Empty names are skipped (a given path may
// not use OpenEBS/EFI); each distinct pool is checked once.
func (vm *VMManager) preflightStorage(config VMConfig) error {
	seen := make(map[string]bool)
	for _, name := range []string{config.BootStorage, config.EFIDiskStorage, config.OpenEBSStorage} {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if err := vm.verifyStorage(name); err != nil {
			return fmt.Errorf("preflight: storage pool %q is not available on the node: %w", name, err)
		}
	}
	return nil
}

func (vm *VMManager) nextVMID() (int, error) {
	if vm.getNextVMIDFn != nil {
		return vm.getNextVMIDFn()
	}
	return vm.client.GetNextVMID()
}

func (vm *VMManager) createVMTask(vmid int, options ...proxmox.VirtualMachineOption) (taskHandle, error) {
	if vm.createVMTaskFn != nil {
		return vm.createVMTaskFn(vmid, options...)
	}
	task, err := vm.client.CreateVM(vmid, options...)
	if err != nil {
		return nil, err
	}
	return proxmoxTaskHandle{task: task}, nil
}

func (vm *VMManager) vmHandleByID(vmid int) (vmHandle, error) {
	if vm.getVMHandleFn != nil {
		return vm.getVMHandleFn(vmid)
	}
	vmObj, err := vm.client.GetVM(vmid)
	if err != nil {
		return nil, err
	}
	return proxmoxVMHandle{vm: vmObj}, nil
}

func formatVMList(vms proxmox.VirtualMachines) string {
	if len(vms) == 0 {
		return "No virtual machines found.\n"
	}

	var b strings.Builder
	writeVMList(&b, vms)
	return b.String()
}

func writeVMList(w io.Writer, vms proxmox.VirtualMachines) {
	summaries := summarizeVMs(vms)
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		rows = append(rows, []string{
			s.ID, s.Name, s.Status,
			fmt.Sprintf("%d", s.MemoryMB), fmt.Sprintf("%d", s.CPUs), s.Details["uptime_seconds"],
		})
	}
	_, _ = fmt.Fprintln(w, ui.Table([]string{"VMID", "NAME", "STATUS", "MEMORY(MB)", "CPUS", "UPTIME(S)"}, rows))
}

// summarizeVMs maps the Proxmox inventory onto the provider-neutral shape.
func summarizeVMs(vms proxmox.VirtualMachines) []provider.VMSummary {
	summaries := make([]provider.VMSummary, 0, len(vms))
	for _, vmItem := range vms {
		summaries = append(summaries, provider.VMSummary{
			Name:     vmItem.Name,
			ID:       fmt.Sprintf("%d", vmItem.VMID),
			Status:   vmItem.Status,
			MemoryMB: int(vmItem.MaxMem / (1024 * 1024)),
			CPUs:     vmItem.CPUs,
			Details:  map[string]string{"uptime_seconds": fmt.Sprintf("%d", vmItem.Uptime)},
		})
	}
	return summaries
}

// VMSummaries returns the inventory in the provider-neutral shape.
func (vm *VMManager) VMSummaries() ([]provider.VMSummary, error) {
	vms, err := vm.listVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	return summarizeVMs(vms), nil
}

func formatVMInfo(name string, vmObj *proxmox.VirtualMachine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VM Information for: %s\n", name)
	fmt.Fprintf(&b, "VMID: %d\n", vmObj.VMID)
	fmt.Fprintf(&b, "Node: %s\n", vmObj.Node)
	fmt.Fprintf(&b, "Status: %s\n", vmObj.Status)
	fmt.Fprintf(&b, "Memory: %d MB (max: %d MB)\n", vmObj.Mem/(1024*1024), vmObj.MaxMem/(1024*1024))
	fmt.Fprintf(&b, "CPUs: %d\n", vmObj.CPUs)
	fmt.Fprintf(&b, "Uptime: %d seconds\n", vmObj.Uptime)

	if vmObj.VirtualMachineConfig != nil {
		config := vmObj.VirtualMachineConfig
		fmt.Fprintln(&b, "\nConfiguration:")
		fmt.Fprintf(&b, "  Name: %s\n", config.Name)
		fmt.Fprintf(&b, "  Cores: %d\n", derefInt(config.Cores))
		fmt.Fprintf(&b, "  Sockets: %d\n", derefInt(config.Sockets))
		fmt.Fprintf(&b, "  BIOS: %s\n", derefStr(config.Bios))
		fmt.Fprintf(&b, "  SCSI HW: %s\n", derefStr(config.SCSIHW))
	}

	return b.String()
}

// derefInt / derefStr safely dereference go-proxmox's *int / *string config
// fields (e.g. Cores, Sockets, Bios, SCSIHW), returning the zero value when the
// pointer is nil so display formatting never prints a pointer address (#199).
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (vm *VMManager) getVMHandle(name string) (vmHandle, error) {
	if vm.lookupVMHandle != nil {
		return vm.lookupVMHandle(name)
	}

	vmObj, err := vm.findVMByName(name)
	if err != nil {
		return nil, err
	}
	return proxmoxVMHandle{vm: vmObj}, nil
}

func (vm *VMManager) verifyVMDeletion(name string) error {
	_, err := vm.getVMHandle(name)
	if err != nil {
		if errors.Is(err, ErrVMNotFound) {
			return nil
		}
		return fmt.Errorf("failed to verify VM deletion: %w", err)
	}

	vm.logger.Warn("VM %s still appears after delete task completion; retrying verification", name)
	sleepForOperation(2 * time.Second)

	_, err = vm.getVMHandle(name)
	if err != nil {
		if errors.Is(err, ErrVMNotFound) {
			return nil
		}
		return fmt.Errorf("failed to verify VM deletion after retry: %w", err)
	}

	return fmt.Errorf("VM %s still exists after deletion request", name)
}

// GetVMNames returns a list of VM names (for CLI completion)
func GetVMNames() ([]string, error) {
	host, tokenID, secret, nodeName, err := getCredentialsFn()
	if err != nil {
		return nil, err
	}

	manager, err := newVMManagerFn(host, tokenID, secret, nodeName, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = manager.Close() }()

	vms, err := manager.listVMs()
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

	task, err := vm.uploadISOTask(storageName, isoURL, filename)
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

func (vm *VMManager) uploadISOTask(storageName, isoURL, filename string) (taskHandle, error) {
	if vm.uploadISOTaskFn != nil {
		return vm.uploadISOTaskFn(storageName, isoURL, filename)
	}
	task, err := vm.client.UploadISO(storageName, isoURL, filename)
	if err != nil {
		return nil, err
	}
	return proxmoxTaskHandle{task: task}, nil
}
