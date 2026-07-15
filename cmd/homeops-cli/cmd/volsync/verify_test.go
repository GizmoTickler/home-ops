package volsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
	"homeops-cli/internal/testutil"
)

func verifyTestKopia() map[string]any {
	return map[string]any{
		"accessModes":           []any{"ReadWriteOnce"},
		"cacheAccessModes":      []any{"ReadWriteOnce"},
		"cacheCapacity":         "5Gi",
		"cacheStorageClassName": "openebs-hostpath",
		"capacity":              "source-value-must-not-win",
		"cleanupCachePVC":       false,
		"cleanupTempPVC":        false,
		"copyMethod":            "Snapshot",
		"destinationPVC":        "source-value-must-not-win",
		"moverSecurityContext": map[string]any{
			"runAsUser":  float64(1000),
			"runAsGroup": float64(1000),
			"fsGroup":    float64(1000),
		},
		"moverVolumes": []any{map[string]any{
			"mountPath": "repository",
			"volumeSource": map[string]any{
				"nfs": map[string]any{"path": "/mnt/flashstor/VolsyncKopia", "server": "192.168.120.10"},
			},
		}},
		"repository":              map[string]any{"repository": "paperless-volsync-secret", "credentialSecret": "kopia-creds"},
		"sourceIdentity":          map[string]any{"sourceName": "wrong-source", "sourceNamespace": "self-hosted"},
		"storageClassName":        "source-value-must-not-win",
		"volumeSnapshotClassName": "csi-ceph-blockpool",
		"futureCredentialField":   map[string]any{"secretName": "future-secret"},
	}
}

func verifyTestPVC() verifyPVC {
	var pvc verifyPVC
	pvc.Metadata.Name = "paperless-data"
	pvc.Spec.AccessModes = []string{"ReadWriteOnce"}
	pvc.Spec.StorageClassName = "ceph-block"
	pvc.Spec.VolumeMode = "Filesystem"
	pvc.Spec.Resources.Requests = map[string]string{"storage": "20Gi"}
	return pvc
}

func verifyTestConfig() verifyRestoreConfig {
	return verifyRestoreConfig{PVC: verifyTestPVC(), Kopia: verifyTestKopia()}
}

func decodeVerifyYAML(t *testing.T, manifest string) map[string]any {
	t.Helper()
	var object map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(manifest), &object))
	return object
}

func TestBuildVerifyObjectsAreNamedLabeledAndOwnerless(t *testing.T) {
	const name = "volsync-verify-paperless-1721000000"
	pvcYAML, err := buildVerifyPVC(name, "self-hosted", "paperless", verifyTestPVC())
	require.NoError(t, err)
	destinationYAML, err := buildVerifyDestination(name, "self-hosted", "paperless", "verify-1", verifyTestConfig())
	require.NoError(t, err)
	podYAML, err := buildVerifyCheckPod(name, "self-hosted", "paperless")
	require.NoError(t, err)

	for _, manifest := range []string{pvcYAML, destinationYAML, podYAML} {
		object := decodeVerifyYAML(t, manifest)
		metadata := object["metadata"].(map[string]any)
		assert.Equal(t, name, metadata["name"])
		assert.Equal(t, "self-hosted", metadata["namespace"])
		assert.NotContains(t, metadata, "ownerReferences")
		labels := metadata["labels"].(map[string]any)
		assert.Equal(t, "paperless", labels[verifyLabelKey])
		assert.Equal(t, name, labels[verifyRunLabelKey])
	}

	pvcSpec := decodeVerifyYAML(t, pvcYAML)["spec"].(map[string]any)
	assert.Equal(t, "ceph-block", pvcSpec["storageClassName"])
	requests := pvcSpec["resources"].(map[string]any)["requests"].(map[string]any)
	assert.Equal(t, "20Gi", requests["storage"])

	destinationSpec := decodeVerifyYAML(t, destinationYAML)["spec"].(map[string]any)
	assert.Equal(t, "verify-1", destinationSpec["trigger"].(map[string]any)["manual"])
	kopia := destinationSpec["kopia"].(map[string]any)
	assert.Equal(t, name, kopia["destinationPVC"])
	assert.Equal(t, "Direct", kopia["copyMethod"])
	assert.Equal(t, "20Gi", kopia["capacity"])
	assert.Equal(t, "ceph-block", kopia["storageClassName"])
	assert.Equal(t, true, kopia["cleanupCachePVC"])
	assert.Equal(t, true, kopia["cleanupTempPVC"])
	assert.EqualValues(t, 0, kopia["previous"])
	assert.Equal(t, "paperless", kopia["sourceIdentity"].(map[string]any)["sourceName"])
	assert.Equal(t, "self-hosted", kopia["sourceIdentity"].(map[string]any)["sourceNamespace"])
	assert.Equal(t, verifyTestKopia()["moverVolumes"], kopia["moverVolumes"])
	assert.Equal(t, verifyTestKopia()["repository"], kopia["repository"])
	securityContext := kopia["moverSecurityContext"].(map[string]any)
	assert.EqualValues(t, 1000, securityContext["runAsUser"])
	assert.EqualValues(t, 1000, securityContext["runAsGroup"])
	assert.EqualValues(t, 1000, securityContext["fsGroup"])
	assert.Equal(t, verifyTestKopia()["futureCredentialField"], kopia["futureCredentialField"])
}

func TestBuildVerifyRestoreConfigInheritsLiveReplicationSourceKopia(t *testing.T) {
	replicationSource := `{
		"apiVersion":"volsync.backube/v1alpha1",
		"kind":"ReplicationSource",
		"metadata":{"name":"paperless","namespace":"self-hosted"},
		"spec":{"sourcePVC":"paperless-data","kopia":{
			"repository":{"repository":"paperless-volsync-secret","credentialSecret":"kopia-creds"},
			"cacheCapacity":"5Gi",
			"cacheStorageClassName":"openebs-hostpath",
			"moverSecurityContext":{"runAsUser":1000,"runAsGroup":1000,"fsGroup":1000},
			"moverVolumes":[{"mountPath":"repository","volumeSource":{"nfs":{"path":"/mnt/flashstor/VolsyncKopia","server":"192.168.120.10"}}}]
		}}
	}`
	pvc, err := json.Marshal(verifyTestPVC())
	require.NoError(t, err)
	testutil.Swap(t, &verifyOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
		switch args[1] {
		case "replicationsource":
			return []byte(replicationSource), nil
		case "pvc":
			assert.Equal(t, "paperless-data", args[2])
			return pvc, nil
		default:
			return nil, errors.New("unexpected resource")
		}
	})

	config, err := buildVerifyRestoreConfig(context.Background(), "self-hosted", "paperless")
	require.NoError(t, err)
	assert.Equal(t, "paperless-volsync-secret", config.Kopia["repository"].(map[string]any)["repository"])
	assert.Equal(t, "/mnt/flashstor/VolsyncKopia", config.Kopia["moverVolumes"].([]any)[0].(map[string]any)["volumeSource"].(map[string]any)["nfs"].(map[string]any)["path"])
	assert.EqualValues(t, 1000, config.Kopia["moverSecurityContext"].(map[string]any)["runAsUser"])
}

func TestBuildVerifyRestoreConfigRejectsMissingReplicationSource(t *testing.T) {
	testutil.Swap(t, &verifyOutputFn, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New(`replicationsources.volsync.backube "missing" not found`)
	})
	_, err := buildVerifyRestoreConfig(context.Background(), "self-hosted", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReplicationSource self-hosted/missing not found; nothing to verify against")
}

func TestVerifyObjectNameIsDNSLengthBounded(t *testing.T) {
	name := verifyObjectName(strings.Repeat("a", 63), time.Unix(1721000000, 0))
	assert.LessOrEqual(t, len(name), 63)
	assert.True(t, strings.HasPrefix(name, "volsync-verify-"))
}

func TestWaitForVerifyDestination(t *testing.T) {
	t.Run("waits for matching successful manual trigger", func(t *testing.T) {
		calls := 0
		testutil.Swap(t, &verifyOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
			if args[1] == "jobs,pods" {
				return []byte(`{"items":[]}`), nil
			}
			calls++
			if calls == 1 {
				return []byte(`{"status":{"latestMoverStatus":{"result":""}}}`), nil
			}
			return []byte(`{"status":{"lastManualSync":"verify-1","lastSyncDuration":"12s","latestMoverStatus":{"result":"Successful","logs":"Processed 12.5 MiB"}}}`), nil
		})
		testutil.Swap(t, &verifySleepFn, func(context.Context, time.Duration) error { return nil })

		status, err := waitForVerifyDestination(context.Background(), "self-hosted", "verify", "verify-1", time.Minute)
		require.NoError(t, err)
		assert.Equal(t, 2, calls)
		assert.Equal(t, "12s", status.Status.LastSyncDuration)
	})

	t.Run("reports mover failure", func(t *testing.T) {
		testutil.Swap(t, &verifyOutputFn, func(context.Context, ...string) ([]byte, error) {
			return []byte(`{"status":{"latestMoverStatus":{"result":"Failed","logs":"repository corrupt"}}}`), nil
		})
		_, err := waitForVerifyDestination(context.Background(), "self-hosted", "verify", "verify-1", time.Minute)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repository corrupt")
	})

	t.Run("honors timeout", func(t *testing.T) {
		testutil.Swap(t, &verifyOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
			if args[1] == "jobs,pods" {
				return []byte(`{"items":[]}`), nil
			}
			return []byte(`{"status":{}}`), nil
		})
		testutil.Swap(t, &verifySleepFn, func(context.Context, time.Duration) error {
			return context.DeadlineExceeded
		})
		_, err := waitForVerifyDestination(context.Background(), "self-hosted", "verify", "verify-1", time.Second)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})

	t.Run("fails fast on mover job backoff with logs", func(t *testing.T) {
		testutil.Swap(t, &verifyOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
			switch args[0] {
			case "logs":
				return []byte("cannot access storage path: stat /mnt/repository: no such file or directory"), nil
			case "get":
				if args[1] == "replicationdestination" {
					return []byte(`{"status":{}}`), nil
				}
				return []byte(`{"items":[{"kind":"Job","metadata":{"name":"volsync-dst-verify","ownerReferences":[{"kind":"ReplicationDestination","name":"verify"}]},"spec":{"backoffLimit":2},"status":{"failed":2}}]}`), nil
			default:
				return nil, errors.New("unexpected command")
			}
		})
		testutil.Swap(t, &verifySleepFn, func(context.Context, time.Duration) error {
			t.Fatal("failure should be reported before sleeping")
			return nil
		})

		_, err := waitForVerifyDestination(context.Background(), "self-hosted", "verify", "verify-1", time.Minute)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackoffLimitReached")
		assert.Contains(t, err.Error(), "/mnt/repository")
	})
}

func TestVerifyCleanupOrderingContinuesAfterFailure(t *testing.T) {
	var calls []string
	testutil.Swap(t, &verifyRunFn, func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "replicationdestination" {
			return errors.New("synthetic cleanup failure")
		}
		return nil
	})
	op := verifyOperation{namespace: "self-hosted", name: "volsync-verify-paperless-1", logger: commonTestLogger()}
	op.cleanup()
	assert.Equal(t, []string{
		"delete pod volsync-verify-paperless-1 --namespace self-hosted --ignore-not-found --wait=true",
		"delete replicationdestination volsync-verify-paperless-1 --namespace self-hosted --ignore-not-found --wait=true",
		"delete pvc volsync-verify-paperless-1 --namespace self-hosted --ignore-not-found --wait=true",
	}, calls)
}

func commonTestLogger() *common.ColorLogger { return common.NewColorLogger() }

func TestRunVolsyncVerifyRefusesExistingSameAppRun(t *testing.T) {
	testutil.Swap(t, &verifyOutputFn, func(context.Context, ...string) ([]byte, error) {
		return []byte("persistentvolumeclaim/volsync-verify-paperless-1721000000\n"), nil
	})
	testutil.Swap(t, &verifyBuildRestoreConfigFn, func(context.Context, string, string) (verifyRestoreConfig, error) {
		t.Fatal("restore config must not load after in-progress refusal")
		return verifyRestoreConfig{}, nil
	})

	err := runVolsyncVerify(context.Background(), verifyOptions{Namespace: "self-hosted", App: "paperless", Timeout: time.Minute}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in progress")
}

func TestRunVolsyncVerifyCleansUpAfterRestoreFailure(t *testing.T) {
	testutil.Swap(t, &volsyncNow, func() time.Time { return time.Unix(1721000000, 0) })
	outputCalls := 0
	testutil.Swap(t, &verifyOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
		outputCalls++
		if args[0] == "get" && args[1] == "replicationdestination,pvc,pod" {
			return nil, nil
		}
		return []byte(`{"status":{"latestMoverStatus":{"result":"Failed","logs":"restore failed"}}}`), nil
	})
	testutil.Swap(t, &verifyBuildRestoreConfigFn, func(context.Context, string, string) (verifyRestoreConfig, error) {
		return verifyTestConfig(), nil
	})
	testutil.Swap(t, &confirmActionFn, func(string, bool) (bool, error) { return true, nil })
	var applied []string
	testutil.Swap(t, &verifyApplyYAMLFn, func(_ context.Context, manifest string) ([]byte, error) {
		applied = append(applied, manifest)
		return nil, nil
	})
	var cleanup []string
	testutil.Swap(t, &verifyRunFn, func(_ context.Context, args ...string) error {
		cleanup = append(cleanup, args[1])
		return nil
	})

	err := runVolsyncVerify(context.Background(), verifyOptions{Namespace: "self-hosted", App: "paperless", Timeout: time.Minute}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore failed")
	assert.Len(t, applied, 2)
	assert.Equal(t, []string{"pod", "replicationdestination", "pvc"}, cleanup)
	assert.GreaterOrEqual(t, outputCalls, 2)
}

func TestRestoredBytes(t *testing.T) {
	assert.Equal(t, "12.5 MiB", restoredBytes("restore complete: Processed 12.5 MiB in 2s"))
	assert.Equal(t, "12.5 MiB", restoredBytes("Processed 20 files, 12.5 MiB"))
	assert.Equal(t, "unknown", restoredBytes("no size in mover log"))
}

func TestBuildVerifyDestinationDropsSourceOnlyKopiaFields(t *testing.T) {
	config := verifyRestoreConfig{
		PVC: verifyPVC{},
		Kopia: map[string]any{
			"repository":  "radarr-volsync-secret",
			"parallelism": int64(2),
			"compression": "zstd-fastest",
			"retain":      map[string]any{"daily": int64(7)},
			"moverVolumes": []any{
				map[string]any{"mountPath": "repository"},
			},
		},
	}
	config.PVC.Spec.Resources.Requests = map[string]string{"storage": "5Gi"}

	manifest, err := buildVerifyDestination("scratch", "downloads", "radarr", "trig", config)
	require.NoError(t, err)
	assert.NotContains(t, manifest, "parallelism")
	assert.NotContains(t, manifest, "compression")
	assert.NotContains(t, manifest, "retain")
	assert.Contains(t, manifest, "moverVolumes")
	assert.Contains(t, manifest, "copyMethod: Direct")
}
