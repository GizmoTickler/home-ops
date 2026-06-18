package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vmlifecycle"

	"github.com/stretchr/testify/require"
)

func stubUnavailable1PasswordCLI(t *testing.T) {
	t.Helper()

	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
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

// fakeVMLifecycle records lifecycle calls for dispatch tests.
type fakeVMLifecycle struct {
	provider string
	calls    *[]string
	closed   *int
}

func (f *fakeVMLifecycle) ListVMs() error {
	*f.calls = append(*f.calls, "list-"+f.provider)
	return nil
}

func (f *fakeVMLifecycle) VMSummaries() ([]vmprov.VMSummary, error) {
	*f.calls = append(*f.calls, "list-"+f.provider)
	return []vmprov.VMSummary{{Name: "fake-vm", ID: "1", Status: "running"}}, nil
}

func (f *fakeVMLifecycle) StartVM(name string) error {
	*f.calls = append(*f.calls, "start-"+f.provider+":"+name)
	return nil
}

func (f *fakeVMLifecycle) StopVM(name string, force bool) error {
	*f.calls = append(*f.calls, fmt.Sprintf("stop-%s:%s:%t", f.provider, name, force))
	return nil
}

func (f *fakeVMLifecycle) DeleteVM(name string) error {
	*f.calls = append(*f.calls, "delete-"+f.provider+":"+name)
	return nil
}

func (f *fakeVMLifecycle) GetVMInfo(name string) error {
	*f.calls = append(*f.calls, "info-"+f.provider+":"+name)
	return nil
}

func (f *fakeVMLifecycle) RestartVM(name string) error {
	*f.calls = append(*f.calls, "restart-"+f.provider+":"+name)
	return nil
}

func (f *fakeVMLifecycle) SetVMResources(name string, memoryMB, cores int) error {
	*f.calls = append(*f.calls, fmt.Sprintf("set-%s:%s:%d:%d", f.provider, name, memoryMB, cores))
	return nil
}

func (f *fakeVMLifecycle) ResizeVMDisk(name, disk, spec string) error {
	*f.calls = append(*f.calls, fmt.Sprintf("resize-%s:%s:%s:%s", f.provider, name, disk, spec))
	return nil
}

func (f *fakeVMLifecycle) SnapshotVM(name, snap string) error {
	*f.calls = append(*f.calls, fmt.Sprintf("snap-create-%s:%s:%s", f.provider, name, snap))
	return nil
}

func (f *fakeVMLifecycle) ListVMSnapshots(name string) error {
	*f.calls = append(*f.calls, "snap-list-"+f.provider+":"+name)
	return nil
}

func (f *fakeVMLifecycle) RollbackVM(name, snap string) error {
	*f.calls = append(*f.calls, fmt.Sprintf("snap-rollback-%s:%s:%s", f.provider, name, snap))
	return nil
}

func (f *fakeVMLifecycle) DeleteVMSnapshot(name, snap string) error {
	*f.calls = append(*f.calls, fmt.Sprintf("snap-delete-%s:%s:%s", f.provider, name, snap))
	return nil
}

func (f *fakeVMLifecycle) Clone(name, newName string, opts vmprov.CloneOptions) error {
	*f.calls = append(*f.calls, fmt.Sprintf("clone-%s:%s:%s:%d:%t", f.provider, name, newName, opts.VMID, opts.Linked))
	return nil
}

func (f *fakeVMLifecycle) VMIPAddresses(name string) ([]string, error) {
	*f.calls = append(*f.calls, "ip-"+f.provider+":"+name)
	return []string{"10.0.0.50"}, nil
}

func (f *fakeVMLifecycle) ConsoleURL(name string) (string, error) {
	*f.calls = append(*f.calls, "console-"+f.provider+":"+name)
	return "https://console.example/" + name, nil
}

func (f *fakeVMLifecycle) Capabilities() vmprov.Capabilities { return vmprov.Capabilities{} }

func (f *fakeVMLifecycle) Close() error {
	if f.closed != nil {
		*f.closed++
	}
	return nil
}

// injectFakeVMLifecycle swaps the lifecycle factory for one returning fakes
// that record into the returned slice, restoring it after the test.
func injectFakeVMLifecycle(t *testing.T) (*[]string, *int) {
	t.Helper()
	oldFactory := vmlifecycle.NewVMLifecycleFn
	t.Cleanup(func() { vmlifecycle.NewVMLifecycleFn = oldFactory })

	calls := &[]string{}
	closed := new(int)
	vmlifecycle.NewVMLifecycleFn = func(normalizedProvider string) (vmprov.VMLifecycle, error) {
		return &fakeVMLifecycle{provider: normalizedProvider, calls: calls, closed: closed}, nil
	}
	return calls, closed
}
