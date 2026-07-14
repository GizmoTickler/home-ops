package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func swapK8sCommandRun(t *testing.T, fn func(name string, args ...string) error) {
	t.Helper()
	oldRun := commandRunFn
	oldRunCtx := commandRunCtxFn
	commandRunFn = fn
	commandRunCtxFn = func(ctx context.Context, name string, args ...string) error {
		return fn(name, args...)
	}
	t.Cleanup(func() {
		commandRunFn = oldRun
		commandRunCtxFn = oldRunCtx
	})
}

func TestK8sSuspendAppRunsExpectedMaintenanceSteps(t *testing.T) {
	var calls []string
	swapK8sCommandRun(t, func(name string, args ...string) error {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "get deployment radarr") {
			return nil
		}
		if strings.Contains(call, "get statefulset radarr") {
			return assert.AnError
		}
		return nil
	})

	err := runK8sAppMaintenance("suspend", "downloads", "radarr", false, &strings.Builder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"flux --namespace downloads suspend kustomization radarr",
		"flux --namespace downloads suspend helmrelease radarr",
		"kubectl --namespace downloads get deployment radarr",
		"kubectl --namespace downloads scale deployment/radarr --replicas=0",
		"kubectl --namespace downloads patch replicationsource radarr --type merge -p {\"spec\":{\"suspend\":true}}",
	}, calls)
}

func TestK8sResumeAppRunsExpectedMaintenanceSteps(t *testing.T) {
	var calls []string
	swapK8sCommandRun(t, func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	})

	err := runK8sAppMaintenance("resume", "downloads", "radarr", false, &strings.Builder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"kubectl --namespace downloads patch replicationsource radarr --type merge -p {\"spec\":{\"suspend\":false}}",
		"flux --namespace downloads resume kustomization radarr",
		"flux --namespace downloads resume helmrelease radarr",
		"flux --namespace downloads reconcile kustomization radarr --with-source",
		"flux --namespace downloads reconcile helmrelease radarr --force",
	}, calls)
}

func TestK8sMaintenanceDryRunDoesNotMutate(t *testing.T) {
	swapK8sCommandRun(t, func(name string, args ...string) error {
		t.Fatalf("dry-run should not run %s %v", name, args)
		return nil
	})
	var out strings.Builder
	err := runK8sAppMaintenance("suspend", "downloads", "radarr", true, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "DRY-RUN")
	assert.Contains(t, out.String(), "flux --namespace downloads suspend kustomization radarr")
	assert.Contains(t, out.String(), "kubectl --namespace downloads scale <detected-controller>/radarr --replicas=0")
}

func TestK8sMaintenanceRequiresNamespaceAndApp(t *testing.T) {
	var out strings.Builder
	err := runK8sAppMaintenance("suspend", "", "radarr", false, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--namespace is required")

	err = runK8sAppMaintenance("suspend", "downloads", "", false, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app is required")
}

func TestK8sSuspendRollsBackCompletedStepsOnMidSequenceFailure(t *testing.T) {
	var calls []string
	swapK8sCommandRun(t, func(name string, args ...string) error {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "get deployment radarr") {
			return nil
		}
		if strings.Contains(call, "patch replicationsource radarr") && strings.Contains(call, "suspend\":true") {
			return assert.AnError
		}
		return nil
	})

	err := runK8sAppMaintenance("suspend", "downloads", "radarr", false, &strings.Builder{})

	require.Error(t, err)
	assert.Equal(t, []string{
		"flux --namespace downloads suspend kustomization radarr",
		"flux --namespace downloads suspend helmrelease radarr",
		"kubectl --namespace downloads get deployment radarr",
		"kubectl --namespace downloads scale deployment/radarr --replicas=0",
		"kubectl --namespace downloads patch replicationsource radarr --type merge -p {\"spec\":{\"suspend\":true}}",
		"kubectl --namespace downloads scale deployment/radarr --replicas=1",
		"flux --namespace downloads resume helmrelease radarr",
		"flux --namespace downloads resume kustomization radarr",
	}, calls)
}

func TestK8sResumeRollsBackCompletedStepsOnMidSequenceFailure(t *testing.T) {
	var calls []string
	swapK8sCommandRun(t, func(name string, args ...string) error {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "reconcile kustomization radarr --with-source") {
			return assert.AnError
		}
		return nil
	})

	err := runK8sAppMaintenance("resume", "downloads", "radarr", false, &strings.Builder{})

	require.Error(t, err)
	assert.Equal(t, []string{
		"kubectl --namespace downloads patch replicationsource radarr --type merge -p {\"spec\":{\"suspend\":false}}",
		"flux --namespace downloads resume kustomization radarr",
		"flux --namespace downloads resume helmrelease radarr",
		"flux --namespace downloads reconcile kustomization radarr --with-source",
		"flux --namespace downloads suspend helmrelease radarr",
		"flux --namespace downloads suspend kustomization radarr",
		"kubectl --namespace downloads patch replicationsource radarr --type merge -p {\"spec\":{\"suspend\":true}}",
	}, calls)
}

func TestK8sMaintenanceThreadsCallerContext(t *testing.T) {
	oldRunCtx := commandRunCtxFn
	t.Cleanup(func() { commandRunCtxFn = oldRunCtx })

	type maintenanceContextKey struct{}
	key := maintenanceContextKey{}
	ctx := context.WithValue(context.Background(), key, "k8s-maintenance")
	var sawContext bool
	commandRunCtxFn = func(ctx context.Context, name string, args ...string) error {
		sawContext = sawContext || ctx.Value(key) == "k8s-maintenance"
		return nil
	}

	err := runK8sAppMaintenanceContext(ctx, "resume", "downloads", "radarr", false, &strings.Builder{})

	require.NoError(t, err)
	assert.True(t, sawContext)
}
