package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSFamilyDefaultsToFlatcar(t *testing.T) {
	// An existing config with no os keys must keep resolving every node to
	// Flatcar: the FCOS support is opt-in and must not change current configs.
	cfg := &Config{Cluster: ClusterConfig{Nodes: []Node{{Name: "k8s-0", IP: "192.168.122.10"}}}}
	assert.Equal(t, OSFlatcar, cfg.ClusterOS())
	assert.Equal(t, OSFlatcar, cfg.NodeOS("k8s-0"))
	assert.Equal(t, OSFlatcar, cfg.NodeOS("unknown"))
	var nilCfg *Config
	assert.Equal(t, OSFlatcar, nilCfg.ClusterOS())
}

func TestOSFamilyNodeOverridesCluster(t *testing.T) {
	cfg := &Config{Cluster: ClusterConfig{
		OS: "flatcar",
		Nodes: []Node{
			{Name: "k8s-0", IP: "192.168.122.10", OS: "fcos"},
			{Name: "k8s-1", IP: "192.168.122.11"},
		},
		TestNode: &Node{Name: "k8s-test", IP: "192.168.122.99", OS: "Fedora-CoreOS"},
	}}
	assert.Equal(t, OSFCOS, cfg.NodeOS("k8s-0"))
	assert.Equal(t, OSFlatcar, cfg.NodeOS("k8s-1"))
	assert.Equal(t, OSFCOS, cfg.NodeOS("k8s-test"), "test_node.os is honoured and aliases normalize")

	cfg.Cluster.OS = "fcos"
	assert.Equal(t, OSFCOS, cfg.NodeOS("k8s-1"), "cluster.os is inherited when the node has no override")
}

func TestIgnitionFwCfgKeyByOSFamily(t *testing.T) {
	assert.Equal(t, "opt/org.flatcar-linux/config", IgnitionFwCfgKey(""))
	assert.Equal(t, "opt/org.flatcar-linux/config", IgnitionFwCfgKey("flatcar"))
	assert.Equal(t, "opt/com.coreos/config", IgnitionFwCfgKey("fcos"))
	assert.Equal(t, "opt/com.coreos/config", IgnitionFwCfgKey("FCOS"))
}

func TestLoadFileParsesAndValidatesOSFamily(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	require.NoError(t, os.WriteFile(good, []byte(`cluster:
  os: fcos
  nodes:
    - name: k8s-0
      ip: 192.168.122.10
      os: flatcar
`), 0o600))
	cfg, err := LoadFile(good)
	require.NoError(t, err)
	assert.Equal(t, OSFCOS, cfg.ClusterOS())
	assert.Equal(t, OSFlatcar, cfg.NodeOS("k8s-0"))

	bad := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte(`cluster:
  os: talos
  nodes:
    - name: k8s-0
      ip: 192.168.122.10
      os: ubuntu
`), 0o600))
	_, err = LoadFile(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cluster.os: "talos" is not supported`)
	assert.Contains(t, err.Error(), `cluster.nodes[k8s-0].os: "ubuntu" is not supported`)
}
