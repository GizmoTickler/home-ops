package vsphere

import (
	"fmt"
	"strconv"
	"strings"

	homeopscfg "homeops-cli/internal/config"
)

const (
	DefaultISODatastore = homeopscfg.DefaultVSphereISODatastore
	DefaultISOFilename  = homeopscfg.DefaultVSphereISOFile
)

// VMConfig represents the configuration for a vSphere VM
type VMConfig struct {
	// Basic VM configuration
	Name        string
	Memory      int // Memory in MB
	VCPUs       int // Number of vCPUs
	DiskSize    int // Boot disk size in GB (default: 250GB) - contains TalosOS
	OpenEBSSize int // OpenEBS disk size in GB (default: 800GB) - virtual disk on truenas-iscsi for local storage class

	// Multi-datastore configuration (for k8s nodes)
	BootDatastore    string // Datastore for boot/OS disk (e.g., "local-nvme1")
	OpenEBSDatastore string // Datastore for OpenEBS disk (e.g., "truenas-iscsi")
	Datastore        string // Legacy: single datastore for all disks (deprecated, use Boot/Ceph datastores)
	Network          string // Network name (e.g., "vl999") - used only for vmxnet3
	ISO              string // ISO path on datastore (e.g., "[datastore1] vmware-amd64.iso")
	ISODatastore     string // Datastore where ISO is stored (e.g., "datastore1")
	MacAddress       string // Static MAC address for network (SR-IOV or vmxnet3)

	// RDM (Raw Device Mapping) configuration for Rook/Ceph distributed storage
	// These are physical Intel SSDs passed through as pRDM for Ceph OSD
	RDMPath string // Path to RDM descriptor (e.g., "[datastore1] rdm/intel-ssd-1.vmdk")

	// SR-IOV PCI Passthrough configuration
	EnableSRIOV  bool   // Use SR-IOV passthrough instead of vmxnet3
	PCIDevice    string // PCI device address for SR-IOV (e.g., "0000:04:00.0")
	PCIDeviceHex string // PCI device in hex format for VMX (e.g., "00000:004:00.0")

	// CPU and Memory optimization
	CPUAffinity    string // CPU affinity mask (e.g., "0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23")
	MemoryPinned   bool   // Pin memory reservation (sched.mem.pin = TRUE)
	CoresPerSocket int    // Cores per socket (default: 1)

	// Deployment options
	PowerOn              bool // Power on VM after creation
	EnableIOMMU          bool // Enable IOMMU/VT-d for VM
	ExposeCounters       bool // Expose CPU performance counters
	ThinProvisioned      bool // Use thin provisioned disks (default: true)
	EnablePrecisionClock bool // Add precision clock device (default: true)
	EnableWatchdog       bool // Add watchdog timer device (default: true)

	// Talos specific
	SchematicID  string // Optional: Talos factory schematic ID
	TalosVersion string // Optional: Talos version

	// Flatcar specific (Ignition via VMware guestinfo, clone-from-OVA-template).
	// Flatcar does not boot an install ISO: the official Flatcar OVA is imported
	// once as a template, each node is a clone of it, and the per-node Ignition is
	// injected through guestinfo ExtraConfig (read by Ignition's VMware provider on
	// first boot). See buildExtraConfig / buildFlatcarCloneSpec.
	IgnitionData string // base64-encoded Ignition JSON (guestinfo.ignition.config.data)
	TemplateName string // name of the imported Flatcar OVA template to clone
}

// VMDeploymentConfig represents configuration for batch VM deployment
type VMDeploymentConfig struct {
	// Connection details
	Host     string
	Username string
	Password string
	Insecure bool

	// Default VM specs
	DefaultMemory      int
	DefaultVCPUs       int
	DefaultDiskSize    int
	DefaultOpenEBSSize int

	// vSphere defaults
	DefaultDatastore string
	DefaultNetwork   string
	ISODatastore     string // Datastore where ISOs are stored
	ISOPath          string // Path to Talos ISO

	// VMs to deploy
	VMs []VMConfig
}

// K8sNodeConfig holds the predefined configuration for each k8s node
type K8sNodeConfig struct {
	RDMPath       string // Path to RDM descriptor
	PCIDevice     string // PCI device for SR-IOV (standard format)
	PCIDeviceHex  string // PCI device for VMX (decimal format)
	MacAddress    string // Static MAC address
	CPUAffinity   string // CPU affinity mask
	BootDatastore string // Datastore for boot disk
}

// GetK8sNodeConfig returns the predefined configuration for a k8s node
func GetK8sNodeConfig(name string) (K8sNodeConfig, bool) {
	if node, found := defaultVSphereK8sNode(name); found {
		return k8sNodeConfigFromNode(node), true
	}
	return K8sNodeConfig{}, false
}

// GetDefaultVMConfig returns a VM configuration with default Talos specs,
// with hypervisors.vsphere.vm overrides from homeops.yaml applied (sizing,
// datastores, network).
func GetDefaultVMConfig(name string) VMConfig {
	cfg := defaultVMConfig(name)
	vm := homeopscfg.Get().Hypervisors.VSphere.VM
	cfg.Memory = vm.MemoryMB
	cfg.VCPUs = vm.Cores
	cfg.DiskSize = vm.BootDiskGB
	cfg.OpenEBSSize = vm.OpenEBSDiskGB
	cfg.BootDatastore = vm.BootStorage
	cfg.OpenEBSDatastore = vm.OpenEBSStorage
	cfg.Network = vm.NetworkBridge
	cfg.CoresPerSocket = vm.CoresPerSocket
	cfg.ISODatastore = homeopscfg.Get().Hypervisors.VSphere.ISODatastore
	return cfg
}

func defaultVMConfig(name string) VMConfig {
	return VMConfig{
		Name:                 name,
		PowerOn:              true, // Power on by default
		EnableIOMMU:          true, // Enable IOMMU/VT-d
		ExposeCounters:       true, // Expose CPU performance counters
		ThinProvisioned:      true, // Use thin provisioned disks
		EnablePrecisionClock: true, // Add precision clock device
		EnableWatchdog:       true, // Add watchdog timer device
		EnableSRIOV:          true, // Use SR-IOV by default for k8s nodes
		MemoryPinned:         true, // Pin memory reservation
	}
}

// GetK8sVMConfig returns a fully configured VMConfig for a k8s node
// This applies all the predefined settings that match the existing VMs
func GetK8sVMConfig(name string) VMConfig {
	config := GetDefaultVMConfig(name)

	// Apply k8s node specific configuration if this is a known k8s node
	if nodeConfig, exists := GetK8sNodeConfig(name); exists {
		config.RDMPath = nodeConfig.RDMPath
		config.PCIDevice = nodeConfig.PCIDevice
		config.PCIDeviceHex = nodeConfig.PCIDeviceHex
		config.MacAddress = nodeConfig.MacAddress
		config.CPUAffinity = nodeConfig.CPUAffinity
		config.BootDatastore = nodeConfig.BootDatastore
	}

	// Per-node VM profile from homeops.yaml wins over the predefined map.
	if node, found := homeopscfg.Get().NodeByName(name); found {
		profile := node.VM.ForProvider("vsphere")
		if profile.Mac != "" {
			config.MacAddress = profile.Mac
		}
		if profile.BootStorage != "" {
			config.BootDatastore = profile.BootStorage
		}
		if profile.OpenEBSStorage != "" {
			config.OpenEBSDatastore = profile.OpenEBSStorage
		}
		if profile.CPUAffinity != "" {
			config.CPUAffinity = profile.CPUAffinity
		}
		if profile.RDMPath != "" {
			config.RDMPath = profile.RDMPath
		}
		if profile.PCIDevice != "" {
			config.PCIDevice = profile.PCIDevice
			config.PCIDeviceHex = pciDeviceHex(profile.PCIDevice)
		}
	}

	// Set ISO path
	config.ISO = BuildISOPath(config.ISODatastore, homeopscfg.Get().Hypervisors.VSphere.ISOFile)

	return config
}

// DefaultISOPath returns the standard Talos ISO path used for vSphere deployments.
func DefaultISOPath() string {
	return homeopscfg.Get().VSphereISOPath()
}

// BuildISOPath constructs the full ISO path for vSphere
func BuildISOPath(isoDatastore, isoFilename string) string {
	// Format: [datastore-name] path/to/file.iso
	// For NFS datastore with ISO at root level
	return fmt.Sprintf("[%s] %s", isoDatastore, isoFilename)
}

func defaultVSphereK8sNode(name string) (homeopscfg.Node, bool) {
	for _, node := range homeopscfg.DefaultNodes() {
		if node.Name == name {
			return node, true
		}
	}
	return homeopscfg.DefaultVSphereK8sNode(name)
}

func k8sNodeConfigFromNode(node homeopscfg.Node) K8sNodeConfig {
	profile := node.VM.ForProvider("vsphere")
	return K8sNodeConfig{
		RDMPath:       profile.RDMPath,
		PCIDevice:     profile.PCIDevice,
		PCIDeviceHex:  pciDeviceHex(profile.PCIDevice),
		MacAddress:    profile.Mac,
		CPUAffinity:   profile.CPUAffinity,
		BootDatastore: profile.BootStorage,
	}
}

func pciDeviceHex(device string) string {
	parts := strings.Split(device, ":")
	if len(parts) != 3 {
		return device
	}
	domain, err := strconv.ParseInt(parts[0], 16, 64)
	if err != nil {
		return device
	}
	bus, err := strconv.ParseInt(parts[1], 16, 64)
	if err != nil {
		return device
	}
	slotFunc := strings.Split(parts[2], ".")
	if len(slotFunc) != 2 {
		return device
	}
	slot, err := strconv.ParseInt(slotFunc[0], 16, 64)
	if err != nil {
		return device
	}
	return fmt.Sprintf("%05d:%03d:%02d.%s", domain, bus, slot, slotFunc[1])
}

// ValidateConfig validates a VM configuration
func (c *VMConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("VM name is required")
	}
	if c.Memory <= 0 {
		return fmt.Errorf("memory must be greater than 0")
	}
	if c.VCPUs <= 0 {
		return fmt.Errorf("vCPUs must be greater than 0")
	}
	if c.DiskSize <= 0 {
		return fmt.Errorf("disk size must be greater than 0")
	}
	if c.Datastore == "" {
		return fmt.Errorf("datastore is required")
	}
	if c.Network == "" {
		return fmt.Errorf("network is required")
	}
	if c.ISO == "" {
		return fmt.Errorf("ISO path is required")
	}
	return nil
}
