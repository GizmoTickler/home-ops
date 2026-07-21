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

func maintenanceNodeJSON(cordoned bool) []byte {
	if cordoned {
		return []byte(`{"metadata":{"name":"k8s-0"},"spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	}
	return []byte(`{"metadata":{"name":"k8s-0"},"spec":{"unschedulable":false},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
}

func setupMaintenanceTest(t *testing.T, cordoned, failDrain bool) *[]string {
	t.Helper()
	events := []string{}
	testutil.Swap(t, &nodeMaintenanceConfigFn, func() *config.Config { return maintenanceTestConfig() })
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		call := "output " + strings.Join(args, " ")
		events = append(events, call)
		switch {
		case strings.HasPrefix(strings.Join(args, " "), "get node"):
			return maintenanceNodeJSON(cordoned), nil
		case strings.HasPrefix(strings.Join(args, " "), "get pods --namespace scale-csi"):
			return []byte(`{"items":[{"metadata":{"name":"scale-csi-node-test","ownerReferences":[{"kind":"DaemonSet","name":"scale-csi-node"}]},"status":{"phase":"Running"}}]}`), nil
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
	events := setupMaintenanceTest(t, false, false)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: 5 * time.Minute, Timeout: time.Minute,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"output get node k8s-0 -o json",
		"output get pods --namespace scale-csi --field-selector spec.nodeName=k8s-0 -o json",
		"run cordon k8s-0",
		"run drain k8s-0 --ignore-daemonsets --delete-emptydir-data --timeout=5m0s",
		"output get node k8s-0 -o json",
	}, *events)
	assert.Equal(t, []string{"DONE", "INFO", "DONE", "DONE", "SKIPPED"}, maintenanceStepStatuses(report))
}

func TestNodeMaintenanceEnterRollsBackInReverseOrder(t *testing.T) {
	events := setupMaintenanceTest(t, false, true)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: 5 * time.Minute, Timeout: time.Minute,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "maintenance enter failed")
	joined := strings.Join(*events, "\n")
	uncordon := strings.Index(joined, "run uncordon k8s-0")
	assert.Greater(t, uncordon, 0)
	assert.Equal(t, "FAILED", report.Steps[3].Status)
	assert.Equal(t, "rollback-uncordon", report.Steps[4].Name)
}

func TestNodeMaintenanceRollbackTimeoutStartsWhenRollbackBegins(t *testing.T) {
	setupMaintenanceTest(t, false, true)
	rollbackStarted := false
	testutil.Swap(t, &nodeMaintenanceRollbackContextFn, func(ctx context.Context) (context.Context, context.CancelFunc) {
		rollbackStarted = true
		return context.WithCancel(context.WithoutCancel(ctx))
	})
	baseRun := nodeMaintenanceKubectlRunFn
	testutil.Swap(t, &nodeMaintenanceKubectlRunFn, func(ctx context.Context, args ...string) error {
		if len(args) > 0 && args[0] == "drain" {
			assert.False(t, rollbackStarted, "rollback timeout started before the forward workflow failed")
		}
		if len(args) > 0 && args[0] == "uncordon" {
			require.NoError(t, ctx.Err(), "rollback received a context that expired before cleanup began")
		}
		return baseRun(ctx, args...)
	})

	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: 5 * time.Minute, Timeout: time.Minute,
	})

	require.Error(t, err)
	assert.Equal(t, "DONE", report.Steps[4].Status)
}

func TestNodeMaintenanceRollbackFailureIsReturned(t *testing.T) {
	setupMaintenanceTest(t, false, true)
	baseRun := nodeMaintenanceKubectlRunFn
	testutil.Swap(t, &nodeMaintenanceKubectlRunFn, func(ctx context.Context, args ...string) error {
		if len(args) > 0 && args[0] == "uncordon" {
			return errors.New("apiserver unavailable during rollback")
		}
		return baseRun(ctx, args...)
	})

	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: 5 * time.Minute, Timeout: time.Minute,
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "rollback-uncordon")
	assert.ErrorContains(t, err, "apiserver unavailable during rollback")
	assert.Equal(t, "FAILED", report.Steps[4].Status)
}

func TestNodeMaintenanceEnterIsIdempotentWhenAlreadyCordoned(t *testing.T) {
	events := setupMaintenanceTest(t, true, false)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "enter", Node: "k8s-0", DrainTimeout: time.Minute, Timeout: time.Minute,
	})

	require.NoError(t, err)
	joined := strings.Join(*events, "\n")
	assert.NotContains(t, joined, "run cordon")
	assert.Equal(t, "SKIPPED", report.Steps[2].Status)
	assert.Contains(t, joined, "run drain k8s-0")
}

func TestNodeMaintenanceExitIsIdempotentWhenAlreadySchedulable(t *testing.T) {
	events := setupMaintenanceTest(t, false, false)
	report, err := runNodeMaintenance(context.Background(), nodeMaintenanceOptions{
		Action: "exit", Node: "k8s-0", Timeout: time.Minute,
	})

	require.NoError(t, err)
	joined := strings.Join(*events, "\n")
	assert.NotContains(t, joined, "run uncordon")
	assert.False(t, report.Final.Cordoned)
}

func TestRenderNodeMaintenanceJSON(t *testing.T) {
	report := nodeMaintenanceReport{Action: "enter", Node: "k8s-0", Steps: []nodeMaintenanceStep{{Name: "cordon", Status: "DONE", Duration: "1s"}}}
	rendered, err := renderNodeMaintenanceReport(report, "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"enter","node":"k8s-0","steps":[{"name":"cordon","status":"DONE","duration":"1s"}],"final":{"node_ready":false,"cordoned":false}}`, rendered)
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
