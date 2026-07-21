package volsync

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

func fakeVolsyncKubectl(t *testing.T, byKind map[string]string) func(name string, args ...string) ([]byte, error) {
	t.Helper()
	fn := func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "kubectl", name)
		kind := ""
		for i, a := range args {
			if a == "get" && i+1 < len(args) {
				kind = args[i+1]
				break
			}
		}
		if body, ok := byKind[kind]; ok {
			return []byte(body), nil
		}
		return []byte(`{"items":[]}`), nil
	}
	testutil.Swap(t, &commandOutputCtxFn, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return fn(name, args...)
	})
	return fn
}

func TestBuildVolsyncStatusReportClassifiesFreshnessAndFailures(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	testutil.Swap(t, &volsyncNow, func() time.Time { return now })

	fresh := now.Add(-1 * time.Hour).Format(time.RFC3339)
	stale := now.Add(-30 * time.Hour).Format(time.RFC3339)
	sources := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"},"spec":{"sourcePVC":"radarr"},"status":{"lastSyncTime":"` + fresh + `","latestMoverStatus":{"result":"Successful"}}},
	  {"metadata":{"name":"sonarr","namespace":"downloads"},"spec":{"sourcePVC":"sonarr"},"status":{"lastSyncTime":"` + stale + `","latestMoverStatus":{"result":"Successful"}}},
	  {"metadata":{"name":"lidarr","namespace":"downloads"},"spec":{"sourcePVC":"lidarr"},"status":{"latestMoverStatus":{"result":"Successful"}}},
	  {"metadata":{"name":"bazarr","namespace":"downloads"},"spec":{"sourcePVC":"bazarr"},"status":{"lastSyncTime":"` + fresh + `","latestMoverStatus":{"result":"Failed"}}}
	]}`
	pvcs := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"},"spec":{"storageClassName":"scale-nvmeof"}},
	  {"metadata":{"name":"sonarr","namespace":"downloads"}},
	  {"metadata":{"name":"lidarr","namespace":"downloads"}},
	  {"metadata":{"name":"bazarr","namespace":"downloads"}}
	]}`
	testutil.Swap(t, &commandOutputFn, fakeVolsyncKubectl(t, map[string]string{
		"replicationsources": sources,
		"pvc":                pvcs,
	}))

	r := buildVolsyncStatusReport("", 24*time.Hour)
	statuses := map[string]volsyncStatus{}
	classes := map[string]string{}
	for _, item := range r.Sources {
		statuses[item.Namespace+"/"+item.App] = item.Status
		classes[item.Namespace+"/"+item.App] = item.StorageClass
	}
	assert.Equal(t, volsyncPass, statuses["downloads/radarr"])
	assert.Equal(t, "scale-nvmeof", classes["downloads/radarr"])
	assert.Equal(t, volsyncWarn, statuses["downloads/sonarr"])
	assert.Equal(t, volsyncFail, statuses["downloads/lidarr"])
	assert.Equal(t, volsyncFail, statuses["downloads/bazarr"])
	assert.True(t, r.hasFail())
}

func TestBuildVolsyncStatusReportFindsPVCsWithoutReplicationSourceOnlyInProtectedNamespaces(t *testing.T) {
	sources := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"},"spec":{"sourcePVC":"radarr"},"status":{"lastSyncTime":"2026-07-13T11:00:00Z","latestMoverStatus":{"result":"Successful"}}}
	]}`
	pvcs := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"}},
	  {"metadata":{"name":"unprotected","namespace":"downloads"}},
	  {"metadata":{"name":"ignored","namespace":"default"}}
	]}`
	testutil.Swap(t, &commandOutputFn, fakeVolsyncKubectl(t, map[string]string{
		"replicationsources": sources,
		"pvc":                pvcs,
	}))

	r := buildVolsyncStatusReport("", 24*time.Hour)
	require.Len(t, r.MissingBackups, 1)
	assert.Equal(t, "downloads", r.MissingBackups[0].Namespace)
	assert.Equal(t, "unprotected", r.MissingBackups[0].PVC)
	assert.Equal(t, volsyncWarn, r.MissingBackups[0].Status)
	assert.False(t, r.hasFail())
}

func TestBuildVolsyncStatusReportExcludesVolsyncOwnedPVCsButWarnsOrdinaryUnprotectedPVCs(t *testing.T) {
	sources := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"},"spec":{"sourcePVC":"radarr"},"status":{"lastSyncTime":"2026-07-13T11:00:00Z","latestMoverStatus":{"result":"Successful"}}}
	]}`
	pvcs := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"}},
	  {"metadata":{"name":"volsync-radarr-src","namespace":"downloads","ownerReferences":[{"apiVersion":"volsync.backube/v1alpha1","kind":"ReplicationSource","name":"radarr"}]}},
	  {"metadata":{"name":"volsync-src-radarr-cache","namespace":"downloads","labels":{"app.kubernetes.io/created-by":"volsync"}}},
	  {"metadata":{"name":"volsync-radarr-cache","namespace":"downloads"}},
	  {"metadata":{"name":"radarr-cache","namespace":"downloads"}}
	]}`
	testutil.Swap(t, &commandOutputFn, fakeVolsyncKubectl(t, map[string]string{
		"replicationsources": sources,
		"pvc":                pvcs,
	}))

	r := buildVolsyncStatusReport("", 24*time.Hour)
	require.Len(t, r.MissingBackups, 1)
	assert.Equal(t, "downloads", r.MissingBackups[0].Namespace)
	assert.Equal(t, "radarr-cache", r.MissingBackups[0].PVC)
	assert.Equal(t, volsyncWarn, r.MissingBackups[0].Status)
}

func TestBuildVolsyncStatusReportExcludesPodOwnedEphemeralPVCsButWarnsUnownedPVCs(t *testing.T) {
	sources := `{"items":[
	  {"metadata":{"name":"nzbget","namespace":"downloads"},"spec":{"sourcePVC":"nzbget"},"status":{"lastSyncTime":"2026-07-13T11:00:00Z","latestMoverStatus":{"result":"Successful"}}}
	]}`
	pvcs := `{"items":[
	  {"metadata":{"name":"nzbget","namespace":"downloads"}},
	  {"metadata":{"name":"nzbget-abc123-incomplete","namespace":"downloads","ownerReferences":[{"apiVersion":"v1","kind":"Pod","name":"nzbget-abc123"}]}},
	  {"metadata":{"name":"ordinary-unowned","namespace":"downloads"}}
	]}`
	testutil.Swap(t, &commandOutputFn, fakeVolsyncKubectl(t, map[string]string{
		"replicationsources": sources,
		"pvc":                pvcs,
	}))

	r := buildVolsyncStatusReport("", 24*time.Hour)
	require.Len(t, r.MissingBackups, 1)
	assert.Equal(t, "ordinary-unowned", r.MissingBackups[0].PVC)
	assert.Equal(t, volsyncWarn, r.MissingBackups[0].Status)
}

func TestVolsyncStatusNamespaceScoping(t *testing.T) {
	var calls []string
	output := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte(`{"items":[]}`), nil
	}
	testutil.Swap(t, &commandOutputFn, output)
	testutil.Swap(t, &commandOutputCtxFn, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return output(name, args...)
	})
	_ = buildVolsyncStatusReport("downloads", 24*time.Hour)
	for _, c := range calls {
		assert.Contains(t, c, "--namespace downloads")
		assert.NotContains(t, c, "-A")
	}
}

func TestRenderVolsyncStatusJSON(t *testing.T) {
	r := volsyncStatusReport{
		Summary: volsyncStatusSummary{Pass: 1, Warn: 1, Fail: 1},
		Sources: []volsyncSourceStatus{
			{App: "radarr", Namespace: "downloads", Status: volsyncPass},
		},
		MissingBackups: []volsyncMissingBackup{
			{PVC: "unprotected", Namespace: "downloads", Status: volsyncWarn},
		},
	}
	out, err := renderVolsyncStatusReport(r, "json")
	require.NoError(t, err)
	var decoded volsyncStatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Len(t, decoded.Sources, 1)
	require.Len(t, decoded.MissingBackups, 1)
	assert.Equal(t, volsyncWarn, decoded.MissingBackups[0].Status)
}

func TestRunVolsyncStatusFailsOnFailedSync(t *testing.T) {
	sources := `{"items":[
	  {"metadata":{"name":"radarr","namespace":"downloads"},"spec":{"sourcePVC":"radarr"},"status":{"lastSyncTime":"2026-07-13T11:00:00Z","latestMoverStatus":{"result":"Failed"}}}
	]}`
	testutil.Swap(t, &commandOutputFn, fakeVolsyncKubectl(t, map[string]string{"replicationsources": sources}))

	var buf strings.Builder
	err := runVolsyncStatus("", "json", 24*time.Hour, &buf)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "Failed")
}

func TestRunVolsyncStatusContextThreadsCallerContext(t *testing.T) {
	type volsyncStatusContextKey struct{}
	key := volsyncStatusContextKey{}
	ctx := context.WithValue(context.Background(), key, "volsync-status")
	var sawContext bool
	testutil.Swap(t, &commandOutputCtxFn, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		sawContext = sawContext || ctx.Value(key) == "volsync-status"
		return []byte(`{"items":[]}`), nil
	})

	var buf strings.Builder
	err := runVolsyncStatusContext(ctx, "", "table", 24*time.Hour, &buf)

	require.NoError(t, err)
	assert.True(t, sawContext)
}
