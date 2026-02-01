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

	// Proxmox VE credentials
	OpProxmoxHost        = "op://Infrastructure/PVE-API/HOST"
	OpProxmoxTokenID     = "op://Infrastructure/PVE-API/TOKENID"
	OpProxmoxTokenSecret = "op://Infrastructure/PVE-API/SECRET"
	OpProxmoxNode        = "op://Infrastructure/PVE-API/node"
)

// Environment variable names
const (
	EnvTrueNASHost   = "TRUENAS_HOST"
	EnvTrueNASAPIKey = "TRUENAS_API_KEY"
	EnvSPICEPassword = "SPICE_PASSWORD"

	EnvVSphereHost     = "VSPHERE_HOST"
	EnvVSphereUsername = "VSPHERE_USERNAME"
	EnvVSpherePassword = "VSPHERE_PASSWORD"

	EnvProxmoxHost        = "PROXMOX_HOST"
	EnvProxmoxTokenID     = "PROXMOX_TOKEN_ID"
	EnvProxmoxTokenSecret = "PROXMOX_TOKEN_SECRET"
	EnvProxmoxNode        = "PROXMOX_NODE"

	EnvKubeconfig        = "KUBECONFIG"
	EnvTalosconfig       = "TALOSCONFIG"
	EnvKubernetesVersion = "KUBERNETES_VERSION"
	EnvTalosVersion      = "TALOS_VERSION"
	EnvSOPSAgeKeyFile    = "SOPS_AGE_KEY_FILE"
	EnvMiniJinjaConfig   = "MINIJINJA_CONFIG_FILE"
	EnvDebug             = "DEBUG"
	EnvLogLevel          = "LOG_LEVEL"
	EnvHomeOpsNoInteract = "HOMEOPS_NO_INTERACTIVE"
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
	NSTrueNASCSI     = "truenas-csi"
	NSOpenEBSSystem  = "openebs-system"
	NSSystem         = "system"
	NSDatabase       = "database"
	NSStorage        = "storage"
	NSMonitoring     = "monitoring"
	NSLogs           = "logs"
	NSHome           = "home"
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

// Application labels used for Kubernetes resources
const (
	LabelAppName     = "app.kubernetes.io/name"
	LabelAppInstance = "app.kubernetes.io/instance"
	LabelAppVersion  = "app.kubernetes.io/version"
)
