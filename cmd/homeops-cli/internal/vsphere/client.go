package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"homeops-cli/internal/common"
)

// Client represents a vSphere/ESXi client
type Client struct {
	client     *govmomi.Client
	vim        *vim25.Client
	finder     *find.Finder
	logger     *common.ColorLogger
	ctx        context.Context
	cancel     context.CancelFunc
	datacenter *object.Datacenter
}

// NewClient creates a new vSphere client
func NewClient(host, username, password string, insecure bool) *Client {
	return &Client{
		logger: common.NewColorLogger(),
	}
}

// Connect establishes connection to vSphere/ESXi
func (c *Client) Connect(host, username, password string, insecure bool) error {
	c.ctx, c.cancel = context.WithCancel(context.Background())

	// Parse URL
	u, err := url.Parse(fmt.Sprintf("https://%s/sdk", host))
	if err != nil {
		return fmt.Errorf("failed to parse URL: %w", err)
	}
	u.User = url.UserPassword(username, password)

	// Create client
	client, err := govmomi.NewClient(c.ctx, u, insecure)
	if err != nil {
		return fmt.Errorf("failed to create vSphere client: %w", err)
	}

	c.client = client
	c.vim = client.Client
	c.finder = find.NewFinder(c.vim, true)

	// Find datacenter (use default for standalone ESXi)
	datacenter, err := c.finder.DefaultDatacenter(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to find datacenter: %w", err)
	}
	c.datacenter = datacenter
	c.finder.SetDatacenter(datacenter)

	c.logger.Success("Connected to vSphere/ESXi: %s", host)
	return nil
}

// Close closes the vSphere connection
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.client != nil {
		return c.client.Logout(c.ctx)
	}
	return nil
}

// CreateVM creates a new VM with specified configuration
func (c *Client) CreateVM(config VMConfig) (*object.VirtualMachine, error) {
	// Find resource pool (use default for standalone ESXi)
	pool, err := c.finder.DefaultResourcePool(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find resource pool: %w", err)
	}

	// Find datastore
	datastore, err := c.finder.Datastore(c.ctx, config.Datastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore %s: %w", config.Datastore, err)
	}

	// Find network - needed for SR-IOV network backing
	network, err := c.finder.Network(c.ctx, config.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to find network %s: %w", config.Network, err)
	}

	// Find VM folder
	folders, err := c.datacenter.Folders(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get folders: %w", err)
	}

	// Create VM spec
	spec := types.VirtualMachineConfigSpec{
		Name:     config.Name,
		GuestId:  "other6xLinux64Guest", // Other 6.x or later Linux (64-bit) - matches manual VMs
		NumCPUs:  int32(config.VCPUs),
		MemoryMB: int64(config.Memory),
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", config.Datastore),
		},
		Firmware: "efi", // Use EFI for Talos
		BootOptions: &types.VirtualMachineBootOptions{
			EfiSecureBootEnabled: types.NewBool(false), // Disable secure boot for Talos
		},
		Flags: &types.VirtualMachineFlagInfo{
			VirtualMmuUsage:  "automatic",
			VirtualExecUsage: "hvAuto",
		},
		// Memory reservation - reserve all guest memory for SR-IOV
		MemoryReservationLockedToMax: types.NewBool(true),
		MemoryAllocation: &types.ResourceAllocationInfo{
			Reservation: types.NewInt64(int64(config.Memory)), // Reserve all memory in MB
			Limit:       types.NewInt64(-1),                   // No limit
			Shares: &types.SharesInfo{
				Level: types.SharesLevelNormal,
			},
		},
		// CPU allocation with high shares
		CpuAllocation: &types.ResourceAllocationInfo{
			Reservation: types.NewInt64(10000), // 10000MHz CPU reservation
			Limit:       types.NewInt64(-1),    // No limit
			Shares: &types.SharesInfo{
				Level: types.SharesLevelHigh, // Set CPU shares to high
			},
		},
		// VMware Tools configuration
		Tools: &types.ToolsConfigInfo{
			SyncTimeWithHost: types.NewBool(true), // Synchronize guest time with host
		},
		// Latency sensitivity configuration
		LatencySensitivity: &types.LatencySensitivity{
			Level: types.LatencySensitivitySensitivityLevelMedium, // Set latency sensitivity to medium
		},
	}

	// Create devices
	var devices []types.BaseVirtualDevice

	// Add SCSI controller
	scsiController := &types.ParaVirtualSCSIController{
		VirtualSCSIController: types.VirtualSCSIController{
			SharedBus: types.VirtualSCSISharingNoSharing,
			VirtualController: types.VirtualController{
				BusNumber: 0,
				VirtualDevice: types.VirtualDevice{
					Key: 1000,
				},
			},
		},
	}
	devices = append(devices, scsiController)

	// Add SR-IOV network adapter - uses regular network backing, not physical function backing
	backing, err := network.EthernetCardBackingInfo(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network backing: %w", err)
	}

	netDevice := &types.VirtualSriovEthernetCard{
		VirtualEthernetCard: types.VirtualEthernetCard{
			VirtualDevice: types.VirtualDevice{
				Key:           13031,   // Match manual VM key exactly
				ControllerKey: 100,     // PCI controller key from manual VM
				Backing:       backing, // Use regular network backing for vl999
			},
			AddressType: "generated",
		},
		AllowGuestOSMtuChange: types.NewBool(false), // Disable guest OS MTU changes
		SriovBacking: &types.VirtualSriovEthernetCardSriovBackingInfo{
			PhysicalFunctionBacking: &types.VirtualPCIPassthroughDeviceBackingInfo{
				Id: config.PhysicalFunction,
			},
		},
	}

	// Set MAC address if provided
	if config.MacAddress != "" {
		netDevice.AddressType = "manual"
		netDevice.MacAddress = config.MacAddress
	}

	devices = append(devices, netDevice)

	// Add SATA controller for CD-ROM (matches manual VMs)
	sataController := &types.VirtualAHCIController{
		VirtualSATAController: types.VirtualSATAController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{
					Key: 15000,
				},
				BusNumber: 0,
			},
		},
	}
	devices = append(devices, sataController)

	// Add CD-ROM with ISO on SATA controller
	if config.ISO != "" {
		cdrom := &types.VirtualCdrom{
			VirtualDevice: types.VirtualDevice{
				Key:           16000,
				ControllerKey: 15000, // SATA controller
				UnitNumber:    types.NewInt32(0),
				Backing: &types.VirtualCdromIsoBackingInfo{
					VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
						FileName: config.ISO,
					},
				},
				Connectable: &types.VirtualDeviceConnectInfo{
					Connected:         true, // Connect ISO for booting
					StartConnected:    true,
					AllowGuestControl: true,
				},
			},
		}
		devices = append(devices, cdrom)
	}

	// Add boot disk
	bootDisk := c.createDisk(config.DiskSize, 0, datastore.Reference().Value, config.Datastore, config.Name)
	devices = append(devices, bootDisk)

	// Add OpenEBS disk if specified
	if config.OpenEBSSize > 0 {
		openebsDisk := c.createDisk(config.OpenEBSSize, 1, datastore.Reference().Value, config.Datastore, config.Name)
		devices = append(devices, openebsDisk)
	}

	// Add Rook disk if specified
	if config.RookSize > 0 {
		rookDisk := c.createDisk(config.RookSize, 2, datastore.Reference().Value, config.Datastore, config.Name)
		devices = append(devices, rookDisk)
	}

	// Add devices to spec
	var deviceChanges []types.BaseVirtualDeviceConfigSpec
	for _, device := range devices {
		spec := &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    device,
		}
		// For VirtualDisk devices, specify file operation to create new disk
		if _, isDisk := device.(*types.VirtualDisk); isDisk {
			spec.FileOperation = types.VirtualDeviceConfigSpecFileOperationCreate
		}
		deviceChanges = append(deviceChanges, spec)
	}
	spec.DeviceChange = deviceChanges

	// Create VM
	task, err := folders.VmFolder.CreateVM(c.ctx, spec, pool, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	info, err := task.WaitForResult(c.ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	vm := object.NewVirtualMachine(c.vim, info.Result.(types.ManagedObjectReference))
	c.logger.Success("VM %s created successfully", config.Name)

	return vm, nil
}

// createDisk creates a virtual disk specification
func (c *Client) createDisk(sizeGB int, unitNumber int, _, datastoreName, _ string) *types.VirtualDisk {
	// All disks are thick provisioned as requested
	// Unit 0: Boot disk (100GB) - thick provisioned
	// Unit 1: OpenEBS disk (800GB) - thick provisioned
	// Unit 2: Rook disk (600GB) - thick provisioned
	thinProvisioned := false

	disk := &types.VirtualDisk{
		VirtualDevice: types.VirtualDevice{
			Key:           int32(2000 + unitNumber),
			ControllerKey: 1000, // SCSI controller key
			UnitNumber:    types.NewInt32(int32(unitNumber)),
			Backing: &types.VirtualDiskFlatVer2BackingInfo{
				VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
					FileName: fmt.Sprintf("[%s]", datastoreName),
				},
				DiskMode:        "persistent",
				ThinProvisioned: types.NewBool(thinProvisioned),
			},
		},
		CapacityInKB:    int64(sizeGB) * 1024 * 1024,        // GB to KB: 1GB = 1024*1024 KB
		CapacityInBytes: int64(sizeGB) * 1024 * 1024 * 1024, // GB to Bytes: 1GB = 1024*1024*1024 Bytes
	}

	// Debug: log what we're actually setting
	c.logger.Info("Disk %d: Input %d GB = %d KB = %d Bytes", unitNumber, sizeGB, int64(sizeGB)*1024*1024, int64(sizeGB)*1024*1024*1024)

	return disk
}

// PowerOnVM powers on a VM
func (c *Client) PowerOnVM(vm *object.VirtualMachine) error {
	task, err := vm.PowerOn(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to power on VM: %w", err)
	}

	_, err = task.WaitForResult(c.ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to power on VM: %w", err)
	}

	c.logger.Success("VM powered on successfully")
	return nil
}

// PowerOffVM powers off a VM
func (c *Client) PowerOffVM(vm *object.VirtualMachine) error {
	task, err := vm.PowerOff(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to power off VM: %w", err)
	}

	if _, err := task.WaitForResult(c.ctx, nil); err != nil {
		return fmt.Errorf("failed to power off VM: %w", err)
	}

	c.logger.Success("VM powered off successfully")
	return nil
}

// DeleteVM deletes a VM
func (c *Client) DeleteVM(vm *object.VirtualMachine) error {
	// Power off if running
	var mvm mo.VirtualMachine
	propErr := vm.Properties(c.ctx, vm.Reference(), []string{"runtime.powerState"}, &mvm)
	if propErr == nil && mvm.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
		c.logger.Info("Powering off VM before deletion...")
		if err := c.PowerOffVM(vm); err != nil {
			c.logger.Warn("Failed to power off VM: %v", err)
		}
	}

	// Delete VM
	task, err := vm.Destroy(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	if _, err := task.WaitForResult(c.ctx, nil); err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	c.logger.Success("VM deleted successfully")
	return nil
}

// FindVM finds a VM by name
func (c *Client) FindVM(name string) (*object.VirtualMachine, error) {
	vm, err := c.finder.VirtualMachine(c.ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	return vm, nil
}

// ListVMs lists all VMs
func (c *Client) ListVMs() ([]*object.VirtualMachine, error) {
	vms, err := c.finder.VirtualMachineList(c.ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	return vms, nil
}

// GetVMInfo gets detailed VM information
func (c *Client) GetVMInfo(vm *object.VirtualMachine) (*mo.VirtualMachine, error) {
	var mvm mo.VirtualMachine
	err := vm.Properties(c.ctx, vm.Reference(), nil, &mvm)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM properties: %w", err)
	}
	return &mvm, nil
}

// UploadISOToDatastore uploads an ISO file to a vSphere datastore
func (c *Client) UploadISOToDatastore(localFilePath, datastoreName, remoteFileName string) error {
	c.logger.Debug("Uploading ISO %s to datastore %s as %s", localFilePath, datastoreName, remoteFileName)

	// Find the datastore
	datastore, err := c.finder.Datastore(c.ctx, datastoreName)
	if err != nil {
		return fmt.Errorf("failed to find datastore %s: %w", datastoreName, err)
	}

	// Get file info for size logging
	fileInfo, err := os.Stat(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	c.logger.Info("Uploading %s (%d MB) to datastore...", remoteFileName, fileInfo.Size()/(1024*1024))

	// Upload file using datastore UploadFile method
	if err := datastore.UploadFile(c.ctx, localFilePath, remoteFileName, nil); err != nil {
		return fmt.Errorf("failed to upload file to datastore: %w", err)
	}

	c.logger.Success("ISO uploaded successfully to [%s] %s", datastoreName, remoteFileName)
	return nil
}

// DeployVMsConcurrently deploys multiple VMs in parallel
func (c *Client) DeployVMsConcurrently(configs []VMConfig) error {
	var wg sync.WaitGroup
	errors := make(chan error, len(configs))

	// Semaphore to limit concurrent deployments (adjust as needed)
	sem := make(chan struct{}, 3)

	for _, config := range configs {
		wg.Add(1)
		go func(cfg VMConfig) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			c.logger.Info("Starting deployment of VM: %s", cfg.Name)
			startTime := time.Now()

			// Create VM
			vm, err := c.CreateVM(cfg)
			if err != nil {
				errors <- fmt.Errorf("failed to create VM %s: %w", cfg.Name, err)
				return
			}

			// Power on VM if requested
			if cfg.PowerOn {
				if err := c.PowerOnVM(vm); err != nil {
					errors <- fmt.Errorf("failed to power on VM %s: %w", cfg.Name, err)
					return
				}
			}

			c.logger.Success("VM %s deployed in %v", cfg.Name, time.Since(startTime))
		}(config)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(errors)

	// Collect errors
	var allErrors []string
	for err := range errors {
		if err != nil {
			allErrors = append(allErrors, err.Error())
		}
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("deployment errors:\n%s", strings.Join(allErrors, "\n"))
	}

	return nil
}
