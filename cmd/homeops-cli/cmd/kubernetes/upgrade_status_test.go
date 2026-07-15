package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func TestParseSystemUpgradePlansVersionChannelConcurrencyAndLastApplied(t *testing.T) {
	raw := []byte(`{
  "items": [
    {
      "metadata": {"name": "kubeadm-control-plane", "namespace": "system-upgrade"},
      "spec": {"version": "v1.37.4", "concurrency": 1},
      "status": {"latestVersion": "v1.36.8", "latestHash": "abc123", "applying": ["k8s-0"]}
    },
    {
      "metadata": {"name": "channel-plan", "namespace": "other"},
      "spec": {"channel": "https://updates.example.invalid/stable", "concurrency": 2},
      "status": {"latestVersion": "v1.38.1"}
    }
  ]
}`)
	plans, err := parseSystemUpgradePlans(raw)
	require.NoError(t, err)
	require.Len(t, plans, 2)
	assert.Equal(t, upgradeStatusPlan{
		Namespace: "system-upgrade", Name: "kubeadm-control-plane", Target: "v1.37.4",
		Concurrency: 1, LastApplied: "abc123", Applying: []string{"k8s-0"},
	}, plans[0])
	assert.Equal(t, "v1.38.1", plans[1].Target, "resolved latestVersion is the target for a channel-only plan")
	assert.Equal(t, "https://updates.example.invalid/stable", plans[1].Channel)
	assert.Equal(t, int64(2), plans[1].Concurrency)
	assert.Equal(t, "v1.37.4", selectKubernetesPlanTarget(plans))
}

func TestKubernetesVersionComparisonAndSkew(t *testing.T) {
	tests := []struct {
		name        string
		left, right string
		comparison  int
	}{
		{name: "v prefix equal", left: "v1.36.2", right: "1.36.2", comparison: 0},
		{name: "build metadata ignored", left: "v1.36.2+flatcar", right: "v1.36.2", comparison: 0},
		{name: "prerelease before release", left: "v1.37.0-rc.1", right: "v1.37.0", comparison: -1},
		{name: "prerelease numeric", left: "v1.37.0-rc.10", right: "v1.37.0-rc.2", comparison: 1},
		{name: "minor pending", left: "v1.36.9", right: "v1.37.0-alpha.1", comparison: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comparison, err := compareKubernetesVersions(tt.left, tt.right)
			require.NoError(t, err)
			assert.Equal(t, tt.comparison, comparison)
		})
	}
	_, err := compareKubernetesVersions("not-a-version", "v1.36.1")
	require.Error(t, err)

	delta, skew := kubernetesMinorSkew("v1.38.0", "v1.36.9-pre.1")
	assert.Equal(t, 2, delta)
	assert.True(t, skew)
	delta, skew = kubernetesMinorSkew("v1.37.2", "v1.36.9")
	assert.Equal(t, 1, delta)
	assert.False(t, skew)
	assert.Equal(t, "Pending", classifyNodeUpgradeStatus("v1.36.9", "v1.37.0"))
	assert.Equal(t, "UpToDate", classifyNodeUpgradeStatus("1.37.0", "v1.37.0"))
	assert.Equal(t, "Unknown", classifyNodeUpgradeStatus("v1.38.0", "v1.37.0"))
}

func TestClassifyUpgradeJobFailure(t *testing.T) {
	imageJob := upgradeJobJSON{}
	imageJob.Status.Active = 1
	imagePod := upgradePodJSON{}
	imagePod.Status.ContainerStatuses = []upgradeContainerState{{}}
	imagePod.Status.ContainerStatuses[0].State.Waiting = &struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}{Reason: "ImagePullBackOff", Message: "Back-off pulling image registry.invalid/tool:1"}
	assert.Contains(t, classifyUpgradeJobFailure(imageJob, []upgradePodJSON{imagePod}), "image pull: ImagePullBackOff")

	drainJob := upgradeJobJSON{}
	drainJob.Status.Failed = 1
	drainJob.Status.Conditions = []conditionJSON{{Type: "Failed", Status: "True", Reason: "DeadlineExceeded", Message: "kubectl drain timed out evicting a PDB-protected pod"}}
	assert.Contains(t, classifyUpgradeJobFailure(drainJob, nil), "drain stuck:")

	unknown := upgradeJobJSON{}
	unknown.Status.Failed = 1
	assert.Equal(t, "job failed without a reported reason", classifyUpgradeJobFailure(unknown, nil))
	assert.Equal(t, "upgrade job is active", classifyUpgradeJobFailure(upgradeJobJSON{}, nil))
}

func TestCollectUpgradeJobsFiltersCompletedAndNonSUCJobs(t *testing.T) {
	failed := upgradeJobJSON{}
	failed.Metadata.Name = "upgrade-k8s-0"
	failed.Metadata.Namespace = "system-upgrade"
	failed.Metadata.Labels = map[string]string{"upgrade.cattle.io/plan": "kubeadm-control-plane", "upgrade.cattle.io/node": "k8s-0"}
	failed.Status.Failed = 1
	completed := upgradeJobJSON{}
	completed.Metadata.Labels = map[string]string{"upgrade.cattle.io/plan": "kubeadm-control-plane"}
	nonSUC := upgradeJobJSON{}
	nonSUC.Status.Failed = 1

	jobs := collectUpgradeJobs([]upgradeJobJSON{failed, completed, nonSUC}, nil)
	require.Len(t, jobs, 1)
	assert.Equal(t, "Failed", jobs[0].Status)
	assert.Equal(t, "k8s-0", jobs[0].Node)
}

func TestBuildUpgradeStatusReportWarnsPendingAndSkewFailsOnlyFailedJobs(t *testing.T) {
	responses := map[string][]byte{
		"get plans.upgrade.cattle.io -A -o json": []byte(`{"items":[{"metadata":{"name":"kubeadm-control-plane","namespace":"system-upgrade"},"spec":{"version":"v1.38.0","concurrency":1},"status":{"latestVersion":"v1.38.0"}}]}`),
		"get nodes -o json":                      []byte(`{"items":[{"metadata":{"name":"k8s-0"},"status":{"nodeInfo":{"kubeletVersion":"v1.36.9","containerRuntimeVersion":"containerd://2.1.4","osImage":"Flatcar Container Linux"}}}]}`),
		"version -o json":                        []byte(`{"serverVersion":{"gitVersion":"v1.38.0"}}`),
		"get jobs -A -o json":                    []byte(`{"items":[{"metadata":{"name":"upgrade-k8s-0","namespace":"system-upgrade","labels":{"upgrade.cattle.io/plan":"kubeadm-control-plane","upgrade.cattle.io/node":"k8s-0"}},"status":{"failed":1,"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"upgrade script failed"}]}}]}`),
		"get pods -A -o json":                    []byte(`{"items":[]}`),
	}
	testutil.Swap(t, &upgradeStatusKubectlOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		value, ok := responses[key]
		if !ok {
			return nil, errors.New("unexpected kubectl call: " + key)
		}
		return value, nil
	})

	report, err := buildUpgradeStatusReport(context.Background())
	require.NoError(t, err)
	assert.Equal(t, upgradeStatusSummary{Pass: 0, Warn: 2, Fail: 1}, report.Summary)
	assert.Equal(t, "Pending", report.Nodes[0].Status)
	require.Len(t, report.Skew, 1)
	require.Len(t, report.Jobs, 1)
	assert.Equal(t, "Failed", report.Jobs[0].Status)

	var out bytes.Buffer
	err = runUpgradeStatus(context.Background(), "json", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 failed")
	assert.Contains(t, out.String(), `"status": "Pending"`)
	assert.Contains(t, out.String(), `"status": "Failed"`)
}

func TestRenderUpgradeStatusTableIncludesAllSections(t *testing.T) {
	report := upgradeStatusReport{
		Summary: upgradeStatusSummary{Pass: 1}, APIServerVersion: "v1.37.4",
		Plans: []upgradeStatusPlan{{Namespace: "system-upgrade", Name: "kubeadm", Target: "v1.37.4", Concurrency: 1, LastApplied: "v1.37.4"}},
		Nodes: []upgradeStatusNode{{Name: "k8s-0", KubeletVersion: "v1.37.4", ContainerRuntime: "containerd://2", OS: "Flatcar", Target: "v1.37.4", Status: "UpToDate"}},
	}
	rendered, err := renderUpgradeStatusReport(report, "table")
	require.NoError(t, err)
	for _, expected := range []string{"Summary: PASS=1 WARN=0 FAIL=0", "Plans", "Nodes", "Active/failed SUC jobs", "Version skew", "UpToDate"} {
		assert.Contains(t, rendered, expected)
	}
	_, err = renderUpgradeStatusReport(report, "yaml")
	require.Error(t, err)
}
