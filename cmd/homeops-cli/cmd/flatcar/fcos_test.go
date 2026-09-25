package flatcar

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	fcosinternal "homeops-cli/internal/fcos"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"
	"homeops-cli/internal/truenas"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testFCOSImage = fcosinternal.QEMUImage{
	Stream:             "stable",
	Release:            "44.20260829.3.1",
	Location:           "https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/44.20260829.3.1/x86_64/fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2.xz",
	SHA256:             strings.Repeat("a", 64),
	UncompressedSHA256: strings.Repeat("b", 64),
}

// stubNodeOS makes every listed node report the given OS family (others flatcar).
func stubNodeOS(t *testing.T, osByNode map[string]string) {
	t.Helper()
	testutil.Swap(t, &nodeOSFn, func(name string) string {
		if family, ok := osByNode[name]; ok {
			return family
		}
		return versionconfig.OSFlatcar
	})
}

func TestNewFCOSCommandStructure(t *testing.T) {
	cmd := NewFCOSCommand()
	assert.Equal(t, "fcos", cmd.Name())
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"render-ignition", "gen-kubeadm", "deploy-vm", "os-status"} {
		assert.True(t, subs[name], name)
	}
	deploy, _, err := cmd.Find([]string{"deploy-vm"})
	require.NoError(t, err)
	stream := deploy.Flags().Lookup("stream")
	require.NotNil(t, stream)
	assert.Equal(t, "stable", stream.DefValue)
	// The Flatcar group gains no FCOS-only flag.
	flatcarDeploy, _, err := NewCommand().Find([]string{"deploy-vm"})
	require.NoError(t, err)
	assert.Nil(t, flatcarDeploy.Flags().Lookup("stream"))
}

func TestFCOSRenderIgnitionCommandUsesFCOSRenderer(t *testing.T) {
	defer stubVersions(t)()
	defer stubSecrets(t)()
	testutil.Swap(t, &renderIgnitionFn, func(flatcar.NodeEnv) ([]byte, error) {
		t.Fatal("fcos render-ignition must not use the Flatcar template")
		return nil, nil
	})
	var got fcosinternal.NodeEnv
	testutil.Swap(t, &renderFCOSIgnitionFn, func(env fcosinternal.NodeEnv) ([]byte, error) {
		got = env
		return []byte(`{"ignition":{"version":"3.6.0"}}`), nil
	})

	cmd := NewFCOSCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"render-ignition", "--node", "k8s-1"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), `"3.6.0"`)
	assert.Equal(t, "k8s-1", got.NodeName)
	assert.Equal(t, "v1.36.1", got.KubernetesVersion)
	assert.Equal(t, "ssh-ed25519 AAAATESTKEY", got.SSHAuthorizedKey)
}

func TestRenderIgnitionForOS(t *testing.T) {
	testutil.Swap(t, &renderIgnitionFn, func(flatcar.NodeEnv) ([]byte, error) { return []byte("flatcar"), nil })
	testutil.Swap(t, &renderFCOSIgnitionFn, func(fcosinternal.NodeEnv) ([]byte, error) { return []byte("fcos"), nil })
	for family, want := range map[string]string{"": "flatcar", "flatcar": "flatcar", "fcos": "fcos"} {
		got, err := renderIgnitionForOS(family, flatcar.NodeEnv{})
		require.NoError(t, err)
		assert.Equal(t, want, string(got), family)
	}
}

func TestSelectDeployNodes(t *testing.T) {
	// No os keys configured: the Flatcar default list is untouched.
	stubNodeOS(t, nil)
	got, err := selectDeployNodes(versionconfig.OSFlatcar, []string{"k8s-0", "k8s-1", "k8s-2"}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-1", "k8s-2"}, got)

	stubNodeOS(t, map[string]string{"k8s-2": versionconfig.OSFCOS})
	got, err = selectDeployNodes(versionconfig.OSFlatcar, []string{"k8s-0", "k8s-1", "k8s-2"}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-1"}, got, "the default list narrows to the group's OS")

	_, err = selectDeployNodes(versionconfig.OSFlatcar, []string{"k8s-2"}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `node "k8s-2" is configured os: fcos`)
	assert.Contains(t, err.Error(), "homeops-cli fcos deploy-vm")

	_, err = selectDeployNodes(versionconfig.OSFCOS, []string{"k8s-0"}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster.nodes[k8s-0].os: fcos")

	got, err = selectDeployNodes(versionconfig.OSFCOS, []string{"k8s-2"}, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-2"}, got)
}

func TestSelectDeployNodesFCOSDefaultUsesConfiguredNodes(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{Cluster: versionconfig.ClusterConfig{
		Nodes: []versionconfig.Node{{Name: "k8s-0", IP: "192.0.2.10"}, {Name: "k8s-1", IP: "192.0.2.11", OS: "fcos"}},
	}})
	t.Cleanup(reset)
	got, err := selectDeployNodes(versionconfig.OSFCOS, nil, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-1"}, got)

	reset()
	restore := versionconfig.SetForTesting(&versionconfig.Config{})
	t.Cleanup(restore)
	_, err = selectDeployNodes(versionconfig.OSFCOS, nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cluster nodes are configured os: fcos")
}

func TestFCOSDeployVMProxmoxStagesStreamImageAndUsesCoreOSKey(t *testing.T) {
	defer stubVersions(t)()
	stubNodeOS(t, map[string]string{"k8s-0": versionconfig.OSFCOS})
	testutil.Swap(t, &proxmoxDefaultVMConfig, func() proxmox.VMConfig { return proxmox.VMConfig{} })
	testutil.Swap(t, &getFlatcarNodeConfigFn, func(name string) (proxmox.FlatcarNodeConfig, bool) {
		return proxmox.FlatcarNodeConfig{Name: name, MacAddress: "00:a0:98:28:c8:83"}, true
	})
	testutil.Swap(t, &renderIgnitionFn, func(flatcar.NodeEnv) ([]byte, error) {
		t.Fatal("an FCOS deploy must never render Flatcar Ignition")
		return nil, nil
	})
	testutil.Swap(t, &renderFCOSIgnitionFn, func(fcosinternal.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.6.0"}}`), nil
	})
	testutil.Swap(t, &getProxmoxCredentialsFn, func() (string, string, string, string, error) {
		return "pve.example.test", "tid", "sec", "pve", nil
	})
	var requestedStream string
	testutil.Swap(t, &resolveFCOSImageFn, func(_ context.Context, stream string) (fcosinternal.QEMUImage, error) {
		requestedStream = stream
		return testFCOSImage, nil
	})
	var stagedOn ssh.SSHConfig
	var stageCommand string
	testutil.Swap(t, &stageFCOSImageFn, func(cfg ssh.SSHConfig, command string) error {
		stagedOn = cfg
		stageCommand = command
		return nil
	})
	testutil.Swap(t, &uploadIgnitionToPVEFn, func(string, string, string, string, []byte) error { return nil })
	mgr := &fakeMgr{}
	testutil.Swap(t, &newProxmoxVMManagerFn, func(string, string, string, string, bool) (proxmoxVMManager, error) {
		return mgr, nil
	})

	cmd := newFCOSDeployVMCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--nodes", "k8s-0", "--snippets-dir", "/var/lib/vz/snippets", "--stream", "stable"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "stable", requestedStream)
	assert.Equal(t, "pve.example.test", stagedOn.Host)
	assert.Contains(t, stageCommand, testFCOSImage.SHA256)
	assert.Contains(t, stageCommand, testFCOSImage.UncompressedSHA256)
	require.Len(t, mgr.deployed, 1)
	vm := mgr.deployed[0]
	assert.Equal(t, "fcos", vm.OSFamily, "selects the opt/com.coreos/config fw_cfg key")
	// Staged into the import storage and referenced by volume ID: PVE refuses
	// an import-from filesystem path from an API token.
	assert.Contains(t, stageCommand, "/var/lib/vz/import/fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2")
	assert.Equal(t, "local:import/fedora-coreos-44.20260829.3.1-qemu.x86_64.qcow2", vm.ImageDiskPath)
	assert.Equal(t, "/var/lib/vz/snippets/ignition-k8s-0.json", vm.IgnitionPath)
}

func TestFCOSDeployVMExplicitImageSkipsStreamAndDryRunNeverStages(t *testing.T) {
	defer stubVersions(t)()
	stubNodeOS(t, map[string]string{"k8s-0": versionconfig.OSFCOS})
	testutil.Swap(t, &renderFCOSIgnitionFn, func(fcosinternal.NodeEnv) ([]byte, error) {
		return []byte(`{}`), nil
	})
	testutil.Swap(t, &stageFCOSImageFn, func(ssh.SSHConfig, string) error {
		t.Fatal("dry-run / explicit image must not stage anything")
		return nil
	})
	testutil.Swap(t, &newProxmoxVMManagerFn, func(string, string, string, string, bool) (proxmoxVMManager, error) {
		t.Fatal("dry-run must not create a Proxmox VM manager")
		return nil, nil
	})

	resolved := 0
	testutil.Swap(t, &resolveFCOSImageFn, func(context.Context, string) (fcosinternal.QEMUImage, error) {
		resolved++
		return testFCOSImage, nil
	})

	cmd := newFCOSDeployVMCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--nodes", "k8s-0", "--dry-run"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, 1, resolved, "dry-run resolves the stream image to report it")
	assert.Contains(t, out.String(), "1 Fedora CoreOS VM(s) planned")

	cmd = newFCOSDeployVMCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--nodes", "k8s-0", "--dry-run", "--image-volume", "vm-ssd:vm-200-disk-0"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, 1, resolved, "an explicit image skips stream resolution")
}

func TestFCOSDeployVMStreamErrorsAreFatal(t *testing.T) {
	defer stubVersions(t)()
	stubNodeOS(t, map[string]string{"k8s-0": versionconfig.OSFCOS})
	testutil.Swap(t, &renderFCOSIgnitionFn, func(fcosinternal.NodeEnv) ([]byte, error) { return []byte(`{}`), nil })
	testutil.Swap(t, &resolveFCOSImageFn, func(context.Context, string) (fcosinternal.QEMUImage, error) {
		return fcosinternal.QEMUImage{}, errors.New("metadata unreachable")
	})
	cmd := newFCOSDeployVMCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--nodes", "k8s-0", "--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata unreachable")
}

func TestFlatcarDeployVMStillRequiresAnImage(t *testing.T) {
	err := validateDeployVMOptions(providerProxmox, deployVMOptions{osFamily: versionconfig.OSFlatcar, snippetsDir: "/s"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--image-path or --image-volume")
	require.NoError(t, validateDeployVMOptions(providerProxmox, deployVMOptions{osFamily: versionconfig.OSFCOS, snippetsDir: "/s"}))
}

func TestTrueNASDeployerPassesOSFamily(t *testing.T) {
	fake := &fakeTrueNASClient{}
	d := &truenasFlatcarDeployer{host: "nas", pool: "flashstor", client: fake, logger: common.NewColorLogger()}
	require.NoError(t, d.DeployNode(flatcarNode{name: "k8s-0", osFamily: versionconfig.OSFCOS}, "/mnt/flashstor/VM/ignition-k8s-0.json"))
	require.NoError(t, d.DeployNode(flatcarNode{name: "k8s-1"}, "/mnt/flashstor/VM/ignition-k8s-1.json"))
	require.Len(t, fake.deployed, 2)
	assert.Equal(t, "fcos", fake.deployed[0].OSFamily)
	assert.Equal(t, truenas.VMConfig{}.OSFamily, fake.deployed[1].OSFamily, "Flatcar nodes keep the zero value")
}
