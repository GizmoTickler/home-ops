package vm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vmlifecycle"
	"homeops-cli/internal/vsphere"
)

// runVMCreate executes the create command with stubbed staging/deploy seams
// and returns what would have been deployed.
func runVMCreate(t *testing.T, args ...string) (proxmox.VMConfig, []string, error) {
	t.Helper()
	var deployed proxmox.VMConfig
	var staged []string
	origStage, origDeploy := stageImageFn, deployCloudInitVMFn
	stageImageFn = func(sshUser, host, url, destPath string) error {
		staged = []string{sshUser, host, url, destPath}
		return nil
	}
	deployCloudInitVMFn = func(cfg proxmox.VMConfig) error { deployed = cfg; return nil }
	t.Cleanup(func() { stageImageFn, deployCloudInitVMFn = origStage, origDeploy })

	cmd := newCreateVMCommand()
	cmd.SetArgs(args)
	err := cmd.Execute()
	return deployed, staged, err
}

func TestVMCreateUbuntuDefaults(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Secrets: map[string]string{
			versionconfig.KeyProxmoxHost:          "literal://pve.test",
			versionconfig.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 KEY",
		},
	})
	defer restore()

	deployed, staged, err := runVMCreate(t, "--name", "dev-vm")
	require.NoError(t, err)

	// staged onto the hypervisor from the latest Ubuntu cloud image
	require.Len(t, staged, 4)
	assert.Equal(t, "root", staged[0])
	assert.Equal(t, "pve.test", staged[1])
	assert.Contains(t, staged[2], "cloud-images.ubuntu.com")
	assert.Contains(t, staged[3], "/var/lib/vz/template/cache/")

	assert.Equal(t, "dev-vm", deployed.Name)
	assert.Equal(t, 4096, deployed.Memory)
	assert.Equal(t, 2, deployed.Cores)
	assert.Equal(t, 40, deployed.BootDiskSize)
	assert.Equal(t, staged[3], deployed.ImageDiskPath)
	require.NotNil(t, deployed.CloudInit)
	assert.Equal(t, "ubuntu", deployed.CloudInit.User)
	assert.Equal(t, "ssh-ed25519 KEY", deployed.CloudInit.SSHKeys)
	assert.Equal(t, "ip=dhcp", deployed.CloudInit.IPConfig)
	assert.True(t, deployed.PowerOn)
}

func TestVMCreateStaticIPAndOverrides(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Secrets: map[string]string{versionconfig.KeyProxmoxHost: "literal://pve.test"},
	})
	defer restore()

	deployed, _, err := runVMCreate(t, "--name", "rocky0", "--os", "rocky",
		"--memory", "8192", "--cores", "4", "--disk-gb", "80",
		"--ip", "192.168.120.50/22", "--gateway", "192.168.123.254",
		"--storage", "tank", "--bridge", "vmbr1", "--vlan", "999", "--user", "admin")
	require.NoError(t, err)

	assert.Equal(t, 8192, deployed.Memory)
	assert.Equal(t, 4, deployed.Cores)
	assert.Equal(t, 80, deployed.BootDiskSize)
	assert.Equal(t, "tank", deployed.BootStorage)
	assert.Equal(t, "vmbr1", deployed.NetworkBridge)
	assert.Equal(t, 999, deployed.VLANID)
	assert.Equal(t, "admin", deployed.CloudInit.User)
	assert.Equal(t, "ip=192.168.120.50/22,gw=192.168.123.254", deployed.CloudInit.IPConfig)
}

func TestVMCreateRHELRequiresImageConfig(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	_, _, err := runVMCreate(t, "--name", "rhel0", "--os", "rhel")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "images.rhel")
}

func TestVMCreateImagesConfigOverride(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Images:  map[string]string{"rhel": "/var/lib/vz/template/cache/rhel-10.1.qcow2"},
		Secrets: map[string]string{versionconfig.KeyProxmoxHost: "literal://pve.test"},
	})
	defer restore()

	deployed, staged, err := runVMCreate(t, "--name", "rhel0", "--os", "rhel")
	require.NoError(t, err)
	assert.Empty(t, staged, "local hypervisor paths must not be re-staged")
	assert.Equal(t, "/var/lib/vz/template/cache/rhel-10.1.qcow2", deployed.ImageDiskPath)
	assert.Equal(t, "cloud-user", deployed.CloudInit.User)
}

func TestVMCreateValidation(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	_, _, err := runVMCreate(t)
	require.Error(t, err) // --name required

	_, _, err = runVMCreate(t, "--name", "x", "--os", "windows")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown OS")

	// TrueNAS VM names cannot contain dashes.
	_, _, err = runVMCreate(t, "--name", "dev-vm", "--provider", "truenas")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot contain dashes")

	// Flags a provider cannot honour are rejected with the uniform message.
	_, _, err = runVMCreate(t, "--name", "dev0", "--provider", "truenas", "--vlan", "999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported on truenas")

	_, _, err = runVMCreate(t, "--name", "dev-vm", "--provider", "vsphere", "--storage", "ds1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported on vsphere")

	// vSphere needs a template (flag or config).
	_, _, err = runVMCreate(t, "--name", "dev-vm", "--provider", "vsphere")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--template")
}

func TestVMCreateTrueNASDispatch(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Hypervisors: versionconfig.HypervisorsConfig{
			TrueNAS: versionconfig.TrueNASConfig{
				ISODir:   "/mnt/tank/ISO",
				ImageDir: "/mnt/tank/images",
				VM:       versionconfig.VMDefaults{BootStorage: "tank/VM", NetworkBridge: "br7"},
			},
		},
		Secrets: map[string]string{
			versionconfig.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 KEY",
		},
	})
	defer restore()

	var got truenas.CloudImageVMConfig
	orig := createTrueNASCloudVMFn
	createTrueNASCloudVMFn = func(cfg truenas.CloudImageVMConfig) error { got = cfg; return nil }
	t.Cleanup(func() { createTrueNASCloudVMFn = orig })

	cmd := newCreateVMCommand()
	cmd.SetArgs([]string{"--provider", "truenas", "--name", "dev0", "--os", "ubuntu",
		"--memory", "2048", "--cores", "2", "--disk-gb", "30",
		"--ip", "192.168.120.50/22", "--gateway", "192.168.123.254", "--nameserver", "192.168.123.249"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "dev0", got.Name)
	assert.Equal(t, "tank/VM", got.Pool)
	assert.Equal(t, "/mnt/tank/images", got.ImageDir)
	assert.Equal(t, "br7", got.NetworkBridge)
	assert.Equal(t, 2048, got.MemoryMB)
	assert.Equal(t, 30, got.DiskGB)
	assert.Contains(t, got.ImageRef, "ubuntu")
	assert.NotEmpty(t, got.SeedISO)
	assert.True(t, got.PowerOn)
}

func TestTrueNASSSHConfigThreadsConfiguredKey(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Hypervisors: versionconfig.HypervisorsConfig{TrueNAS: versionconfig.TrueNASConfig{SSHKey: "~/.ssh/keys/nas01-ssh"}},
	})
	defer restore()

	assert.Equal(t, ssh.SSHConfig{
		Host: "nas", Username: "admin", Port: "22", KeyPath: "~/.ssh/keys/nas01-ssh",
	}, trueNASSSHConfig("nas", "admin"))
}

func TestVMCreateVSphereDispatch(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Hypervisors: versionconfig.HypervisorsConfig{
			VSphere: versionconfig.VSphereConfig{Template: "ubuntu-tpl"},
		},
		Secrets: map[string]string{
			versionconfig.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 KEY",
		},
	})
	defer restore()

	var got vsphere.CloudInitVMConfig
	orig := createVSphereCloudVMFn
	createVSphereCloudVMFn = func(cfg vsphere.CloudInitVMConfig) error { got = cfg; return nil }
	t.Cleanup(func() { createVSphereCloudVMFn = orig })

	cmd := newCreateVMCommand()
	cmd.SetArgs([]string{"--provider", "vsphere", "--name", "dev-vm", "--memory", "8192"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "ubuntu-tpl", got.TemplateName) // from config default
	assert.Equal(t, "dev-vm", got.Name)
	assert.Equal(t, 8192, got.MemoryMB)
	assert.Contains(t, got.Userdata, "#cloud-config")
	assert.Contains(t, got.Userdata, "ubuntu") // default user from --os default
	assert.Contains(t, got.Metadata, "instance-id")
	assert.True(t, got.PowerOn)
}

func TestVMTemplateImportDispatch(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Images: map[string]string{"rhel": "/var/lib/vz/template/cache/rhel.qcow2"},
		Secrets: map[string]string{
			versionconfig.KeyProxmoxHost:          "literal://pve.test",
			versionconfig.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 KEY",
		},
	})
	defer restore()

	var imported []proxmox.VMConfig
	origImport := importProxmoxTemplateFn
	importProxmoxTemplateFn = func(cfg proxmox.VMConfig) error { imported = append(imported, cfg); return nil }
	t.Cleanup(func() { importProxmoxTemplateFn = origImport })

	// Proxmox image import (local path: no staging).
	cmd := newVMTemplateImportCommand()
	cmd.SetArgs([]string{"--name", "rhel-tpl", "--os", "rhel"})
	require.NoError(t, cmd.Execute())
	require.Len(t, imported, 1)
	assert.Equal(t, "rhel-tpl", imported[0].Name)
	assert.Equal(t, "/var/lib/vz/template/cache/rhel.qcow2", imported[0].ImageDiskPath)
	require.NotNil(t, imported[0].CloudInit)
	assert.Equal(t, "cloud-user", imported[0].CloudInit.User)

	// TrueNAS: uniform unsupported.
	cmd = newVMTemplateImportCommand()
	cmd.SetArgs([]string{"--name", "x", "--provider", "truenas"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported on truenas")

	// vSphere image mode: uniform unsupported with the govc/--from-vm hint.
	cmd = newVMTemplateImportCommand()
	cmd.SetArgs([]string{"--name", "x", "--provider", "vsphere"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported on vsphere")
	assert.Contains(t, err.Error(), "--from-vm")

	// vSphere --from-vm conversion.
	var marked []string
	origMark := markVSphereTemplateFn
	markVSphereTemplateFn = func(name string) error { marked = append(marked, name); return nil }
	t.Cleanup(func() { markVSphereTemplateFn = origMark })
	cmd = newVMTemplateImportCommand()
	cmd.SetArgs([]string{"--from-vm", "golden", "--provider", "vsphere"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"golden"}, marked)
}

func TestVMHardwareCommandsValidate(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	set := newSetVMCommand()
	set.SetArgs([]string{"--memory", "8192"})
	require.Error(t, set.Execute()) // --name required

	resize := newResizeDiskCommand()
	resize.SetArgs([]string{"--name", "x"})
	require.Error(t, resize.Execute()) // --grow or --size required

	resize = newResizeDiskCommand()
	resize.SetArgs([]string{"--name", "x", "--grow", "10G", "--size", "50G"})
	require.Error(t, resize.Execute()) // mutually exclusive
}

func TestVMSetDispatchesToLifecycle(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	calls, closed := injectFakeVMLifecycle(t)

	set := newSetVMCommand()
	set.SetArgs([]string{"--name", "x", "--memory", "8192"})
	require.NoError(t, set.Execute())
	assert.Equal(t, []string{"set-proxmox:x:8192:0"}, *calls)
	assert.Equal(t, 1, *closed)
}

func TestVMHardwareLifecycleDispatch(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	calls, _ := injectFakeVMLifecycle(t)

	resize := newResizeDiskCommand()
	resize.SetArgs([]string{"--provider", "truenas", "--name", "web0", "--disk", "openebs", "--grow", "20G"})
	require.NoError(t, resize.Execute())

	restart := newRestartVMCommand()
	restart.SetArgs([]string{"--provider", "vsphere", "--name", "vc0"})
	require.NoError(t, restart.Execute())

	clone := newCloneVMCommand()
	clone.SetArgs([]string{"--name", "a", "--to", "b", "--vmid", "123"})
	require.NoError(t, clone.Execute())

	snapCreate := newSnapshotCommand()
	snapCreate.SetArgs([]string{"create", "--provider", "truenas", "--name", "web0", "--snap", "pre"})
	require.NoError(t, snapCreate.Execute())

	assert.Equal(t, []string{
		"resize-truenas:web0:openebs:+20G",
		"restart-vsphere:vc0",
		"clone-proxmox:a:b:123:false",
		"snap-create-truenas:web0:pre",
	}, *calls)
}

func TestVMConsoleAndIPDispatch(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	calls, _ := injectFakeVMLifecycle(t)

	console := newVMConsoleCommand()
	console.SetArgs([]string{"--provider", "vsphere", "vc0"})
	require.NoError(t, console.Execute())

	ip := newVMIPCommand()
	ip.SetArgs([]string{"--provider", "vsphere", "vc0"})
	require.NoError(t, ip.Execute())

	assert.Equal(t, []string{"console-vsphere:vc0", "ip-vsphere:vc0"}, *calls)
}

func TestVMPositionalCommandsPromptWhenNameless(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	calls, _ := injectFakeVMLifecycle(t)

	oldNames, oldChoose := vmlifecycle.GetProxmoxVMNamesFn, vmlifecycle.ChooseVMFunc
	vmlifecycle.GetProxmoxVMNamesFn = func() ([]string, error) { return []string{"dev0", "dev1"}, nil }
	vmlifecycle.ChooseVMFunc = func(prompt string, options []string) (string, error) {
		assert.Contains(t, prompt, "show IPs for")
		assert.Equal(t, []string{"dev0", "dev1"}, options)
		return "dev1", nil
	}
	t.Cleanup(func() { vmlifecycle.GetProxmoxVMNamesFn, vmlifecycle.ChooseVMFunc = oldNames, oldChoose })

	// No positional arg (the interactive-menu path): must prompt, not panic.
	ip := newVMIPCommand()
	ip.SetArgs([]string{})
	require.NoError(t, ip.Execute())
	assert.Equal(t, []string{"ip-proxmox:dev1"}, *calls)
}

func TestVMTreeIsProviderFirst(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	vm := NewVMCommand()

	providerGroups := 0
	for _, sub := range vm.Commands() {
		switch sub.Name() {
		case "proxmox", "truenas", "vsphere":
			providerGroups++
			assert.False(t, sub.Hidden, "provider group %q is the primary structure and must be visible", sub.Name())
			assert.True(t, sub.HasSubCommands(), "provider group %q must hold the verbs", sub.Name())
			hasCleanup := false
			for _, verb := range sub.Commands() {
				if verb.Name() == "cleanup-zvols" {
					hasCleanup = true
				}
			}
			assert.Equal(t, sub.Name() == "truenas", hasCleanup,
				"cleanup-zvols is TrueNAS-only and must appear exactly there (group %q)", sub.Name())
		default:
			assert.True(t, sub.Hidden, "flat verb %q must be a hidden shorthand", sub.Name())
		}
	}
	assert.Equal(t, 3, providerGroups)
}

func TestVMProviderGroupPinsProvider(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	calls, _ := injectFakeVMLifecycle(t)

	vm := NewVMCommand()
	vm.SetArgs([]string{"truenas", "restart", "--name", "web0"})
	require.NoError(t, vm.Execute())

	// snapshot's --provider is persistent on the group; pinning must reach it.
	vm = NewVMCommand()
	vm.SetArgs([]string{"vsphere", "snapshot", "create", "--name", "vc0", "--snap", "pre"})
	require.NoError(t, vm.Execute())

	assert.Equal(t, []string{
		"restart-truenas:web0",
		"snap-create-vsphere:vc0:pre",
	}, *calls)
}

func TestVMListOutputFormats(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	_, _ = injectFakeVMLifecycle(t)

	testutil.Swap(t, &vmlifecycle.EnsureVMLifecycleProviderFn, func(string, string) error { return nil })

	out, _, err := testutil.CaptureOutput(func() {
		cmd := newListVMsCommand()
		cmd.SetArgs([]string{"--provider", "truenas", "--output", "json"})
		require.NoError(t, cmd.Execute())
	})
	require.NoError(t, err)
	var inventory struct {
		Provider string `json:"provider"`
		VMs      []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"vms"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &inventory))
	assert.Equal(t, "truenas", inventory.Provider)
	require.Len(t, inventory.VMs, 1)
	assert.Equal(t, "fake-vm", inventory.VMs[0].Name)

	// yaml works; unknown format errors
	cmd := newListVMsCommand()
	cmd.SetArgs([]string{"--output", "yaml"})
	_, _, err = testutil.CaptureOutput(func() { require.NoError(t, cmd.Execute()) })
	require.NoError(t, err)

	cmd = newListVMsCommand()
	cmd.SetArgs([]string{"--output", "csv"})
	require.ErrorContains(t, cmd.Execute(), "unsupported output format")
}
