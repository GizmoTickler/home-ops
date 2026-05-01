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
	"github.com/vmware/govmomi/vim25/soap"
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

type lifecycleTask interface {
	Wait(context.Context) error
}

type vmLifecycle interface {
	PowerOn(context.Context) (lifecycleTask, error)
	PowerOff(context.Context) (lifecycleTask, error)
	Destroy(context.Context) (lifecycleTask, error)
	Properties(context.Context, types.ManagedObjectReference, []string, interface{}) error
	Reference() types.ManagedObjectReference
}

type datastoreUploader interface {
	UploadFile(context.Context, string, string, *soap.Upload) error
}

var (
	vsphereSleep             = time.Sleep
	get1PasswordSecretsBatch = common.Get1PasswordSecretsBatch
	newClientWithConnectFn   = NewClientWithConnect
	listVMNamesFn            = listVMNames
	listVMObjectsFn          = func(client *Client) ([]*object.VirtualMachine, error) { return client.ListVMs() }
	sshCombinedOutputFn      = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).CombinedOutput() }
	newGovmomiClientFn       = govmomi.NewClient
	newFinderFn              = func(client *vim25.Client) *find.Finder { return find.NewFinder(client, true) }
	defaultDatacenterFn      = func(ctx context.Context, finder *find.Finder) (*object.Datacenter, error) {
		return finder.DefaultDatacenter(ctx)
	}
	setFinderDatacenterFn = func(finder *find.Finder, datacenter *object.Datacenter) { finder.SetDatacenter(datacenter) }
	logoutVSphereClientFn = func(ctx context.Context, client *govmomi.Client) error { return client.Logout(ctx) }
	findVirtualMachineFn  = func(finder *find.Finder, ctx context.Context, name string) (*object.VirtualMachine, error) {
		return finder.VirtualMachine(ctx, name)
	}
	listVirtualMachinesFn = func(finder *find.Finder, ctx context.Context) ([]*object.VirtualMachine, error) {
		return finder.VirtualMachineList(ctx, "*")
	}
	getVMPropertiesFn = func(vm *object.VirtualMachine, ctx context.Context, ref types.ManagedObjectReference, props []string, dst interface{}) error {
		return vm.Properties(ctx, ref, props, dst)
	}
	findDatastoreFn = func(finder *find.Finder, ctx context.Context, name string) (datastoreUploader, error) {
		return finder.Datastore(ctx, name)
	}
	statFileFn            = os.Stat
	uploadDatastoreFileFn = func(datastore datastoreUploader, ctx context.Context, localFilePath, remoteFileName string) error {
		return datastore.UploadFile(ctx, localFilePath, remoteFileName, nil)
	}
	createVMForDeployFn = func(client *Client, config VMConfig) (*object.VirtualMachine, error) {
		return client.CreateVM(config)
	}
)

type objectTaskLifecycle struct {
	task *object.Task
}

func (t objectTaskLifecycle) Wait(ctx context.Context) error {
	_, err := t.task.WaitForResult(ctx, nil)
	return err
}

type objectVMLifecycle struct {
	vm *object.VirtualMachine
}

func (v objectVMLifecycle) PowerOn(ctx context.Context) (lifecycleTask, error) {
	task, err := v.vm.PowerOn(ctx)
	if err != nil {
		return nil, err
	}
	return objectTaskLifecycle{task: task}, nil
}

func (v objectVMLifecycle) PowerOff(ctx context.Context) (lifecycleTask, error) {
	task, err := v.vm.PowerOff(ctx)
	if err != nil {
		return nil, err
	}
	return objectTaskLifecycle{task: task}, nil
}

func (v objectVMLifecycle) Destroy(ctx context.Context) (lifecycleTask, error) {
	task, err := v.vm.Destroy(ctx)
	if err != nil {
		return nil, err
	}
	return objectTaskLifecycle{task: task}, nil
}

func (v objectVMLifecycle) Properties(ctx context.Context, ref types.ManagedObjectReference, props []string, dst interface{}) error {
	return v.vm.Properties(ctx, ref, props, dst)
}

func (v objectVMLifecycle) Reference() types.ManagedObjectReference {
	return v.vm.Reference()
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
	client, err := newGovmomiClientFn(c.ctx, u, insecure)
	if err != nil {
		return fmt.Errorf("failed to create vSphere client: %w", err)
	}

	c.client = client
	c.vim = client.Client
	c.finder = newFinderFn(c.vim)

	// Find datacenter (use default for standalone ESXi)
	datacenter, err := defaultDatacenterFn(c.ctx, c.finder)
	if err != nil {
		return fmt.Errorf("failed to find datacenter: %w", err)
	}
	c.datacenter = datacenter
	setFinderDatacenterFn(c.finder, datacenter)

	c.logger.Success("Connected to vSphere/ESXi: %s", host)
	return nil
}

// Close closes the vSphere connection
func (c *Client) Close() error {
	// Logout first before canceling context
	if c.client != nil {
		if err := logoutVSphereClientFn(c.ctx, c.client); err != nil {
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

	spec := buildInitialVMSpec(config)

	// Log IOMMU status
	if config.EnableIOMMU {
		c.logger.Debug("IOMMU/VT-d enabled for VM %s", config.Name)
	}

	datastoreRef := datastore.Reference()

	// Create vmxnet3 network adapter and set to vl999 portgroup
	backing, err := network.EthernetCardBackingInfo(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network backing: %w", err)
	}
	spec.DeviceChange = buildInitialDeviceChanges(config, datastoreRef, backing)

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

	nvme0Key, nvme1Key, err := findNVMEControllerKeys(vmInfo.Config.Hardware.Device)
	if err != nil {
		return nil, err
	}

	c.logger.Debug("Found NVME controllers: nvme0=%d, nvme1=%d", nvme0Key, nvme1Key)

	// Reconfigure VM to add disks
	configSpec := types.VirtualMachineConfigSpec{
		DeviceChange: buildDiskDeviceChanges(config, datastoreRef, nvme0Key, nvme1Key),
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
	vsphereSleep(10 * time.Second)
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
		if err := powerOnWithRetry(c.ctx, c.logger, objectVMLifecycle{vm: vm}, 3, []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}, config.Name); err != nil {
			return nil, err
		}
	}

	return vm, nil
}

// PowerOnVM powers on a VM
func (c *Client) PowerOnVM(vm *object.VirtualMachine) error {
	return c.powerOnVM(objectVMLifecycle{vm: vm})
}

func (c *Client) powerOnVM(vm vmLifecycle) error {
	task, err := vm.PowerOn(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to power on VM: %w", err)
	}

	if err := task.Wait(c.ctx); err != nil {
		return fmt.Errorf("failed to power on VM: %w", err)
	}

	c.logger.Success("VM powered on successfully")
	return nil
}

// PowerOffVM powers off a VM
func (c *Client) PowerOffVM(vm *object.VirtualMachine) error {
	return c.powerOffVM(objectVMLifecycle{vm: vm})
}

func (c *Client) powerOffVM(vm vmLifecycle) error {
	task, err := vm.PowerOff(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to power off VM: %w", err)
	}

	if err := task.Wait(c.ctx); err != nil {
		return fmt.Errorf("failed to power off VM: %w", err)
	}

	c.logger.Success("VM powered off successfully")
	return nil
}

// DeleteVM deletes a VM
func (c *Client) DeleteVM(vm *object.VirtualMachine) error {
	return c.deleteVM(objectVMLifecycle{vm: vm})
}

func (c *Client) deleteVM(vm vmLifecycle) error {
	// Power off if running
	var mvm mo.VirtualMachine
	propErr := vm.Properties(c.ctx, vm.Reference(), []string{"runtime.powerState"}, &mvm)
	if propErr == nil && mvm.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
		c.logger.Info("Powering off VM before deletion...")
		if err := c.powerOffVM(vm); err != nil {
			return fmt.Errorf("failed to power off running VM before deletion: %w", err)
		}
	}

	// Delete VM
	task, err := vm.Destroy(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	if err := task.Wait(c.ctx); err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	c.logger.Success("VM deleted successfully")
	return nil
}

// FindVM finds a VM by name
func (c *Client) FindVM(name string) (*object.VirtualMachine, error) {
	vm, err := findVirtualMachineFn(c.finder, c.ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to find VM %s: %w", name, err)
	}
	return vm, nil
}

// ListVMs lists all VMs
func (c *Client) ListVMs() ([]*object.VirtualMachine, error) {
	vms, err := listVirtualMachinesFn(c.finder, c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	return vms, nil
}

// GetVMInfo gets detailed VM information
func (c *Client) GetVMInfo(vm *object.VirtualMachine) (*mo.VirtualMachine, error) {
	var mvm mo.VirtualMachine
	err := getVMPropertiesFn(vm, c.ctx, vm.Reference(), nil, &mvm)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM properties: %w", err)
	}
	return &mvm, nil
}

// UploadISOToDatastore uploads an ISO file to a vSphere datastore
func (c *Client) UploadISOToDatastore(localFilePath, datastoreName, remoteFileName string) error {
	c.logger.Debug("Uploading ISO %s to datastore %s as %s", localFilePath, datastoreName, remoteFileName)

	// Find the datastore
	datastore, err := findDatastoreFn(c.finder, c.ctx, datastoreName)
	if err != nil {
		return fmt.Errorf("failed to find datastore %s: %w", datastoreName, err)
	}

	// Get file info for size logging
	fileInfo, err := statFileFn(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	c.logger.Info("Uploading %s (%d MB) to datastore...", remoteFileName, fileInfo.Size()/(1024*1024))

	// Upload file using datastore UploadFile method
	if err := uploadDatastoreFileFn(datastore, c.ctx, localFilePath, remoteFileName); err != nil {
		return fmt.Errorf("failed to upload file to datastore: %w", err)
	}

	c.logger.Success("ISO uploaded successfully to [%s] %s", datastoreName, remoteFileName)
	return nil
}

// DeployVMsConcurrently deploys multiple VMs in parallel
func (c *Client) DeployVMsConcurrently(configs []VMConfig) error {
	return deployVMsConcurrently(configs, c.logger, func(cfg VMConfig) error {
		_, err := createVMForDeployFn(c, cfg)
		return err
	})
}

func deployVMsConcurrently(configs []VMConfig, logger *common.ColorLogger, deployFn func(VMConfig) error) error {
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

			logger.Info("Starting deployment of VM: %s", cfg.Name)
			startTime := time.Now()

			if err := deployFn(cfg); err != nil {
				errors <- fmt.Errorf("failed to create VM %s: %w", cfg.Name, err)
				return
			}

			logger.Success("VM %s deployed in %v", cfg.Name, time.Since(startTime))
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
	host, username, password, usedEnvFallback := resolveVSphereCredentials()

	if host == "" || username == "" || password == "" {
		return nil, fmt.Errorf("vSphere credentials not found")
	}

	// Warn if using environment variables (less secure than 1Password)
	if usedEnvFallback {
		logger.Warn("Using environment variables for vSphere credentials. Consider using 1Password for better security.")
	}

	// Create vSphere client and connect
	client, err := newClientWithConnectFn(host, username, password, true)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() { _ = client.Close() }()

	// List VMs
	vmNames, err := listVMNamesFn(client)
	if err != nil {
		return nil, err
	}

	return vmNames, nil
}

// ESXiSSHClient wraps SSH operations for ESXi
type ESXiSSHClient struct {
	host     string
	username string
	keyFile  string // Path to temporary key file
	logger   *common.ColorLogger
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
		_ = os.Remove(keyFile.Name())
		return nil, fmt.Errorf("failed to set SSH key permissions: %w", err)
	}

	// Write the key content
	if _, err := keyFile.WriteString(privateKey); err != nil {
		_ = os.Remove(keyFile.Name())
		return nil, fmt.Errorf("failed to write SSH key: %w", err)
	}
	_ = keyFile.Close()

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
		_ = os.Remove(c.keyFile)
		c.logger.Debug("Cleaned up SSH key file")
	}
}

// ExecuteCommand executes a command on ESXi via SSH using the 1Password key
func (c *ESXiSSHClient) ExecuteCommand(command string) (string, error) {
	c.logger.Debug("Executing ESXi command: %s", command)

	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-i", c.keyFile,
		fmt.Sprintf("%s@%s", c.username, c.host),
		command,
	}

	output, err := sshCombinedOutputFn("ssh", args...)
	if err != nil {
		redactedOutput := common.RedactCommandOutput(string(output))
		return redactedOutput, fmt.Errorf("SSH command failed: %w\nOutput: %s", err, redactedOutput)
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
	if _, err := c.ExecuteCommand(fmt.Sprintf("mkdir -p %s", shellQuote(vmDir))); err != nil {
		return fmt.Errorf("failed to create VM directory: %w", err)
	}

	// Step 2: Create OpenEBS directory on truenas-iscsi
	openebsDir := fmt.Sprintf("/vmfs/volumes/%s/%s", config.OpenEBSDatastore, config.Name)
	c.logger.Info("Creating OpenEBS directory: %s", openebsDir)
	if _, err := c.ExecuteCommand(fmt.Sprintf("mkdir -p %s", shellQuote(openebsDir))); err != nil {
		return fmt.Errorf("failed to create OpenEBS directory: %w", err)
	}

	// Step 3: Create boot disk VMDK
	bootVMDK := fmt.Sprintf("%s/%s.vmdk", vmDir, config.Name)
	c.logger.Info("Creating boot disk: %s (%dGB)", bootVMDK, config.DiskSize)
	createBootDisk := fmt.Sprintf("vmkfstools -c %dG -d thin %s", config.DiskSize, shellQuote(bootVMDK))
	if _, err := c.ExecuteCommand(createBootDisk); err != nil {
		return fmt.Errorf("failed to create boot disk: %w", err)
	}

	// Step 4: Create OpenEBS disk VMDK
	openebsVMDK := fmt.Sprintf("%s/%s.vmdk", openebsDir, config.Name)
	c.logger.Info("Creating OpenEBS disk: %s (%dGB)", openebsVMDK, config.OpenEBSSize)
	createOpenEBSDisk := fmt.Sprintf("vmkfstools -c %dG -d thin %s", config.OpenEBSSize, shellQuote(openebsVMDK))
	if _, err := c.ExecuteCommand(createOpenEBSDisk); err != nil {
		return fmt.Errorf("failed to create OpenEBS disk: %w", err)
	}

	// Step 5: Generate VMX file
	vmxPath := fmt.Sprintf("%s/%s.vmx", vmDir, config.Name)
	vmxContent := c.generateK8sVMX(config, vmDir, openebsDir)
	c.logger.Info("Writing VMX file: %s", vmxPath)

	// Write VMX file via SSH using heredoc
	writeVMXCmd := fmt.Sprintf("cat > %s << 'VMXEOF'\n%s\nVMXEOF", shellQuote(vmxPath), vmxContent)
	if _, err := c.ExecuteCommand(writeVMXCmd); err != nil {
		return fmt.Errorf("failed to write VMX file: %w", err)
	}

	// Step 6: Register VM
	c.logger.Info("Registering VM...")
	registerCmd := fmt.Sprintf("vim-cmd solo/registervm %s", shellQuote(vmxPath))
	output, err := c.ExecuteCommand(registerCmd)
	if err != nil {
		return fmt.Errorf("failed to register VM: %w", err)
	}
	vmID, err := parseRegisteredVMID(output)
	if err != nil {
		return fmt.Errorf("failed to parse registered VM ID: %w", err)
	}
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

func buildInitialVMSpec(config VMConfig) types.VirtualMachineConfigSpec {
	return types.VirtualMachineConfigSpec{
		Name:     config.Name,
		GuestId:  "other6xLinux64Guest",
		NumCPUs:  int32(config.VCPUs),
		MemoryMB: int64(config.Memory),
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s] %s", config.Datastore, config.Name),
		},
		Firmware: "efi",
		BootOptions: &types.VirtualMachineBootOptions{
			EfiSecureBootEnabled: types.NewBool(false),
		},
		Flags: &types.VirtualMachineFlagInfo{
			VirtualMmuUsage:  "automatic",
			VirtualExecUsage: "hvAuto",
			VvtdEnabled:      types.NewBool(config.EnableIOMMU),
		},
		VPMCEnabled: types.NewBool(config.ExposeCounters),
		ExtraConfig: buildExtraConfig(config),
		Tools: &types.ToolsConfigInfo{
			SyncTimeWithHost: types.NewBool(true),
		},
	}
}

func buildExtraConfig(config VMConfig) []types.BaseOptionValue {
	extraConfig := []types.BaseOptionValue{
		&types.OptionValue{Key: "disk.EnableUUID", Value: "TRUE"},
	}
	if config.ExposeCounters {
		extraConfig = append(extraConfig, &types.OptionValue{Key: "monitor.phys_bits_used", Value: "45"})
	}
	return extraConfig
}

func buildInitialDeviceChanges(config VMConfig, datastoreRef types.ManagedObjectReference, backing types.BaseVirtualDeviceBackingInfo) []types.BaseVirtualDeviceConfigSpec {
	return buildDeviceChangeSpecs(buildInitialDevices(config, datastoreRef, backing))
}

func buildInitialDevices(config VMConfig, datastoreRef types.ManagedObjectReference, backing types.BaseVirtualDeviceBackingInfo) []types.BaseVirtualDevice {
	devices := []types.BaseVirtualDevice{
		&types.VirtualNVMEController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{Key: -100},
				BusNumber:     0,
			},
		},
		&types.VirtualNVMEController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{Key: -101},
				BusNumber:     1,
			},
		},
		buildVmxnet3Device(config, backing),
	}

	if config.ISO != "" {
		devices = append(devices,
			&types.VirtualAHCIController{
				VirtualSATAController: types.VirtualSATAController{
					VirtualController: types.VirtualController{
						VirtualDevice: types.VirtualDevice{Key: -105},
						BusNumber:     0,
					},
				},
			},
			&types.VirtualCdrom{
				VirtualDevice: types.VirtualDevice{
					Key:           -106,
					ControllerKey: -105,
					UnitNumber:    types.NewInt32(0),
					Backing: &types.VirtualCdromIsoBackingInfo{
						VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
							FileName:  config.ISO,
							Datastore: &datastoreRef,
						},
					},
					Connectable: connectedDeviceInfo(),
				},
			},
		)
	}

	if config.EnablePrecisionClock {
		devices = append(devices, &types.VirtualPrecisionClock{
			VirtualDevice: types.VirtualDevice{
				Key: -107,
				Backing: &types.VirtualPrecisionClockSystemClockBackingInfo{
					Protocol: "ntp",
				},
			},
		})
	}

	if config.EnableWatchdog {
		devices = append(devices, &types.VirtualWDT{
			VirtualDevice: types.VirtualDevice{Key: -108},
			RunOnBoot:     true,
		})
	}

	return devices
}

func buildVmxnet3Device(config VMConfig, backing types.BaseVirtualDeviceBackingInfo) *types.VirtualVmxnet3 {
	netDevice := &types.VirtualVmxnet3{
		VirtualVmxnet: types.VirtualVmxnet{
			VirtualEthernetCard: types.VirtualEthernetCard{
				VirtualDevice: types.VirtualDevice{
					Key:         -104,
					Backing:     backing,
					Connectable: connectedDeviceInfo(),
				},
				AddressType: "generated",
			},
		},
	}
	if config.MacAddress != "" {
		netDevice.AddressType = "manual"
		netDevice.MacAddress = config.MacAddress
	}
	return netDevice
}

func connectedDeviceInfo() *types.VirtualDeviceConnectInfo {
	return &types.VirtualDeviceConnectInfo{
		Connected:         true,
		StartConnected:    true,
		AllowGuestControl: true,
	}
}

func buildDeviceChangeSpecs(devices []types.BaseVirtualDevice) []types.BaseVirtualDeviceConfigSpec {
	deviceChanges := make([]types.BaseVirtualDeviceConfigSpec, 0, len(devices))
	for _, device := range devices {
		deviceSpec := &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    device,
		}
		if _, isDisk := device.(*types.VirtualDisk); isDisk {
			deviceSpec.FileOperation = types.VirtualDeviceConfigSpecFileOperationCreate
		}
		deviceChanges = append(deviceChanges, deviceSpec)
	}
	return deviceChanges
}

func findNVMEControllerKeys(devices []types.BaseVirtualDevice) (int32, int32, error) {
	var nvme0Key, nvme1Key int32
	for _, device := range devices {
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
		return 0, 0, fmt.Errorf("failed to find NVME controllers (nvme0: %d, nvme1: %d)", nvme0Key, nvme1Key)
	}
	return nvme0Key, nvme1Key, nil
}

func buildDiskDeviceChanges(config VMConfig, datastoreRef types.ManagedObjectReference, nvme0Key, nvme1Key int32) []types.BaseVirtualDeviceConfigSpec {
	diskChanges := []types.BaseVirtualDeviceConfigSpec{
		&types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
			Device: &types.VirtualDisk{
				VirtualDevice: types.VirtualDevice{
					Key:           -1,
					ControllerKey: nvme0Key,
					UnitNumber:    types.NewInt32(0),
					Backing: &types.VirtualDiskFlatVer2BackingInfo{
						VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
							FileName:  "",
							Datastore: &datastoreRef,
						},
						DiskMode:        "persistent",
						ThinProvisioned: types.NewBool(true),
						EagerlyScrub:    types.NewBool(false),
					},
				},
				CapacityInKB: int64(config.DiskSize) * 1024 * 1024,
			},
		},
	}

	if config.OpenEBSSize > 0 {
		diskChanges = append(diskChanges, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
			Device: &types.VirtualDisk{
				VirtualDevice: types.VirtualDevice{
					Key:           -2,
					ControllerKey: nvme1Key,
					UnitNumber:    types.NewInt32(0),
					Backing: &types.VirtualDiskFlatVer2BackingInfo{
						VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
							FileName:  "",
							Datastore: &datastoreRef,
						},
						DiskMode:        "persistent",
						ThinProvisioned: types.NewBool(true),
						EagerlyScrub:    types.NewBool(false),
					},
				},
				CapacityInKB: int64(config.OpenEBSSize) * 1024 * 1024,
			},
		})
	}

	return diskChanges
}

func powerOnWithRetry(ctx context.Context, logger *common.ColorLogger, vm vmLifecycle, maxRetries int, retryDelays []time.Duration, vmName string) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			logger.Warn("Power-on attempt %d/%d failed: %v", attempt, maxRetries, lastErr)
			logger.Info("Waiting %v before retry (vSphere may need time to process VMDK files)...", retryDelays[attempt-1])
			vsphereSleep(retryDelays[attempt-1])
			logger.Info("Retrying power-on (attempt %d/%d)...", attempt+1, maxRetries+1)
		}

		task, err := vm.PowerOn(ctx)
		if err != nil {
			lastErr = fmt.Errorf("failed to start power-on: %w", err)
			continue
		}

		if err := task.Wait(ctx); err != nil {
			lastErr = fmt.Errorf("power-on task failed: %w", err)
			continue
		}

		logger.Success("VM %s powered on successfully", vmName)
		return nil
	}

	return fmt.Errorf("failed to power on VM after %d attempts (total wait time: ~%v): %w",
		maxRetries+1,
		10*time.Second+totalRetryDelay(retryDelays),
		lastErr,
	)
}

func totalRetryDelay(delays []time.Duration) time.Duration {
	var total time.Duration
	for _, delay := range delays {
		total += delay
	}
	return total
}

func listVMNames(client *Client) ([]string, error) {
	vmObjects, err := listVMObjectsFn(client)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	if len(vmObjects) == 0 {
		return nil, fmt.Errorf("no VMs found on vSphere/ESXi")
	}

	vmNames := make([]string, 0, len(vmObjects))
	for _, vm := range vmObjects {
		vmNames = append(vmNames, vm.Name())
	}
	if len(vmNames) == 0 {
		return nil, fmt.Errorf("failed to extract VM names from vSphere/ESXi")
	}

	return vmNames, nil
}

func resolveVSphereCredentials() (host, username, password string, usedEnvFallback bool) {
	secrets := get1PasswordSecretsBatch([]string{
		constants.OpESXiHost,
		constants.OpESXiUsername,
		constants.OpESXiPassword,
	})
	host = secrets[constants.OpESXiHost]
	username = secrets[constants.OpESXiUsername]
	password = secrets[constants.OpESXiPassword]

	if host == "" {
		host = os.Getenv(constants.EnvVSphereHost)
		usedEnvFallback = usedEnvFallback || host != ""
	}
	if username == "" {
		username = os.Getenv(constants.EnvVSphereUsername)
		usedEnvFallback = usedEnvFallback || username != ""
	}
	if password == "" {
		password = os.Getenv(constants.EnvVSpherePassword)
		usedEnvFallback = usedEnvFallback || password != ""
	}

	return host, username, password, usedEnvFallback
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func parseRegisteredVMID(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", fmt.Errorf("empty VM registration output")
	}
	for _, line := range strings.Split(trimmed, "\n") {
		for _, field := range strings.Fields(line) {
			candidate := strings.Trim(field, ":")
			if isDigits(candidate) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("unable to find numeric VM ID in output: %s", common.RedactCommandOutput(trimmed))
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
