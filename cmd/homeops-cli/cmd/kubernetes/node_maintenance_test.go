package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"
)

func maintenanceTestConfig() *config.Config {
	return &config.Config{
		Cluster: config.ClusterConfig{Nodes: []config.Node{{
			Name: "k8s-0",
			IP:   "192.0.2.10",
			VM: config.VMProfile{Providers: config.ProviderVMProfiles{
				Flatcar: config.ProviderVMProfile{VMID: 200},
			}},
		}}},
		Hypervisors: config.HypervisorsConfig{Default: "proxmox"},
	}
}

func maintenanceNodeJSON(cordoned bool, annotation string) []byte {
	annotations := "{}"
	if annotation != "" {
		annotations = fmt.Sprintf(`{%q:%q}`, constants.CephNooutAnnotation, annotation)
	}
	return []byte(fmt.Sprintf(`{"metadata":{"name":"k8s-0","annotations":%s},"spec":{"unschedulable":%t},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`, annotations, cordoned))
}

func setupMaintenanceTest(t *testing.T, cordoned bool, annotation, flags string, failDrain bool) *[]string {
	t.Helper()
	events := []string{}
	testutil.Swap(t, &nodeMaintenanceConfigFn, func() *config.Config { return maintenanceTestConfig() })
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		call := "output " + strings.Join(args, " ")
		events = append(events, call)
		switch {
		case strings.HasPrefix(strings.Join(args, " "), "get node"):
			return maintenanceNodeJSON(cordoned, annotation), nil
		case strings.Contains(call, "get namespace rook-ceph"):
			return []byte("namespace/rook-ceph\n"), nil
		case strings.Contains(call, "ceph osd dump"):
			return []byte(fmt.Sprintf(`{"flags":%q}`, flags)), nil
		case strings.Contains(call, "ceph status"):
			return []byte(`{"health":{"status":"HEALTH_OK"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected output call: %s", call)
		}
	})
	testutil.Swap(t, &nodeMaintenanceKubectlRunFn, func(_ context.Context, args ...string) error {
		call := "run " + strings.Join(args, " ")
		events = append(events, call)
		if failDrain && strings.HasPrefix(strings.Join(args, " "), "drain ") {
			return errors.New("eviction failed")
		}
		return nil
	})
	testutil.Swap(t, &nodeMaintenanceVMPowerFn, func(context.Context, *config.Config, config.Node, bool, time.Duration) error {
		events = append(events, "vm power")
		return nil
	})
	return &events
}

func TestNodeMaintenanceEnterSequencesSafeSteps(t *testing.T) {
	events := setupMaintenanceTest(t, false, "", "sortbitwise", false)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: 5 * time.Minute, Timeout: time.Minute,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"output get node k8s-0 -o json",
		"output get namespace rook-ceph --ignore-not-found -o name",
		"output -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump --format json",
		"run -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd set noout",
		"run annotate node k8s-0 " + constants.CephNooutAnnotation + "=owned --overwrite",
		"run cordon k8s-0",
		"run drain k8s-0 --ignore-daemonsets --delete-emptydir-data --timeout=5m0s",
		"output get node k8s-0 -o json",
	}, *events)
	assert.Equal(t, []string{"DONE", "DONE", "DONE", "DONE", "SKIPPED"}, maintenanceStepStatuses(report))
}

func TestNodeMaintenanceEnterRollsBackInReverseOrder(t *testing.T) {
	events := setupMaintenanceTest(t, false, "", "", true)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: 5 * time.Minute, Timeout: time.Minute,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "maintenance enter failed")
	joined := strings.Join(*events, "\n")
	uncordon := strings.Index(joined, "run uncordon k8s-0")
	unset := strings.Index(joined, "ceph osd unset noout")
	remove := strings.Index(joined, constants.CephNooutAnnotation+"-")
	assert.Greater(t, uncordon, 0)
	assert.Greater(t, unset, uncordon)
	assert.Greater(t, remove, unset)
	assert.Equal(t, "FAILED", report.Steps[3].Status)
	assert.Equal(t, "rollback-uncordon", report.Steps[4].Name)
	assert.Equal(t, "rollback-unset-ceph-noout", report.Steps[5].Name)
}

func TestNodeMaintenanceEnterIsIdempotentWhenAlreadyCordoned(t *testing.T) {
	events := setupMaintenanceTest(t, true, nodeMaintenanceNooutOwned, "noout,sortbitwise", false)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: time.Minute, Timeout: time.Minute,
	})

	require.NoError(t, err)
	joined := strings.Join(*events, "\n")
	assert.NotContains(t, joined, "run cordon")
	assert.NotContains(t, joined, "ceph osd set noout")
	assert.NotContains(t, joined, constants.CephNooutAnnotation+"=owned")
	assert.Equal(t, "SKIPPED", report.Steps[2].Status)
	assert.Contains(t, joined, "run drain k8s-0")
}

func TestNodeMaintenanceExitIsIdempotentAndPreservesPreexistingNoout(t *testing.T) {
	events := setupMaintenanceTest(t, false, nodeMaintenanceNooutPreexisting, "noout", false)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "exit", Node: "k8s-0", Timeout: time.Minute,
	})

	require.NoError(t, err)
	joined := strings.Join(*events, "\n")
	assert.NotContains(t, joined, "run uncordon")
	assert.NotContains(t, joined, "ceph osd unset noout")
	assert.Contains(t, joined, constants.CephNooutAnnotation+"-")
	assert.Contains(t, joined, constants.LegacyCephNooutAnnotation+"-")
	assert.Equal(t, "HEALTH_OK", report.Final.Ceph)
}

func TestMaintenanceNooutOwnershipAcceptsLegacyAnnotation(t *testing.T) {
	assert.Equal(t, nodeMaintenanceNooutOwned, maintenanceNooutOwnership(map[string]string{
		constants.LegacyCephNooutAnnotation: nodeMaintenanceNooutOwned,
	}))
	assert.Equal(t, nodeMaintenanceNooutPreexisting, maintenanceNooutOwnership(map[string]string{
		constants.CephNooutAnnotation:       nodeMaintenanceNooutPreexisting,
		constants.LegacyCephNooutAnnotation: nodeMaintenanceNooutOwned,
	}))
}

func TestNodeMaintenanceUsesConfiguredRookLocation(t *testing.T) {
	testutil.Swap(t, &nodeMaintenanceConfigFn, func() *config.Config {
		return &config.Config{Cluster: config.ClusterConfig{Rook: config.RookConfig{
			Namespace:         "ceph-custom",
			ToolboxDeployment: "ceph-toolbox-custom",
		}}}
	})
	var calls []string
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "get" {
			return []byte("namespace/ceph-custom\n"), nil
		}
		return []byte(`{"flags":""}`), nil
	})

	present, err := rookCephPresent(context.Background())
	require.NoError(t, err)
	assert.True(t, present)
	_, err = cephCommandOutput(context.Background(), "osd", "dump", "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"get namespace ceph-custom --ignore-not-found -o name",
		"-n ceph-custom exec deploy/ceph-toolbox-custom -- ceph osd dump --format json",
	}, calls)
}

func TestRenderNodeMaintenanceJSON(t *testing.T) {
	report := nodeMaintenanceReport{Action: "enter", Node: "k8s-0", Steps: []nodeMaintenanceStep{{Name: "cordon", Status: "DONE", Duration: "1s"}}}
	rendered, err := renderNodeMaintenanceReport(report, "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"enter","node":"k8s-0","steps":[{"name":"cordon","status":"DONE","duration":"1s"}],"final":{"node_ready":false,"cordoned":false,"ceph":""}}`, rendered)
}

func TestConfiguredMaintenanceDuration(t *testing.T) {
	assert.Equal(t, 12*time.Minute, configuredMaintenanceDuration("12m", time.Minute))
	assert.Equal(t, time.Minute, configuredMaintenanceDuration("invalid", time.Minute))
	assert.Equal(t, time.Minute, configuredMaintenanceDuration("0s", time.Minute))
}

func maintenanceStepStatuses(report nodeMaintenanceReport) []string {
	statuses := make([]string, 0, len(report.Steps))
	for _, step := range report.Steps {
		statuses = append(statuses, step.Status)
	}
	return statuses
}
