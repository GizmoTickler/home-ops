package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func fluxTestKustomization(namespace, name, ready, message string, dependencies ...fluxDependencyRef) fluxKustomization {
	var item fluxKustomization
	item.Metadata.Namespace = namespace
	item.Metadata.Name = name
	item.Spec.DependsOn = dependencies
	item.Spec.TargetNamespace = namespace
	item.Status.LastAppliedRevision = "main@sha1:abc"
	item.Status.Conditions = []conditionJSON{{Type: "Ready", Status: ready, Reason: "TestReason", Message: message}}
	return item
}

func fluxTestRelease(namespace, name, owner, ready, message string) fluxHelmRelease {
	var release fluxHelmRelease
	release.Metadata.Namespace = namespace
	release.Metadata.Name = name
	release.Metadata.Labels = map[string]string{
		"kustomize.toolkit.fluxcd.io/name":      owner,
		"kustomize.toolkit.fluxcd.io/namespace": "flux-system",
	}
	release.Status.LastAppliedRevision = "1.2.3"
	release.Status.Conditions = []conditionJSON{{Type: "Ready", Status: ready, Reason: "InstallFailed", Message: message}}
	return release
}

func TestBuildFluxTreeMissingDependency(t *testing.T) {
	root := fluxTestKustomization("flux-system", "app", "False", "waiting",
		fluxDependencyRef{Name: "missing", Namespace: "infra"})
	report, err := buildFluxTree([]fluxKustomization{root}, nil, "flux-system", "app")
	require.NoError(t, err)

	missing, ok := report.Nodes["infra/missing"]
	require.True(t, ok)
	assert.True(t, missing.Missing)
	assert.Equal(t, "dependency not found", missing.Message)
	assert.Equal(t, []string{"infra/missing", "flux-system/app"}, report.BlockingChain)
}

func TestBuildFluxTreeDetectsCycle(t *testing.T) {
	a := fluxTestKustomization("flux-system", "a", "False", "waiting", fluxDependencyRef{Name: "b"})
	b := fluxTestKustomization("flux-system", "b", "False", "waiting", fluxDependencyRef{Name: "a"})
	report, err := buildFluxTree([]fluxKustomization{a, b}, nil, "flux-system", "a")
	require.NoError(t, err)
	require.Len(t, report.Cycles, 1)
	assert.Equal(t, "flux-system/a -> flux-system/b -> flux-system/a", report.Cycles[0])

	rendered, err := renderFluxTree(report, "table", false)
	require.NoError(t, err)
	assert.Contains(t, rendered, "cycle detected")
}

func TestDeepestBlockingChainSelection(t *testing.T) {
	root := fluxTestKustomization("flux-system", "app", "False", "blocked",
		fluxDependencyRef{Name: "middle"}, fluxDependencyRef{Name: "shallow"})
	middle := fluxTestKustomization("flux-system", "middle", "False", "blocked", fluxDependencyRef{Name: "root-cause"})
	deep := fluxTestKustomization("flux-system", "root-cause", "False", "disk unavailable")
	shallow := fluxTestKustomization("flux-system", "shallow", "False", "other problem")

	report, err := buildFluxTree([]fluxKustomization{root, middle, deep, shallow}, nil, "flux-system", "app")
	require.NoError(t, err)
	assert.Equal(t, []string{"flux-system/root-cause", "flux-system/middle", "flux-system/app"}, report.BlockingChain)

	rendered, err := renderFluxTree(report, "table", false)
	require.NoError(t, err)
	assert.Contains(t, rendered, "app blocked by: root-cause (Ready=False: TestReason: disk unavailable) <- middle <- app")
}

func TestRenderFluxTreeSnapshot(t *testing.T) {
	root := fluxTestKustomization("flux-system", "app", "False", "dependency not ready", fluxDependencyRef{Name: "database"})
	root.Spec.TargetNamespace = "media"
	dependency := fluxTestKustomization("flux-system", "database", "True", "")
	release := fluxTestRelease("media", "app", "app", "False", "timed out")

	report, err := buildFluxTree([]fluxKustomization{root, dependency}, []fluxHelmRelease{release}, "flux-system", "app")
	require.NoError(t, err)
	rendered, err := renderFluxTree(report, "table", false)
	require.NoError(t, err)

	expected := `✗ Kustomization flux-system/app [Ready=False] revision=main@sha1:abc — TestReason: dependency not ready
├── ✓ Kustomization flux-system/database [Ready=True] revision=main@sha1:abc
└── ✗ HelmRelease media/app [Ready=False] revision=1.2.3 — InstallFailed: timed out

BLOCKING ANALYSIS: app blocked by: app (Ready=False: TestReason: dependency not ready)`
	assert.Equal(t, expected, rendered)
}

func TestRenderFluxTreeAllControlsReadyHelmReleases(t *testing.T) {
	root := fluxTestKustomization("flux-system", "app", "True", "")
	root.Spec.TargetNamespace = "media"
	release := fluxTestRelease("media", "app", "app", "True", "")
	report, err := buildFluxTree([]fluxKustomization{root}, []fluxHelmRelease{release}, "flux-system", "app")
	require.NoError(t, err)

	withoutReady, err := renderFluxTree(report, "table", false)
	require.NoError(t, err)
	assert.NotContains(t, withoutReady, "HelmRelease")
	withReady, err := renderFluxTree(report, "table", true)
	require.NoError(t, err)
	assert.Contains(t, withReady, "HelmRelease media/app")
}

func TestFluxTreeCommandListsKustomizationsWithReadOnlyFake(t *testing.T) {
	var calls [][]string
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		return []byte(`{"items":[{"metadata":{"namespace":"flux-system","name":"app"},"status":{"lastAppliedRevision":"main@sha1:abc","conditions":[{"type":"Ready","status":"True"}]}}]}`), nil
	})
	cmd := newFluxTreeCommand()
	var output strings.Builder
	cmd.SetOut(&output)
	require.NoError(t, cmd.Execute())
	assert.Contains(t, output.String(), "app")
	require.Len(t, calls, 1)
	assert.Equal(t, "get", calls[0][0])
	assert.Contains(t, calls[0], "-A")
	assert.NotContains(t, calls[0], "patch")
	assert.NotContains(t, calls[0], "apply")
}

func TestResolveFluxRootNamespace(t *testing.T) {
	items := []fluxKustomization{
		{Metadata: fluxTreeMetadata{Name: "radarr", Namespace: "downloads"}},
		{Metadata: fluxTreeMetadata{Name: "grafana", Namespace: "observability"}},
		{Metadata: fluxTreeMetadata{Name: "dupe", Namespace: "a"}},
		{Metadata: fluxTreeMetadata{Name: "dupe", Namespace: "b"}},
	}

	ns, err := resolveFluxRootNamespace(items, "", "radarr")
	require.NoError(t, err)
	assert.Equal(t, "downloads", ns)

	ns, err = resolveFluxRootNamespace(items, "explicit", "radarr")
	require.NoError(t, err)
	assert.Equal(t, "explicit", ns)

	_, err = resolveFluxRootNamespace(items, "", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in any namespace")

	_, err = resolveFluxRootNamespace(items, "", "dupe")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple namespaces")
}
