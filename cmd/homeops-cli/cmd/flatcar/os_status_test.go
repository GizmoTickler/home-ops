package flatcar

import (
	"context"
	"embed"
	"errors"
	"strings"
	"testing"
	"time"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/update_engine_*.txt
var updateEngineSamples embed.FS

func readUpdateEngineSample(t *testing.T, name string) string {
	t.Helper()
	raw, err := updateEngineSamples.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return string(raw)
}

func TestParseUpdateEngineStatusCapturedSamples(t *testing.T) {
	tests := []struct {
		name       string
		file       string
		currentOp  string
		newVersion string
		progress   float64
	}{
		{
			name:      "idle",
			file:      "update_engine_idle.txt",
			currentOp: "UPDATE_STATUS_IDLE",
		},
		{
			name:       "downloading",
			file:       "update_engine_downloading.txt",
			currentOp:  "UPDATE_STATUS_DOWNLOADING",
			newVersion: "4230.2.0",
			progress:   0.4275,
		},
		{
			name:       "updated needs reboot",
			file:       "update_engine_updated_need_reboot.txt",
			currentOp:  "UPDATE_STATUS_UPDATED_NEED_REBOOT",
			newVersion: "4230.2.0",
			progress:   1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUpdateEngineStatus(readUpdateEngineSample(t, tc.file))
			assert.Equal(t, tc.currentOp, got.CurrentOp)
			assert.Equal(t, tc.newVersion, got.NewVersion)
			assert.InDelta(t, tc.progress, got.Progress, 0.0001)
		})
	}
}

func TestParseUpdateEngineStatusToleratesNoiseAndPercent(t *testing.T) {
	status := parseUpdateEngineStatus("update_engine_client status:\nSTATUS = UPDATE_STATUS_DOWNLOADING\nTARGET_VERSION=4240.0.1\nPROGRESS=75%\nignored")
	assert.Equal(t, "UPDATE_STATUS_DOWNLOADING", status.CurrentOp)
	assert.Equal(t, "4240.0.1", status.NewVersion)
	assert.InDelta(t, 0.75, status.Progress, 0.0001)
}

func TestParseUpdateEngineStatusHidesIdleSentinelVersion(t *testing.T) {
	status := parseUpdateEngineStatus("CURRENT_OP=UPDATE_STATUS_IDLE\nNEW_VERSION=0.0.0\n")
	assert.Equal(t, "UPDATE_STATUS_IDLE", status.CurrentOp)
	assert.Empty(t, status.NewVersion)
}

func TestParseFlatcarOSStatus(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	raw := "__HOMEOPS_OS_RELEASE__\n" +
		"NAME=\"Flatcar Container Linux by Kinvolk\"\nVERSION=\"4230.1.2 (Oklo)\"\nVERSION_ID=4230.1.2\n" +
		"__HOMEOPS_UPDATE_ENGINE__\n" +
		readUpdateEngineSample(t, "update_engine_updated_need_reboot.txt") +
		"__HOMEOPS_UPDATE_RC__=0\n" +
		"__HOMEOPS_REBOOT_REQUIRED__=false\n" +
		"__HOMEOPS_KERNEL__=6.6.90-flatcar\n" +
		"__HOMEOPS_UP_SINCE__=2026-07-10 08:30:00\n" +
		"__HOMEOPS_UPTIME_SECONDS__=450000.00\n" +
		"__HOMEOPS_END__\n"
	node := parseFlatcarOSStatus(versionconfig.Node{Name: "k8s-0", IP: "192.0.2.10"}, raw, now)
	assert.Equal(t, "4230.1.2 (Oklo)", node.Version)
	assert.Equal(t, "UPDATE_STATUS_UPDATED_NEED_REBOOT", node.UpdateStatus)
	assert.Equal(t, "4230.2.0", node.NewVersion)
	assert.True(t, node.RebootNeeded)
	assert.Equal(t, "6.6.90-flatcar", node.Kernel)
	assert.Equal(t, "2026-07-10 08:30:00", node.UpSince)
}

func TestParseFlatcarOSStatusFallsBackToProcUptime(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	raw := "__HOMEOPS_OS_RELEASE__\nVERSION_ID=4230.1.2\n" +
		"__HOMEOPS_UPDATE_ENGINE__\nCURRENT_OP=UPDATE_STATUS_IDLE\n" +
		"__HOMEOPS_UPDATE_RC__=0\n__HOMEOPS_REBOOT_REQUIRED__=true\n" +
		"__HOMEOPS_KERNEL__=6.6.90-flatcar\n__HOMEOPS_UP_SINCE__=\n" +
		"__HOMEOPS_UPTIME_SECONDS__=3600.50\n__HOMEOPS_END__\n"
	node := parseFlatcarOSStatus(versionconfig.Node{Name: "k8s-1"}, raw, now)
	assert.Equal(t, "2026-07-15T10:59:59Z", node.UpSince)
	assert.True(t, node.RebootNeeded)
}

func TestUpdateNeedsReboot(t *testing.T) {
	assert.True(t, updateNeedsReboot("UPDATE_STATUS_UPDATED_NEED_REBOOT"))
	assert.True(t, updateNeedsReboot("updated_need_reboot"))
	assert.False(t, updateNeedsReboot("UPDATE_STATUS_IDLE"))
}

func TestFlatcarOSStatusWarnings(t *testing.T) {
	nodes := []flatcarOSNodeStatus{
		{Node: "k8s-0", Version: "4230.1.2"},
		{Node: "k8s-1", Version: "4230.2.0", RebootNeeded: true},
		{Node: "k8s-2", Version: "4230.2.0", RebootNeeded: true},
	}
	warnings := flatcarOSStatusWarnings(nodes)
	require.Len(t, warnings, 2)
	assert.Contains(t, warnings[0], "version skew")
	assert.Contains(t, warnings[0], "4230.1.2 (k8s-0)")
	assert.Equal(t, "reboot needed: k8s-1, k8s-2", warnings[1])

	assert.Empty(t, flatcarOSStatusWarnings([]flatcarOSNodeStatus{
		{Node: "k8s-0", Version: "4230.2.0"},
		{Node: "k8s-1", Version: "4230.2.0"},
	}))
}

func TestBuildFlatcarOSStatusUsesConfiguredNodesAndReadOnlyCommand(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{Nodes: []versionconfig.Node{
			{Name: "k8s-1", IP: "192.0.2.11"},
			{Name: "k8s-0", IP: "192.0.2.10"},
		}},
		Secrets: map[string]string{
			versionconfig.KeyNodeSSHUser: "literal://core",
		},
	})
	t.Cleanup(reset)
	var calls []string
	testutil.Swap(t, &osStatusNodeCommandFn, func(_ context.Context, node versionconfig.Node, user, command string) (string, error) {
		assert.Equal(t, "core", user)
		assert.Equal(t, osStatusInspectCommand, command)
		calls = append(calls, node.Name)
		return "__HOMEOPS_OS_RELEASE__\nVERSION_ID=4230.2.0\n" +
			"__HOMEOPS_UPDATE_ENGINE__\nCURRENT_OP=UPDATE_STATUS_IDLE\n" +
			"__HOMEOPS_UPDATE_RC__=0\n__HOMEOPS_REBOOT_REQUIRED__=false\n" +
			"__HOMEOPS_KERNEL__=6.6.90-flatcar\n__HOMEOPS_UP_SINCE__=2026-07-10 08:30:00\n" +
			"__HOMEOPS_UPTIME_SECONDS__=1\n__HOMEOPS_END__\n", nil
	})

	report, err := buildFlatcarOSStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"k8s-0", "k8s-1", "k8s-2"}, calls)
	assert.Equal(t, "k8s-0", report.Nodes[0].Node, "output should be sorted")
	assert.Empty(t, report.Warnings)
	assert.Contains(t, osStatusInspectCommand, "cat /etc/os-release")
	assert.Contains(t, osStatusInspectCommand, "update_engine_client -status")
	assert.Contains(t, osStatusInspectCommand, "sudo -n update_engine_client -status")
	assert.NotContains(t, osStatusInspectCommand, "reboot ")
	assert.NotContains(t, osStatusInspectCommand, "systemctl")
}

func TestBuildFlatcarOSStatusReturnsErrorOnlyForSSHFailure(t *testing.T) {
	reset := versionconfig.SetForTesting(&versionconfig.Config{
		Cluster: versionconfig.ClusterConfig{Nodes: []versionconfig.Node{{Name: "k8s-0", IP: "192.0.2.10"}}},
	})
	t.Cleanup(reset)
	testutil.Swap(t, &osStatusNodeCommandFn, func(context.Context, versionconfig.Node, string, string) (string, error) {
		return "", errors.New("connection refused")
	})

	report, err := buildFlatcarOSStatus(context.Background())
	require.Error(t, err)
	require.Len(t, report.Errors, 3)
	require.Len(t, report.Nodes, 3)
	assert.Equal(t, "SSH ERROR", report.Nodes[0].UpdateStatus)
}

func TestRenderFlatcarOSStatusTableAndJSON(t *testing.T) {
	report := flatcarOSStatusReport{
		Nodes: []flatcarOSNodeStatus{{
			Node: "k8s-0", Version: "4230.2.0", UpdateStatus: "UPDATE_STATUS_IDLE",
			Kernel: "6.6.90-flatcar", UpSince: "2026-07-10 08:30:00",
		}},
		Warnings: []string{"version skew: sample"},
	}
	table, err := renderFlatcarOSStatus(report, "table")
	require.NoError(t, err)
	assert.Contains(t, table, "FLATCAR VERSION")
	assert.Contains(t, table, "REBOOT NEEDED")
	assert.Contains(t, table, "WARN: version skew")
	assert.NotContains(t, table, "0.0.0")

	jsonOutput, err := renderFlatcarOSStatus(report, "json")
	require.NoError(t, err)
	assert.Contains(t, jsonOutput, `"flatcar_version": "4230.2.0"`)
	assert.Contains(t, jsonOutput, `"warnings"`)
}

func TestOSStatusCommandRejectsUnsupportedOutput(t *testing.T) {
	cmd := newOSStatusCommand()
	cmd.SetArgs([]string{"--output", "yaml"})
	var output strings.Builder
	cmd.SetOut(&output)
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported output")
}

func TestOSStatusCommandIsRegistered(t *testing.T) {
	command, _, err := NewCommand().Find([]string{"os-status"})
	require.NoError(t, err)
	assert.Equal(t, "os-status", command.Name())
}
