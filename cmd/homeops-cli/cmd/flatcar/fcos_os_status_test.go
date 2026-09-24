package flatcar

import (
	"context"
	"embed"
	"strings"
	"testing"
	"time"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/rpm_ostree_status_*.json
var rpmOstreeSamples embed.FS

func readRPMOstreeSample(t *testing.T, name string) string {
	t.Helper()
	raw, err := rpmOstreeSamples.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return string(raw)
}

func fcosProbeOutput(rpmOstreeJSON, rc, greenboot string) string {
	return "__HOMEOPS_OS_RELEASE__\n" +
		"NAME=\"Fedora Linux\"\nVERSION=\"44.20260829.3.1 (CoreOS)\"\nID=fedora\nVERSION_ID=44\nVARIANT_ID=coreos\nOSTREE_VERSION='44.20260829.3.1'\n" +
		"__HOMEOPS_RPM_OSTREE__\n" + rpmOstreeJSON + "\n" +
		"__HOMEOPS_RPM_OSTREE_RC__=" + rc + "\n" +
		"__HOMEOPS_REBOOT_REQUIRED__=false\n" +
		"__HOMEOPS_GREENBOOT__=" + greenboot + "\n" +
		"__HOMEOPS_KERNEL__=7.1.3-200.fc44.x86_64\n" +
		"__HOMEOPS_UP_SINCE__=2026-09-20 08:30:00\n" +
		"__HOMEOPS_UPTIME_SECONDS__=1\n" +
		"__HOMEOPS_END__\n"
}

func TestParseFCOSOSStatusIdle(t *testing.T) {
	raw := fcosProbeOutput(readRPMOstreeSample(t, "rpm_ostree_status_idle.json"), "0", "active")
	node := parseFCOSOSStatus(versionconfig.Node{Name: "k8s-0", IP: "192.0.2.10"}, raw, time.Now())
	assert.Equal(t, "44.20260829.3.1", node.Version)
	assert.Equal(t, "idle", node.UpdateStatus)
	assert.Empty(t, node.NewVersion)
	assert.False(t, node.RebootNeeded)
	assert.Equal(t, "44.20260815.3.0", node.RollbackVersion)
	assert.Equal(t, []string{"greenboot", "greenboot-default-health-checks"}, node.LayeredPackages)
	assert.Equal(t, "active", node.Greenboot)
	assert.Equal(t, "7.1.3-200.fc44.x86_64", node.Kernel)
	assert.Equal(t, "2026-09-20 08:30:00", node.UpSince)
	assert.Empty(t, node.Error)
}

func TestParseFCOSOSStatusStagedDeployment(t *testing.T) {
	// A merge-gated `rpm-ostree deploy <build>` stages the next deployment
	// ahead of the booted one; Kured's reboot activates it.
	raw := fcosProbeOutput(readRPMOstreeSample(t, "rpm_ostree_status_staged.json"), "0", "active")
	node := parseFCOSOSStatus(versionconfig.Node{Name: "k8s-1"}, raw, time.Now())
	assert.Equal(t, "44.20260829.3.1", node.Version, "the BOOTED deployment is the running version")
	assert.Equal(t, "staged", node.UpdateStatus)
	assert.Equal(t, "44.20260915.3.0", node.NewVersion)
	assert.True(t, node.RebootNeeded)
	assert.Empty(t, node.RollbackVersion, "the staged sample carries no older deployment")
}

func TestParseFCOSOSStatusPendingDeploymentAndRebootRequiredMarker(t *testing.T) {
	raw := fcosProbeOutput(`{"deployments":[
		{"version":"44.20260915.3.0","booted":false,"staged":false},
		{"version":"44.20260829.3.1","booted":true,"staged":false}]}`, "0", "inactive")
	node := parseFCOSOSStatus(versionconfig.Node{Name: "k8s-2"}, raw, time.Now())
	assert.Equal(t, "pending", node.UpdateStatus)
	assert.Equal(t, "44.20260915.3.0", node.NewVersion)
	assert.True(t, node.RebootNeeded)

	idle := strings.Replace(fcosProbeOutput(readRPMOstreeSample(t, "rpm_ostree_status_idle.json"), "0", "active"),
		"__HOMEOPS_REBOOT_REQUIRED__=false", "__HOMEOPS_REBOOT_REQUIRED__=true", 1)
	node = parseFCOSOSStatus(versionconfig.Node{Name: "k8s-2"}, idle, time.Now())
	assert.Equal(t, "idle", node.UpdateStatus)
	assert.True(t, node.RebootNeeded, "/run/reboot-required also means a reboot is needed")
}

func TestParseFCOSOSStatusUnavailableFallsBackToOSRelease(t *testing.T) {
	for name, raw := range map[string]string{
		"not installed": fcosProbeOutput("", "127", ""),
		"garbage":       fcosProbeOutput("error: org.projectatomic.rpmostree1 not available", "1", ""),
		"no booted":     fcosProbeOutput(`{"deployments":[{"version":"44.1","booted":false}]}`, "0", ""),
	} {
		t.Run(name, func(t *testing.T) {
			node := parseFCOSOSStatus(versionconfig.Node{Name: "k8s-0"}, raw, time.Now())
			assert.Equal(t, "RPM_OSTREE_UNAVAILABLE", node.UpdateStatus)
			assert.NotEqual(t, "unknown", node.Version)
		})
	}
}

func TestBuildFCOSOSStatusQueriesOnlyFCOSNodes(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{Nodes: []versionconfig.Node{
			{Name: "k8s-0", IP: "192.0.2.10", OS: "fcos"},
			{Name: "k8s-1", IP: "192.0.2.11"},
			{Name: "k8s-2", IP: "192.0.2.12", OS: "fcos"},
		}},
		Secrets: map[string]string{versionconfig.KeyNodeSSHUser: "literal://core"},
	})
	t.Cleanup(reset)
	var calls []string
	testutil.Swap(t, &osStatusNodeCommandFn, func(_ context.Context, node versionconfig.Node, user, command string) (string, error) {
		assert.Equal(t, "core", user)
		assert.Equal(t, fcosOSStatusInspectCommand, command)
		calls = append(calls, node.Name)
		sample := "rpm_ostree_status_idle.json"
		if node.Name == "k8s-2" {
			sample = "rpm_ostree_status_staged.json"
		}
		return fcosProbeOutput(readRPMOstreeSample(t, sample), "0", "active"), nil
	})

	report, err := buildFCOSOSStatus(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-2"}, calls)
	assert.Equal(t, []string{"reboot needed: k8s-2"}, report.Warnings)

	// The probe is read-only.
	assert.Contains(t, fcosOSStatusInspectCommand, "rpm-ostree status --json")
	assert.NotContains(t, fcosOSStatusInspectCommand, "rpm-ostree deploy")
	assert.NotContains(t, fcosOSStatusInspectCommand, "reboot ")
	assert.NotContains(t, fcosOSStatusInspectCommand, "sudo")
}

func TestBuildFCOSOSStatusRequiresFCOSNodesOrExplicitNames(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{})
	t.Cleanup(reset)
	_, err := buildFCOSOSStatus(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cluster nodes are configured os: fcos")

	_, err = buildFCOSOSStatus(context.Background(), []string{"nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown node "nope"`)

	testutil.Swap(t, &osStatusNodeCommandFn, func(_ context.Context, node versionconfig.Node, _, _ string) (string, error) {
		return fcosProbeOutput(readRPMOstreeSample(t, "rpm_ostree_status_idle.json"), "0", "failed"), nil
	})
	report, err := buildFCOSOSStatus(context.Background(), []string{"k8s-1"})
	require.NoError(t, err, "explicit names are queried even before the config is switched")
	require.Len(t, report.Nodes, 1)
	assert.Contains(t, strings.Join(report.Warnings, "\n"), "greenboot health check failed")
}

func TestFlatcarOSStatusSkipsFCOSNodes(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{Nodes: []versionconfig.Node{
			{Name: "k8s-0", IP: "192.0.2.10", OS: "fcos"},
			{Name: "k8s-1", IP: "192.0.2.11"},
			{Name: "k8s-2", IP: "192.0.2.12"},
		}},
	})
	t.Cleanup(reset)
	var calls []string
	testutil.Swap(t, &osStatusNodeCommandFn, func(_ context.Context, node versionconfig.Node, _, _ string) (string, error) {
		calls = append(calls, node.Name)
		return "__HOMEOPS_OS_RELEASE__\nVERSION_ID=4230.2.0\n__HOMEOPS_UPDATE_ENGINE__\nCURRENT_OP=UPDATE_STATUS_IDLE\n" +
			"__HOMEOPS_UPDATE_RC__=0\n__HOMEOPS_END__\n", nil
	})
	report, err := buildFlatcarOSStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-1", "k8s-2"}, calls)
	assert.Contains(t, report.Warnings, "skipped os: fcos node(s) (see `homeops-cli fcos os-status`): k8s-0")
}

func TestRenderFCOSOSStatusTableAndJSON(t *testing.T) {
	report := fcosOSStatusReport{
		Nodes: []fcosOSNodeStatus{{
			Node: "k8s-0", Version: "44.20260829.3.1", UpdateStatus: "staged", NewVersion: "44.20260915.3.0",
			RebootNeeded: true, Greenboot: "active", Kernel: "7.1.3-200.fc44.x86_64", UpSince: "2026-09-20 08:30:00",
			LayeredPackages: []string{"greenboot"},
		}},
		Warnings: []string{"reboot needed: k8s-0"},
	}
	table, err := renderFCOSOSStatus(report, "table")
	require.NoError(t, err)
	assert.Contains(t, table, "FCOS VERSION")
	assert.Contains(t, table, "GREENBOOT")
	assert.Contains(t, table, "44.20260915.3.0")
	assert.Contains(t, table, "WARN: reboot needed")

	jsonOutput, err := renderFCOSOSStatus(report, "json")
	require.NoError(t, err)
	assert.Contains(t, jsonOutput, `"fcos_version": "44.20260829.3.1"`)
	assert.Contains(t, jsonOutput, `"update_status": "staged"`)
	assert.Contains(t, jsonOutput, `"layered_packages"`)

	_, err = renderFCOSOSStatus(report, "yaml")
	require.Error(t, err)
}

func TestFCOSOSStatusCommandRendersJSONAndRejectsBadOutput(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{Nodes: []versionconfig.Node{{Name: "k8s-0", IP: "192.0.2.10", OS: "fcos"}}},
	})
	t.Cleanup(reset)
	testutil.Swap(t, &osStatusNodeCommandFn, func(context.Context, versionconfig.Node, string, string) (string, error) {
		return fcosProbeOutput(readRPMOstreeSample(t, "rpm_ostree_status_idle.json"), "0", "active"), nil
	})

	_, _, err := NewFCOSCommand().Find([]string{"os-status"})
	require.NoError(t, err, "os-status is registered in the fcos group")
	command := newFCOSOSStatusCommand()
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"--output", "json"})
	command.SetContext(context.Background())
	require.NoError(t, command.Execute())
	assert.Contains(t, output.String(), `"fcos_version": "44.20260829.3.1"`)

	bad := newFCOSOSStatusCommand()
	bad.SetOut(&strings.Builder{})
	bad.SetArgs([]string{"--output", "yaml"})
	err = bad.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported output")
}
