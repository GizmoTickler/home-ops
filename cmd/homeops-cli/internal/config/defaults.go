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

// defaultNodes is the embedded default 3-node control-plane topology and node
// VM identity. It preserves the historical Proxmox accessors when homeops.yaml
// has no cluster.nodes entries.
var defaultNodes = []Node{
	defaultNode("k8s-0", "192.168.122.10", 200, "00:a0:98:28:c8:83", "0-7,32-39", 0, "ata-INTEL_SSDSC2BB012T7_PHDV6484011X1P2DGN", "nvme1", "nvme-mirror"),
	defaultNode("k8s-1", "192.168.122.11", 201, "00:a0:98:1a:f3:72", "16-23,48-55", 1, "ata-INTEL_SSDSC2BB012T7_PHDV650101691P2DGN", "nvme2", "nvme-mirror"),
	defaultNode("k8s-2", "192.168.122.12", 202, "00:a0:98:3e:6c:22", "8-15,40-47", 0, "ata-INTEL_SSDSC2BB012T7_PHDV650101LU1P2DGN", "nvme1", "nvme-mirror"),
}

func defaultNode(name, ip string, vmid int, mac, affinity string, numa int, cephDiskByID, talosBootStorage, flatcarBootStorage string) Node {
	talosProfile := defaultProviderVMProfile(vmid, mac, affinity, numa, cephDiskByID)
	talosProfile.BootStorage = talosBootStorage
	flatcarProfile := defaultProviderVMProfile(vmid, mac, affinity, numa, cephDiskByID)
	flatcarProfile.BootStorage = flatcarBootStorage
	return Node{
		Name: name,
		IP:   ip,
		VM: VMProfile{
			Providers: ProviderVMProfiles{
				Talos:   talosProfile,
				Flatcar: flatcarProfile,
			},
		},
	}
}

func defaultProviderVMProfile(vmid int, mac, affinity string, numa int, cephDiskByID string) ProviderVMProfile {
	return ProviderVMProfile{
		VMID:           vmid,
		Mac:            mac,
		OpenEBSStorage: "nvmeof-vmdata",
		CPUAffinity:    affinity,
		NUMANode:       intPtr(numa),
		Ceph:           CephDisk{DiskByID: cephDiskByID},
	}
}

func intPtr(v int) *int { return &v }

// DefaultNodes returns a deep copy of the embedded default node topology.
func DefaultNodes() []Node {
	return copyNodes(defaultNodes)
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
	c.Cluster.Nodes = mergeNodesWithDefaults(c.Cluster.Nodes)
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

func mergeNodesWithDefaults(overrides []Node) []Node {
	if len(overrides) == 0 {
		return DefaultNodes()
	}

	merged := DefaultNodes()
	byName := make(map[string]int, len(merged))
	for i, n := range merged {
		byName[n.Name] = i
	}
	for _, override := range overrides {
		if idx, ok := byName[override.Name]; ok {
			merged[idx] = mergeNode(merged[idx], override)
			continue
		}
		merged = append(merged, copyNode(override))
		byName[override.Name] = len(merged) - 1
	}
	return merged
}

func mergeNode(base, override Node) Node {
	out := copyNode(base)
	if override.Name != "" {
		out.Name = override.Name
	}
	if override.IP != "" {
		out.IP = override.IP
	}
	out.VM = mergeVMProfile(out.VM, override.VM)
	return out
}

func mergeVMProfile(base, override VMProfile) VMProfile {
	out := copyVMProfile(base)
	sharedOverride := vmProfileToProviderProfile(override)
	out.Providers.Talos = mergeProviderVMProfile(out.Providers.Talos, sharedOverride)
	out.Providers.Flatcar = mergeProviderVMProfile(out.Providers.Flatcar, sharedOverride)
	out.Providers.Talos = mergeProviderVMProfile(out.Providers.Talos, override.Providers.Talos)
	out.Providers.Flatcar = mergeProviderVMProfile(out.Providers.Flatcar, override.Providers.Flatcar)
	applyVMProfile(&out, override)
	return out
}

func mergeProviderVMProfile(base, override ProviderVMProfile) ProviderVMProfile {
	out := copyProviderVMProfile(base)
	applyProviderProfile(&out, override)
	return out
}

func applyVMProfile(out *VMProfile, override VMProfile) {
	if override.VMID != 0 {
		out.VMID = override.VMID
	}
	if override.Mac != "" {
		out.Mac = override.Mac
	}
	if override.BootStorage != "" {
		out.BootStorage = override.BootStorage
	}
	if override.OpenEBSStorage != "" {
		out.OpenEBSStorage = override.OpenEBSStorage
	}
	if override.CPUAffinity != "" {
		out.CPUAffinity = override.CPUAffinity
	}
	if override.NUMANode != nil {
		out.NUMANode = intPtr(*override.NUMANode)
	}
	out.Ceph = mergeCephDisk(out.Ceph, override.Ceph)
}

func applyProviderProfile(out *ProviderVMProfile, override ProviderVMProfile) {
	if override.VMID != 0 {
		out.VMID = override.VMID
	}
	if override.Mac != "" {
		out.Mac = override.Mac
	}
	if override.BootStorage != "" {
		out.BootStorage = override.BootStorage
	}
	if override.OpenEBSStorage != "" {
		out.OpenEBSStorage = override.OpenEBSStorage
	}
	if override.CPUAffinity != "" {
		out.CPUAffinity = override.CPUAffinity
	}
	if override.NUMANode != nil {
		out.NUMANode = intPtr(*override.NUMANode)
	}
	out.Ceph = mergeCephDisk(out.Ceph, override.Ceph)
}

func vmProfileToProviderProfile(profile VMProfile) ProviderVMProfile {
	return ProviderVMProfile{
		VMID:           profile.VMID,
		Mac:            profile.Mac,
		BootStorage:    profile.BootStorage,
		OpenEBSStorage: profile.OpenEBSStorage,
		CPUAffinity:    profile.CPUAffinity,
		NUMANode:       profile.NUMANode,
		Ceph:           profile.Ceph,
	}
}

func applyProviderVMProfile(out *VMProfile, override ProviderVMProfile) {
	if override.VMID != 0 {
		out.VMID = override.VMID
	}
	if override.Mac != "" {
		out.Mac = override.Mac
	}
	if override.BootStorage != "" {
		out.BootStorage = override.BootStorage
	}
	if override.OpenEBSStorage != "" {
		out.OpenEBSStorage = override.OpenEBSStorage
	}
	if override.CPUAffinity != "" {
		out.CPUAffinity = override.CPUAffinity
	}
	if override.NUMANode != nil {
		out.NUMANode = intPtr(*override.NUMANode)
	}
	out.Ceph = mergeCephDisk(out.Ceph, override.Ceph)
}

func mergeCephDisk(base, override CephDisk) CephDisk {
	out := base
	if override.Mode != "" {
		out.Mode = override.Mode
	}
	if override.DiskByID != "" {
		out.DiskByID = override.DiskByID
	}
	if override.SizeGB != 0 {
		out.SizeGB = override.SizeGB
	}
	if override.Storage != "" {
		out.Storage = override.Storage
	}
	return out
}

func copyNodes(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	for i, n := range nodes {
		out[i] = copyNode(n)
	}
	return out
}

func copyNode(n Node) Node {
	n.VM = copyVMProfile(n.VM)
	return n
}

func copyVMProfile(p VMProfile) VMProfile {
	if p.NUMANode != nil {
		p.NUMANode = intPtr(*p.NUMANode)
	}
	p.Providers.Talos = copyProviderVMProfile(p.Providers.Talos)
	p.Providers.Flatcar = copyProviderVMProfile(p.Providers.Flatcar)
	return p
}

func copyProviderVMProfile(p ProviderVMProfile) ProviderVMProfile {
	if p.NUMANode != nil {
		p.NUMANode = intPtr(*p.NUMANode)
	}
	return p
}

// TrueNASISOPath returns the full default ISO path on the TrueNAS host.
func (c *Config) TrueNASISOPath() string {
	return filepath.Join(c.Hypervisors.TrueNAS.ISODir, c.Hypervisors.TrueNAS.ISOFile)
}
