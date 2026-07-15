// Package constants provides centralized constant definitions for the homeops-cli.
// This reduces magic strings throughout the codebase and makes configuration easier to manage.
//
// NOTE: secret locations and cluster topology are NOT constants — they live in
// the homeops config file (homeops.yaml; see internal/config). Only values that
// are genuinely the same for every user of the tool belong here.
package constants

// Environment variable names
const (
	EnvTrueNASHost   = "TRUENAS_HOST"
	EnvTrueNASAPIKey = "TRUENAS_API_KEY" // #nosec G101 -- environment variable name only, not a secret value
	EnvSPICEPassword = "SPICE_PASSWORD"

	EnvVSphereHost     = "VSPHERE_HOST"
	EnvVSphereUsername = "VSPHERE_USERNAME"
	EnvVSpherePassword = "VSPHERE_PASSWORD"
	// EnvVSphereInsecure: set to "true" to DISABLE TLS verification of the vSphere
	// endpoint (for self-signed certs). Defaults to verifying (secure).
	EnvVSphereInsecure = "VSPHERE_INSECURE"

	EnvProxmoxHost        = "PROXMOX_HOST"
	EnvProxmoxTokenID     = "PROXMOX_TOKEN_ID"     // #nosec G101 -- environment variable name only, not a secret value
	EnvProxmoxTokenSecret = "PROXMOX_TOKEN_SECRET" // #nosec G101 -- environment variable name only, not a secret value
	EnvProxmoxNode        = "PROXMOX_NODE"
	// EnvProxmoxInsecure: set to "true" to DISABLE TLS verification of the Proxmox
	// endpoint (for self-signed certs). Defaults to verifying (secure).
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
	EnvNodeMAC          = "NODE_MAC"
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
	EnvClusterName      = "CLUSTER_NAME"
	EnvPodCIDR          = "POD_CIDR"
	EnvServiceCIDR      = "SERVICE_CIDR"
	EnvDNSDomain        = "DNS_DOMAIN"
	EnvClusterDNS       = "CLUSTER_DNS"
	EnvExtraCertSANs    = "EXTRA_CERT_SANS"
	EnvKubeletMaxPods   = "KUBELET_MAX_PODS"
	EnvImageGCHigh      = "IMAGE_GC_HIGH_PERCENT"
	EnvImageGCLow       = "IMAGE_GC_LOW_PERCENT"
	EnvNTPServers       = "NTP_SERVERS"
	EnvNetworkMTU       = "NETWORK_MTU"
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
