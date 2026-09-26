package flatcar

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Boot hook deploys the VM powered off and runs before anything touches the
// guest; without one the VM is powered on at creation as before.
func TestDeployRehearsalNodeBootDeploysPoweredOff(t *testing.T) {
	defer stubVersions(t)()
	restore := versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{
			DomainRef: "literal://example.test", ControlPlaneVIP: "192.0.2.1", NodeInterface: "eth0",
			Nodes: []versionconfig.Node{{Name: "k8s-2", IP: "192.0.2.12"}},
		},
		Hypervisors: versionconfig.HypervisorsConfig{Proxmox: versionconfig.ProxmoxConfig{SnippetsDir: "/var/lib/vz/snippets", SSHUser: "root"}},
		Secrets:     map[string]string{versionconfig.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 AAAATESTKEY"},
	})
	defer restore()
	testutil.Swap(t, &getFlatcarNodeConfigFn, func(name string) (proxmox.FlatcarNodeConfig, bool) {
		return proxmox.FlatcarNodeConfig{Name: name}, true
	})
	testutil.Swap(t, &renderIgnitionFn, func(flatcar.NodeEnv) ([]byte, error) { return []byte(`{}`), nil })
	testutil.Swap(t, &renderKubeadmJoinFn, func(flatcar.NodeEnv) (string, error) { return "join", nil })
	testutil.Swap(t, &getProxmoxCredentialsFn, func() (string, string, string, string, error) { return "h", "tid", "sec", "pve", nil })
	testutil.Swap(t, &uploadIgnitionToPVEFn, func(string, string, string, string, []byte) error { return nil })
	mgr := &fakeMgr{}
	testutil.Swap(t, &newProxmoxVMManagerFn, func(string, string, string, string, bool) (proxmoxVMManager, error) { return mgr, nil })

	stop := errors.New("stop before kubeadm wait")
	var bootSawDeployed int
	options := RehearsalDeployOptions{
		Node: versionconfig.Node{Name: "k8s-2", IP: "192.0.2.12"}, Provider: "proxmox", ImageVolume: "vm-ssd:vm-900-disk-0",
		Join: flatcar.KubeadmResult{
			BootstrapToken: "abcdef.0123456789abcdef",
			CACertHash:     "sha256:" + strings.Repeat("1", 64),
			CertificateKey: strings.Repeat("2", 64),
		},
		Timeout: time.Minute,
		Boot: func(context.Context) error {
			bootSawDeployed = len(mgr.deployed)
			return stop
		},
	}
	err := DeployRehearsalNode(context.Background(), options)
	require.ErrorIs(t, err, stop)
	require.Len(t, mgr.deployed, 1)
	assert.False(t, mgr.deployed[0].PowerOn, "a Boot hook deploys powered off")
	assert.Equal(t, 1, bootSawDeployed, "Boot runs after the VM is created")

	options.Provider = "vsphere"
	require.ErrorContains(t, DeployRehearsalNode(context.Background(), options), "only supported on Proxmox")
	assert.Len(t, mgr.deployed, 1)
}
