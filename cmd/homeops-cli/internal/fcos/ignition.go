// Package fcos provides Fedora CoreOS + kubeadm provisioning helpers for
// homeops-cli: Butane->Ignition rendering for FCOS control-plane nodes and FCOS
// stream (image) resolution. It is the sibling of internal/flatcar and reuses it
// wherever the two OSes agree — the node env/variables, the kubeadm init/join
// templates and the shared template files — so an FCOS node differs from a
// Flatcar node only where the OS forces it (networking, Kubernetes binaries,
// OS updates, rollback, NTP). The kubeadm-over-SSH orchestration is OS-agnostic
// and stays in internal/flatcar.
package fcos

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/templates"

	butane "github.com/coreos/butane/config"
	butanecommon "github.com/coreos/butane/config/common"
)

// Pinned versions of the Kubernetes sysext payload that does not come from
// dl.k8s.io. The Flatcar sysext-bakery image bundles the same components (it
// resolves the latest CNI release at build time and ships crictl in the base
// OS); keep crictl on the pinned Kubernetes minor.
const (
	DefaultCrictlVersion     = "v1.36.0"
	DefaultCNIPluginsVersion = "v1.9.1"
)

// unresolvedPlaceholderRe matches a real (still-unsubstituted) {{ ENV.NAME }}
// placeholder. It intentionally requires an uppercase identifier so descriptive
// comments like "{{ ENV.* }}" in the templates are not flagged as unresolved.
var unresolvedPlaceholderRe = regexp.MustCompile(`{{ ENV\.[A-Z0-9_]+ }}`)

// Swappable function vars for testability, mirroring internal/flatcar.
var (
	renderFCOSTemplateFn    = templates.RenderFCOSTemplate
	listFCOSFilesFn         = templates.ListFCOSFiles
	renderFlatcarTemplateFn = templates.RenderFlatcarTemplate
	listFlatcarFilesFn      = templates.ListFlatcarFiles
	translateButaneFn       = translateButaneStrict
)

// translateButaneStrict transpiles Butane to Ignition and, like `butane
// --strict`, fails on ANY report entry (warnings included), not just fatal
// ones: a warning in a node's first-boot config is a latent outage.
//
// Automatic resource compression is disabled: the payload is small enough for
// fw_cfg either way, and uncompressed data URLs keep the golden files readable
// and independent of the Go toolchain's compress/flate output.
func translateButaneStrict(input []byte, dir string) ([]byte, error) {
	out, report, err := butane.TranslateBytes(input, butanecommon.TranslateBytesOptions{
		TranslateOptions: butanecommon.TranslateOptions{
			FilesDir:                  dir,
			NoResourceAutoCompression: true,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(report.Entries) > 0 {
		return nil, fmt.Errorf("butane transpile reported problems (strict mode): %s", report.String())
	}
	return out, nil
}

// NodeEnv holds the per-node values needed to render the FCOS templates: every
// Flatcar/kubeadm variable (embedded, so builders and the kubeadm renderers
// are shared) plus the FCOS-only sysext pins.
type NodeEnv struct {
	flatcar.NodeEnv

	CrictlVersion     string // CRICTL_VERSION (default DefaultCrictlVersion)
	CNIPluginsVersion string // CNI_PLUGINS_VERSION (default DefaultCNIPluginsVersion)
}

// envMap starts from the shared Flatcar map and overrides what differs on FCOS:
// storage NICs become NetworkManager keyfiles and NTP servers become chrony
// "server" lines. NFS trunk anchors are plain systemd mount units and are
// reused as-is.
func (e NodeEnv) envMap() map[string]string {
	m := e.NodeEnv.EnvMap()
	crictl := e.CrictlVersion
	if crictl == "" {
		crictl = DefaultCrictlVersion
	}
	cni := e.CNIPluginsVersion
	if cni == "" {
		cni = DefaultCNIPluginsVersion
	}
	m[constants.EnvCrictlVersion] = crictl
	m[constants.EnvCNIPluginsVersion] = cni
	// Always replaced (possibly with ""), exactly like the Flatcar map, so
	// nodes without storage NICs keep the base file set.
	m[constants.EnvStorageNetworkFiles] = formatStorageNMConnections(e.StorageNICs, m[constants.EnvNetworkMTU])
	m[constants.EnvChronyServers] = formatChronyServers(m[constants.EnvNTPServers])
	return m
}

// formatStorageNMConnections renders the NVMe-oF storage fabric NICs as
// NetworkManager keyfiles — the FCOS port of Flatcar's 30-stor<vlan>.network
// units. Each keyfile is matched by MAC (like [Match] MACAddress=), carries the
// jumbo MTU and the static /24, and has no gateway (never-default), no IPv6 or
// link-local addressing (LinkLocalAddressing=no) and no LLDP (LLDP=no).
func formatStorageNMConnections(storageNICs []config.StorageNIC, mtu string) string {
	if len(storageNICs) == 0 {
		return ""
	}

	nics := append([]config.StorageNIC(nil), storageNICs...)
	sort.Slice(nics, func(i, j int) bool { return nics[i].VLAN < nics[j].VLAN })
	var b strings.Builder
	for index, nic := range nics {
		if index > 0 {
			b.WriteByte('\n')
		}
		_, _ = fmt.Fprintf(&b, `    - path: /etc/NetworkManager/system-connections/30-stor%d.nmconnection
      mode: 0600
      overwrite: true
      contents:
        inline: |
          [connection]
          id=30-stor%d
          type=ethernet
          autoconnect=true
          lldp=0

          [ethernet]
          mac-address=%s
          mtu=%s

          [ipv4]
          method=manual
          address1=%s
          never-default=true

          [ipv6]
          method=disabled
`, nic.VLAN, nic.VLAN, nic.MAC, mtu, nic.IP)
	}
	return b.String()
}

// formatChronyServers turns the space-separated NTP_SERVERS value into chrony
// "server <addr> iburst" lines (one per configured server, in order).
func formatChronyServers(ntpServers string) string {
	fields := strings.Fields(ntpServers)
	lines := make([]string, 0, len(fields))
	for _, server := range fields {
		lines = append(lines, "server "+server+" iburst")
	}
	return strings.Join(lines, "\n")
}

// filesDirLayer is one embedded template tree materialized into the Butane
// FilesDir. Later layers overwrite earlier ones file-by-file.
type filesDirLayer struct {
	name    string
	subdirs []string
	list    func(subdir string) ([]string, error)
	render  func(name string, env map[string]string) (string, error)
}

// RenderIgnition renders the FCOS control-plane Butane config for a node and
// transpiles it (strictly) to Ignition JSON. The Butane FilesDir is the Flatcar
// files/ + manifests/ with the FCOS files/ + systemd/ overlaid, all with the
// same {{ ENV.* }} substitution, so shared files are rendered from their single
// Flatcar source and FCOS-only files (or FCOS replacements, e.g. the modules
// list) come from internal/templates/fcos.
//
// Returns the Ignition JSON bytes. The temp FilesDir is removed before returning.
func RenderIgnition(env NodeEnv) ([]byte, error) {
	envVars := env.envMap()

	butaneDoc, err := renderFCOSTemplateFn("butane/controlplane.bu", envVars)
	if err != nil {
		return nil, fmt.Errorf("failed to render FCOS Butane controlplane: %w", err)
	}
	// Fail loudly on an unresolved placeholder rather than baking a literal
	// "{{ ENV.X }}" into Ignition (a 1Password miss for the SSH key would
	// otherwise produce an unreachable node).
	if m := unresolvedPlaceholderRe.FindString(butaneDoc); m != "" {
		return nil, fmt.Errorf("butane controlplane has unresolved placeholder %s: missing required ENV value (1Password miss for SSH key / domain?)", m)
	}

	dir, err := os.MkdirTemp("", "homeops-fcos-files-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp files dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	layers := []filesDirLayer{
		{name: "flatcar", subdirs: []string{"files", "manifests"}, list: listFlatcarFilesFn, render: renderFlatcarTemplateFn},
		{name: "fcos", subdirs: []string{"files", "systemd"}, list: listFCOSFilesFn, render: renderFCOSTemplateFn},
	}
	for _, layer := range layers {
		for _, subdir := range layer.subdirs {
			if err := materializeSubdir(dir, subdir, layer, envVars); err != nil {
				return nil, err
			}
		}
	}

	ign, err := translateButaneFn([]byte(butaneDoc), dir)
	if err != nil {
		return nil, fmt.Errorf("failed to transpile FCOS Butane to Ignition: %w", err)
	}
	return ign, nil
}

// materializeSubdir writes every embedded file of layer under <subdir> into
// <baseDir>/<subdir>/<name>, applying ENV substitution to each.
func materializeSubdir(baseDir, subdir string, layer filesDirLayer, envVars map[string]string) error {
	names, err := layer.list(subdir)
	if err != nil {
		return fmt.Errorf("failed to list %s %s: %w", layer.name, subdir, err)
	}

	targetDir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return fmt.Errorf("failed to create %s dir: %w", subdir, err)
	}

	for _, name := range names {
		rendered, err := layer.render(name, envVars)
		if err != nil {
			return fmt.Errorf("failed to render %s file %s: %w", layer.name, name, err)
		}
		if m := unresolvedPlaceholderRe.FindString(rendered); m != "" {
			return fmt.Errorf("%s file %s has unresolved placeholder %s: missing required ENV value", layer.name, name, m)
		}
		dest := filepath.Join(targetDir, filepath.Base(name))
		if err := os.WriteFile(dest, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("failed to write %s file %s: %w", layer.name, dest, err)
		}
	}
	return nil
}

// RenderKubeadmInitConfig renders the kubeadm init configuration (node0). The
// kubeadm configs are OS-independent, so this is the Flatcar template.
func RenderKubeadmInitConfig(env NodeEnv) (string, error) {
	return flatcar.RenderKubeadmInitConfig(env.NodeEnv)
}

// RenderKubeadmJoinConfig renders the kubeadm join configuration (node1/node2)
// from the shared Flatcar template.
func RenderKubeadmJoinConfig(env NodeEnv) (string, error) {
	return flatcar.RenderKubeadmJoinConfig(env.NodeEnv)
}
