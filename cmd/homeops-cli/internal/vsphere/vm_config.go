package vsphere

import "fmt"

// VMConfig represents the configuration for a vSphere VM
type VMConfig struct {
	// Basic VM configuration
	Name        string
	Memory      int // Memory in MB
	VCPUs       int // Number of vCPUs
	DiskSize    int // Boot disk size in GB (default: 250GB) - contains TalosOS
	OpenEBSSize int // OpenEBS disk size in GB (default: 800GB) - virtual disk on truenas-iscsi for local storage class

	// Multi-datastore configuration (for k8s nodes)
	BootDatastore   string // Datastore for boot/OS disk (e.g., "local-nvme1")
	OpenEBSDatastore string // Datastore for OpenEBS disk (e.g., "truenas-iscsi")
	Datastore       string // Legacy: single datastore for all disks (deprecated, use Boot/Ceph datastores)
	Network         string // Network name (e.g., "vl999") - used only for vmxnet3
	ISO             string // ISO path on datastore (e.g., "[datastore1] vmware-amd64.iso")
	ISODatastore    string // Datastore where ISO is stored (e.g., "datastore1")
	MacAddress      string // Static MAC address for network (SR-IOV or vmxnet3)

	// RDM (Raw Device Mapping) configuration for Rook/Ceph distributed storage
	// These are physical Intel SSDs passed through as pRDM for Ceph OSD
	RDMPath string // Path to RDM descriptor (e.g., "[datastore1] rdm/intel-ssd-1.vmdk")

	// SR-IOV PCI Passthrough configuration
	EnableSRIOV  bool   // Use SR-IOV passthrough instead of vmxnet3
	PCIDevice    string // PCI device address for SR-IOV (e.g., "0000:04:00.0")
	PCIDeviceHex string // PCI device in hex format for VMX (e.g., "00000:004:00.0")

	// CPU and Memory optimization
	CPUAffinity   string // CPU affinity mask (e.g., "0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23")
	MemoryPinned  bool   // Pin memory reservation (sched.mem.pin = TRUE)
	CoresPerSocket int   // Cores per socket (default: 1)

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
	RDMPath      string // Path to RDM descriptor
	PCIDevice    string // PCI device for SR-IOV (standard format)
	PCIDeviceHex string // PCI device for VMX (decimal format)
	MacAddress   string // Static MAC address
	CPUAffinity  string // CPU affinity mask
	BootDatastore string // Datastore for boot disk
}

// k8sNodeConfigs contains the predefined configurations for k8s nodes
// CPU Affinity Layout (2×E5-2697 v4: 2 sockets × 16 cores × 2 HT = 64 CPUs):
//   Socket 0 (NUMA Node 0): Physical cores 0-15 → CPUs 0-15, HT siblings → CPUs 16-31
//   Socket 1 (NUMA Node 1): Physical cores 0-15 → CPUs 32-47, HT siblings → CPUs 48-63
// Each VM gets 8 physical cores + 8 HT siblings = 16 vCPUs with no overlap
var k8sNodeConfigs = map[string]K8sNodeConfig{
	"k8s-0": {
		RDMPath:       "[datastore1] rdm/intel-ssd-1.vmdk",
		PCIDevice:     "0000:04:00.0",
		PCIDeviceHex:  "00000:004:00.0",
		MacAddress:    "00:a0:98:28:c8:83",
		CPUAffinity:   "0,1,2,3,4,5,6,7,16,17,18,19,20,21,22,23", // Socket 0, cores 0-7 + HT
		BootDatastore: "local-nvme1",
	},
	"k8s-1": {
		RDMPath:       "[datastore1] rdm/intel-ssd-2.vmdk",
		PCIDevice:     "0000:0b:00.0",
		PCIDeviceHex:  "00000:011:00.0", // 0x0b = 11 decimal
		MacAddress:    "00:a0:98:1a:f3:72",
		CPUAffinity:   "32,33,34,35,36,37,38,39,48,49,50,51,52,53,54,55", // Socket 1, cores 0-7 + HT
		BootDatastore: "local-nvme1",
	},
	"k8s-2": {
		RDMPath:       "[datastore1] rdm/intel-ssd-3.vmdk",
		PCIDevice:     "0000:04:00.1",
		PCIDeviceHex:  "00000:004:00.1",
		MacAddress:    "00:a0:98:3e:6c:22",
		CPUAffinity:   "8,9,10,11,12,13,14,15,24,25,26,27,28,29,30,31", // Socket 0, cores 8-15 + HT
		BootDatastore: "local-nvme2",
	},
	"k8s-3": {
		RDMPath:       "[datastore1] rdm/intel-ssd-4.vmdk",
		PCIDevice:     "0000:0b:00.1",    // Second port on second X540
		PCIDeviceHex:  "00000:011:00.1",
		MacAddress:    "",                // To be assigned
		CPUAffinity:   "40,41,42,43,44,45,46,47,56,57,58,59,60,61,62,63", // Socket 1, cores 8-15 + HT
		BootDatastore: "local-nvme2",
	},
}

// GetK8sNodeConfig returns the predefined configuration for a k8s node
func GetK8sNodeConfig(name string) (K8sNodeConfig, bool) {
	config, exists := k8sNodeConfigs[name]
	return config, exists
}

// GetDefaultVMConfig returns a VM configuration with default Talos specs
func GetDefaultVMConfig(name string) VMConfig {
	return VMConfig{
		Name:                 name,
		Memory:               64 * 1024, // 64GB (matches actual VMs)
		VCPUs:                16,
		DiskSize:             250,             // 250GB boot (TalosOS)
		OpenEBSSize:          800,             // 800GB OpenEBS (matches actual VMs)
		BootDatastore:        "local-nvme1",   // Boot disk on local NVMe
		OpenEBSDatastore:     "truenas-iscsi", // OpenEBS disk on TrueNAS iSCSI
		ISODatastore:         "datastore1",    // ISO stored on datastore1
		Network:              "vl999",
		PowerOn:              true,  // Power on by default
		EnableIOMMU:          true,  // Enable IOMMU/VT-d
		ExposeCounters:       true,  // Expose CPU performance counters
		ThinProvisioned:      true,  // Use thin provisioned disks
		EnablePrecisionClock: true,  // Add precision clock device
		EnableWatchdog:       true,  // Add watchdog timer device
		EnableSRIOV:          true,  // Use SR-IOV by default for k8s nodes
		MemoryPinned:         true,  // Pin memory reservation
		CoresPerSocket:       1,     // 1 core per socket (matches actual VMs)
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

	// Set ISO path
	config.ISO = fmt.Sprintf("[%s] vmware-amd64.iso", config.ISODatastore)

	return config
}

// BuildISOPath constructs the full ISO path for vSphere
func BuildISOPath(isoDatastore, isoFilename string) string {
	// Format: [datastore-name] path/to/file.iso
	// For NFS datastore with ISO at root level
	return fmt.Sprintf("[%s] %s", isoDatastore, isoFilename)
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
