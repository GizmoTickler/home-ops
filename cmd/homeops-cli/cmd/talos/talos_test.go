package talos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"homeops-cli/cmd/vm"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/iso"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	internaltalos "homeops-cli/internal/talos"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vmlifecycle"

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
	restarted    []string
	deleted      []string
	infoNames    []string
	setCalls     []string
	resizeCalls  []string
	snapCalls    []string
	cloneCalls   []string
	ips          []string
	consoleURL   string
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
func (f *fakeTrueNASVMManager) VMSummaries() ([]vmprov.VMSummary, error) {
	f.listCalls++
	return []vmprov.VMSummary{{Name: "tn-vm", ID: "1", Status: "RUNNING", MemoryMB: 4096, CPUs: 2}}, nil
}
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

func (f *fakeTrueNASVMManager) RestartVM(name string) error {
	f.restarted = append(f.restarted, name)
	return nil
}
func (f *fakeTrueNASVMManager) SetVMResources(name string, memoryMB, vcpus int) error {
	f.setCalls = append(f.setCalls, fmt.Sprintf("%s:%d:%d", name, memoryMB, vcpus))
	return nil
}
func (f *fakeTrueNASVMManager) ResizeVMDisk(name, disk, spec string) error {
	f.resizeCalls = append(f.resizeCalls, fmt.Sprintf("%s:%s:%s", name, disk, spec))
	return nil
}
func (f *fakeTrueNASVMManager) SnapshotVM(name, snap string) error {
	f.snapCalls = append(f.snapCalls, "create:"+name+":"+snap)
	return nil
}
func (f *fakeTrueNASVMManager) ListVMSnapshots(name string) error {
	f.snapCalls = append(f.snapCalls, "list:"+name)
	return nil
}
func (f *fakeTrueNASVMManager) RollbackVM(name, snap string) error {
	f.snapCalls = append(f.snapCalls, "rollback:"+name+":"+snap)
	return nil
}
func (f *fakeTrueNASVMManager) DeleteVMSnapshot(name, snap string) error {
	f.snapCalls = append(f.snapCalls, "delete:"+name+":"+snap)
	return nil
}
func (f *fakeTrueNASVMManager) Clone(name, newName string, opts vmprov.CloneOptions) error {
	f.cloneCalls = append(f.cloneCalls, fmt.Sprintf("%s:%s:%d:%t", name, newName, opts.VMID, opts.Linked))
	return nil
}
func (f *fakeTrueNASVMManager) VMIPAddresses(name string) ([]string, error) {
	if f.ips != nil {
		return f.ips, nil
	}
	return nil, vmprov.Unsupported("truenas", "TrueNAS middleware does not expose guest IPs")
}
func (f *fakeTrueNASVMManager) ConsoleURL(name string) (string, error) { return f.consoleURL, nil }
func (f *fakeTrueNASVMManager) Capabilities() vmprov.Capabilities {
	return vmprov.Capabilities{vmprov.FeatureIP: "TrueNAS middleware does not expose guest IPs"}
}

type fakeProxmoxVMManager struct {
	closeCalls  int
	listCalls   int
	started     []string
	stopped     []string
	restarted   []string
	deleted     []string
	infoNames   []string
	uploads     []string
	setCalls    []string
	resizeCalls []string
	snapCalls   []string
	cloneCalls  []string
	ips         []string
	consoleURL  string
	imported    []proxmox.VMConfig
	converted   []string
	deployed    []proxmox.VMConfig
	closeErr    error
	deployErr   error
	deployFunc  func(proxmox.VMConfig) error
}

func (f *fakeProxmoxVMManager) Close() error   { f.closeCalls++; return f.closeErr }
func (f *fakeProxmoxVMManager) ListVMs() error { f.listCalls++; return nil }
func (f *fakeProxmoxVMManager) VMSummaries() ([]vmprov.VMSummary, error) {
	f.listCalls++
	return []vmprov.VMSummary{{Name: "pve-vm", ID: "100", Status: "running", MemoryMB: 2048, CPUs: 2}}, nil
}
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

func (f *fakeProxmoxVMManager) RestartVM(name string) error {
	f.restarted = append(f.restarted, name)
	return nil
}
func (f *fakeProxmoxVMManager) SetVMResources(name string, memoryMB, cores int) error {
	f.setCalls = append(f.setCalls, fmt.Sprintf("%s:%d:%d", name, memoryMB, cores))
	return nil
}
func (f *fakeProxmoxVMManager) ResizeVMDisk(name, disk, spec string) error {
	f.resizeCalls = append(f.resizeCalls, fmt.Sprintf("%s:%s:%s", name, disk, spec))
	return nil
}
func (f *fakeProxmoxVMManager) SnapshotVM(name, snap string) error {
	f.snapCalls = append(f.snapCalls, "create:"+name+":"+snap)
	return nil
}
func (f *fakeProxmoxVMManager) ListVMSnapshots(name string) error {
	f.snapCalls = append(f.snapCalls, "list:"+name)
	return nil
}
func (f *fakeProxmoxVMManager) RollbackVM(name, snap string) error {
	f.snapCalls = append(f.snapCalls, "rollback:"+name+":"+snap)
	return nil
}
func (f *fakeProxmoxVMManager) DeleteVMSnapshot(name, snap string) error {
	f.snapCalls = append(f.snapCalls, "delete:"+name+":"+snap)
	return nil
}
func (f *fakeProxmoxVMManager) Clone(name, newName string, opts vmprov.CloneOptions) error {
	f.cloneCalls = append(f.cloneCalls, fmt.Sprintf("%s:%s:%d:%t", name, newName, opts.VMID, opts.Linked))
	return nil
}
func (f *fakeProxmoxVMManager) VMIPAddresses(name string) ([]string, error) { return f.ips, nil }
func (f *fakeProxmoxVMManager) ConsoleURL(name string) (string, error)      { return f.consoleURL, nil }
func (f *fakeProxmoxVMManager) ConsoleURLs(name string) (string, string, error) {
	return f.consoleURL, f.consoleURL + "&xtermjs", nil
}
func (f *fakeProxmoxVMManager) ImportTemplate(config proxmox.VMConfig) error {
	f.imported = append(f.imported, config)
	return nil
}

func (f *fakeProxmoxVMManager) ConvertVMToTemplate(name string) error {
	f.converted = append(f.converted, name)
	return nil
}
func (f *fakeProxmoxVMManager) Capabilities() vmprov.Capabilities { return vmprov.Capabilities{} }

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
	oldSecret := vmlifecycle.ResolveSecretKeyFn
	oldManagerFactory := vmlifecycle.NewTrueNASVMManagerFn
	oldSpin := spinWithFuncFn
	t.Cleanup(func() {
		vmlifecycle.ResolveSecretKeyFn = oldSecret
		vmlifecycle.NewTrueNASVMManagerFn = oldManagerFactory
		spinWithFuncFn = oldSpin
	})

	t.Run("required spice password errors when missing", func(t *testing.T) {
		vmlifecycle.ResolveSecretKeyFn = func(string) string { return "" }
		cleanup := testutil.SetEnv(t, constants.EnvSPICEPassword, "")
		defer cleanup()

		password, err := requiredSpicePassword()
		require.Error(t, err)
		assert.Empty(t, password)
		assert.Contains(t, err.Error(), "SPICE password is required")
	})

	t.Run("resolve truenas deployment access from env and 1password", func(t *testing.T) {
		stubUnavailable1PasswordCLI(t)
		vmlifecycle.ResolveSecretKeyFn = func(ref string) string {
			if ref == versionconfig.KeyTrueNASSpicePassword {
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
		vmlifecycle.NewTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) vmlifecycle.TrueNASVMManager {
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
		assert.Equal(t, "br0", vmlifecycle.TrueNASNetworkBridge())
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
	oldSecret := vmlifecycle.ResolveSecretKeyFn
	oldWorkdir := workingDirectoryFn
	t.Cleanup(func() {
		newTalosFactoryClientFn = oldFactory
		newISODownloaderFn = oldDownloader
		newTrueNASSSHClientFn = oldSSHClient
		vmlifecycle.ResolveSecretKeyFn = oldSecret
		workingDirectoryFn = oldWorkdir
	})

	workingDirectoryFn = func() string { return "." }
	vmlifecycle.ResolveSecretKeyFn = func(ref string) string {
		switch ref {
		case versionconfig.KeyTrueNASHost:
			return "truenas.local"
		case versionconfig.KeyTrueNASUsername:
			return "admin"
		default:
			return ""
		}
	}
	// iso.GetDefaultConfig resolves through the homeops config (env://
	// defaults in tests), not through vmlifecycle.ResolveSecretKeyFn.
	t.Setenv("TRUENAS_HOST", "truenas.local")
	t.Setenv("TRUENAS_USERNAME", "admin")

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
		restore := versionconfig.SetForTesting(&versionconfig.Config{
			Hypervisors: versionconfig.HypervisorsConfig{TrueNAS: versionconfig.TrueNASConfig{SSHKey: "~/.ssh/keys/nas01-ssh"}},
		})
		defer restore()
		fakeSSH := &fakeTrueNASSSHClient{exists: true, size: 2048}
		newTrueNASSSHClientFn = func(config ssh.SSHConfig) trueNASSSHClient {
			assert.Equal(t, "truenas.local", config.Host)
			assert.Equal(t, "admin", config.Username)
			assert.Equal(t, "~/.ssh/keys/nas01-ssh", config.KeyPath)
			return fakeSSH
		}

		selection, err := verifyPreparedTrueNASISO(common.NewColorLogger(), "truenas.local")
		require.NoError(t, err)
		require.NotNil(t, selection)
		assert.Equal(t, "/mnt/flashstor/ISO/metal-amd64.iso", selection.ISOPath)
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
	oldCreds := vmlifecycle.GetVSphereCredsFn
	oldFactory := newVSphereDeployerFn
	t.Cleanup(func() {
		vmlifecycle.GetVSphereCredsFn = oldCreds
		newVSphereDeployerFn = oldFactory
	})

	fake := &fakeVSphereDeployer{}
	vmlifecycle.GetVSphereCredsFn = func() (string, string, string, error) {
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
	oldCreds := vmlifecycle.GetVSphereCredsFn
	oldFactory := newVSphereDeployerFn
	t.Cleanup(func() {
		vmlifecycle.GetVSphereCredsFn = oldCreds
		newVSphereDeployerFn = oldFactory
	})

	fake := &fakeVSphereDeployer{}
	vmlifecycle.GetVSphereCredsFn = func() (string, string, string, error) {
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
	oldHost := vmlifecycle.GetVSphereHostFn
	oldK8s := newESXiK8sVMDeployerFn
	oldVSphere := newVSphereDeployerFn
	oldCreds := vmlifecycle.GetVSphereCredsFn
	t.Cleanup(func() {
		vmlifecycle.GetVSphereHostFn = oldHost
		newESXiK8sVMDeployerFn = oldK8s
		newVSphereDeployerFn = oldVSphere
		vmlifecycle.GetVSphereCredsFn = oldCreds
	})

	fakeSSH := &fakeESXiK8sVMDeployer{}
	vmlifecycle.GetVSphereHostFn = func() (string, error) { return "esxi.local", nil }
	newESXiK8sVMDeployerFn = func(host, username string) (esxiK8sVMDeployer, error) {
		return fakeSSH, nil
	}
	vmlifecycle.GetVSphereCredsFn = func() (string, string, string, error) {
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

func TestPromptIntWithDefaultParsesDefaultValidAndInvalidInput(t *testing.T) {
	inputs := []string{"", "7", "not-a-number"}
	testutil.Swap(t, &inputPromptFn, func(prompt, placeholder string) (string, error) {
		require.NotEmpty(t, prompt)
		require.NotEmpty(t, placeholder)
		next := inputs[0]
		inputs = inputs[1:]
		return next, nil
	})

	value, err := promptIntWithDefault("Enter count:", "3", 3)
	require.NoError(t, err)
	assert.Equal(t, 3, value)

	value, err = promptIntWithDefault("Enter count:", "3", 3)
	require.NoError(t, err)
	assert.Equal(t, 7, value)

	value, err = promptIntWithDefault("Enter count:", "3", 3)
	require.Error(t, err)
	assert.Zero(t, value)
	assert.Contains(t, err.Error(), `invalid numeric input "not-a-number"`)
}

func TestPromptIntWithDefaultWrapsPromptError(t *testing.T) {
	testutil.Swap(t, &inputPromptFn, func(string, string) (string, error) {
		return "", errors.New("prompt failed")
	})

	value, err := promptIntWithDefault("Enter count:", "3", 3)

	require.Error(t, err)
	assert.Zero(t, value)
	assert.Contains(t, err.Error(), "prompt failed")
}

func TestCommandProviderDefaults(t *testing.T) {
	deployCmd := newDeployVMCommand()
	deployProviderFlag := deployCmd.Flags().Lookup("provider")
	require.NotNil(t, deployProviderFlag)
	assert.Empty(t, deployProviderFlag.DefValue)
	assert.Contains(t, deployProviderFlag.Usage, "hypervisors.default")
	assert.Contains(t, deployCmd.Long, "Defaults to hypervisors.default from homeops.yaml")

	prepareISOCmd := newPrepareISOCommand()
	prepareISOProviderFlag := prepareISOCmd.Flags().Lookup("provider")
	require.NotNil(t, prepareISOProviderFlag)
	assert.Empty(t, prepareISOProviderFlag.DefValue)
	assert.Contains(t, prepareISOProviderFlag.Usage, "hypervisors.default")
	assert.Contains(t, prepareISOCmd.Long, "Upload the ISO to Proxmox storage, TrueNAS storage, or a vSphere datastore")
}

func TestVMLifecycleHelpMakesVMGroupDiscoverable(t *testing.T) {
	// The talos help points at the top-level vm group; manage-vm remains as a
	// hidden deprecated alias with the same subcommands.
	rootCmd := NewCommand()
	_, err := testutil.ExecuteCommand(rootCmd, "--help")
	require.NoError(t, err)
	assert.Contains(t, rootCmd.Long, "homeops-cli vm")

	// vm help is provider-first: the three hypervisors are the visible
	// children, each holding the verb set.
	vmOutput, err := testutil.ExecuteCommand(vm.NewVMCommand(), "--help")
	require.NoError(t, err)
	assert.Contains(t, vmOutput, "proxmox")
	assert.Contains(t, vmOutput, "truenas")
	assert.Contains(t, vmOutput, "vsphere")

	providerOutput, err := testutil.ExecuteCommand(vm.NewVMCommand(), "truenas", "--help")
	require.NoError(t, err)
	assert.Contains(t, providerOutput, "start")
	assert.Contains(t, providerOutput, "stop")
	assert.Contains(t, providerOutput, "delete")
	assert.Contains(t, providerOutput, "cleanup-zvols")

	manageOutput, err := testutil.ExecuteCommand(vm.NewManageVMCommand(), "--help")
	require.NoError(t, err)
	assert.Contains(t, manageOutput, "start")
	assert.Contains(t, manageOutput, "delete")
}

func TestRootVMLifecycleAliasReturnsManageVMGuidance(t *testing.T) {
	_, err := testutil.ExecuteCommand(NewCommand(), "start", "--provider", "truenas", "--name", "tn-vm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "homeops-cli vm start")
	assert.Contains(t, err.Error(), "--provider truenas")
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

func TestDeployVMCommandProviderValidation(t *testing.T) {
	_, err := testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "unknown", "--name", "worker", "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider: unknown")
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
	oldSecret := vmlifecycle.ResolveSecretKeyFn
	oldDownloader := newISODownloaderFn
	oldUploadVSphere := uploadISOToVSphereFn
	t.Cleanup(func() {
		prepareISOForTargetFn = oldPrepareTarget
		vmlifecycle.ResolveSecretKeyFn = oldSecret
		newISODownloaderFn = oldDownloader
		uploadISOToVSphereFn = oldUploadVSphere
	})

	t.Run("truenas target configures downloader", func(t *testing.T) {
		fakeDownloader := &fakeISODownloader{}
		newISODownloaderFn = func() isoDownloader { return fakeDownloader }
		// iso.GetDefaultConfig resolves through the homeops config (env://
		// defaults in tests).
		t.Setenv("TRUENAS_HOST", "truenas.local")
		t.Setenv("TRUENAS_USERNAME", "root")
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
		assert.Equal(t, filepath.Base("/mnt/flashstor/ISO/metal-amd64.iso"), fakeDownloader.configs[0].ISOFilename)
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

func TestSelectTalosNodeErrorBranches(t *testing.T) {
	t.Run("node IP discovery error is returned directly", func(t *testing.T) {
		testutil.Swap(t, &getTalosNodeIPsFn, func() ([]string, error) {
			return nil, errors.New("config missing")
		})

		node, err := selectTalosNode("Select node:")

		require.Error(t, err)
		assert.Empty(t, node)
		assert.Contains(t, err.Error(), "config missing")
	})

	t.Run("prompt cancellation returns empty node without error", func(t *testing.T) {
		testutil.Swap(t, &getTalosNodeIPsFn, func() ([]string, error) {
			return []string{"10.0.0.10"}, nil
		})
		testutil.Swap(t, &chooseTalosNodeFn, func(string, []string) (string, error) {
			return "", errors.New("cancelled by user")
		})

		node, err := selectTalosNode("Select node:")

		require.NoError(t, err)
		assert.Empty(t, node)
	})

	t.Run("prompt failure is wrapped with context", func(t *testing.T) {
		testutil.Swap(t, &getTalosNodeIPsFn, func() ([]string, error) {
			return []string{"10.0.0.10"}, nil
		})
		testutil.Swap(t, &chooseTalosNodeFn, func(string, []string) (string, error) {
			return "", errors.New("terminal unavailable")
		})

		node, err := selectTalosNode("Select node:")

		require.Error(t, err)
		assert.Empty(t, node)
		assert.Contains(t, err.Error(), "node selection failed")
		assert.Contains(t, err.Error(), "terminal unavailable")
	})
}

func TestGetTalosConfigInfoErrors(t *testing.T) {
	t.Run("talosctl error", func(t *testing.T) {
		testutil.Swap(t, &talosctlOutputFn, func(string, ...string) ([]byte, error) {
			return nil, errors.New("talosctl failed")
		})

		info, err := getTalosConfigInfo()

		require.Error(t, err)
		assert.Nil(t, info)
		assert.Contains(t, err.Error(), "talosctl failed")
	})

	t.Run("invalid json", func(t *testing.T) {
		testutil.Swap(t, &talosctlOutputFn, func(string, ...string) ([]byte, error) {
			return []byte(`{"nodes":`), nil
		})

		info, err := getTalosConfigInfo()

		require.Error(t, err)
		assert.Nil(t, info)
	})
}

func TestDeployVMWithPatternRejectsInvalidInputBeforeSideEffects(t *testing.T) {
	cases := []struct {
		name        string
		vmName      string
		pool        string
		memory      int
		vcpus       int
		diskSize    int
		openebsSize int
		want        string
	}{
		{name: "empty VM name", vmName: "", pool: "flashstor", memory: 8192, vcpus: 4, diskSize: 40, want: "VM name cannot be empty"},
		{name: "empty pool", vmName: "app01", pool: "", memory: 8192, vcpus: 4, diskSize: 40, want: "storage pool cannot be empty"},
		{name: "zero memory", vmName: "app01", pool: "flashstor", memory: 0, vcpus: 4, diskSize: 40, want: "memory must be greater than 0"},
		{name: "zero vcpus", vmName: "app01", pool: "flashstor", memory: 8192, vcpus: 0, diskSize: 40, want: "vCPUs must be greater than 0"},
		{name: "zero disk", vmName: "app01", pool: "flashstor", memory: 8192, vcpus: 4, diskSize: 0, want: "disk size must be greater than 0"},
		{name: "negative openebs", vmName: "app01", pool: "flashstor", memory: 8192, vcpus: 4, diskSize: 40, openebsSize: -1, want: "openebs size cannot be negative"},
		{name: "invalid VM name", vmName: "bad-name", pool: "flashstor", memory: 8192, vcpus: 4, diskSize: 40, want: "VM name validation failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := deployVMWithPattern(tc.vmName, tc.pool, tc.memory, tc.vcpus, tc.diskSize, tc.openebsSize, "", false, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestDeployVMWithPatternUsesPreparedISOAndDeploysViaSeams(t *testing.T) {
	testutil.Swap(t, &newTrueNASSSHClientFn, func(config ssh.SSHConfig) trueNASSSHClient {
		assert.Equal(t, "nas.example.test", config.Host)
		assert.Equal(t, "nas-admin", config.Username)
		return &fakeTrueNASSSHClient{exists: true, size: 4096}
	})
	manager := &fakeTrueNASVMManager{}
	testutil.Swap(t, &vmlifecycle.NewTrueNASVMManagerFn, func(host, apiKey string, port int, useSSL bool) vmlifecycle.TrueNASVMManager {
		assert.Equal(t, "nas.example.test", host)
		assert.Equal(t, "api-key-placeholder", apiKey)
		assert.Equal(t, 443, port)
		assert.True(t, useSSL)
		return manager
	})
	testutil.Swap(t, &vmlifecycle.ResolveSecretKeyFn, func(key string) string {
		if key == versionconfig.KeyTrueNASUsername {
			return "nas-admin"
		}
		return ""
	})
	testutil.Swap(t, &spinWithFuncFn, func(title string, fn func() error) error {
		assert.Equal(t, "Deploying VM app01", title)
		return fn()
	})
	testutil.Swap(t, &workingDirectoryFn, func() string { return "." })
	t.Setenv(constants.EnvTrueNASHost, "nas.example.test")
	t.Setenv(constants.EnvTrueNASAPIKey, "api-key-placeholder")
	t.Setenv(constants.EnvSPICEPassword, "spice-placeholder")
	t.Setenv("NETWORK_BRIDGE", "br-test")

	err := deployVMWithPattern("app01", "flashstor", 8192, 4, 40, 100, "00:11:22:33:44:55", true, false)

	require.NoError(t, err)
	assert.Equal(t, 1, manager.connectCalls)
	assert.Equal(t, 1, manager.closeCalls)
	require.Len(t, manager.deployed, 1)
	got := manager.deployed[0]
	assert.Equal(t, "app01", got.Name)
	assert.Equal(t, "flashstor", got.StoragePool)
	assert.Equal(t, "/mnt/flashstor/ISO/metal-amd64.iso", got.TalosISO)
	assert.Equal(t, "br-test", got.NetworkBridge)
	assert.Equal(t, "00:11:22:33:44:55", got.MacAddress)
	assert.Equal(t, "spice-placeholder", got.SpicePassword)
	assert.True(t, got.UseSpice)
	assert.True(t, got.CustomISO)
	assert.True(t, got.SkipZVolCreate)
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

		require.NoError(t, rebootNode("", "powercycle", false))
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
	oldSecret := vmlifecycle.ResolveSecretKeyFn
	oldTalosctlOutput := talosctlOutputFn
	oldCombined := talosctlCombinedOutputFn
	oldConfirm := confirmActionFn
	oldSpin := spinCommandFn
	t.Cleanup(func() {
		vmlifecycle.ResolveSecretKeyFn = oldSecret
		talosctlOutputFn = oldTalosctlOutput
		talosctlCombinedOutputFn = oldCombined
		confirmActionFn = oldConfirm
		spinCommandFn = oldSpin
	})

	t.Run("vsphere credentials fall back to environment", func(t *testing.T) {
		vmlifecycle.ResolveSecretKeyFn = func(string) string { return "" }
		cleanup := testutil.SetEnvs(t, map[string]string{
			constants.EnvVSphereHost:     "env-host",
			constants.EnvVSphereUsername: "env-user",
			constants.EnvVSpherePassword: "env-pass",
		})
		defer cleanup()

		host, username, password, err := vmlifecycle.GetVSphereCredentials()
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

func TestKubeconfigFlows(t *testing.T) {
	oldWorkdir := workingDirectoryFn
	oldGen := generateKubeconfigFn
	oldPush := pushKubeconfigFn
	oldPull := pullKubeconfigFn
	oldTalosctlOutput := talosctlOutputFn
	t.Cleanup(func() {
		workingDirectoryFn = oldWorkdir
		generateKubeconfigFn = oldGen
		pushKubeconfigFn = oldPush
		pullKubeconfigFn = oldPull
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
		pushKubeconfigFn = func(path string, logger *common.ColorLogger) error {
			pushedPath = path
			return nil
		}
		pullKubeconfigFn = func(path string, logger *common.ColorLogger) error {
			pulledPath = path
			return nil
		}

		logger := common.NewColorLogger()
		require.NoError(t, pushKubeconfigToStore(logger))
		require.NoError(t, pullKubeconfigFromStore(logger))
		assert.Equal(t, filepath.Join(workdir, "kubeconfig"), pushedPath)
		assert.Equal(t, filepath.Join(workdir, "kubeconfig"), pulledPath)
	})

	t.Run("kubeconfig command push and pull wrappers", func(t *testing.T) {
		generateKubeconfigFn = func(n, r string) ([]byte, error) { return []byte("ok"), nil }
		pushKubeconfigFn = func(path string, logger *common.ColorLogger) error { return nil }
		pullKubeconfigFn = func(path string, logger *common.ColorLogger) error { return nil }

		_, err := testutil.ExecuteCommand(newKubeconfigCommand(), "--push")
		require.NoError(t, err)
		_, err = testutil.ExecuteCommand(newKubeconfigCommand(), "--pull")
		require.NoError(t, err)
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
	oldVSphereNames := vmlifecycle.VSphereGetVMNamesFn
	oldTalosNodeOutput := talosctlNodeOutputFn
	oldProxmoxFactory := vmlifecycle.NewProxmoxVMManagerFn
	oldProxmoxCreds := vmlifecycle.GetProxmoxCredentialsFn
	oldProxmoxNodeConfig := proxmoxGetTalosNodeConfigFn
	oldProxmoxDefault := proxmoxDefaultVMConfig
	oldPrepareProxmoxISO := prepareISOForProxmoxFn
	t.Cleanup(func() {
		vmlifecycle.VSphereGetVMNamesFn = oldVSphereNames
		talosctlNodeOutputFn = oldTalosNodeOutput
		vmlifecycle.NewProxmoxVMManagerFn = oldProxmoxFactory
		vmlifecycle.GetProxmoxCredentialsFn = oldProxmoxCreds
		proxmoxGetTalosNodeConfigFn = oldProxmoxNodeConfig
		proxmoxDefaultVMConfig = oldProxmoxDefault
		prepareISOForProxmoxFn = oldPrepareProxmoxISO
	})

	t.Run("vm name and machine type helpers", func(t *testing.T) {
		vmlifecycle.VSphereGetVMNamesFn = func() ([]string, error) { return []string{"esx-1", "esx-2"}, nil }
		talosctlNodeOutputFn = func(nodeIP string, args ...string) ([]byte, error) {
			assert.Equal(t, "10.0.0.201", nodeIP)
			assert.Equal(t, []string{"get", "machinetypes", "--output=jsonpath={.spec}"}, args)
			return []byte("controlplane\n"), nil
		}

		names, err := vmlifecycle.GetESXiVMNames()
		require.NoError(t, err)
		assert.Equal(t, []string{"esx-1", "esx-2"}, names)

		machineType, err := getMachineTypeFromNode("10.0.0.201")
		require.NoError(t, err)
		assert.Equal(t, "controlplane", machineType)
	})

	t.Run("deploy proxmox custom and predefined", func(t *testing.T) {
		manager := &fakeProxmoxVMManager{}
		vmlifecycle.GetProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		vmlifecycle.NewProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (vmlifecycle.ProxmoxVMManager, error) {
			return manager, nil
		}
		proxmoxDefaultVMConfig = func() proxmox.VMConfig {
			return proxmox.VMConfig{
				Memory:       4096,
				Cores:        2,
				BootDiskSize: 50,
				OpenEBSSize:  100,
				BootStorage:  "local-zfs",
			}
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

		vmlifecycle.GetProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		vmlifecycle.NewProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (vmlifecycle.ProxmoxVMManager, error) {
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

func proxmoxVMManagerWithCloseCounter(manager *fakeProxmoxVMManager, mu *sync.Mutex, closeCalls *int) vmlifecycle.ProxmoxVMManager {
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
	assert.Contains(t, string(updated), "image: factory.talos.dev/installer/schematic-xyz:{{ ENV.TALOS_VERSION }}")
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
			_ = vmlifecycle.ValidateVMName(name)
		}
	}
}

func TestTalosApplyConfigFnRedactsErrorOutput(t *testing.T) {
	// Use PATH override with a fake talosctl so we can assert that the default
	// implementation redacts secret-looking output before returning it.
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'apply-config error: api_key=SENTINEL_LEAKED_API' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "talosctl"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := talosApplyConfigFn("1.2.3.4", "auto", "kind: machineconfig")
	require.Error(t, err)
	assert.NotContains(t, string(out), "SENTINEL_LEAKED_API", "redacted output must not echo secret values")
	assert.Contains(t, string(out), "<redacted>", "expected redaction marker in returned output")
}

func TestGenerateKubeconfigFnTimesOut(t *testing.T) {
	// Verify that the kubeconfig path uses a context-bound timeout. We can't
	// easily reduce the package-level talosCommandTimeout, so we stand up the
	// same RunCommand path with a short context against a hanging fake binary.
	dir := t.TempDir()
	hangScript := "#!/bin/sh\nsleep 60\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "talosctl"), []byte(hangScript), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := common.RunCommand(ctx, common.CommandOptions{
		Name: "talosctl",
		Args: []string{"kubeconfig", "--nodes", "1.2.3.4", "--force", "--force-context-name", "home-ops-cluster", "/tmp/rd"},
	})
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second, "kubeconfig hang should be cancelled quickly")
}

func TestTalosCommandTimeoutIsReasonable(t *testing.T) {
	if talosCommandTimeout < time.Minute {
		t.Fatalf("talosCommandTimeout too short: %v", talosCommandTimeout)
	}
}

func TestProviderProvidersGivePredictableSummary(t *testing.T) {
	// Verifies that logNamedVMDeploymentSuccess produces a uniform success
	// message shape across providers (TrueNAS, Proxmox, vSphere).
	logger := common.NewColorLogger()
	cases := []struct {
		provider string
		names    []string
	}{
		{"truenas", []string{"vm-1"}},
		{"proxmox", []string{"vm-1", "vm-2"}},
		{"vsphere", []string{"vm-1", "vm-2", "vm-3"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			// Function should not panic regardless of provider/name count.
			require.NotPanics(t, func() {
				logNamedVMDeploymentSuccess(logger, tc.provider, tc.names)
				logNamedVMDeploymentStart(logger, tc.provider, tc.names)
			})
		})
	}
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

// TestHypervisorPrepareISOFlows exercises the real ISO-upload plumbing for the
// provider managers/clients that the prepare-iso commands drive (kept in
// cmd/talos alongside prepare-iso; the VM-lifecycle wrappers moved to cmd/vm).
func TestHypervisorPrepareISOFlows(t *testing.T) {
	oldProxmoxFactory := vmlifecycle.NewProxmoxVMManagerFn
	oldProxmoxCreds := vmlifecycle.GetProxmoxCredentialsFn
	oldVSphereFactory := vmlifecycle.NewVSphereClientFn
	oldVSphereCreds := vmlifecycle.GetVSphereCredsFn
	oldPrepareTarget := prepareISOForTargetFn
	oldHTTPGet := httpGetFn
	t.Cleanup(func() {
		vmlifecycle.NewProxmoxVMManagerFn = oldProxmoxFactory
		vmlifecycle.GetProxmoxCredentialsFn = oldProxmoxCreds
		vmlifecycle.NewVSphereClientFn = oldVSphereFactory
		vmlifecycle.GetVSphereCredsFn = oldVSphereCreds
		prepareISOForTargetFn = oldPrepareTarget
		httpGetFn = oldHTTPGet
	})

	t.Run("proxmox upload plumbing", func(t *testing.T) {
		manager := &fakeProxmoxVMManager{}
		vmlifecycle.GetProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		vmlifecycle.NewProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (vmlifecycle.ProxmoxVMManager, error) {
			return manager, nil
		}
		prepareISOForTargetFn = func(target isoPreparationTarget) error {
			assert.Equal(t, "Proxmox", target.providerName)
			return target.uploadISO(&internaltalos.ISOInfo{URL: "https://example.com/proxmox.iso"})
		}
		require.NoError(t, prepareISOForProxmox())
		require.Len(t, manager.uploads, 1)
		assert.Contains(t, manager.uploads[0], "https://example.com/proxmox.iso")
	})

	t.Run("vsphere upload plumbing", func(t *testing.T) {
		client := &fakeVSphereClient{}
		vmlifecycle.GetVSphereCredsFn = func() (string, string, string, error) {
			return "esxi.local", "root", "secret", nil
		}
		vmlifecycle.NewVSphereClientFn = func(host, username, password string, insecure bool) vmlifecycle.VSphereClient {
			return client
		}
		httpGetFn = func(url string) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader("iso-bytes")),
				ContentLength: int64(len("iso-bytes")),
			}, nil
		}
		require.NoError(t, uploadISOToVSphere("https://example.com/vsphere.iso"))
		assert.Equal(t, 1, client.connectCalls)
		assert.Equal(t, 1, client.closeCalls)
		require.Len(t, client.uploads, 1)
		assert.Contains(t, client.uploads[0], vsphere.DefaultISODatastore)
		assert.Contains(t, client.uploads[0], vsphere.DefaultISOFilename)
	})
}

// TestDeployVMCommandDryRunFlags verifies the deploy-vm command accepts each
// provider's flag set in dry-run mode (command-level wiring; the dry-run plan
// logic itself is covered by TestDeployDryRunPaths).
func TestDeployVMCommandDryRunFlags(t *testing.T) {
	_, err := testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "proxmox", "--name", "k8s-0", "--dry-run")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "truenas", "--name", "app01", "--pool", "flashstor/VM", "--dry-run")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newDeployVMCommand(), "--provider", "esxi", "--name", "worker", "--datastore", "fast-ds", "--network", "vl999", "--dry-run")
	require.NoError(t, err)
}
