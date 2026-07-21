package volsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMigrateOptions() migrateOptions {
	return migrateOptions{
		Namespace:       "media",
		App:             "seerr",
		TargetClass:     constants.ScaleCSIStorageClassNVMeOF,
		TargetSnapClass: constants.ScaleCSIVolumeSnapshotClass,
	}
}

func TestMigrationPreflightRejectsStaleKustomizationSubstitutes(t *testing.T) {
	testutil.Swap(t, &commandOutputFn, func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "kubectl", name)
		switch strings.Join(args, " ") {
		case "get replicationsource seerr --namespace media -o json":
			return []byte(`{"spec":{"sourcePVC":"seerr","trigger":{"schedule":"*/15 * * * *"}},"status":{"latestMoverStatus":{"result":"Successful"}}}`), nil
		case "get kustomization seerr --namespace media -o json":
			return []byte(`{"spec":{"postBuild":{"substitute":{"VOLSYNC_STORAGECLASS":"old-class","VOLSYNC_SNAPSHOTCLASS":"old-snapclass"}}}}`), nil
		default:
			return nil, errors.New("unexpected read: " + strings.Join(args, " "))
		}
	})
	testutil.Swap(t, &commandRunFn, func(name string, args ...string) error {
		t.Fatalf("preflight must abort before cluster class checks: %s %v", name, args)
		return nil
	})

	_, alreadyMigrated, err := preflightMigration(testMigrateOptions())

	require.Error(t, err)
	assert.False(t, alreadyMigrated)
	assert.Contains(t, err.Error(), "VOLSYNC_STORAGECLASS: scale-nvmeof")
	assert.Contains(t, err.Error(), "VOLSYNC_SNAPSHOTCLASS: scale-snapshot")
	assert.Contains(t, err.Error(), "merge to git first")
	assert.Contains(t, err.Error(), "Do not edit the live CR")
}

func TestMigrationPreflightAlreadyOnTargetIsNoOp(t *testing.T) {
	testutil.Swap(t, &commandOutputFn, func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "kubectl", name)
		switch strings.Join(args, " ") {
		case "get replicationsource seerr --namespace media -o json":
			return []byte(`{"spec":{"sourcePVC":"seerr","trigger":{"schedule":"*/15 * * * *"}},"status":{"latestMoverStatus":{"result":"Successful"}}}`), nil
		case "get kustomization seerr --namespace media -o json":
			return []byte(`{"spec":{"postBuild":{"substitute":{"VOLSYNC_STORAGECLASS":"scale-nvmeof","VOLSYNC_SNAPSHOTCLASS":"scale-snapshot"}}}}`), nil
		case "get pvc seerr --namespace media -o json":
			return []byte(`{"spec":{"storageClassName":"scale-nvmeof","dataSourceRef":{"apiGroup":"volsync.backube","kind":"ReplicationDestination","name":"seerr-dst"},"resources":{"requests":{"storage":"10Gi"}}},"status":{"phase":"Bound"}}`), nil
		default:
			return nil, errors.New("unexpected read: " + strings.Join(args, " "))
		}
	})
	var classChecks []string
	testutil.Swap(t, &commandRunFn, func(name string, args ...string) error {
		require.Equal(t, "kubectl", name)
		classChecks = append(classChecks, strings.Join(args, " "))
		return nil
	})

	plan, alreadyMigrated, err := preflightMigration(testMigrateOptions())

	require.NoError(t, err)
	assert.True(t, alreadyMigrated)
	assert.Equal(t, "scale-nvmeof", plan.CurrentClass)
	assert.Equal(t, "*/15 * * * *", plan.OriginalSchedule)
	assert.Equal(t, []string{
		"get storageclass scale-nvmeof",
		"get volumesnapshotclass scale-snapshot",
	}, classChecks)
}

func TestMigrationPreflightRejectsPVCWithoutComponentDataSourceRefBeforeMutation(t *testing.T) {
	testutil.Swap(t, &commandOutputFn, func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "kubectl", name)
		switch strings.Join(args, " ") {
		case "get replicationsource seerr --namespace media -o json":
			return []byte(`{"spec":{"sourcePVC":"seerr","trigger":{"schedule":"*/15 * * * *"}},"status":{"latestMoverStatus":{"result":"Successful"}}}`), nil
		case "get kustomization seerr --namespace media -o json":
			return []byte(`{"spec":{"postBuild":{"substitute":{"VOLSYNC_STORAGECLASS":"scale-nvmeof","VOLSYNC_SNAPSHOTCLASS":"scale-snapshot"}}}}`), nil
		case "get pvc seerr --namespace media -o json":
			return []byte(`{"spec":{"storageClassName":"ceph-block","resources":{"requests":{"storage":"10Gi"}}},"status":{"phase":"Bound"}}`), nil
		default:
			return nil, errors.New("unexpected read: " + strings.Join(args, " "))
		}
	})
	var commands []string
	testutil.Swap(t, &commandRunFn, func(name string, args ...string) error {
		command := strings.Join(append([]string{name}, args...), " ")
		commands = append(commands, command)
		require.Equal(t, "kubectl", name)
		require.NotEmpty(t, args)
		assert.Equal(t, "get", args[0], "preflight must not issue a mutation")
		return nil
	})

	_, alreadyMigrated, err := preflightMigration(testMigrateOptions())

	require.Error(t, err)
	assert.False(t, alreadyMigrated)
	assert.Contains(t, err.Error(), "spec.dataSourceRef must reference ReplicationDestination volsync.backube/seerr-dst")
	assert.Contains(t, err.Error(), "StatefulSet-template PVC would be recreated EMPTY")
	assert.Contains(t, err.Error(), "data loss")
	assert.Equal(t, []string{
		"kubectl get storageclass scale-nvmeof",
		"kubectl get volumesnapshotclass scale-snapshot",
	}, commands)
}

func TestMigrationRefusesPVCDeletionWhilePodsStillReferenceIt(t *testing.T) {
	testutil.Swap(t, &commandOutputFn, func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "kubectl", name)
		return []byte(`{"items":[{"metadata":{"name":"seerr-abc"},"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"seerr"}}]}}]}`), nil
	})
	testutil.Swap(t, &commandRunFn, func(name string, args ...string) error {
		t.Fatalf("no delete may be issued while a pod references the PVC: %s %v", name, args)
		return nil
	})

	err := refusePVCDeletionWhileReferenced("media", "seerr")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to delete PVC media/seerr")
	assert.Contains(t, err.Error(), "seerr-abc")
}

func TestRunVolsyncMigratePostDeleteFailureNeverRollsBackScale(t *testing.T) {
	harness := newMigrateRunHarness(t)
	harness.migratedPVCFn = func() ([]byte, error) {
		return nil, errors.New("restored PVC not found")
	}

	err := runVolsyncMigrate(t.Context(), migrateRunOptions(), &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not finish after PVC deletion was issued")
	assert.Contains(t, err.Error(), "Continue forward with")
	assert.Contains(t, err.Error(), `"schedule":"*/15 * * * *"`)
	deleteIndex := indexMigrationCall(harness.runCalls, "kubectl delete pvc seerr --namespace media --ignore-not-found --wait=false")
	require.NotEqual(t, -1, deleteIndex, "test must reach the destructive PVC deletion")
	for _, call := range harness.runCalls[deleteIndex+1:] {
		assert.NotEqual(t, "kubectl scale statefulset/seerr --namespace media --replicas=2", call,
			"post-delete recovery must never roll the workload back up")
	}
}

func TestRunVolsyncMigratePreDeleteGuardsBlockReappearingPod(t *testing.T) {
	tests := []struct {
		name                           string
		reappearAfterDestinationDelete bool
	}{
		{name: "before ReplicationDestination deletion", reappearAfterDestinationDelete: false},
		{name: "after ReplicationDestination deletion", reappearAfterDestinationDelete: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newMigrateRunHarness(t)
			harness.podsFn = func() []byte {
				switch harness.podReads {
				case 1:
					return migrationConsumerPodJSON("seerr-0")
				case 2:
					return []byte(`{"items":[]}`)
				default:
					if harness.destinationDeleted == tt.reappearAfterDestinationDelete {
						return migrationConsumerPodJSON("seerr-reappeared")
					}
					return []byte(`{"items":[]}`)
				}
			}

			err := runVolsyncMigrate(t.Context(), migrateRunOptions(), &bytes.Buffer{})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to delete PVC media/seerr")
			assert.Contains(t, err.Error(), "seerr-reappeared")
			assert.Equal(t, -1, indexMigrationCall(harness.runCalls, "kubectl delete pvc seerr --namespace media --ignore-not-found --wait=false"),
				"neither pre-delete guard may allow PVC deletion after a pod reappears")
		})
	}
}

func TestRunVolsyncMigrateRestoresOriginalScheduleAfterMidCutoverFailure(t *testing.T) {
	harness := newMigrateRunHarness(t)
	harness.podsFn = func() []byte {
		return migrationConsumerPodJSON("seerr-0")
	}

	err := runVolsyncMigrate(t.Context(), migrateRunOptions(), &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out waiting for all pods referencing PVC")
	combinedCalls := strings.Join(harness.combinedCalls, "\n")
	assert.Contains(t, combinedCalls, `"schedule":"*/15 * * * *"`)
	assert.NotContains(t, combinedCalls, `"schedule":"0 * * * *"`)
	assert.Contains(t, harness.runCalls, "kubectl scale statefulset/seerr --namespace media --replicas=2",
		"pre-delete failures should still restore the original replica count")
}

type migrateRunHarness struct {
	t                  *testing.T
	originalSchedule   string
	sourceReads        int
	podReads           int
	pvcReads           int
	destinationDeleted bool
	runCalls           []string
	combinedCalls      []string
	podsFn             func() []byte
	migratedPVCFn      func() ([]byte, error)
}

func newMigrateRunHarness(t *testing.T) *migrateRunHarness {
	t.Helper()
	harness := &migrateRunHarness{t: t, originalSchedule: "*/15 * * * *"}
	testutil.Swap(t, &volsyncNow, func() time.Time { return time.Unix(1_700_000_000, 0) })
	testutil.Swap(t, &migrationSleepFn, func(context.Context, time.Duration) error {
		return context.DeadlineExceeded
	})
	testutil.Swap(t, &commandOutputFn, harness.commandOutput)
	testutil.Swap(t, &commandRunFn, harness.commandRun)
	testutil.Swap(t, &commandCombinedOutputFn, harness.commandCombinedOutput)
	testutil.Swap(t, &fluxRunFn, func(args ...string) error { return nil })
	return harness
}

func (h *migrateRunHarness) commandOutput(name string, args ...string) ([]byte, error) {
	h.t.Helper()
	require.Equal(h.t, "kubectl", name)
	command := strings.Join(args, " ")
	switch command {
	case "get replicationsource seerr --namespace media -o json":
		h.sourceReads++
		if h.sourceReads == 1 {
			return []byte(fmt.Sprintf(`{"spec":{"sourcePVC":"seerr","trigger":{"schedule":%q}},"status":{"latestMoverStatus":{"result":"Successful"}}}`, h.originalSchedule)), nil
		}
		return []byte(`{"spec":{"sourcePVC":"seerr","trigger":{"manual":"migrate-1700000000"}},"status":{"lastManualSync":"migrate-1700000000","latestMoverStatus":{"result":"Successful"}}}`), nil
	case "get kustomization seerr --namespace media -o json":
		return []byte(`{"spec":{"postBuild":{"substitute":{"VOLSYNC_STORAGECLASS":"scale-nvmeof","VOLSYNC_SNAPSHOTCLASS":"scale-snapshot"}}}}`), nil
	case "get pvc seerr --namespace media -o json":
		h.pvcReads++
		if h.pvcReads == 1 {
			return []byte(`{"spec":{"storageClassName":"ceph-block","dataSourceRef":{"apiGroup":"volsync.backube","kind":"ReplicationDestination","name":"seerr-dst"},"resources":{"requests":{"storage":"10Gi"}}},"status":{"phase":"Bound"}}`), nil
		}
		if h.migratedPVCFn != nil {
			return h.migratedPVCFn()
		}
		return []byte(`{"spec":{"storageClassName":"scale-nvmeof","volumeName":"pvc-new"},"status":{"phase":"Bound"}}`), nil
	case "get pods --namespace media -o json":
		h.podReads++
		if h.podsFn != nil {
			return h.podsFn(), nil
		}
		if h.podReads == 1 {
			return migrationConsumerPodJSON("seerr-0"), nil
		}
		return []byte(`{"items":[]}`), nil
	case "get statefulset seerr --namespace media -o json":
		return []byte(`{"spec":{"replicas":2},"status":{"readyReplicas":2}}`), nil
	case "get jobs --namespace media --selector app.kubernetes.io/name=seerr -o json",
		"get jobs --namespace media -o json":
		return []byte(`{"items":[]}`), nil
	case "get replicationdestination seerr-dst --namespace media --ignore-not-found -o name",
		"get pvc seerr --namespace media --ignore-not-found -o name":
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected kubectl output command: %s", command)
	}
}

func (h *migrateRunHarness) commandRun(name string, args ...string) error {
	h.t.Helper()
	require.Equal(h.t, "kubectl", name)
	call := strings.Join(append([]string{name}, args...), " ")
	h.runCalls = append(h.runCalls, call)
	if strings.HasPrefix(call, "kubectl delete replicationdestination seerr-dst ") {
		h.destinationDeleted = true
	}
	return nil
}

func (h *migrateRunHarness) commandCombinedOutput(name string, args ...string) ([]byte, error) {
	h.t.Helper()
	require.Equal(h.t, "kubectl", name)
	h.combinedCalls = append(h.combinedCalls, strings.Join(append([]string{name}, args...), " "))
	return nil, nil
}

func migrateRunOptions() migrateOptions {
	options := testMigrateOptions()
	options.Timeout = time.Minute
	options.Yes = true
	return options
}

func migrationConsumerPodJSON(name string) []byte {
	return []byte(fmt.Sprintf(`{"items":[{"metadata":{"name":%q,"ownerReferences":[{"kind":"StatefulSet","name":"seerr"}]},"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"seerr"}}]}}]}`, name))
}

func indexMigrationCall(calls []string, target string) int {
	for index, call := range calls {
		if call == target {
			return index
		}
	}
	return -1
}
