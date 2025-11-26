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
)

// Environment variable names
const (
	EnvTrueNASHost   = "TRUENAS_HOST"
	EnvTrueNASAPIKey = "TRUENAS_API_KEY"
	EnvSPICEPassword = "SPICE_PASSWORD"

	EnvVSphereHost     = "VSPHERE_HOST"
	EnvVSphereUsername = "VSPHERE_USERNAME"
	EnvVSpherePassword = "VSPHERE_PASSWORD"

	EnvKubeconfig         = "KUBECONFIG"
	EnvTalosconfig        = "TALOSCONFIG"
	EnvKubernetesVersion  = "KUBERNETES_VERSION"
	EnvTalosVersion       = "TALOS_VERSION"
	EnvSOPSAgeKeyFile     = "SOPS_AGE_KEY_FILE"
	EnvMiniJinjaConfig    = "MINIJINJA_CONFIG_FILE"
	EnvDebug              = "DEBUG"
	EnvLogLevel           = "LOG_LEVEL"
	EnvHomeOpsNoInteract  = "HOMEOPS_NO_INTERACTIVE"
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
