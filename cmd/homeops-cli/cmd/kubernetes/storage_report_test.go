package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func storageTestPVC(namespace, name, size string) storagePVC {
	var pvc storagePVC
	pvc.Metadata.Namespace = namespace
	pvc.Metadata.Name = name
	pvc.Metadata.CreationTimestamp = "2026-07-01T00:00:00Z"
	pvc.Spec.StorageClassName = "ceph-block"
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
			return []byte(`{"items":[{"metadata":{"namespace":"media","name":"orphan","creationTimestamp":"2026-07-01T00:00:00Z"},"spec":{"storageClassName":"ceph-block","resources":{"requests":{"storage":"5Gi"}}},"status":{"phase":"Bound"}}]}`), nil
		case cephClusterResource:
			return []byte(`{"items":[{"metadata":{"namespace":"rook-ceph","name":"rook-ceph"},"status":{"ceph":{"health":"HEALTH_OK","capacity":{"bytesUsed":90,"bytesTotal":100}}}}]}`), nil
		default:
			return []byte(`{"items":[]}`), nil
		}
	})

	report := buildStorageReport(context.Background(), "", 80)
	assert.Len(t, report.OrphanedPVCs, 1)
	require.Len(t, report.CephCapacity, 1)
	assert.Equal(t, storageWarn, report.CephCapacity[0].Status)
	assert.NotZero(t, report.Findings)
	for _, call := range calls {
		assert.Equal(t, "get", call[0])
		assert.Contains(t, call, "-o")
		assert.NotContains(t, call, "apply")
		assert.NotContains(t, call, "patch")
		assert.NotContains(t, call, "delete")
	}
}

func TestCephCapacityParsingAndThresholds(t *testing.T) {
	var list cephCapacityList
	err := json.Unmarshal([]byte(`{"items":[
		{"metadata":{"namespace":"rook-ceph","name":"warning"},"status":{"ceph":{"health":"HEALTH_OK","capacity":{"bytesUsed":"800","bytesTotal":1000}}}},
		{"metadata":{"namespace":"rook-ceph","name":"healthy"},"status":{"ceph":{"health":"HEALTH_OK","capacity":{"bytesUsed":799,"bytesTotal":"1000"}}}},
		{"metadata":{"namespace":"rook-ceph","name":"broken"},"status":{"ceph":{"health":"HEALTH_ERR","capacity":{"bytesUsed":1,"bytesTotal":1000}}}}
	]}`), &list)
	require.NoError(t, err)

	capacities := parseCephCapacities(list.Items, 80)
	byName := map[string]cephCapacity{}
	for _, capacity := range capacities {
		byName[capacity.Name] = capacity
	}
	assert.Equal(t, storageWarn, byName["warning"].Status)
	assert.InDelta(t, 80, byName["warning"].UsedPercent, 0.001)
	assert.Equal(t, storageOK, byName["healthy"].Status)
	assert.Equal(t, storageFail, byName["broken"].Status)
}

func TestCalculateProvisioningOvercommit(t *testing.T) {
	first := storageTestPVC("media", "one", "60Gi")
	second := storageTestPVC("media", "two", "50Gi")
	other := storageTestPVC("media", "three", "20Gi")
	other.Spec.StorageClassName = "openebs-hostpath"
	capacity := int64(100 * 1024 * 1024 * 1024)

	result := calculateProvisioning([]storagePVC{first, second, other}, capacity)
	require.Len(t, result, 2)
	byClass := map[string]storageProvisioning{}
	for _, item := range result {
		byClass[item.StorageClass] = item
	}
	assert.Equal(t, storageWarn, byClass["ceph-block"].Status)
	assert.InDelta(t, 1.1, byClass["ceph-block"].Ratio, 0.001)
	assert.Equal(t, storageOK, byClass["openebs-hostpath"].Status)
}

func TestDetectPVIssues(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	makePV := func(name, phase string, age time.Duration) storagePV {
		var pv storagePV
		pv.Metadata.Name = name
		pv.Metadata.CreationTimestamp = now.Add(-age).Format(time.RFC3339)
		pv.Spec.StorageClassName = "ceph-block"
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
