package kubernetes

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const realisticHelmRelease = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: paperless
spec:
  values:
    controllers:
      paperless:
        annotations:
          reloader.stakater.com/auto: "true"
        containers:
          app:
            image:
              repository: ghcr.io/paperless-ngx/paperless-ngx
            probes:
              liveness: &probes # keep this anchor and comment byte-for-byte
                enabled: true
              readiness: *probes
            resources:
              requests:
                cpu: 500m # measured over seven days
                memory: 512Mi
              limits:
                cpu: 1
                memory: 1Gi # do not lower this limit
          exporter:
            resources:
              requests:
                cpu: 25m
                memory: 32Mi
`

func TestEditRightSizeContainerResourcesIsSurgicalWithAnchorsAndComments(t *testing.T) {
	want := strings.Replace(realisticHelmRelease, "cpu: 500m # measured over seven days", "cpu: 130m # measured over seven days", 1)
	want = strings.Replace(want, "memory: 512Mi", "memory: 144Mi", 1)

	updated, fields, err := editRightSizeContainerResources([]byte(realisticHelmRelease), rightSizeContainer{
		Container: "app", Verdict: rightSizeOver,
		SuggestedCPUCores: 0.13, SuggestedMemoryBytes: 144 * 1024 * 1024,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"requests.cpu=130m", "requests.memory=144Mi"}, fields)
	assert.Equal(t, want, string(updated))
	assert.Contains(t, string(updated), "liveness: &probes # keep this anchor and comment byte-for-byte")
	assert.Contains(t, string(updated), "exporter:\n            resources:\n              requests:\n                cpu: 25m")
}

func TestEditRightSizeContainerResourcesRaisesOnlyNearMemoryLimit(t *testing.T) {
	updated, fields, err := editRightSizeContainerResources([]byte(realisticHelmRelease), rightSizeContainer{
		Container: "app", Verdict: rightSizeUnder,
		SuggestedCPUCores: 0.6, SuggestedMemoryBytes: 640 * 1024 * 1024,
		MemoryLimitBytes: 1024 * 1024 * 1024, MemoryMaxBytes: 960 * 1024 * 1024,
	})
	require.NoError(t, err)
	assert.Contains(t, fields, "limits.memory=1200Mi")
	assert.Contains(t, string(updated), "memory: 1200Mi # do not lower this limit")
	assert.Contains(t, string(updated), "cpu: 1\n")

	notNear, _, err := editRightSizeContainerResources([]byte(realisticHelmRelease), rightSizeContainer{
		Container: "app", Verdict: rightSizeUnder,
		SuggestedCPUCores: 0.6, SuggestedMemoryBytes: 640 * 1024 * 1024,
		MemoryLimitBytes: 1024 * 1024 * 1024, MemoryMaxBytes: 800 * 1024 * 1024,
	})
	require.NoError(t, err)
	assert.Contains(t, string(notNear), "memory: 1Gi # do not lower this limit")
}

func TestFindRightSizeHelmReleaseMissingAndAmbiguous(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "kubernetes", "apps", "media"), 0o755))

	path, reason := findRightSizeHelmRelease(root, "media", "radarr")
	assert.Empty(t, path)
	assert.Contains(t, reason, "no HelmRelease")
	missingReport := rightSizeReport{Containers: []rightSizeContainer{{
		Namespace: "media", Workload: "radarr", Container: "app", Verdict: rightSizeOver,
	}}}
	var missingOutput bytes.Buffer
	require.NoError(t, applyRightSizeReportToGit(context.Background(), missingReport, rightSizeGitOptions{RepoRoot: root}, &missingOutput))
	assert.Contains(t, missingOutput.String(), "SKIP")
	assert.Contains(t, missingOutput.String(), "no HelmRelease")

	for _, directory := range []string{"movies", "requests"} {
		file := filepath.Join(root, "kubernetes", "apps", "media", directory, "app", "helmrelease.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte("apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: radarr\n"), 0o644))
	}
	path, reason = findRightSizeHelmRelease(root, "media", "radarr")
	assert.Empty(t, path)
	assert.Contains(t, reason, "ambiguous")
	var ambiguousOutput bytes.Buffer
	require.NoError(t, applyRightSizeReportToGit(context.Background(), missingReport, rightSizeGitOptions{RepoRoot: root}, &ambiguousOutput))
	assert.Contains(t, ambiguousOutput.String(), "SKIP")
	assert.Contains(t, ambiguousOutput.String(), "ambiguous")
}

func TestApplyRightSizeReportPreviewVersusWrite(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "kubernetes", "apps", "self-hosted", "paperless", "app", "helmrelease.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(realisticHelmRelease), 0o640))
	report := rightSizeReport{Containers: []rightSizeContainer{{
		Namespace: "self-hosted", Workload: "paperless", Container: "app", Verdict: rightSizeOver,
		SuggestedCPUCores: 0.13, SuggestedMemoryBytes: 144 * 1024 * 1024,
	}}}

	var preview bytes.Buffer
	require.NoError(t, applyRightSizeReportToGit(context.Background(), report, rightSizeGitOptions{RepoRoot: root}, &preview))
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, realisticHelmRelease, string(content))
	assert.Contains(t, preview.String(), "--- a/kubernetes/apps/self-hosted/paperless/app/helmrelease.yaml")
	assert.Contains(t, preview.String(), "-                cpu: 500m # measured over seven days")
	assert.Contains(t, preview.String(), "+                cpu: 130m # measured over seven days")
	assert.Contains(t, preview.String(), "PREVIEW SUMMARY")
	assert.Contains(t, preview.String(), "rerun with --write")

	var written bytes.Buffer
	require.NoError(t, applyRightSizeReportToGit(context.Background(), report, rightSizeGitOptions{RepoRoot: root, Write: true}, &written))
	content, err = os.ReadFile(file)
	require.NoError(t, err)
	assert.Contains(t, string(content), "cpu: 130m # measured over seven days")
	assert.Contains(t, string(content), "memory: 144Mi")
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	assert.Contains(t, written.String(), "WRITE SUMMARY")
}
