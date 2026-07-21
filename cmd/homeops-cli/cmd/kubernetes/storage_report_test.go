package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"
)

func storageTestPVC(namespace, name, size string) storagePVC {
	var pvc storagePVC
	pvc.Metadata.Namespace = namespace
	pvc.Metadata.Name = name
	pvc.Metadata.CreationTimestamp = "2026-07-01T00:00:00Z"
	pvc.Spec.StorageClassName = constants.ScaleCSIStorageClassNVMeOF
	pvc.Spec.Resources.Requests = map[string]string{"storage": size}
	pvc.Status.Phase = "Bound"
	return pvc
}

func TestDetectOrphanedPVCsReferencesAndTrueOrphan(t *testing.T) {
	podPVC := storageTestPVC("media", "pod-data", "10Gi")
	volsyncPVC := storageTestPVC("media", "backed-up", "20Gi")
	workloadPVC := storageTestPVC("media", "scaled-to-zero", "8Gi")
	orphanPVC := storageTestPVC("media", "forgotten", "5Gi")
	cachePVC := storageTestPVC("media", "volsync-radarr-cache", "1Gi")

	var pods storagePodList
	pods.Items = append(pods.Items, struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Volumes []struct {
				PersistentVolumeClaim *struct {
					ClaimName string `json:"claimName"`
				} `json:"persistentVolumeClaim"`
			} `json:"volumes"`
		} `json:"spec"`
	}{})
	pods.Items[0].Metadata.Namespace = "media"
	pods.Items[0].Spec.Volumes = append(pods.Items[0].Spec.Volumes, struct {
		PersistentVolumeClaim *struct {
			ClaimName string `json:"claimName"`
		} `json:"persistentVolumeClaim"`
	}{PersistentVolumeClaim: &struct {
		ClaimName string `json:"claimName"`
	}{ClaimName: "pod-data"}})

	var sources storageReplicationList
	sources.Items = append(sources.Items, struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			SourcePVC      string `json:"sourcePVC"`
			DestinationPVC string `json:"destinationPVC"`
		} `json:"spec"`
	}{})
	sources.Items[0].Metadata.Namespace = "media"
	sources.Items[0].Spec.SourcePVC = "backed-up"
	var workloads storageWorkloadList
	require.NoError(t, json.Unmarshal([]byte(`{"items":[{"metadata":{"namespace":"media","name":"scaled"},"spec":{"template":{"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"scaled-to-zero"}}]}}}}]}`), &workloads))

	result := detectOrphanedPVCs([]storagePVC{podPVC, volsyncPVC, workloadPVC, orphanPVC, cachePVC}, pods,
		storageStatefulSetList{}, workloads, sources, storageReplicationList{}, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	require.Len(t, result, 1)
	assert.Equal(t, "forgotten", result[0].Name)
	assert.Equal(t, "14d", result[0].Age)
}

func TestBuildStorageReportUsesReadOnlyFake(t *testing.T) {
	testutil.Swap(t, &storageNowFn, func() time.Time {
		return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	})
	var calls [][]string
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		resource := args[1]
		switch resource {
		case "persistentvolumeclaims":
			return []byte(`{"items":[{"metadata":{"namespace":"media","name":"orphan","creationTimestamp":"2026-07-01T00:00:00Z"},"spec":{"storageClassName":"scale-nvmeof","resources":{"requests":{"storage":"5Gi"}}},"status":{"phase":"Bound"}}]}`), nil
		case deploymentResource:
			return []byte(`{"spec":{"replicas":2},"status":{"readyReplicas":2}}`), nil
		case daemonSetResource:
			return []byte(`{"status":{"desiredNumberScheduled":3,"numberReady":3}}`), nil
		default:
			return []byte(`{"items":[]}`), nil
		}
	})

	report := buildStorageReport(context.Background(), "")
	assert.Len(t, report.OrphanedPVCs, 1)
	require.Len(t, report.StorageClasses, 3)
	assert.Equal(t, storageOK, report.ScaleCSIHealth.Status)
	assert.False(t, report.ScaleCSIMetrics.Available)
	assert.NotZero(t, report.Findings)
	for _, call := range calls {
		assert.Equal(t, "get", call[0])
		assert.Contains(t, call, "-o")
		assert.NotContains(t, call, "apply")
		assert.NotContains(t, call, "patch")
		assert.NotContains(t, call, "delete")
	}
}

func TestStorageClassAndSnapshotRollups(t *testing.T) {
	var pvcs storagePVCList
	require.NoError(t, json.Unmarshal([]byte(`{"items":[
		{"metadata":{"namespace":"media","name":"one"},"spec":{"storageClassName":"scale-nvmeof","resources":{"requests":{"storage":"10Gi"}}}},
		{"metadata":{"namespace":"media","name":"two"},"spec":{"storageClassName":"scale-nvmeof","resources":{"requests":{"storage":"5Gi"}}}},
		{"metadata":{"namespace":"other","name":"three"},"spec":{"storageClassName":"scale-nfs","resources":{"requests":{"storage":"2Gi"}}}}
	]}`), &pvcs))
	var pvs storagePVList
	require.NoError(t, json.Unmarshal([]byte(`{"items":[
		{"metadata":{"name":"pv-one"},"spec":{"storageClassName":"scale-nvmeof","capacity":{"storage":"20Gi"},"claimRef":{"namespace":"media","name":"one"}}},
		{"metadata":{"name":"pv-three"},"spec":{"storageClassName":"scale-nfs","capacity":{"storage":"2Gi"},"claimRef":{"namespace":"other","name":"three"}}}
	]}`), &pvs))

	rollups, problems := calculateStorageClassRollups(pvcs.Items[:2], pvs.Items, "media")
	require.Empty(t, problems)
	byClass := map[string]storageClassRollup{}
	for _, rollup := range rollups {
		byClass[rollup.StorageClass] = rollup
	}
	assert.Equal(t, 2, byClass[constants.ScaleCSIStorageClassNVMeOF].PVCCount)
	assert.EqualValues(t, 15*1024*1024*1024, byClass[constants.ScaleCSIStorageClassNVMeOF].PVCRequestedBytes)
	assert.Equal(t, 1, byClass[constants.ScaleCSIStorageClassNVMeOF].PVCount)
	assert.Equal(t, 0, byClass[constants.ScaleCSIStorageClassNFS].PVCount)

	var snapshots storageVolumeSnapshotList
	require.NoError(t, json.Unmarshal([]byte(`{"items":[
		{"spec":{"volumeSnapshotClassName":"scale-snapshot"}},
		{"spec":{"volumeSnapshotClassName":"scale-snapshot"}},
		{"spec":{"volumeSnapshotClassName":"other-snapshot"}}
	]}`), &snapshots))
	snapshotRollups := calculateVolumeSnapshotRollups(snapshots)
	assert.Equal(t, []volumeSnapshotRollup{
		{VolumeSnapshotClass: "other-snapshot", Count: 1},
		{VolumeSnapshotClass: constants.ScaleCSIVolumeSnapshotClass, Count: 2},
	}, snapshotRollups)
}

func TestParseScaleCSIMetricsAndFindings(t *testing.T) {
	raw := []byte(`# HELP scale_csi_orphan_volumes Orphan volumes
scale_csi_orphan_volumes 2
scale_csi_orphan_snapshots 0
scale_csi_spent_restore_snapshots 1
scale_csi_truenas_connection_status 1
`)
	values, missing, err := parseScaleCSIMetrics(raw)
	require.NoError(t, err)
	assert.Empty(t, missing)
	assert.Equal(t, float64(2), values[scaleCSIOrphanVolumesMetric])
	assert.Equal(t, 2, scaleCSIMetricsFindings(scaleCSIMetricsReport{Available: true, Values: values}))
}

func TestBuildStorageReportCollectsScaleCSIMetricsThroughServiceProxy(t *testing.T) {
	var rawPath string
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 2 && args[1] == "--raw" {
			rawPath = args[2]
			return []byte(`scale_csi_orphan_volumes 1
scale_csi_orphan_snapshots 0
scale_csi_spent_restore_snapshots 0
scale_csi_truenas_connection_status 1
`), nil
		}
		switch args[1] {
		case deploymentResource:
			return []byte(`{"spec":{"replicas":2},"status":{"readyReplicas":2}}`), nil
		case daemonSetResource:
			return []byte(`{"status":{"desiredNumberScheduled":3,"numberReady":3}}`), nil
		case "services":
			return []byte(`{"items":[
				{"metadata":{"name":"other-metrics"},"spec":{"ports":[{"name":"metrics","port":9090}]}},
				{"metadata":{"name":"scale-csi-controller-metrics"},"spec":{"ports":[{"name":"metrics","port":9809}]}}
			]}`), nil
		default:
			return []byte(`{"items":[]}`), nil
		}
	})

	report := buildStorageReport(context.Background(), "")
	assert.True(t, report.ScaleCSIMetrics.Available)
	assert.Equal(t, storageWarn, report.ScaleCSIMetrics.Status)
	assert.Equal(t, float64(1), report.ScaleCSIMetrics.Values[scaleCSIOrphanVolumesMetric])
	assert.Equal(t, "/api/v1/namespaces/scale-csi/services/scale-csi-controller-metrics:9809/proxy/metrics", rawPath)
	assert.Equal(t, 1, report.Findings)
}

func TestDetectPVIssues(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	makePV := func(name, phase string, age time.Duration) storagePV {
		var pv storagePV
		pv.Metadata.Name = name
		pv.Metadata.CreationTimestamp = now.Add(-age).Format(time.RFC3339)
		pv.Spec.StorageClassName = constants.ScaleCSIStorageClassNVMeOF
		pv.Spec.Capacity = map[string]string{"storage": "10Gi"}
		pv.Status.Phase = phase
		return pv
	}
	issues := detectPVIssues([]storagePV{
		makePV("released", "Released", time.Hour),
		makePV("failed", "Failed", time.Hour),
		makePV("old-available", "Available", 25*time.Hour),
		makePV("young-available", "Available", 23*time.Hour),
		makePV("bound", "Bound", 30*24*time.Hour),
	}, "", now, 24*time.Hour)
	require.Len(t, issues, 3)
	byName := map[string]pvIssue{}
	for _, issue := range issues {
		byName[issue.Name] = issue
	}
	assert.Equal(t, storageFail, byName["failed"].Status)
	assert.Equal(t, storageWarn, byName["released"].Status)
	assert.Equal(t, storageWarn, byName["old-available"].Status)
}

func TestStorageReportCommandFailOnFindings(t *testing.T) {
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "persistentvolumeclaims" {
			return []byte(`{"items":[{"metadata":{"namespace":"media","name":"orphan"},"spec":{"resources":{"requests":{"storage":"1Gi"}}}}]}`), nil
		}
		return []byte(`{"items":[]}`), nil
	})
	cmd := newStorageReportCommand()
	cmd.SetArgs([]string{"--fail-on-findings"})
	var output strings.Builder
	cmd.SetOut(&output)
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage report found")
	assert.Contains(t, output.String(), "ORPHANED PVCs")
}
