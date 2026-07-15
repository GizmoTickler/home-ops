package flatcar

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vsphere"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeIgnitionSSHClient struct {
	commands []string
	uploads  []fakeIgnitionUpload
}

type fakeIgnitionUpload struct {
	path    string
	content []byte
}

func (f *fakeIgnitionSSHClient) Connect() error { return nil }
func (f *fakeIgnitionSSHClient) Close() error   { return nil }
func (f *fakeIgnitionSSHClient) ExecuteCommand(command string) (string, error) {
	f.commands = append(f.commands, command)
	return "", nil
}
func (f *fakeIgnitionSSHClient) UploadBytes(content []byte, remotePath string) error {
	f.uploads = append(f.uploads, fakeIgnitionUpload{
		path:    remotePath,
		content: append([]byte(nil), content...),
	})
	return nil
}
func (f *fakeIgnitionSSHClient) VerifyFile(string) (bool, int64, error) {
	return true, 123, nil
}

func stubVersions(t *testing.T) func() {
	t.Helper()
	orig := getVersionsFn
	getVersionsFn = func(string) *versionconfig.VersionConfig {
		return &versionconfig.VersionConfig{
			KubernetesVersion: "v1.36.1",
			KubeVipVersion:    "v0.8.9",
			PauseImage:        "registry.k8s.io/pause:3.10",
		}
	}
	return func() { getVersionsFn = orig }
}

func TestUploadIgnitionFileStreamsContentOnlyViaUploadPayload(t *testing.T) {
	const remotePath = "/snippets/node.ign"
	secretContent := []byte(`{"ignition":{"secret":"resolved-ssh-key-and-join-material"}}`)
	fake := &fakeIgnitionSSHClient{}
	testutil.Swap(t, &newIgnitionSSHClientFn, func(ssh.SSHConfig) ignitionSSHClient {
		return fake
	})

	require.NoError(t, uploadIgnitionFile(ssh.SSHConfig{Host: "pve", Username: "root", Port: "22"}, remotePath, secretContent))

	assert.NotContains(t, strings.Join(fake.commands, "\n"), string(secretContent))
	assert.NotContains(t, strings.Join(fake.commands, "\n"), base64.StdEncoding.EncodeToString(secretContent))
	require.Len(t, fake.uploads, 1)
	assert.Equal(t, remotePath, fake.uploads[0].path)
	assert.Equal(t, secretContent, fake.uploads[0].content)
}

func TestTrueNASIgnitionSSHConfigThreadsConfiguredKey(t *testing.T) {
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Hypervisors: versionconfig.HypervisorsConfig{TrueNAS: versionconfig.TrueNASConfig{SSHKey: "~/.ssh/keys/nas01-ssh"}},
	})
	defer restore()

	assert.Equal(t, ssh.SSHConfig{
		Host: "nas", Username: "admin", Port: "22", KeyPath: "~/.ssh/keys/nas01-ssh",
	}, trueNASIgnitionSSHConfig("nas", "admin", "22"))
}

// stubSecrets makes the config-sourced node identifiers deterministic (no
// real secret-backend access) so buildNodeEnv is hermetic.
func stubSecrets(t *testing.T) func() {
	t.Helper()
	return versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{DomainRef: "literal://example.test"},
		Secrets: map[string]string{
			versionconfig.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 AAAATESTKEY",
		},
	})
}

func TestDeployVMConfigDerivedFlagDefaultsAreLazy(t *testing.T) {
	versionconfig.ResetForTesting()
	t.Cleanup(versionconfig.ResetForTesting)

	fixturePath := writeFlatcarConfigFixture(t, t.TempDir())
	cmd := newDeployVMCommand()
	versionconfig.SetExplicitPath(fixturePath)

	opts := deployVMOptions{}
	applyDeployVMConfigDefaults(cmd, &opts)

	assert.Equal(t, "/fixture/snippets", opts.snippetsDir)
}

func writeFlatcarConfigFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "homeops.yaml")
	content := strings.Join([]string{
		"cluster:",
		"  name: fixture-cluster",
		"  endpoint: fixture.k8s.test",
		"hypervisors:",
		"  proxmox:",
		"    snippets_dir: /fixture/snippets",
		"secrets:",
		"  node_ssh_authorized_key: literal://ssh-ed25519 AAAATESTKEY",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestNewCommandStructure(t *testing.T) {
	cmd := NewCommand()
	assert.Equal(t, "flatcar", cmd.Name())
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	assert.True(t, subs["deploy-vm"])
	assert.True(t, subs["render-ignition"])
	assert.True(t, subs["gen-kubeadm"])
	assert.True(t, subs["save-pki"])
}

func TestRenderIgnitionUsesOutputFileCanonicalFlag(t *testing.T) {
	cmd := newRenderIgnitionCommand()

	require.NotNil(t, cmd.Flags().Lookup("output-file"))
	for _, name := range []string{"output", "out"} {
		legacy := cmd.Flags().Lookup(name)
		require.NotNil(t, legacy)
		assert.True(t, legacy.Hidden, name)
		assert.NotEmpty(t, legacy.Deprecated, name)
	}
	assert.Equal(t, "o", cmd.Flags().Lookup("output").Shorthand)
}

func TestChooseFlatcarNodeExplicitAndNonInteractiveRequiredError(t *testing.T) {
	node, err := chooseFlatcarNode("k8s-1", "reboot")
	require.NoError(t, err)
	assert.Equal(t, "k8s-1", node)

	node, err = chooseFlatcarNode("", "reboot")
	require.Error(t, err)
	assert.Empty(t, node)
	assert.Contains(t, err.Error(), "--node is required")
	assert.Contains(t, err.Error(), "k8s-0")
}

func TestResetNodeCommand(t *testing.T) {
	defer stubSecrets(t)()
	origConfirm, origReset := confirmActionFn, resetNodeFn
	var resetIP string
	confirmActionFn = func(string, bool) (bool, error) { return true, nil }
	resetNodeFn = func(sshUser, ip string) error { resetIP = ip; return nil }
	defer func() { confirmActionFn, resetNodeFn = origConfirm, origReset }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"reset-node", "--node", "k8s-1", "--force"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "192.168.122.11", resetIP)
}

func TestResetNodeCommandCancelled(t *testing.T) {
	origConfirm, origReset := confirmActionFn, resetNodeFn
	called := false
	confirmActionFn = func(string, bool) (bool, error) { return false, nil } // user declines
	resetNodeFn = func(string, string) error { called = true; return nil }
	defer func() { confirmActionFn, resetNodeFn = origConfirm, origReset }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"reset-node", "--node", "k8s-1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
	assert.False(t, called, "reset must not run when confirmation is declined")
}

func TestResetClusterCommand(t *testing.T) {
	defer stubSecrets(t)()
	origConfirm, origReset := confirmActionFn, resetNodeFn
	var order []string
	confirmActionFn = func(string, bool) (bool, error) { return true, nil }
	resetNodeFn = func(sshUser, ip string) error { order = append(order, ip); return nil }
	defer func() { confirmActionFn, resetNodeFn = origConfirm, origReset }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"reset-cluster", "--force"})
	require.NoError(t, cmd.Execute())
	// reverse node order so the init node (k8s-0) is reset last
	assert.Equal(t, []string{"192.168.122.12", "192.168.122.11", "192.168.122.10"}, order)
}

func TestRebootNodeCommand(t *testing.T) {
	defer stubSecrets(t)()
	origConfirm, origReboot := confirmActionFn, rebootNodeFn
	var rebootIP string
	confirmActionFn = func(string, bool) (bool, error) { return true, nil }
	rebootNodeFn = func(sshUser, ip string) error { rebootIP = ip; return nil }
	defer func() { confirmActionFn, rebootNodeFn = origConfirm, origReboot }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"reboot-node", "--node", "k8s-1", "--force"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "192.168.122.11", rebootIP)
}

func TestRebootNodeCommandCancelled(t *testing.T) {
	origConfirm, origReboot := confirmActionFn, rebootNodeFn
	called := false
	confirmActionFn = func(string, bool) (bool, error) { return false, nil } // user declines
	rebootNodeFn = func(string, string) error { called = true; return nil }
	defer func() { confirmActionFn, rebootNodeFn = origConfirm, origReboot }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"reboot-node", "--node", "k8s-1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
	assert.False(t, called, "reboot must not run when confirmation is declined")
}

func TestRebootNodeCommandUnknownNode(t *testing.T) {
	origReboot := rebootNodeFn
	called := false
	rebootNodeFn = func(string, string) error { called = true; return nil }
	defer func() { rebootNodeFn = origReboot }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"reboot-node", "--node", "does-not-exist", "--force"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flatcar node")
	assert.False(t, called, "reboot must not run for an unknown node")
}

func TestShutdownClusterCommand(t *testing.T) {
	defer stubSecrets(t)()
	origConfirm, origShutdown := confirmActionFn, shutdownNodeFn
	var order []string
	confirmActionFn = func(string, bool) (bool, error) { return true, nil }
	shutdownNodeFn = func(sshUser, ip string) error { order = append(order, ip); return nil }
	defer func() { confirmActionFn, shutdownNodeFn = origConfirm, origShutdown }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"shutdown-cluster", "--force"})
	require.NoError(t, cmd.Execute())
	// reverse node order so the init node (k8s-0) powers off last
	assert.Equal(t, []string{"192.168.122.12", "192.168.122.11", "192.168.122.10"}, order)
}

func TestPatchKubeconfigServer(t *testing.T) {
	in := "clusters:\n- cluster:\n    server: https://192.168.122.10:6443\n    name: x\n"
	out := patchKubeconfigServer(in, "192.168.123.253")
	assert.Contains(t, out, "server: https://192.168.123.253:6443")
	assert.NotContains(t, out, "192.168.122.10")
	assert.Contains(t, out, "name: x") // non-server lines untouched
}

func TestKubeconfigCommandFetchAndPush(t *testing.T) {
	defer stubSecrets(t)() // OpFlatcarSSHUser -> "" -> sshUser defaults "core"
	origFetch, origSave := fetchAdminKubeconfigFn, saveKubeconfigFn
	var saved []byte
	fetchAdminKubeconfigFn = func(sshUser, ip string) (string, error) {
		assert.Equal(t, "core", sshUser)
		assert.Equal(t, "192.168.122.10", ip)
		return "apiVersion: v1\nclusters:\n- cluster:\n    server: https://1.2.3.4:6443\n", nil
	}
	saveKubeconfigFn = func(b []byte, _ *common.ColorLogger) error { saved = b; return nil }
	defer func() { fetchAdminKubeconfigFn, saveKubeconfigFn = origFetch, origSave }()

	out := filepath.Join(t.TempDir(), "kubeconfig")
	cmd := NewCommand()
	cmd.SetArgs([]string{"kubeconfig", "--node", "k8s-0", "--output", out, "--push"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), "server: https://192.168.123.253:6443", "server patched to VIP")
	assert.Contains(t, string(saved), "192.168.123.253", "pushed content is the patched kubeconfig")
}

func TestKubeconfigCommandPull(t *testing.T) {
	origPull, origFetch := pullKubeconfigFn, fetchAdminKubeconfigFn
	var pulledTo string
	pullKubeconfigFn = func(dest string, _ *common.ColorLogger) error { pulledTo = dest; return nil }
	fetchAdminKubeconfigFn = func(string, string) (string, error) {
		t.Fatal("--pull must not fetch from a node")
		return "", nil
	}
	defer func() { pullKubeconfigFn, fetchAdminKubeconfigFn = origPull, origFetch }()

	out := filepath.Join(t.TempDir(), "kc")
	cmd := NewCommand()
	cmd.SetArgs([]string{"kubeconfig", "--output", out, "--pull"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, out, pulledTo)
}

func TestSavePKICommand(t *testing.T) {
	defer stubSecrets(t)() // OpFlatcarSSHUser -> "" so sshUser defaults to "core"
	var saved map[string]string
	origCap, origSave := capturePKIFn, savePKIFn
	capturePKIFn = func(sshUser, ip string) (map[string]string, error) {
		assert.Equal(t, "core", sshUser)
		assert.Equal(t, "192.168.122.10", ip)
		return map[string]string{"ca_crt": "X", "ca_key": "Y"}, nil
	}
	savePKIFn = func(f map[string]string) error { saved = f; return nil }
	defer func() { capturePKIFn = origCap; savePKIFn = origSave }()

	cmd := newSavePKICommand()
	cmd.SetArgs([]string{"--node", "k8s-0"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, map[string]string{"ca_crt": "X", "ca_key": "Y"}, saved)
}

func TestBuildNodeEnv(t *testing.T) {
	defer stubVersions(t)()
	defer stubSecrets(t)()

	env, err := buildNodeEnv("k8s-0", "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "k8s-0", env.NodeName)
	assert.Equal(t, "192.168.122.10", env.NodeIP)
	assert.Equal(t, "192.168.123.253", env.ControlPlaneVIP)
	assert.Equal(t, "v1.36", env.KubernetesMinor)
	assert.Equal(t, "v0.8.9", env.KubeVipVersion)
	assert.Equal(t, "registry.k8s.io/pause:3.10", env.PauseImage)
	assert.Equal(t, "eth0", env.NodeInterface)
	assert.Equal(t, "k8s.example.test", env.K8sEndpoint)
	assert.Equal(t, "ssh-ed25519 AAAATESTKEY", env.SSHAuthorizedKey)

	_, err = buildNodeEnv("nope", "", "", "", "")
	require.Error(t, err)
}

func TestRenderIgnitionCommand(t *testing.T) {
	defer stubVersions(t)()
	orig := renderIgnitionFn
	defer func() { renderIgnitionFn = orig }()
	renderIgnitionFn = func(env flatcar.NodeEnv) ([]byte, error) {
		assert.Equal(t, "k8s-0", env.NodeName)
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	}

	cmd := newRenderIgnitionCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--node", "k8s-0"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "3.4.0")
}

// TestRenderIgnitionCommandWritesToNestedOutDir verifies --out creates any
// missing parent directories before writing, matching the kubeconfig command's
// behavior, rather than failing on os.WriteFile against a nonexistent path.
func TestRenderIgnitionCommandWritesToNestedOutDir(t *testing.T) {
	defer stubVersions(t)()
	orig := renderIgnitionFn
	defer func() { renderIgnitionFn = orig }()
	renderIgnitionFn = func(env flatcar.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	}

	outPath := filepath.Join(t.TempDir(), "nested", "dir", "ignition.json")
	cmd := newRenderIgnitionCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--node", "k8s-0", "--out", outPath})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "3.4.0")
	info, err := os.Stat(outPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestGenKubeadmCommandInit(t *testing.T) {
	defer stubVersions(t)()
	orig := renderKubeadmInitFn
	defer func() { renderKubeadmInitFn = orig }()
	renderKubeadmInitFn = func(env flatcar.NodeEnv) (string, error) {
		return "kind: InitConfiguration", nil
	}

	cmd := newGenKubeadmCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--node", "k8s-0", "--mode", "init"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "InitConfiguration")
}

func TestGenKubeadmCommandJoin(t *testing.T) {
	defer stubVersions(t)()
	orig := renderKubeadmJoinFn
	defer func() { renderKubeadmJoinFn = orig }()
	var captured flatcar.NodeEnv
	renderKubeadmJoinFn = func(env flatcar.NodeEnv) (string, error) {
		captured = env
		return "kind: JoinConfiguration", nil
	}

	// Join material must pass flatcar.ValidateJoinMaterial format checks.
	validToken := "abcdef.0123456789abcdef"
	validHash := "sha256:" + strings.Repeat("ab", 32)
	validCertKey := strings.Repeat("cd", 32)

	cmd := newGenKubeadmCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--node", "k8s-1", "--mode", "join", "--cert-key", validCertKey, "--token", validToken, "--ca-cert-hash", validHash})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "JoinConfiguration")
	assert.Equal(t, validCertKey, captured.CertificateKey)
	assert.Equal(t, validToken, captured.BootstrapToken)
	assert.Equal(t, validHash, captured.CACertHash)

	// Malformed material is rejected before rendering.
	badCmd := newGenKubeadmCommand()
	badCmd.SetOut(&bytes.Buffer{})
	badCmd.SetErr(&bytes.Buffer{})
	badCmd.SetArgs([]string{"--node", "k8s-1", "--mode", "join", "--token", "nope", "--ca-cert-hash", validHash})
	err := badCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --token")
}

func TestDeployVMDryRun(t *testing.T) {
	defer stubVersions(t)()
	origIgn := renderIgnitionFn
	defer func() { renderIgnitionFn = origIgn }()
	renderIgnitionFn = func(env flatcar.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	}

	// Ensure deploy is not attempted: stub the manager constructor to fail loudly.
	origMgr := newProxmoxVMManagerFn
	defer func() { newProxmoxVMManagerFn = origMgr }()
	newProxmoxVMManagerFn = func(string, string, string, string, bool) (proxmoxVMManager, error) {
		t.Fatal("dry-run must not create a Proxmox VM manager")
		return nil, nil
	}

	cmd := newDeployVMCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--nodes", "k8s-0", "--image-volume", "nvme1:vm-200-disk-0", "--dry-run"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "DRY RUN")
}

type fakeMgr struct {
	deployed []string
}

func (f *fakeMgr) Close() error { return nil }
func (f *fakeMgr) DeployVM(c proxmox.VMConfig) error {
	f.deployed = append(f.deployed, c.Name)
	return nil
}

func TestDeployVMRealPath(t *testing.T) {
	defer stubVersions(t)()
	testutil.Swap(t, &renderIgnitionFn, func(env flatcar.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	})

	testutil.Swap(t, &getProxmoxCredentialsFn, func() (string, string, string, string, error) {
		return "h", "tid", "sec", "pve", nil
	})

	mgr := &fakeMgr{}
	testutil.Swap(t, &newProxmoxVMManagerFn, func(string, string, string, string, bool) (proxmoxVMManager, error) {
		return mgr, nil
	})

	var uploadedTo string
	testutil.Swap(t, &uploadIgnitionToPVEFn, func(sshHost, sshUser, sshPort, remotePath string, content []byte) error {
		uploadedTo = sshHost + ":" + remotePath
		return nil
	})

	// Use a temp snippets dir; the Ignition upload to Proxmox is stubbed above.
	snip := t.TempDir()

	cmd := newDeployVMCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--nodes", "k8s-0", "--image-volume", "nvme1:vm-200-disk-0", "--snippets-dir", snip})
	require.NoError(t, cmd.Execute())
	require.Len(t, mgr.deployed, 1)
	assert.Equal(t, "k8s-0", mgr.deployed[0])
	// Ignition is uploaded to the Proxmox API host (default) at the snippets path.
	assert.Equal(t, "h:"+snip+"/ignition-k8s-0.json", uploadedTo)
}

// TestRunDeployVMRejectsUnsafeProxmoxOpts asserts deploy-vm refuses values that
// would break a Proxmox option string (import-from=, fw_cfg file=) or inject a
// shell command. Validation runs before any credential/render call, so these
// failure cases need no stubbing.
func TestRunDeployVMRejectsUnsafeProxmoxOpts(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	base := deployVMOptions{
		nodes:       []string{"k8s-0"},
		snippetsDir: "/var/lib/vz/snippets",
		imagePath:   "/mnt/flashstor/images/flatcar.raw",
		dryRun:      true,
	}

	cases := []struct {
		name   string
		mutate func(*deployVMOptions)
		want   string
	}{
		{"comma in image-path", func(o *deployVMOptions) { o.imagePath = "/mnt/a,b.raw" }, "--image-path"},
		{"semicolon in snippets-dir", func(o *deployVMOptions) { o.snippetsDir = "/var/lib/vz/snippets;reboot" }, "--snippets-dir"},
		{"space in image-volume", func(o *deployVMOptions) { o.imagePath = ""; o.imageVolume = "local-zfs:vm 1" }, "--image-volume"},
		{"command substitution in image-path", func(o *deployVMOptions) { o.imagePath = "/mnt/$(reboot).raw" }, "--image-path"},
		{"empty snippets-dir", func(o *deployVMOptions) { o.snippetsDir = "" }, "--snippets-dir"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := base
			c.mutate(&opts)
			err := runDeployVM(cmd, opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// fakeDeployer records the flatcarDeployer contract calls so deployFlatcarNodes
// can be tested independently of any hypervisor.
type fakeDeployer struct {
	mu        sync.Mutex
	staged    []string
	deployed  []string
	closed    bool
	stageErr  map[string]error
	deployErr map[string]error
}

func (f *fakeDeployer) StageIgnition(n flatcarNode) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.stageErr[n.name]; err != nil {
		return "", err
	}
	f.staged = append(f.staged, n.name)
	return "handle:" + n.name, nil
}

func (f *fakeDeployer) DeployNode(n flatcarNode, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.deployErr[n.name]; err != nil {
		return err
	}
	f.deployed = append(f.deployed, n.name+"="+handle)
	return nil
}

func (f *fakeDeployer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func TestDeployFlatcarNodes(t *testing.T) {
	logger := common.NewColorLogger()
	nodes := []flatcarNode{
		{name: "a", ignition: []byte("x")},
		{name: "b", ignition: []byte("y")},
	}

	t.Run("happy path wires staged handle into deploy and closes", func(t *testing.T) {
		f := &fakeDeployer{}
		require.NoError(t, deployFlatcarNodes(logger, f, nodes, 2))
		assert.Equal(t, []string{"a", "b"}, f.staged) // staging is sequential, in order
		assert.ElementsMatch(t, []string{"a=handle:a", "b=handle:b"}, f.deployed)
		assert.True(t, f.closed)
	})

	t.Run("a staging failure aborts before any deploy", func(t *testing.T) {
		f := &fakeDeployer{stageErr: map[string]error{"b": errors.New("upload boom")}}
		err := deployFlatcarNodes(logger, f, nodes, 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upload boom")
		assert.Equal(t, []string{"a"}, f.staged) // a staged, then b failed
		assert.Empty(t, f.deployed)              // no VM created when staging is incomplete
		assert.True(t, f.closed)
	})

	t.Run("deploy failures are aggregated", func(t *testing.T) {
		f := &fakeDeployer{deployErr: map[string]error{"a": errors.New("create boom")}}
		err := deployFlatcarNodes(logger, f, nodes, 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a: create boom")
		assert.Contains(t, err.Error(), "1/2")
		assert.ElementsMatch(t, []string{"b=handle:b"}, f.deployed) // b still succeeds
		assert.True(t, f.closed)
	})
}

func TestNormalizeFlatcarProvider(t *testing.T) {
	ok := map[string]string{
		"":          "proxmox",
		"proxmox":   "proxmox",
		"Proxmox":   "proxmox",
		"vsphere":   "vsphere",
		"esxi":      "vsphere",
		" VSPHERE ": "vsphere",
		"truenas":   "truenas",
		"TrueNAS":   "truenas",
	}
	for in, want := range ok {
		got, err := normalizeFlatcarProvider(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"aws", "xen", "hyperv"} {
		_, err := normalizeFlatcarProvider(bad)
		assert.Error(t, err, bad)
	}
}

// fakeVSphereClient records the CloneFlatcarVM calls so the deployer can be tested
// without a live vCenter.
type fakeVSphereClient struct {
	cloned []vsphere.VMConfig
	closed bool
	err    error
}

func (f *fakeVSphereClient) CloneFlatcarVM(c vsphere.VMConfig) error {
	f.cloned = append(f.cloned, c)
	return f.err
}

func (f *fakeVSphereClient) Close() error { f.closed = true; return nil }

func TestVSphereFlatcarDeployer(t *testing.T) {
	fake := &fakeVSphereClient{}
	d := &vsphereFlatcarDeployer{
		template:  "flatcar-ova",
		datastore: "ds1",
		network:   "vl999",
		vcpus:     8,
		memory:    16384,
		powerOn:   true,
		logger:    common.NewColorLogger(),
		client:    fake, // pre-connected, bypassing newVSphereFlatcarClientFn
	}

	ign := []byte(`{"ignition":{"version":"3.4.0"}}`)
	handle, err := d.StageIgnition(flatcarNode{name: "k8s-0", ignition: ign})
	require.NoError(t, err)
	assert.Equal(t, base64.StdEncoding.EncodeToString(ign), handle, "vSphere handle is the base64 Ignition (no upload)")

	require.NoError(t, d.DeployNode(flatcarNode{name: "k8s-0", ignition: ign}, handle))
	require.Len(t, fake.cloned, 1)
	got := fake.cloned[0]
	assert.Equal(t, "k8s-0", got.Name)
	assert.Equal(t, "flatcar-ova", got.TemplateName)
	assert.Equal(t, "ds1", got.Datastore)
	assert.Equal(t, "vl999", got.Network)
	assert.Equal(t, 8, got.VCPUs)
	assert.Equal(t, 16384, got.Memory)
	assert.True(t, got.PowerOn)
	assert.Equal(t, handle, got.IgnitionData) // base64 handle flows straight into guestinfo

	require.NoError(t, d.Close())
	assert.True(t, fake.closed)
}

func TestVSphereFlatcarDeployerConnectCachesClientAndCloseHandlesNil(t *testing.T) {
	fake := &fakeVSphereClient{}
	var calls int
	testutil.Swap(t, &newVSphereFlatcarClientFn, func(host, username, password string, insecure bool) (vsphereFlatcarClient, error) {
		calls++
		assert.Equal(t, "vc.example.test", host)
		assert.Equal(t, "svc-user", username)
		assert.Equal(t, "svc-password", password)
		assert.True(t, insecure)
		return fake, nil
	})

	d := &vsphereFlatcarDeployer{
		host:     "vc.example.test",
		username: "svc-user",
		password: "svc-password",
		insecure: true,
	}

	first, err := d.connect()
	require.NoError(t, err)
	second, err := d.connect()
	require.NoError(t, err)

	assert.Same(t, first, second)
	assert.Equal(t, 1, calls)
	require.NoError(t, d.Close())
	assert.True(t, fake.closed)

	empty := &vsphereFlatcarDeployer{}
	require.NoError(t, empty.Close())
}

func TestVSphereFlatcarDeployerConnectWrapsConstructorError(t *testing.T) {
	testutil.Swap(t, &newVSphereFlatcarClientFn, func(string, string, string, bool) (vsphereFlatcarClient, error) {
		return nil, errors.New("dial failed")
	})

	_, err := (&vsphereFlatcarDeployer{}).connect()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to vSphere")
	assert.Contains(t, err.Error(), "dial failed")
}

func TestResolveVSphereCredentialsPrefersConfigSecretsAndFallsBackToEnv(t *testing.T) {
	testutil.Swap(t, &resolveSecretKeyFn, func(key string) string {
		switch key {
		case versionconfig.KeyVSphereHost:
			return "vc-from-config"
		case versionconfig.KeyVSphereUsername:
			return "user-from-config"
		case versionconfig.KeyVSpherePassword:
			return "password-from-config"
		default:
			return ""
		}
	})
	t.Setenv(constants.EnvVSphereHost, "vc-from-env")
	t.Setenv(constants.EnvVSphereUsername, "user-from-env")
	t.Setenv(constants.EnvVSpherePassword, "password-from-env")

	host, username, password, err := resolveVSphereCredentials()

	require.NoError(t, err)
	assert.Equal(t, "vc-from-config", host)
	assert.Equal(t, "user-from-config", username)
	assert.Equal(t, "password-from-config", password)

	testutil.Swap(t, &resolveSecretKeyFn, func(string) string { return "" })
	host, username, password, err = resolveVSphereCredentials()

	require.NoError(t, err)
	assert.Equal(t, "vc-from-env", host)
	assert.Equal(t, "user-from-env", username)
	assert.Equal(t, "password-from-env", password)
}

func TestResolveVSphereCredentialsErrorsWhenIncomplete(t *testing.T) {
	testutil.Swap(t, &resolveSecretKeyFn, func(string) string { return "" })
	t.Setenv(constants.EnvVSphereHost, "")
	t.Setenv(constants.EnvVSphereUsername, "user-only")
	t.Setenv(constants.EnvVSpherePassword, "")

	_, _, _, err := resolveVSphereCredentials()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "vSphere credentials not found")
	assert.Contains(t, err.Error(), constants.EnvVSphereHost)
}

func TestDeployVMVSphereRequiresTemplateAndDatastore(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := runDeployVM(cmd, deployVMOptions{provider: "vsphere", nodes: []string{"k8s-0"}, datastore: "ds1", dryRun: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--vsphere-template")

	err = runDeployVM(cmd, deployVMOptions{provider: "esxi", nodes: []string{"k8s-0"}, vsphereTemplate: "t", dryRun: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--datastore")
}

func TestDeployVMVSphereDryRun(t *testing.T) {
	defer stubVersions(t)()
	origIgn := renderIgnitionFn
	defer func() { renderIgnitionFn = origIgn }()
	renderIgnitionFn = func(env flatcar.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	}
	// Dry-run must neither connect to vSphere nor resolve credentials.
	origClient := newVSphereFlatcarClientFn
	defer func() { newVSphereFlatcarClientFn = origClient }()
	newVSphereFlatcarClientFn = func(string, string, string, bool) (vsphereFlatcarClient, error) {
		t.Fatal("dry-run must not connect to vSphere")
		return nil, nil
	}

	cmd := newDeployVMCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--provider", "vsphere", "--nodes", "k8s-0", "--vsphere-template", "flatcar-ova", "--datastore", "ds1", "--dry-run"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "DRY RUN")
}

func TestDeployVSphereRealPathUsesCredentialAndClientSeams(t *testing.T) {
	testutil.Swap(t, &getVSphereCredentialsFn, func() (string, string, string, error) {
		return "vc.example.test", "svc-user", "svc-password", nil
	})
	fake := &fakeVSphereClient{}
	testutil.Swap(t, &newVSphereFlatcarClientFn, func(host, username, password string, insecure bool) (vsphereFlatcarClient, error) {
		assert.Equal(t, "vc.example.test", host)
		assert.Equal(t, "svc-user", username)
		assert.Equal(t, "svc-password", password)
		return fake, nil
	})
	t.Setenv(constants.EnvVSphereInsecure, "true")

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	nodes := []flatcarNode{{name: "k8s-0", ignition: []byte(`{"ignition":{"version":"3.4.0"}}`)}}
	opts := deployVMOptions{
		vsphereTemplate: "flatcar-ova",
		datastore:       "ds1",
		vsphereNetwork:  "vl999",
		vcpus:           8,
		memory:          16384,
		powerOn:         true,
		concurrent:      1,
	}

	err := deployVSphere(cmd, opts, common.NewColorLogger(), nodes)

	require.NoError(t, err)
	assert.True(t, fake.closed)
	require.Len(t, fake.cloned, 1)
	got := fake.cloned[0]
	assert.Equal(t, "k8s-0", got.Name)
	assert.Equal(t, "flatcar-ova", got.TemplateName)
	assert.Equal(t, "ds1", got.Datastore)
	assert.Equal(t, "vl999", got.Network)
	assert.Equal(t, 8, got.VCPUs)
	assert.Equal(t, 16384, got.Memory)
	assert.True(t, got.PowerOn)
	assert.Equal(t, base64.StdEncoding.EncodeToString(nodes[0].ignition), got.IgnitionData)
}

// fakeTrueNASClient records DeployVM calls so the deployer can be tested without
// a live TrueNAS.
type fakeTrueNASClient struct {
	deployed  []truenas.VMConfig
	connected bool
	closed    bool
	err       error
}

func (f *fakeTrueNASClient) Connect() error { f.connected = true; return nil }
func (f *fakeTrueNASClient) DeployVM(c truenas.VMConfig) error {
	f.deployed = append(f.deployed, c)
	return f.err
}
func (f *fakeTrueNASClient) Close() error { f.closed = true; return nil }

func TestTrueNASFlatcarDeployer(t *testing.T) {
	fake := &fakeTrueNASClient{}

	var uploadedTo string
	origUpload := uploadIgnitionToNASFn
	defer func() { uploadIgnitionToNASFn = origUpload }()
	uploadIgnitionToNASFn = func(sshHost, sshUser, sshPort, remotePath string, content []byte) error {
		uploadedTo = sshHost + ":" + remotePath
		return nil
	}

	d := &truenasFlatcarDeployer{
		host:          "nas",
		apiKey:        "k",
		port:          443,
		useSSL:        true,
		sshHost:       "nas",
		sshUser:       "truenas_admin",
		ignitionDir:   "/mnt/flashstor/VM",
		pool:          "flashstor",
		networkBridge: "br0",
		logger:        common.NewColorLogger(),
		client:        fake, // pre-connected, bypassing newTrueNASFlatcarClientFn
	}

	ign := []byte(`{"ignition":{"version":"3.4.0"}}`)
	handle, err := d.StageIgnition(flatcarNode{name: "k8s-0", ignition: ign})
	require.NoError(t, err)
	assert.Equal(t, "/mnt/flashstor/VM/ignition-k8s-0.json", handle, "handle is the host path the .ign was written to")
	assert.Equal(t, "nas:/mnt/flashstor/VM/ignition-k8s-0.json", uploadedTo)

	require.NoError(t, d.DeployNode(flatcarNode{name: "k8s-0", ignition: ign}, handle))
	require.Len(t, fake.deployed, 1)
	got := fake.deployed[0]
	assert.Equal(t, "k8s-0", got.Name)
	assert.True(t, got.Flatcar)
	assert.True(t, got.SkipZVolCreate)
	assert.Equal(t, handle, got.IgnitionPath) // fw_cfg file= path
	assert.Equal(t, "flashstor", got.StoragePool)
	assert.Equal(t, "br0", got.NetworkBridge)

	require.NoError(t, d.Close())
	assert.True(t, fake.closed)
}

func TestTrueNASFlatcarDeployerConnectCachesClientAndCloseHandlesNil(t *testing.T) {
	fake := &fakeTrueNASClient{}
	var calls int
	testutil.Swap(t, &newTrueNASFlatcarClientFn, func(host, apiKey string, port int, useSSL bool) (truenasFlatcarClient, error) {
		calls++
		assert.Equal(t, "nas.example.test", host)
		assert.Equal(t, "api-key-placeholder", apiKey)
		assert.Equal(t, 8443, port)
		assert.False(t, useSSL)
		return fake, nil
	})

	d := &truenasFlatcarDeployer{
		host:   "nas.example.test",
		apiKey: "api-key-placeholder",
		port:   8443,
		useSSL: false,
	}

	first, err := d.connect()
	require.NoError(t, err)
	second, err := d.connect()
	require.NoError(t, err)

	assert.Same(t, first, second)
	assert.Equal(t, 1, calls)
	require.NoError(t, d.Close())
	assert.True(t, fake.closed)

	empty := &truenasFlatcarDeployer{}
	require.NoError(t, empty.Close())
}

func TestTrueNASFlatcarDeployerConnectWrapsConstructorError(t *testing.T) {
	testutil.Swap(t, &newTrueNASFlatcarClientFn, func(string, string, int, bool) (truenasFlatcarClient, error) {
		return nil, errors.New("api unavailable")
	})

	_, err := (&truenasFlatcarDeployer{}).connect()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to TrueNAS")
	assert.Contains(t, err.Error(), "api unavailable")
}

func TestResolveTrueNASCredentialsPrefersConfigSecretsAndFallsBackToEnv(t *testing.T) {
	testutil.Swap(t, &resolveSecretKeyFn, func(key string) string {
		switch key {
		case versionconfig.KeyTrueNASHost:
			return "nas-from-config"
		case versionconfig.KeyTrueNASAPIKey:
			return "key-from-config"
		default:
			return ""
		}
	})
	t.Setenv(constants.EnvTrueNASHost, "nas-from-env")
	t.Setenv(constants.EnvTrueNASAPIKey, "key-from-env")

	host, apiKey, err := resolveTrueNASCredentials()

	require.NoError(t, err)
	assert.Equal(t, "nas-from-config", host)
	assert.Equal(t, "key-from-config", apiKey)

	testutil.Swap(t, &resolveSecretKeyFn, func(string) string { return "" })
	host, apiKey, err = resolveTrueNASCredentials()

	require.NoError(t, err)
	assert.Equal(t, "nas-from-env", host)
	assert.Equal(t, "key-from-env", apiKey)
}

func TestResolveTrueNASCredentialsErrorsWhenIncomplete(t *testing.T) {
	testutil.Swap(t, &resolveSecretKeyFn, func(string) string { return "" })
	t.Setenv(constants.EnvTrueNASHost, "nas-only")
	t.Setenv(constants.EnvTrueNASAPIKey, "")

	_, _, err := resolveTrueNASCredentials()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TrueNAS credentials not found")
	assert.Contains(t, err.Error(), constants.EnvTrueNASAPIKey)
}

func TestDeployVMTrueNASValidation(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := runDeployVM(cmd, deployVMOptions{provider: "truenas", nodes: []string{"k8s-0"}, dryRun: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--truenas-pool")

	err = runDeployVM(cmd, deployVMOptions{provider: "truenas", nodes: []string{"k8s-0", "k8s-1"}, truenasPool: "flashstor", bootZVol: "z", dryRun: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--boot-zvol")
}

func TestDeployVMTrueNASDryRun(t *testing.T) {
	defer stubVersions(t)()
	origIgn := renderIgnitionFn
	defer func() { renderIgnitionFn = origIgn }()
	renderIgnitionFn = func(env flatcar.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	}
	// Dry-run must neither connect to TrueNAS nor resolve credentials.
	origClient := newTrueNASFlatcarClientFn
	defer func() { newTrueNASFlatcarClientFn = origClient }()
	newTrueNASFlatcarClientFn = func(string, string, int, bool) (truenasFlatcarClient, error) {
		t.Fatal("dry-run must not connect to TrueNAS")
		return nil, nil
	}

	cmd := newDeployVMCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--provider", "truenas", "--nodes", "k8s-0", "--truenas-pool", "flashstor", "--dry-run"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "DRY RUN")
}

func TestDeployTrueNASRealPathUsesCredentialUploadAndClientSeams(t *testing.T) {
	testutil.Swap(t, &getTrueNASCredentialsFn, func() (string, string, error) {
		return "nas.example.test", "api-key-placeholder", nil
	})
	fake := &fakeTrueNASClient{}
	testutil.Swap(t, &newTrueNASFlatcarClientFn, func(host, apiKey string, port int, useSSL bool) (truenasFlatcarClient, error) {
		assert.Equal(t, "nas.example.test", host)
		assert.Equal(t, "api-key-placeholder", apiKey)
		assert.Equal(t, 443, port)
		assert.True(t, useSSL)
		return fake, nil
	})
	var uploadedTo string
	testutil.Swap(t, &uploadIgnitionToNASFn, func(sshHost, sshUser, sshPort, remotePath string, content []byte) error {
		uploadedTo = sshUser + "@" + sshHost + ":" + sshPort + ":" + remotePath + ":" + string(content)
		return nil
	})

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	nodes := []flatcarNode{{name: "k8s-0", ignition: []byte(`{"ignition":{"version":"3.4.0"}}`)}}
	opts := deployVMOptions{
		truenasPool:   "flashstor",
		networkBridge: "br-test",
		concurrent:    1,
	}

	err := deployTrueNAS(cmd, opts, common.NewColorLogger(), nodes)

	require.NoError(t, err)
	assert.True(t, fake.closed)
	assert.Equal(t, `truenas_admin@nas.example.test:22:/mnt/flashstor/VM/ignition-k8s-0.json:{"ignition":{"version":"3.4.0"}}`, uploadedTo)
	require.Len(t, fake.deployed, 1)
	got := fake.deployed[0]
	assert.Equal(t, "k8s-0", got.Name)
	assert.Equal(t, "flashstor", got.StoragePool)
	assert.Equal(t, "br-test", got.NetworkBridge)
	assert.Equal(t, "/mnt/flashstor/VM/ignition-k8s-0.json", got.IgnitionPath)
	assert.True(t, got.Flatcar)
	assert.True(t, got.SkipZVolCreate)
	assert.Equal(t, "nas.example.test", got.TrueNASHost)
	assert.Equal(t, 443, got.TrueNASPort)
	assert.False(t, got.NoSSL)
}

var _ = cobra.Command{}
