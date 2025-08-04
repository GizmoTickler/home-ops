package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"truenas-vm-tools/truenas"
)

type Config struct {
	Name          string
	Memory        int
	VCPUs         int
	DiskSize      int
	TrueNASHost   string
	TrueNASAPIKey string
	TrueNASPort   int
	NoSSL         bool
	TalosISO      string
	NetworkBridge string
	StoragePool   string
	ZVolPath      string
	MacAddress    string
	BootZVol      string
	OpenEBSZVol   string
	RookZVol      string
	SkipZVolCreate bool
	SpicePassword string
	UseSpice      bool
}

func main() {
	var config Config

	// Parse command line flags
	flag.StringVar(&config.Name, "name", "", "VM name (required)")
	flag.IntVar(&config.Memory, "memory", 4096, "Memory in MB")
	flag.IntVar(&config.VCPUs, "vcpus", 2, "Number of vCPUs")
	flag.IntVar(&config.DiskSize, "disk-size", 20, "Disk size in GB")
	flag.StringVar(&config.TrueNASHost, "truenas-host", "", "TrueNAS hostname or IP (required)")
	flag.StringVar(&config.TrueNASAPIKey, "truenas-api-key", "", "TrueNAS API key (required)")
	flag.IntVar(&config.TrueNASPort, "truenas-port", 443, "TrueNAS port")
	flag.BoolVar(&config.NoSSL, "no-ssl", false, "Disable SSL/TLS")
	flag.StringVar(&config.TalosISO, "talos-iso", "https://github.com/siderolabs/talos/releases/latest/download/metal-amd64.iso", "Talos ISO URL")
	flag.StringVar(&config.NetworkBridge, "network-bridge", "br0", "Network bridge")
	flag.StringVar(&config.StoragePool, "storage-pool", "tank", "Storage pool name (deprecated, use specific zvol flags)")
	flag.StringVar(&config.ZVolPath, "zvol-path", "", "Full ZVol path (deprecated, use --boot-zvol)")
	flag.StringVar(&config.MacAddress, "mac-address", "", "MAC address for VM (if not specified, auto-generated)")
	flag.StringVar(&config.BootZVol, "boot-zvol", "", "Boot disk ZVol path (e.g., tank/vms/vm-name-boot)")
	flag.StringVar(&config.OpenEBSZVol, "openebs-zvol", "", "OpenEBS ZVol path (e.g., tank/vms/vm-name-openebs)")
	flag.StringVar(&config.RookZVol, "rook-zvol", "", "Rook ZVol path (e.g., tank/vms/vm-name-rook)")
	flag.BoolVar(&config.SkipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation (assume they already exist)")
	flag.StringVar(&config.SpicePassword, "spice-password", "", "SPICE password (if not specified, uses VNC)")
	flag.BoolVar(&config.UseSpice, "use-spice", false, "Use SPICE display instead of VNC")

	flag.Parse()

	// Validate required flags
	if config.Name == "" {
		log.Fatal("VM name is required")
	}
	if config.TrueNASHost == "" {
		log.Fatal("TrueNAS host is required")
	}
	if config.TrueNASAPIKey == "" {
		log.Fatal("TrueNAS API key is required")
	}

	// Create TrueNAS client using the working client
	client := truenas.NewWorkingClient(config.TrueNASHost, config.TrueNASAPIKey, config.TrueNASPort, !config.NoSSL)

	// Connect to TrueNAS
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect to TrueNAS: %v", err)
	}
	defer client.Close()

	// Deploy the VM
	if err := deployVM(client, config); err != nil {
		log.Fatalf("Failed to deploy VM: %v", err)
	}

	log.Printf("Successfully deployed VM: %s", config.Name)
}

func deployVM(client *truenas.WorkingClient, config Config) error {
	// Check if VM already exists by querying all VMs and filtering by name
	allVMs, err := client.QueryVMs(nil)
	if err != nil {
		return fmt.Errorf("failed to query existing VMs: %w", err)
	}

	// Check if VM with this name already exists
	for _, vm := range allVMs {
		if vm.Name == config.Name {
			return fmt.Errorf("VM %s already exists", config.Name)
		}
	}

	// Create or verify ZVols
	if !config.SkipZVolCreate {
		if err := createZVols(client, config); err != nil {
			return fmt.Errorf("failed to create ZVols: %w", err)
		}
	} else {
		if err := verifyZVols(client, config); err != nil {
			return fmt.Errorf("failed to verify ZVols: %w", err)
		}
	}

	// Create VM configuration
	vmConfig := buildVMConfig(config)

	// Create the VM using the working client's Call method
	result, err := client.Call("vm.create", []interface{}{vmConfig}, 60)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// Parse the VM creation result
	var vmResult map[string]interface{}
	if err := json.Unmarshal(result, &vmResult); err != nil {
		return fmt.Errorf("failed to parse VM creation result: %w", err)
	}

	// Extract VM ID from the result
	var vmID int
	if resultField, exists := vmResult["result"]; exists {
		if vmData, ok := resultField.(map[string]interface{}); ok {
			if id, exists := vmData["id"]; exists {
				if idFloat, ok := id.(float64); ok {
					vmID = int(idFloat)
				}
			}
		}
	}

	log.Printf("Created VM %s with ID %d", config.Name, vmID)

	// Now create devices for the VM
	if err := createVMDevices(client, vmID, config); err != nil {
		return fmt.Errorf("failed to create VM devices: %w", err)
	}

	return nil
}

func createZVols(client *truenas.WorkingClient, config Config) error {
	zvols := getZVolPaths(config)

	for name, path := range zvols {
		if path == "" {
			log.Printf("Skipping %s ZVol (not specified)", name)
			continue
		}

		if err := createSingleZVol(client, path, config.DiskSize, name); err != nil {
			return fmt.Errorf("failed to create %s ZVol: %w", name, err)
		}
	}

	return nil
}

func verifyZVols(client *truenas.WorkingClient, config Config) error {
	zvols := getZVolPaths(config)

	for name, path := range zvols {
		if path == "" {
			continue
		}

		// Query all datasets and filter by name
		allDatasets, err := client.QueryDatasets(nil)
		if err != nil {
			return fmt.Errorf("failed to query datasets: %w", err)
		}

		// Check if the ZVol exists
		found := false
		for _, dataset := range allDatasets {
			if dataset.Name == path {
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("%s ZVol %s does not exist", name, path)
		}
		log.Printf("Verified %s ZVol: %s", name, path)
	}

	return nil
}

func getZVolPaths(config Config) map[string]string {
	zvols := make(map[string]string)

	// Primary boot disk
	if config.BootZVol != "" {
		zvols["boot"] = config.BootZVol
	} else if config.ZVolPath != "" {
		// Backward compatibility
		zvols["boot"] = config.ZVolPath
	} else {
		// Legacy format
		zvols["boot"] = fmt.Sprintf("%s/vms/%s-boot", config.StoragePool, config.Name)
	}

	// Additional storage ZVols
	if config.OpenEBSZVol != "" {
		zvols["openebs"] = config.OpenEBSZVol
	}

	if config.RookZVol != "" {
		zvols["rook"] = config.RookZVol
	}

	return zvols
}

func createSingleZVol(client *truenas.WorkingClient, zvolPath string, sizeGB int, zvolType string) error {
	log.Printf("Creating %s ZVol: %s", zvolType, zvolPath)

	// Check if ZVol already exists
	allDatasets, err := client.QueryDatasets(nil)
	if err != nil {
		return fmt.Errorf("failed to query existing datasets: %w", err)
	}

	// Check if ZVol already exists
	for _, dataset := range allDatasets {
		if dataset.Name == zvolPath {
			log.Printf("ZVol %s already exists", zvolPath)
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
			log.Printf("Creating parent dataset: %s", parentPath)
			_, err := client.CreateDataset(truenas.DatasetCreateRequest{
				Name: parentPath,
				Type: "FILESYSTEM",
			})
			if err != nil {
				return fmt.Errorf("failed to create parent dataset %s: %w", parentPath, err)
			}
		}
	}

	// Determine size based on ZVol type
	var volsize int64
	switch zvolType {
	case "boot":
		volsize = int64(sizeGB) * 1024 * 1024 * 1024 // Use specified size for boot disk
	case "openebs":
		volsize = int64(100) * 1024 * 1024 * 1024 // 100GB for OpenEBS
	case "rook":
		volsize = int64(200) * 1024 * 1024 * 1024 // 200GB for Rook
	default:
		volsize = int64(sizeGB) * 1024 * 1024 * 1024 // Default to specified size
	}

	log.Printf("Creating %s ZVol: %s (%.1fGB)", zvolType, zvolPath, float64(volsize)/(1024*1024*1024))

	_, err = client.CreateDataset(truenas.DatasetCreateRequest{
		Name:         zvolPath,
		Type:         "VOLUME",
		Volsize:      &volsize,
		Volblocksize: "16K",
	})
	if err != nil {
		return fmt.Errorf("failed to create ZVol: %w", err)
	}

	log.Printf("Created %s ZVol: %s", zvolType, zvolPath)
	return nil
}

func buildVMConfig(config Config) map[string]interface{} {
	// Generate MAC address if not provided
	macAddress := config.MacAddress
	if macAddress == "" {
		macAddress = generateRandomMAC()
	}

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

// generateRandomMAC generates a random MAC address
func generateRandomMAC() string {
	rand.Seed(time.Now().UnixNano())
	mac := make([]byte, 6)
	mac[0] = 0x00
	mac[1] = 0xa0
	mac[2] = 0x98
	for i := 3; i < 6; i++ {
		mac[i] = byte(rand.Intn(256))
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

// createVMDevices creates devices for the VM after it's been created
func createVMDevices(client *truenas.WorkingClient, vmID int, config Config) error {
	log.Printf("Creating devices for VM ID %d", vmID)

	// Generate MAC address if not provided
	macAddress := config.MacAddress
	if macAddress == "" {
		macAddress = generateRandomMAC()
	}

	// Create CD-ROM device first (order 1000) - matching GUI structure exactly
	cdromDevice := map[string]interface{}{
		"vm": vmID,
		"attributes": map[string]interface{}{
			"dtype": "CDROM",
			"path":  "/mnt/flashstor/ISO/metal-amd64.iso", // Use the same ISO as in GUI
		},
		"order": 1000,
	}

	_, err := client.Call("vm.device.create", []interface{}{cdromDevice}, 30)
	if err != nil {
		return fmt.Errorf("failed to create CD-ROM device: %w", err)
	}
	log.Printf("Created CD-ROM device with ISO: /mnt/flashstor/ISO/metal-amd64.iso")

	// Create network device (order 1002) - matching GUI structure
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

	_, err = client.Call("vm.device.create", []interface{}{nicDevice}, 30)
	if err != nil {
		return fmt.Errorf("failed to create NIC device: %w", err)
	}
	log.Printf("Created NIC device with MAC %s on bridge %s", macAddress, config.NetworkBridge)

	// Create disk devices with correct order matching GUI
	zvols := getZVolPaths(config)

	// Boot disk (order 1001) - matching GUI structure
	if bootPath, exists := zvols["boot"]; exists && bootPath != "" {
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
				"serial":              generateRandomSerial(),
				"zvol_name":           nil,
				"zvol_volsize":        nil,
			},
			"order": 1001,
		}

		_, err = client.Call("vm.device.create", []interface{}{bootDevice}, 30)
		if err != nil {
			return fmt.Errorf("failed to create boot disk device: %w", err)
		}
		log.Printf("Created boot disk device: /dev/zvol/%s", bootPath)
	}

	// OpenEBS disk (order 1004) - matching GUI structure
	if openebsPath, exists := zvols["openebs"]; exists && openebsPath != "" {
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
				"serial":              generateRandomSerial(),
				"zvol_name":           nil,
				"zvol_volsize":        nil,
			},
			"order": 1004,
		}

		_, err = client.Call("vm.device.create", []interface{}{openebsDevice}, 30)
		if err != nil {
			return fmt.Errorf("failed to create OpenEBS disk device: %w", err)
		}
		log.Printf("Created OpenEBS disk device: /dev/zvol/%s", openebsPath)
	}

	// Rook disk (order 1005) - matching GUI structure
	if rookPath, exists := zvols["rook"]; exists && rookPath != "" {
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
				"serial":              generateRandomSerial(),
				"zvol_name":           nil,
				"zvol_volsize":        nil,
			},
			"order": 1005,
		}

		_, err = client.Call("vm.device.create", []interface{}{rookDevice}, 30)
		if err != nil {
			return fmt.Errorf("failed to create Rook disk device: %w", err)
		}
		log.Printf("Created Rook disk device: /dev/zvol/%s", rookPath)
	}

	// Use SPICE password from config (from 1Password via op inject)
	if config.SpicePassword == "" {
		return fmt.Errorf("SPICE password is required - use -spice-password flag")
	}

	// Create SPICE display device (order 1003) - let TrueNAS auto-assign ports
	displayDevice := map[string]interface{}{
		"vm": vmID,
		"attributes": map[string]interface{}{
			"bind":       "192.168.120.10", // Same bind IP as GUI
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

	_, err = client.Call("vm.device.create", []interface{}{displayDevice}, 30)
	if err != nil {
		return fmt.Errorf("failed to create display device: %w", err)
	}

	log.Printf("Created SPICE display device with password from config")
	log.Printf("Display access: SPICE://192.168.120.10:[auto-assigned] (web: https://192.168.120.10:[auto-assigned])")

	return nil
}

// generateRandomSerial generates a random serial number for disks
func generateRandomSerial() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	rand.Seed(time.Now().UnixNano())
	serial := make([]byte, 8)
	for i := range serial {
		serial[i] = charset[rand.Intn(len(charset))]
	}
	return string(serial)
}
