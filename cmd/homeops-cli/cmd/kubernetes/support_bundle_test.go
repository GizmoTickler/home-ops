package kubernetes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"
)

func TestDefaultSupportBundleCollectorsIncludeScaleCSI(t *testing.T) {
	collectors := defaultSupportBundleCollectors(false)
	byName := map[string]supportBundleCollector{}
	for _, collector := range collectors {
		byName[collector.Name] = collector
	}
	for _, name := range []string{
		"scale-csi-pods",
		"scale-csi-controller-logs",
		"scale-csi-node-logs",
		"scale-csi-helmrelease",
		"scale-csi-driver",
		"scale-csi-storageclasses",
		"scale-csi-volumeattachments",
		"scale-csi-events",
	} {
		assert.Contains(t, byName, name)
	}

	var calls [][]string
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{}`), nil
	})
	_, _ = collectSupportScaleCSIPods(context.Background())
	_, _ = collectSupportScaleCSIControllerLogs(context.Background())
	_, _ = collectSupportScaleCSINodeLogs(context.Background())
	_, _ = collectSupportScaleCSIHelmRelease(context.Background())
	_, _ = collectSupportScaleCSIDriver(context.Background())
	_, _ = collectSupportScaleCSIStorageClasses(context.Background())
	_, _ = collectSupportScaleCSIVolumeAttachments(context.Background())
	_, _ = collectSupportScaleCSIEvents(context.Background())

	joined := make([]string, 0, len(calls))
	for _, call := range calls {
		joined = append(joined, strings.Join(call, " "))
	}
	all := strings.Join(joined, "\n")
	assert.Contains(t, all, "--namespace "+constants.NSScaleCSI)
	assert.Contains(t, all, "deployment/"+constants.ScaleCSIController)
	assert.Contains(t, all, "daemonset/"+constants.ScaleCSINode)
	assert.Contains(t, all, csiDriverResource+" "+constants.ScaleCSIDriver)
	assert.Contains(t, all, constants.ScaleCSIStorageClassNVMeOF)
	assert.Contains(t, all, "volumeattachments.storage.k8s.io")
}

func TestCollectSupportScaleCSIVolumeAttachmentsFiltersOtherDrivers(t *testing.T) {
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"apiVersion":"storage.k8s.io/v1","kind":"VolumeAttachmentList","items":[
			{"metadata":{"name":"scale"},"spec":{"attacher":"csi.scale.io"},"status":{"attached":true}},
			{"metadata":{"name":"other"},"spec":{"attacher":"example.csi.io"},"status":{"attached":true}}
		]}`), nil
	})

	raw, err := collectSupportScaleCSIVolumeAttachments(context.Background())
	require.NoError(t, err)
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(raw, &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "scale", list.Items[0].Metadata.Name)
}

func TestSupportBundleCollectorIsolationManifestAndTarStructure(t *testing.T) {
	fixed := time.Date(2026, 7, 15, 12, 30, 0, 0, time.UTC)
	testutil.Swap(t, &supportBundleNowFn, func() time.Time { return fixed })
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{
			{Name: "healthy", Filename: "healthy.json", Collect: func(context.Context) ([]byte, error) {
				return []byte(`{"status":"ok"}`), nil
			}},
			{Name: "broken", Filename: "broken.json", Collect: func(context.Context) ([]byte, error) {
				return nil, errors.New("probe unavailable")
			}},
			{Name: "malformed", Filename: "malformed.json", Collect: func(context.Context) ([]byte, error) {
				return []byte(`not-json`), nil
			}},
		}
	})
	output := filepath.Join(t.TempDir(), "bundle.tar.gz")
	result, err := runSupportBundle(context.Background(), output, false)
	require.NoError(t, err)
	assert.Equal(t, output, result.Path)
	assert.Positive(t, result.Size)
	require.Len(t, result.Collectors, 3)
	assert.Equal(t, "OK", result.Collectors[0].Status)
	assert.Equal(t, "ERROR", result.Collectors[1].Status)
	assert.Equal(t, "ERROR", result.Collectors[2].Status)

	entries := readSupportBundleArchive(t, output)
	assert.Equal(t, []string{"broken.error", "healthy.json", "malformed.error", "manifest.json"}, sortedMapKeys(entries))
	assert.Equal(t, "probe unavailable\n", string(entries["broken.error"]))
	assert.Contains(t, string(entries["malformed.error"]), "invalid JSON")
	var manifest supportBundleManifest
	require.NoError(t, json.Unmarshal(entries["manifest.json"], &manifest))
	assert.Equal(t, fixed.Format(time.RFC3339), manifest.CreatedAt)
	assert.NotEmpty(t, manifest.CLIVersion)
	assert.NotEmpty(t, manifest.Versions["go"])
	assert.Equal(t, []string{"broken.error", "healthy.json", "malformed.error", "manifest.json"}, manifest.Contents)
	require.Len(t, manifest.Collectors, 3)
	assert.Equal(t, "broken.error", manifest.Collectors[1].File)
	assert.Equal(t, "ERROR", manifest.Collectors[1].Status)
}

func TestSupportBundleCollectorRecoversPanic(t *testing.T) {
	_, err := runSupportBundleCollector(context.Background(), supportBundleCollector{Collect: func(context.Context) ([]byte, error) {
		panic("probe panic")
	}})
	require.Error(t, err)
	assert.ErrorContains(t, err, "collector panic")
}

func TestSupportBundleSecurityScanRefusesArchive(t *testing.T) {
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{{
			Name: "leaky", Filename: "leaky.txt", Collect: func(context.Context) ([]byte, error) {
				return []byte("-----BEGIN PRIVATE KEY-----\nknown-secret\n-----END PRIVATE KEY-----\n"), nil
			},
		}}
	})
	output := filepath.Join(t.TempDir(), "must-not-exist.tar.gz")
	_, err := runSupportBundle(context.Background(), output, false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "security scan")
	_, statErr := os.Stat(output)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSupportBundleNoSSHSkipsCollectors(t *testing.T) {
	called := false
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{{
			Name: "ssh", Filename: "ssh.json", SSH: true, Collect: func(context.Context) ([]byte, error) {
				called = true
				return []byte(`{}`), nil
			},
		}}
	})
	output := filepath.Join(t.TempDir(), "bundle.tar.gz")
	result, err := runSupportBundle(context.Background(), output, true)
	require.NoError(t, err)
	assert.False(t, called)
	require.Len(t, result.Collectors, 1)
	assert.Equal(t, "SKIPPED", result.Collectors[0].Status)
	entries := readSupportBundleArchive(t, output)
	assert.Equal(t, []string{"manifest.json"}, sortedMapKeys(entries))
}

func TestCollectSupportEventsKeepsNewestTwoHundred(t *testing.T) {
	items := make([]json.RawMessage, 205)
	for index := range items {
		items[index] = json.RawMessage(fmt.Sprintf(`{"index":%d}`, index))
	}
	raw, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "EventList", "items": items})
	require.NoError(t, err)
	testutil.Swap(t, &kubectlOutputCtxFn, func(context.Context, ...string) ([]byte, error) { return raw, nil })
	collected, err := collectSupportEvents(context.Background())
	require.NoError(t, err)
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(collected, &list))
	assert.Len(t, list.Items, 200)
}

func TestSupportBundleDriftClassificationMatrix(t *testing.T) {
	tests := []struct {
		name                   string
		oldStatus, newStatus   string
		oldPresent, newPresent bool
		want                   string
	}{
		{name: "new fail", newStatus: "FAIL", newPresent: true, want: "NEW-FAIL"},
		{name: "warn worsens to fail", oldStatus: "WARN", newStatus: "FAIL", oldPresent: true, newPresent: true, want: "NEW-FAIL"},
		{name: "new warn", newStatus: "WARN", newPresent: true, want: "NEW-WARN"},
		{name: "pass worsens to warn", oldStatus: "PASS", newStatus: "WARN", oldPresent: true, newPresent: true, want: "NEW-WARN"},
		{name: "fail becomes pass", oldStatus: "FAIL", newStatus: "PASS", oldPresent: true, newPresent: true, want: "RESOLVED"},
		{name: "warn disappears", oldStatus: "WARN", oldPresent: true, want: "RESOLVED"},
		{name: "fail improves to warn", oldStatus: "FAIL", newStatus: "WARN", oldPresent: true, newPresent: true, want: "CHANGED"},
		{name: "neutral status changes", oldStatus: "PASS", newStatus: "OK", oldPresent: true, newPresent: true, want: "CHANGED"},
		{name: "unchanged", oldStatus: "WARN", newStatus: "WARN", oldPresent: true, newPresent: true},
		{name: "new healthy row", newStatus: "OK", newPresent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, classifyDriftStatus(test.oldStatus, test.oldPresent, test.newStatus, test.newPresent))
		})
	}
}

func TestSupportBundleDriftReportShapes(t *testing.T) {
	tests := []struct {
		name, collector, oldJSON, newJSON string
		wantClass, wantKey                string
		wantChanged                       int
	}{
		{
			name: "doctor", collector: "doctor",
			oldJSON:   `{"checks":[{"group":"pods","kind":"Pod","name":"media/plex","status":"PASS"}]}`,
			newJSON:   `{"checks":[{"group":"pods","kind":"Pod","name":"media/plex","status":"FAIL","detail":"CrashLoopBackOff"}]}`,
			wantClass: "NEW-FAIL", wantKey: "pods/Pod/media/plex",
		},
		{
			name: "net doctor", collector: "net-doctor",
			oldJSON:   `{"checks":[{"group":"GATEWAYS","kind":"Gateway","name":"network/internal","status":"WARN"}]}`,
			newJSON:   `{"checks":[]}`,
			wantClass: "RESOLVED", wantKey: "GATEWAYS/Gateway/network/internal",
		},
		{
			name: "storage", collector: "storage-report",
			oldJSON:   `{"orphaned_pvcs":[],"pv_issues":[],"volsync_coverage_gaps":[]}`,
			newJSON:   `{"orphaned_pvcs":[{"namespace":"media","name":"stale","storage_class":"scale-csi","size":"1Gi"}],"pv_issues":[],"volsync_coverage_gaps":[]}`,
			wantClass: "NEW-WARN", wantKey: "orphaned-pvc/media/stale",
		},
		{
			name: "certificates", collector: "certificates",
			oldJSON:   `{"before":{"checks":[{"node":"k8s-0","name":"apiserver","status":"WARN"}]}}`,
			newJSON:   `{"before":{"checks":[{"node":"k8s-0","name":"apiserver","status":"OK"}]}}`,
			wantClass: "RESOLVED", wantKey: "k8s-0/apiserver",
		},
		{
			name: "etcd", collector: "etcd-status",
			oldJSON:   `{"endpoints":[{"endpoint":"https://127.0.0.1:2379","healthy":true}],"backup":{"status":"OK"}}`,
			newJSON:   `{"endpoints":[{"endpoint":"https://127.0.0.1:2379","healthy":false,"error":"timeout"}],"backup":{"status":"OK"}}`,
			wantClass: "NEW-FAIL", wantKey: "endpoint/https://127.0.0.1:2379",
		},
		{
			name: "upgrade", collector: "upgrade-status",
			oldJSON:   `{"apiserver_version":"v1.35.1","plans":[],"nodes":[{"name":"k8s-0","status":"UpToDate","kubelet_version":"v1.35.1"}],"jobs":[],"skew":[]}`,
			newJSON:   `{"apiserver_version":"v1.35.1","plans":[],"nodes":[{"name":"k8s-0","status":"Pending","kubelet_version":"v1.35.1"}],"jobs":[],"skew":[]}`,
			wantClass: "NEW-WARN", wantKey: "node/k8s-0",
		},
		{
			name: "flatcar", collector: "flatcar-os-status",
			oldJSON:   `{"nodes":[{"node":"k8s-0","flatcar_version":"4300.0.0","kernel":"6.12.1","reboot_needed":false}]}`,
			newJSON:   `{"nodes":[{"node":"k8s-0","flatcar_version":"4310.0.0","kernel":"6.12.1","reboot_needed":true}]}`,
			wantClass: "NEW-WARN", wantKey: "node/k8s-0", wantChanged: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldRows, oldVersions, err := extractDriftRows(test.collector, []byte(test.oldJSON))
			require.NoError(t, err)
			newRows, newVersions, err := extractDriftRows(test.collector, []byte(test.newJSON))
			require.NoError(t, err)
			findings := append(diffDriftRows(test.collector, oldRows, newRows), diffVersionValues(test.collector, oldVersions, newVersions)...)
			require.NotEmpty(t, findings)
			assert.Equal(t, test.wantClass, findings[0].Classification)
			assert.Equal(t, test.wantKey, findings[0].Key)
			changed := 0
			for _, finding := range findings {
				if finding.Classification == "CHANGED" {
					changed++
				}
			}
			assert.Equal(t, test.wantChanged, changed)
		})
	}
}

func TestSupportBundleStorageDriftIncludesScaleCSIHealth(t *testing.T) {
	rows, _, err := extractDriftRows("storage-report", []byte(`{
		"orphaned_pvcs":[],"pv_issues":[],"volsync_coverage_gaps":[],
		"scale_csi_health":{
			"controller":{"ready":1,"desired":2,"status":"FAIL"},
			"node":{"ready":3,"desired":3,"status":"OK"},
			"status":"FAIL"
		},
		"scale_csi_metrics":{"available":true,"status":"WARN","values":{"scale_csi_orphan_volumes":1}}
	}`))
	require.NoError(t, err)
	byKey := map[string]supportBundleDriftRow{}
	for _, row := range rows {
		byKey[row.Key] = row
	}
	assert.Equal(t, "FAIL", byKey["scale-csi/controller"].Status)
	assert.Equal(t, "OK", byKey["scale-csi/node"].Status)
	assert.Equal(t, "WARN", byKey["scale-csi/metrics"].Status)
}

func TestSupportBundleVersionChanges(t *testing.T) {
	oldValues, err := extractVersionValues("kubectl-version", []byte(`{"clientVersion":{"gitVersion":"v1.35.0"},"serverVersion":{"gitVersion":"v1.35.1"}}`))
	require.NoError(t, err)
	newValues, err := extractVersionValues("kubectl-version", []byte(`{"clientVersion":{"gitVersion":"v1.36.0"},"serverVersion":{"gitVersion":"v1.35.1"}}`))
	require.NoError(t, err)
	findings := diffVersionValues("kubectl-version", oldValues, newValues)
	require.Len(t, findings, 1)
	assert.Equal(t, "client", findings[0].Key)
	assert.Equal(t, "v1.35.0", findings[0].OldValue)
	assert.Equal(t, "v1.36.0", findings[0].NewValue)
}

func TestSupportBundleDriftMissingCollectorsAndIncomparable(t *testing.T) {
	oldPath := writeSupportBundleFixture(t, map[string][]byte{
		"doctor.json": []byte(`{"checks":[]}`),
		"broken.json": []byte(`{"unexpected":true}`),
		"legacy.json": []byte(`{}`),
	}, []supportBundleCollectorResult{
		{Name: "doctor", File: "doctor.json", Status: "OK"},
		{Name: "net-doctor", File: "broken.json", Status: "OK"},
		{Name: "legacy", File: "legacy.json", Status: "OK"},
	})
	newPath := writeSupportBundleFixture(t, map[string][]byte{
		"doctor.json":      []byte(`{"checks":[]}`),
		"broken.json":      []byte(`{"checks":42}`),
		"cli-version.json": []byte(`{"version":"v2","go_version":"go1.25"}`),
	}, []supportBundleCollectorResult{
		{Name: "doctor", File: "doctor.json", Status: "OK"},
		{Name: "net-doctor", File: "broken.json", Status: "OK"},
		{Name: "cli-version", File: "cli-version.json", Status: "OK"},
	})
	oldBundle, err := loadSupportBundleArchive(oldPath)
	require.NoError(t, err)
	newBundle, err := loadSupportBundleArchive(newPath)
	require.NoError(t, err)
	report := compareSupportBundles(oldBundle, newBundle)
	states := map[string]string{}
	for _, collector := range report.Collectors {
		states[collector.Collector] = collector.State
		if collector.Collector == "net-doctor" {
			assert.Contains(t, collector.Detail, "incomparable:")
		}
	}
	assert.Equal(t, "ADDED", states["cli-version"])
	assert.Equal(t, "INCOMPARABLE", states["net-doctor"])
	assert.Equal(t, "REMOVED", states["legacy"])
}

func TestSupportBundleDiffValidatesArchiveBeforeCollecting(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "invalid.tar.gz")
	writeTarFixture(t, invalid, map[string][]byte{"doctor.json": []byte(`{"checks":[]}`)})
	called := false
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		called = true
		return nil
	})
	cmd := newSupportBundleCommand()
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--diff", invalid})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorContains(t, err, "manifest.json")
	assert.False(t, called)
}

func TestSupportBundleFailOnDriftAndJSONOutput(t *testing.T) {
	oldPath := writeSupportBundleFixture(t, map[string][]byte{
		"doctor.json": []byte(`{"checks":[{"group":"pods","kind":"Pod","name":"media/plex","status":"PASS"}]}`),
	}, []supportBundleCollectorResult{{Name: "doctor", File: "doctor.json", Status: "OK"}})
	testutil.Swap(t, &supportBundleCollectorsFn, func(bool) []supportBundleCollector {
		return []supportBundleCollector{{Name: "doctor", Filename: "doctor.json", Collect: func(context.Context) ([]byte, error) {
			return []byte(`{"checks":[{"group":"pods","kind":"Pod","name":"media/plex","status":"FAIL"}]}`), nil
		}}}
	})
	t.Chdir(t.TempDir())
	cmd := newSupportBundleCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--diff", oldPath, "--output", "json", "--fail-on-findings"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorContains(t, err, "1 new failing")
	jsonStart := strings.Index(output.String(), "{")
	require.NotEqual(t, -1, jsonStart)
	var result supportBundleCommandResult
	require.NoError(t, json.Unmarshal([]byte(output.String()[jsonStart:]), &result))
	require.NotNil(t, result.Drift)
	assert.Equal(t, 1, result.Drift.Summary.NewFail)
	require.Len(t, result.Drift.Findings, 1)
	assert.Equal(t, "NEW-FAIL", result.Drift.Findings[0].Classification)
}

func TestSupportBundleFailOnDriftIsHiddenDeprecatedAlias(t *testing.T) {
	cmd := newSupportBundleCommand()
	alias := cmd.Flags().Lookup("fail-on-drift")
	require.NotNil(t, alias)
	assert.True(t, alias.Hidden)
	assert.Contains(t, alias.Deprecated, "--fail-on-findings")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--fail-on-drift"})
	require.ErrorContains(t, cmd.Execute(), "--fail-on-findings requires --diff")
}

func writeSupportBundleFixture(t *testing.T, entries map[string][]byte, collectors []supportBundleCollectorResult) string {
	t.Helper()
	manifest := supportBundleManifest{CreatedAt: "2026-07-15T00:00:00Z", CLIVersion: "test", Collectors: collectors}
	for name := range entries {
		manifest.Contents = append(manifest.Contents, name)
	}
	manifest.Contents = append(manifest.Contents, "manifest.json")
	sort.Strings(manifest.Contents)
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	entries["manifest.json"] = raw
	archivePath := filepath.Join(t.TempDir(), "bundle.tar.gz")
	writeTarFixture(t, archivePath, entries)
	return archivePath
}

func writeTarFixture(t *testing.T, archivePath string, entries map[string][]byte) {
	t.Helper()
	sourceDir := t.TempDir()
	contents := make([]string, 0, len(entries))
	for name, data := range entries {
		require.NoError(t, os.WriteFile(filepath.Join(sourceDir, name), data, 0o600))
		contents = append(contents, name)
	}
	sort.Strings(contents)
	require.NoError(t, writeSupportBundleArchive(sourceDir, archivePath, contents))
}

func readSupportBundleArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	archive, err := os.Open(path) // #nosec G304 -- test opens its own temporary output path.
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()
	gzipReader, err := gzip.NewReader(archive)
	require.NoError(t, err)
	defer func() { require.NoError(t, gzipReader.Close()) }()
	tarReader := tar.NewReader(gzipReader)
	entries := map[string][]byte{}
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		require.NoError(t, nextErr)
		data, readErr := io.ReadAll(tarReader)
		require.NoError(t, readErr)
		entries[header.Name] = data
		assert.Equal(t, int64(0o600), header.Mode)
	}
	return entries
}

func sortedMapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
