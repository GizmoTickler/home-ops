// Package config loads the homeops configuration file (homeops.yaml) that
// defines cluster topology, hypervisor settings, state storage, and the
// mapping from semantic secret keys to secret-backend references.
//
// Discovery order (first hit wins):
//  1. --config flag / HOMEOPS_CONFIG environment variable
//  2. ./homeops.yaml
//  3. <git repo root>/homeops.yaml
//  4. ~/.config/homeops/config.yaml
//
// With no config file at all, built-in portable defaults apply: every secret
// resolves from environment variables (env://) and cluster state (kubeconfig,
// PKI) is stored in ~/.config/homeops/state. This means the tool is fully
// usable without 1Password — `op://` is just one of several secret backends.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"homeops-cli/internal/common"
	"homeops-cli/internal/secrets"
)

// EnvConfigFile names the environment variable that points at the config file.
const EnvConfigFile = "HOMEOPS_CONFIG"

// VMProfile customizes one node's VM hardware on the hypervisor. Unset
// fields keep the provider's built-in defaults.
type VMProfile struct {
	VMID           int    `yaml:"vmid,omitempty"`            // hypervisor VM id (Proxmox)
	Mac            string `yaml:"mac,omitempty"`             // static MAC address
	BootStorage    string `yaml:"boot_storage,omitempty"`    // pool/datastore for the boot disk
	OpenEBSStorage string `yaml:"openebs_storage,omitempty"` // pool/datastore for the OpenEBS/data disk
	CPUAffinity    string `yaml:"cpu_affinity,omitempty"`    // host core pinning (e.g. "0-7,32-39")
	NUMANode       *int   `yaml:"numa_node,omitempty"`       // host NUMA node
	// Ceph configures this node's Rook-Ceph OSD disk (overrides the provider
	// default and any built-in node profile).
	Ceph CephDisk `yaml:"ceph,omitempty"`
}

// CephDisk describes how a node gets its Rook-Ceph OSD disk.
type CephDisk struct {
	// Mode: "passthrough" (a physical disk via /dev/disk/by-id), "virtual"
	// (a plain virtual disk — no dedicated SSD needed), or "none" (no Ceph
	// disk, e.g. when not running Rook). Empty keeps the built-in default.
	Mode string `yaml:"mode,omitempty"`
	// DiskByID is the /dev/disk/by-id identifier (passthrough mode).
	DiskByID string `yaml:"disk_by_id,omitempty"`
	// SizeGB is the virtual disk size (virtual mode).
	SizeGB int `yaml:"size_gb,omitempty"`
	// Storage is the pool/datastore for the virtual disk (virtual mode);
	// defaults to the boot disk's storage when empty.
	Storage string `yaml:"storage,omitempty"`
}

// VMDefaults customizes the per-provider defaults for VM composition: sizing,
// disk layout/backends, and network attachment. Unset fields keep the
// provider's built-in defaults.
type VMDefaults struct {
	MemoryMB       int    `yaml:"memory_mb,omitempty"`
	Cores          int    `yaml:"cores,omitempty"`
	BootDiskGB     int    `yaml:"boot_disk_gb,omitempty"`
	OpenEBSDiskGB  int    `yaml:"openebs_disk_gb,omitempty"`
	BootStorage    string `yaml:"boot_storage,omitempty"`    // default pool/datastore for boot disks
	OpenEBSStorage string `yaml:"openebs_storage,omitempty"` // default pool/datastore for data disks
	// Ceph sets the default Rook-Ceph OSD disk for every node (per-node
	// cluster.nodes[].vm.ceph overrides this).
	Ceph          CephDisk `yaml:"ceph,omitempty"`
	NetworkBridge string   `yaml:"network_bridge,omitempty"`
	NetworkMTU    int      `yaml:"network_mtu,omitempty"`
	VLANID        int      `yaml:"vlan_id,omitempty"`
}

// Node is one control-plane node of the cluster.
type Node struct {
	Name string `yaml:"name"`
	IP   string `yaml:"ip"`
	// VM customizes this node's VM hardware profile on the hypervisor.
	VM VMProfile `yaml:"vm,omitempty"`
}

// ClusterConfig is the cluster topology section.
type ClusterConfig struct {
	// Name is informational (shown in config show).
	Name string `yaml:"name,omitempty"`
	// DomainRef is a secret reference resolving to the cluster base domain.
	// The apiserver endpoint is derived as "k8s." + domain unless Endpoint is
	// set explicitly. Optional: with neither set, no extra certSAN is added.
	DomainRef string `yaml:"domain_ref,omitempty"`
	// Endpoint optionally pins the apiserver DNS name (overrides derivation).
	Endpoint string `yaml:"endpoint,omitempty"`
	// ControlPlaneVIP is the kube-vip virtual IP fronting the apiserver.
	ControlPlaneVIP string `yaml:"control_plane_vip,omitempty"`
	// NodeInterface is the primary NIC name on the nodes (e.g. eth0).
	NodeInterface string `yaml:"node_interface,omitempty"`
	// Nodes are the control-plane nodes in order; the first is the kubeadm
	// init node.
	Nodes []Node `yaml:"nodes,omitempty"`
}

// ProxmoxConfig holds Proxmox-specific knobs.
type ProxmoxConfig struct {
	// SnippetsDir is where rendered Ignition files are uploaded on the PVE
	// host (read by qemu via fw_cfg).
	SnippetsDir string `yaml:"snippets_dir,omitempty"`
	// VM overrides the default VM composition (sizing, disk backends, network).
	VM VMDefaults `yaml:"vm,omitempty"`
}

// TrueNASConfig holds TrueNAS-specific knobs.
type TrueNASConfig struct {
	// ISODir is the dataset path where installer ISOs are stored.
	ISODir string `yaml:"iso_dir,omitempty"`
	// ISOFile is the default installer ISO filename within ISODir.
	ISOFile string `yaml:"iso_file,omitempty"`
	// SpiceHost is the address SPICE consoles bind to; defaults to the
	// TrueNAS host itself when empty.
	SpiceHost string `yaml:"spice_host,omitempty"`
	// ImageDir is where cloud images and NoCloud seed ISOs are staged on
	// the NAS for `vm create`. Defaults to an "images" directory next to
	// ISODir.
	ImageDir string `yaml:"image_dir,omitempty"`
	// VM overrides the default VM composition (sizing, zvol pool, network).
	// BootStorage doubles as the zvol parent dataset (e.g. "flashstor/VM").
	VM VMDefaults `yaml:"vm,omitempty"`
}

// VSphereConfig holds vSphere/ESXi-specific knobs.
type VSphereConfig struct {
	// VM overrides the default VM composition (sizing, datastores).
	VM VMDefaults `yaml:"vm,omitempty"`
	// Template is the default VM template `vm create` clones (cloud image
	// imported once with govc/ovftool or converted via
	// `vm template import --from-vm`). Override per-call with --template.
	Template string `yaml:"template,omitempty"`
}

// HypervisorsConfig groups per-hypervisor settings.
type HypervisorsConfig struct {
	// Default is the hypervisor used when --provider is not given
	// (proxmox | truenas | vsphere).
	Default string        `yaml:"default,omitempty"`
	Proxmox ProxmoxConfig `yaml:"proxmox,omitempty"`
	TrueNAS TrueNASConfig `yaml:"truenas,omitempty"`
	VSphere VSphereConfig `yaml:"vsphere,omitempty"`
}

// OpLocation addresses an item (and optionally a field) in 1Password.
type OpLocation struct {
	Vault string `yaml:"vault,omitempty"`
	Item  string `yaml:"item,omitempty"`
	Field string `yaml:"field,omitempty"`
}

// StoreConfig selects where a piece of cluster state (kubeconfig, PKI) is
// persisted: "op" (a 1Password item) or "file" (a local path, 0600).
type StoreConfig struct {
	Backend string     `yaml:"backend,omitempty"` // op | file
	Op      OpLocation `yaml:"op,omitempty"`
	// Path is the file-backend location: a file path for kubeconfig, a
	// directory for PKI. ~ is expanded.
	Path string `yaml:"path,omitempty"`
}

// StateConfig groups the persisted-state stores.
type StateConfig struct {
	Kubeconfig StoreConfig `yaml:"kubeconfig,omitempty"`
	PKI        StoreConfig `yaml:"pki,omitempty"`
}

// TemplatesConfig controls template resolution.
type TemplatesConfig struct {
	// Dir optionally points at a directory whose files shadow the embedded
	// templates by relative path (e.g. <dir>/talos/controlplane.yaml).
	Dir string `yaml:"dir,omitempty"`
}

// Config is the root of homeops.yaml.
type Config struct {
	Cluster     ClusterConfig     `yaml:"cluster,omitempty"`
	Hypervisors HypervisorsConfig `yaml:"hypervisors,omitempty"`
	State       StateConfig       `yaml:"state,omitempty"`
	Templates   TemplatesConfig   `yaml:"templates,omitempty"`
	// Images overrides the cloud-image catalog used by `vm create`: a map of
	// OS key (ubuntu, rocky, rhel, debian, fedora) to a qcow2 URL or a path
	// already present on the hypervisor. RHEL requires this (subscription).
	Images map[string]string `yaml:"images,omitempty"`
	// Secrets maps semantic secret keys (see Keys in keys.go) to secret
	// references (op://, env://, file://, cmd://, literal://). Keys not
	// listed here fall back to their portable env:// defaults.
	Secrets map[string]string `yaml:"secrets,omitempty"`

	// Source is the path the config was loaded from ("" = built-in defaults).
	Source string `yaml:"-"`
}

var (
	loadOnce sync.Once
	loaded   *Config
	loadErr  error
	loadPath string

	// explicitPath is set by the root command's --config flag before any
	// command runs.
	explicitPathMu sync.Mutex
	explicitPath   string
)

// SetExplicitPath records the --config flag value. Must be called before the
// first Get().
func SetExplicitPath(path string) {
	explicitPathMu.Lock()
	defer explicitPathMu.Unlock()
	explicitPath = path
}

// Get returns the process-wide configuration, loading it on first use. Load
// problems with an explicitly requested file are fatal; discovery problems
// fall back to defaults with a warning so read-only commands keep working.
func Get() *Config {
	loadOnce.Do(func() {
		loaded, loadPath, loadErr = load()
		if loadErr != nil {
			if loadPath != "" {
				common.NewColorLogger().Warn("homeops config %s: %v — continuing with built-in defaults", loadPath, loadErr)
			} else {
				common.NewColorLogger().Warn("homeops config: %v — continuing with built-in defaults", loadErr)
			}
			loaded = defaultConfig()
		}
		if loaded == nil {
			loaded = defaultConfig()
		}
		registerKeymap(loaded)
	})
	return loaded
}

// LoadError returns the error (if any) encountered loading the config file.
// Get() must have been called first.
func LoadError() error { return loadErr }

// ResetForTesting clears cached config state so tests can force a fresh load.
func ResetForTesting() {
	loadOnce = sync.Once{}
	loaded = nil
	loadErr = nil
	loadPath = ""
	explicitPathMu.Lock()
	explicitPath = ""
	explicitPathMu.Unlock()
}

// SetForTesting replaces the loaded config for the duration of a test.
// Returns a restore function the caller should defer.
func SetForTesting(c *Config) func() {
	old := loaded
	loadOnce.Do(func() {}) // mark as loaded
	if c == nil {
		c = defaultConfig()
	}
	applyDefaults(c)
	loaded = c
	registerKeymap(c)
	return func() {
		if old == nil {
			// SetForTesting ran before the first Get(); fall back to defaults
			// rather than reinstating a nil config.
			old = defaultConfig()
		}
		loaded = old
		registerKeymap(old)
	}
}

// registerKeymap wires the secret:// indirection scheme to this config.
func registerKeymap(c *Config) {
	secrets.RegisterKeymap(func(key string) (string, bool) {
		ref := c.SecretRef(key)
		return ref, ref != ""
	})
}

func load() (*Config, string, error) {
	path, explicit := Locate()
	if path == "" {
		return defaultConfig(), "", nil
	}
	cfg, err := LoadFile(path)
	if err != nil {
		if explicit {
			return nil, path, explicitLoadError{path: path, err: err}
		}
		return nil, path, fmt.Errorf("discovered config %s failed to load: %w", path, err)
	}
	return cfg, path, nil
}

type explicitLoadError struct {
	path string
	err  error
}

func (e explicitLoadError) Error() string {
	return e.err.Error()
}

func (e explicitLoadError) Unwrap() error {
	return e.err
}

// IsExplicitLoadError reports whether the config failure came from an explicit
// --config or HOMEOPS_CONFIG request and therefore must be treated as fatal.
func IsExplicitLoadError(err error) bool {
	var explicitErr explicitLoadError
	return err != nil && errors.As(err, &explicitErr)
}

// Locate finds the config file. Returns the path (or "") and whether the
// location was explicitly requested (flag/env) rather than discovered.
func Locate() (path string, explicit bool) {
	explicitPathMu.Lock()
	flagPath := explicitPath
	explicitPathMu.Unlock()
	if flagPath != "" {
		return flagPath, true
	}
	if envPath := os.Getenv(EnvConfigFile); envPath != "" {
		return envPath, true
	}
	// Test binaries must stay hermetic: never auto-discover a developer's real
	// config (which may point at live secret backends). Tests opt in via
	// SetForTesting or an explicit HOMEOPS_CONFIG.
	if strings.HasSuffix(os.Args[0], ".test") {
		return "", false
	}
	if _, err := os.Stat("homeops.yaml"); err == nil {
		abs, _ := filepath.Abs("homeops.yaml")
		return abs, false
	}
	if gitRoot, err := common.FindGitRoot("."); err == nil {
		candidate := filepath.Join(gitRoot, "homeops.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, false
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".config", "homeops", "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, false
		}
	}
	return "", false
}

// LoadFile parses a config file and applies built-in defaults to unset fields.
func LoadFile(path string) (*Config, error) {
	expanded, err := secrets.ExpandHome(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", expanded, err)
	}
	cfg := &Config{}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", expanded, err)
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config file %s: %w", expanded, err)
	}
	applyDefaults(cfg)
	cfg.Source = expanded
	return cfg, nil
}

// validate rejects obviously broken configs early with actionable messages.
func validate(c *Config) error {
	var problems []string
	for key, ref := range c.Secrets {
		if _, known := defaultSecretRefs[key]; !known {
			problems = append(problems, fmt.Sprintf("secrets.%s: unknown secret key (known keys: run 'homeops-cli config init --print-keys')", key))
		}
		if ref != "" && !secrets.IsReference(ref) {
			problems = append(problems, fmt.Sprintf("secrets.%s: %q is not a valid secret reference (expected op://, env://, file://, cmd://, or literal://)", key, ref))
		}
	}
	for _, store := range []struct {
		name string
		cfg  StoreConfig
	}{{"state.kubeconfig", c.State.Kubeconfig}, {"state.pki", c.State.PKI}} {
		switch store.cfg.Backend {
		case "", "op", "file":
		default:
			problems = append(problems, fmt.Sprintf("%s.backend: %q is not supported (use \"op\" or \"file\")", store.name, store.cfg.Backend))
		}
	}
	cephModes := []struct {
		name string
		mode string
	}{{"hypervisors.proxmox.vm.ceph", c.Hypervisors.Proxmox.VM.Ceph.Mode},
		{"hypervisors.truenas.vm.ceph", c.Hypervisors.TrueNAS.VM.Ceph.Mode},
		{"hypervisors.vsphere.vm.ceph", c.Hypervisors.VSphere.VM.Ceph.Mode}}
	for _, n := range c.Cluster.Nodes {
		cephModes = append(cephModes, struct {
			name string
			mode string
		}{fmt.Sprintf("cluster.nodes[%s].vm.ceph", n.Name), n.VM.Ceph.Mode})
	}
	for _, cm := range cephModes {
		switch cm.mode {
		case "", "passthrough", "virtual", "none":
		default:
			problems = append(problems, fmt.Sprintf("%s.mode: %q is not supported (use passthrough, virtual, or none)", cm.name, cm.mode))
		}
	}
	switch strings.ToLower(c.Hypervisors.Default) {
	case "", "proxmox", "truenas", "vsphere":
	default:
		problems = append(problems, fmt.Sprintf("hypervisors.default: %q is not supported (use proxmox, truenas, or vsphere)", c.Hypervisors.Default))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

// SecretRef returns the reference configured for a semantic secret key,
// falling back to the key's portable default. Returns "" for unknown keys.
func (c *Config) SecretRef(key string) string {
	if c != nil && c.Secrets != nil {
		if ref, ok := c.Secrets[key]; ok && ref != "" {
			return ref
		}
	}
	return defaultSecretRefs[key]
}

// ResolveSecret resolves a semantic secret key through its configured
// reference.
func (c *Config) ResolveSecret(key string) (string, error) {
	ref := c.SecretRef(key)
	if ref == "" {
		return "", fmt.Errorf("unknown secret key %q", key)
	}
	value, err := secrets.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("secret %s (%s): %w", key, ref, err)
	}
	return value, nil
}

// ResolveSecretSilent resolves a semantic key, returning "" on any failure.
func (c *Config) ResolveSecretSilent(key string) string {
	value, _ := c.ResolveSecret(key)
	return value
}

// UsesOpReferences reports whether any effective secret reference or state
// store uses the 1Password backend — i.e. whether the `op` CLI is required.
func (c *Config) UsesOpReferences() bool {
	for key := range defaultSecretRefs {
		if strings.HasPrefix(c.SecretRef(key), "op://") {
			return true
		}
	}
	return c.State.Kubeconfig.Backend == "op" || c.State.PKI.Backend == "op"
}

// NodeNames returns the configured node names in order.
func (c *Config) NodeNames() []string {
	names := make([]string, len(c.Cluster.Nodes))
	for i, n := range c.Cluster.Nodes {
		names[i] = n.Name
	}
	return names
}

// NodeByName returns the node with the given name.
func (c *Config) NodeByName(name string) (Node, bool) {
	for _, n := range c.Cluster.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return Node{}, false
}

// APIEndpoint returns the apiserver DNS name: the explicit endpoint if set,
// otherwise "k8s." + the resolved cluster domain, otherwise "".
func (c *Config) APIEndpoint() string {
	if c.Cluster.Endpoint != "" {
		return c.Cluster.Endpoint
	}
	if c.Cluster.DomainRef == "" {
		return ""
	}
	domain := strings.TrimSpace(secrets.ResolveSilent(c.Cluster.DomainRef))
	if domain == "" {
		return ""
	}
	return "k8s." + domain
}
