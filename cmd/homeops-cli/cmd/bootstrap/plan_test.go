package bootstrap

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
)

func installBootstrapPlanConfig(t *testing.T) *config.Config {
	t.Helper()
	numaZero := 0
	numaOne := 1
	cfg := &config.Config{
		Source: "fake-homeops.yaml",
		Cluster: config.ClusterConfig{
			Name: "plan-test",
			Nodes: []config.Node{
				{Name: "k8s-0", IP: "10.0.0.10", VM: config.VMProfile{Providers: config.ProviderVMProfiles{
					Flatcar: config.ProviderVMProfile{VMID: 300, Mac: "02:00:00:00:00:10", BootStorage: "flatcar-fast", NUMANode: &numaZero},
					Talos:   config.ProviderVMProfile{VMID: 400, Mac: "02:00:00:00:01:10", BootStorage: "talos-fast", NUMANode: &numaOne},
				}}},
				{Name: "k8s-1", IP: "10.0.0.11"},
				{Name: "k8s-2", IP: "10.0.0.12"},
			},
		},
		Hypervisors: config.HypervisorsConfig{
			Default: "proxmox",
			Proxmox: config.ProxmoxConfig{VM: config.VMDefaults{
				MemoryMB: 32768, Cores: 8, BootDiskGB: 64, OpenEBSDiskGB: 512,
				BootStorage: "default-boot", OpenEBSStorage: "default-data", NetworkBridge: "vmbr9", NetworkMTU: 9000,
			}},
		},
		Secrets: map[string]string{
			config.KeyClusterDomain:        "env://PLAN_DOMAIN",
			config.KeyNodeSSHUser:          "op://Infrastructure/SSH/username",
			config.KeyNodeSSHAuthorizedKey: "literal://ssh-ed25519 TOP-SECRET-MATERIAL",
			config.KeyOpConnectToken:       "op://Infrastructure/Connect/token",
		},
	}
	restoreConfig := config.SetForTesting(cfg)
	t.Cleanup(restoreConfig)
	oldVersions := bootstrapPlanVersionsFn
	bootstrapPlanVersionsFn = func(string) *config.VersionConfig {
		return &config.VersionConfig{
			KubernetesVersion: "v1.36.2", FlatcarVersion: "4400.2.1", TalosVersion: "v1.12.3",
			KubeVipVersion: "v0.9.1", PauseImage: "registry.k8s.io/pause:3.10",
		}
	}
	t.Cleanup(func() { bootstrapPlanVersionsFn = oldVersions })
	return cfg
}

func TestBuildBootstrapPlanUsesProviderSpecificConfig(t *testing.T) {
	installBootstrapPlanConfig(t)
	flatcarPlan, err := buildBootstrapPlan(BootstrapConfig{Provider: "flatcar", RootDir: "/repo"})
	require.NoError(t, err)
	require.Len(t, flatcarPlan.Nodes, 3)
	assert.Equal(t, "k8s-0", flatcarPlan.Nodes[0].Name)
	assert.Equal(t, "control-plane init node", flatcarPlan.Nodes[0].Role)
	require.Len(t, flatcarPlan.VMs, 3)
	assert.Equal(t, 300, flatcarPlan.VMs[0].VMID)
	assert.Equal(t, "flatcar-fast", flatcarPlan.VMs[0].BootStorage)
	assert.Equal(t, 32768, flatcarPlan.VMs[0].MemoryMB)
	assert.Contains(t, flatcarPlan.VMs[0].Network, "vmbr9")
	assert.Contains(t, flatcarPlan.VMs[0].Network, "mtu=9000")
	assert.Equal(t, "v1.36.2", flatcarPlan.Versions[0].Version)
	assert.Contains(t, flatcarPlan.Versions[0].Source, "System Upgrade Controller")
	assert.Equal(t, "kubeadm init", flatcarPlan.JoinSequence[0].Action)
	assert.Equal(t, "kubeadm join", flatcarPlan.JoinSequence[1].Action)

	talosPlan, err := buildBootstrapPlan(BootstrapConfig{Provider: "talos", RootDir: "/repo"})
	require.NoError(t, err)
	assert.Equal(t, 400, talosPlan.VMs[0].VMID)
	assert.Equal(t, "talos-fast", talosPlan.VMs[0].BootStorage)
	assert.Equal(t, "talos", talosPlan.Versions[1].Component)
	assert.Equal(t, "v1.12.3", talosPlan.Versions[1].Version)
	assert.Equal(t, "apply Talos machine config", talosPlan.JoinSequence[0].Action)
	assert.Equal(t, "talosctl bootstrap", talosPlan.JoinSequence[len(talosPlan.JoinSequence)-1].Action)
	assert.Contains(t, talosPlan.Artifacts[1].Name, "10.0.0.10")
}

func TestBootstrapPlanSecretsAreRedactedAndUncheckedByDefault(t *testing.T) {
	installBootstrapPlanConfig(t)
	oldCheck := bootstrapPlanSecretCheckFn
	bootstrapPlanSecretCheckFn = func(string) bool {
		t.Fatal("secret resolver called without --check-secrets")
		return false
	}
	t.Cleanup(func() { bootstrapPlanSecretCheckFn = oldCheck })

	plan, err := buildBootstrapPlan(BootstrapConfig{Provider: "flatcar"})
	require.NoError(t, err)
	table, err := renderBootstrapPlan(plan, "table")
	require.NoError(t, err)
	jsonOutput, err := renderBootstrapPlan(plan, "json")
	require.NoError(t, err)
	for _, output := range []string{table, jsonOutput} {
		assert.NotContains(t, output, "op://")
		assert.NotContains(t, output, "literal://")
		assert.NotContains(t, output, "TOP-SECRET-MATERIAL")
		assert.NotContains(t, output, "Infrastructure/SSH")
		assert.Contains(t, output, config.KeyNodeSSHUser)
		assert.Contains(t, output, "NOT CHECKED")
	}

	var checkedRefs []string
	bootstrapPlanSecretCheckFn = func(reference string) bool {
		checkedRefs = append(checkedRefs, reference)
		return !strings.Contains(reference, "PLAN_DOMAIN")
	}
	checked, err := buildBootstrapPlan(BootstrapConfig{Provider: "flatcar", CheckSecrets: true})
	require.NoError(t, err)
	assert.NotEmpty(t, checkedRefs)
	statuses := map[string]string{}
	for _, secret := range checked.Secrets {
		statuses[secret.Key] = secret.Status
	}
	assert.Equal(t, "MISSING", statuses[config.KeyClusterDomain])
	assert.Equal(t, "AVAILABLE", statuses[config.KeyNodeSSHUser])
}

func TestBootstrapPlanFlagCompositionIsNonMutating(t *testing.T) {
	installBootstrapPlanConfig(t)
	oldRun := runBootstrapFn
	oldConfirm := bootstrapConfirm
	runBootstrapFn = func(*BootstrapConfig) error {
		t.Fatal("operational bootstrap called in --plan mode")
		return nil
	}
	bootstrapConfirm = func(string, bool) (bool, error) {
		t.Fatal("confirmation called in --plan mode")
		return false, nil
	}
	t.Cleanup(func() {
		runBootstrapFn = oldRun
		bootstrapConfirm = oldConfirm
	})

	cmd := NewCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--plan", "--dry-run", "--skip-preflight", "--skip-resources", "--skip-kubeadm", "--output", "json"})
	require.NoError(t, cmd.Execute())
	var plan bootstrapPlan
	require.NoError(t, json.Unmarshal(output.Bytes(), &plan))
	assert.True(t, plan.NoChanges)
	assert.Equal(t, "SKIP (--skip-preflight)", plan.Preflight[0].Status)
	assert.Equal(t, "SKIP (--skip-kubeadm)", plan.JoinSequence[0].Status)
	for _, artifact := range plan.Artifacts {
		assert.NotEqual(t, "bootstrap/resources.yaml", artifact.Name)
	}

	cmd = NewCommand()
	cmd.SetArgs([]string{"--check-secrets"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires --plan")
}

func TestBootstrapPlanRejectsUnknownProviderAndOutput(t *testing.T) {
	installBootstrapPlanConfig(t)
	_, err := buildBootstrapPlan(BootstrapConfig{Provider: "other"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported bootstrap provider")
	_, err = renderBootstrapPlan(bootstrapPlan{}, "yaml")
	require.Error(t, err)
}
