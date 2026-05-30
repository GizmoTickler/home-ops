// Package constants provides centralized constant definitions for the homeops-cli.
// This reduces magic strings throughout the codebase and makes configuration easier to manage.
package constants

// 1Password reference paths
const (
	// TrueNAS credentials
	OpTrueNASHost      = "op://Infrastructure/talosdeploy/TRUENAS_HOST"
	OpTrueNASAPI       = "op://Infrastructure/talosdeploy/TRUENAS_API"
	OpTrueNASSPICEPass = "op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS"

	// vSphere/ESXi credentials
	OpESXiHost     = "op://Infrastructure/esxi/add more/host"
	OpESXiUsername = "op://Infrastructure/esxi/username"
	OpESXiPassword = "op://Infrastructure/esxi/password"

	// ESXi SSH credentials
	OpESXiSSHPrivateKey = "op://Infrastructure/esxi-ssh/private key"

	// TrueNAS SSH/username credentials
	OpTrueNASUsername      = "op://Infrastructure/talosdeploy/TRUENAS_USERNAME"
	OpTrueNASSSHPrivateKey = "op://Infrastructure/NAS01/private key"

	// Proxmox VE credentials
	OpProxmoxHost        = "op://Infrastructure/PVE-API/HOST"
	OpProxmoxTokenID     = "op://Infrastructure/PVE-API/TOKENID"
	OpProxmoxTokenSecret = "op://Infrastructure/PVE-API/SECRET"
	OpProxmoxNode        = "op://Infrastructure/PVE-API/node"

	// Flatcar/kubeadm SSH credentials (node access for kubeadm orchestration).
	// The SSH user/key the homeops-cli uses to reach the k8s-* Flatcar nodes.
	OpFlatcarSSHUser       = "op://Infrastructure/flatcar/SSH_USER"
	OpFlatcarSSHPrivateKey = "op://Infrastructure/flatcar/private key"

	// Proxmox node SSH key (root@pve) — used to upload Flatcar Ignition to the
	// Proxmox snippets dir for the fw_cfg attach. Auth is via the ambient ssh-agent;
	// this op ref is the macOS 1Password SSH-agent identity.
	OpProxmoxSSHKey = "op://Infrastructure/proxmox-ssh/private key"

	// Flatcar node SSH public key (non-secret) injected into the Ignition
	// ssh_authorized_keys at render time. Matches OpFlatcarSSHPrivateKey.
	OpFlatcarPublicKey = "op://Infrastructure/flatcar/public key"

	// Cluster base domain — kept out of this (public) repo. The Flatcar kubeadm
	// apiserver certSAN is derived as "k8s." + this value at render time.
	OpSecretDomain = "op://Infrastructure/cluster-config/SECRET_DOMAIN"

	// Persisted kubeadm cluster PKI (base64-encoded), restored onto node0 before
	// `kubeadm init` so the cluster keeps a stable identity across nuke/pave.
	OpPKICACrt           = "op://Infrastructure/kubernetes-pki/ca_crt"
	OpPKICAKey           = "op://Infrastructure/kubernetes-pki/ca_key"
	OpPKISAKey           = "op://Infrastructure/kubernetes-pki/sa_key"
	OpPKISAPub           = "op://Infrastructure/kubernetes-pki/sa_pub"
	OpPKIFrontProxyCACrt = "op://Infrastructure/kubernetes-pki/front_proxy_ca_crt"
	OpPKIFrontProxyCAKey = "op://Infrastructure/kubernetes-pki/front_proxy_ca_key"
	OpPKIEtcdCACrt       = "op://Infrastructure/kubernetes-pki/etcd_ca_crt"
	OpPKIEtcdCAKey       = "op://Infrastructure/kubernetes-pki/etcd_ca_key"
)

// Environment variable names
const (
	EnvTrueNASHost   = "TRUENAS_HOST"
	EnvTrueNASAPIKey = "TRUENAS_API_KEY"
	EnvSPICEPassword = "SPICE_PASSWORD"

	EnvVSphereHost     = "VSPHERE_HOST"
	EnvVSphereUsername = "VSPHERE_USERNAME"
	EnvVSpherePassword = "VSPHERE_PASSWORD"
	// EnvVSphereInsecure: set to "false" to enable TLS verification of the
	// vSphere endpoint. Defaults to insecure (true) for self-signed homelab certs.
	EnvVSphereInsecure = "VSPHERE_INSECURE"

	EnvProxmoxHost        = "PROXMOX_HOST"
	EnvProxmoxTokenID     = "PROXMOX_TOKEN_ID"
	EnvProxmoxTokenSecret = "PROXMOX_TOKEN_SECRET"
	EnvProxmoxNode        = "PROXMOX_NODE"
	// EnvProxmoxInsecure: set to "false" to enable TLS verification of the
	// Proxmox endpoint. Defaults to insecure (true) for self-signed homelab certs.
	EnvProxmoxInsecure = "PROXMOX_INSECURE"

	EnvKubeconfig        = "KUBECONFIG"
	EnvTalosconfig       = "TALOSCONFIG"
	EnvKubernetesVersion = "KUBERNETES_VERSION"
	EnvTalosVersion      = "TALOS_VERSION"
	EnvMiniJinjaConfig   = "MINIJINJA_CONFIG_FILE"
	EnvDebug             = "DEBUG"
	EnvLogLevel          = "LOG_LEVEL"
	EnvHomeOpsNoInteract = "HOMEOPS_NO_INTERACTIVE"

	// Flatcar / kubeadm template substitution variable names. These are the keys
	// expected by the embedded flatcar templates ({{ ENV.<NAME> }}).
	EnvKubernetesMinor  = "KUBERNETES_MINOR"
	EnvFlatcarVersion   = "FLATCAR_VERSION"
	EnvControlPlaneVIP  = "CONTROL_PLANE_VIP"
	EnvPauseImage       = "PAUSE_IMAGE"
	EnvKubeVipVersion   = "KUBE_VIP_VERSION"
	EnvNodeInterface    = "NODE_INTERFACE"
	EnvNodeName         = "NODE_NAME"
	EnvNodeIP           = "NODE_IP"
	EnvNode0IP          = "NODE0_IP"
	EnvNode1IP          = "NODE1_IP"
	EnvNode2IP          = "NODE2_IP"
	EnvCertificateKey   = "CERTIFICATE_KEY"
	EnvBootstrapToken   = "BOOTSTRAP_TOKEN"
	EnvCACertHash       = "CA_CERT_HASH"
	EnvK8sEndpoint      = "K8S_ENDPOINT"
	EnvSSHAuthorizedKey = "SSH_AUTHORIZED_KEY"
)

// Kubernetes namespaces commonly used
const (
	NSFluxSystem     = "flux-system"
	NSVolsyncSystem  = "volsync-system"
	NSKubeSystem     = "kube-system"
	NSCertManager    = "cert-manager"
	NSExternalSecret = "external-secrets"
	NSObservability  = "observability"
	NSNetwork        = "network"
	NSDownloads      = "downloads"
	NSMedia          = "media"
	NSSelfHosted     = "self-hosted"
	NSAutomation     = "automation"
	NSAuth           = "auth"
	NSOpenEBSSystem  = "openebs-system"
	NSRookCeph       = "rook-ceph"
	NSSystemUpgrade  = "system-upgrade"
	NSSystem         = "system"
	NSDatabase       = "database"
)

// Timeouts and intervals
const (
	// Default timeout for kubectl operations
	DefaultKubectlTimeout = "30s"

	// Default timeout for external command execution
	DefaultCommandTimeout = 120000 // milliseconds (2 minutes)

	// Retry intervals
	RetryIntervalShort  = 5  // seconds
	RetryIntervalMedium = 10 // seconds
	RetryIntervalLong   = 30 // seconds

	// Max retry attempts
	MaxRetryAttempts = 3
)

// Bootstrap-specific timing constants
// These use a progress-based approach: keep waiting as long as progress is being made
// Only fail if no progress is detected for the "stall timeout" period
const (
	// Check intervals - how often to poll for status
	BootstrapCheckIntervalFast   = 4  // seconds - for CRDs, quick checks
	BootstrapCheckIntervalNormal = 5  // seconds - for most operations
	BootstrapCheckIntervalSlow   = 10 // seconds - for heavy operations

	// Stall detection - fail only if no progress for this duration
	BootstrapStallTimeout = 120 // seconds (2 minutes) - no progress = failure

	// Maximum wait times (safety net) - these are very generous
	BootstrapCRDMaxWait        = 600  // 10 minutes max for CRDs
	BootstrapExtSecMaxWait     = 300  // 5 minutes max for external-secrets
	BootstrapFluxMaxWait       = 900  // 15 minutes max for Flux reconciliation
	BootstrapNodeMaxWait       = 1200 // 20 minutes max for nodes
	BootstrapKubeconfigMaxWait = 300  // 5 minutes max for kubeconfig

	// Legacy constants for backward compatibility (converted to use new approach)
	BootstrapExtSecInstallAttempts = 12 // 1 minute to check if deployment exists

	// Helm sync retries
	BootstrapHelmMaxAttempts    = 3
	BootstrapHelmRetryBaseDelay = 30 // seconds
)

// Talos Factory API
const (
	TalosFactoryBaseURL = "https://factory.talos.dev"
)

// Flatcar / kubeadm cluster design constants. These mirror the migration design
// (k8s-0/1/2 control-plane, kube-vip ARP VIP, Cilium BGP external API LB).
const (
	// FlatcarReleaseBaseURL is the base for stable-channel Flatcar images.
	FlatcarReleaseBaseURL = "https://stable.release.flatcar-linux.net/amd64-usr"
	// FlatcarSysextBaseURL is where the Kubernetes systemd-sysext bundles live.
	FlatcarSysextBaseURL = "https://extensions.flatcar.org/extensions"

	// Defaults for the configurable knobs (overridable via flags/env).
	DefaultControlPlaneVIP = "192.168.123.253"
	DefaultPauseImage      = "registry.k8s.io/pause:3.10"
	DefaultKubeVipVersion  = "v0.8.9"
	DefaultNodeInterface   = "eth0"
	DefaultFlatcarChannel  = "stable"

	// Node IPs for the 3 control-plane nodes.
	FlatcarNode0IP = "192.168.122.10"
	FlatcarNode1IP = "192.168.122.11"
	FlatcarNode2IP = "192.168.122.12"
)

// TrueNAS storage paths
const (
	TrueNASStandardISOPath = "/mnt/flashstor/ISO/metal-amd64.iso"
)

// Application labels used for Kubernetes resources
const (
	LabelAppName     = "app.kubernetes.io/name"
	LabelAppInstance = "app.kubernetes.io/instance"
	LabelAppVersion  = "app.kubernetes.io/version"
)
