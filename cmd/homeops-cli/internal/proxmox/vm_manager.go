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
	id, ok := common.SafeUint64ToInt(uint64(h.vm.VMID))
	if !ok {
		common.NewColorLogger().Warn("Proxmox VMID %d exceeds int range; clamping to %d", h.vm.VMID, id)
	}
	return id
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
	// go-proxmox v0.8.0 added a delete-options arg; nil keeps PVE defaults
	// (remove the VM config and its owned disks), matching prior behavior.
	task, err := h.vm.Delete(ctx, nil)
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
	OpenEBSSize    int    // OpenEBS disk GB
	OpenEBSStorage string // Storage pool for OpenEBS (e.g., "openebs-ssd")
	OpenEBSSlot    string // Proxmox SCSI slot (default: scsi1; Flatcar: scsi3)
	OpenEBSSSD     bool   // Expose the OpenEBS disk as an SSD

	// Legacy OSD disk retained for nodes[].vm.ceph compatibility: either a
	// physical SSD passthrough (CephDiskByID) or a virtual disk. mode=none
	// disables the attachment.
	CephMode     string // "", "passthrough", "virtual", or "none" (explicit selector)
	CephDiskByID string // e.g., "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN"
	CephDiskSize int    // virtual legacy OSD disk size in GB (used when CephDiskByID is empty)
	CephStorage  string // storage pool for the virtual legacy OSD disk (default: BootStorage)

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
	CephDiskByID   string // legacy OSD-disk passthrough by-id path
	CephDiskGB     int    // virtual legacy OSD disk size GB (alternative to passthrough)
	CephStorage    string // storage pool for the virtual legacy OSD disk
	CPUAffinity    string // CPU core pinning
	NUMANode       int    // NUMA node (0 or 1)
	MacAddress     string // Static MAC address
}

// TalosNodeConfigs contains the embedded default Talos node configurations.
//
// Deprecated: use GetTalosNodeConfig so homeops.yaml overrides are included.
var TalosNodeConfigs = talosNodeConfigMap(homeopscfg.DefaultNodes())

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
	CephDiskByID   string // legacy OSD-disk passthrough by-id path
	CephDiskGB     int    // virtual legacy OSD disk size GB (alternative to passthrough)
	CephStorage    string // storage pool for the virtual legacy OSD disk
	CPUAffinity    string // CPU core pinning
	NUMANode       int    // NUMA node (0 or 1)
	MacAddress     string // Static MAC address
}

// FlatcarNodeConfigs contains the embedded default Flatcar node configurations.
// VMIDs, MACs, CPU affinity and NUMA nodes are intentionally identical to the
// Talos slots so the migration reuses the same Proxmox identities.
//
// Deprecated: use GetFlatcarNodeConfig so homeops.yaml overrides are included.
var FlatcarNodeConfigs = flatcarNodeConfigMap(homeopscfg.DefaultNodes())

func talosNodeConfigMap(nodes []homeopscfg.Node) map[string]TalosNodeConfig {
	out := make(map[string]TalosNodeConfig, len(nodes))
	for _, node := range nodes {
		out[node.Name] = talosNodeConfigFromNode(node)
	}
	return out
}

func flatcarNodeConfigMap(nodes []homeopscfg.Node) map[string]FlatcarNodeConfig {
	out := make(map[string]FlatcarNodeConfig, len(nodes))
	for _, node := range nodes {
		out[node.Name] = flatcarNodeConfigFromNode(node)
	}
	return out
}

func talosNodeConfigFromNode(node homeopscfg.Node) TalosNodeConfig {
	profile := node.VM.ForProvider("talos")
	return TalosNodeConfig{
		Name:           node.Name,
		VMID:           profile.VMID,
		BootStorage:    profile.BootStorage,
		OpenEBSStorage: profile.OpenEBSStorage,
		CephMode:       profile.Ceph.Mode,
		CephDiskByID:   profile.Ceph.DiskByID,
		CephDiskGB:     profile.Ceph.SizeGB,
		CephStorage:    profile.Ceph.Storage,
		CPUAffinity:    profile.CPUAffinity,
		NUMANode:       vmProfileNUMANode(profile),
		MacAddress:     profile.Mac,
	}
}

func flatcarNodeConfigFromNode(node homeopscfg.Node) FlatcarNodeConfig {
	profile := node.VM.ForProvider("flatcar")
	return FlatcarNodeConfig{
		Name:           node.Name,
		VMID:           profile.VMID,
		NodeIP:         node.IP,
		BootStorage:    profile.BootStorage,
		OpenEBSStorage: profile.OpenEBSStorage,
		CephMode:       profile.Ceph.Mode,
		CephDiskByID:   profile.Ceph.DiskByID,
		CephDiskGB:     profile.Ceph.SizeGB,
		CephStorage:    profile.Ceph.Storage,
		CPUAffinity:    profile.CPUAffinity,
		NUMANode:       vmProfileNUMANode(profile),
		MacAddress:     profile.Mac,
	}
}

func vmProfileNUMANode(profile homeopscfg.VMProfile) int {
	if profile.NUMANode == nil {
		return 0
	}
	return *profile.NUMANode
}

// GetFlatcarNodeConfig retrieves the effective config for a Flatcar node from
// homeops.yaml after embedded defaults and per-provider overlays are applied.
func GetFlatcarNodeConfig(name string) (FlatcarNodeConfig, bool) {
	node, found := homeopscfg.Get().ProvisioningNodeByName(name)
	if !found {
		return FlatcarNodeConfig{}, false
	}
	return flatcarNodeConfigFromNode(node), true
}

// GetDefaultVMConfig returns DefaultVMConfig with the hypervisors.proxmox.vm
// overrides from homeops.yaml applied (sizing, disk backends, network).
func GetDefaultVMConfig() VMConfig {
	cfg := DefaultVMConfig
	homeCfg := homeopscfg.Get()
	vm := homeCfg.Hypervisors.Proxmox.VM
	cfg.Memory = vm.MemoryMB
	cfg.Cores = vm.Cores
	cfg.BootDiskSize = vm.BootDiskGB
	cfg.OpenEBSSize = vm.OpenEBSDiskGB
	cfg.BootStorage = vm.BootStorage
	cfg.OpenEBSStorage = vm.OpenEBSStorage
	cfg.Node = homeCfg.ProxmoxNodeName()
	cfg.NetworkBridge = vm.NetworkBridge
	cfg.NetworkMTU = vm.NetworkMTU
	cfg.NetworkQueues = vm.NetworkQueues
	cfg.VLANID = vm.VLANID
	cfg.CPUType = vm.CPUType
	cfg.SCSIController = vm.SCSIController
	cfg.WatchdogModel = vm.WatchdogModel
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
	Sockets:        1,
	NUMAEnabled:    true,
	ISOStorage:     "local",
	IOThread:       true,
	Discard:        true,
	BIOS:           "ovmf",
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

// GetTalosNodeConfig retrieves the effective config for a Talos node from
// homeops.yaml after embedded defaults and per-provider overlays are applied.
func GetTalosNodeConfig(name string) (TalosNodeConfig, bool) {
	node, found := homeopscfg.Get().NodeByName(name)
	if !found {
		return TalosNodeConfig{}, false
	}
	return talosNodeConfigFromNode(node), true
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
		existingVMID, ok := common.SafeUint64ToInt(uint64(existingVM.VMID))
		if !ok {
			vm.logger.Warn("Skipping Proxmox VM '%s' with VMID %d: exceeds int range", existingVM.Name, existingVM.VMID)
			continue
		}
		if existingVMID == vmid {
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
	return vm.buildParameterizedVMOptions(config, vmOptionsProfile{
		useConfigCPU:     true,
		useAffinity:      true,
		useNUMA:          true,
		useUEFI:          true,
		useConfigSCSI:    true,
		bootDisk:         talosBootDiskOpts,
		includeOpenEBS:   true,
		includeLegacyOSD: true,
		includeISO:       true,
		bootOrder:        talosBootOrder,
		network: vmNetworkOptionsProfile{
			includeQueues:         true,
			usePositiveMTUAndVLAN: true,
		},
		includeWatchdog: true,
		useConfigAgent:  true,
		includeOnBoot:   true,
	})
}

// buildFlatcarVMOptions builds the VirtualMachineOption slice for a Flatcar/kubeadm
// node. It mirrors buildVMOptions (CPU/NUMA/UEFI/network/watchdog/agent) but:
//   - boots scsi0 from a pre-staged Flatcar disk image (import-from=<path>) or an
//     existing volume (ImageVolume) instead of installing from an ISO,
//   - keeps the Flatcar OpenEBS disk on its configured slot (scsi3 in the
//     provisioning path) and the optional legacy OSD disk on scsi2,
//   - injects the rendered Ignition via fw_cfg:
//     args = -fw_cfg name=opt/org.flatcar-linux/config,file=<IgnitionPath>
//
// This does not alter the Talos buildVMOptions path.
func (vm *VMManager) buildFlatcarVMOptions(config VMConfig) []proxmox.VirtualMachineOption {
	return vm.buildParameterizedVMOptions(config, vmOptionsProfile{
		useConfigCPU:     true,
		useAffinity:      true,
		useNUMA:          true,
		useUEFI:          true,
		useConfigSCSI:    true,
		bootDisk:         (*VMManager).flatcarBootDiskOpts,
		includeOpenEBS:   true,
		includeLegacyOSD: true,
		bootOrder:        flatcarBootOrder,
		network: vmNetworkOptionsProfile{
			includeQueues:         true,
			usePositiveMTUAndVLAN: true,
		},
		includeWatchdog: true,
		useConfigAgent:  true,
		includeOnBoot:   true,
		afterOnBoot:     addFlatcarIgnitionArgs,
	})
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
			vmID, ok := common.SafeUint64ToInt(uint64(vmItem.VMID))
			if !ok {
				return nil, fmt.Errorf("proxmox VM %q has VMID %d outside supported int range", name, vmItem.VMID)
			}
			return vm.client.GetVM(vmID)
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
	logger := common.NewColorLogger()
	summaries := make([]provider.VMSummary, 0, len(vms))
	for _, vmItem := range vms {
		memoryMB, ok := common.SafeUint64ToInt(vmItem.MaxMem / (1024 * 1024))
		if !ok {
			logger.Warn("Proxmox VM '%s' memory %d bytes exceeds int range when converted to MiB; clamping to %d", vmItem.Name, vmItem.MaxMem, memoryMB)
		}
		summaries = append(summaries, provider.VMSummary{
			Name:     vmItem.Name,
			ID:       fmt.Sprintf("%d", vmItem.VMID),
			Status:   vmItem.Status,
			MemoryMB: memoryMB,
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
