package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"

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

	// Create boot disk (250GB) on NVME controller 0
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

// GetVMNames retrieves the list of VM names from ESXi/vSphere
func GetVMNames() ([]string, error) {
	logger := common.NewColorLogger()
	usedEnvFallback := false

	// Get vSphere credentials - batch lookup from 1Password for better performance
	secrets := common.Get1PasswordSecretsBatch([]string{
		constants.OpESXiHost,
		constants.OpESXiUsername,
		constants.OpESXiPassword,
	})
	host := secrets[constants.OpESXiHost]
	username := secrets[constants.OpESXiUsername]
	password := secrets[constants.OpESXiPassword]

	// Fall back to environment variables if 1Password fails
	if host == "" {
		host = os.Getenv(constants.EnvVSphereHost)
		if host != "" {
			usedEnvFallback = true
		}
	}
	if username == "" {
		username = os.Getenv(constants.EnvVSphereUsername)
		if username != "" {
			usedEnvFallback = true
		}
	}
	if password == "" {
		password = os.Getenv(constants.EnvVSpherePassword)
		if password != "" {
			usedEnvFallback = true
		}
	}

	if host == "" || username == "" || password == "" {
		return nil, fmt.Errorf("vSphere credentials not found")
	}

	// Warn if using environment variables (less secure than 1Password)
	if usedEnvFallback {
		logger.Warn("Using environment variables for vSphere credentials. Consider using 1Password for better security.")
	}

	// Create vSphere client and connect
	client, err := NewClientWithConnect(host, username, password, true)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() { _ = client.Close() }()

	// List VMs
	vmObjects, err := client.ListVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}

	if len(vmObjects) == 0 {
		return nil, fmt.Errorf("no VMs found on vSphere/ESXi")
	}

	// Extract VM names from VM objects
	vmNames := make([]string, 0, len(vmObjects))
	for _, vm := range vmObjects {
		// VM objects have a Name() method that returns the VM name
		vmNames = append(vmNames, vm.Name())
	}

	if len(vmNames) == 0 {
		return nil, fmt.Errorf("failed to extract VM names from vSphere/ESXi")
	}

	return vmNames, nil
}

// ESXiSSHClient wraps SSH operations for ESXi
type ESXiSSHClient struct {
	host        string
	username    string
	keyFile     string // Path to temporary key file
	logger      *common.ColorLogger
}

// NewESXiSSHClient creates a new ESXi SSH client with 1Password key retrieval
func NewESXiSSHClient(host, username string) (*ESXiSSHClient, error) {
	logger := common.NewColorLogger()

	// Fetch SSH private key from 1Password
	logger.Debug("Fetching ESXi SSH key from 1Password...")
	privateKey, err := common.Get1PasswordSecret(constants.OpESXiSSHPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get ESXi SSH key from 1Password: %w", err)
	}

	// Write key to temporary file with proper permissions
	keyFile, err := os.CreateTemp("", "esxi-ssh-key-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for SSH key: %w", err)
	}

	// Set restrictive permissions (600) before writing
	if err := keyFile.Chmod(0600); err != nil {
		os.Remove(keyFile.Name())
		return nil, fmt.Errorf("failed to set SSH key permissions: %w", err)
	}

	// Write the key content
	if _, err := keyFile.WriteString(privateKey); err != nil {
		os.Remove(keyFile.Name())
		return nil, fmt.Errorf("failed to write SSH key: %w", err)
	}
	keyFile.Close()

	logger.Debug("ESXi SSH key written to %s", keyFile.Name())

	return &ESXiSSHClient{
		host:     host,
		username: username,
		keyFile:  keyFile.Name(),
		logger:   logger,
	}, nil
}

// Close cleans up the temporary SSH key file
func (c *ESXiSSHClient) Close() {
	if c.keyFile != "" {
		os.Remove(c.keyFile)
		c.logger.Debug("Cleaned up SSH key file")
	}
}

// ExecuteCommand executes a command on ESXi via SSH using the 1Password key
func (c *ESXiSSHClient) ExecuteCommand(command string) (string, error) {
	c.logger.Debug("Executing ESXi command: %s", command)

	cmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-i", c.keyFile,
		fmt.Sprintf("%s@%s", c.username, c.host),
		command)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("SSH command failed: %w\nOutput: %s", err, string(output))
	}

	return string(output), nil
}

// CreateK8sVM creates a k8s VM on ESXi using SSH for exact configuration control
// This method ensures the VM matches the existing manually-deployed VMs exactly
func (c *ESXiSSHClient) CreateK8sVM(config VMConfig) error {
	c.logger.Info("Creating k8s VM %s via SSH with production configuration", config.Name)

	// Validate k8s node config exists
	nodeConfig, exists := GetK8sNodeConfig(config.Name)
	if !exists {
		return fmt.Errorf("no predefined configuration for k8s node: %s", config.Name)
	}

	// Merge node-specific config
	config.RDMPath = nodeConfig.RDMPath
	config.PCIDevice = nodeConfig.PCIDevice
	config.PCIDeviceHex = nodeConfig.PCIDeviceHex
	config.MacAddress = nodeConfig.MacAddress
	config.CPUAffinity = nodeConfig.CPUAffinity
	config.BootDatastore = nodeConfig.BootDatastore

	// Step 1: Create VM directory on boot datastore
	vmDir := fmt.Sprintf("/vmfs/volumes/%s/%s", config.BootDatastore, config.Name)
	c.logger.Info("Creating VM directory: %s", vmDir)
	if _, err := c.ExecuteCommand(fmt.Sprintf("mkdir -p %s", vmDir)); err != nil {
		return fmt.Errorf("failed to create VM directory: %w", err)
	}

	// Step 2: Create OpenEBS directory on truenas-iscsi
	openebsDir := fmt.Sprintf("/vmfs/volumes/%s/%s", config.OpenEBSDatastore, config.Name)
	c.logger.Info("Creating OpenEBS directory: %s", openebsDir)
	if _, err := c.ExecuteCommand(fmt.Sprintf("mkdir -p %s", openebsDir)); err != nil {
		return fmt.Errorf("failed to create OpenEBS directory: %w", err)
	}

	// Step 3: Create boot disk VMDK
	bootVMDK := fmt.Sprintf("%s/%s.vmdk", vmDir, config.Name)
	c.logger.Info("Creating boot disk: %s (%dGB)", bootVMDK, config.DiskSize)
	createBootDisk := fmt.Sprintf("vmkfstools -c %dG -d thin %s", config.DiskSize, bootVMDK)
	if _, err := c.ExecuteCommand(createBootDisk); err != nil {
		return fmt.Errorf("failed to create boot disk: %w", err)
	}

	// Step 4: Create OpenEBS disk VMDK
	openebsVMDK := fmt.Sprintf("%s/%s.vmdk", openebsDir, config.Name)
	c.logger.Info("Creating OpenEBS disk: %s (%dGB)", openebsVMDK, config.OpenEBSSize)
	createOpenEBSDisk := fmt.Sprintf("vmkfstools -c %dG -d thin %s", config.OpenEBSSize, openebsVMDK)
	if _, err := c.ExecuteCommand(createOpenEBSDisk); err != nil {
		return fmt.Errorf("failed to create OpenEBS disk: %w", err)
	}

	// Step 5: Generate VMX file
	vmxPath := fmt.Sprintf("%s/%s.vmx", vmDir, config.Name)
	vmxContent := c.generateK8sVMX(config, vmDir, openebsDir)
	c.logger.Info("Writing VMX file: %s", vmxPath)

	// Write VMX file via SSH using heredoc
	writeVMXCmd := fmt.Sprintf("cat > %s << 'VMXEOF'\n%s\nVMXEOF", vmxPath, vmxContent)
	if _, err := c.ExecuteCommand(writeVMXCmd); err != nil {
		return fmt.Errorf("failed to write VMX file: %w", err)
	}

	// Step 6: Register VM
	c.logger.Info("Registering VM...")
	registerCmd := fmt.Sprintf("vim-cmd solo/registervm %s", vmxPath)
	output, err := c.ExecuteCommand(registerCmd)
	if err != nil {
		return fmt.Errorf("failed to register VM: %w", err)
	}
	vmID := strings.TrimSpace(output)
	c.logger.Info("VM registered with ID: %s", vmID)

	// Step 7: Power on VM if requested
	if config.PowerOn {
		c.logger.Info("Powering on VM...")
		powerOnCmd := fmt.Sprintf("vim-cmd vmsvc/power.on %s", vmID)
		if _, err := c.ExecuteCommand(powerOnCmd); err != nil {
			return fmt.Errorf("failed to power on VM: %w", err)
		}
		c.logger.Success("VM %s powered on successfully", config.Name)
	}

	c.logger.Success("K8s VM %s created successfully with production configuration!", config.Name)
	return nil
}

// generateK8sVMX generates a VMX file content that exactly matches the production VMs
func (c *ESXiSSHClient) generateK8sVMX(config VMConfig, vmDir, openebsDir string) string {
	// Calculate datastore UUIDs from paths (these are looked up dynamically)
	// For now, use the datastore names directly in paths

	vmx := fmt.Sprintf(`.encoding = "UTF-8"
config.version = "8"
virtualHW.version = "21"
vmci0.present = "TRUE"
floppy0.present = "FALSE"
numvcpus = "%d"
memSize = "%d"
bios.bootRetry.delay = "10"
firmware = "efi"
powerType.suspend = "soft"
tools.upgrade.policy = "manual"
sched.cpu.units = "mhz"
sched.cpu.affinity = "%s"
scsi0.virtualDev = "pvscsi"
scsi0.present = "TRUE"
sata0.present = "TRUE"
usb.present = "TRUE"
ehci.present = "TRUE"
vwdt.present = "TRUE"
vwdt.runOnBoot = "TRUE"
precisionclock0.refClockProtocol = "ntp"
precisionclock0.present = "TRUE"
scsi0:0.deviceType = "scsi-hardDisk"
scsi0:0.fileName = "%s"
scsi0:0.mode = "independent-persistent"
sched.scsi0:0.shares = "normal"
sched.scsi0:0.throughputCap = "off"
scsi0:0.present = "TRUE"
nvme0.present = "TRUE"
nvme0:0.fileName = "%s.vmdk"
sched.nvme0:0.shares = "normal"
sched.nvme0:0.throughputCap = "off"
nvme0:0.present = "TRUE"
nvme1.present = "TRUE"
nvme1:0.fileName = "%s/%s.vmdk"
nvme1:0.mode = "independent-persistent"
sched.nvme1:0.shares = "normal"
sched.nvme1:0.throughputCap = "off"
nvme1:0.present = "TRUE"
sata0:0.deviceType = "cdrom-image"
sata0:0.fileName = "%s"
sata0:0.present = "TRUE"
displayName = "%s"
guestOS = "other6xlinux-64"
chipset.motherboardLayout = "acpi"
toolScripts.afterPowerOn = "TRUE"
toolScripts.afterResume = "TRUE"
toolScripts.beforeSuspend = "TRUE"
toolScripts.beforePowerOff = "TRUE"
tools.syncTime = "FALSE"
sched.cpu.min = "0"
sched.cpu.shares = "normal"
sched.mem.min = "%d"
sched.mem.minSize = "%d"
sched.mem.shares = "normal"
sched.mem.pin = "TRUE"
bios.bootOrder = "hdd,cdrom"
pciPassthru31.MACAddressType = "static"
pciPassthru31.MACAddress = "%s"
pciPassthru31.networkName = "%s"
pciPassthru31.pfId = "%s"
pciPassthru31.deviceId = "0"
pciPassthru31.vendorId = "0"
pciPassthru31.systemId = "BYPASS"
pciPassthru31.id = "%s"
pciPassthru31.present = "TRUE"
pciPassthru31.pxm = "0"
pciPassthru31.pciSlotNumber = "64"
cpuid.coresPerSocket = "1"
nvram = "%s.nvram"
svga.present = "TRUE"
hpet0.present = "TRUE"
RemoteDisplay.maxConnections = "-1"
sched.cpu.latencySensitivity = "normal"
svga.autodetect = "TRUE"
tools.guest.desktop.autolock = "TRUE"
disk.EnableUUID = "TRUE"
pciBridge1.present = "TRUE"
pciBridge1.virtualDev = "pciRootBridge"
pciBridge1.functions = "2"
pciBridge1:0.pxm = "0"
pciBridge1:1.pxm = "-1"
pciBridge0.present = "TRUE"
pciBridge0.virtualDev = "pciRootBridge"
pciBridge0.functions = "1"
pciBridge0.pxm = "-1"
scsi0.pciSlotNumber = "32"
usb.pciSlotNumber = "33"
ethernet0.pciSlotNumber = "-1"
ehci.pciSlotNumber = "35"
sata0.pciSlotNumber = "36"
nvme0.pciSlotNumber = "37"
nvme1.pciSlotNumber = "38"
monitor.phys_bits_used = "45"
softPowerOff = "FALSE"
usb:1.speed = "2"
usb:1.present = "TRUE"
usb:1.deviceType = "hub"
usb:1.port = "1"
usb:1.parent = "-1"
tools.remindInstall = "FALSE"
svga.vramSize = "16777216"
usb:0.present = "TRUE"
usb:0.deviceType = "hid"
usb:0.port = "0"
usb:0.parent = "-1"
`,
		config.VCPUs,
		config.Memory,
		config.CPUAffinity,
		config.RDMPath,
		config.Name,
		openebsDir, config.Name,
		config.ISO,
		config.Name,
		config.Memory, config.Memory,
		config.MacAddress,
		config.Network,
		config.PCIDeviceHex,
		config.PCIDeviceHex,
		config.Name,
	)

	return vmx
}
