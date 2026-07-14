package vmlifecycle

import (
	"errors"
	"testing"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/testutil"
	"homeops-cli/internal/truenas"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
)

type helperFakeLifecycle struct {
	closed int
}

func (f *helperFakeLifecycle) ListVMs() error                                  { return nil }
func (f *helperFakeLifecycle) VMSummaries() ([]vmprov.VMSummary, error)        { return nil, nil }
func (f *helperFakeLifecycle) StartVM(string) error                            { return nil }
func (f *helperFakeLifecycle) StopVM(string, bool) error                       { return nil }
func (f *helperFakeLifecycle) RestartVM(string) error                          { return nil }
func (f *helperFakeLifecycle) DeleteVM(string) error                           { return nil }
func (f *helperFakeLifecycle) GetVMInfo(string) error                          { return nil }
func (f *helperFakeLifecycle) SetVMResources(string, int, int) error           { return nil }
func (f *helperFakeLifecycle) ResizeVMDisk(string, string, string) error       { return nil }
func (f *helperFakeLifecycle) SnapshotVM(string, string) error                 { return nil }
func (f *helperFakeLifecycle) ListVMSnapshots(string) error                    { return nil }
func (f *helperFakeLifecycle) RollbackVM(string, string) error                 { return nil }
func (f *helperFakeLifecycle) DeleteVMSnapshot(string, string) error           { return nil }
func (f *helperFakeLifecycle) Clone(string, string, vmprov.CloneOptions) error { return nil }
func (f *helperFakeLifecycle) VMIPAddresses(string) ([]string, error)          { return nil, nil }
func (f *helperFakeLifecycle) ConsoleURL(string) (string, error)               { return "", nil }
func (f *helperFakeLifecycle) Capabilities() vmprov.Capabilities               { return vmprov.Capabilities{} }
func (f *helperFakeLifecycle) Close() error                                    { f.closed++; return nil }
func (f *helperFakeLifecycle) UploadISOFromURL(string, string, string) error   { return nil }
func (f *helperFakeLifecycle) DeployVM(proxmox.VMConfig) error                 { return nil }
func (f *helperFakeLifecycle) ImportTemplate(proxmox.VMConfig) error           { return nil }
func (f *helperFakeLifecycle) ConvertVMToTemplate(string) error                { return nil }
func (f *helperFakeLifecycle) ConsoleURLs(string) (string, string, error)      { return "", "", nil }

type helperFakeTrueNASManager struct {
	connects int
	closed   int
}

func (f *helperFakeTrueNASManager) Connect() error                                  { f.connects++; return nil }
func (f *helperFakeTrueNASManager) Close() error                                    { f.closed++; return nil }
func (f *helperFakeTrueNASManager) DeployVM(truenas.VMConfig) error                 { return nil }
func (f *helperFakeTrueNASManager) ListVMs() error                                  { return nil }
func (f *helperFakeTrueNASManager) VMSummaries() ([]vmprov.VMSummary, error)        { return nil, nil }
func (f *helperFakeTrueNASManager) StartVM(string) error                            { return nil }
func (f *helperFakeTrueNASManager) StopVM(string, bool) error                       { return nil }
func (f *helperFakeTrueNASManager) RestartVM(string) error                          { return nil }
func (f *helperFakeTrueNASManager) DeleteVM(string, bool, string) error             { return nil }
func (f *helperFakeTrueNASManager) GetVMInfo(string) error                          { return nil }
func (f *helperFakeTrueNASManager) SetVMResources(string, int, int) error           { return nil }
func (f *helperFakeTrueNASManager) ResizeVMDisk(string, string, string) error       { return nil }
func (f *helperFakeTrueNASManager) SnapshotVM(string, string) error                 { return nil }
func (f *helperFakeTrueNASManager) ListVMSnapshots(string) error                    { return nil }
func (f *helperFakeTrueNASManager) RollbackVM(string, string) error                 { return nil }
func (f *helperFakeTrueNASManager) DeleteVMSnapshot(string, string) error           { return nil }
func (f *helperFakeTrueNASManager) Clone(string, string, vmprov.CloneOptions) error { return nil }
func (f *helperFakeTrueNASManager) VMIPAddresses(string) ([]string, error)          { return nil, nil }
func (f *helperFakeTrueNASManager) ConsoleURL(string) (string, error)               { return "", nil }
func (f *helperFakeTrueNASManager) Capabilities() vmprov.Capabilities               { return vmprov.Capabilities{} }
func (f *helperFakeTrueNASManager) CleanupOrphanedZVols(string, string) error       { return nil }

type helperFakeVSphereClient struct {
	connectArgs []interface{}
	closed      int
}

func (f *helperFakeVSphereClient) Connect(host, username, password string, insecure bool) error {
	f.connectArgs = []interface{}{host, username, password, insecure}
	return nil
}
func (f *helperFakeVSphereClient) Close() error                                  { f.closed++; return nil }
func (f *helperFakeVSphereClient) FindVM(string) (*object.VirtualMachine, error) { return nil, nil }
func (f *helperFakeVSphereClient) ListVMs() ([]*object.VirtualMachine, error)    { return nil, nil }
func (f *helperFakeVSphereClient) GetVMInfo(*object.VirtualMachine) (*mo.VirtualMachine, error) {
	return nil, nil
}
func (f *helperFakeVSphereClient) UploadISOToDatastore(string, string, string) error { return nil }
func (f *helperFakeVSphereClient) PowerOnVM(*object.VirtualMachine) error            { return nil }
func (f *helperFakeVSphereClient) PowerOffVM(*object.VirtualMachine) error           { return nil }
func (f *helperFakeVSphereClient) DeleteVM(*object.VirtualMachine) error             { return nil }

func TestWithTrueNASVMManagerConnectsClosesAndPassesManager(t *testing.T) {
	fake := &helperFakeTrueNASManager{}
	testutil.Swap(t, &GetTrueNASCredentialsFn, func() (string, string, error) {
		return "nas.local", "api-key", nil
	})
	testutil.Swap(t, &NewTrueNASVMManagerFn, func(host, apiKey string, port int, useSSL bool) TrueNASVMManager {
		assert.Equal(t, "nas.local", host)
		assert.Equal(t, "api-key", apiKey)
		assert.Equal(t, 443, port)
		assert.True(t, useSSL)
		return fake
	})

	var received TrueNASVMManager
	err := WithTrueNASVMManager(common.NewColorLogger(), func(manager TrueNASVMManager) error {
		received = manager
		return errors.New("callback failed")
	})

	require.ErrorContains(t, err, "callback failed")
	assert.Same(t, fake, received)
	assert.Equal(t, 1, fake.connects)
	assert.Equal(t, 1, fake.closed)
}

func TestWithProxmoxVMManagerConstructsWithCredentialsAndCloses(t *testing.T) {
	fake := &helperFakeLifecycle{}
	t.Setenv(constants.EnvProxmoxInsecure, "true")
	testutil.Swap(t, &GetProxmoxCredentialsFn, func() (string, string, string, string, error) {
		return "pve.local", "token-id", "token-secret", "pve-node", nil
	})
	testutil.Swap(t, &NewProxmoxVMManagerFn, func(host, tokenID, secret, nodeName string, insecure bool) (ProxmoxVMManager, error) {
		assert.Equal(t, "pve.local", host)
		assert.Equal(t, "token-id", tokenID)
		assert.Equal(t, "token-secret", secret)
		assert.Equal(t, "pve-node", nodeName)
		assert.True(t, insecure)
		return fake, nil
	})

	err := WithProxmoxVMManager(common.NewColorLogger(), func(manager ProxmoxVMManager) error {
		assert.Same(t, fake, manager)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, fake.closed)
}

func TestWithVSphereClientConstructsConnectsAndCloses(t *testing.T) {
	fake := &helperFakeVSphereClient{}
	t.Setenv(constants.EnvVSphereInsecure, "true")
	testutil.Swap(t, &GetVSphereCredsFn, func() (string, string, string, error) {
		return "vc.local", "administrator", "password", nil
	})
	testutil.Swap(t, &NewVSphereClientFn, func(host, username, password string, insecure bool) VSphereClient {
		assert.Equal(t, "vc.local", host)
		assert.Equal(t, "administrator", username)
		assert.Equal(t, "password", password)
		assert.True(t, insecure)
		return fake
	})

	err := WithVSphereClient(common.NewColorLogger(), func(client VSphereClient) error {
		assert.Same(t, fake, client)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []interface{}{"vc.local", "administrator", "password", true}, fake.connectArgs)
	assert.Equal(t, 1, fake.closed)
}

func TestWithVMLifecycleConstructsAndClosesProviderLifecycle(t *testing.T) {
	fake := &helperFakeLifecycle{}
	testutil.Swap(t, &NewVMLifecycleFn, func(provider string) (vmprov.VMLifecycle, error) {
		assert.Equal(t, "proxmox", provider)
		return fake, nil
	})

	err := WithVMLifecycle("proxmox", func(lifecycle vmprov.VMLifecycle) error {
		assert.Same(t, fake, lifecycle)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, fake.closed)
}

func TestRunVMLifecycleActionNormalizesProviderChoosesNameAndRunsOperation(t *testing.T) {
	fake := &helperFakeLifecycle{}
	testutil.Swap(t, &GetESXiVMNamesFn, func() ([]string, error) {
		return []string{"vc-vm"}, nil
	})
	testutil.Swap(t, &ChooseVMFunc, func(prompt string, options []string) (string, error) {
		assert.Equal(t, "Select VM to restart:", prompt)
		assert.Equal(t, []string{"vc-vm"}, options)
		return "vc-vm", nil
	})
	testutil.Swap(t, &NewVMLifecycleFn, func(provider string) (vmprov.VMLifecycle, error) {
		assert.Equal(t, "vsphere", provider)
		return fake, nil
	})

	var gotName string
	err := RunVMLifecycleAction("", "esxi", "restart", func(lifecycle vmprov.VMLifecycle, name string) error {
		assert.Same(t, fake, lifecycle)
		gotName = name
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "vc-vm", gotName)
	assert.Equal(t, 1, fake.closed)
}

func TestGetESXiVMNamesUsesSeam(t *testing.T) {
	testutil.Swap(t, &VSphereGetVMNamesFn, func() ([]string, error) {
		return []string{"vm-a", "vm-b"}, nil
	})

	names, err := GetESXiVMNames()

	require.NoError(t, err)
	assert.Equal(t, []string{"vm-a", "vm-b"}, names)
}

func TestGetVSphereHostPrefersSecretThenEnvAndErrorsWhenMissing(t *testing.T) {
	testutil.Swap(t, &ResolveSecretKeyFn, func(key string) string {
		if key == versionconfig.KeyVSphereHost {
			return "secret-vc.local"
		}
		return ""
	})
	host, err := GetVSphereHost()
	require.NoError(t, err)
	assert.Equal(t, "secret-vc.local", host)

	testutil.Swap(t, &ResolveSecretKeyFn, func(string) string { return "" })
	t.Setenv(constants.EnvVSphereHost, "env-vc.local")
	host, err = GetVSphereHost()
	require.NoError(t, err)
	assert.Equal(t, "env-vc.local", host)

	t.Setenv(constants.EnvVSphereHost, "")
	_, err = GetVSphereHost()
	require.ErrorContains(t, err, constants.EnvVSphereHost)
}

func TestDefaultProviderNameReadsConfigWithProxmoxFallback(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Hypervisors: versionconfig.HypervisorsConfig{Default: "truenas"},
	})
	defer restore()

	assert.Equal(t, "truenas", DefaultProviderName())

	restore = versionconfig.SetForTesting(&versionconfig.Config{})
	defer restore()
	assert.Equal(t, "proxmox", DefaultProviderName())
}

func TestTrueNASNetworkBridgeUsesEnvWithDefault(t *testing.T) {
	assert.Equal(t, "br0", TrueNASNetworkBridge())
	t.Setenv("NETWORK_BRIDGE", "br42")
	assert.Equal(t, "br42", TrueNASNetworkBridge())
}

func TestGetSpicePasswordPrefersConfiguredSecretThenEnv(t *testing.T) {
	testutil.Swap(t, &ResolveSecretKeyFn, func(key string) string {
		if key == versionconfig.KeyTrueNASSpicePassword {
			return "secret-spice"
		}
		return ""
	})
	assert.Equal(t, "secret-spice", GetSpicePassword())

	testutil.Swap(t, &ResolveSecretKeyFn, func(string) string { return "" })
	t.Setenv(constants.EnvSPICEPassword, "env-spice")
	assert.Equal(t, "env-spice", GetSpicePassword())
}
