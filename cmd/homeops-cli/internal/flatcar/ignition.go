// Package flatcar provides Flatcar Container Linux + kubeadm provisioning helpers
// for homeops-cli: Butane->Ignition transpilation, image URL resolution, and
// SSH-based kubeadm orchestration. It is additive to the existing Talos support and
// does not modify any Talos code path.
package flatcar

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/templates"

	butane "github.com/coreos/butane/config"
	butanecommon "github.com/coreos/butane/config/common"
)

// unresolvedPlaceholderRe matches a real (still-unsubstituted) {{ ENV.NAME }}
// placeholder. It intentionally requires an uppercase identifier so descriptive
// comments like "{{ ENV.* }}" in the templates are not flagged as unresolved.
var unresolvedPlaceholderRe = regexp.MustCompile(`{{ ENV\.[A-Z0-9_]+ }}`)

// Swappable function vars for testability. Tests can stub the transpile step and
// the template renderers without pulling in the real butane library behavior.
var (
	renderFlatcarTemplateFn = templates.RenderFlatcarTemplate
	listFlatcarFilesFn      = templates.ListFlatcarFiles
	translateButaneFn       = func(input []byte, dir string) ([]byte, error) {
		out, report, err := butane.TranslateBytes(input, butanecommon.TranslateBytesOptions{
			TranslateOptions: butanecommon.TranslateOptions{
				FilesDir: dir,
			},
		})
		if err != nil {
			return nil, err
		}
		if report.IsFatal() {
			return nil, fmt.Errorf("butane transpile reported fatal errors: %s", report.String())
		}
		return out, nil
	}
)

// NodeEnv holds the per-node values needed to render the Flatcar/kubeadm templates.
// Fields map 1:1 onto the {{ ENV.* }} placeholders in the embedded templates.
type NodeEnv struct {
	NodeName          string // NODE_NAME (e.g. k8s-0)
	NodeIP            string // NODE_IP
	Node0IP           string // NODE0_IP
	Node1IP           string // NODE1_IP
	Node2IP           string // NODE2_IP
	KubernetesVersion string // KUBERNETES_VERSION (e.g. v1.36.1)
	KubernetesMinor   string // KUBERNETES_MINOR (e.g. v1.36)
	ControlPlaneVIP   string // CONTROL_PLANE_VIP
	PauseImage        string // PAUSE_IMAGE
	KubeVipVersion    string // KUBE_VIP_VERSION
	NodeInterface     string // NODE_INTERFACE (e.g. eth0)
	NodeMAC           string // NODE_MAC (primary NIC MAC; pins the eth0 name via 10-eth0.link)
	K8sEndpoint       string // K8S_ENDPOINT (apiserver cert SAN DNS, e.g. k8s.<domain>; sourced from op)
	SSHAuthorizedKey  string // SSH_AUTHORIZED_KEY (node access public key; sourced from op)
	ClusterName       string // CLUSTER_NAME
	PodCIDR           string // POD_CIDR
	ServiceCIDR       string // SERVICE_CIDR
	DNSDomain         string // DNS_DOMAIN / kubelet clusterDomain
	ClusterDNS        string // CLUSTER_DNS derived from SERVICE_CIDR
	ExtraCertSANs     string // EXTRA_CERT_SANS pre-rendered kubeadm YAML list
	KubeletMaxPods    string // KUBELET_MAX_PODS
	ImageGCHigh       string // IMAGE_GC_HIGH_PERCENT
	ImageGCLow        string // IMAGE_GC_LOW_PERCENT
	NTPServers        string // NTP_SERVERS space-separated
	NetworkMTU        string // NETWORK_MTU

	// Runtime join material (only set for join configs, after `kubeadm init`).
	CertificateKey string // CERTIFICATE_KEY
	BootstrapToken string // BOOTSTRAP_TOKEN
	CACertHash     string // CA_CERT_HASH
}

// envMap converts a NodeEnv into the map[string]string used by the renderers,
// omitting empty values so unrelated placeholders are left intact.
func (e NodeEnv) envMap() map[string]string {
	e = e.withClusterDefaults()
	m := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	add(constants.EnvNodeName, e.NodeName)
	add(constants.EnvNodeIP, e.NodeIP)
	add(constants.EnvNode0IP, e.Node0IP)
	add(constants.EnvNode1IP, e.Node1IP)
	add(constants.EnvNode2IP, e.Node2IP)
	add(constants.EnvKubernetesVersion, e.KubernetesVersion)
	add(constants.EnvKubernetesMinor, e.KubernetesMinor)
	add(constants.EnvControlPlaneVIP, e.ControlPlaneVIP)
	add(constants.EnvPauseImage, e.PauseImage)
	add(constants.EnvKubeVipVersion, e.KubeVipVersion)
	add(constants.EnvNodeInterface, e.NodeInterface)
	add(constants.EnvNodeMAC, e.NodeMAC)
	add(constants.EnvK8sEndpoint, e.K8sEndpoint)
	add(constants.EnvSSHAuthorizedKey, e.SSHAuthorizedKey)
	add(constants.EnvClusterName, e.ClusterName)
	add(constants.EnvPodCIDR, e.PodCIDR)
	add(constants.EnvServiceCIDR, e.ServiceCIDR)
	add(constants.EnvDNSDomain, e.DNSDomain)
	add(constants.EnvClusterDNS, e.ClusterDNS)
	add(constants.EnvExtraCertSANs, e.ExtraCertSANs)
	add(constants.EnvKubeletMaxPods, e.KubeletMaxPods)
	add(constants.EnvImageGCHigh, e.ImageGCHigh)
	add(constants.EnvImageGCLow, e.ImageGCLow)
	add(constants.EnvNTPServers, e.NTPServers)
	add(constants.EnvNetworkMTU, e.NetworkMTU)
	add(constants.EnvCertificateKey, e.CertificateKey)
	add(constants.EnvBootstrapToken, e.BootstrapToken)
	add(constants.EnvCACertHash, e.CACertHash)
	return m
}

func (e NodeEnv) withClusterDefaults() NodeEnv {
	cfg := config.Get()
	if e.ClusterName == "" {
		e.ClusterName = cfg.ClusterNameWithDefault()
	}
	if e.PodCIDR == "" {
		e.PodCIDR = cfg.Cluster.PodCIDR
	}
	if e.ServiceCIDR == "" {
		e.ServiceCIDR = cfg.Cluster.ServiceCIDR
	}
	if e.DNSDomain == "" {
		e.DNSDomain = cfg.Cluster.DNSDomain
	}
	if e.ClusterDNS == "" {
		e.ClusterDNS = cfg.ClusterDNS()
	}
	if e.ExtraCertSANs == "" {
		e.ExtraCertSANs = formatFlatcarCertSANs(cfg.Cluster.ExtraCertSANs)
	}
	if e.KubeletMaxPods == "" {
		e.KubeletMaxPods = strconv.Itoa(cfg.Cluster.Kubelet.MaxPods)
	}
	if e.ImageGCHigh == "" {
		e.ImageGCHigh = strconv.Itoa(cfg.Cluster.Kubelet.ImageGCHighPercent)
	}
	if e.ImageGCLow == "" {
		e.ImageGCLow = strconv.Itoa(cfg.Cluster.Kubelet.ImageGCLowPercent)
	}
	if e.NTPServers == "" {
		e.NTPServers = strings.Join(cfg.Cluster.NTPServers, " ")
	}
	if e.NetworkMTU == "" {
		e.NetworkMTU = strconv.Itoa(cfg.Hypervisors.Proxmox.VM.NetworkMTU)
	}
	return e
}

func formatFlatcarCertSANs(values []string) string {
	if len(values) == 1 && values[0] == "192.168.255.10" {
		return "    - 192.168.255.10                  # Cilium BGP external API LB (second path)"
	}
	lines := make([]string, 0, len(values))
	for _, v := range values {
		lines = append(lines, "    - "+v)
	}
	return strings.Join(lines, "\n")
}

// RenderIgnition renders the Flatcar control-plane Butane config for a node and
// transpiles it to Ignition JSON. The embedded local:-referenced files (under
// flatcar/files and flatcar/manifests) are first materialized into a temp FilesDir
// with the SAME {{ ENV.* }} substitution applied, so files like
// containerd-config.toml (PAUSE_IMAGE) and kube-vip.yaml (CONTROL_PLANE_VIP,
// KUBE_VIP_VERSION, NODE_INTERFACE) come out fully rendered.
//
// Returns the Ignition JSON bytes. The temp FilesDir is removed before returning.
func RenderIgnition(env NodeEnv) ([]byte, error) {
	envVars := env.envMap()

	// 1. Render the Butane document itself.
	butaneDoc, err := renderFlatcarTemplateFn("butane/controlplane.bu", envVars)
	if err != nil {
		return nil, fmt.Errorf("failed to render Butane controlplane: %w", err)
	}
	// Fail loudly on an unresolved placeholder rather than baking a literal
	// "{{ ENV.X }}" into Ignition. envMap() omits empty values, so a silent
	// 1Password miss for SSH_AUTHORIZED_KEY/K8S_ENDPOINT would otherwise produce
	// a node with a garbage SSH key (unreachable -> bootstrap silently fails).
	if m := unresolvedPlaceholderRe.FindString(butaneDoc); m != "" {
		return nil, fmt.Errorf("butane controlplane has unresolved placeholder %s: missing required ENV value (1Password miss for SSH key / domain?)", m)
	}

	// 2. Materialize local: files into a temp FilesDir, layout mirroring the
	//    local: paths in the Butane doc (files/..., manifests/...).
	dir, err := os.MkdirTemp("", "homeops-flatcar-files-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp files dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	for _, subdir := range []string{"files", "manifests"} {
		if err := materializeFlatcarSubdir(dir, subdir, envVars); err != nil {
			return nil, err
		}
	}

	// 3. Transpile Butane -> Ignition with the FilesDir for local: resolution.
	ign, err := translateButaneFn([]byte(butaneDoc), dir)
	if err != nil {
		return nil, fmt.Errorf("failed to transpile Butane to Ignition: %w", err)
	}

	return ign, nil
}

// materializeFlatcarSubdir writes every embedded file under flatcar/<subdir> into
// <baseDir>/<subdir>/<name>, applying ENV substitution to each.
func materializeFlatcarSubdir(baseDir, subdir string, envVars map[string]string) error {
	names, err := listFlatcarFilesFn(subdir)
	if err != nil {
		return fmt.Errorf("failed to list flatcar %s: %w", subdir, err)
	}

	targetDir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return fmt.Errorf("failed to create %s dir: %w", subdir, err)
	}

	for _, name := range names {
		rendered, err := renderFlatcarTemplateFn(name, envVars)
		if err != nil {
			return fmt.Errorf("failed to render flatcar file %s: %w", name, err)
		}
		if m := unresolvedPlaceholderRe.FindString(rendered); m != "" {
			return fmt.Errorf("flatcar file %s has unresolved placeholder %s: missing required ENV value", name, m)
		}
		// name is "<subdir>/<file>"; preserve only the basename under targetDir.
		dest := filepath.Join(targetDir, filepath.Base(name))
		if err := os.WriteFile(dest, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("failed to write flatcar file %s: %w", dest, err)
		}
	}
	return nil
}

// RenderKubeadmInitConfig renders the kubeadm init configuration (node0).
func RenderKubeadmInitConfig(env NodeEnv) (string, error) {
	out, err := renderFlatcarTemplateFn("kubeadm/init-config.yaml", env.envMap())
	if err != nil {
		return "", fmt.Errorf("failed to render kubeadm init config: %w", err)
	}
	if m := unresolvedPlaceholderRe.FindString(out); m != "" {
		return "", fmt.Errorf("kubeadm init config has unresolved placeholder %s: missing required ENV value", m)
	}
	return out, nil
}

// RenderKubeadmJoinConfig renders the kubeadm join configuration (node1/node2).
// The env must carry CertificateKey, BootstrapToken and CACertHash (from
// `kubeadm init`), in addition to the node identity fields.
func RenderKubeadmJoinConfig(env NodeEnv) (string, error) {
	out, err := renderFlatcarTemplateFn("kubeadm/join-config.yaml", env.envMap())
	if err != nil {
		return "", fmt.Errorf("failed to render kubeadm join config: %w", err)
	}
	if m := unresolvedPlaceholderRe.FindString(out); m != "" {
		return "", fmt.Errorf("kubeadm join config has unresolved placeholder %s: missing required ENV value (cert key / token / CA hash?)", m)
	}
	return out, nil
}
