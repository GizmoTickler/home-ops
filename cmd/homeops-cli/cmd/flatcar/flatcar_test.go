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
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vsphere"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// stubSecrets makes the op-sourced node identifiers deterministic (no real
// 1Password access) so buildNodeEnv is hermetic.
func stubSecrets(t *testing.T) func() {
	t.Helper()
	orig := get1PasswordSecretFn
	get1PasswordSecretFn = func(ref string) string {
		switch ref {
		case constants.OpSecretDomain:
			return "example.test"
		case constants.OpFlatcarPublicKey:
			return "ssh-ed25519 AAAATESTKEY"
		}
		return ""
	}
	return func() { get1PasswordSecretFn = orig }
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

func TestBuildPKITemplate(t *testing.T) {
	// *.key fields must be CONCEALED; others STRING; deterministic order; notes first.
	tmpl := buildPKITemplate(map[string]string{"ca_crt": "AAA", "ca_key": "BBB", "etcd_ca_key": "CCC"})
	assert.Equal(t, "kubernetes-pki", tmpl.Title)
	assert.Equal(t, "SECURE_NOTE", tmpl.Category)
	require.Len(t, tmpl.Fields, 4) // notesPlain + 3
	assert.Equal(t, "notesPlain", tmpl.Fields[0].ID)
	got := map[string]opField{}
	for _, f := range tmpl.Fields[1:] {
		got[f.Label] = f
	}
	assert.Equal(t, "STRING", got["ca_crt"].Type)
	assert.Equal(t, "AAA", got["ca_crt"].Value)
	assert.Equal(t, "CONCEALED", got["ca_key"].Type)      // *.key concealed
	assert.Equal(t, "CONCEALED", got["etcd_ca_key"].Type) // *.key concealed
}

func TestSavePKIToOpUsesStdinNotArgv(t *testing.T) {
	// The base64 keys must travel via stdin (template), never argv (/proc leak).
	var deleteArgs []string
	var createStdin []byte
	var createArgs []string
	origDel, origStdin := runOpFn, runOpStdinFn
	runOpFn = func(args ...string) error { deleteArgs = args; return nil }
	runOpStdinFn = func(stdin []byte, args ...string) error { createStdin = stdin; createArgs = args; return nil }
	defer func() { runOpFn, runOpStdinFn = origDel, origStdin }()

	require.NoError(t, savePKIToOp(map[string]string{"ca_crt": "AAA", "ca_key": "BBB"}))
	assert.Equal(t, "delete", deleteArgs[1])
	assert.Equal(t, []string{"item", "create", "--vault", "Infrastructure"}, createArgs)
	// secret values present in stdin payload, absent from argv
	assert.Contains(t, string(createStdin), "BBB")
	assert.NotContains(t, strings.Join(createArgs, " "), "BBB")
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
	origCap, origSave := capturePKIFn, savePKIToOpFn
	capturePKIFn = func(sshUser, ip string) (map[string]string, error) {
		assert.Equal(t, "core", sshUser)
		assert.Equal(t, "192.168.122.10", ip)
		return map[string]string{"ca_crt": "X", "ca_key": "Y"}, nil
	}
	savePKIToOpFn = func(f map[string]string) error { saved = f; return nil }
	defer func() { capturePKIFn = origCap; savePKIToOpFn = origSave }()

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
	origIgn := renderIgnitionFn
	defer func() { renderIgnitionFn = origIgn }()
	renderIgnitionFn = func(env flatcar.NodeEnv) ([]byte, error) {
		return []byte(`{"ignition":{"version":"3.4.0"}}`), nil
	}

	origCreds := getProxmoxCredentialsFn
	defer func() { getProxmoxCredentialsFn = origCreds }()
	getProxmoxCredentialsFn = func() (string, string, string, string, error) {
		return "h", "tid", "sec", "pve", nil
	}

	mgr := &fakeMgr{}
	origMgr := newProxmoxVMManagerFn
	defer func() { newProxmoxVMManagerFn = origMgr }()
	newProxmoxVMManagerFn = func(string, string, string, string, bool) (proxmoxVMManager, error) {
		return mgr, nil
	}

	var uploadedTo string
	origUpload := uploadIgnitionToPVEFn
	defer func() { uploadIgnitionToPVEFn = origUpload }()
	uploadIgnitionToPVEFn = func(sshHost, sshUser, sshPort, remotePath string, content []byte) error {
		uploadedTo = sshHost + ":" + remotePath
		return nil
	}

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

var _ = cobra.Command{}
