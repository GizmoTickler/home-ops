// Package config implements the `homeops-cli config` command group: scaffold,
// inspect, and validate the homeops configuration file that defines cluster
// topology and the mapping from semantic secret keys to secret backends.
package config

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/secrets"
	"homeops-cli/internal/state"

	"github.com/spf13/cobra"
)

// Swappable for tests.
var (
	lookPathFn = func(bin string) error {
		_, err := exec.LookPath(bin)
		return err
	}
	resolveRefFn    = secrets.Resolve
	locateConfigFn  = config.Locate
	loadConfigFn    = config.LoadFile
	currentConfigFn = config.Get
)

// NewCommand builds the `config` command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the homeops configuration file (homeops.yaml)",
		Long: `Manage the homeops configuration file that makes this CLI portable: cluster
topology (nodes, VIP), hypervisor settings, state stores, and the mapping from
semantic secret keys to secret backends (op://, env://, file://, cmd://).

Discovery order: --config flag > $HOMEOPS_CONFIG > ./homeops.yaml >
<git root>/homeops.yaml > ~/.config/homeops/config.yaml. With no file at all,
fully-portable defaults apply (env:// secrets, local-file state stores).`,
	}
	cmd.AddCommand(newInitCommand(), newShowCommand(), newDoctorCommand())
	return cmd
}

func newInitCommand() *cobra.Command {
	var output, backend string
	var force, printKeys bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a homeops.yaml config file",
		Long: `Write a commented homeops.yaml scaffold with every secret key, the cluster
topology, hypervisor settings, and state stores. Choose the secret backend
style to prefill (--backend env|op|file); every reference can be changed to any
backend afterwards.`,
		Example: `  # Scaffold with portable env:// references (default)
  homeops-cli config init

  # Scaffold prefilled with 1Password references for vault "Infrastructure"
  homeops-cli config init --backend op

  # Scaffold with file:// references under ~/.config/homeops/secrets
  homeops-cli config init --backend file --output-file ~/.config/homeops/config.yaml

  # List the canonical secret keys and their portable defaults
  homeops-cli config init --print-keys`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			if printKeys {
				for _, key := range config.KnownSecretKeys() {
					fmt.Printf("%-36s %s\n", key, config.DefaultSecretRef(key))
				}
				return nil
			}

			switch backend {
			case "env", "op", "file":
			default:
				return fmt.Errorf("--backend must be env, op, or file (got %q)", backend)
			}

			path, err := secrets.ExpandHome(output)
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists — pass --force to overwrite", path)
			}
			if dir := filepath.Dir(path); dir != "." {
				if err := os.MkdirAll(dir, 0755); err != nil {
					return fmt.Errorf("failed to create %s: %w", dir, err)
				}
			}
			if err := os.WriteFile(path, []byte(scaffold(backend)), 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", path, err)
			}
			logger.Success("Wrote %s", path)
			logger.Info("Next steps:")
			logger.Info("  1. Edit the file: set your node IPs, VIP, and secret references")
			logger.Info("  2. Validate it:   homeops-cli config doctor")
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output-file", "homeops.yaml", "where to write the config file")
	cmd.Flags().StringVar(&output, "output", "homeops.yaml", "Deprecated alias for --output-file")
	_ = cmd.Flags().MarkDeprecated("output", "use --output-file")
	_ = cmd.Flags().MarkHidden("output")
	cmd.Flags().StringVar(&backend, "backend", "env", "secret backend style to prefill: env, op, or file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.Flags().BoolVar(&printKeys, "print-keys", false, "print the canonical secret keys and defaults, then exit")
	return cmd
}

// scaffoldRef renders the prefilled reference for a key in the chosen style.
func scaffoldRef(key, backend string) string {
	def := config.DefaultSecretRef(key)
	switch backend {
	case "op":
		// Suggest a per-key field in a single item; users adjust freely.
		return fmt.Sprintf("op://Infrastructure/homeops/%s", key)
	case "file":
		return fmt.Sprintf("file://~/.config/homeops/secrets/%s", key)
	default:
		return def
	}
}

func scaffold(backend string) string {
	var b strings.Builder
	b.WriteString(`# homeops-cli configuration. See 'homeops-cli config --help'.
#
# Secret references support several backends; mix them freely:
#   op://vault/item/field    1Password CLI
#   env://VAR_NAME           environment variable
#   file:///path/to/file     file contents (~ expands)
#   cmd://command args       stdout of a command (pass, vault, sops...)
#   literal://value          the value itself (non-sensitive knobs)

cluster:
  name: my-cluster
  # Apiserver DNS name added to the cert SANs. Either set endpoint directly,
  # or set domain_ref and the endpoint is derived as "k8s." + domain.
  #endpoint: k8s.example.com
  #domain_ref: env://SECRET_DOMAIN
  control_plane_vip: 192.168.123.253
  node_interface: eth0
  nodes:
    - name: k8s-0
      ip: 192.168.122.10
      # Optional per-node VM hardware profile (unset fields keep defaults):
      #vm:
      #  vmid: 200
      #  mac: "00:a0:98:00:00:01"
      #  boot_storage: nvme-mirror
      #  openebs_storage: nvmeof-vmdata
      #  ceph:                               # Rook-Ceph OSD disk for this node
      #    mode: passthrough                 # passthrough | virtual | none
      #    disk_by_id: ata-INTEL_SSD...      # passthrough: physical disk id
      #    #size_gb: 500                     # virtual: disk size
      #    #storage: my-pool                 # virtual: pool/datastore
      #  cpu_affinity: "0-7,32-39"
      #  numa_node: 0
      #  providers:                          # provider-specific overlays
      #    talos:
      #      boot_storage: nvme1
      #    flatcar:
      #      boot_storage: nvme-mirror
    - name: k8s-1
      ip: 192.168.122.11
    - name: k8s-2
      ip: 192.168.122.12

hypervisors:
  default: proxmox          # proxmox | truenas | vsphere
  proxmox:
    snippets_dir: /var/lib/vz/snippets
    # Default VM composition for new VMs (unset fields keep built-in defaults):
    #vm:
    #  memory_mb: 98304
    #  cores: 16
    #  boot_disk_gb: 100
    #  openebs_disk_gb: 800
    #  boot_storage: nvme1
    #  openebs_storage: nvmeof-vmdata
    #  ceph:                      # default Rook-Ceph OSD disk for every node
    #    mode: virtual            # passthrough | virtual | none
    #    size_gb: 500             # virtual: disk size
    #    storage: my-pool         # virtual: pool/datastore (defaults to boot storage)
    #  network_bridge: vmbr0
    #  network_mtu: 9000
    #  vlan_id: 999
  truenas:
    iso_dir: /mnt/tank/ISO
    iso_file: metal-amd64.iso
    #spice_host: 0.0.0.0
    # Where 'vm create' stages cloud images and NoCloud seed ISOs on the NAS
    # (default: an "images" dir next to iso_dir):
    #image_dir: /mnt/tank/images
    #vm:
    #  boot_storage: tank/VM    # zvol parent dataset
  #vsphere:
  #  # Default template 'vm create --provider vsphere' clones (see
  #  # 'vm template import --help'); override per-call with --template.
  #  template: ubuntu-cloud-template
  #  vm:
  #    boot_storage: local-nvme1     # boot datastore
  #    openebs_storage: truenas-iscsi

# Where cluster state that must survive rebuilds lives: the admin kubeconfig
# and the kubeadm PKI. backend: file (local, 0600) or op (a 1Password item).
state:
  kubeconfig:
    backend: file
    path: ~/.config/homeops/state/kubeconfig
    #backend: op
    #op: {vault: Infrastructure, item: kubeconfig, field: kubeconfig}
  pki:
    backend: file
    path: ~/.config/homeops/state/pki
    #backend: op
    #op: {vault: Infrastructure, item: kubernetes-pki}

# Optional: override/pin the cloud-image catalog used by 'vm create'
# (ubuntu/rocky/debian/fedora resolve to latest stable automatically; RHEL
# requires this since its KVM guest image is subscription-gated).
#images:
#  rhel: /var/lib/vz/template/cache/rhel-10.1-x86_64-kvm.qcow2
#  ubuntu: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img

# Optional: a directory whose files shadow the embedded templates by relative
# path (e.g. <dir>/talos/controlplane.yaml).
#templates:
#  dir: ~/.config/homeops/templates

secrets:
`)
	for _, key := range config.KnownSecretKeys() {
		fmt.Fprintf(&b, "  %s: %s\n", key, scaffoldRef(key, backend))
	}
	return b.String()
}

func newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the effective configuration and where it came from",
		Long: `Print the effective configuration after defaults are applied. Secret values are
never shown — only their references.`,
		Example: `  homeops-cli config show`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := currentConfigFn()
			if cfg.Source != "" {
				fmt.Printf("# source: %s\n", cfg.Source)
			} else {
				fmt.Printf("# source: built-in defaults (no config file found — run 'homeops-cli config init')\n")
			}
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Print(string(out))

			// Show effective secret references including defaulted keys.
			fmt.Println("# effective secret references (defaults included):")
			for _, key := range config.KnownSecretKeys() {
				marker := ""
				if _, set := cfg.Secrets[key]; !set {
					marker = "   # default"
				}
				fmt.Printf("#   %-36s %s%s\n", key, cfg.SecretRef(key), marker)
			}
			return nil
		},
	}
	return cmd
}

func newDoctorCommand() *cobra.Command {
	var skipSecrets bool
	var network bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate the configuration and check that secrets resolve",
		Long: `Run health checks against the effective configuration: config file syntax,
required binaries, cluster topology sanity, state-store access, and (unless
--skip-secrets) that every configured secret reference actually resolves.
Secret values are never printed.

With --network, doctor additionally probes each configured hypervisor API
(Proxmox, TrueNAS, vSphere — providers without credentials are skipped) and
HEAD-checks the cloud-image catalog URLs used by 'vm create'.`,
		Example: `  # Full offline check
  homeops-cli config doctor

  # Also probe hypervisor APIs and image URLs
  homeops-cli config doctor --network

  # Skip resolving secret references (no 1Password/backend calls)
  homeops-cli config doctor --skip-secrets`,
		// A failed health check is not a usage error — don't dump help text.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(skipSecrets, network)
		},
	}
	cmd.Flags().BoolVar(&skipSecrets, "skip-secrets", false, "skip resolving secret references")
	cmd.Flags().BoolVar(&network, "network", false, "probe hypervisor APIs and image URLs (network calls)")
	return cmd
}

func runDoctor(skipSecrets, network bool) error {
	logger := common.NewColorLogger()
	failures := 0
	warn := func(format string, args ...interface{}) { logger.Warn(format, args...) }
	fail := func(format string, args ...interface{}) {
		failures++
		logger.Error(format, args...)
	}

	// 1. Config file
	path, explicit := locateConfigFn()
	if path == "" {
		warn("no config file found — using built-in defaults (env:// secrets, local state). Run 'homeops-cli config init' to create one")
	} else {
		if _, err := loadConfigFn(path); err != nil {
			fail("config file %s failed to load: %v", path, err)
			return fmt.Errorf("doctor found %d problem(s)", failures)
		}
		src := "discovered"
		if explicit {
			src = "explicit"
		}
		logger.Success("config: %s (%s)", path, src)
	}
	cfg := currentConfigFn()

	// 2. Topology sanity
	if len(cfg.Cluster.Nodes) == 0 {
		fail("cluster.nodes is empty")
	}
	for _, n := range cfg.Cluster.Nodes {
		if net.ParseIP(n.IP) == nil {
			fail("cluster.nodes: node %q has invalid IP %q", n.Name, n.IP)
		}
	}
	if net.ParseIP(cfg.Cluster.ControlPlaneVIP) == nil {
		fail("cluster.control_plane_vip %q is not a valid IP", cfg.Cluster.ControlPlaneVIP)
	} else {
		logger.Success("topology: %d node(s), VIP %s, interface %s", len(cfg.Cluster.Nodes), cfg.Cluster.ControlPlaneVIP, cfg.Cluster.NodeInterface)
	}

	// 3. Required binaries
	binaries := []string{"kubectl", "helmfile"}
	if cfg.UsesOpReferences() {
		binaries = append(binaries, "op")
	}
	for _, bin := range binaries {
		if err := lookPathFn(bin); err != nil {
			hint := ""
			if bin == "op" {
				hint = " (required because the config uses op:// references — https://developer.1password.com/docs/cli/get-started/)"
			}
			fail("binary %q not found in PATH%s", bin, hint)
		} else {
			logger.Success("binary: %s", bin)
		}
	}

	// 4. State stores
	logger.Info("state: kubeconfig -> %s", state.NewKubeconfigStore(cfg.State.Kubeconfig).Describe())
	logger.Info("state: pki        -> %s", state.NewPKIStore(cfg.State.PKI).Describe())

	// 5. Secret references
	if skipSecrets {
		logger.Info("secrets: skipped (--skip-secrets)")
	} else {
		keys := config.KnownSecretKeys()
		sort.Strings(keys)
		resolved, missed := 0, 0
		for _, key := range keys {
			ref := cfg.SecretRef(key)
			if _, err := resolveRefFn(ref); err != nil {
				missed++
				// Defaulted env:// keys that are unset are expected on most
				// setups — warn instead of fail unless explicitly configured.
				if _, set := cfg.Secrets[key]; set {
					fail("secret %-36s %s — %v", key, ref, err)
				} else {
					warn("secret %-36s %s (default) does not resolve — fine unless a command needs it", key, ref)
				}
			} else {
				resolved++
			}
		}
		logger.Success("secrets: %d/%d references resolve", resolved, resolved+missed)
	}

	// 6. Network probes (opt-in)
	if network {
		runNetworkChecks(logger, cfg, fail)
	}

	if failures > 0 {
		return fmt.Errorf("doctor found %d problem(s)", failures)
	}
	logger.Success("doctor: all checks passed")
	return nil
}
