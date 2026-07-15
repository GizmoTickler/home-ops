package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/secrets"
)

func TestDefaultConfigIsPortable(t *testing.T) {
	c := defaultConfig()

	// Topology defaults
	assert.Equal(t, DefaultClusterName, c.ClusterNameWithDefault())
	assert.Equal(t, DefaultControlPlaneVIP, c.Cluster.ControlPlaneVIP)
	assert.Equal(t, DefaultNodeInterface, c.Cluster.NodeInterface)
	assert.Equal(t, DefaultPodCIDR, c.Cluster.PodCIDR)
	assert.Equal(t, DefaultServiceCIDR, c.Cluster.ServiceCIDR)
	assert.Equal(t, DefaultDNSDomain, c.Cluster.DNSDomain)
	assert.Equal(t, "10.43.0.10", c.ClusterDNS())
	assert.Equal(t, DefaultNodeSubnet, c.Cluster.NodeSubnet)
	assert.Equal(t, []string{"10.123.123.123", "10.123.123.124", "10.123.123.125", "10.123.123.126", "10.123.123.127"}, c.Cluster.NTPServers)
	assert.Equal(t, []string{"192.168.255.10"}, c.Cluster.ExtraCertSANs)
	assert.Equal(t, 250, c.Cluster.Kubelet.MaxPods)
	assert.Equal(t, 60, c.Cluster.Kubelet.ImageGCHighPercent)
	assert.Equal(t, 50, c.Cluster.Kubelet.ImageGCLowPercent)
	assert.Equal(t, "http://192.168.123.152:3000", c.Cluster.Talos.DiscoveryEndpoint)
	assert.Equal(t, "/dev/sda", c.Cluster.Talos.ControlPlaneInstallDisk)
	assert.Equal(t, "/dev/nvme0n1", c.Cluster.Talos.WorkerInstallDisk)
	assert.Equal(t, "/dev/sdb", c.Cluster.Talos.UserVolume.Disk)
	assert.Equal(t, "800GB", c.Cluster.Talos.UserVolume.MinSize)
	assert.Equal(t, "900GB", c.Cluster.Talos.UserVolume.MaxSize)
	assert.Len(t, c.Cluster.Nodes, 3)
	assert.Equal(t, []string{"k8s-0", "k8s-1", "k8s-2"}, c.NodeNames())
	assert.Equal(t, DefaultSnippetsDir, c.Hypervisors.Proxmox.SnippetsDir)
	assert.Equal(t, DefaultProxmoxSSHUser, c.Hypervisors.Proxmox.SSHUser)
	assert.Equal(t, DefaultProxmoxImageCacheDir, c.Hypervisors.Proxmox.ImageCacheDir)
	assert.Equal(t, 98304, c.Hypervisors.Proxmox.VM.MemoryMB)
	assert.Equal(t, 8, c.Hypervisors.Proxmox.VM.NetworkQueues)
	assert.Equal(t, "host,flags=+pdpe1gb;-spec-ctrl", c.Hypervisors.Proxmox.VM.CPUType)
	assert.Equal(t, "virtio-scsi-single", c.Hypervisors.Proxmox.VM.SCSIController)
	assert.Equal(t, "i6300esb", c.Hypervisors.Proxmox.VM.WatchdogModel)
	assert.Equal(t, DefaultTrueNASSSHUser, c.Hypervisors.TrueNAS.SSHUser)
	assert.Equal(t, DefaultTrueNASVMBootStorage, c.Hypervisors.TrueNAS.VM.BootStorage)
	assert.Equal(t, DefaultTrueNASNetworkBridge, c.Hypervisors.TrueNAS.VM.NetworkBridge)
	assert.Equal(t, DefaultVSphereISODatastore, c.Hypervisors.VSphere.ISODatastore)
	assert.Equal(t, DefaultVSphereISOFile, c.Hypervisors.VSphere.ISOFile)
	assert.Equal(t, 1, c.Hypervisors.VSphere.VM.CoresPerSocket)
	assert.Equal(t, "vl999", c.Hypervisors.VSphere.VM.NetworkBridge)

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
	assert.Equal(t, "test-cluster", c.ClusterNameWithDefault())
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

func TestLoadFileAcceptsClusterEnvironmentKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  name: home-ops-cluster
  pod_cidr: 10.244.0.0/16
  service_cidr: 10.96.0.0/12
  dns_domain: corp.local
  node_subnet: 10.0.0.0/24
  ntp_servers:
    - 10.0.0.1
    - 10.0.0.2
  extra_cert_sans:
    - 10.0.0.100
    - api.internal
  kubelet:
    max_pods: 111
    image_gc_high_percent: 70
    image_gc_low_percent: 55
  talos:
    discovery_endpoint: http://10.0.0.10:3000
    controlplane_install_disk: /dev/disk/by-id/control
    worker_install_disk: /dev/disk/by-id/worker
    user_volume:
      disk: /dev/disk/by-id/local
      min_size: 750GB
      max_size: 950GB
bootstrap:
  op_vault: OpsVault
`), 0o644))

	c, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "home-ops-cluster", c.ClusterNameWithDefault())
	assert.Equal(t, "10.244.0.0/16", c.Cluster.PodCIDR)
	assert.Equal(t, "10.96.0.0/12", c.Cluster.ServiceCIDR)
	assert.Equal(t, "10.96.0.10", c.ClusterDNS())
	assert.Equal(t, "corp.local", c.Cluster.DNSDomain)
	assert.Equal(t, "10.0.0.0/24", c.Cluster.NodeSubnet)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, c.Cluster.NTPServers)
	assert.Equal(t, []string{"10.0.0.100", "api.internal"}, c.Cluster.ExtraCertSANs)
	assert.Equal(t, 111, c.Cluster.Kubelet.MaxPods)
	assert.Equal(t, 70, c.Cluster.Kubelet.ImageGCHighPercent)
	assert.Equal(t, 55, c.Cluster.Kubelet.ImageGCLowPercent)
	assert.Equal(t, "http://10.0.0.10:3000", c.Cluster.Talos.DiscoveryEndpoint)
	assert.Equal(t, "/dev/disk/by-id/control", c.Cluster.Talos.ControlPlaneInstallDisk)
	assert.Equal(t, "/dev/disk/by-id/worker", c.Cluster.Talos.WorkerInstallDisk)
	assert.Equal(t, "/dev/disk/by-id/local", c.Cluster.Talos.UserVolume.Disk)
	assert.Equal(t, "750GB", c.Cluster.Talos.UserVolume.MinSize)
	assert.Equal(t, "950GB", c.Cluster.Talos.UserVolume.MaxSize)
	assert.Equal(t, "OpsVault", c.Bootstrap.OpVault)
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
		{"negative numeric vm knob", "hypervisors:\n  proxmox:\n    vm:\n      network_queues: -1\n", "must not be negative"},
		{"bad pod cidr", "cluster:\n  pod_cidr: not-a-cidr\n", "cluster.pod_cidr"},
		{"bad service cidr", "cluster:\n  service_cidr: not-a-cidr\n", "cluster.service_cidr"},
		{"bad node subnet", "cluster:\n  node_subnet: not-a-cidr\n", "cluster.node_subnet"},
		{"bad kubelet max pods", "cluster:\n  kubelet:\n    max_pods: -1\n", "cluster.kubelet.max_pods"},
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

func TestProposedHomeopsAdditionsLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(ProposedHomeopsAdditionsForTest), 0o644))

	c, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "home-ops-cluster", c.ClusterNameWithDefault())
	assert.Equal(t, "10.43.0.10", c.ClusterDNS())
	assert.Equal(t, "Infrastructure", c.Bootstrap.OpVault)
}

const ProposedHomeopsAdditionsForTest = `
cluster:
  name: home-ops-cluster
  pod_cidr: 10.42.0.0/16
  service_cidr: 10.43.0.0/16
  dns_domain: cluster.local
  node_subnet: 192.168.120.0/22
  ntp_servers:
    - 10.123.123.123
    - 10.123.123.124
    - 10.123.123.125
    - 10.123.123.126
    - 10.123.123.127
  extra_cert_sans:
    - 192.168.255.10
  kubelet:
    max_pods: 250
    image_gc_high_percent: 60
    image_gc_low_percent: 50
  talos:
    discovery_endpoint: http://192.168.123.152:3000
    controlplane_install_disk: /dev/sda
    worker_install_disk: /dev/nvme0n1
    user_volume:
      disk: /dev/sdb
      min_size: 800GB
      max_size: 900GB
bootstrap:
  op_vault: Infrastructure
`

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
	assert.Equal(t, "flashstor", c.TrueNASPool())
	assert.Equal(t, "/mnt/flashstor/VM", c.TrueNASIgnitionDir(c.TrueNASPool()))
	assert.Equal(t, "[datastore1] vmware-amd64.iso", c.VSphereISOPath())
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

func TestSetExplicitPathTakesPrecedenceOverEnvConfig(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	t.Setenv(EnvConfigFile, "/tmp/env-homeops.yaml")

	SetExplicitPath("/tmp/flag-homeops.yaml")

	path, explicit := Locate()
	assert.Equal(t, "/tmp/flag-homeops.yaml", path)
	assert.True(t, explicit)
}

func TestSetExplicitPathInvalidatesEarlierLoad(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	dir := t.TempDir()
	decoyPath := writeMinimalConfigFixture(t, dir, "decoy.yaml", "decoy-cluster", "/decoy/snippets")
	fixturePath := writeMinimalConfigFixture(t, dir, "fixture.yaml", "fixture-cluster", "/fixture/snippets")

	t.Setenv(EnvConfigFile, decoyPath)
	require.Equal(t, "decoy-cluster", Get().ClusterNameWithDefault())

	SetExplicitPath(fixturePath)

	cfg := Get()
	require.Equal(t, "fixture-cluster", cfg.ClusterNameWithDefault())
	assert.Equal(t, "/fixture/snippets", cfg.Hypervisors.Proxmox.SnippetsDir)
	assert.Equal(t, fixturePath, cfg.Source)
}

func TestSetExplicitPathWinsOverDiscoveredCWDConfigAfterEarlyLoad(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	oldArg0 := os.Args[0]
	os.Args[0] = "homeops-cli"
	t.Cleanup(func() { os.Args[0] = oldArg0 })

	decoyDir := t.TempDir()
	decoyPath := writeMinimalConfigFixture(t, decoyDir, "homeops.yaml", "decoy-cwd-cluster", "/decoy/snippets")
	fixturePath := writeMinimalConfigFixture(t, t.TempDir(), "fixture.yaml", "fixture-cluster", "/fixture/snippets")
	t.Chdir(decoyDir)

	require.Equal(t, decoyPath, Get().Source)
	require.Equal(t, "decoy-cwd-cluster", Get().ClusterNameWithDefault())

	SetExplicitPath(fixturePath)

	cfg := Get()
	require.Equal(t, "fixture-cluster", cfg.ClusterNameWithDefault())
	assert.Equal(t, "/fixture/snippets", cfg.Hypervisors.Proxmox.SnippetsDir)
	assert.Equal(t, fixturePath, cfg.Source)
}

func TestHomeopsConfigEnvLoadsFixtureFromTempWorkingDirectory(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	dir := t.TempDir()
	fixturePath := writeMinimalConfigFixture(t, dir, "fixture.yaml", "env-fixture-cluster", "/env/snippets")
	t.Chdir(t.TempDir())
	t.Setenv(EnvConfigFile, fixturePath)

	cfg := Get()
	require.Equal(t, "env-fixture-cluster", cfg.ClusterNameWithDefault())
	assert.Equal(t, "/env/snippets", cfg.Hypervisors.Proxmox.SnippetsDir)
	assert.Equal(t, fixturePath, cfg.Source)
}

func writeMinimalConfigFixture(t *testing.T, dir, name, clusterName, snippetsDir string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "cluster:\n  name: " + clusterName + "\nhypervisors:\n  proxmox:\n    snippets_dir: " + snippetsDir + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
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

func TestExplicitLoadErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("parse failed")
	err := explicitLoadError{path: "/tmp/homeops.yaml", err: cause}

	assert.Equal(t, "parse failed", err.Error())
	assert.True(t, errors.Is(err, cause))
}

func TestRegisteredKeymapResolvesUnknownAndRecursiveSecretReferences(t *testing.T) {
	restore := SetForTesting(&Config{
		Secrets: map[string]string{KeyClusterDomain: "literal://example.test"},
	})

	value, err := secrets.Resolve("secret://" + KeyClusterDomain)
	if assert.NoError(t, err) {
		assert.Equal(t, "example.test", value)
	}

	_, err = secrets.Resolve("secret://not_defined")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "not_defined")
		assert.Contains(t, err.Error(), "not defined")
	}

	restore()
	restore = SetForTesting(&Config{
		Secrets: map[string]string{KeyClusterDomain: "secret://nested"},
	})
	defer restore()

	_, err = secrets.Resolve("secret://" + KeyClusterDomain)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "only one level of indirection")
	}
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
