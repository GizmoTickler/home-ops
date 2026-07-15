package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"homeops-cli/internal/constants"
)

// Topology defaults. These are plain defaults (not references to anyone's
// infrastructure manager) and every one of them is overridable in
// homeops.yaml.
const (
	DefaultClusterName     = "home-ops-cluster"
	DefaultControlPlaneVIP = "192.168.123.253"
	DefaultNodeInterface   = "eth0"
	DefaultPodCIDR         = "10.42.0.0/16"
	DefaultServiceCIDR     = "10.43.0.0/16"
	DefaultDNSDomain       = "cluster.local"
	DefaultNodeSubnet      = "192.168.120.0/22"
	DefaultOpVault         = "Infrastructure"
	DefaultProxmoxNodeName = "pve"
	DefaultTrueNASISODir   = "/mnt/flashstor/ISO"
	DefaultTrueNASISOFile  = "metal-amd64.iso"
	DefaultSnippetsDir     = "/var/lib/vz/snippets"
	DefaultEtcdBackupKeep  = 7
	DefaultDrainTimeout    = "5m"
	DefaultMaintenanceWait = "10m"
)

var (
	defaultNTPServers = []string{
		"10.123.123.123",
		"10.123.123.124",
		"10.123.123.125",
		"10.123.123.126",
		"10.123.123.127",
	}
	defaultExtraCertSANs = []string{"192.168.255.10"}
)

const (
	DefaultProxmoxSSHUser        = "root"
	DefaultProxmoxImageCacheDir  = "/var/lib/vz/template/cache"
	DefaultTrueNASSSHUser        = "truenas_admin"
	DefaultTrueNASNetworkBridge  = "br0"
	DefaultTrueNASVMBootStorage  = "flashstor/VM"
	DefaultVSphereISODatastore   = "datastore1"
	DefaultVSphereISOFile        = "vmware-amd64.iso"
	defaultVSphereOpenEBSStorage = "truenas-iscsi"
	defaultVSphereNetwork        = "vl999"
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
	vsphereProfile := defaultVSphereProviderVMProfile(name, mac)
	return Node{
		Name: name,
		IP:   ip,
		VM: VMProfile{
			Providers: ProviderVMProfiles{
				Talos:   talosProfile,
				Flatcar: flatcarProfile,
				VSphere: vsphereProfile,
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

func defaultVSphereProviderVMProfile(name, mac string) ProviderVMProfile {
	switch name {
	case "k8s-0":
		return ProviderVMProfile{
			Mac:            mac,
			BootStorage:    "local-nvme1",
			OpenEBSStorage: defaultVSphereOpenEBSStorage,
			CPUAffinity:    "0,1,2,3,4,5,6,7,16,17,18,19,20,21,22,23",
			PCIDevice:      "0000:04:00.0",
			RDMPath:        "[datastore1] rdm/intel-ssd-1.vmdk",
		}
	case "k8s-1":
		return ProviderVMProfile{
			Mac:            mac,
			BootStorage:    "local-nvme1",
			OpenEBSStorage: defaultVSphereOpenEBSStorage,
			CPUAffinity:    "32,33,34,35,36,37,38,39,48,49,50,51,52,53,54,55",
			PCIDevice:      "0000:0b:00.0",
			RDMPath:        "[datastore1] rdm/intel-ssd-2.vmdk",
		}
	case "k8s-2":
		return ProviderVMProfile{
			Mac:            mac,
			BootStorage:    "local-nvme2",
			OpenEBSStorage: defaultVSphereOpenEBSStorage,
			CPUAffinity:    "8,9,10,11,12,13,14,15,24,25,26,27,28,29,30,31",
			PCIDevice:      "0000:04:00.1",
			RDMPath:        "[datastore1] rdm/intel-ssd-3.vmdk",
		}
	case "k8s-3":
		return ProviderVMProfile{
			BootStorage:    "local-nvme2",
			OpenEBSStorage: defaultVSphereOpenEBSStorage,
			CPUAffinity:    "40,41,42,43,44,45,46,47,56,57,58,59,60,61,62,63",
			PCIDevice:      "0000:0b:00.1",
			RDMPath:        "[datastore1] rdm/intel-ssd-4.vmdk",
		}
	default:
		return ProviderVMProfile{}
	}
}

// DefaultVSphereK8sNode returns the built-in vSphere-only k8s node profile for
// names that are not necessarily part of cluster.nodes (k8s-3 is a retained
// historical vSphere preset).
func DefaultVSphereK8sNode(name string) (Node, bool) {
	profile := defaultVSphereProviderVMProfile(name, "")
	if profile == (ProviderVMProfile{}) {
		return Node{}, false
	}
	return Node{
		Name: name,
		VM: VMProfile{
			Providers: ProviderVMProfiles{VSphere: profile},
		},
	}, true
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
	if c.Cluster.NodeSSHPort == 0 {
		c.Cluster.NodeSSHPort = constants.DefaultNodeSSHPort
	}
	if c.Cluster.Rook.Namespace == "" {
		c.Cluster.Rook.Namespace = constants.NSRookCeph
	}
	if c.Cluster.Rook.ToolboxDeployment == "" {
		c.Cluster.Rook.ToolboxDeployment = constants.DefaultRookToolboxDeployment
	}
	if c.Cluster.PodCIDR == "" {
		c.Cluster.PodCIDR = DefaultPodCIDR
	}
	if c.Cluster.ServiceCIDR == "" {
		c.Cluster.ServiceCIDR = DefaultServiceCIDR
	}
	if c.Cluster.DNSDomain == "" {
		c.Cluster.DNSDomain = DefaultDNSDomain
	}
	if c.Cluster.NodeSubnet == "" {
		c.Cluster.NodeSubnet = DefaultNodeSubnet
	}
	if len(c.Cluster.NTPServers) == 0 {
		c.Cluster.NTPServers = append([]string(nil), defaultNTPServers...)
	}
	if len(c.Cluster.ExtraCertSANs) == 0 {
		c.Cluster.ExtraCertSANs = append([]string(nil), defaultExtraCertSANs...)
	}
	applyKubeletDefaults(&c.Cluster.Kubelet)
	if c.Cluster.Maintenance.DrainTimeout == "" {
		c.Cluster.Maintenance.DrainTimeout = DefaultDrainTimeout
	}
	if c.Cluster.Maintenance.Timeout == "" {
		c.Cluster.Maintenance.Timeout = DefaultMaintenanceWait
	}
	applyTalosDefaults(&c.Cluster.Talos)
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
	if c.Hypervisors.Proxmox.SSHUser == "" {
		c.Hypervisors.Proxmox.SSHUser = DefaultProxmoxSSHUser
	}
	if c.Hypervisors.Proxmox.ImageCacheDir == "" {
		c.Hypervisors.Proxmox.ImageCacheDir = DefaultProxmoxImageCacheDir
	}
	applyProxmoxVMDefaults(&c.Hypervisors.Proxmox.VM)
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
	if c.Hypervisors.TrueNAS.SSHUser == "" {
		c.Hypervisors.TrueNAS.SSHUser = DefaultTrueNASSSHUser
	}
	applyTrueNASVMDefaults(&c.Hypervisors.TrueNAS.VM)
	if c.Hypervisors.VSphere.ISODatastore == "" {
		c.Hypervisors.VSphere.ISODatastore = DefaultVSphereISODatastore
	}
	if c.Hypervisors.VSphere.ISOFile == "" {
		c.Hypervisors.VSphere.ISOFile = DefaultVSphereISOFile
	}
	applyVSphereVMDefaults(&c.Hypervisors.VSphere.VM)
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
	if c.State.EtcdBackup.Dir == "" {
		c.State.EtcdBackup.Dir = filepath.Join(defaultStateDir(), "etcd")
	}
	if c.State.EtcdBackup.Keep == 0 {
		c.State.EtcdBackup.Keep = DefaultEtcdBackupKeep
	}
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
	if c.Bootstrap.OpVault == "" {
		c.Bootstrap.OpVault = DefaultOpVault
	}
	if c.Volsync.CheckImage == "" {
		c.Volsync.CheckImage = constants.DefaultVolsyncCheckImage
	}
}

func applyKubeletDefaults(k *KubeletConfig) {
	if k.MaxPods == 0 {
		k.MaxPods = 250
	}
	if k.ImageGCHighPercent == 0 {
		k.ImageGCHighPercent = 60
	}
	if k.ImageGCLowPercent == 0 {
		k.ImageGCLowPercent = 50
	}
}

func applyTalosDefaults(t *TalosSettings) {
	if t.DiscoveryEndpoint == "" {
		t.DiscoveryEndpoint = "http://192.168.123.152:3000"
	}
	if t.ControlPlaneInstallDisk == "" {
		t.ControlPlaneInstallDisk = "/dev/sda"
	}
	if t.WorkerInstallDisk == "" {
		t.WorkerInstallDisk = "/dev/nvme0n1"
	}
	if t.UserVolume.Disk == "" {
		t.UserVolume.Disk = "/dev/sdb"
	}
	if t.UserVolume.MinSize == "" {
		t.UserVolume.MinSize = "800GB"
	}
	if t.UserVolume.MaxSize == "" {
		t.UserVolume.MaxSize = "900GB"
	}
}

func applyProxmoxVMDefaults(vm *VMDefaults) {
	if vm.MemoryMB == 0 {
		vm.MemoryMB = 98304
	}
	if vm.Cores == 0 {
		vm.Cores = 16
	}
	if vm.BootDiskGB == 0 {
		vm.BootDiskGB = 100
	}
	if vm.OpenEBSDiskGB == 0 {
		vm.OpenEBSDiskGB = 800
	}
	if vm.BootStorage == "" {
		vm.BootStorage = "nvme1"
	}
	if vm.OpenEBSStorage == "" {
		vm.OpenEBSStorage = "nvmeof-vmdata"
	}
	if vm.NetworkBridge == "" {
		vm.NetworkBridge = "vmbr0"
	}
	if vm.NetworkMTU == 0 {
		vm.NetworkMTU = 9000
	}
	if vm.NetworkQueues == 0 {
		vm.NetworkQueues = 8
	}
	if vm.VLANID == 0 {
		vm.VLANID = 999
	}
	if vm.CPUType == "" {
		vm.CPUType = "host,flags=+pdpe1gb;-spec-ctrl"
	}
	if vm.SCSIController == "" {
		vm.SCSIController = "virtio-scsi-single"
	}
	if vm.WatchdogModel == "" {
		vm.WatchdogModel = "i6300esb"
	}
}

func applyTrueNASVMDefaults(vm *VMDefaults) {
	if vm.MemoryMB == 0 {
		vm.MemoryMB = 64 * 1024
	}
	if vm.Cores == 0 {
		vm.Cores = 16
	}
	if vm.BootDiskGB == 0 {
		vm.BootDiskGB = 250
	}
	if vm.OpenEBSDiskGB == 0 {
		vm.OpenEBSDiskGB = 800
	}
	if vm.BootStorage == "" {
		vm.BootStorage = DefaultTrueNASVMBootStorage
	}
	if vm.NetworkBridge == "" {
		vm.NetworkBridge = DefaultTrueNASNetworkBridge
	}
}

func applyVSphereVMDefaults(vm *VMDefaults) {
	if vm.MemoryMB == 0 {
		vm.MemoryMB = 64 * 1024
	}
	if vm.Cores == 0 {
		vm.Cores = 16
	}
	if vm.CoresPerSocket == 0 {
		vm.CoresPerSocket = 1
	}
	if vm.BootDiskGB == 0 {
		vm.BootDiskGB = 250
	}
	if vm.OpenEBSDiskGB == 0 {
		vm.OpenEBSDiskGB = 800
	}
	if vm.BootStorage == "" {
		vm.BootStorage = "local-nvme1"
	}
	if vm.OpenEBSStorage == "" {
		vm.OpenEBSStorage = defaultVSphereOpenEBSStorage
	}
	if vm.NetworkBridge == "" {
		vm.NetworkBridge = defaultVSphereNetwork
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
	out.Providers.VSphere = mergeProviderVMProfile(out.Providers.VSphere, sharedOverride)
	out.Providers.Talos = mergeProviderVMProfile(out.Providers.Talos, override.Providers.Talos)
	out.Providers.Flatcar = mergeProviderVMProfile(out.Providers.Flatcar, override.Providers.Flatcar)
	out.Providers.VSphere = mergeProviderVMProfile(out.Providers.VSphere, override.Providers.VSphere)
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
	if override.PCIDevice != "" {
		out.PCIDevice = override.PCIDevice
	}
	if override.RDMPath != "" {
		out.RDMPath = override.RDMPath
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
	if override.PCIDevice != "" {
		out.PCIDevice = override.PCIDevice
	}
	if override.RDMPath != "" {
		out.RDMPath = override.RDMPath
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
		PCIDevice:      profile.PCIDevice,
		RDMPath:        profile.RDMPath,
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
	if override.PCIDevice != "" {
		out.PCIDevice = override.PCIDevice
	}
	if override.RDMPath != "" {
		out.RDMPath = override.RDMPath
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
	p.Providers.VSphere = copyProviderVMProfile(p.Providers.VSphere)
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

// TrueNASPool returns the root pool/dataset prefix for lifecycle operations.
func (c *Config) TrueNASPool() string {
	storage := c.Hypervisors.TrueNAS.VM.BootStorage
	if storage == "" {
		return ""
	}
	if idx := strings.IndexByte(storage, '/'); idx >= 0 {
		return storage[:idx]
	}
	return storage
}

// TrueNASIgnitionDir returns the configured Flatcar Ignition staging dir, or
// the historical /mnt/<pool>/VM derivation when the key is unset.
func (c *Config) TrueNASIgnitionDir(pool string) string {
	if c.Hypervisors.TrueNAS.IgnitionDir != "" {
		return c.Hypervisors.TrueNAS.IgnitionDir
	}
	return filepath.Join("/mnt", pool, "VM")
}

// VSphereISOPath returns the default installer ISO datastore path.
func (c *Config) VSphereISOPath() string {
	return fmt.Sprintf("[%s] %s", c.Hypervisors.VSphere.ISODatastore, c.Hypervisors.VSphere.ISOFile)
}

// ProxmoxNodeName resolves the configured Proxmox node name, falling back to
// the built-in node default when the secret reference is absent/unresolved.
func (c *Config) ProxmoxNodeName() string {
	if node := c.ResolveSecretSilent(KeyProxmoxNode); node != "" {
		return node
	}
	return DefaultProxmoxNodeName
}
