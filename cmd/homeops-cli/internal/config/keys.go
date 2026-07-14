package config

import "sort"

// Semantic secret keys. Code and templates refer to secrets by these keys
// (templates via `secret://<key>`); the homeops config maps each key to a
// backend reference. The defaults below are fully portable — everything
// resolves from environment variables when no config file maps it elsewhere.
const (
	// Hypervisor credentials
	KeyTrueNASHost          = "truenas_host"
	KeyTrueNASAPIKey        = "truenas_api_key" // #nosec G101 -- semantic config key string only, not a secret value
	KeyTrueNASUsername      = "truenas_username"
	KeyTrueNASSpicePassword = "truenas_spice_password" // #nosec G101 -- semantic config key string only, not a secret value
	KeyProxmoxHost          = "proxmox_host"
	KeyProxmoxTokenID       = "proxmox_token_id"     // #nosec G101 -- semantic config key string only, not a secret value
	KeyProxmoxTokenSecret   = "proxmox_token_secret" // #nosec G101 -- semantic config key string only, not a secret value
	KeyProxmoxNode          = "proxmox_node"
	KeyVSphereHost          = "vsphere_host"
	KeyVSphereUsername      = "vsphere_username"
	KeyVSpherePassword      = "vsphere_password"
	KeyVSphereSSHKey        = "vsphere_ssh_private_key"

	// Cluster identity / node access
	KeyClusterDomain        = "cluster_domain"
	KeyNodeSSHUser          = "node_ssh_user"
	KeyNodeSSHAuthorizedKey = "node_ssh_authorized_key"

	// Talos machine secrets (legacy provider; generate with `talosctl gen secrets`)
	KeyTalosMachineCACrt    = "talos_machine_ca_crt"
	KeyTalosMachineCAKey    = "talos_machine_ca_key"
	KeyTalosMachineToken    = "talos_machine_token"
	KeyTalosClusterCACrt    = "talos_cluster_ca_crt"
	KeyTalosClusterCAKey    = "talos_cluster_ca_key"
	KeyTalosClusterID       = "talos_cluster_id"
	KeyTalosClusterSecret   = "talos_cluster_secret" // #nosec G101 -- semantic config key string only, not a secret value
	KeyTalosClusterToken    = "talos_cluster_token"
	KeyTalosK8sEndpoint     = "talos_k8s_endpoint"
	KeyTalosAggregatorCrt   = "talos_aggregator_ca_crt"
	KeyTalosAggregatorKey   = "talos_aggregator_ca_key"
	KeyTalosEtcdCACrt       = "talos_etcd_ca_crt"
	KeyTalosEtcdCAKey       = "talos_etcd_ca_key"
	KeyTalosSecretboxSecret = "talos_secretbox_encryption_secret"
	KeyTalosSAKey           = "talos_service_account_key"

	// Bootstrap workload secrets (used by the embedded bootstrap manifests;
	// override the templates via templates.dir if your cluster differs)
	KeyOpCredentialsJSON  = "op_credentials_json"
	KeyOpConnectToken     = "op_connect_token"
	KeyCloudflareTunnelID = "cloudflare_tunnel_id"
)

// defaultSecretRefs is the canonical key registry with portable defaults.
// A key absent from this map is unknown to the CLI.
var defaultSecretRefs = map[string]string{ // #nosec G101 -- map contains backend reference identifiers like env var names, not secret values
	KeyTrueNASHost:          "env://TRUENAS_HOST",
	KeyTrueNASAPIKey:        "env://TRUENAS_API_KEY",
	KeyTrueNASUsername:      "env://TRUENAS_USERNAME",
	KeyTrueNASSpicePassword: "env://SPICE_PASSWORD",
	KeyProxmoxHost:          "env://PROXMOX_HOST",
	KeyProxmoxTokenID:       "env://PROXMOX_TOKEN_ID",
	KeyProxmoxTokenSecret:   "env://PROXMOX_TOKEN_SECRET",
	KeyProxmoxNode:          "env://PROXMOX_NODE",
	KeyVSphereHost:          "env://VSPHERE_HOST",
	KeyVSphereUsername:      "env://VSPHERE_USERNAME",
	KeyVSpherePassword:      "env://VSPHERE_PASSWORD",
	KeyVSphereSSHKey:        "env://VSPHERE_SSH_PRIVATE_KEY",

	KeyClusterDomain:        "env://SECRET_DOMAIN",
	KeyNodeSSHUser:          "literal://core",
	KeyNodeSSHAuthorizedKey: "env://SSH_AUTHORIZED_KEY",

	KeyTalosMachineCACrt:    "env://TALOS_MACHINE_CA_CRT",
	KeyTalosMachineCAKey:    "env://TALOS_MACHINE_CA_KEY",
	KeyTalosMachineToken:    "env://TALOS_MACHINE_TOKEN",
	KeyTalosClusterCACrt:    "env://TALOS_CLUSTER_CA_CRT",
	KeyTalosClusterCAKey:    "env://TALOS_CLUSTER_CA_KEY",
	KeyTalosClusterID:       "env://TALOS_CLUSTER_ID",
	KeyTalosClusterSecret:   "env://TALOS_CLUSTER_SECRET",
	KeyTalosClusterToken:    "env://TALOS_CLUSTER_TOKEN",
	KeyTalosK8sEndpoint:     "env://TALOS_K8S_ENDPOINT",
	KeyTalosAggregatorCrt:   "env://TALOS_AGGREGATOR_CA_CRT",
	KeyTalosAggregatorKey:   "env://TALOS_AGGREGATOR_CA_KEY",
	KeyTalosEtcdCACrt:       "env://TALOS_ETCD_CA_CRT",
	KeyTalosEtcdCAKey:       "env://TALOS_ETCD_CA_KEY",
	KeyTalosSecretboxSecret: "env://TALOS_SECRETBOX_ENCRYPTION_SECRET",
	KeyTalosSAKey:           "env://TALOS_SERVICE_ACCOUNT_KEY",

	KeyOpCredentialsJSON:  "env://OP_CREDENTIALS_JSON",
	KeyOpConnectToken:     "env://OP_CONNECT_TOKEN",
	KeyCloudflareTunnelID: "env://CLOUDFLARE_TUNNEL_ID",
}

// KnownSecretKeys returns the canonical secret keys (sorted) — used by
// `config init` scaffolding and `config doctor`.
func KnownSecretKeys() []string {
	keys := make([]string, 0, len(defaultSecretRefs))
	for k := range defaultSecretRefs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DefaultSecretRef returns the portable default reference for a key.
func DefaultSecretRef(key string) string { return defaultSecretRefs[key] }
