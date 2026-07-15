package kubeutil

import (
	"strconv"

	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
)

// NodeSSHConfig builds SSH connection settings for a configured cluster node.
func NodeSSHConfig(node config.Node, username string) ssh.SSHConfig {
	return ssh.SSHConfig{Host: node.IP, Username: username, Port: strconv.Itoa(config.Get().Cluster.NodeSSHPort)}
}
