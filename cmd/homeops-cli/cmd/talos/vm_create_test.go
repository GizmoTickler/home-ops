package talos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/proxmox"
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

	_, _, err = runVMCreate(t, "--name", "x", "--provider", "truenas")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxmox")
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

func TestVMSetDispatchesToManager(t *testing.T) {
	defer versionconfig.SetForTesting(nil)()
	orig := withProxmoxManagerFn
	called := false
	withProxmoxManagerFn = func(fn func(*proxmox.VMManager) error) error { called = true; return nil }
	t.Cleanup(func() { withProxmoxManagerFn = orig })

	set := newSetVMCommand()
	set.SetArgs([]string{"--name", "x", "--memory", "8192"})
	require.NoError(t, set.Execute())
	assert.True(t, called)
}
