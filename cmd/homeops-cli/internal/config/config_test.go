package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigIsPortable(t *testing.T) {
	c := defaultConfig()

	// Topology defaults
	assert.Equal(t, DefaultControlPlaneVIP, c.Cluster.ControlPlaneVIP)
	assert.Equal(t, DefaultNodeInterface, c.Cluster.NodeInterface)
	assert.Len(t, c.Cluster.Nodes, 3)
	assert.Equal(t, []string{"k8s-0", "k8s-1", "k8s-2"}, c.NodeNames())

	// State stores default to local files, not 1Password
	assert.Equal(t, "file", c.State.Kubeconfig.Backend)
	assert.Equal(t, "file", c.State.PKI.Backend)
	assert.NotEmpty(t, c.State.Kubeconfig.Path)
	assert.NotEmpty(t, c.State.PKI.Path)

	// No default secret reference uses 1Password
	assert.False(t, c.UsesOpReferences())
	for _, key := range KnownSecretKeys() {
		ref := c.SecretRef(key)
		assert.NotEmpty(t, ref, "key %s has no default", key)
		assert.False(t, strings.HasPrefix(ref, "op://"), "default for %s must not be op:// (got %s)", key, ref)
	}
}

func TestSecretRefPrecedence(t *testing.T) {
	c := defaultConfig()
	c.Secrets[KeyTrueNASHost] = "op://Vault/item/host"

	assert.Equal(t, "op://Vault/item/host", c.SecretRef(KeyTrueNASHost))
	assert.Equal(t, "env://TRUENAS_API_KEY", c.SecretRef(KeyTrueNASAPIKey))
	assert.Equal(t, "", c.SecretRef("unknown_key"))
	assert.True(t, c.UsesOpReferences())
}

func TestResolveSecret(t *testing.T) {
	t.Setenv("TRUENAS_HOST", "nas.test")
	c := defaultConfig()

	v, err := c.ResolveSecret(KeyTrueNASHost)
	require.NoError(t, err)
	assert.Equal(t, "nas.test", v)

	_, err = c.ResolveSecret("nope")
	require.Error(t, err)

	assert.Equal(t, "core", c.ResolveSecretSilent(KeyNodeSSHUser))
}

func TestLoadFileValidAndDefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  name: test-cluster
  control_plane_vip: 10.0.0.5
  nodes:
    - name: n0
      ip: 10.0.0.10
secrets:
  truenas_host: literal://nas.local
state:
  pki:
    backend: op
`), 0o644))

	c, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "test-cluster", c.Cluster.Name)
	assert.Equal(t, "10.0.0.5", c.Cluster.ControlPlaneVIP)
	assert.Equal(t, []string{"k8s-0", "k8s-1", "k8s-2", "n0"}, c.NodeNames())
	// defaults fill the rest
	assert.Equal(t, DefaultNodeInterface, c.Cluster.NodeInterface)
	assert.Equal(t, "file", c.State.Kubeconfig.Backend)
	assert.Equal(t, "op", c.State.PKI.Backend)
	assert.Equal(t, "kubernetes-pki", c.State.PKI.Op.Item)
	assert.Equal(t, "literal://nas.local", c.SecretRef(KeyTrueNASHost))
	assert.Equal(t, path, c.Source)

	n, ok := c.NodeByName("n0")
	assert.True(t, ok)
	assert.Equal(t, "10.0.0.10", n.IP)
	_, ok = c.NodeByName("absent")
	assert.False(t, ok)
}

func TestLoadFileRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"unknown secret key", "secrets:\n  no_such_key: env://X\n", "unknown secret key"},
		{"invalid reference", "secrets:\n  truenas_host: just-a-string\n", "not a valid secret reference"},
		{"bad store backend", "state:\n  pki:\n    backend: s3\n", "not supported"},
		{"bad hypervisor", "hypervisors:\n  default: xen\n", "not supported"},
		{"unknown field", "clusterz:\n  name: x\n", "field clusterz not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o644))
			_, err := LoadFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAPIEndpoint(t *testing.T) {
	c := defaultConfig()
	assert.Equal(t, "", c.APIEndpoint())

	c.Cluster.DomainRef = "literal://example.test"
	assert.Equal(t, "k8s.example.test", c.APIEndpoint())

	c.Cluster.Endpoint = "api.example.test"
	assert.Equal(t, "api.example.test", c.APIEndpoint())
}

func TestSetForTestingRegistersKeymap(t *testing.T) {
	restore := SetForTesting(&Config{
		Secrets: map[string]string{KeyClusterDomain: "literal://from-test"},
	})
	defer restore()

	v, err := Get().ResolveSecret(KeyClusterDomain)
	require.NoError(t, err)
	assert.Equal(t, "from-test", v)
}

func TestTrueNASISOPath(t *testing.T) {
	c := defaultConfig()
	assert.Equal(t, "/mnt/flashstor/ISO/metal-amd64.iso", c.TrueNASISOPath())
}

func TestLocateSkipsDiscoveryInTests(t *testing.T) {
	// Running inside a test binary: no discovery, only explicit paths.
	path, explicit := Locate()
	assert.Equal(t, "", path)
	assert.False(t, explicit)

	t.Setenv(EnvConfigFile, "/tmp/explicit.yaml")
	path, explicit = Locate()
	assert.Equal(t, "/tmp/explicit.yaml", path)
	assert.True(t, explicit)
}

func TestGetKeepsExplicitLoadErrorsFatal(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	explicitPath = "/definitely/missing/homeops.yaml"

	cfg := Get()
	require.NotNil(t, cfg)
	require.Error(t, LoadError())
	assert.True(t, IsExplicitLoadError(LoadError()))
	assert.Equal(t, "/definitely/missing/homeops.yaml", loadPath)
}

func TestLoadFileMergesNodeOverridesByName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  nodes:
    - name: k8s-0
      ip: 10.0.0.10
      vm:
        vmid: 250
        providers:
          talos:
            boot_storage: talos-fast
          flatcar:
            boot_storage: flatcar-mirror
    - name: k8s-9
      ip: 10.0.0.19
      vm:
        vmid: 209
        mac: "02:00:00:00:00:09"
`), 0o644))

	c, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-1", "k8s-2", "k8s-9"}, c.NodeNames())

	n0, ok := c.NodeByName("k8s-0")
	require.True(t, ok)
	assert.Equal(t, "10.0.0.10", n0.IP)
	assert.Equal(t, 250, n0.VM.VMID)
	assert.Equal(t, "00:a0:98:28:c8:83", n0.VM.ForProvider("talos").Mac)
	assert.Equal(t, "talos-fast", n0.VM.ForProvider("talos").BootStorage)
	assert.Equal(t, "flatcar-mirror", n0.VM.ForProvider("flatcar").BootStorage)

	n1, ok := c.NodeByName("k8s-1")
	require.True(t, ok)
	assert.Equal(t, "192.168.122.11", n1.IP)
	assert.Equal(t, 201, n1.VM.ForProvider("talos").VMID)

	n9, ok := c.NodeByName("k8s-9")
	require.True(t, ok)
	assert.Equal(t, "10.0.0.19", n9.IP)
	assert.Equal(t, 209, n9.VM.VMID)
}
