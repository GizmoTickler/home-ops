package kubernetes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestK8sSuspendAppRunsExpectedMaintenanceSteps(t *testing.T) {
	oldRun := commandRunFn
	t.Cleanup(func() { commandRunFn = oldRun })
	var calls []string
	commandRunFn = func(name string, args ...string) error {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "get deployment radarr") {
			return nil
		}
		if strings.Contains(call, "get statefulset radarr") {
			return assert.AnError
		}
		return nil
	}

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
	oldRun := commandRunFn
	t.Cleanup(func() { commandRunFn = oldRun })
	var calls []string
	commandRunFn = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}

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
	oldRun := commandRunFn
	t.Cleanup(func() { commandRunFn = oldRun })
	commandRunFn = func(name string, args ...string) error {
		t.Fatalf("dry-run should not run %s %v", name, args)
		return nil
	}
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
