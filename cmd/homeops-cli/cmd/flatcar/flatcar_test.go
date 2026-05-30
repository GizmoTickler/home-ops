package flatcar

import (
	"bytes"
	"testing"

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
