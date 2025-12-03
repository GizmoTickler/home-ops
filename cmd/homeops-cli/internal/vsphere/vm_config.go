package vsphere

import "fmt"

// VMConfig represents the configuration for a vSphere VM
type VMConfig struct {
	// Basic VM configuration
	Name        string
	Memory      int // Memory in MB
	VCPUs       int // Number of vCPUs
	DiskSize    int // Boot disk size in GB (default: 250GB) - contains TalosOS
	OpenEBSSize int // OpenEBS disk size in GB (default: 1000GB) - for local storage

	// vSphere specific configuration
	Datastore  string // Datastore name (e.g., "truenas-iscsi")
	Network    string // Network name (e.g., "vl999")
	ISO        string // ISO path on datastore (e.g., "[truenas-iscsi] vmware-amd64.iso")
	MacAddress string // Optional MAC address

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

// GetDefaultVMConfig returns a VM configuration with default Talos specs
func GetDefaultVMConfig(name string) VMConfig {
	return VMConfig{
		Name:                 name,
		Memory:               48 * 1024, // 48GB
		VCPUs:                16,
		DiskSize:             250,             // 250GB boot (TalosOS)
		OpenEBSSize:          1000,            // 1TB OpenEBS local storage
		Datastore:            "truenas-iscsi", // Match the actual datastore name
		Network:              "vl999",
		PowerOn:              true, // Power on by default with auto unregister/re-register on failure
		EnableIOMMU:          true, // Enable IOMMU/VT-d by default for Talos VMs
		ExposeCounters:       true, // Expose CPU performance counters
		ThinProvisioned:      true, // Use thin provisioned disks (matches manual VM)
		EnablePrecisionClock: true, // Add precision clock device
		EnableWatchdog:       true, // Add watchdog timer device
	}
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
