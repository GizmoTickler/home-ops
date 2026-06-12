package config

import (
	"os"
	"path/filepath"
)

// Topology defaults. These are plain defaults (not references to anyone's
// infrastructure manager) and every one of them is overridable in
// homeops.yaml.
const (
	DefaultControlPlaneVIP = "192.168.123.253"
	DefaultNodeInterface   = "eth0"
	DefaultProxmoxNodeName = "pve"
	DefaultTrueNASISODir   = "/mnt/flashstor/ISO"
	DefaultTrueNASISOFile  = "metal-amd64.iso"
	DefaultSnippetsDir     = "/var/lib/vz/snippets"
)

// defaultNodes is the default 3-node control-plane topology.
var defaultNodes = []Node{
	{Name: "k8s-0", IP: "192.168.122.10"},
	{Name: "k8s-1", IP: "192.168.122.11"},
	{Name: "k8s-2", IP: "192.168.122.12"},
}

// defaultStateDir returns the local directory for file-backend cluster state.
func defaultStateDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "homeops", "state")
	}
	return filepath.Join(".homeops", "state")
}

// defaultConfig returns the fully-portable built-in configuration: secrets
// from environment variables, cluster state on local disk.
func defaultConfig() *Config {
	c := &Config{}
	applyDefaults(c)
	return c
}

// applyDefaults fills unset fields with built-in defaults.
func applyDefaults(c *Config) {
	if c.Cluster.ControlPlaneVIP == "" {
		c.Cluster.ControlPlaneVIP = DefaultControlPlaneVIP
	}
	if c.Cluster.NodeInterface == "" {
		c.Cluster.NodeInterface = DefaultNodeInterface
	}
	if len(c.Cluster.Nodes) == 0 {
		c.Cluster.Nodes = append([]Node(nil), defaultNodes...)
	}
	if c.Hypervisors.Default == "" {
		c.Hypervisors.Default = "proxmox"
	}
	if c.Hypervisors.Proxmox.SnippetsDir == "" {
		c.Hypervisors.Proxmox.SnippetsDir = DefaultSnippetsDir
	}
	if c.Hypervisors.TrueNAS.ISODir == "" {
		c.Hypervisors.TrueNAS.ISODir = DefaultTrueNASISODir
	}
	if c.Hypervisors.TrueNAS.ISOFile == "" {
		c.Hypervisors.TrueNAS.ISOFile = DefaultTrueNASISOFile
	}
	if c.Hypervisors.TrueNAS.ImageDir == "" {
		// Stage cloud images next to the ISO dataset by default.
		c.Hypervisors.TrueNAS.ImageDir = filepath.Join(filepath.Dir(c.Hypervisors.TrueNAS.ISODir), "images")
	}
	if c.State.Kubeconfig.Backend == "" {
		c.State.Kubeconfig.Backend = "file"
	}
	if c.State.Kubeconfig.Backend == "file" && c.State.Kubeconfig.Path == "" {
		c.State.Kubeconfig.Path = filepath.Join(defaultStateDir(), "kubeconfig")
	}
	if c.State.Kubeconfig.Backend == "op" {
		if c.State.Kubeconfig.Op.Item == "" {
			c.State.Kubeconfig.Op.Item = "kubeconfig"
		}
		if c.State.Kubeconfig.Op.Field == "" {
			c.State.Kubeconfig.Op.Field = "kubeconfig"
		}
	}
	if c.State.PKI.Backend == "" {
		c.State.PKI.Backend = "file"
	}
	if c.State.PKI.Backend == "file" && c.State.PKI.Path == "" {
		c.State.PKI.Path = filepath.Join(defaultStateDir(), "pki")
	}
	if c.State.PKI.Backend == "op" && c.State.PKI.Op.Item == "" {
		c.State.PKI.Op.Item = "kubernetes-pki"
	}
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
}

// TrueNASISOPath returns the full default ISO path on the TrueNAS host.
func (c *Config) TrueNASISOPath() string {
	return filepath.Join(c.Hypervisors.TrueNAS.ISODir, c.Hypervisors.TrueNAS.ISOFile)
}
