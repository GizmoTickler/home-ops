package vsphere

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

type fakePowerOnVM struct {
	powerOnResults []powerOnResult
	powerOnCalls   int
}

type powerOnResult struct {
	task lifecycleTask
	err  error
}

type fakeDatastoreUploader struct {
	uploads []uploadCall
	err     error
}

type uploadCall struct {
	local  string
	remote string
}

func (f *fakeDatastoreUploader) UploadFile(_ context.Context, localFilePath, remoteFileName string, _ *soap.Upload) error {
	f.uploads = append(f.uploads, uploadCall{local: localFilePath, remote: remoteFileName})
	return f.err
}

func (f *fakePowerOnVM) PowerOn(context.Context) (lifecycleTask, error) {
	if f.powerOnCalls >= len(f.powerOnResults) {
		f.powerOnCalls++
		return nil, fmt.Errorf("unexpected power on call %d", f.powerOnCalls)
	}
	result := f.powerOnResults[f.powerOnCalls]
	f.powerOnCalls++
	return result.task, result.err
}

func (f *fakePowerOnVM) PowerOff(context.Context) (lifecycleTask, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePowerOnVM) Destroy(context.Context) (lifecycleTask, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePowerOnVM) Properties(context.Context, types.ManagedObjectReference, []string, interface{}) error {
	return errors.New("not implemented")
}

func (f *fakePowerOnVM) Reference() types.ManagedObjectReference {
	return types.ManagedObjectReference{}
}

func TestBuildInitialVMSpecAndDevices(t *testing.T) {
	config := VMConfig{
		Name:                 "k8s-0",
		Memory:               49152,
		VCPUs:                16,
		Datastore:            "local-nvme1",
		ISO:                  "[datastore1] vmware-amd64.iso",
		MacAddress:           "00:11:22:33:44:55",
		EnableIOMMU:          true,
		ExposeCounters:       true,
		EnablePrecisionClock: true,
		EnableWatchdog:       true,
	}
	datastoreRef := types.ManagedObjectReference{Type: "Datastore", Value: "ds-1"}
	backing := &types.VirtualEthernetCardNetworkBackingInfo{}

	spec := buildInitialVMSpec(config)
	require.Equal(t, "k8s-0", spec.Name)
	require.Equal(t, int32(16), spec.NumCPUs)
	require.Equal(t, int64(49152), spec.MemoryMB)
	require.Len(t, spec.ExtraConfig, 2)
	require.True(t, *spec.Flags.VvtdEnabled)
	require.True(t, *spec.VPMCEnabled)

	deviceChanges := buildInitialDeviceChanges(config, datastoreRef, backing)
	require.Len(t, deviceChanges, 7)

	var seenNet, seenCDROM, seenClock, seenWatchdog bool
	for _, change := range deviceChanges {
		spec, ok := change.(*types.VirtualDeviceConfigSpec)
		require.True(t, ok)
		require.Equal(t, types.VirtualDeviceConfigSpecOperationAdd, spec.Operation)

		switch device := spec.Device.(type) {
		case *types.VirtualVmxnet3:
			seenNet = true
			assert.Equal(t, "manual", device.AddressType)
			assert.Equal(t, "00:11:22:33:44:55", device.MacAddress)
		case *types.VirtualCdrom:
			seenCDROM = true
			backing, ok := device.Backing.(*types.VirtualCdromIsoBackingInfo)
			require.True(t, ok)
			assert.Equal(t, "[datastore1] vmware-amd64.iso", backing.FileName)
		case *types.VirtualPrecisionClock:
			seenClock = true
		case *types.VirtualWDT:
			seenWatchdog = true
		}
	}

	assert.True(t, seenNet)
	assert.True(t, seenCDROM)
	assert.True(t, seenClock)
	assert.True(t, seenWatchdog)
}

func TestFindNVMEControllerKeysAndDiskChanges(t *testing.T) {
	devices := []types.BaseVirtualDevice{
		&types.VirtualNVMEController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{Key: 201},
				BusNumber:     1,
			},
		},
		&types.VirtualNVMEController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{Key: 200},
				BusNumber:     0,
			},
		},
	}

	nvme0, nvme1, err := findNVMEControllerKeys(devices)
	require.NoError(t, err)
	assert.Equal(t, int32(200), nvme0)
	assert.Equal(t, int32(201), nvme1)

	_, _, err = findNVMEControllerKeys([]types.BaseVirtualDevice{
		&types.VirtualNVMEController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{Key: 200},
				BusNumber:     0,
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to find NVME controllers")

	config := VMConfig{DiskSize: 250, OpenEBSSize: 800}
	datastoreRef := types.ManagedObjectReference{Type: "Datastore", Value: "ds-1"}
	changes := buildDiskDeviceChanges(config, datastoreRef, 200, 201)
	require.Len(t, changes, 2)

	bootSpec := changes[0].(*types.VirtualDeviceConfigSpec)
	bootDisk := bootSpec.Device.(*types.VirtualDisk)
	assert.Equal(t, int32(200), bootDisk.ControllerKey)
	assert.Equal(t, int64(250*1024*1024), bootDisk.CapacityInKB)
	assert.Equal(t, types.VirtualDeviceConfigSpecFileOperationCreate, bootSpec.FileOperation)

	config.OpenEBSSize = 0
	changes = buildDiskDeviceChanges(config, datastoreRef, 200, 201)
	require.Len(t, changes, 1)
}

func TestPowerOnWithRetry(t *testing.T) {
	originalSleep := vsphereSleep
	t.Cleanup(func() { vsphereSleep = originalSleep })

	t.Run("retries and succeeds", func(t *testing.T) {
		var sleeps []time.Duration
		vsphereSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		vm := &fakePowerOnVM{
			powerOnResults: []powerOnResult{
				{err: errors.New("busy")},
				{task: &fakeLifecycleTask{}},
			},
		}

		err := powerOnWithRetry(context.Background(), common.NewColorLogger(), vm, 1, []time.Duration{time.Second}, "k8s-0")
		require.NoError(t, err)
		assert.Equal(t, 2, vm.powerOnCalls)
		assert.Equal(t, []time.Duration{time.Second}, sleeps)
	})

	t.Run("fails after all retries", func(t *testing.T) {
		var sleeps []time.Duration
		vsphereSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		vm := &fakePowerOnVM{
			powerOnResults: []powerOnResult{
				{err: errors.New("busy")},
				{task: &fakeLifecycleTask{waitErr: errors.New("still busy")}},
				{err: errors.New("busy again")},
			},
		}

		err := powerOnWithRetry(context.Background(), common.NewColorLogger(), vm, 2, []time.Duration{time.Second, 2 * time.Second}, "k8s-0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to power on VM after 3 attempts")
		assert.Contains(t, err.Error(), "busy again")
		assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, sleeps)
	})
}

func TestDeployVMsConcurrently(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var (
			mu    sync.Mutex
			names []string
		)

		err := deployVMsConcurrently([]VMConfig{{Name: "a"}, {Name: "b"}, {Name: "c"}}, common.NewColorLogger(), func(cfg VMConfig) error {
			mu.Lock()
			names = append(names, cfg.Name)
			mu.Unlock()
			return nil
		})
		require.NoError(t, err)
		assert.Len(t, names, 3)
	})

	t.Run("aggregates failures", func(t *testing.T) {
		err := deployVMsConcurrently([]VMConfig{{Name: "a"}, {Name: "b"}}, common.NewColorLogger(), func(cfg VMConfig) error {
			if cfg.Name == "b" {
				return errors.New("creation failed")
			}
			return nil
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deployment errors")
		assert.Contains(t, err.Error(), "failed to create VM b: creation failed")
	})

	t.Run("client wrapper delegates to create seam", func(t *testing.T) {
		originalCreateVMForDeploy := createVMForDeployFn
		t.Cleanup(func() { createVMForDeployFn = originalCreateVMForDeploy })

		var seen []string
		createVMForDeployFn = func(client *Client, config VMConfig) (*object.VirtualMachine, error) {
			require.NotNil(t, client)
			seen = append(seen, config.Name)
			return nil, nil
		}

		client := &Client{logger: common.NewColorLogger()}
		err := client.DeployVMsConcurrently([]VMConfig{{Name: "a"}, {Name: "b"}})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"a", "b"}, seen)
	})
}

func TestResolveVSphereCredentials(t *testing.T) {
	originalGetSecrets := get1PasswordSecretsBatch
	t.Cleanup(func() { get1PasswordSecretsBatch = originalGetSecrets })

	t.Run("uses onepassword values first", func(t *testing.T) {
		get1PasswordSecretsBatch = func([]string) map[string]string {
			return map[string]string{
				constants.OpESXiHost:     "esxi.local",
				constants.OpESXiUsername: "root",
				constants.OpESXiPassword: "secret",
			}
		}

		host, username, password, usedEnvFallback := resolveVSphereCredentials()
		assert.Equal(t, "esxi.local", host)
		assert.Equal(t, "root", username)
		assert.Equal(t, "secret", password)
		assert.False(t, usedEnvFallback)
	})

	t.Run("falls back to environment", func(t *testing.T) {
		get1PasswordSecretsBatch = func([]string) map[string]string { return map[string]string{} }
		t.Setenv(constants.EnvVSphereHost, "env-host")
		t.Setenv(constants.EnvVSphereUsername, "env-user")
		t.Setenv(constants.EnvVSpherePassword, "env-pass")

		host, username, password, usedEnvFallback := resolveVSphereCredentials()
		assert.Equal(t, "env-host", host)
		assert.Equal(t, "env-user", username)
		assert.Equal(t, "env-pass", password)
		assert.True(t, usedEnvFallback)
	})
}

func TestGetVMNamesWithSeams(t *testing.T) {
	originalGetSecrets := get1PasswordSecretsBatch
	originalNewClient := newClientWithConnectFn
	originalListVMNames := listVMNamesFn
	t.Cleanup(func() {
		get1PasswordSecretsBatch = originalGetSecrets
		newClientWithConnectFn = originalNewClient
		listVMNamesFn = originalListVMNames
	})

	get1PasswordSecretsBatch = func([]string) map[string]string { return map[string]string{} }
	t.Setenv(constants.EnvVSphereHost, "env-host")
	t.Setenv(constants.EnvVSphereUsername, "env-user")
	t.Setenv(constants.EnvVSpherePassword, "env-pass")

	newClientWithConnectFn = func(host, username, password string, insecure bool) (*Client, error) {
		assert.Equal(t, "env-host", host)
		assert.Equal(t, "env-user", username)
		assert.Equal(t, "env-pass", password)
		assert.True(t, insecure)
		return &Client{}, nil
	}
	listVMNamesFn = func(client *Client) ([]string, error) {
		require.NotNil(t, client)
		return []string{"k8s-0", "k8s-1"}, nil
	}

	names, err := GetVMNames()
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-1"}, names)

	listVMNamesFn = func(*Client) ([]string, error) {
		return nil, errors.New("list failure")
	}
	_, err = GetVMNames()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list failure")
}

func TestConnectAndCloseWithSeams(t *testing.T) {
	originalNewGovmomiClient := newGovmomiClientFn
	originalNewFinder := newFinderFn
	originalDefaultDatacenter := defaultDatacenterFn
	originalSetFinderDatacenter := setFinderDatacenterFn
	originalLogout := logoutVSphereClientFn
	t.Cleanup(func() {
		newGovmomiClientFn = originalNewGovmomiClient
		newFinderFn = originalNewFinder
		defaultDatacenterFn = originalDefaultDatacenter
		setFinderDatacenterFn = originalSetFinderDatacenter
		logoutVSphereClientFn = originalLogout
	})

	var (
		sawURL          *url.URL
		sawInsecure     bool
		setDatacenterOK bool
	)

	var finder *find.Finder
	datacenter := &object.Datacenter{}

	newGovmomiClientFn = func(ctx context.Context, u *url.URL, insecure bool) (*govmomi.Client, error) {
		sawURL = u
		sawInsecure = insecure
		return &govmomi.Client{Client: &vim25.Client{}}, nil
	}
	newFinderFn = func(client *vim25.Client) *find.Finder {
		require.NotNil(t, client)
		return finder
	}
	defaultDatacenterFn = func(ctx context.Context, gotFinder *find.Finder) (*object.Datacenter, error) {
		require.Nil(t, gotFinder)
		return datacenter, nil
	}
	setFinderDatacenterFn = func(gotFinder *find.Finder, gotDatacenter *object.Datacenter) {
		require.Nil(t, gotFinder)
		require.Equal(t, datacenter, gotDatacenter)
		setDatacenterOK = true
	}

	client, err := NewClientWithConnect("esxi.local", "root", "secret", true)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "https://root:secret@esxi.local/sdk", sawURL.String())
	assert.Equal(t, "root:secret", sawURL.User.String())
	assert.True(t, sawInsecure)
	assert.True(t, setDatacenterOK)
	assert.Nil(t, client.finder)
	assert.Equal(t, datacenter, client.datacenter)

	logoutCalled := 0
	logoutVSphereClientFn = func(ctx context.Context, gotClient *govmomi.Client) error {
		logoutCalled++
		require.Equal(t, client.client, gotClient)
		return nil
	}

	require.NoError(t, client.Close())
	assert.Equal(t, 1, logoutCalled)
}

func TestConnectAndCloseErrors(t *testing.T) {
	originalNewGovmomiClient := newGovmomiClientFn
	originalNewFinder := newFinderFn
	originalDefaultDatacenter := defaultDatacenterFn
	originalLogout := logoutVSphereClientFn
	t.Cleanup(func() {
		newGovmomiClientFn = originalNewGovmomiClient
		newFinderFn = originalNewFinder
		defaultDatacenterFn = originalDefaultDatacenter
		logoutVSphereClientFn = originalLogout
	})

	newGovmomiClientFn = func(context.Context, *url.URL, bool) (*govmomi.Client, error) {
		return nil, errors.New("dial failure")
	}
	err := (&Client{logger: common.NewColorLogger()}).Connect("esxi.local", "root", "secret", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create vSphere client")

	newGovmomiClientFn = func(context.Context, *url.URL, bool) (*govmomi.Client, error) {
		return &govmomi.Client{Client: &vim25.Client{}}, nil
	}
	newFinderFn = func(client *vim25.Client) *find.Finder { return nil }
	defaultDatacenterFn = func(context.Context, *find.Finder) (*object.Datacenter, error) {
		return nil, errors.New("no datacenter")
	}
	err = (&Client{logger: common.NewColorLogger()}).Connect("esxi.local", "root", "secret", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to find datacenter")

	cancelled := false
	client := &Client{
		client: &govmomi.Client{Client: &vim25.Client{}},
		ctx:    context.Background(),
		cancel: func() { cancelled = true },
	}
	logoutVSphereClientFn = func(context.Context, *govmomi.Client) error {
		return errors.New("logout failure")
	}
	err = client.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logout failure")
	assert.True(t, cancelled)
}

func TestListVMNames(t *testing.T) {
	originalListVMObjects := listVMObjectsFn
	t.Cleanup(func() { listVMObjectsFn = originalListVMObjects })

	vm0 := object.NewVirtualMachine(nil, types.ManagedObjectReference{})
	vm0.InventoryPath = "/dc/vm/k8s-0"
	vm1 := object.NewVirtualMachine(nil, types.ManagedObjectReference{})
	vm1.InventoryPath = "/dc/vm/k8s-1"

	listVMObjectsFn = func(*Client) ([]*object.VirtualMachine, error) {
		return []*object.VirtualMachine{vm0, vm1}, nil
	}
	got, err := listVMNames(&Client{})
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-1"}, got)

	listVMObjectsFn = func(*Client) ([]*object.VirtualMachine, error) {
		return nil, errors.New("backend failure")
	}
	_, err = listVMNames(&Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list VMs")

	listVMObjectsFn = func(*Client) ([]*object.VirtualMachine, error) {
		return []*object.VirtualMachine{}, nil
	}
	_, err = listVMNames(&Client{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no VMs found")
}

func TestClientFinderAndUploadHelpers(t *testing.T) {
	originalFindVM := findVirtualMachineFn
	originalListVMs := listVirtualMachinesFn
	originalGetVMProperties := getVMPropertiesFn
	originalFindDatastore := findDatastoreFn
	originalStatFile := statFileFn
	originalUploadDatastoreFile := uploadDatastoreFileFn
	t.Cleanup(func() {
		findVirtualMachineFn = originalFindVM
		listVirtualMachinesFn = originalListVMs
		getVMPropertiesFn = originalGetVMProperties
		findDatastoreFn = originalFindDatastore
		statFileFn = originalStatFile
		uploadDatastoreFileFn = originalUploadDatastoreFile
	})

	client := &Client{ctx: context.Background(), logger: common.NewColorLogger()}
	vm := object.NewVirtualMachine(nil, types.ManagedObjectReference{})
	vm.InventoryPath = "/dc/vm/k8s-0"

	findVirtualMachineFn = func(finder *find.Finder, ctx context.Context, name string) (*object.VirtualMachine, error) {
		assert.Nil(t, finder)
		assert.Equal(t, "k8s-0", name)
		return vm, nil
	}
	found, err := client.FindVM("k8s-0")
	require.NoError(t, err)
	assert.Equal(t, vm, found)

	findVirtualMachineFn = func(*find.Finder, context.Context, string) (*object.VirtualMachine, error) {
		return nil, errors.New("not found")
	}
	_, err = client.FindVM("k8s-0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to find VM k8s-0")

	listVirtualMachinesFn = func(*find.Finder, context.Context) ([]*object.VirtualMachine, error) {
		return []*object.VirtualMachine{vm}, nil
	}
	vms, err := client.ListVMs()
	require.NoError(t, err)
	assert.Len(t, vms, 1)

	listVirtualMachinesFn = func(*find.Finder, context.Context) ([]*object.VirtualMachine, error) {
		return nil, errors.New("list failure")
	}
	_, err = client.ListVMs()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list VMs")

	getVMPropertiesFn = func(_ *object.VirtualMachine, _ context.Context, _ types.ManagedObjectReference, _ []string, dst interface{}) error {
		target := dst.(*mo.VirtualMachine)
		target.Config = &types.VirtualMachineConfigInfo{Version: "vmx-21"}
		return nil
	}
	info, err := client.GetVMInfo(vm)
	require.NoError(t, err)
	assert.Equal(t, "vmx-21", info.Config.Version)

	getVMPropertiesFn = func(*object.VirtualMachine, context.Context, types.ManagedObjectReference, []string, interface{}) error {
		return errors.New("property failure")
	}
	_, err = client.GetVMInfo(vm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get VM properties")

	tmpDir := t.TempDir()
	localISO := tmpDir + "/talos.iso"
	require.NoError(t, os.WriteFile(localISO, []byte("iso"), 0o644))
	datastore := &fakeDatastoreUploader{}

	findDatastoreFn = func(*find.Finder, context.Context, string) (datastoreUploader, error) {
		return datastore, nil
	}
	statFileFn = os.Stat
	uploadDatastoreFileFn = func(datastore datastoreUploader, ctx context.Context, localFilePath, remoteFileName string) error {
		return datastore.UploadFile(ctx, localFilePath, remoteFileName, nil)
	}

	err = client.UploadISOToDatastore(localISO, "datastore1", "talos.iso")
	require.NoError(t, err)
	require.Len(t, datastore.uploads, 1)
	assert.Equal(t, localISO, datastore.uploads[0].local)
	assert.Equal(t, "talos.iso", datastore.uploads[0].remote)

	findDatastoreFn = func(*find.Finder, context.Context, string) (datastoreUploader, error) {
		return nil, errors.New("missing datastore")
	}
	err = client.UploadISOToDatastore(localISO, "datastore1", "talos.iso")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to find datastore datastore1")

	findDatastoreFn = func(*find.Finder, context.Context, string) (datastoreUploader, error) {
		return datastore, nil
	}
	statFileFn = func(string) (os.FileInfo, error) { return nil, errors.New("stat failure") }
	err = client.UploadISOToDatastore(localISO, "datastore1", "talos.iso")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get file info")

	statFileFn = os.Stat
	uploadDatastoreFileFn = func(datastore datastoreUploader, ctx context.Context, localFilePath, remoteFileName string) error {
		return errors.New("upload failure")
	}
	err = client.UploadISOToDatastore(localISO, "datastore1", "talos.iso")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to upload file to datastore")
}

func TestSmallHelpers(t *testing.T) {
	assert.Equal(t, "'/tmp/vm dir'", shellQuote("/tmp/vm dir"))
	assert.Equal(t, "'node'\"'\"'s vm'", shellQuote("node's vm"))
	assert.Equal(t, 3*time.Second, totalRetryDelay([]time.Duration{time.Second, 2 * time.Second}))
	assert.True(t, isDigits("123"))
	assert.False(t, isDigits("12a"))

	vmID, err := parseRegisteredVMID("Registered virtual machine: 123\n")
	require.NoError(t, err)
	assert.Equal(t, "123", vmID)

	vmID, err = parseRegisteredVMID("456\n")
	require.NoError(t, err)
	assert.Equal(t, "456", vmID)

	_, err = parseRegisteredVMID("no numeric id here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to find numeric VM ID")
}
