package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"homeops-cli/internal/common"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
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

// NewClient creates a new vSphere client (deprecated - use NewClientWithConnect instead)
// Note: The parameters are unused; call Connect() separately to establish connection
func NewClient(host, username, password string, insecure bool) *Client {
	return &Client{
		logger: common.NewColorLogger(),
	}
}

// NewClientWithConnect creates a new vSphere client and connects immediately
func NewClientWithConnect(host, username, password string, insecure bool) (*Client, error) {
	c := &Client{
		logger: common.NewColorLogger(),
	}
	if err := c.Connect(host, username, password, insecure); err != nil {
		return nil, err
	}
	return c, nil
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
	// Logout first before canceling context
	if c.client != nil {
		if err := c.client.Logout(c.ctx); err != nil {
			// Cancel context even if logout fails
			if c.cancel != nil {
				c.cancel()
			}
			return err
		}
	}
	// Cancel context after successful logout
	if c.cancel != nil {
		c.cancel()
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

	// Find network
	network, err := c.finder.Network(c.ctx, config.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to find network %s: %w", config.Network, err)
	}

	// Find VM folder
	folders, err := c.datacenter.Folders(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get folders: %w", err)
	}

	// Create extra config for specific requirements
	extraConfig := []types.BaseOptionValue{
		&types.OptionValue{Key: "disk.EnableUUID", Value: "TRUE"},
	}

	// Add CPU counter exposure if requested
	if config.ExposeCounters {
		extraConfig = append(extraConfig, &types.OptionValue{Key: "monitor.phys_bits_used", Value: "45"})
	}

	// Create VM spec with basic configuration
	spec := types.VirtualMachineConfigSpec{
		Name:     config.Name,
		GuestId:  "other6xLinux64Guest", // Other 6.x or later Linux (64-bit)
		NumCPUs:  int32(config.VCPUs),   // Default: 16 vCPUs
		MemoryMB: int64(config.Memory),  // Default: 48GB (49152 MB)
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s] %s", config.Datastore, config.Name),
		},
		Firmware: "efi", // Use EFI boot
		BootOptions: &types.VirtualMachineBootOptions{
			EfiSecureBootEnabled: types.NewBool(false), // Disable UEFI secure boot
		},
		Flags: &types.VirtualMachineFlagInfo{
			VirtualMmuUsage:  "automatic",
			VirtualExecUsage: "hvAuto",
			VvtdEnabled:      types.NewBool(config.EnableIOMMU), // Enable IOMMU
		},
		VPMCEnabled: types.NewBool(config.ExposeCounters), // Enable virtualized CPU performance counters
		ExtraConfig: extraConfig,
		// VMware Tools configuration - sync time with host
		Tools: &types.ToolsConfigInfo{
			SyncTimeWithHost: types.NewBool(true),
		},
	}

	// Log IOMMU status
	if config.EnableIOMMU {
		c.logger.Debug("IOMMU/VT-d enabled for VM %s", config.Name)
	}

	// PHASE 1: Create devices for initial VM creation (controllers only, NO disks yet)
	var devices []types.BaseVirtualDevice
	datastoreRef := datastore.Reference()

	// Create NVME controller 0 for boot disk
	nvmeController0 := &types.VirtualNVMEController{
		VirtualController: types.VirtualController{
			VirtualDevice: types.VirtualDevice{
				Key: -100, // Use negative key for automatic assignment
			},
			BusNumber: 0,
		},
	}
	devices = append(devices, nvmeController0)

	// Create NVME controller 1 for Longhorn disk
	nvmeController1 := &types.VirtualNVMEController{
		VirtualController: types.VirtualController{
			VirtualDevice: types.VirtualDevice{
				Key: -101, // Use negative key for automatic assignment
			},
			BusNumber: 1,
		},
	}
	devices = append(devices, nvmeController1)

	// Create vmxnet3 network adapter and set to vl999 portgroup
	backing, err := network.EthernetCardBackingInfo(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network backing: %w", err)
	}

	netDevice := &types.VirtualVmxnet3{
		VirtualVmxnet: types.VirtualVmxnet{
			VirtualEthernetCard: types.VirtualEthernetCard{
				VirtualDevice: types.VirtualDevice{
					Key:     -104, // Use negative key for automatic assignment
					Backing: backing,
					Connectable: &types.VirtualDeviceConnectInfo{
						Connected:         true,
						StartConnected:    true,
						AllowGuestControl: true,
					},
				},
				AddressType: "generated",
			},
		},
	}

	// Set MAC address if provided
	if config.MacAddress != "" {
		netDevice.AddressType = "manual"
		netDevice.MacAddress = config.MacAddress
	}

	devices = append(devices, netDevice)

	// Add CD-ROM with ISO if specified - use SATA controller for CD-ROM
	if config.ISO != "" {
		// Create SATA controller for CD-ROM
		sataController := &types.VirtualAHCIController{
			VirtualSATAController: types.VirtualSATAController{
				VirtualController: types.VirtualController{
					VirtualDevice: types.VirtualDevice{
						Key: -105, // Use negative key for automatic assignment
					},
					BusNumber: 0,
				},
			},
		}
		devices = append(devices, sataController)

		// Create CD-ROM on SATA controller
		cdrom := &types.VirtualCdrom{
			VirtualDevice: types.VirtualDevice{
				Key:           -106, // Use negative key for automatic assignment
				ControllerKey: -105, // SATA controller
				UnitNumber:    types.NewInt32(0),
				Backing: &types.VirtualCdromIsoBackingInfo{
					VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
						FileName:  config.ISO,
						Datastore: &datastoreRef,
					},
				},
				Connectable: &types.VirtualDeviceConnectInfo{
					Connected:         true,
					StartConnected:    true,
					AllowGuestControl: true,
				},
			},
		}
		devices = append(devices, cdrom)
	}

	// Add precision clock device with NTP protocol
	if config.EnablePrecisionClock {
		precisionClock := &types.VirtualPrecisionClock{
			VirtualDevice: types.VirtualDevice{
				Key: -107, // Use negative key for automatic assignment
				Backing: &types.VirtualPrecisionClockSystemClockBackingInfo{
					Protocol: "ntp", // Set protocol to NTP as requested
				},
			},
		}
		devices = append(devices, precisionClock)
	}

	// Add watchdog timer device (set to start with BIOS/UEFI)
	if config.EnableWatchdog {
		watchdog := &types.VirtualWDT{
			VirtualDevice: types.VirtualDevice{
				Key: -108, // Use negative key for automatic assignment
			},
			RunOnBoot: true, // Start with BIOS/UEFI
		}
		devices = append(devices, watchdog)
	}

	// Add devices to spec
	var deviceChanges []types.BaseVirtualDeviceConfigSpec
	for _, device := range devices {
		deviceSpec := &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    device,
		}
		// For VirtualDisk devices, specify file operation to create new disk
		if _, isDisk := device.(*types.VirtualDisk); isDisk {
			deviceSpec.FileOperation = types.VirtualDeviceConfigSpecFileOperationCreate
		}
		deviceChanges = append(deviceChanges, deviceSpec)
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
	c.logger.Success("VM %s created successfully (with controllers)", config.Name)

	// PHASE 2: Add disks to the VM after controllers are created
	c.logger.Info("Adding disks to VM %s...", config.Name)

	// Get the actual controller keys from the created VM
	var vmInfo mo.VirtualMachine
	err = vm.Properties(c.ctx, vm.Reference(), []string{"config.hardware.device"}, &vmInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM properties: %w", err)
	}

	// Find the NVME controller keys
	var nvme0Key, nvme1Key int32
	for _, device := range vmInfo.Config.Hardware.Device {
		if ctrl, ok := device.(*types.VirtualNVMEController); ok {
			switch ctrl.BusNumber {
			case 0:
				nvme0Key = ctrl.Key
			case 1:
				nvme1Key = ctrl.Key
			}
		}
	}

	if nvme0Key == 0 || nvme1Key == 0 {
		return nil, fmt.Errorf("failed to find NVME controllers (nvme0: %d, nvme1: %d)", nvme0Key, nvme1Key)
	}

	c.logger.Debug("Found NVME controllers: nvme0=%d, nvme1=%d", nvme0Key, nvme1Key)

	// Build disk device changes
	var diskChanges []types.BaseVirtualDeviceConfigSpec

	// Create boot disk (500GB) on NVME controller 0
	bootDisk := &types.VirtualDisk{
		VirtualDevice: types.VirtualDevice{
			Key:           -1, // Use negative key for automatic assignment
			ControllerKey: nvme0Key,
			UnitNumber:    types.NewInt32(0),
			Backing: &types.VirtualDiskFlatVer2BackingInfo{
				VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
					FileName:  "", // Auto-generate in VM folder (follows govmomi pattern)
					Datastore: &datastoreRef,
				},
				DiskMode:        "persistent",
				ThinProvisioned: types.NewBool(true),
				EagerlyScrub:    types.NewBool(false),
			},
		},
		CapacityInKB: int64(config.DiskSize) * 1024 * 1024,
	}
	diskChanges = append(diskChanges, &types.VirtualDeviceConfigSpec{
		Operation:     types.VirtualDeviceConfigSpecOperationAdd,
		FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
		Device:        bootDisk,
	})

	// Create OpenEBS disk (1TB) on NVME controller 1 - for local storage
	if config.OpenEBSSize > 0 {
		openebsDisk := &types.VirtualDisk{
			VirtualDevice: types.VirtualDevice{
				Key:           -2,
				ControllerKey: nvme1Key,
				UnitNumber:    types.NewInt32(0),
				Backing: &types.VirtualDiskFlatVer2BackingInfo{
					VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
						FileName:  "", // Auto-generate in VM folder (follows govmomi pattern)
						Datastore: &datastoreRef,
					},
					DiskMode:        "persistent",
					ThinProvisioned: types.NewBool(true),
					EagerlyScrub:    types.NewBool(false),
				},
			},
			CapacityInKB: int64(config.OpenEBSSize) * 1024 * 1024,
		}
		diskChanges = append(diskChanges, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
			Device:        openebsDisk,
		})
	}

	// Reconfigure VM to add disks
	configSpec := types.VirtualMachineConfigSpec{
		DeviceChange: diskChanges,
	}

	task, err = vm.Reconfigure(c.ctx, configSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to add disks to VM: %w", err)
	}

	err = task.Wait(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to add disks to VM: %w", err)
	}

	c.logger.Success("Disks added successfully to VM %s", config.Name)

	// PHASE 2.5: Wait for vSphere to fully process VMDK files
	// vSphere needs time to complete background operations on newly created VMDK files
	// before they can be used for booting. This typically takes a few seconds.
	c.logger.Debug("Waiting for vSphere to process VMDK files...")
	time.Sleep(10 * time.Second)
	c.logger.Debug("Wait complete, proceeding with VM registration...")

	// PHASE 2.5: Unregister and re-register VM to fix VMDK descriptor adapter types
	// When vSphere re-registers a VM, it reads the VMX file and corrects VMDK descriptors
	// to match the controller types specified in the VMX
	c.logger.Debug("Re-registering VM to fix VMDK descriptor adapter types...")

	// Get the VM's VMX path before unregistering
	var vmConfig mo.VirtualMachine
	err = vm.Properties(c.ctx, vm.Reference(), []string{"config.files"}, &vmConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM VMX path: %w", err)
	}
	vmxPath := vmConfig.Config.Files.VmPathName
	c.logger.Debug("VMX path: %s", vmxPath)

	// Get the folder and resource pool for re-registration
	regFolders, err := c.datacenter.Folders(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get folders: %w", err)
	}

	regPool, err := c.finder.DefaultResourcePool(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find resource pool: %w", err)
	}

	// Unregister the VM
	c.logger.Debug("Unregistering VM from inventory...")
	err = vm.Unregister(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to unregister VM: %w", err)
	}

	// Re-register the VM (this fixes VMDK descriptors to match VMX controller types)
	c.logger.Debug("Re-registering VM to fix VMDK descriptors...")
	regTask, err := regFolders.VmFolder.RegisterVM(c.ctx, vmxPath, config.Name, false, regPool, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to re-register VM: %w", err)
	}

	regInfo, err := regTask.WaitForResult(c.ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for re-register task: %w", err)
	}

	vm = object.NewVirtualMachine(c.vim, regInfo.Result.(types.ManagedObjectReference))
	c.logger.Success("VM %s re-registered with corrected VMDK descriptors", config.Name)

	// PHASE 3: Power on VM if requested with retry logic
	if config.PowerOn {
		c.logger.Info("Powering on VM %s...", config.Name)

		// Retry power-on with exponential backoff if vSphere needs more time to process VMDK files
		// Observed behavior: VMs may need 5+ minutes after VMDK creation before successful power-on
		maxRetries := 3
		retryDelays := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}

		var lastErr error
		var powerSuccess bool

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				c.logger.Warn("Power-on attempt %d/%d failed: %v", attempt, maxRetries, lastErr)
				c.logger.Info("Waiting %v before retry (vSphere may need time to process VMDK files)...", retryDelays[attempt-1])
				time.Sleep(retryDelays[attempt-1])
				c.logger.Info("Retrying power-on (attempt %d/%d)...", attempt+1, maxRetries+1)
			}

			powerTask, err := vm.PowerOn(c.ctx)
			if err != nil {
				lastErr = fmt.Errorf("failed to start power-on: %w", err)
				continue
			}

			err = powerTask.Wait(c.ctx)
			if err != nil {
				lastErr = fmt.Errorf("power-on task failed: %w", err)
				continue
			}

			// Success!
			powerSuccess = true
			c.logger.Success("VM %s powered on successfully", config.Name)
			break
		}

		if !powerSuccess {
			return nil, fmt.Errorf("failed to power on VM after %d attempts (total wait time: ~%v): %w",
				maxRetries+1,
				10*time.Second+retryDelays[0]+retryDelays[1]+retryDelays[2],
				lastErr)
		}
	}

	return vm, nil
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

			// Create VM - note: CreateVM already handles power-on with retry logic
			// when cfg.PowerOn is true, so we don't need to call PowerOnVM separately
			_, err := c.CreateVM(cfg)
			if err != nil {
				errors <- fmt.Errorf("failed to create VM %s: %w", cfg.Name, err)
				return
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
