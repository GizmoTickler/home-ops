package fcos

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"homeops-cli/internal/config"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleEnv() NodeEnv {
	return NodeEnv{NodeEnv: flatcar.NodeEnv{
		NodeName:          "k8s-0",
		NodeIP:            "192.168.122.10",
		Node0IP:           "192.168.122.10",
		Node1IP:           "192.168.122.11",
		Node2IP:           "192.168.122.12",
		KubernetesVersion: "v1.36.1",
		KubernetesMinor:   "v1.36",
		ControlPlaneVIP:   "192.168.123.253",
		PauseImage:        "registry.k8s.io/pause:3.10",
		KubeVipVersion:    "v0.8.9",
		NodeInterface:     "eth0",
		NodeMAC:           "00:a0:98:28:c8:83",
		NodeMACIoT:        "bc:24:11:b9:55:83",
		NodeMACVPN:        "bc:24:11:33:4c:37",
		K8sEndpoint:       "k8s.example.test",
		SSHAuthorizedKey:  "ssh-ed25519 AAAATESTKEY",
	}}
}

// ignitionFile is one decoded storage.files entry.
type ignitionFile struct {
	Path     string
	Mode     int
	Contents string
}

func ignitionFiles(t *testing.T, ign []byte) map[string]ignitionFile {
	t.Helper()
	var doc struct {
		Storage struct {
			Files []struct {
				Path     string `json:"path"`
				Mode     int    `json:"mode"`
				Contents struct {
					Compression string `json:"compression"`
					Source      string `json:"source"`
				} `json:"contents"`
			} `json:"files"`
		} `json:"storage"`
	}
	require.NoError(t, json.Unmarshal(ign, &doc))
	out := make(map[string]ignitionFile, len(doc.Storage.Files))
	for _, f := range doc.Storage.Files {
		// Auto-compression is disabled for FCOS, so every source is a plain
		// (percent- or base64-encoded) data URL.
		require.Empty(t, f.Contents.Compression, "%s must not be compressed", f.Path)
		source := f.Contents.Source
		var body string
		switch {
		case strings.HasPrefix(source, "data:;base64,"):
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(source, "data:;base64,"))
			require.NoError(t, err)
			body = string(decoded)
		case strings.HasPrefix(source, "data:,"):
			decoded, err := url.PathUnescape(strings.TrimPrefix(source, "data:,"))
			require.NoError(t, err)
			body = decoded
		default:
			t.Fatalf("unsupported data URL for %s: %q", f.Path, source)
		}
		out[f.Path] = ignitionFile{Path: f.Path, Mode: f.Mode, Contents: body}
	}
	return out
}

func ignitionFileContent(t *testing.T, ign []byte, path string) string {
	t.Helper()
	f, ok := ignitionFiles(t, ign)[path]
	require.True(t, ok, "Ignition file %s not found", path)
	return f.Contents
}

type ignitionDropin struct {
	Name     string `json:"name"`
	Contents string `json:"contents"`
}

type ignitionSystemdUnit struct {
	Name     string           `json:"name"`
	Enabled  *bool            `json:"enabled"`
	Mask     *bool            `json:"mask"`
	Contents string           `json:"contents"`
	Dropins  []ignitionDropin `json:"dropins"`
}

func ignitionSystemdUnits(t *testing.T, ign []byte) map[string]ignitionSystemdUnit {
	t.Helper()
	var doc struct {
		Systemd struct {
			Units []ignitionSystemdUnit `json:"units"`
		} `json:"systemd"`
	}
	require.NoError(t, json.Unmarshal(ign, &doc))
	units := make(map[string]ignitionSystemdUnit, len(doc.Systemd.Units))
	for _, unit := range doc.Systemd.Units {
		units[unit.Name] = unit
	}
	return units
}

func TestRenderIgnitionProducesStrictValidFCOSIgnition(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)

	var doc struct {
		Ignition struct {
			Version string `json:"version"`
		} `json:"ignition"`
	}
	require.NoError(t, json.Unmarshal(ign, &doc))
	// variant fcos 1.7.0 (newest stable spec in the vendored butane) -> Ignition 3.6.0.
	assert.Equal(t, "3.6.0", doc.Ignition.Version)
	assert.Contains(t, string(ign), "data:,k8s-0")
	assert.NotRegexp(t, `{{ ENV\.[A-Z0-9_]+ }}`, string(ign))
}

func TestRenderIgnitionRejectsUnresolvedPlaceholder(t *testing.T) {
	env := sampleEnv()
	env.SSHAuthorizedKey = ""
	_, err := RenderIgnition(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved placeholder")
	assert.Contains(t, err.Error(), "SSH_AUTHORIZED_KEY")
}

// TestRenderIgnitionFCOSInvariants is the guard the migration relies on: an FCOS
// node must never be handed a Flatcar-only mechanism (systemd-networkd units,
// the extensions.flatcar.org sysext), and must always carry the FCOS
// replacements (Zincati off, greenboot check, zram override, ublk_drv).
func TestRenderIgnitionFCOSInvariants(t *testing.T) {
	env := sampleEnv()
	env.StorageNICs = []config.StorageNIC{
		{VLAN: 1201, MAC: "BC:24:11:3B:E0:50", IP: "192.168.201.20/24"},
		{VLAN: 1202, MAC: "BC:24:11:ED:F6:B6", IP: "192.168.202.20/24"},
	}
	ign, err := RenderIgnition(env)
	require.NoError(t, err)
	files := ignitionFiles(t, ign)

	var everything strings.Builder
	everything.Write(ign)
	for _, f := range files {
		everything.WriteString(f.Contents)
		// No systemd-networkd config: FCOS runs NetworkManager and would ignore it.
		assert.False(t, strings.HasSuffix(f.Path, ".network"), "systemd-networkd file shipped to FCOS: %s", f.Path)
		assert.False(t, strings.HasPrefix(f.Path, "/etc/flatcar/"), "Flatcar-only file shipped to FCOS: %s", f.Path)
	}
	for _, unit := range ignitionSystemdUnits(t, ign) {
		everything.WriteString(unit.Contents)
		for _, dropin := range unit.Dropins {
			everything.WriteString(dropin.Contents)
		}
		assert.NotContains(t, unit.Name, "sysupdate", "systemd-sysupdate is Flatcar-only")
		assert.NotEqual(t, "locksmithd.service", unit.Name)
	}
	// The Flatcar sysext images carry ID=flatcar and do not merge on FCOS.
	assert.NotContains(t, everything.String(), "extensions.flatcar.org")

	zincati, ok := files["/etc/zincati/config.d/90-disable-auto-updates.toml"]
	require.True(t, ok, "Zincati must be disabled (it is active by default on FCOS)")
	assert.Equal(t, "[updates]\nenabled = false\n", zincati.Contents)

	check, ok := files["/etc/greenboot/check/required.d/50-homeops-kubernetes.sh"]
	require.True(t, ok, "greenboot required health check missing")
	assert.Equal(t, 0o755, check.Mode)
	assert.Contains(t, check.Contents, "containerd.service")
	assert.Contains(t, check.Contents, "kubelet.service")
	assert.Contains(t, check.Contents, "/etc/kubernetes/kubelet.conf")

	zram, ok := files["/etc/systemd/zram-generator.conf"]
	require.True(t, ok, "empty zram-generator override missing (F45 enables zram swap by default)")
	assert.Empty(t, zram.Contents, "the override must define no zram devices")

	modules := strings.Fields(files["/etc/modules-load.d/homeops.conf"].Contents)
	for _, module := range []string{"ublk_drv", "nvme_tcp", "nbd", "vfio-pci", "uio_pci_generic"} {
		assert.Contains(t, modules, module)
	}

	// Every NetworkManager keyfile must be 0600 or NM silently ignores it.
	keyfiles := 0
	for path, f := range files {
		if strings.HasPrefix(path, "/etc/NetworkManager/system-connections/") {
			keyfiles++
			assert.True(t, strings.HasSuffix(path, ".nmconnection"), path)
			assert.Equal(t, 0o600, f.Mode, path)
		}
	}
	assert.Equal(t, 5, keyfiles, "eth0 + eth1 + eth2 + two storage NICs")
}

func TestRenderIgnitionPortsPrimaryAndMacvlanNetworking(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Hypervisors: config.HypervisorsConfig{
			Proxmox: config.ProxmoxConfig{VM: config.VMDefaults{NetworkMTU: 1400}},
		},
	})
	defer restore()

	env := sampleEnv()
	ign, err := RenderIgnition(env)
	require.NoError(t, err)

	// Interface names are still pinned by MAC with udev .link files.
	for link, mac := range map[string]string{
		"/etc/systemd/network/10-eth0.link": env.NodeMAC,
		"/etc/systemd/network/10-eth1.link": env.NodeMACIoT,
		"/etc/systemd/network/10-eth2.link": env.NodeMACVPN,
	} {
		name := strings.TrimSuffix(strings.TrimPrefix(link, "/etc/systemd/network/10-"), ".link")
		assert.Equal(t, "[Match]\nMACAddress="+mac+"\n[Link]\nName="+name+"\n", ignitionFileContent(t, ign, link))
	}

	primary := ignitionFileContent(t, ign, "/etc/NetworkManager/system-connections/10-k8s.nmconnection")
	assert.Contains(t, primary, "interface-name=eth0\n")
	assert.Contains(t, primary, "[ethernet]\nmtu=1400\n")
	assert.Contains(t, primary, "[ipv4]\nmethod=auto\nmay-fail=false\n")

	// The macvlan masters stay address-less: no DHCP, no link-local, no RA.
	// A DHCP'd eth1 once installed a second default route via the IoT VLAN.
	for path, iface := range map[string]string{
		"/etc/NetworkManager/system-connections/15-eth1-iot.nmconnection": "eth1",
		"/etc/NetworkManager/system-connections/15-eth2-vpn.nmconnection": "eth2",
	} {
		body := ignitionFileContent(t, ign, path)
		assert.Contains(t, body, "interface-name="+iface+"\n", path)
		assert.Contains(t, body, "[ipv4]\nmethod=disabled\n", path)
		assert.Contains(t, body, "[ipv6]\nmethod=disabled\n", path)
		assert.NotContains(t, body, "method=auto", path)
	}
}

func TestRenderIgnitionStorageNICsBecomeNMKeyfiles(t *testing.T) {
	env := sampleEnv()
	env.StorageNICs = []config.StorageNIC{
		// Deliberately unsorted: output order must follow the VLAN.
		{VLAN: 1204, MAC: "BC:24:11:FB:16:76", IP: "192.168.204.20/24"},
		{VLAN: 1201, MAC: "BC:24:11:3B:E0:50", IP: "192.168.201.20/24"},
	}
	first, err := RenderIgnition(env)
	require.NoError(t, err)
	second, err := RenderIgnition(env)
	require.NoError(t, err)
	assert.Equal(t, first, second, "re-rendering the same node must be byte-stable")

	for _, nic := range env.StorageNICs {
		path := fmt.Sprintf("/etc/NetworkManager/system-connections/30-stor%d.nmconnection", nic.VLAN)
		want := fmt.Sprintf("[connection]\nid=30-stor%d\ntype=ethernet\nautoconnect=true\nlldp=0\n\n"+
			"[ethernet]\nmac-address=%s\nmtu=9000\n\n"+
			"[ipv4]\nmethod=manual\naddress1=%s\nnever-default=true\n\n"+
			"[ipv6]\nmethod=disabled\n", nic.VLAN, nic.MAC, nic.IP)
		assert.Equal(t, want, ignitionFileContent(t, first, path), path)
	}

	var order []string
	for path := range ignitionFiles(t, first) {
		if strings.Contains(path, "30-stor") {
			order = append(order, path)
		}
	}
	sort.Strings(order)
	assert.Len(t, order, 2)
}

func TestRenderIgnitionWithoutStorageNICsKeepsKeyfilesAbsent(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	assert.NotContains(t, string(ign), "30-stor")
}

func TestRenderIgnitionUsesChronyForNTP(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Cluster: config.ClusterConfig{NTPServers: []string{"10.0.0.1", "10.0.0.2"}},
	})
	defer restore()

	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	chrony := ignitionFileContent(t, ign, "/etc/chrony.conf")
	assert.Contains(t, chrony, "\nserver 10.0.0.1 iburst\nserver 10.0.0.2 iburst\n")
	assert.NotContains(t, chrony, "\npool ", "only the configured servers may be used")
	assert.NotContains(t, chrony, "\nsourcedir ", "DHCP-provided NTP sources are not used")
	_, hasTimesyncd := ignitionFiles(t, ign)["/etc/systemd/timesyncd.conf"]
	assert.False(t, hasTimesyncd, "FCOS runs chronyd, not timesyncd")
}

func TestRenderIgnitionReusesFlatcarSharedFiles(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	// Shared files come from their single Flatcar source (ENV-substituted), so
	// the FCOS copy can never drift from what Flatcar nodes run.
	for path, want := range map[string]string{
		"/etc/sysctl.d/99-homeops.conf":         "sysctl-99-homeops.conf",
		"/etc/nfsmount.conf":                    "nfsmount.conf",
		"/etc/kubernetes/audit/policy.yaml":     "audit-policy.yaml",
		"/etc/kubernetes/scheduler/config.yaml": "scheduler-config.yaml",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "templates", "flatcar", "files", want))
		require.NoError(t, err)
		assert.Equal(t, string(raw), ignitionFileContent(t, ign, path), path)
	}
	containerd := ignitionFileContent(t, ign, "/etc/containerd/config.toml")
	assert.Contains(t, containerd, "sandbox = 'registry.k8s.io/pause:3.10'")
	assert.Contains(t, containerd, "SystemdCgroup = true")
	kubeVip := ignitionFileContent(t, ign, "/etc/kubernetes/manifests/kube-vip.yaml")
	assert.Contains(t, kubeVip, "ghcr.io/kube-vip/kube-vip:v0.8.9")
	assert.Contains(t, kubeVip, `value: "eth0"`)
}

func TestRenderIgnitionBuildsKubernetesDirectorySysext(t *testing.T) {
	env := sampleEnv()
	env.CrictlVersion = "v1.36.9"
	env.CNIPluginsVersion = "v1.9.9"
	ign, err := RenderIgnition(env)
	require.NoError(t, err)

	script := ignitionFileContent(t, ign, "/usr/local/bin/homeops-install-k8s-sysext")
	assert.Contains(t, script, `K8S_VERSION="${1:-v1.36.1}"`)
	assert.Contains(t, script, `CRICTL_VERSION="v1.36.9"`)
	assert.Contains(t, script, `CNI_VERSION="v1.9.9"`)
	assert.Contains(t, script, "https://dl.k8s.io/release/${K8S_VERSION}/bin/linux/${ARCH}/${bin}")
	assert.Contains(t, script, `fetch "${url}.sha256"`)
	assert.Contains(t, script, "sha256sum --check --strict")
	assert.Contains(t, script, "EXT_DIR=/var/lib/extensions/kubernetes")
	assert.Contains(t, script, "printf 'ID=_any\\n' >\"${STAGE}/usr/lib/extension-release.d/extension-release.kubernetes\"")
	assert.Contains(t, script, "systemd-sysext refresh")
	assert.NotContains(t, script, "touch /run/reboot-required", "a binary swap never needs a reboot")

	units := ignitionSystemdUnits(t, ign)
	install := units["install-k8s-sysext.service"]
	require.NotNil(t, install.Enabled)
	assert.True(t, *install.Enabled)
	assert.Contains(t, install.Contents, "After=network-online.target")
	assert.Contains(t, install.Contents, "ConditionPathExists=!/var/lib/homeops/k8s-sysext-v1.36.1.stamp")
	assert.Contains(t, install.Contents, "ExecStart=/usr/local/bin/homeops-install-k8s-sysext")

	// kubelet.service + 10-kubeadm.conf are the upstream kubeadm packaging, verbatim.
	kubelet := units["kubelet.service"]
	upstreamUnit, err := os.ReadFile(filepath.Join("..", "templates", "fcos", "systemd", "kubelet.service"))
	require.NoError(t, err)
	assert.Equal(t, string(upstreamUnit), kubelet.Contents)
	dropins := map[string]string{}
	for _, d := range kubelet.Dropins {
		dropins[d.Name] = d.Contents
	}
	upstreamDropin, err := os.ReadFile(filepath.Join("..", "templates", "fcos", "systemd", "10-kubeadm.conf"))
	require.NoError(t, err)
	assert.Equal(t, string(upstreamDropin), dropins["10-kubeadm.conf"])
	assert.Contains(t, dropins["20-homeops-fcos.conf"], "ExecStartPre=/usr/bin/cp -r /usr/libexec/cni/. /opt/cni/bin/")

	assert.True(t, *units["systemd-sysext.service"].Enabled)
	assert.True(t, *units["docker.service"].Mask)
	assert.True(t, *units["docker.socket"].Mask)
}

func TestRenderIgnitionLayersGreenbootBeforeKubernetes(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	units := ignitionSystemdUnits(t, ign)

	layer := units["homeops-layer-greenboot.service"]
	require.NotNil(t, layer.Enabled)
	assert.True(t, *layer.Enabled)
	assert.Contains(t, layer.Contents, "ExecStart=/usr/bin/rpm-ostree install --idempotent --allow-inactive greenboot greenboot-default-health-checks")
	assert.Contains(t, layer.Contents, "Before=install-k8s-sysext.service kubelet.service")
	// Exactly once, and never on a node that already joined the cluster.
	assert.Contains(t, layer.Contents, "ConditionPathExists=!/var/lib/homeops/greenboot-layered")
	assert.Contains(t, layer.Contents, "ConditionPathExists=!/etc/kubernetes/kubelet.conf")
	// The stamp is written BEFORE the reboot is queued.
	stamp := strings.Index(layer.Contents, "touch /var/lib/homeops/greenboot-layered")
	reboot := strings.Index(layer.Contents, "systemctl --no-block reboot")
	require.Positive(t, stamp)
	assert.Less(t, stamp, reboot)

	// Kubernetes binaries are not built until the layering unit succeeded.
	install := units["install-k8s-sysext.service"].Contents
	assert.Contains(t, install, "Requires=homeops-layer-greenboot.service")
	assert.Contains(t, install, "After=network-online.target nss-lookup.target homeops-layer-greenboot.service")

	// greenboot 0.16 (Fedora 44) ships exactly these two units; enabling a
	// unit it no longer ships fails the whole enable (seen on a live node).
	enable := units["homeops-enable-greenboot.service"].Contents
	assert.Contains(t, enable, "systemctl enable --now greenboot-healthcheck.service greenboot-set-rollback-trigger.service\n")
	for _, gone := range []string{"greenboot-task-runner", "greenboot-grub2-", "greenboot-status", "redboot-"} {
		assert.NotContains(t, enable, gone)
	}
}

// TestRenderedShellScriptsParse runs `bash -n` over the shipped scripts so a
// template edit cannot hand a node a first-boot script with a syntax error.
func TestRenderedShellScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	for _, path := range []string{
		"/usr/local/bin/homeops-install-k8s-sysext",
		"/etc/greenboot/check/required.d/50-homeops-kubernetes.sh",
	} {
		script := filepath.Join(t.TempDir(), filepath.Base(path))
		require.NoError(t, os.WriteFile(script, []byte(ignitionFileContent(t, ign, path)), 0o600))
		out, err := exec.Command(bash, "-n", script).CombinedOutput() // #nosec G204 -- fixed test binary and temp file
		assert.NoError(t, err, "%s: %s", path, out)
	}
}

func TestTranslateButaneStrictFailsOnWarnings(t *testing.T) {
	// An enabled unit without an [Install] section is only a Butane WARNING;
	// strict mode must turn it into an error.
	doc := []byte(`variant: fcos
version: 1.7.0
systemd:
  units:
    - name: warn.service
      enabled: true
      contents: |
        [Service]
        ExecStart=/usr/bin/true
`)
	_, err := translateButaneStrict(doc, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strict mode")
}

func TestRenderIgnitionTranspileError(t *testing.T) {
	testutil.Swap(t, &translateButaneFn, func(input []byte, dir string) ([]byte, error) {
		return nil, errors.New("boom")
	})
	_, err := RenderIgnition(sampleEnv())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transpile")
}

func TestRenderIgnitionFCOSFilesOverlayFlatcarFiles(t *testing.T) {
	var rendered []string
	origFCOS := renderFCOSTemplateFn
	origFlatcar := renderFlatcarTemplateFn
	testutil.Swap(t, &renderFCOSTemplateFn, func(name string, env map[string]string) (string, error) {
		rendered = append(rendered, "fcos/"+name)
		return origFCOS(name, env)
	})
	testutil.Swap(t, &renderFlatcarTemplateFn, func(name string, env map[string]string) (string, error) {
		rendered = append(rendered, "flatcar/"+name)
		return origFlatcar(name, env)
	})

	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	assert.Contains(t, rendered, "fcos/butane/controlplane.bu")
	assert.Contains(t, rendered, "flatcar/files/containerd-config.toml")
	assert.Contains(t, rendered, "flatcar/manifests/kube-vip.yaml")
	assert.Contains(t, rendered, "fcos/files/modules-load-homeops.conf")
	assert.NotContains(t, rendered, "flatcar/butane/controlplane.bu")
	// The FCOS modules list shadows the Flatcar one of the same name.
	assert.Contains(t, ignitionFileContent(t, ign, "/etc/modules-load.d/homeops.conf"), "ublk_drv")
}

func TestMaterializeSubdirWritesRenderedFiles0600(t *testing.T) {
	layer := filesDirLayer{
		name:    "fcos",
		subdirs: []string{"files"},
		list: func(subdir string) ([]string, error) {
			assert.Equal(t, "files", subdir)
			return []string{"files/secret-ish.sh"}, nil
		},
		render: func(name string, env map[string]string) (string, error) {
			return "token=" + env["TOKEN"], nil
		},
	}
	baseDir := t.TempDir()
	require.NoError(t, materializeSubdir(baseDir, "files", layer, map[string]string{"TOKEN": "abc"}))
	info, err := os.Stat(filepath.Join(baseDir, "files", "secret-ish.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	layer.render = func(string, map[string]string) (string, error) { return "{{ ENV.MISSING }}", nil }
	err = materializeSubdir(baseDir, "files", layer, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved placeholder")
}

func TestRenderKubeadmConfigsMatchFlatcar(t *testing.T) {
	env := sampleEnv()
	fcosInit, err := RenderKubeadmInitConfig(env)
	require.NoError(t, err)
	flatcarInit, err := flatcar.RenderKubeadmInitConfig(env.NodeEnv)
	require.NoError(t, err)
	assert.Equal(t, flatcarInit, fcosInit, "kubeadm init config must not change with the OS")

	env.NodeName = "k8s-1"
	env.NodeIP = "192.168.122.11"
	env.CertificateKey = strings.Repeat("b", 64)
	env.BootstrapToken = "abcdef.0123456789abcdef"
	env.CACertHash = "sha256:" + strings.Repeat("a", 64)
	fcosJoin, err := RenderKubeadmJoinConfig(env)
	require.NoError(t, err)
	flatcarJoin, err := flatcar.RenderKubeadmJoinConfig(env.NodeEnv)
	require.NoError(t, err)
	assert.Equal(t, flatcarJoin, fcosJoin)
}

// Flatcar gets IP forwarding and rp_filter=0 from its own baselayout sysctl;
// FCOS defaults to ip_forward=0 (kubeadm join preflight fails) and loose
// rp_filter. The FCOS node must carry the same settings explicitly.
func TestRenderIgnitionCarriesFlatcarBaselayoutSysctls(t *testing.T) {
	ign, err := RenderIgnition(sampleEnv())
	require.NoError(t, err)
	baselayout := ignitionFileContent(t, ign, "/etc/sysctl.d/60-homeops-baselayout.conf")
	for _, line := range []string{
		"net.ipv4.ip_forward = 1",
		"net.ipv4.conf.default.rp_filter = 0",
		"net.ipv4.conf.all.rp_filter = 0",
	} {
		assert.Contains(t, baselayout, line+"\n")
	}
}
