package volsync

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/common"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseKopiaSnapshots(t *testing.T) {
	output := `fusion@default:/data
2025-08-20 11:44:44 EDT k5b070ce6951b490d1641ea00ecc2fb0b 190.3 MB (latest-1,weekly-1)
+ 2 identical snapshots until 2025-08-19 11:44:44 EDT

paperless@media:/data
2025-08-18 10:00:00 EDT abcd1234 10.0 GB (latest-1)
`

	snapshots, err := parseKopiaSnapshots(output, "")
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	assert.Equal(t, "fusion", snapshots[0].App)
	assert.Equal(t, "default", snapshots[0].Namespace)
	assert.Equal(t, 3, snapshots[0].Count)
	assert.Equal(t, "k5b070ce6951b490d1641ea00ecc2fb0b", snapshots[0].LatestID)
	assert.Equal(t, "190.3 MB", snapshots[0].Size)
	assert.Contains(t, snapshots[0].RetentionTags, "weekly-1")

	filtered, err := parseKopiaSnapshots(output, "paperless")
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "paperless", filtered[0].App)
}

func TestParseKopiaSnapshotsSupportsOtherTimezonesAndIDs(t *testing.T) {
	output := `paperless@media:/data
2025-08-18 10:00:00 UTC abcd1234 10.0 GB (latest-1)

radarr@downloads:/data
2025-08-18 22:15:00 PST deadbeef 2.1 GB (latest-1,hourly-1)
+ 1 identical snapshots until 2025-08-18 21:15:00 PST
`

	snapshots, err := parseKopiaSnapshots(output, "")
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	assert.Equal(t, "abcd1234", snapshots[0].LatestID)
	assert.Equal(t, "UTC", snapshots[0].LatestTime[len(snapshots[0].LatestTime)-3:])
	assert.Equal(t, 2, snapshots[1].Count)
	assert.Equal(t, "deadbeef", snapshots[1].LatestID)
	assert.Contains(t, snapshots[1].RetentionTags, "hourly-1")
}

func TestSnapshotIDFromSelection(t *testing.T) {
	assert.Equal(t, "abcd1234", snapshotIDFromSelection("2025-08-18 10:00:00 UTC (abcd1234)"))
	assert.Equal(t, "42", snapshotIDFromSelection("42"))
	assert.Equal(t, "weird (value", snapshotIDFromSelection("weird (value"))
}

func TestParseReplicationSourcesOutput(t *testing.T) {
	sources, err := parseReplicationSourcesOutput("default app-a\nmedia app-b\n")
	require.NoError(t, err)
	assert.Equal(t, []ReplicationSource{
		{Namespace: "default", Name: "app-a"},
		{Namespace: "media", Name: "app-b"},
	}, sources)

	_, err = parseReplicationSourcesOutput("broken-line\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected at least 2 fields")
}

func TestDisplaySnapshotsOutput(t *testing.T) {
	snapshots := []AppSnapshot{{
		App:           "fusion",
		Namespace:     "default",
		Count:         2,
		LatestTime:    "2025-08-20 11:44:44 EDT",
		LatestID:      "abcd",
		Size:          "10 MB",
		RetentionTags: "latest-1",
		AllSnapshots:  []string{"2025-08-20 11:44:44 EDT (abcd)"},
	}}

	stdout, _, err := testutil.CaptureOutput(func() {
		displaySnapshotsTable(snapshots, common.NewColorLogger())
		displaySnapshotsJSON(snapshots)
		displaySnapshotsYAML(snapshots)
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "fusion")
	assert.Contains(t, stdout, "Total applications: 1")
	assert.Contains(t, stdout, "\"app\": \"fusion\"")
	assert.Contains(t, stdout, "latest_id: abcd")
}

func TestFindKopiaPod(t *testing.T) {
	oldOutput := kubectlOutputFn
	t.Cleanup(func() {
		kubectlOutputFn = oldOutput
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return []byte("kopia-0"), nil
	}

	pod, err := findKopiaPod()
	require.NoError(t, err)
	assert.Equal(t, "kopia-0", pod)
}

func TestFindKopiaPodEmpty(t *testing.T) {
	oldOutput := kubectlOutputFn
	t.Cleanup(func() {
		kubectlOutputFn = oldOutput
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return []byte(""), nil
	}

	_, err := findKopiaPod()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no kopia pod found")
}

func TestFindKopiaPodFallbackBinaryPath(t *testing.T) {
	scriptDir := t.TempDir()
	kubectlPath := filepath.Join(scriptDir, "kubectl")
	require.NoError(t, os.WriteFile(kubectlPath, []byte("#!/bin/sh\nprintf kopia-0\n"), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pod, err := findKopiaPod()
	require.NoError(t, err)
	assert.Equal(t, "kopia-0", pod)
}

func TestDetectController(t *testing.T) {
	oldRun := kubectlRunFn
	t.Cleanup(func() {
		kubectlRunFn = oldRun
	})

	kubectlRunFn = func(args ...string) error {
		switch {
		case len(args) == 3 && args[0] == "get" && args[1] == "namespace" && args[2] == "media":
			return nil
		case len(args) >= 5 && args[0] == "--namespace" && args[1] == "media" && args[2] == "get" && args[3] == "deployment":
			return fmt.Errorf("not found")
		case len(args) >= 5 && args[0] == "--namespace" && args[1] == "media" && args[2] == "get" && args[3] == "statefulset":
			return nil
		default:
			return fmt.Errorf("unexpected args: %v", args)
		}
	}

	controller, err := detectController("media", "paperless")
	require.NoError(t, err)
	assert.Equal(t, "statefulset", controller)
}

func TestDetectControllerFallback(t *testing.T) {
	oldRun := kubectlRunFn
	t.Cleanup(func() {
		kubectlRunFn = oldRun
	})

	kubectlRunFn = func(args ...string) error {
		if len(args) == 3 && args[0] == "get" && args[1] == "namespace" && args[2] == "media" {
			return nil
		}
		return fmt.Errorf("not found")
	}

	controller, err := detectController("media", "paperless")
	require.Error(t, err)
	assert.Equal(t, "deployment", controller)
	assert.Contains(t, err.Error(), "defaulting to deployment")
}

func TestParseKopiaSnapshotsPicksNewestAsLatest(t *testing.T) {
	// kopia lists each source oldest-first; "latest" must be the newest
	// line (the one kopia tags latest-1), not the first.
	output := `radarr@downloads:/data
2025-12-31 18:05:35 UTC oldannual 27.3 MB (monthly-7,annual-2)
2026-05-31 18:05:35 UTC oldmonthly 27.9 MB (monthly-1)
2026-06-12 06:00:12 UTC newest123 28.1 MB (latest-1,hourly-1,daily-1)
+ 30 identical snapshots until 2026-06-11 06:00:12 UTC
`

	snapshots, err := parseKopiaSnapshots(output, "")
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, "2026-06-12 06:00:12 UTC", snapshots[0].LatestTime)
	assert.Equal(t, "newest123", snapshots[0].LatestID)
	assert.Equal(t, "28.1 MB", snapshots[0].Size)
	assert.Contains(t, snapshots[0].RetentionTags, "latest-1")
	assert.Equal(t, 33, snapshots[0].Count)
}

func TestParseKopiaSnapshotsUsesIdenticalRunEnd(t *testing.T) {
	// When the data hasn't changed, kopia collapses the newest snapshots
	// into "+ N identical snapshots until <ts>" after the run's first line;
	// LATEST must show the end of that run, not its start.
	output := `actual@self-hosted:/data
2026-06-06 19:00:52 EDT ka085 249 KB (daily-4..7,weekly-2)
+ 3 identical snapshots until 2026-06-09 19:00:46 EDT
2026-06-10 19:01:04 EDT k5c88 249 KB (latest-1..10,hourly-1..24)
+ 24 identical snapshots until 2026-06-12 14:01:09 EDT
`

	snapshots, err := parseKopiaSnapshots(output, "")
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, "2026-06-12 14:01:09 EDT", snapshots[0].LatestTime)
	assert.Equal(t, "k5c88", snapshots[0].LatestID, "the run's snapshot ID still identifies the content")
	assert.Contains(t, snapshots[0].RetentionTags, "latest-1")
	assert.Equal(t, 29, snapshots[0].Count)
}
