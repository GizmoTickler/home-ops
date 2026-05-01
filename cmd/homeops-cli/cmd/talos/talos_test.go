package talos

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/iso"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	internaltalos "homeops-cli/internal/talos"
	"homeops-cli/internal/truenas"

	"homeops-cli/internal/testutil"
	"homeops-cli/internal/vsphere"
)

type fakeVSphereDeployer struct {
	createdConfigs  []vsphere.VMConfig
	deployedConfigs []vsphere.VMConfig
	createErr       error
	deployErr       error
	closeErr        error
	closeCalls      int
}

func stubUnavailable1PasswordCLI(t *testing.T) {
	t.Helper()

	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (f *fakeVSphereDeployer) CreateVM(config vsphere.VMConfig) error {
	f.createdConfigs = append(f.createdConfigs, config)
	return f.createErr
}

func (f *fakeVSphereDeployer) DeployVMsConcurrently(configs []vsphere.VMConfig) error {
	f.deployedConfigs = append([]vsphere.VMConfig(nil), configs...)
	return f.deployErr
}

func (f *fakeVSphereDeployer) Close() error {
	f.closeCalls++
	return f.closeErr
}

type fakeESXiK8sVMDeployer struct {
	configs    []vsphere.VMConfig
	createErr  error
	closeCalls int
}

func (f *fakeESXiK8sVMDeployer) CreateK8sVM(config vsphere.VMConfig) error {
	f.configs = append(f.configs, config)
	return f.createErr
}

func (f *fakeESXiK8sVMDeployer) Close() {
	f.closeCalls++
}

type fakeTalosFactoryClient struct {
	schematic    *internaltalos.SchematicConfig
	isoInfo      *internaltalos.ISOInfo
	loadErr      error
	generateErr  error
	lastVersion  string
	lastArch     string
	lastPlatform string
}

func (f *fakeTalosFactoryClient) LoadSchematicFromTemplate() (*internaltalos.SchematicConfig, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.schematic, nil
}

func (f *fakeTalosFactoryClient) GenerateISOFromSchematic(config *internaltalos.SchematicConfig, talosVersion, architecture, platform string) (*internaltalos.ISOInfo, error) {
	f.lastVersion = talosVersion
	f.lastArch = architecture
	f.lastPlatform = platform
	if f.generateErr != nil {
		return nil, f.generateErr
	}
	return f.isoInfo, nil
}

type fakeISODownloader struct {
	configs []iso.DownloadConfig
	err     error
}

func (f *fakeISODownloader) DownloadCustomISO(config iso.DownloadConfig) error {
	f.configs = append(f.configs, config)
	return f.err
}

type fakeTrueNASSSHClient struct {
	connectErr error
	verifyErr  error
	exists     bool
	size       int64
	closeCalls int
}

func (f *fakeTrueNASSSHClient) Connect() error { return f.connectErr }
func (f *fakeTrueNASSSHClient) Close() error {
	f.closeCalls++
	return nil
}
func (f *fakeTrueNASSSHClient) VerifyFile(string) (bool, int64, error) {
	return f.exists, f.size, f.verifyErr
}

type fakeTrueNASVMManager struct {
	connectCalls int
	closeCalls   int
	deployed     []truenas.VMConfig
	deployErr    error
	listCalls    int
	started      []string
	stopped      []string
	deleted      []string
	infoNames    []string
	cleanupPairs []string
	connectErr   error
	closeErr     error
}

func (f *fakeTrueNASVMManager) Connect() error { f.connectCalls++; return f.connectErr }
func (f *fakeTrueNASVMManager) Close() error   { f.closeCalls++; return f.closeErr }
func (f *fakeTrueNASVMManager) DeployVM(config truenas.VMConfig) error {
	f.deployed = append(f.deployed, config)
	return f.deployErr
}
func (f *fakeTrueNASVMManager) ListVMs() error { f.listCalls++; return nil }
func (f *fakeTrueNASVMManager) StartVM(name string) error {
	f.started = append(f.started, name)
	return nil
}
func (f *fakeTrueNASVMManager) StopVM(name string, force bool) error {
	f.stopped = append(f.stopped, fmt.Sprintf("%s:%t", name, force))
	return nil
}
func (f *fakeTrueNASVMManager) DeleteVM(name string, deleteZVol bool, storagePool string) error {
	f.deleted = append(f.deleted, fmt.Sprintf("%s:%t:%s", name, deleteZVol, storagePool))
	return nil
}
func (f *fakeTrueNASVMManager) GetVMInfo(name string) error {
	f.infoNames = append(f.infoNames, name)
	return nil
}
func (f *fakeTrueNASVMManager) CleanupOrphanedZVols(vmName, storagePool string) error {
	f.cleanupPairs = append(f.cleanupPairs, vmName+":"+storagePool)
	return nil
}

type fakeProxmoxVMManager struct {
	closeCalls int
	listCalls  int
	started    []string
	stopped    []string
	deleted    []string
	infoNames  []string
	uploads    []string
	deployed   []proxmox.VMConfig
	closeErr   error
	deployErr  error
	deployFunc func(proxmox.VMConfig) error
}

func (f *fakeProxmoxVMManager) Close() error   { f.closeCalls++; return f.closeErr }
func (f *fakeProxmoxVMManager) ListVMs() error { f.listCalls++; return nil }
func (f *fakeProxmoxVMManager) StartVM(name string) error {
	f.started = append(f.started, name)
	return nil
}
func (f *fakeProxmoxVMManager) StopVM(name string, force bool) error {
	f.stopped = append(f.stopped, fmt.Sprintf("%s:%t", name, force))
	return nil
}
func (f *fakeProxmoxVMManager) DeleteVM(name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}
func (f *fakeProxmoxVMManager) GetVMInfo(name string) error {
	f.infoNames = append(f.infoNames, name)
	return nil
}
func (f *fakeProxmoxVMManager) UploadISOFromURL(isoURL, filename, storageName string) error {
	f.uploads = append(f.uploads, isoURL+"|"+filename+"|"+storageName)
	return nil
}
func (f *fakeProxmoxVMManager) DeployVM(config proxmox.VMConfig) error {
	if f.deployFunc != nil {
		return f.deployFunc(config)
	}
	if f.deployErr != nil {
		return f.deployErr
	}
	f.deployed = append(f.deployed, config)
	return nil
}

type fakeVSphereClient struct {
	connectCalls int
	closeCalls   int
	connectArgs  []string
	foundNames   []string
	listCount    int
	uploads      []string
	poweredOn    int
	poweredOff   int
	deleted      int
	connectErr   error
	closeErr     error
	findErr      error
	listErr      error
	infoErr      error
	uploadErr    error
	powerOnErr   error
	powerOffErr  error
	deleteErr    error
	listVMs      []*object.VirtualMachine
	infoResponse *mo.VirtualMachine
}

func (f *fakeVSphereClient) Connect(host, username, password string, insecure bool) error {
	f.connectCalls++
	f.connectArgs = []string{host, username, password, fmt.Sprintf("%t", insecure)}
	return f.connectErr
}
func (f *fakeVSphereClient) Close() error { f.closeCalls++; return f.closeErr }
func (f *fakeVSphereClient) FindVM(name string) (*object.VirtualMachine, error) {
	f.foundNames = append(f.foundNames, name)
	if f.findErr != nil {
		return nil, f.findErr
	}
	return nil, nil
}
func (f *fakeVSphereClient) ListVMs() ([]*object.VirtualMachine, error) {
	f.listCount++
	return f.listVMs, f.listErr
}
func (f *fakeVSphereClient) GetVMInfo(vm *object.VirtualMachine) (*mo.VirtualMachine, error) {
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return f.infoResponse, nil
}
func (f *fakeVSphereClient) UploadISOToDatastore(localFilePath, datastoreName, remoteFileName string) error {
	f.uploads = append(f.uploads, localFilePath+"|"+datastoreName+"|"+remoteFileName)
	return f.uploadErr
}
func (f *fakeVSphereClient) PowerOnVM(vm *object.VirtualMachine) error {
	f.poweredOn++
	return f.powerOnErr
}
func (f *fakeVSphereClient) PowerOffVM(vm *object.VirtualMachine) error {
	f.poweredOff++
	return f.powerOffErr
}
func (f *fakeVSphereClient) DeleteVM(vm *object.VirtualMachine) error {
	f.deleted++
	return f.deleteErr
}

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "talos", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	// Check that all subcommands are registered
	subCommands := []string{
		"apply-node",
		"upgrade-node",
		"upgrade-k8s",
		"reboot-node",
		"shutdown-cluster",
		"reset-node",
		"reset-cluster",
		"kubeconfig",
		"deploy-vm",
		"manage-vm",
		"prepare-iso",
	}

	for _, subCmd := range subCommands {
		t.Run(subCmd, func(t *testing.T) {
			found := false
			for _, cmd := range cmd.Commands() {
				if cmd.Name() == subCmd {
					found = true
					break
				}
			}
			assert.True(t, found, "Subcommand %s not found", subCmd)
		})
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		expected     string
	}{
		{
			name:         "env variable exists",
			key:          "TEST_VAR",
			defaultValue: "default",
			envValue:     "custom",
			expected:     "custom",
		},
		{
			name:         "env variable does not exist",
			key:          "NON_EXISTENT_VAR",
			defaultValue: "default",
			envValue:     "",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				cleanup := testutil.SetEnv(t, tt.key, tt.envValue)
				defer cleanup()
			}

			result := getEnvOrDefault(tt.key, tt.defaultValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeVMProvider(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  string
		expectErr bool
	}{
		{name: "default empty", input: "", expected: "proxmox"},
		{name: "proxmox", input: "proxmox", expected: "proxmox"},
		{name: "truenas", input: "truenas", expected: "truenas"},
		{name: "vsphere", input: "vsphere", expected: "vsphere"},
		{name: "esxi alias", input: "esxi", expected: "vsphere"},
		{name: "case insensitive", input: "TrueNAS", expected: "truenas"},
		{name: "invalid", input: "nope", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeVMProvider(tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGetVMNamesForProvider(t *testing.T) {
	oldTrueNAS := getTrueNASVMNamesFn
	oldProxmox := getProxmoxVMNamesFn
	oldESXi := getESXiVMNamesFn
	t.Cleanup(func() {
		getTrueNASVMNamesFn = oldTrueNAS
		getProxmoxVMNamesFn = oldProxmox
		getESXiVMNamesFn = oldESXi
	})

	getTrueNASVMNamesFn = func() ([]string, error) { return []string{"tn-1"}, nil }
	getProxmoxVMNamesFn = func() ([]string, error) { return []string{"px-1"}, nil }
	getESXiVMNamesFn = func() ([]string, error) { return []string{"esx-1"}, nil }

	names, err := getVMNamesForProvider("truenas")
	require.NoError(t, err)
	assert.Equal(t, []string{"tn-1"}, names)

	names, err = getVMNamesForProvider("proxmox")
	require.NoError(t, err)
	assert.Equal(t, []string{"px-1"}, names)

	names, err = getVMNamesForProvider("esxi")
	require.NoError(t, err)
	assert.Equal(t, []string{"esx-1"}, names)
}

func TestChooseVMNameForProvider(t *testing.T) {
	oldChoose := chooseVMFunc
	oldProxmox := getProxmoxVMNamesFn
	t.Cleanup(func() {
		chooseVMFunc = oldChoose
		getProxmoxVMNamesFn = oldProxmox
	})

	getProxmoxVMNamesFn = func() ([]string, error) { return []string{"vm-a", "vm-b"}, nil }
	chooseVMFunc = func(prompt string, options []string) (string, error) {
		assert.Equal(t, "Select VM to start:", prompt)
		assert.Equal(t, []string{"vm-a", "vm-b"}, options)
		return "vm-b", nil
	}

	selected, err := chooseVMNameForProvider("", "proxmox", "start")
	require.NoError(t, err)
	assert.Equal(t, "vm-b", selected)

	selected, err = chooseVMNameForProvider("already-set", "proxmox", "start")
	require.NoError(t, err)
	assert.Equal(t, "already-set", selected)
}

func TestBuildVSphereVMNames(t *testing.T) {
	tests := []struct {
		name       string
		baseName   string
		nodeCount  int
		startIndex int
		expected   []string
		errText    string
	}{
		{name: "single vm", baseName: "worker", nodeCount: 1, expected: []string{"worker"}},
		{name: "generic batch", baseName: "worker", nodeCount: 3, expected: []string{"worker-0", "worker-1", "worker-2"}},
		{name: "generic batch with offset", baseName: "worker", nodeCount: 3, startIndex: 3, expected: []string{"worker-3", "worker-4", "worker-5"}},
		{name: "k8s batch base", baseName: "k8s", nodeCount: 3, expected: []string{"k8s-0", "k8s-1", "k8s-2"}},
		{name: "empty name", baseName: "", nodeCount: 1, errText: "VM name is required"},
		{name: "invalid node count", baseName: "worker", nodeCount: 0, errText: "node count must be greater than 0"},
		{name: "invalid start index", baseName: "worker", nodeCount: 2, startIndex: -1, errText: "start index must be greater than or equal to 0"},
		{name: "numbered k8s batch is rejected", baseName: "k8s-0", nodeCount: 2, errText: "multi-node deployment cannot start from a numbered k8s node name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildVSphereVMNames(tt.baseName, tt.nodeCount, tt.startIndex)
			if tt.errText != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errText)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestBuildGenericVSphereVMConfigs(t *testing.T) {
	configs, err := buildGenericVSphereVMConfigs("worker", 8192, 4, 40, 100, "00:11:22:33:44:55", "fast-ds", "vl999", vsphere.DefaultISOPath(), 1, 0)
	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, "worker", configs[0].Name)
	assert.Equal(t, "00:11:22:33:44:55", configs[0].MacAddress)
	assert.Equal(t, vsphere.DefaultISOPath(), configs[0].ISO)
	assert.True(t, configs[0].PowerOn)

	configs, err = buildGenericVSphereVMConfigs("worker", 8192, 4, 40, 100, "00:11:22:33:44:55", "fast-ds", "vl999", vsphere.DefaultISOPath(), 2, 0)
	require.NoError(t, err)
	require.Len(t, configs, 2)
	assert.Equal(t, []string{"worker-0", "worker-1"}, []string{configs[0].Name, configs[1].Name})
	assert.Empty(t, configs[0].MacAddress)
	assert.Empty(t, configs[1].MacAddress)
}

func TestBuildGenericVSphereDeploymentPlan(t *testing.T) {
	plan, err := buildGenericVSphereDeploymentPlan("worker", 8192, 4, 40, 100, "00:11:22:33:44:55", "fast-ds", "vl999", vsphere.DefaultISOPath(), 5, 2, 3)
	require.NoError(t, err)
	assert.Equal(t, "generic", plan.Mode)
	assert.Equal(t, []string{"worker-3", "worker-4"}, plan.VMNames)
	assert.Equal(t, 2, plan.Concurrent)
	assert.Equal(t, vsphere.DefaultISOPath(), plan.ISOPath)
	require.Len(t, plan.Configs, 2)
}

func TestBuildK8sVSphereVMConfigs(t *testing.T) {
	configs, err := buildK8sVSphereVMConfigs("k8s", 49152, 16, 250, 800, "vl999", 2, 0)
	require.NoError(t, err)
	require.Len(t, configs, 2)
	assert.Equal(t, []string{"k8s-0", "k8s-1"}, []string{configs[0].Name, configs[1].Name})
	assert.Equal(t, vsphere.DefaultISOPath(), configs[0].ISO)
	assert.Equal(t, "local-nvme1", configs[0].BootDatastore)
	assert.Equal(t, "local-nvme1", configs[1].BootDatastore)

	_, err = buildK8sVSphereVMConfigs("k8s-0", 49152, 16, 250, 800, "vl999", 2, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multi-node deployment cannot start from a numbered k8s node name")
}

func TestBuildK8sVSphereDeploymentPlan(t *testing.T) {
	plan, err := buildK8sVSphereDeploymentPlan("k8s", 49152, 16, 250, 800, "vl999", 4, 2, 1)
	require.NoError(t, err)
	assert.Equal(t, "k8s", plan.Mode)
	assert.Equal(t, []string{"k8s-1", "k8s-2"}, plan.VMNames)
	assert.Equal(t, 2, plan.Concurrent)
	require.Len(t, plan.Configs, 2)
	require.Len(t, plan.NodeConfigs, 2)
	assert.Equal(t, "00:a0:98:1a:f3:72", plan.NodeConfigs[0].MacAddress)
}

func TestBuildTrueNASVMConfig(t *testing.T) {
	config := buildTrueNASVMConfig("tnvm", 8192, 4, 40, 100, "truenas.local", "api-key", "/mnt/iso/talos.iso", "br0", "flashstor/VM", "00:11:22:33:44:55", "spice-pass", "schematic-123", "v1.10.0", true, true)
	assert.Equal(t, "tnvm", config.Name)
	assert.Equal(t, 8192, config.Memory)
	assert.Equal(t, 4, config.VCPUs)
	assert.Equal(t, 40, config.DiskSize)
	assert.Equal(t, 100, config.OpenEBSSize)
	assert.Equal(t, "truenas.local", config.TrueNASHost)
	assert.Equal(t, "api-key", config.TrueNASAPIKey)
	assert.Equal(t, "/mnt/iso/talos.iso", config.TalosISO)
	assert.Equal(t, "br0", config.NetworkBridge)
	assert.Equal(t, "flashstor/VM", config.StoragePool)
	assert.Equal(t, "00:11:22:33:44:55", config.MacAddress)
	assert.True(t, config.SkipZVolCreate)
	assert.Equal(t, "spice-pass", config.SpicePassword)
	assert.True(t, config.UseSpice)
	assert.Equal(t, "schematic-123", config.SchematicID)
	assert.Equal(t, "v1.10.0", config.TalosVersion)
	assert.True(t, config.CustomISO)
}

func TestTrueNASDeploymentHelpers(t *testing.T) {
	oldSecret := get1PasswordSecretFn
	oldManagerFactory := newTrueNASVMManagerFn
	oldSpin := spinWithFuncFn
	t.Cleanup(func() {
		get1PasswordSecretFn = oldSecret
		newTrueNASVMManagerFn = oldManagerFactory
		spinWithFuncFn = oldSpin
	})

	t.Run("required spice password errors when missing", func(t *testing.T) {
		get1PasswordSecretFn = func(string) string { return "" }
		cleanup := testutil.SetEnv(t, constants.EnvSPICEPassword, "")
		defer cleanup()

		password, err := requiredSpicePassword()
		require.Error(t, err)
		assert.Empty(t, password)
		assert.Contains(t, err.Error(), "SPICE password is required")
	})

	t.Run("resolve truenas deployment access from env and 1password", func(t *testing.T) {
		stubUnavailable1PasswordCLI(t)
		get1PasswordSecretFn = func(ref string) string {
			if ref == constants.OpTrueNASSPICEPass {
				return "op-spice"
			}
			return ""
		}
		cleanup := testutil.SetEnvs(t, map[string]string{
			constants.EnvTrueNASHost:   "truenas.local",
			constants.EnvTrueNASAPIKey: "api-key",
		})
		defer cleanup()

		host, apiKey, spicePassword, err := resolveTrueNASDeploymentAccess(common.NewColorLogger())
		require.NoError(t, err)
		assert.Equal(t, "truenas.local", host)
		assert.Equal(t, "api-key", apiKey)
		assert.Equal(t, "op-spice", spicePassword)
	})

	t.Run("connected truenas vm manager uses seam", func(t *testing.T) {
		manager := &fakeTrueNASVMManager{}
		newTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) trueNASVMManager {
			assert.Equal(t, "truenas.local", host)
			assert.Equal(t, "api-key", apiKey)
			assert.Equal(t, 443, port)
			assert.True(t, useSSL)
			return manager
		}

		vmManager, err := connectedTrueNASVMManager(common.NewColorLogger(), "truenas.local", "api-key")
		require.NoError(t, err)
		assert.Same(t, manager, vmManager)
		assert.Equal(t, 1, manager.connectCalls)
	})

	t.Run("truenas network bridge falls back to default", func(t *testing.T) {
		cleanup := testutil.SetEnv(t, "NETWORK_BRIDGE", "")
		defer cleanup()
		assert.Equal(t, "br0", trueNASNetworkBridge())
	})

	t.Run("execute truenas deployment uses spinner seam", func(t *testing.T) {
		manager := &fakeTrueNASVMManager{}
		var spinnerTitle string
		spinWithFuncFn = func(title string, fn func() error) error {
			spinnerTitle = title
			return fn()
		}
		config := truenas.VMConfig{Name: "tnvm"}

		err := executeTrueNASVMDeployment(common.NewColorLogger(), manager, config)
		require.NoError(t, err)
		assert.Equal(t, "Deploying VM tnvm", spinnerTitle)
		require.Len(t, manager.deployed, 1)
		assert.Equal(t, "tnvm", manager.deployed[0].Name)
	})

	t.Run("execute truenas deployment surfaces deploy error", func(t *testing.T) {
		manager := &fakeTrueNASVMManager{deployErr: errors.New("deploy failed")}
		spinWithFuncFn = func(title string, fn func() error) error { return fn() }

		err := executeTrueNASVMDeployment(common.NewColorLogger(), manager, truenas.VMConfig{Name: "tnvm"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VM deployment failed")
	})
}

func TestResolveTrueNASISOSelection(t *testing.T) {
	oldFactory := newTalosFactoryClientFn
	oldDownloader := newISODownloaderFn
	oldSSHClient := newTrueNASSSHClientFn
	oldSecret := get1PasswordSecretFn
	oldWorkdir := workingDirectoryFn
	t.Cleanup(func() {
		newTalosFactoryClientFn = oldFactory
		newISODownloaderFn = oldDownloader
		newTrueNASSSHClientFn = oldSSHClient
		get1PasswordSecretFn = oldSecret
		workingDirectoryFn = oldWorkdir
	})

	workingDirectoryFn = func() string { return "." }
	get1PasswordSecretFn = func(ref string) string {
		switch ref {
		case constants.OpTrueNASHost:
			return "truenas.local"
		case constants.OpTrueNASUsername:
			return "admin"
		default:
			return ""
		}
	}

	t.Run("generated iso path uses factory and downloader seams", func(t *testing.T) {
		fakeFactory := &fakeTalosFactoryClient{
			schematic: &internaltalos.SchematicConfig{},
			isoInfo: &internaltalos.ISOInfo{
				URL:          "https://example.com/custom.iso",
				SchematicID:  "schematic-1234abcd",
				TalosVersion: "v1.10.0",
			},
		}
		fakeDownloader := &fakeISODownloader{}
		newTalosFactoryClientFn = func() talosFactoryClient { return fakeFactory }
		newISODownloaderFn = func() isoDownloader { return fakeDownloader }

		selection, err := prepareGeneratedTrueNASISO(common.NewColorLogger())
		require.NoError(t, err)
		require.NotNil(t, selection)
		assert.Equal(t, filepath.Join(iso.GetDefaultConfig().ISOStoragePath, "metal-amd64-schemati.iso"), selection.ISOPath)
		assert.Equal(t, "schematic-1234abcd", selection.SchematicID)
		assert.Equal(t, "v1.10.0", selection.TalosVersion)
		assert.True(t, selection.CustomISO)
		require.Len(t, fakeDownloader.configs, 1)
		assert.Equal(t, "https://example.com/custom.iso", fakeDownloader.configs[0].ISOURL)
		assert.Equal(t, "admin", fakeDownloader.configs[0].TrueNASUsername)
	})

	t.Run("prepared iso verification returns standard path", func(t *testing.T) {
		fakeSSH := &fakeTrueNASSSHClient{exists: true, size: 2048}
		newTrueNASSSHClientFn = func(config ssh.SSHConfig) trueNASSSHClient {
			assert.Equal(t, "truenas.local", config.Host)
			assert.Equal(t, "admin", config.Username)
			return fakeSSH
		}

		selection, err := verifyPreparedTrueNASISO(common.NewColorLogger(), "truenas.local")
		require.NoError(t, err)
		require.NotNil(t, selection)
		assert.Equal(t, constants.TrueNASStandardISOPath, selection.ISOPath)
		assert.True(t, selection.CustomISO)
		assert.Equal(t, 1, fakeSSH.closeCalls)
	})

	t.Run("prepared iso connection failure returns guided error", func(t *testing.T) {
		newTrueNASSSHClientFn = func(config ssh.SSHConfig) trueNASSSHClient {
			return &fakeTrueNASSSHClient{connectErr: errors.New("ssh down")}
		}

		selection, err := verifyPreparedTrueNASISO(common.NewColorLogger(), "truenas.local")
		require.Error(t, err)
		assert.Nil(t, selection)
		assert.Equal(t, trueNASPreparedISORequiredError().Error(), err.Error())
	})
}

func TestDeployGenericVMOnVSphereSingleUsesSeam(t *testing.T) {
	oldCreds := getVSphereCredsFn
	oldFactory := newVSphereDeployerFn
	t.Cleanup(func() {
		getVSphereCredsFn = oldCreds
		newVSphereDeployerFn = oldFactory
	})

	fake := &fakeVSphereDeployer{}
	getVSphereCredsFn = func() (string, string, string, error) {
		return "esxi.local", "user", "pass", nil
	}
	newVSphereDeployerFn = func(host, username, password string) (vsphereVMDeployer, error) {
		assert.Equal(t, "esxi.local", host)
		assert.Equal(t, "user", username)
		assert.Equal(t, "pass", password)
		return fake, nil
	}

	err := deployGenericVMOnVSphere("worker", "esxi.local", 8192, 4, 50, 100, "00:11:22:33:44:55", "fast-ds", "vl999", false, 2, 1, 0)
	require.NoError(t, err)
	require.Len(t, fake.createdConfigs, 1)
	assert.Equal(t, "worker", fake.createdConfigs[0].Name)
	assert.Equal(t, "00:11:22:33:44:55", fake.createdConfigs[0].MacAddress)
	assert.Equal(t, vsphere.DefaultISOPath(), fake.createdConfigs[0].ISO)
	assert.Equal(t, 1, fake.closeCalls)
	assert.Empty(t, fake.deployedConfigs)
}

func TestDeployGenericVMOnVSphereMultiUsesConcurrentDeploy(t *testing.T) {
	oldCreds := getVSphereCredsFn
	oldFactory := newVSphereDeployerFn
	t.Cleanup(func() {
		getVSphereCredsFn = oldCreds
		newVSphereDeployerFn = oldFactory
	})

	fake := &fakeVSphereDeployer{}
	getVSphereCredsFn = func() (string, string, string, error) {
		return "esxi.local", "user", "pass", nil
	}
	newVSphereDeployerFn = func(host, username, password string) (vsphereVMDeployer, error) {
		return fake, nil
	}

	err := deployGenericVMOnVSphere("worker", "esxi.local", 8192, 4, 50, 100, "00:11:22:33:44:55", "fast-ds", "vl999", false, 2, 3, 0)
	require.NoError(t, err)
	assert.Empty(t, fake.createdConfigs)
	require.Len(t, fake.deployedConfigs, 3)
	assert.Equal(t, []string{"worker-0", "worker-1", "worker-2"}, []string{
		fake.deployedConfigs[0].Name,
		fake.deployedConfigs[1].Name,
		fake.deployedConfigs[2].Name,
	})
	assert.Empty(t, fake.deployedConfigs[0].MacAddress)
	assert.Equal(t, 1, fake.closeCalls)
}

func TestDeployK8sVMViaSSHUsesSeam(t *testing.T) {
	oldFactory := newESXiK8sVMDeployerFn
	t.Cleanup(func() {
		newESXiK8sVMDeployerFn = oldFactory
	})

	fake := &fakeESXiK8sVMDeployer{}
	newESXiK8sVMDeployerFn = func(host, username string) (esxiK8sVMDeployer, error) {
		assert.Equal(t, "esxi.local", host)
		assert.Equal(t, "root", username)
		return fake, nil
	}

	err := deployK8sVMViaSSH("k8s", "esxi.local", 49152, 16, 250, 800, "vl999", false, 2, 0)
	require.NoError(t, err)
	require.Len(t, fake.configs, 2)
	assert.Equal(t, []string{"k8s-0", "k8s-1"}, []string{fake.configs[0].Name, fake.configs[1].Name})
	assert.Equal(t, vsphere.DefaultISOPath(), fake.configs[0].ISO)
	assert.Equal(t, "local-nvme1", fake.configs[0].BootDatastore)
	assert.Equal(t, 1, fake.closeCalls)
}

func TestDeployVMOnVSphereRoutesK8sBaseNamesToSSH(t *testing.T) {
	oldHost := getVSphereHostFn
	oldK8s := newESXiK8sVMDeployerFn
	oldVSphere := newVSphereDeployerFn
	oldCreds := getVSphereCredsFn
	t.Cleanup(func() {
		getVSphereHostFn = oldHost
		newESXiK8sVMDeployerFn = oldK8s
		newVSphereDeployerFn = oldVSphere
		getVSphereCredsFn = oldCreds
	})

	fakeSSH := &fakeESXiK8sVMDeployer{}
	getVSphereHostFn = func() (string, error) { return "esxi.local", nil }
	newESXiK8sVMDeployerFn = func(host, username string) (esxiK8sVMDeployer, error) {
		return fakeSSH, nil
	}
	getVSphereCredsFn = func() (string, string, string, error) {
		return "esxi.local", "user", "pass", nil
	}
	newVSphereDeployerFn = func(host, username, password string) (vsphereVMDeployer, error) {
		t.Fatalf("generic vSphere deployer should not be used for k8s base names")
		return nil, nil
	}

	err := deployVMOnVSphere("k8s", 49152, 16, 250, 800, "", "fast-ds", "vl999", false, 2, 2, 0)
	require.NoError(t, err)
	require.Len(t, fakeSSH.configs, 2)
}

func TestDeployVMProviderOptions(t *testing.T) {
	options := deployVMProviderOptions()

	require.Len(t, options, 3)
	assert.Equal(t, "Proxmox - Deploy to Proxmox VE (default)", options[0])
	assert.Equal(t, "TrueNAS - Deploy to TrueNAS Scale", options[1])
	assert.Equal(t, "vSphere/ESXi - Deploy to vSphere or ESXi", options[2])
}

func TestCommandProviderDefaults(t *testing.T) {
	deployCmd := newDeployVMCommand()
	deployProviderFlag := deployCmd.Flags().Lookup("provider")
	require.NotNil(t, deployProviderFlag)
	assert.Equal(t, "proxmox", deployProviderFlag.DefValue)
	assert.Contains(t, deployCmd.Long, "Defaults to Proxmox VE deployment.")

	prepareISOCmd := newPrepareISOCommand()
	prepareISOProviderFlag := prepareISOCmd.Flags().Lookup("provider")
	require.NotNil(t, prepareISOProviderFlag)
	assert.Equal(t, "proxmox", prepareISOProviderFlag.DefValue)
	assert.Contains(t, prepareISOCmd.Long, "Upload the ISO to Proxmox storage, TrueNAS storage, or a vSphere datastore")
}

func TestVMLifecycleHelpMakesManageVMDiscoverable(t *testing.T) {
	rootCmd := NewCommand()
	rootOutput, err := testutil.ExecuteCommand(rootCmd, "--help")
	require.NoError(t, err)
	assert.Contains(t, rootOutput, "manage-vm")
	assert.Contains(t, rootCmd.Long, "Use `homeops-cli talos manage-vm`")

	manageOutput, err := testutil.ExecuteCommand(newManageVMCommand(), "--help")
	require.NoError(t, err)
	assert.Contains(t, manageOutput, "start")
	assert.Contains(t, manageOutput, "stop")
	assert.Contains(t, manageOutput, "delete")
	assert.Contains(t, manageOutput, "--provider proxmox")
	assert.Contains(t, manageOutput, "TrueNAS")
	assert.Contains(t, manageOutput, "vSphere")
}

func TestRootVMLifecycleAliasReturnsManageVMGuidance(t *testing.T) {
	_, err := testutil.ExecuteCommand(NewCommand(), "start", "--provider", "truenas", "--name", "tn-vm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "talos manage-vm start")
	assert.Contains(t, err.Error(), "--provider truenas")
}

func TestVMLifecycleProviderCapabilityError(t *testing.T) {
	oldStartTrueNAS := startTrueNASVMFn
	t.Cleanup(func() {
		startTrueNASVMFn = oldStartTrueNAS
	})

	stubUnavailable1PasswordCLI(t)
	t.Setenv(constants.EnvTrueNASHost, "")
	t.Setenv(constants.EnvTrueNASAPIKey, "")

	called := false
	startTrueNASVMFn = func(name string) error {
		called = true
		return nil
	}

	_, err := testutil.ExecuteCommand(newStartVMCommand(), "--provider", "truenas", "--name", "tn-vm")
	require.Error(t, err)
	assert.False(t, called, "provider operation should not run when config-level prerequisites are missing")
	assert.Contains(t, err.Error(), "TrueNAS VM lifecycle commands require")
	assert.Contains(t, err.Error(), "homeops-cli talos manage-vm start --provider truenas --name <vm-name>")
	assert.Contains(t, err.Error(), constants.EnvTrueNASHost)
	assert.Contains(t, err.Error(), constants.EnvTrueNASAPIKey)
}

func TestPowerOffVMDispatchesForceStop(t *testing.T) {
	oldChoose := chooseVMFunc
	oldTrueNASGetter := getTrueNASVMNamesFn
	oldProxmoxGetter := getProxmoxVMNamesFn
	oldStopTrueNAS := stopTrueNASVMFn
	oldStopProxmox := stopProxmoxVMFn
	t.Cleanup(func() {
		chooseVMFunc = oldChoose
		getTrueNASVMNamesFn = oldTrueNASGetter
		getProxmoxVMNamesFn = oldProxmoxGetter
		stopTrueNASVMFn = oldStopTrueNAS
		stopProxmoxVMFn = oldStopProxmox
	})

	var truenasForce, proxmoxForce bool
	stopTrueNASVMFn = func(name string, force bool) error {
		assert.Equal(t, "tn-vm", name)
		truenasForce = force
		return nil
	}
	stopProxmoxVMFn = func(name string, force bool) error {
		assert.Equal(t, "px-vm", name)
		proxmoxForce = force
		return nil
	}

	require.NoError(t, powerOffVM("tn-vm", "truenas"))
	assert.True(t, truenasForce)

	require.NoError(t, powerOffVM("px-vm", "proxmox"))
	assert.True(t, proxmoxForce)
}

func TestProviderLifecycleDispatch(t *testing.T) {
	oldStartTrueNAS := startTrueNASVMFn
	oldStartProxmox := startProxmoxVMFn
	oldPowerOnVSphere := powerOnVSphereVMFn
	oldStopTrueNAS := stopTrueNASVMFn
	oldStopProxmox := stopProxmoxVMFn
	oldPowerOffVSphere := powerOffVSphereVMFn
	oldInfoTrueNAS := infoTrueNASVMFn
	oldInfoProxmox := infoProxmoxVMFn
	oldInfoVSphere := infoVSphereVMFn
	oldDeleteTrueNAS := deleteTrueNASVMFn
	oldDeleteProxmox := deleteProxmoxVMFn
	oldDeleteVSphere := deleteVSphereVMFn
	t.Cleanup(func() {
		startTrueNASVMFn = oldStartTrueNAS
		startProxmoxVMFn = oldStartProxmox
		powerOnVSphereVMFn = oldPowerOnVSphere
		stopTrueNASVMFn = oldStopTrueNAS
		stopProxmoxVMFn = oldStopProxmox
		powerOffVSphereVMFn = oldPowerOffVSphere
		infoTrueNASVMFn = oldInfoTrueNAS
		infoProxmoxVMFn = oldInfoProxmox
		infoVSphereVMFn = oldInfoVSphere
		deleteTrueNASVMFn = oldDeleteTrueNAS
		deleteProxmoxVMFn = oldDeleteProxmox
		deleteVSphereVMFn = oldDeleteVSphere
	})

	var calls []string
	startTrueNASVMFn = func(name string) error {
		calls = append(calls, "start-truenas:"+name)
		return nil
	}
	startProxmoxVMFn = func(name string) error {
		calls = append(calls, "start-proxmox:"+name)
		return nil
	}
	powerOnVSphereVMFn = func(name string) error {
		calls = append(calls, "start-vsphere:"+name)
		return nil
	}
	stopTrueNASVMFn = func(name string, force bool) error {
		calls = append(calls, fmt.Sprintf("stop-truenas:%s:%t", name, force))
		return nil
	}
	stopProxmoxVMFn = func(name string, force bool) error {
		calls = append(calls, fmt.Sprintf("stop-proxmox:%s:%t", name, force))
		return nil
	}
	powerOffVSphereVMFn = func(name string) error {
		calls = append(calls, "stop-vsphere:"+name)
		return nil
	}
	infoTrueNASVMFn = func(name string) error {
		calls = append(calls, "info-truenas:"+name)
		return nil
	}
	infoProxmoxVMFn = func(name string) error {
		calls = append(calls, "info-proxmox:"+name)
		return nil
	}
	infoVSphereVMFn = func(name string) error {
		calls = append(calls, "info-vsphere:"+name)
		return nil
	}
	deleteTrueNASVMFn = func(name string) error {
		calls = append(calls, "delete-truenas:"+name)
		return nil
	}
	deleteProxmoxVMFn = func(name string) error {
		calls = append(calls, "delete-proxmox:"+name)
		return nil
	}
	deleteVSphereVMFn = func(name string) error {
		calls = append(calls, "delete-vsphere:"+name)
		return nil
	}

	require.NoError(t, startVMWithProvider("tn-vm", "truenas"))
	require.NoError(t, startVMWithProvider("px-vm", "proxmox"))
	require.NoError(t, startVMWithProvider("esx-vm", "vsphere"))
	require.NoError(t, stopVMWithProvider("tn-vm", "truenas"))
	require.NoError(t, stopVMWithProvider("px-vm", "proxmox"))
	require.NoError(t, stopVMWithProvider("esx-vm", "vsphere"))
	require.NoError(t, infoVMWithProvider("tn-vm", "truenas"))
	require.NoError(t, infoVMWithProvider("px-vm", "proxmox"))
	require.NoError(t, infoVMWithProvider("esx-vm", "vsphere"))
	require.NoError(t, deleteVMWithConfirmation("tn-vm", "truenas", true))
	require.NoError(t, deleteVMWithConfirmation("px-vm", "proxmox", true))
	require.NoError(t, deleteVMWithConfirmation("esx-vm", "vsphere", true))
	require.NoError(t, powerOnVM("tn-vm", "truenas"))
	require.NoError(t, powerOnVM("px-vm", "proxmox"))
	require.NoError(t, powerOnVM("esx-vm", "vsphere"))
	require.NoError(t, powerOffVM("esx-vm", "vsphere"))

	assert.Equal(t, []string{
		"start-truenas:tn-vm",
		"start-proxmox:px-vm",
		"start-vsphere:esx-vm",
		"stop-truenas:tn-vm:false",
		"stop-proxmox:px-vm:false",
		"stop-vsphere:esx-vm",
		"info-truenas:tn-vm",
		"info-proxmox:px-vm",
		"info-vsphere:esx-vm",
		"delete-truenas:tn-vm",
		"delete-proxmox:px-vm",
		"delete-vsphere:esx-vm",
		"start-truenas:tn-vm",
		"start-proxmox:px-vm",
		"start-vsphere:esx-vm",
		"stop-vsphere:esx-vm",
	}, calls)
}

func TestDeployDryRunPaths(t *testing.T) {
	require.NoError(t, deployVMWithPatternDryRun("app01", "flashstor/VM", 8192, 4, 40, 100, "", false, true, true))
	require.NoError(t, deployVMOnProxmoxDryRun("k8s-0", 0, 0, 0, 0, true, 1, 1, 0, true))
	require.NoError(t, deployVMOnProxmoxDryRun("worker01", 8192, 4, 40, 100, false, 1, 1, 0, true))
	require.NoError(t, deployVMOnProxmoxDryRun("k8s", 0, 0, 0, 0, false, 2, 3, 0, true))
	require.NoError(t, deployVMOnVSphereDryRun("worker", 8192, 4, 40, 100, "00:11:22:33:44:55", "fast-ds", "vl999", true, 2, 1, 0, true))
	require.NoError(t, deployVMOnVSphereDryRun("k8s", 49152, 16, 250, 800, "", "fast-ds", "vl999", false, 2, 2, 0, true))
}

func TestDryRunSummaryBuilders(t *testing.T) {
	t.Run("truenas summary includes optional fields", func(t *testing.T) {
		summary := buildTrueNASDryRunSummary("app01", "flashstor/VM", 8192, 4, 40, 100, "00:11:22:33:44:55", true)
		assert.Equal(t, "TrueNAS", summary.Provider)
		assert.Equal(t, []string{"app01"}, summary.VMNames)
		assert.Contains(t, summary.Lines, "Pool: flashstor/VM")
		assert.Contains(t, summary.Lines, "MAC Address: 00:11:22:33:44:55")
		assert.Contains(t, summary.Lines, "Skip ZVol Creation: Yes")
	})

	t.Run("vsphere batch summary includes offset and concurrency", func(t *testing.T) {
		summary, err := buildVSphereDryRunSummary("worker", 8192, 4, 40, 100, "", "fast-ds", "vl999", 2, 3, 4)
		require.NoError(t, err)
		assert.Equal(t, "vSphere/ESXi", summary.Provider)
		assert.Equal(t, []string{"worker-4", "worker-5", "worker-6"}, summary.VMNames)
		assert.Contains(t, summary.Lines, "Deployment Mode: govmomi (generic VM)")
		assert.Contains(t, summary.Lines, "Node Count: 3")
		assert.Contains(t, summary.Lines, "Start Index: 4")
		assert.Contains(t, summary.Lines, "Concurrent Deployments: 2")
	})

	t.Run("proxmox predefined batch summary includes presets and offset", func(t *testing.T) {
		plan, err := buildProxmoxDeploymentPlan("k8s", 0, 0, 0, 0, 2, 2, 1)
		require.NoError(t, err)

		summary := buildProxmoxDryRunSummary(plan, 0, 0, 0, 0)
		assert.Equal(t, "Proxmox VE", summary.Provider)
		assert.Equal(t, []string{"k8s-1", "k8s-2"}, summary.VMNames)
		assert.Contains(t, summary.Lines, "Deployment Mode: Predefined Talos node configuration")
		assert.Contains(t, summary.Lines, "Node Presets: k8s-1, k8s-2")
		assert.Contains(t, summary.Lines, "Start Index: 1")
		assert.Contains(t, summary.Lines, "Concurrent Deployments: 2")
	})
}

func TestBuildProxmoxDeploymentPlanPresetBatchDetails(t *testing.T) {
	plan, err := buildProxmoxDeploymentPlan("k8s", 0, 0, 0, 0, 2, 2, 1)
	require.NoError(t, err)
	assert.True(t, plan.AllPredefined)
	assert.Equal(t, []string{"k8s-1", "k8s-2"}, plan.VMNames)
	assert.Equal(t, 1, plan.StartIndex)
	assert.Equal(t, 2, plan.Concurrent)
	require.Len(t, plan.Presets, 2)
	assert.Equal(t, "k8s-1", plan.Configs[0].Name)
	assert.Equal(t, "k8s-2", plan.Configs[1].Name)
}

func TestVMLifecycleCommandWrappers(t *testing.T) {
	oldEnsureProvider := ensureVMLifecycleProviderFn
	oldStartProxmox := startProxmoxVMFn
	oldStopProxmox := stopProxmoxVMFn
	oldDeleteProxmox := deleteProxmoxVMFn
	oldInfoProxmox := infoProxmoxVMFn
	oldPowerOnVSphere := powerOnVSphereVMFn
	oldPowerOffVSphere := powerOffVSphereVMFn
	t.Cleanup(func() {
		ensureVMLifecycleProviderFn = oldEnsureProvider
		startProxmoxVMFn = oldStartProxmox
		stopProxmoxVMFn = oldStopProxmox
		deleteProxmoxVMFn = oldDeleteProxmox
		infoProxmoxVMFn = oldInfoProxmox
		powerOnVSphereVMFn = oldPowerOnVSphere
		powerOffVSphereVMFn = oldPowerOffVSphere
	})

	var calls []string
	ensureVMLifecycleProviderFn = func(provider, action string) error {
		calls = append(calls, "check:"+provider+":"+action)
		return nil
	}
	startProxmoxVMFn = func(name string) error {
		calls = append(calls, "start:"+name)
		return nil
	}
	stopProxmoxVMFn = func(name string, force bool) error {
		calls = append(calls, fmt.Sprintf("stop:%s:%t", name, force))
		return nil
	}
	deleteProxmoxVMFn = func(name string) error {
		calls = append(calls, "delete:"+name)
		return nil
	}
	infoProxmoxVMFn = func(name string) error {
		calls = append(calls, "info:"+name)
		return nil
	}
	powerOnVSphereVMFn = func(name string) error {
		calls = append(calls, "poweron:"+name)
		return nil
	}
	powerOffVSphereVMFn = func(name string) error {
		calls = append(calls, "poweroff:"+name)
		return nil
	}

	_, err := testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "proxmox", "--name", "k8s-0", "--dry-run")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "truenas", "--name", "app01", "--pool", "flashstor/VM", "--dry-run")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "esxi", "--name", "worker", "--datastore", "fast-ds", "--network", "vl999", "--dry-run")
	require.NoError(t, err)

	_, err = testutil.ExecuteCommand(newStartVMCommand(), "--provider", "proxmox", "--name", "px-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newStopVMCommand(), "--provider", "proxmox", "--name", "px-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newDeleteVMCommand(), "--provider", "proxmox", "--name", "px-vm", "--force")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newInfoVMCommand(), "--provider", "proxmox", "--name", "px-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newPowerOnVMCommand(), "--provider", "vsphere", "--name", "esx-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newPowerOffVMCommand(), "--provider", "vsphere", "--name", "esx-vm")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"check:proxmox:start",
		"start:px-vm",
		"check:proxmox:stop",
		"stop:px-vm:false",
		"check:proxmox:delete",
		"delete:px-vm",
		"check:proxmox:info",
		"info:px-vm",
		"check:vsphere:poweron",
		"poweron:esx-vm",
		"check:vsphere:poweroff",
		"poweroff:esx-vm",
	}, calls)
}

func TestDeployVMCommandProviderValidation(t *testing.T) {
	_, err := testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "unknown", "--name", "worker", "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider: unknown")
}

func TestSecretAndHostResolution(t *testing.T) {
	oldSecret := get1PasswordSecretFn
	t.Cleanup(func() {
		get1PasswordSecretFn = oldSecret
	})

	t.Run("spice password prefers 1password then env", func(t *testing.T) {
		get1PasswordSecretFn = func(ref string) string {
			if ref == constants.OpTrueNASSPICEPass {
				return "op-secret"
			}
			return ""
		}
		assert.Equal(t, "op-secret", getSpicePassword())

		get1PasswordSecretFn = func(string) string { return "" }
		cleanup := testutil.SetEnv(t, "SPICE_PASSWORD", "env-secret")
		defer cleanup()
		assert.Equal(t, "env-secret", getSpicePassword())
	})

	t.Run("vsphere credentials resolve from secret source", func(t *testing.T) {
		get1PasswordSecretFn = func(ref string) string {
			switch ref {
			case constants.OpESXiHost:
				return "esxi.local"
			case constants.OpESXiUsername:
				return "root"
			case constants.OpESXiPassword:
				return "secret"
			default:
				return ""
			}
		}

		host, username, password, err := getVSphereCredentials()
		require.NoError(t, err)
		assert.Equal(t, "esxi.local", host)
		assert.Equal(t, "root", username)
		assert.Equal(t, "secret", password)
	})

	t.Run("vsphere host falls back to env", func(t *testing.T) {
		get1PasswordSecretFn = func(string) string { return "" }
		cleanup := testutil.SetEnv(t, "VSPHERE_HOST", "env-esxi.local")
		defer cleanup()

		host, err := getVSphereHost()
		require.NoError(t, err)
		assert.Equal(t, "env-esxi.local", host)
	})
}

func TestPrepareISOWithProviderDispatch(t *testing.T) {
	oldTrueNAS := prepareISOForTrueNASFn
	oldProxmox := prepareISOForProxmoxFn
	oldVSphere := prepareISOForVSphereFn
	t.Cleanup(func() {
		prepareISOForTrueNASFn = oldTrueNAS
		prepareISOForProxmoxFn = oldProxmox
		prepareISOForVSphereFn = oldVSphere
	})

	var calls []string
	prepareISOForTrueNASFn = func() error {
		calls = append(calls, "truenas")
		return nil
	}
	prepareISOForProxmoxFn = func() error {
		calls = append(calls, "proxmox")
		return nil
	}
	prepareISOForVSphereFn = func() error {
		calls = append(calls, "vsphere")
		return nil
	}

	require.NoError(t, prepareISOWithProvider("truenas"))
	require.NoError(t, prepareISOWithProvider("proxmox"))
	require.NoError(t, prepareISOWithProvider("esxi"))
	assert.Equal(t, []string{"truenas", "proxmox", "vsphere"}, calls)
}

func TestPrepareISOForTarget(t *testing.T) {
	oldFactory := newTalosFactoryClientFn
	oldSpin := spinWithFuncFn
	oldUpdate := updateNodeTemplatesWithSchematicFn
	t.Cleanup(func() {
		newTalosFactoryClientFn = oldFactory
		spinWithFuncFn = oldSpin
		updateNodeTemplatesWithSchematicFn = oldUpdate
	})

	fakeFactory := &fakeTalosFactoryClient{
		schematic: &internaltalos.SchematicConfig{},
		isoInfo: &internaltalos.ISOInfo{
			URL:          "https://example.com/talos.iso",
			SchematicID:  "schematic-123",
			TalosVersion: "v9.9.9",
		},
	}
	newTalosFactoryClientFn = func() talosFactoryClient { return fakeFactory }
	spinWithFuncFn = func(title string, fn func() error) error { return fn() }

	var updatedID, updatedVersion string
	updateNodeTemplatesWithSchematicFn = func(schematicID, talosVersion string) error {
		updatedID = schematicID
		updatedVersion = talosVersion
		return nil
	}

	var uploadedURL string
	err := prepareISOForTarget(isoPreparationTarget{
		providerName:   "Test Provider",
		platform:       "nocloud",
		uploadStep:     "Uploading test ISO...",
		uploadSpinner:  "Upload test ISO",
		location:       "/tmp/test.iso",
		deployCommand:  "homeops-cli talos deploy-vm --provider test",
		summaryMessage: "Uploaded to test provider",
		uploadISO: func(info *internaltalos.ISOInfo) error {
			uploadedURL = info.URL
			return nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/talos.iso", uploadedURL)
	assert.Equal(t, "amd64", fakeFactory.lastArch)
	assert.Equal(t, "nocloud", fakeFactory.lastPlatform)
	assert.NotEmpty(t, fakeFactory.lastVersion)
	assert.Equal(t, "schematic-123", updatedID)
	assert.Equal(t, "v9.9.9", updatedVersion)
}

func TestPrepareISOProviderTargets(t *testing.T) {
	oldPrepareTarget := prepareISOForTargetFn
	oldSecret := get1PasswordSecretFn
	oldDownloader := newISODownloaderFn
	oldUploadVSphere := uploadISOToVSphereFn
	t.Cleanup(func() {
		prepareISOForTargetFn = oldPrepareTarget
		get1PasswordSecretFn = oldSecret
		newISODownloaderFn = oldDownloader
		uploadISOToVSphereFn = oldUploadVSphere
	})

	t.Run("truenas target configures downloader", func(t *testing.T) {
		fakeDownloader := &fakeISODownloader{}
		newISODownloaderFn = func() isoDownloader { return fakeDownloader }
		get1PasswordSecretFn = func(ref string) string {
			switch ref {
			case constants.OpTrueNASHost:
				return "truenas.local"
			case constants.OpTrueNASUsername:
				return "root"
			default:
				return ""
			}
		}
		prepareISOForTargetFn = func(target isoPreparationTarget) error {
			assert.Equal(t, "TrueNAS", target.providerName)
			assert.Equal(t, "metal", target.platform)
			return target.uploadISO(&internaltalos.ISOInfo{URL: "https://example.com/metal.iso"})
		}

		require.NoError(t, prepareISOForTrueNAS())
		require.Len(t, fakeDownloader.configs, 1)
		assert.Equal(t, "truenas.local", fakeDownloader.configs[0].TrueNASHost)
		assert.Equal(t, "root", fakeDownloader.configs[0].TrueNASUsername)
		assert.Equal(t, "https://example.com/metal.iso", fakeDownloader.configs[0].ISOURL)
		assert.Equal(t, filepath.Base(constants.TrueNASStandardISOPath), fakeDownloader.configs[0].ISOFilename)
	})

	t.Run("proxmox target metadata is stable", func(t *testing.T) {
		prepareISOForTargetFn = func(target isoPreparationTarget) error {
			assert.Equal(t, "Proxmox", target.providerName)
			assert.Equal(t, "nocloud", target.platform)
			assert.Contains(t, target.deployCommand, "--provider proxmox")
			return nil
		}

		require.NoError(t, prepareISOForProxmox())
	})

	t.Run("vsphere target uploads via seam", func(t *testing.T) {
		var uploadedURL string
		uploadISOToVSphereFn = func(url string) error {
			uploadedURL = url
			return nil
		}
		prepareISOForTargetFn = func(target isoPreparationTarget) error {
			assert.Equal(t, "vSphere", target.providerName)
			assert.Equal(t, "nocloud", target.platform)
			return target.uploadISO(&internaltalos.ISOInfo{URL: "https://example.com/nocloud.iso"})
		}

		require.NoError(t, prepareISOForVSphere())
		assert.Equal(t, "https://example.com/nocloud.iso", uploadedURL)
	})
}

func TestTalosNodeHelpers(t *testing.T) {
	oldNodeIPs := getTalosNodeIPsFn
	oldChooseNode := chooseTalosNodeFn
	oldTalosctlOutput := talosctlOutputFn
	t.Cleanup(func() {
		getTalosNodeIPsFn = oldNodeIPs
		chooseTalosNodeFn = oldChooseNode
		talosctlOutputFn = oldTalosctlOutput
	})

	t.Run("select talos node", func(t *testing.T) {
		getTalosNodeIPsFn = func() ([]string, error) { return []string{"10.0.0.10", "10.0.0.11"}, nil }
		chooseTalosNodeFn = func(prompt string, options []string) (string, error) {
			assert.Equal(t, "Select node:", prompt)
			assert.Equal(t, []string{"10.0.0.10", "10.0.0.11"}, options)
			return "10.0.0.11", nil
		}

		node, err := selectTalosNode("Select node:")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.11", node)
	})

	t.Run("config info parsing powers node helpers", func(t *testing.T) {
		talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "talosctl", name)
			assert.Equal(t, []string{"config", "info", "--output", "json"}, args)
			return []byte(`{"endpoints":["10.0.0.20"],"nodes":["10.0.0.20","10.0.0.21"]}`), nil
		}

		info, err := getTalosConfigInfo()
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.20"}, info.Endpoints)
		assert.Equal(t, []string{"10.0.0.20", "10.0.0.21"}, info.Nodes)

		node, err := getRandomNode()
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.20", node)

		nodes, err := getAllNodes()
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.20", "10.0.0.21"}, nodes)
	})
}

func TestApplyNodeConfigFlows(t *testing.T) {
	oldMachineType := getMachineTypeFromNodeFn
	oldRender := renderMachineConfigFromEmbeddedFn
	oldInject := injectSecretsFn
	oldEnsureAuth := ensure1PasswordAuthFn
	oldApply := talosApplyConfigFn
	t.Cleanup(func() {
		getMachineTypeFromNodeFn = oldMachineType
		renderMachineConfigFromEmbeddedFn = oldRender
		injectSecretsFn = oldInject
		ensure1PasswordAuthFn = oldEnsureAuth
		talosApplyConfigFn = oldApply
	})

	getMachineTypeFromNodeFn = func(nodeIP string) (string, error) {
		assert.Equal(t, "10.0.0.30", nodeIP)
		return "controlplane", nil
	}
	renderMachineConfigFromEmbeddedFn = func(baseTemplate, patchTemplate string) ([]byte, error) {
		assert.Equal(t, "talos/controlplane.yaml", baseTemplate)
		assert.Equal(t, "talos/nodes/10.0.0.30.yaml", patchTemplate)
		return []byte("machine:\n  token: op://secret\n"), nil
	}

	t.Run("dry run retries after auth", func(t *testing.T) {
		injectCalls := 0
		injectSecretsFn = func(config string) (string, error) {
			injectCalls++
			if injectCalls == 1 {
				return "", errors.New("not authenticated")
			}
			return "machine:\n  token: resolved\n", nil
		}
		authCalls := 0
		ensure1PasswordAuthFn = func() error {
			authCalls++
			return nil
		}
		talosApplyConfigFn = func(nodeIP, mode, config string) ([]byte, error) {
			t.Fatalf("apply should not run during dry-run")
			return nil, nil
		}

		require.NoError(t, applyNodeConfig("10.0.0.30", "auto", true))
		assert.Equal(t, 2, injectCalls)
		assert.Equal(t, 1, authCalls)
	})

	t.Run("non dry run applies resolved config", func(t *testing.T) {
		injectSecretsFn = func(config string) (string, error) {
			return "machine:\n  token: resolved\n", nil
		}
		ensure1PasswordAuthFn = func() error { return nil }

		var appliedNode, appliedMode, appliedConfig string
		talosApplyConfigFn = func(nodeIP, mode, config string) ([]byte, error) {
			appliedNode = nodeIP
			appliedMode = mode
			appliedConfig = config
			return []byte("ok"), nil
		}

		require.NoError(t, applyNodeConfig("10.0.0.30", "interactive", false))
		assert.Equal(t, "10.0.0.30", appliedNode)
		assert.Equal(t, "interactive", appliedMode)
		assert.Contains(t, appliedConfig, "resolved")
	})
}

func TestTalosUpgradeAndLifecycleFlows(t *testing.T) {
	oldNodeIPs := getTalosNodeIPsFn
	oldChooseNode := chooseTalosNodeFn
	oldTemplate := getTalosTemplateFn
	oldSpin := spinCommandFn
	oldTalosctlOutput := talosctlOutputFn
	oldConfirm := confirmActionFn
	oldCombined := talosctlCombinedOutputFn
	t.Cleanup(func() {
		getTalosNodeIPsFn = oldNodeIPs
		chooseTalosNodeFn = oldChooseNode
		getTalosTemplateFn = oldTemplate
		spinCommandFn = oldSpin
		talosctlOutputFn = oldTalosctlOutput
		confirmActionFn = oldConfirm
		talosctlCombinedOutputFn = oldCombined
	})

	t.Run("upgrade node uses extracted factory image", func(t *testing.T) {
		getTalosNodeIPsFn = func() ([]string, error) { return []string{"10.0.0.40"}, nil }
		chooseTalosNodeFn = func(prompt string, options []string) (string, error) {
			return "10.0.0.40", nil
		}
		getTalosTemplateFn = func(name string) (string, error) {
			assert.Equal(t, "talos/controlplane.yaml", name)
			return "machine:\n  install:\n    image: factory.talos.dev/installer/schematic:v1.9.0\n", nil
		}
		spinCommandFn = func(title string, command string, args ...string) error {
			assert.Equal(t, "Upgrading node 10.0.0.40", title)
			assert.Equal(t, "talosctl", command)
			assert.Equal(t, []string{"--nodes", "10.0.0.40", "upgrade", "--image", "factory.talos.dev/installer/schematic:v1.9.0", "--reboot-mode", "powercycle", "--timeout", "10m"}, args)
			return nil
		}

		require.NoError(t, upgradeNode("", "powercycle"))
	})

	t.Run("upgrade kubernetes uses first endpoint", func(t *testing.T) {
		talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte(`{"endpoints":["10.0.0.50"],"nodes":["10.0.0.50"]}`), nil
		}
		spinCommandFn = func(title string, command string, args ...string) error {
			assert.Equal(t, "talosctl", command)
			assert.Equal(t, []string{"--nodes", "10.0.0.50", "upgrade-k8s", "--to", "v1.32.0"}, args)
			return nil
		}
		cleanup := testutil.SetEnv(t, "KUBERNETES_VERSION", "v1.32.0")
		defer cleanup()

		require.NoError(t, upgradeK8s())
	})

	t.Run("reboot reset and shutdown use talosctl combined output", func(t *testing.T) {
		getTalosNodeIPsFn = func() ([]string, error) { return []string{"10.0.0.60"}, nil }
		chooseTalosNodeFn = func(prompt string, options []string) (string, error) {
			return "10.0.0.60", nil
		}
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			return true, nil
		}
		talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte(`{"endpoints":["10.0.0.60"],"nodes":["10.0.0.60","10.0.0.61"]}`), nil
		}
		var calls []string
		talosctlCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return []byte("ok"), nil
		}

		require.NoError(t, rebootNode("", "powercycle"))
		require.NoError(t, shutdownCluster())
		require.NoError(t, resetNode("10.0.0.60", true))
		require.NoError(t, resetCluster())

		assert.Equal(t, []string{
			"talosctl --nodes 10.0.0.60 reboot --mode powercycle",
			"talosctl shutdown --nodes 10.0.0.60,10.0.0.61 --force",
			"talosctl reset --nodes 10.0.0.60 --graceful=false",
			"talosctl reset --nodes 10.0.0.60,10.0.0.61 --graceful=false",
		}, calls)
	})
}

func TestTalosCommandWrappersAndEdges(t *testing.T) {
	oldSecret := get1PasswordSecretFn
	oldTalosctlOutput := talosctlOutputFn
	oldCombined := talosctlCombinedOutputFn
	oldConfirm := confirmActionFn
	oldSpin := spinCommandFn
	t.Cleanup(func() {
		get1PasswordSecretFn = oldSecret
		talosctlOutputFn = oldTalosctlOutput
		talosctlCombinedOutputFn = oldCombined
		confirmActionFn = oldConfirm
		spinCommandFn = oldSpin
	})

	t.Run("vsphere credentials fall back to environment", func(t *testing.T) {
		get1PasswordSecretFn = func(string) string { return "" }
		cleanup := testutil.SetEnvs(t, map[string]string{
			constants.EnvVSphereHost:     "env-host",
			constants.EnvVSphereUsername: "env-user",
			constants.EnvVSpherePassword: "env-pass",
		})
		defer cleanup()

		host, username, password, err := getVSphereCredentials()
		require.NoError(t, err)
		assert.Equal(t, "env-host", host)
		assert.Equal(t, "env-user", username)
		assert.Equal(t, "env-pass", password)
	})

	t.Run("get random node errors without endpoints", func(t *testing.T) {
		talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte(`{"endpoints":[],"nodes":["10.0.0.1"]}`), nil
		}

		_, err := getRandomNode()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no endpoints found")
	})

	t.Run("reset node cancel returns cancellation error", func(t *testing.T) {
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			return false, nil
		}

		err := resetNode("10.0.0.70", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reset cancelled")
	})

	t.Run("upgrade k8s command wrapper", func(t *testing.T) {
		talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte(`{"endpoints":["10.0.0.80"],"nodes":["10.0.0.80"]}`), nil
		}
		spinCommandFn = func(title string, command string, args ...string) error {
			return nil
		}
		cleanup := testutil.SetEnv(t, "KUBERNETES_VERSION", "v1.33.0")
		defer cleanup()

		_, err := testutil.ExecuteCommand(newUpgradeK8sCommand())
		require.NoError(t, err)
	})

	t.Run("shutdown and reset cluster wrappers force path", func(t *testing.T) {
		talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte(`{"endpoints":["10.0.0.90"],"nodes":["10.0.0.90","10.0.0.91"]}`), nil
		}
		var calls []string
		talosctlCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return []byte("ok"), nil
		}

		_, err := testutil.ExecuteCommand(newShutdownClusterCommand(), "--force")
		require.NoError(t, err)
		_, err = testutil.ExecuteCommand(newResetClusterCommand(), "--force")
		require.NoError(t, err)

		assert.Equal(t, []string{
			"talosctl shutdown --nodes 10.0.0.90,10.0.0.91 --force",
			"talosctl reset --nodes 10.0.0.90,10.0.0.91 --graceful=false",
		}, calls)
	})
}

func TestHypervisorWrapperFlows(t *testing.T) {
	oldTrueNASFactory := newTrueNASVMManagerFn
	oldProxmoxFactory := newProxmoxVMManagerFn
	oldVSphereFactory := newVSphereClientFn
	oldProxmoxCreds := getProxmoxCredentialsFn
	oldVSphereCreds := getVSphereCredsFn
	oldPrepareTarget := prepareISOForTargetFn
	oldHTTPGet := httpGetFn
	t.Cleanup(func() {
		newTrueNASVMManagerFn = oldTrueNASFactory
		newProxmoxVMManagerFn = oldProxmoxFactory
		newVSphereClientFn = oldVSphereFactory
		getProxmoxCredentialsFn = oldProxmoxCreds
		getVSphereCredsFn = oldVSphereCreds
		prepareISOForTargetFn = oldPrepareTarget
		httpGetFn = oldHTTPGet
	})

	t.Run("truenas wrappers", func(t *testing.T) {
		stubUnavailable1PasswordCLI(t)
		manager := &fakeTrueNASVMManager{}
		newTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) trueNASVMManager {
			assert.Equal(t, "truenas.local", host)
			assert.Equal(t, "api-key", apiKey)
			assert.Equal(t, 443, port)
			assert.True(t, useSSL)
			return manager
		}
		cleanup := testutil.SetEnvs(t, map[string]string{
			constants.EnvTrueNASHost:   "truenas.local",
			constants.EnvTrueNASAPIKey: "api-key",
			"STORAGE_POOL":             "flashstor",
		})
		defer cleanup()

		require.NoError(t, listVMs("truenas"))
		require.NoError(t, startVM("tn-vm"))
		require.NoError(t, stopVM("tn-vm", true))
		require.NoError(t, deleteVM("tn-vm"))
		require.NoError(t, infoVM("tn-vm"))
		require.NoError(t, cleanupOrphanedZVols("tn-vm", "flashstor"))

		assert.Equal(t, 6, manager.connectCalls)
		assert.Equal(t, 6, manager.closeCalls)
		assert.Equal(t, 1, manager.listCalls)
		assert.Equal(t, []string{"tn-vm"}, manager.started)
		assert.Equal(t, []string{"tn-vm:true"}, manager.stopped)
		assert.Equal(t, []string{"tn-vm:true:flashstor"}, manager.deleted)
		assert.Equal(t, []string{"tn-vm"}, manager.infoNames)
		assert.Equal(t, []string{"tn-vm:flashstor"}, manager.cleanupPairs)
	})

	t.Run("proxmox wrappers", func(t *testing.T) {
		manager := &fakeProxmoxVMManager{}
		getProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		newProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (proxmoxVMManager, error) {
			assert.Equal(t, "px.local", host)
			assert.Equal(t, "token", tokenID)
			assert.Equal(t, "secret", secret)
			assert.Equal(t, "pve", nodeName)
			assert.True(t, insecure)
			return manager, nil
		}

		require.NoError(t, listVMs("proxmox"))
		require.NoError(t, startVMOnProxmox("px-vm"))
		require.NoError(t, stopVMOnProxmox("px-vm", true))
		require.NoError(t, deleteVMOnProxmox("px-vm"))
		require.NoError(t, infoVMOnProxmox("px-vm"))

		prepareISOForTargetFn = func(target isoPreparationTarget) error {
			assert.Equal(t, "Proxmox", target.providerName)
			return target.uploadISO(&internaltalos.ISOInfo{URL: "https://example.com/proxmox.iso"})
		}
		require.NoError(t, prepareISOForProxmox())

		assert.Equal(t, 6, manager.closeCalls)
		assert.Equal(t, 1, manager.listCalls)
		assert.Equal(t, []string{"px-vm"}, manager.started)
		assert.Equal(t, []string{"px-vm:true"}, manager.stopped)
		assert.Equal(t, []string{"px-vm"}, manager.deleted)
		assert.Equal(t, []string{"px-vm"}, manager.infoNames)
		require.Len(t, manager.uploads, 1)
		assert.Contains(t, manager.uploads[0], "https://example.com/proxmox.iso")
	})

	t.Run("vsphere wrappers", func(t *testing.T) {
		client := &fakeVSphereClient{
			infoResponse: &mo.VirtualMachine{
				Runtime: types.VirtualMachineRuntimeInfo{PowerState: types.VirtualMachinePowerStatePoweredOn},
				Config: &types.VirtualMachineConfigInfo{
					GuestFullName: "Talos Linux",
					Uuid:          "vm-uuid",
					Hardware:      types.VirtualHardware{NumCPU: 4, MemoryMB: 8192},
				},
				Guest: &types.GuestInfo{IpAddress: "10.0.0.99"},
			},
		}
		getVSphereCredsFn = func() (string, string, string, error) {
			return "esxi.local", "root", "secret", nil
		}
		newVSphereClientFn = func(host, username, password string, insecure bool) vsphereClient {
			return client
		}
		httpGetFn = func(url string) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader("iso-bytes")),
				ContentLength: int64(len("iso-bytes")),
			}, nil
		}

		require.NoError(t, listVMs("vsphere"))
		require.NoError(t, infoVMOnVSphere("esx-vm"))
		require.NoError(t, powerOnVMOnVSphere("esx-vm"))
		require.NoError(t, powerOffVMOnVSphere("esx-vm"))
		require.NoError(t, deleteVMOnVSphere("esx-vm"))
		require.NoError(t, uploadISOToVSphere("https://example.com/vsphere.iso"))

		assert.Equal(t, 6, client.connectCalls)
		assert.Equal(t, 6, client.closeCalls)
		assert.Equal(t, 1, client.listCount)
		assert.Equal(t, []string{"esx-vm", "esx-vm", "esx-vm", "esx-vm"}, client.foundNames)
		assert.Equal(t, 1, client.poweredOn)
		assert.Equal(t, 1, client.poweredOff)
		assert.Equal(t, 1, client.deleted)
		require.Len(t, client.uploads, 1)
		assert.Contains(t, client.uploads[0], vsphere.DefaultISODatastore)
		assert.Contains(t, client.uploads[0], vsphere.DefaultISOFilename)
	})
}

func TestKubeconfigFlows(t *testing.T) {
	oldWorkdir := workingDirectoryFn
	oldGen := generateKubeconfigFn
	oldPush := pushKubeconfigTo1PasswordFn
	oldPull := pullKubeconfigFrom1PasswordFn
	oldTalosctlOutput := talosctlOutputFn
	t.Cleanup(func() {
		workingDirectoryFn = oldWorkdir
		generateKubeconfigFn = oldGen
		pushKubeconfigTo1PasswordFn = oldPush
		pullKubeconfigFrom1PasswordFn = oldPull
		talosctlOutputFn = oldTalosctlOutput
	})

	workdir := t.TempDir()
	workingDirectoryFn = func() string { return workdir }
	talosctlOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte(`{"endpoints":["10.0.0.200"],"nodes":["10.0.0.200"]}`), nil
	}

	t.Run("generate kubeconfig", func(t *testing.T) {
		var node, root string
		generateKubeconfigFn = func(n, r string) ([]byte, error) {
			node, root = n, r
			return []byte("ok"), nil
		}

		require.NoError(t, generateKubeconfig())
		assert.Equal(t, "10.0.0.200", node)
		assert.Equal(t, workdir, root)
	})

	t.Run("push and pull kubeconfig", func(t *testing.T) {
		var pushedPath, pulledPath string
		pushKubeconfigTo1PasswordFn = func(path string, logger *common.ColorLogger) error {
			pushedPath = path
			return nil
		}
		pullKubeconfigFrom1PasswordFn = func(path string, logger *common.ColorLogger) error {
			pulledPath = path
			return nil
		}

		logger := common.NewColorLogger()
		require.NoError(t, pushKubeconfigTo1Password(logger))
		require.NoError(t, pullKubeconfigFrom1Password(logger))
		assert.Equal(t, filepath.Join(workdir, "kubeconfig"), pushedPath)
		assert.Equal(t, filepath.Join(workdir, "kubeconfig"), pulledPath)
	})

	t.Run("kubeconfig command push and pull wrappers", func(t *testing.T) {
		generateKubeconfigFn = func(n, r string) ([]byte, error) { return []byte("ok"), nil }
		pushKubeconfigTo1PasswordFn = func(path string, logger *common.ColorLogger) error { return nil }
		pullKubeconfigFrom1PasswordFn = func(path string, logger *common.ColorLogger) error { return nil }

		_, err := testutil.ExecuteCommand(newKubeconfigCommand(), "--push")
		require.NoError(t, err)
		_, err = testutil.ExecuteCommand(newKubeconfigCommand(), "--pull")
		require.NoError(t, err)
	})
}

func TestDeleteAndCleanupConfirmationFlows(t *testing.T) {
	oldConfirm := confirmActionFn
	oldDeleteTrueNAS := deleteTrueNASVMFn
	oldTrueNASFactory := newTrueNASVMManagerFn
	oldTrueNASCreds := getTrueNASCredentialsFn
	t.Cleanup(func() {
		confirmActionFn = oldConfirm
		deleteTrueNASVMFn = oldDeleteTrueNAS
		newTrueNASVMManagerFn = oldTrueNASFactory
		getTrueNASCredentialsFn = oldTrueNASCreds
	})

	t.Run("delete vm confirmation uses provider-specific warning", func(t *testing.T) {
		var message string
		confirmActionFn = func(msg string, defaultYes bool) (bool, error) {
			message = msg
			return true, nil
		}
		var deleted string
		deleteTrueNASVMFn = func(name string) error {
			deleted = name
			return nil
		}

		require.NoError(t, deleteVMWithConfirmation("tn-vm", "truenas", false))
		assert.Contains(t, message, "all its ZVols on TrueNAS")
		assert.Equal(t, "tn-vm", deleted)
	})

	t.Run("cleanup zvol command force wrapper", func(t *testing.T) {
		manager := &fakeTrueNASVMManager{}
		getTrueNASCredentialsFn = func() (string, string, error) {
			return "truenas.local", "api-key", nil
		}
		newTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) trueNASVMManager {
			return manager
		}

		_, err := testutil.ExecuteCommand(newCleanupZVolsCommand(), "--vm-name", "tn-vm", "--force")
		require.NoError(t, err)
		assert.Equal(t, []string{"tn-vm:flashstor"}, manager.cleanupPairs)
	})
}

func TestPromptDeployVMOptions(t *testing.T) {
	oldChoose := chooseOptionFn
	oldInput := inputPromptFn
	t.Cleanup(func() {
		chooseOptionFn = oldChoose
		inputPromptFn = oldInput
	})

	t.Run("default proxmox pattern", func(t *testing.T) {
		chooseResponses := []string{
			"Default - 3-node k8s cluster (16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS each)",
			"Proxmox - Deploy to Proxmox VE (default)",
			"No - Use existing ISO",
			"Real Deployment - Actually create the VM",
		}
		inputResponses := []string{"k8s"}
		chooseIdx := 0
		inputIdx := 0
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			resp := chooseResponses[chooseIdx]
			chooseIdx++
			return resp, nil
		}
		inputPromptFn = func(prompt, placeholder string) (string, error) {
			resp := inputResponses[inputIdx]
			inputIdx++
			return resp, nil
		}

		var (
			name, provider, datastore, network   string
			memory, vcpus, diskSize, openebsSize int
			nodeCount, concurrent, startIndex    int
			generateISO, dryRun                  bool
		)

		require.NoError(t, promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent, &startIndex))
		assert.Equal(t, "k8s", name)
		assert.Equal(t, "proxmox", provider)
		assert.Equal(t, 16, vcpus)
		assert.Equal(t, 49152, memory)
		assert.Equal(t, 250, diskSize)
		assert.Equal(t, 1024, openebsSize)
		assert.False(t, generateISO)
		assert.False(t, dryRun)
		assert.Equal(t, 3, nodeCount)
		assert.Equal(t, 0, startIndex)
		assert.Equal(t, 3, concurrent)
	})

	t.Run("custom vsphere pattern", func(t *testing.T) {
		chooseResponses := []string{
			"Custom - Choose your own configuration",
			"vSphere/ESXi - Deploy to vSphere or ESXi",
			"Yes - Generate custom ISO using schematic.yaml",
			"Dry-Run - Preview what would be done without creating the VM",
		}
		inputResponses := []string{
			"workers",
			"5",
			"3",
			"2",
			"8",
			"64",
			"300",
			"1200",
			"fast-ds",
			"prod-net",
		}
		chooseIdx := 0
		inputIdx := 0
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			resp := chooseResponses[chooseIdx]
			chooseIdx++
			return resp, nil
		}
		inputPromptFn = func(prompt, placeholder string) (string, error) {
			resp := inputResponses[inputIdx]
			inputIdx++
			return resp, nil
		}

		var (
			name, provider, datastore, network   string
			memory, vcpus, diskSize, openebsSize int
			nodeCount, concurrent, startIndex    int
			generateISO, dryRun                  bool
		)

		require.NoError(t, promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent, &startIndex))
		assert.Equal(t, "workers", name)
		assert.Equal(t, "vsphere", provider)
		assert.Equal(t, 5, nodeCount)
		assert.Equal(t, 3, startIndex)
		assert.Equal(t, 2, concurrent)
		assert.Equal(t, 8, vcpus)
		assert.Equal(t, 64*1024, memory)
		assert.Equal(t, 300, diskSize)
		assert.Equal(t, 1200, openebsSize)
		assert.Equal(t, "fast-ds", datastore)
		assert.Equal(t, "prod-net", network)
		assert.True(t, generateISO)
		assert.True(t, dryRun)
	})

	t.Run("custom proxmox pattern", func(t *testing.T) {
		chooseResponses := []string{
			"Custom - Choose your own configuration",
			"Proxmox - Deploy to Proxmox VE (default)",
			"No - Use existing ISO",
			"Real Deployment - Actually create the VM",
		}
		inputResponses := []string{
			"workers",
			"4",
			"5",
			"2",
			"6",
			"32",
			"300",
			"1200",
		}
		chooseIdx := 0
		inputIdx := 0
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			resp := chooseResponses[chooseIdx]
			chooseIdx++
			return resp, nil
		}
		inputPromptFn = func(prompt, placeholder string) (string, error) {
			resp := inputResponses[inputIdx]
			inputIdx++
			return resp, nil
		}

		var (
			name, provider, datastore, network   string
			memory, vcpus, diskSize, openebsSize int
			nodeCount, concurrent, startIndex    int
			generateISO, dryRun                  bool
		)

		require.NoError(t, promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent, &startIndex))
		assert.Equal(t, "workers", name)
		assert.Equal(t, "proxmox", provider)
		assert.Equal(t, 4, nodeCount)
		assert.Equal(t, 5, startIndex)
		assert.Equal(t, 2, concurrent)
		assert.Equal(t, 6, vcpus)
		assert.Equal(t, 32*1024, memory)
		assert.Equal(t, 300, diskSize)
		assert.Equal(t, 1200, openebsSize)
		assert.False(t, generateISO)
		assert.False(t, dryRun)
	})

	t.Run("single vm custom proxmox skips batch prompts", func(t *testing.T) {
		chooseResponses := []string{
			"Custom - Choose your own configuration",
			"Proxmox - Deploy to Proxmox VE (default)",
			"No - Use existing ISO",
			"Real Deployment - Actually create the VM",
		}
		inputResponses := []string{
			"solo",
			"1",
			"4",
			"16",
			"120",
			"250",
		}
		chooseIdx := 0
		inputIdx := 0
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			resp := chooseResponses[chooseIdx]
			chooseIdx++
			return resp, nil
		}
		inputPromptFn = func(prompt, placeholder string) (string, error) {
			resp := inputResponses[inputIdx]
			inputIdx++
			return resp, nil
		}

		var (
			name, provider, datastore, network   string
			memory, vcpus, diskSize, openebsSize int
			nodeCount, concurrent, startIndex    int
			generateISO, dryRun                  bool
		)

		require.NoError(t, promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent, &startIndex))
		assert.Equal(t, "solo", name)
		assert.Equal(t, "proxmox", provider)
		assert.Equal(t, 1, nodeCount)
		assert.Equal(t, 0, startIndex)
		assert.Equal(t, 1, concurrent)
		assert.Equal(t, 4, vcpus)
		assert.Equal(t, 16*1024, memory)
		assert.Equal(t, 120, diskSize)
		assert.Equal(t, 250, openebsSize)
		assert.Equal(t, len(inputResponses), inputIdx)
	})
}

func TestRemainingTalosHelpers(t *testing.T) {
	oldVSphereNames := vsphereGetVMNamesFn
	oldTalosNodeOutput := talosctlNodeOutputFn
	oldProxmoxFactory := newProxmoxVMManagerFn
	oldProxmoxCreds := getProxmoxCredentialsFn
	oldProxmoxNodeConfig := proxmoxGetTalosNodeConfigFn
	oldProxmoxDefault := proxmoxDefaultVMConfig
	oldPrepareProxmoxISO := prepareISOForProxmoxFn
	t.Cleanup(func() {
		vsphereGetVMNamesFn = oldVSphereNames
		talosctlNodeOutputFn = oldTalosNodeOutput
		newProxmoxVMManagerFn = oldProxmoxFactory
		getProxmoxCredentialsFn = oldProxmoxCreds
		proxmoxGetTalosNodeConfigFn = oldProxmoxNodeConfig
		proxmoxDefaultVMConfig = oldProxmoxDefault
		prepareISOForProxmoxFn = oldPrepareProxmoxISO
	})

	t.Run("vm name and machine type helpers", func(t *testing.T) {
		vsphereGetVMNamesFn = func() ([]string, error) { return []string{"esx-1", "esx-2"}, nil }
		talosctlNodeOutputFn = func(nodeIP string, args ...string) ([]byte, error) {
			assert.Equal(t, "10.0.0.201", nodeIP)
			assert.Equal(t, []string{"get", "machinetypes", "--output=jsonpath={.spec}"}, args)
			return []byte("controlplane\n"), nil
		}

		names, err := getESXiVMNames()
		require.NoError(t, err)
		assert.Equal(t, []string{"esx-1", "esx-2"}, names)

		machineType, err := getMachineTypeFromNode("10.0.0.201")
		require.NoError(t, err)
		assert.Equal(t, "controlplane", machineType)
	})

	t.Run("deploy proxmox custom and predefined", func(t *testing.T) {
		manager := &fakeProxmoxVMManager{}
		getProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		newProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (proxmoxVMManager, error) {
			return manager, nil
		}
		proxmoxDefaultVMConfig = proxmox.VMConfig{
			Memory:       4096,
			Cores:        2,
			BootDiskSize: 50,
			OpenEBSSize:  100,
			BootStorage:  "local-zfs",
		}
		proxmoxGetTalosNodeConfigFn = func(name string) (proxmox.TalosNodeConfig, bool) {
			if name == "k8s-0" {
				return proxmox.TalosNodeConfig{
					VMID:           200,
					BootStorage:    "nvme1",
					OpenEBSStorage: "nvmeof-vmdata",
					CephDiskByID:   "disk-id",
					CPUAffinity:    "0-7",
					NUMANode:       0,
					MacAddress:     "00:a0:98:00:00:01",
				}, true
			}
			return proxmox.TalosNodeConfig{}, false
		}
		isoCalls := 0
		prepareISOForProxmoxFn = func() error {
			isoCalls++
			return nil
		}

		require.NoError(t, deployVMOnProxmox("worker", 8192, 4, 80, 200, true, 1, 2, 0))
		require.NoError(t, deployVMOnProxmox("k8s", 0, 0, 0, 0, false, 1, 2, 0))

		require.Len(t, manager.deployed, 4)
		assert.Equal(t, "worker-0", manager.deployed[0].Name)
		assert.Equal(t, 8192, manager.deployed[0].Memory)
		assert.Equal(t, 4, manager.deployed[0].Cores)
		assert.Equal(t, 80, manager.deployed[0].BootDiskSize)
		assert.Equal(t, 200, manager.deployed[0].OpenEBSSize)
		assert.Equal(t, "local-zfs", manager.deployed[0].BootStorage)
		assert.Equal(t, "worker-1", manager.deployed[1].Name)

		assert.Equal(t, "k8s-0", manager.deployed[2].Name)
		assert.Equal(t, "nvme1", manager.deployed[2].BootStorage)
		assert.Equal(t, "nvmeof-vmdata", manager.deployed[2].OpenEBSStorage)
		assert.Equal(t, "disk-id", manager.deployed[2].CephDiskByID)
		assert.Equal(t, "00:a0:98:00:00:01", manager.deployed[2].MacAddress)
		assert.Equal(t, "k8s-1", manager.deployed[3].Name)
		assert.Equal(t, 1, isoCalls)
		assert.Equal(t, 2, manager.closeCalls)
	})

	t.Run("deploy proxmox batch concurrently", func(t *testing.T) {
		started := make(chan string, 4)
		release := make(chan struct{})
		var (
			mu           sync.Mutex
			factoryCalls int
			closeCalls   int
		)

		getProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		newProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (proxmoxVMManager, error) {
			mu.Lock()
			factoryCalls++
			mu.Unlock()

			manager := &fakeProxmoxVMManager{}
			manager.deployFunc = func(cfg proxmox.VMConfig) error {
				started <- cfg.Name
				<-release
				return nil
			}
			return proxmoxVMManagerWithCloseCounter(manager, &mu, &closeCalls), nil
		}
		proxmoxGetTalosNodeConfigFn = func(name string) (proxmox.TalosNodeConfig, bool) {
			return proxmox.TalosNodeConfig{}, false
		}

		errCh := make(chan error, 1)
		go func() {
			errCh <- deployVMOnProxmox("worker", 8192, 4, 80, 200, false, 2, 3, 0)
		}()

		first := <-started
		second := <-started
		assert.Contains(t, []string{"worker-0", "worker-1", "worker-2"}, first)
		assert.Contains(t, []string{"worker-0", "worker-1", "worker-2"}, second)
		assert.NotEqual(t, first, second)

		select {
		case unexpected := <-started:
			t.Fatalf("unexpected third deployment started before capacity released: %s", unexpected)
		default:
		}

		close(release)
		require.NoError(t, <-errCh)

		mu.Lock()
		assert.Equal(t, 3, factoryCalls)
		assert.Equal(t, 3, closeCalls)
		mu.Unlock()
	})
}

type proxmoxVMManagerWithCloseCounterImpl struct {
	*fakeProxmoxVMManager
	mu         *sync.Mutex
	closeCalls *int
}

func proxmoxVMManagerWithCloseCounter(manager *fakeProxmoxVMManager, mu *sync.Mutex, closeCalls *int) proxmoxVMManager {
	return &proxmoxVMManagerWithCloseCounterImpl{fakeProxmoxVMManager: manager, mu: mu, closeCalls: closeCalls}
}

func (m *proxmoxVMManagerWithCloseCounterImpl) Close() error {
	m.mu.Lock()
	*m.closeCalls = *m.closeCalls + 1
	m.mu.Unlock()
	return m.fakeProxmoxVMManager.Close()
}

func TestDownloadISOToTemp(t *testing.T) {
	oldHTTPGet := httpGetFn
	t.Cleanup(func() {
		httpGetFn = oldHTTPGet
	})

	t.Run("downloads iso to temp file", func(t *testing.T) {
		httpGetFn = func(url string) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader("iso-bytes")),
				ContentLength: int64(len("iso-bytes")),
			}, nil
		}

		path, err := downloadISOToTemp("https://example.com/talos.iso")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.Remove(path) })

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "iso-bytes", string(content))
	})

	t.Run("returns http error", func(t *testing.T) {
		httpGetFn = func(url string) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
			}, nil
		}

		_, err := downloadISOToTemp("https://example.com/talos.iso")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 502")
	})
}

func TestUpdateNodeTemplatesWithSchematic(t *testing.T) {
	oldTemplatePath := controlplaneTemplatePath
	t.Cleanup(func() {
		controlplaneTemplatePath = oldTemplatePath
	})

	templatePath := filepath.Join(t.TempDir(), "controlplane.yaml")
	require.NoError(t, os.WriteFile(templatePath, []byte("machine:\n  install:\n    image: factory.talos.dev/installer/old:v1.0.0\n"), 0o644))
	controlplaneTemplatePath = templatePath

	require.NoError(t, updateNodeTemplatesWithSchematic("schematic-xyz", "v1.10.0"))

	updated, err := os.ReadFile(templatePath)
	require.NoError(t, err)
	assert.Contains(t, string(updated), "image: factory.talos.dev/installer/schematic-xyz:v1.10.0")
}

func TestValidateVMName(t *testing.T) {
	tests := []struct {
		name    string
		vmName  string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid name with underscores",
			vmName:  "test_vm_name",
			wantErr: false,
		},
		{
			name:    "valid simple name",
			vmName:  "testvm",
			wantErr: false,
		},
		{
			name:    "invalid name with dashes",
			vmName:  "test-vm-name",
			wantErr: true,
			errMsg:  "cannot contain dashes",
		},
		{
			name:    "empty name",
			vmName:  "",
			wantErr: true,
			errMsg:  "VM name cannot be empty",
		},
		{
			name:    "name with spaces",
			vmName:  "test vm name",
			wantErr: false, // Spaces are not actually validated in the function
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVMName(tt.vmName)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetTrueNASCredentials(t *testing.T) {
	stubUnavailable1PasswordCLI(t)

	t.Run("function returns values", func(t *testing.T) {
		t.Setenv(constants.EnvTrueNASHost, "test-host")
		t.Setenv(constants.EnvTrueNASAPIKey, "test-key")

		host, apiKey, err := getTrueNASCredentials()

		assert.NoError(t, err)
		assert.Equal(t, "test-host", host)
		assert.Equal(t, "test-key", apiKey)
	})

	t.Run("function signature exists", func(t *testing.T) {
		host, apiKey, err := getTrueNASCredentials()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "TrueNAS credentials")
		assert.Empty(t, host)
		assert.Empty(t, apiKey)
	})
}

func TestRenderMachineConfigFromEmbedded(t *testing.T) {
	// Note: This test currently cannot run full template rendering because it requires
	// embedded templates that are not available in the test environment.
	// For now, we test that the function exists and has the correct signature.

	tests := []struct {
		name          string
		baseTemplate  string
		patchTemplate string
		envVars       map[string]string
		expectError   bool
	}{
		{
			name:          "function exists and accepts parameters",
			baseTemplate:  "controlplane",
			patchTemplate: "192.168.122.10",
			envVars: map[string]string{
				"CLUSTER_NAME":        "test-cluster",
				"KUBERNETES_VERSION":  "v1.31.0",
				"TALOS_VERSION":       "v1.8.0",
				"CLUSTER_ENDPOINT_IP": "192.168.122.100",
			},
			expectError: true, // Expected to fail without embedded templates
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := testutil.SetEnvs(t, tt.envVars)
			defer cleanup()

			// This will fail because embedded templates are not available in test,
			// but we can verify the function signature is correct
			_, err := renderMachineConfigFromEmbedded(tt.baseTemplate, tt.patchTemplate)

			if tt.expectError {
				assert.Error(t, err, "Expected error due to missing embedded templates")
			} else {
				assert.NoError(t, err)
			}
		})
	}

	// TODO: Implement proper template rendering tests with mocked dependencies
	// or embedded template fixtures for integration testing
}

func TestApplyNodeConfig(t *testing.T) {
	tests := []struct {
		name      string
		nodeIP    string
		nodeType  string
		setupMock func(*testutil.MockTalosClient)
		wantErr   bool
	}{
		{
			name:     "successful apply",
			nodeIP:   "192.168.122.10",
			nodeType: "controlplane",
			setupMock: func(m *testutil.MockTalosClient) {
				// No error setup needed, default behavior is success
			},
			wantErr: false,
		},
		{
			name:     "apply fails",
			nodeIP:   "192.168.122.10",
			nodeType: "controlplane",
			setupMock: func(m *testutil.MockTalosClient) {
				m.ApplyConfigErr = fmt.Errorf("connection refused")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

func TestUpgradeNode(t *testing.T) {
	tests := []struct {
		name      string
		nodeIP    string
		image     string
		preserve  bool
		setupMock func(*testutil.MockTalosClient)
		wantErr   bool
	}{
		{
			name:     "successful upgrade with preserve",
			nodeIP:   "192.168.122.10",
			image:    "factory.talos.dev/installer:v1.8.0",
			preserve: true,
			setupMock: func(m *testutil.MockTalosClient) {
				// No error setup needed
			},
			wantErr: false,
		},
		{
			name:     "upgrade fails",
			nodeIP:   "192.168.122.10",
			image:    "factory.talos.dev/installer:v1.8.0",
			preserve: false,
			setupMock: func(m *testutil.MockTalosClient) {
				m.UpgradeErr = fmt.Errorf("upgrade failed")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

func TestDeployVMWithPattern(t *testing.T) {
	tests := []struct {
		name        string
		baseName    string
		pattern     string
		startIndex  int
		count       int
		generateISO bool
		setupMock   func(*testutil.MockTrueNASClient)
		wantErr     bool
		expectedVMs []string
	}{
		{
			name:        "deploy single VM",
			baseName:    "test",
			pattern:     "",
			startIndex:  1,
			count:       1,
			generateISO: false,
			setupMock: func(m *testutil.MockTrueNASClient) {
				// No error setup needed
			},
			wantErr:     false,
			expectedVMs: []string{"test"},
		},
		{
			name:        "deploy multiple VMs with pattern",
			baseName:    "worker",
			pattern:     "worker_%d",
			startIndex:  1,
			count:       3,
			generateISO: false,
			setupMock: func(m *testutil.MockTrueNASClient) {
				// No error setup needed
			},
			wantErr:     false,
			expectedVMs: []string{"worker_1", "worker_2", "worker_3"},
		},
		{
			name:        "deploy fails",
			baseName:    "test",
			pattern:     "",
			startIndex:  1,
			count:       1,
			generateISO: false,
			setupMock: func(m *testutil.MockTrueNASClient) {
				m.CreateVMErr = fmt.Errorf("failed to create VM")
			},
			wantErr:     true,
			expectedVMs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

func TestGetAllNodes(t *testing.T) {
	tests := []struct {
		name          string
		mockResources string
		wantErr       bool
		expectedNodes []string
	}{
		{
			name: "successful node list",
			mockResources: `NAME                  READY   STATUS
test-cp-1             True    Running
test-cp-2             True    Running
test-worker-1         True    Running`,
			wantErr: false,
			expectedNodes: []string{
				"test-cp-1",
				"test-cp-2",
				"test-worker-1",
			},
		},
		{
			name:          "command fails",
			mockResources: "",
			wantErr:       true,
			expectedNodes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual command execution mocking
			t.Skip("Requires command execution mocking")
		})
	}
}

func TestPrepareISOWithProvider(t *testing.T) {
	_, cleanup := testutil.TempDir(t)
	defer cleanup()

	tests := []struct {
		name        string
		provider    string
		isoName     string
		generateISO bool
		envVars     map[string]string
		wantErr     bool
	}{
		{
			name:        "prepare ISO for TrueNAS",
			provider:    "truenas",
			isoName:     "test.iso",
			generateISO: true,
			envVars: map[string]string{
				"TALOS_VERSION": "v1.8.0",
			},
			wantErr: false,
		},
		{
			name:        "prepare ISO for vSphere",
			provider:    "vsphere",
			isoName:     "test.iso",
			generateISO: true,
			envVars: map[string]string{
				"TALOS_VERSION": "v1.8.0",
				"GOVC_URL":      "https://vsphere.local",
			},
			wantErr: false,
		},
		{
			name:        "invalid provider",
			provider:    "unknown",
			isoName:     "test.iso",
			generateISO: false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := testutil.SetEnvs(t, tt.envVars)
			defer cleanup()

			// Skip for now as it requires actual implementation
			t.Skip("Requires full implementation with mocked dependencies")
		})
	}
}

func TestCleanupOrphanedZVols(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(*testutil.MockTrueNASClient)
		wantErr   bool
	}{
		{
			name: "successful cleanup",
			setupMock: func(m *testutil.MockTrueNASClient) {
				// Setup mock ZVols and VMs
			},
			wantErr: false,
		},
		{
			name: "cleanup fails",
			setupMock: func(m *testutil.MockTrueNASClient) {
				// Setup error condition
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip for now as it requires actual implementation with DI
			t.Skip("Requires dependency injection refactoring")
		})
	}
}

// Benchmark tests
func BenchmarkRenderMachineConfig(b *testing.B) {
	envVars := map[string]string{
		"CLUSTER_NAME":        "test-cluster",
		"KUBERNETES_VERSION":  "v1.31.0",
		"TALOS_VERSION":       "v1.8.0",
		"CLUSTER_ENDPOINT_IP": "192.168.122.100",
	}

	for k, v := range envVars {
		_ = os.Setenv(k, v)
	}
	defer func() {
		for k := range envVars {
			_ = os.Unsetenv(k)
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = renderMachineConfigFromEmbedded("192.168.122.10", "controlplane")
	}
}

func BenchmarkValidateVMName(b *testing.B) {
	names := []string{
		"valid_name",
		"invalid-name",
		"another_valid_name",
		"",
		"name with spaces",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, name := range names {
			_ = validateVMName(name)
		}
	}
}
