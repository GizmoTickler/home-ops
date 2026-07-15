package kubernetes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

const upgradePlanFixture = `# keep this header
apiVersion: upgrade.cattle.io/v1
kind: Plan
metadata:
  name: kubeadm-control-plane # keep name comment
spec:
  # target comment
  version: v1.36.1 # renovate: datasource=github-releases
  concurrency: 1
`

func writeUpgradePlanFixture(t *testing.T, root, relative, name string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	content := strings.Replace(upgradePlanFixture, "kubeadm-control-plane", name, 1)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestDiscoverKubeadmPlanSingleAmbiguousAndNone(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		root := t.TempDir()
		path := writeUpgradePlanFixture(t, root, "kubernetes/custom/release.yaml", "kubeadm-workers")
		candidate, err := discoverKubeadmPlan(root, "")
		require.NoError(t, err)
		assert.Equal(t, path, candidate.Path)
		assert.Equal(t, "v1.36.1", candidate.Version)
		assert.Equal(t, 8, candidate.Line)
	})

	t.Run("ambiguous lists candidates", func(t *testing.T) {
		root := t.TempDir()
		writeUpgradePlanFixture(t, root, "kubernetes/a/plan.yaml", "kubeadm-a")
		writeUpgradePlanFixture(t, root, "kubernetes/b/other.yaml", "kubeadm-b")
		_, err := discoverKubeadmPlan(root, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--plan-file")
		assert.Contains(t, err.Error(), "kubernetes/a/plan.yaml")
		assert.Contains(t, err.Error(), "kubernetes/b/other.yaml")
	})

	t.Run("none", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "kubernetes"), 0o755))
		_, err := discoverKubeadmPlan(root, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no kubeadm")
	})
}

func TestDiscoverKubeadmPlanFileDisambiguates(t *testing.T) {
	root := t.TempDir()
	writeUpgradePlanFixture(t, root, "kubernetes/a/plan.yaml", "kubeadm-a")
	selected := writeUpgradePlanFixture(t, root, "kubernetes/b/plan.yaml", "kubeadm-b")
	candidate, err := discoverKubeadmPlan(root, "kubernetes/b/plan.yaml")
	require.NoError(t, err)
	assert.Equal(t, selected, candidate.Path)
}

func TestValidateUpgradePlanVersionMatrix(t *testing.T) {
	tests := []struct {
		name, target, current string
		allow                 bool
		wantErr, wantWarn     bool
	}{
		{"patch upgrade", "v1.36.2", "v1.36.1", false, false, false},
		{"same", "v1.36.1", "v1.36.1", false, false, false},
		{"one minor", "v1.37.0", "v1.36.1", false, false, false},
		{"minor jump", "v1.38.0", "v1.36.1", false, false, true},
		{"missing v", "1.36.2", "v1.36.1", false, true, false},
		{"prerelease", "v1.37.0-rc.1", "v1.36.1", false, true, false},
		{"downgrade refused", "v1.36.0", "v1.36.1", false, true, false},
		{"downgrade allowed", "v1.36.0", "v1.36.1", true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warning, err := validateUpgradePlanVersion(tc.target, tc.current, tc.allow)
			assert.Equal(t, tc.wantErr, err != nil)
			assert.Equal(t, tc.wantWarn, warning != "")
		})
	}
}

func TestEditUpgradePlanVersionPreservesEveryOtherByte(t *testing.T) {
	root := t.TempDir()
	path := writeUpgradePlanFixture(t, root, "kubernetes/app/plan.yaml", "kubeadm")
	candidate, err := discoverKubeadmPlan(root, "")
	require.NoError(t, err)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	edited, err := editUpgradePlanVersion(original, candidate, "v1.36.2")
	require.NoError(t, err)
	expected := strings.Replace(string(original), "version: v1.36.1", "version: v1.36.2", 1)
	assert.Equal(t, expected, string(edited))
}

func TestRenderUpgradePlanDiffIsUnifiedAndKeepsContext(t *testing.T) {
	before := []byte(upgradePlanFixture)
	after := []byte(strings.Replace(upgradePlanFixture, "v1.36.1", "v1.36.2", 1))
	diff := renderUpgradePlanDiff("kubernetes/app/plan.yaml", before, after, 8)
	assert.Contains(t, diff, "--- a/kubernetes/app/plan.yaml")
	assert.Contains(t, diff, "+++ b/kubernetes/app/plan.yaml")
	assert.Contains(t, diff, "@@ -")
	assert.Contains(t, diff, "-  version: v1.36.1 # renovate")
	assert.Contains(t, diff, "+  version: v1.36.2 # renovate")
	assert.Contains(t, diff, "   concurrency: 1")
}

func TestRunUpgradePlanSetWriteAndCommitGates(t *testing.T) {
	live := func(context.Context) (upgradeStatusReport, error) {
		return upgradeStatusReport{APIServerVersion: "v1.36.2", Nodes: []upgradeStatusNode{{Name: "k8s-0", KubeletVersion: "v1.36.2"}}}, nil
	}
	testutil.Swap(t, &upgradePlanLiveContextFn, live)

	t.Run("dry run leaves file unchanged", func(t *testing.T) {
		root := t.TempDir()
		path := writeUpgradePlanFixture(t, root, "kubernetes/app/plan.yaml", "kubeadm")
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		var out strings.Builder
		require.NoError(t, runUpgradePlanSet(context.Background(), upgradePlanSetOptions{Version: "v1.36.2", RepoRoot: root}, &out))
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, content)
		assert.Contains(t, out.String(), "Dry run")
		assert.Contains(t, out.String(), "apiserver=v1.36.2")
	})

	t.Run("write changes file without commit", func(t *testing.T) {
		root := t.TempDir()
		path := writeUpgradePlanFixture(t, root, "kubernetes/app/plan.yaml", "kubeadm")
		called := false
		testutil.Swap(t, &upgradePlanGitRunFn, func(context.Context, ...string) error { called = true; return nil })
		require.NoError(t, runUpgradePlanSet(context.Background(), upgradePlanSetOptions{Version: "v1.36.2", RepoRoot: root, Write: true}, ioDiscard{}))
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(content), "version: v1.36.2")
		assert.False(t, called)
	})

	t.Run("commit requires write", func(t *testing.T) {
		err := runUpgradePlanSet(context.Background(), upgradePlanSetOptions{Version: "v1.36.2", Commit: true}, ioDiscard{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--commit requires --write")
	})

	t.Run("commit is scoped and never pushes", func(t *testing.T) {
		root := t.TempDir()
		writeUpgradePlanFixture(t, root, "kubernetes/app/plan.yaml", "kubeadm")
		var got []string
		testutil.Swap(t, &upgradePlanGitRunFn, func(_ context.Context, args ...string) error {
			got = append([]string(nil), args...)
			return nil
		})
		require.NoError(t, runUpgradePlanSet(context.Background(), upgradePlanSetOptions{Version: "v1.36.2", RepoRoot: root, Write: true, Commit: true}, ioDiscard{}))
		assert.Equal(t, []string{"-C", root, "commit", "--only", "-m", "feat(system-upgrade): bump kubeadm plan to v1.36.2", "--", "kubernetes/app/plan.yaml"}, got)
		assert.NotContains(t, got, "push")
	})
}

func TestRenderUpgradePlanLiveContextFailureIsNonFatal(t *testing.T) {
	testutil.Swap(t, &upgradePlanLiveContextFn, func(context.Context) (upgradeStatusReport, error) {
		return upgradeStatusReport{}, errors.New("cluster offline")
	})
	assert.Contains(t, renderUpgradePlanLiveContext(context.Background()), "non-fatal")
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
