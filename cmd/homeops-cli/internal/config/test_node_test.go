package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFileParsesClusterTestNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homeops.yaml")
	contents := []byte(`cluster:
  test_node:
    name: k8s-test
    ip: 192.168.122.99
    vm:
      vmid: 299
      mac: "02:00:00:00:02:99"
      boot_storage: nvme-mirror
      openebs_storage: nvmeof-vmdata
      ceph:
        mode: none
volsync:
  check_image: docker.io/library/alpine:3.22
`)
	require.NoError(t, os.WriteFile(path, contents, 0o600))

	cfg, err := LoadFile(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Cluster.TestNode)
	assert.Equal(t, "k8s-test", cfg.Cluster.TestNode.Name)
	assert.Equal(t, "192.168.122.99", cfg.Cluster.TestNode.IP)
	assert.Equal(t, 299, cfg.Cluster.TestNode.VM.VMID)
	assert.Equal(t, "02:00:00:00:02:99", cfg.Cluster.TestNode.VM.Mac)
	assert.Equal(t, "nvme-mirror", cfg.Cluster.TestNode.VM.BootStorage)
	assert.Equal(t, "none", cfg.Cluster.TestNode.VM.Ceph.Mode)

	node, ok := cfg.ProvisioningNodeByName("k8s-test")
	require.True(t, ok)
	assert.Equal(t, 299, node.VM.VMID)
	_, production := cfg.NodeByName("k8s-test")
	assert.False(t, production, "the disposable identity must stay outside cluster.nodes")
}
