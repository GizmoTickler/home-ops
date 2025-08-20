package truenas

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"homeops-cli/internal/common"
)

// VMConfig represents the configuration for VM deployment
type VMConfig struct {
	Name          string
	Memory        int
	VCPUs         int
	DiskSize      int // Boot disk size in GB
	OpenEBSSize   int // OpenEBS disk size in GB
	RookSize      int // Rook disk size in GB
	TrueNASHost   string
	TrueNASAPIKey string
	TrueNASPort   int
	NoSSL         bool
	TalosISO      string
	NetworkBridge string
	StoragePool   string
	MacAddress    string
	BootZVol      string
	OpenEBSZVol   string
	RookZVol      string
	SkipZVolCreate bool
	SpicePassword string
	UseSpice      bool
	// Schematic configuration fields
	SchematicID   string // Optional: Talos factory schematic ID for custom ISOs
	TalosVersion  string // Optional: Specific Talos version for custom ISOs
	CustomISO     bool   // Flag indicating if using a custom generated ISO
}

// VMManager handles VM operations
type VMManager struct {
	client *WorkingClient
	logger *common.ColorLogger
}

// NewVMManager creates a new VM manager
func NewVMManager(host, apiKey string, port int, useSSL bool) *VMManager {
	client := NewWorkingClient(host, apiKey, port, useSSL)
	return &VMManager{
		client: client,
		logger: common.NewColorLogger(),
	}
}

// Connect establishes connection to TrueNAS
func (vm *VMManager) Connect() error {
	return vm.client.Connect()
}

// Close closes the connection
func (vm *VMManager) Close() error {
	return vm.client.Close()
}

// DeployVM deploys a new VM with the specified configuration
func (vm *VMManager) DeployVM(config VMConfig) error {
	vm.logger.Info("Starting VM deployment: %s", config.Name)

	// Check if VM already exists
	allVMs, err := vm.client.QueryVMs(nil)
	if err != nil {
		return fmt.Errorf("failed to query existing VMs: %w", err)
	}

	for _, existingVM := range allVMs {
		if existingVM.Name == config.Name {
			return fmt.Errorf("VM with name '%s' already exists", config.Name)
		}
	}

	// Create ZVols if not skipping
	if !config.SkipZVolCreate {
		if err := vm.createZVols(config); err != nil {
			return fmt.Errorf("failed to create ZVols: %w", err)
		}
	} else {
		if err := vm.verifyZVols(config); err != nil {
			return fmt.Errorf("failed to verify ZVols: %w", err)
		}
	}

	// Build VM configuration
	vmConfig := vm.buildVMConfig(config)

	// Create the VM
	createdVM, err := vm.client.CreateVM(vmConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	vm.logger.Info("VM created with ID: %d", createdVM.ID)

	// Create VM devices
	if err := vm.createVMDevices(createdVM.ID, config); err != nil {
		return fmt.Errorf("failed to create VM devices: %w", err)
	}

	vm.logger.Success("Successfully deployed VM: %s", config.Name)
	return nil
}

// ListVMs lists all VMs
func (vm *VMManager) ListVMs() error {
	vms, err := vm.client.QueryVMs(nil)
	if err != nil {
		return fmt.Errorf("failed to query VMs: %w", err)
	}

	if len(vms) == 0 {
		fmt.Println("No virtual machines found.")
		return nil
	}

	fmt.Printf("%-20s %-5s %-10s %-8s %-6s %-10s\n", "Name", "ID", "Status", "Memory", "vCPUs", "Autostart")
	fmt.Println(strings.Repeat("-", 70))

	for _, vmItem := range vms {
		status := "unknown"
		if vmItem.Status != nil {
			if state, ok := vmItem.Status["state"]; ok {
				status = fmt.Sprintf("%v", state)
			}
		}

		autostart := "No"
		if vmItem.Autostart {
			autostart = "Yes"
		}

		fmt.Printf("%-20s %-5d %-10s %-8d %-6d %-10s\n",
			vmItem.Name, vmItem.ID, status, vmItem.Memory, vmItem.VCPUs, autostart)
	}

	return nil
}

// StartVM starts a VM by name
func (vm *VMManager) StartVM(name string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}

	vm.logger.Info("Starting VM: %s (ID: %d)", vmItem.Name, vmItem.ID)

	if err := vm.client.StartVM(vmItem.ID); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	vm.logger.Success("VM %s started successfully", name)
	return nil
}

// StopVM stops a VM by name
func (vm *VMManager) StopVM(name string, force bool) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}

	action := "Stopping"
	if force {
		action = "Force stopping"
	}
	vm.logger.Info("%s VM: %s (ID: %d)", action, vmItem.Name, vmItem.ID)

	if err := vm.client.StopVM(vmItem.ID); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	vm.logger.Success("VM %s stopped successfully", name)
	return nil
}

// DeleteVM deletes a VM by name
func (vm *VMManager) DeleteVM(name string, deleteZVol bool, storagePool string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}

	vm.logger.Info("Deleting VM: %s (ID: %d)", vmItem.Name, vmItem.ID)

	// Discover ZVols if we need to delete them
	var zvolPaths []string
	if deleteZVol {
		zvolPaths, err = vm.discoverVMZVols(vmItem)
		if err != nil {
			vm.logger.Warn("Failed to discover ZVols for VM %s: %v", name, err)
		}
	}

	// Delete the VM first
	vm.logger.Info("Calling TrueNAS API to delete VM ID: %d", vmItem.ID)
	if err := vm.client.DeleteVM(vmItem.ID); err != nil {
		vm.logger.Error("VM deletion API call failed: %v", err)
		return fmt.Errorf("failed to delete VM: %w", err)
	}
	vm.logger.Info("VM deletion API call completed successfully")

	// Verify VM is actually deleted by checking if it still exists
	vm.logger.Info("Verifying VM deletion...")
	_, verifyErr := vm.getVMByName(name)
	if verifyErr == nil {
		vm.logger.Warn("VM still exists after deletion API call - this may indicate a TrueNAS API issue")
	} else {
		vm.logger.Info("VM deletion verified - VM no longer exists")
	}

	// Delete ZVols if requested
	if deleteZVol && len(zvolPaths) > 0 {
		if err := vm.deleteZVolsByPaths(zvolPaths, name); err != nil {
			vm.logger.Warn("Failed to delete some ZVols: %v", err)
		}
	}

	vm.logger.Success("VM %s deleted successfully", name)
	return nil
}

// GetVMInfo displays detailed information about a VM
func (vm *VMManager) GetVMInfo(name string) error {
	vmItem, err := vm.getVMByName(name)
	if err != nil {
		return err
	}

	fmt.Printf("VM Information for: %s\n", vmItem.Name)
	fmt.Printf("ID: %d\n", vmItem.ID)
	fmt.Printf("Description: %s\n", vmItem.Description)
	fmt.Printf("Memory: %d MB\n", vmItem.Memory)
	fmt.Printf("vCPUs: %d\n", vmItem.VCPUs)
	fmt.Printf("Bootloader: %s\n", vmItem.Bootloader)
	fmt.Printf("Autostart: %t\n", vmItem.Autostart)

	if vmItem.Status != nil {
		fmt.Printf("Status: %v\n", vmItem.Status)
	}

	if len(vmItem.Devices) > 0 {
		fmt.Printf("\nDevices (%d):\n", len(vmItem.Devices))
		for i, device := range vmItem.Devices {
			fmt.Printf("  Device %d: %v\n", i+1, device)
		}
	}

	return nil
}

// Helper methods

func (vm *VMManager) getVMByName(name string) (*VM, error) {
	vms, err := vm.client.QueryVMs(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query VMs: %w", err)
	}

	for _, vmItem := range vms {
		if vmItem.Name == name {
			return &vmItem, nil
		}
	}

	return nil, fmt.Errorf("VM '%s' not found", name)
}

func (vm *VMManager) createZVols(config VMConfig) error {
	vm.logger.Info("Creating ZVols...")

	zvolPaths := vm.getZVolPaths(config)

	// Create boot ZVol
	if err := vm.createSingleZVol(zvolPaths["boot"], config.DiskSize, "boot"); err != nil {
		return err
	}

	// Create OpenEBS ZVol
	if err := vm.createSingleZVol(zvolPaths["openebs"], config.OpenEBSSize, "OpenEBS"); err != nil {
		return err
	}

	// Create Rook ZVol
	if err := vm.createSingleZVol(zvolPaths["rook"], config.RookSize, "Rook"); err != nil {
		return err
	}

	return nil
}

func (vm *VMManager) verifyZVols(config VMConfig) error {
	vm.logger.Info("Verifying ZVols exist...")

	zvolPaths := vm.getZVolPaths(config)

	for zvolType, zvolPath := range zvolPaths {
		datasets, err := vm.client.QueryDatasets([][]interface{}{{"name", "=", zvolPath}})
		if err != nil {
			return fmt.Errorf("failed to query %s ZVol %s: %w", zvolType, zvolPath, err)
		}

		if len(datasets) == 0 {
			return fmt.Errorf("%s ZVol %s does not exist", zvolType, zvolPath)
		}

		vm.logger.Info("✓ %s ZVol verified: %s", zvolType, zvolPath)
	}

	return nil
}

func (vm *VMManager) getZVolPaths(config VMConfig) map[string]string {
	paths := make(map[string]string)

	if config.BootZVol != "" {
		paths["boot"] = config.BootZVol
	} else {
		// Check if StoragePool already contains /VM to avoid duplication
		if strings.HasSuffix(config.StoragePool, "/VM") {
			paths["boot"] = fmt.Sprintf("%s/%s-boot", config.StoragePool, config.Name)
		} else {
			paths["boot"] = fmt.Sprintf("%s/VM/%s-boot", config.StoragePool, config.Name)
		}
	}

	if config.OpenEBSZVol != "" {
		paths["openebs"] = config.OpenEBSZVol
	} else {
		// Check if StoragePool already contains /VM to avoid duplication
		if strings.HasSuffix(config.StoragePool, "/VM") {
			paths["openebs"] = fmt.Sprintf("%s/%s-ebs", config.StoragePool, config.Name)
		} else {
			paths["openebs"] = fmt.Sprintf("%s/VM/%s-ebs", config.StoragePool, config.Name)
		}
	}

	if config.RookZVol != "" {
		paths["rook"] = config.RookZVol
	} else {
		// Check if StoragePool already contains /VM to avoid duplication
		if strings.HasSuffix(config.StoragePool, "/VM") {
			paths["rook"] = fmt.Sprintf("%s/%s-rook", config.StoragePool, config.Name)
		} else {
			paths["rook"] = fmt.Sprintf("%s/VM/%s-rook", config.StoragePool, config.Name)
		}
	}

	return paths
}

func (vm *VMManager) createSingleZVol(zvolPath string, sizeGB int, zvolType string) error {
	vm.logger.Info("Creating thin provisioned %s ZVol: %s (%dGB)", zvolType, zvolPath, sizeGB)

	// Check if ZVol already exists
	allDatasets, err := vm.client.QueryDatasets(nil)
	if err != nil {
		return fmt.Errorf("failed to query existing datasets: %w", err)
	}

	// Check if ZVol already exists
	for _, dataset := range allDatasets {
		if dataset.Name == zvolPath {
			vm.logger.Info("✓ %s ZVol already exists: %s", zvolType, zvolPath)
			return nil
		}
	}

	// Parse the ZVol path to determine parent datasets
	parts := strings.Split(zvolPath, "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid ZVol path: %s (must be in format pool/dataset/name)", zvolPath)
	}

	// Create parent datasets if they don't exist
	for i := 1; i < len(parts); i++ {
		parentPath := strings.Join(parts[:i+1], "/")

		// Skip if this is the final ZVol name
		if i == len(parts)-1 {
			break
		}

		// Check if parent dataset exists
		parentExists := false
		for _, dataset := range allDatasets {
			if dataset.Name == parentPath {
				parentExists = true
				break
			}
		}

		if !parentExists {
			vm.logger.Info("Creating parent dataset: %s", parentPath)
			// Create parent dataset using raw API call for compatibility
			parentConfig := map[string]interface{}{
				"name": parentPath,
				"type": "FILESYSTEM",
			}
			_, err := vm.client.Call("pool.dataset.create", []interface{}{parentConfig}, 60)
			if err != nil {
				return fmt.Errorf("failed to create parent dataset %s: %w", parentPath, err)
			}
		}
	}

	// Use the specified size
	volsize := int64(sizeGB) * 1024 * 1024 * 1024 // Convert GB to bytes

	vm.logger.Info("Creating thin provisioned %s ZVol: %s (%.1fGB)", zvolType, zvolPath, float64(volsize)/(1024*1024*1024))

	// Create thin provisioned ZVol with basic parameters - matching the working script
	zvolConfig := map[string]interface{}{
		"name":    zvolPath,
		"type":    "VOLUME",
		"volsize": volsize,
		"sparse":  true,   // Enable thin provisioning - this is the critical missing piece!
	}

	_, err = vm.client.Call("pool.dataset.create", []interface{}{zvolConfig}, 60)
	if err != nil {
		return fmt.Errorf("failed to create thin provisioned ZVol: %w", err)
	}

	vm.logger.Success("✓ Created thin provisioned %s ZVol: %s (%dGB)", zvolType, zvolPath, sizeGB)
	return nil
}

func (vm *VMManager) buildVMConfig(config VMConfig) map[string]interface{} {
	// Build VM configuration based on real TrueNAS API structure
	vmConfig := map[string]interface{}{
		"name":                           config.Name,
		"description":                    fmt.Sprintf("Talos Linux VM - %s", config.Name),
		"vcpus":                          config.VCPUs,
		"cores":                          1,
		"threads":                        1,
		"memory":                         config.Memory,
		"bootloader":                     "UEFI",
		"bootloader_ovmf":               "OVMF_CODE.fd",
		"autostart":                      false,
		"time":                           "LOCAL",
		"shutdown_timeout":               90,
		"cpu_mode":                       "HOST-PASSTHROUGH",
		"cpu_model":                      nil,
		"cpuset":                         "",
		"nodeset":                        "",
		"enable_cpu_topology_extension":  false,
		"pin_vcpus":                      false,
		"suspend_on_snapshot":            false,
		"trusted_platform_module":        false,
		"min_memory":                     nil,
		"hyperv_enlightenments":          false,
		"command_line_args":              "",
		"arch_type":                      nil,
	}

	return vmConfig
}

func (vm *VMManager) generateRandomMAC() string {
	// Generate a random MAC address with VMware OUI prefix
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("00:0c:29:%02x:%02x:%02x",
		r.Intn(256), r.Intn(256), r.Intn(256))
}

func (vm *VMManager) generateRandomSerial() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	serial := make([]byte, 8)
	for i := range serial {
		serial[i] = charset[r.Intn(len(charset))]
	}
	return string(serial)
}

func (vm *VMManager) createVMDevices(vmID int, config VMConfig) error {
	vm.logger.Info("Creating VM devices...")

	// Generate MAC address if not provided
	macAddress := config.MacAddress
	if macAddress == "" {
		macAddress = vm.generateRandomMAC()
	}

	// Create CD-ROM device first (order 1000) - matching working script structure
	// Use the TalosISO path from config to support both default and custom ISOs
	isoPath := config.TalosISO
	if isoPath == "" {
		isoPath = "/mnt/flashstor/ISO/metal-amd64.iso" // Fallback to default
	}
	
	cdromDevice := map[string]interface{}{
		"vm": vmID,
		"attributes": map[string]interface{}{
			"dtype": "CDROM",
			"path":  isoPath,
		},
		"order": 1000,
	}

	if _, err := vm.client.Call("vm.device.create", []interface{}{cdromDevice}, 30); err != nil {
		return fmt.Errorf("failed to create CD-ROM device: %w", err)
	}
	vm.logger.Info("Created CD-ROM device with ISO: %s", isoPath)

	// Create network device (order 1002) - matching working script structure
	nicDevice := map[string]interface{}{
		"vm": vmID,
		"attributes": map[string]interface{}{
			"dtype":                    "NIC",
			"type":                     "VIRTIO",
			"mac":                      macAddress,
			"nic_attach":               config.NetworkBridge,
			"trust_guest_rx_filters":   false,
		},
		"order": 1002,
	}

	if _, err := vm.client.Call("vm.device.create", []interface{}{nicDevice}, 30); err != nil {
		return fmt.Errorf("failed to create NIC device: %w", err)
	}
	vm.logger.Info("Created NIC device with MAC %s on bridge %s", macAddress, config.NetworkBridge)

	// Create disk devices with correct order matching working script
	zvolPaths := vm.getZVolPaths(config)

	// Boot disk (order 1001) - matching working script structure
	if bootPath, exists := zvolPaths["boot"]; exists && bootPath != "" {
		bootDevice := map[string]interface{}{
			"vm": vmID,
			"attributes": map[string]interface{}{
				"dtype":               "DISK",
				"type":                "VIRTIO",
				"path":                fmt.Sprintf("/dev/zvol/%s", bootPath),
				"iotype":              "THREADS",
				"create_zvol":         false,
				"logical_sectorsize":  nil,
				"physical_sectorsize": nil,
				"serial":              vm.generateRandomSerial(),
				"zvol_name":           nil,
				"zvol_volsize":        nil,
			},
			"order": 1001,
		}

		if _, err := vm.client.Call("vm.device.create", []interface{}{bootDevice}, 30); err != nil {
			return fmt.Errorf("failed to create boot disk device: %w", err)
		}
		vm.logger.Info("Created boot disk device: /dev/zvol/%s", bootPath)
	}

	// OpenEBS disk (order 1004) - matching working script structure
	if openebsPath, exists := zvolPaths["openebs"]; exists && openebsPath != "" {
		openebsDevice := map[string]interface{}{
			"vm": vmID,
			"attributes": map[string]interface{}{
				"dtype":               "DISK",
				"type":                "VIRTIO",
				"path":                fmt.Sprintf("/dev/zvol/%s", openebsPath),
				"iotype":              "THREADS",
				"create_zvol":         false,
				"logical_sectorsize":  nil,
				"physical_sectorsize": nil,
				"serial":              vm.generateRandomSerial(),
				"zvol_name":           nil,
				"zvol_volsize":        nil,
			},
			"order": 1004,
		}

		if _, err := vm.client.Call("vm.device.create", []interface{}{openebsDevice}, 30); err != nil {
			return fmt.Errorf("failed to create OpenEBS disk device: %w", err)
		}
		vm.logger.Info("Created OpenEBS disk device: /dev/zvol/%s", openebsPath)
	}

	// Rook disk (order 1005) - matching working script structure
	if rookPath, exists := zvolPaths["rook"]; exists && rookPath != "" {
		rookDevice := map[string]interface{}{
			"vm": vmID,
			"attributes": map[string]interface{}{
				"dtype":               "DISK",
				"type":                "VIRTIO",
				"path":                fmt.Sprintf("/dev/zvol/%s", rookPath),
				"iotype":              "THREADS",
				"create_zvol":         false,
				"logical_sectorsize":  nil,
				"physical_sectorsize": nil,
				"serial":              vm.generateRandomSerial(),
				"zvol_name":           nil,
				"zvol_volsize":        nil,
			},
			"order": 1005,
		}

		if _, err := vm.client.Call("vm.device.create", []interface{}{rookDevice}, 30); err != nil {
			return fmt.Errorf("failed to create Rook disk device: %w", err)
		}
		vm.logger.Info("Created Rook disk device: /dev/zvol/%s", rookPath)
	}

	// Create SPICE display device (order 1003) - matching working script
	if config.SpicePassword == "" {
		return fmt.Errorf("SPICE password is required for display device")
	}

	displayDevice := map[string]interface{}{
		"vm": vmID,
		"attributes": map[string]interface{}{
			"bind":       "192.168.120.10", // SPICE bind interface
			"dtype":      "DISPLAY",
			"password":   config.SpicePassword,
			"port":       nil,      // Let TrueNAS auto-assign port
			"resolution": "1920x1080",
			"type":       "SPICE",  // Always SPICE
			"wait":       false,
			"web":        true,
			"web_port":   nil,      // Let TrueNAS auto-assign web port
		},
		"order": 1003,
	}

	if _, err := vm.client.Call("vm.device.create", []interface{}{displayDevice}, 30); err != nil {
		return fmt.Errorf("failed to create display device: %w", err)
	}

	vm.logger.Info("Created SPICE display device with password from config")
	vm.logger.Info("Display access: SPICE://192.168.120.10:[auto-assigned] (web: https://192.168.120.10:[auto-assigned])")

	vm.logger.Success("All VM devices created successfully")
	return nil
}

func (vm *VMManager) discoverVMZVols(vmItem *VM) ([]string, error) {
	vm.logger.Info("Discovering ZVols for VM %s (ID: %d)", vmItem.Name, vmItem.ID)
	var zvolPaths []string

	// Query VM devices to find disk devices
	devices, err := vm.client.QueryVMDevices(vmItem.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to query VM devices: %w", err)
	}

	vm.logger.Info("Found %d devices for VM %s", len(devices), vmItem.Name)

	for i, device := range devices {
		vm.logger.Info("Device %d: %+v", i+1, device)
		if attributes, ok := device["attributes"].(map[string]interface{}); ok {
			vm.logger.Info("Device attributes: %+v", attributes)
			if dtype, ok := attributes["dtype"].(string); ok && dtype == "DISK" {
				vm.logger.Info("Found DISK device: %+v", device)
				if path, ok := attributes["path"].(string); ok {
					vm.logger.Info("Device path: %s", path)
					// Extract ZVol path from device path
					if strings.HasPrefix(path, "/dev/zvol/") {
						zvolPath := strings.TrimPrefix(path, "/dev/zvol/")
						zvolPaths = append(zvolPaths, zvolPath)
						vm.logger.Info("Found ZVol: %s", zvolPath)
					}
				}
			}
		}
	}

	vm.logger.Info("Discovered %d ZVols for VM %s: %v", len(zvolPaths), vmItem.Name, zvolPaths)
	return zvolPaths, nil
}

func (vm *VMManager) deleteZVolsByPaths(zvolPaths []string, vmName string) error {
	for _, zvolPath := range zvolPaths {
		vm.logger.Info("Deleting ZVol: %s", zvolPath)
		if err := vm.client.DeleteDataset(zvolPath, false); err != nil {
			vm.logger.Error("Failed to delete ZVol %s: %v", zvolPath, err)
			return err
		}
		vm.logger.Success("✓ Deleted ZVol: %s", zvolPath)
	}
	return nil
}