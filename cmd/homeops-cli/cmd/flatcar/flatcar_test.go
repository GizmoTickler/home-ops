package flatcar

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"

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

	cmd := newGenKubeadmCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--node", "k8s-1", "--mode", "join", "--cert-key", "k", "--token", "t", "--ca-cert-hash", "h"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "JoinConfiguration")
	assert.Equal(t, "k", captured.CertificateKey)
	assert.Equal(t, "t", captured.BootstrapToken)
	assert.Equal(t, "h", captured.CACertHash)
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

var _ = cobra.Command{}
