package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"truenas-vm-tools/truenas"

	"github.com/google/uuid"
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

	// Create TrueNAS client
	client := truenas.NewSimpleClient(config.TrueNASHost, config.TrueNASAPIKey, !config.NoSSL)

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

func deployVM(client *truenas.SimpleClient, config Config) error {
	// Check if VM already exists
	existingVMs, err := client.QueryVMs(map[string]string{"name": config.Name})
	if err != nil {
		return fmt.Errorf("failed to query existing VMs: %w", err)
	}
	if len(existingVMs) > 0 {
		return fmt.Errorf("VM %s already exists", config.Name)
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

	// Create the VM
	vm, err := client.CreateVM(vmConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	log.Printf("Created VM %s with ID %d", vm.Name, vm.ID)
	return nil
}

func createZVols(client *truenas.SimpleClient, config Config) error {
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

func verifyZVols(client *truenas.SimpleClient, config Config) error {
	zvols := getZVolPaths(config)

	for name, path := range zvols {
		if path == "" {
			continue
		}

		existingDatasets, err := client.QueryDatasets(map[string]string{"name": path})
		if err != nil {
			return fmt.Errorf("failed to query %s ZVol %s: %w", name, path, err)
		}
		if len(existingDatasets) == 0 {
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

func createSingleZVol(client *truenas.SimpleClient, zvolPath string, sizeGB int, zvolType string) error {
	log.Printf("Creating %s ZVol: %s", zvolType, zvolPath)

	// Check if ZVol already exists
	existingDatasets, err := client.QueryDatasets(map[string]string{"name": zvolPath})
	if err != nil {
		return fmt.Errorf("failed to query existing datasets: %w", err)
	}
	if len(existingDatasets) > 0 {
		log.Printf("ZVol %s already exists", zvolPath)
		return nil
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

		existingParent, err := client.QueryDatasets(map[string]string{"name": parentPath})
		if err != nil {
			return fmt.Errorf("failed to query parent dataset %s: %w", parentPath, err)
		}

		if len(existingParent) == 0 {
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

func buildVMConfig(config Config) truenas.VMCreateRequest {
	// Generate random VNC password
	vncPassword := strings.ReplaceAll(uuid.New().String()[:8], "-", "")

	vmConfig := truenas.VMCreateRequest{
		Name:            config.Name,
		Description:     fmt.Sprintf("Talos Linux VM - %s", config.Name),
		VCPUs:           config.VCPUs,
		Memory:          config.Memory,
		Bootloader:      "UEFI",
		Autostart:       false,
		Time:            "LOCAL",
		ShutdownTimeout: 90,
		Devices:         []truenas.VMDevice{},
	}

	// Add disk devices
	zvols := getZVolPaths(config)

	// Boot disk (primary)
	if bootPath, exists := zvols["boot"]; exists && bootPath != "" {
		bootDevice := truenas.VMDevice{
			"dtype":  "DISK",
			"path":   fmt.Sprintf("/dev/zvol/%s", bootPath),
			"type":   "VIRTIO",
			"iotype": "THREADS",
		}
		vmConfig.Devices = append(vmConfig.Devices, bootDevice)
		log.Printf("Added boot disk: /dev/zvol/%s", bootPath)
	}

	// OpenEBS disk
	if openebsPath, exists := zvols["openebs"]; exists && openebsPath != "" {
		openebsDevice := truenas.VMDevice{
			"dtype":  "DISK",
			"path":   fmt.Sprintf("/dev/zvol/%s", openebsPath),
			"type":   "VIRTIO",
			"iotype": "THREADS",
		}
		vmConfig.Devices = append(vmConfig.Devices, openebsDevice)
		log.Printf("Added OpenEBS disk: /dev/zvol/%s", openebsPath)
	}

	// Rook disk
	if rookPath, exists := zvols["rook"]; exists && rookPath != "" {
		rookDevice := truenas.VMDevice{
			"dtype":  "DISK",
			"path":   fmt.Sprintf("/dev/zvol/%s", rookPath),
			"type":   "VIRTIO",
			"iotype": "THREADS",
		}
		vmConfig.Devices = append(vmConfig.Devices, rookDevice)
		log.Printf("Added Rook disk: /dev/zvol/%s", rookPath)
	}

	// TODO: Add CD-ROM device for Talos ISO later
	// cdromDevice := truenas.VMDevice{
	// 	DType: "CDROM",
	// 	Attributes: map[string]interface{}{
	// 		"path": fmt.Sprintf("/mnt/%s/isos/talos-%s.iso", config.StoragePool, config.Name),
	// 	},
	// }
	// vmConfig.Devices = append(vmConfig.Devices, cdromDevice)

	// Add network device
	nicDevice := truenas.VMDevice{
		"dtype":      "NIC",
		"type":       "VIRTIO",
		"nic_attach": config.NetworkBridge,
	}

	// Add MAC address if specified
	if config.MacAddress != "" {
		nicDevice["mac"] = config.MacAddress
		log.Printf("Using specified MAC address: %s", config.MacAddress)
	} else {
		log.Printf("MAC address will be auto-generated")
	}

	vmConfig.Devices = append(vmConfig.Devices, nicDevice)

	// Add display device (SPICE or VNC)
	var displayInfo string

	if config.UseSpice || config.SpicePassword != "" {
		// Use SPICE display
		displayDevice := truenas.VMDevice{
			"dtype":    "DISPLAY",
			"type":     "SPICE",
			"bind":     "0.0.0.0",
			"password": config.SpicePassword,
		}

		if config.SpicePassword != "" {
			displayInfo = "SPICE with password"
		} else {
			displayInfo = "SPICE without password"
		}

		vmConfig.Devices = append(vmConfig.Devices, displayDevice)
	} else {
		// Use VNC display (default)
		displayDevice := truenas.VMDevice{
			"dtype":    "DISPLAY",
			"type":     "VNC",
			"bind":     "0.0.0.0",
			"password": vncPassword,
			"web":      true,
		}
		displayInfo = fmt.Sprintf("VNC with password: %s", vncPassword)
		vmConfig.Devices = append(vmConfig.Devices, displayDevice)
	}

	log.Printf("VM Configuration:")
	log.Printf("  Name: %s", vmConfig.Name)
	log.Printf("  Memory: %d MB", vmConfig.Memory)
	log.Printf("  vCPUs: %d", vmConfig.VCPUs)
	if bootPath, exists := getZVolPaths(config)["boot"]; exists && bootPath != "" {
		log.Printf("  Boot Disk: /dev/zvol/%s", bootPath)
	}
	log.Printf("  Network: %s", config.NetworkBridge)
	log.Printf("  Display: %s", displayInfo)

	return vmConfig
}
