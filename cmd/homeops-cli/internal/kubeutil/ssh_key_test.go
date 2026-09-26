package kubeutil

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"homeops-cli/internal/config"
)

// Every node SSH connection offers cluster.node_ssh_key, so a node without an
// ssh_config Host entry (a rehearsal node) is still reachable.
func TestNodeSSHConfigThreadsConfiguredKey(t *testing.T) {
	restore := config.SetForTesting(&config.Config{Cluster: config.ClusterConfig{NodeSSHPort: 22, NodeSSHKey: "~/.ssh/keys/flatcar"}})
	defer restore()

	cfg := NodeSSHConfig(config.Node{Name: "k8s-test", IP: "192.0.2.98"}, "core")
	assert.Equal(t, "~/.ssh/keys/flatcar", cfg.KeyPath)
	assert.Equal(t, "192.0.2.98", cfg.Host)
}
