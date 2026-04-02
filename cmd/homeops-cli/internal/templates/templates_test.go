package templates

import (
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/common"
	"homeops-cli/internal/metrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedTemplateHelpers(t *testing.T) {
	renderedVolsync, err := RenderVolsyncTemplate("replicationdestination.yaml.j2", map[string]string{
		"APP":                 "demo",
		"NS":                  "default",
		"ACCESS_MODES":        "[ReadWriteOnce]",
		"CACHE_ACCESS_MODES":  "[ReadWriteOnce]",
		"CACHE_CAPACITY":      "1Gi",
		"CACHE_STORAGE_CLASS": "fast",
		"CAPACITY":            "5Gi",
		"CLAIM":               "demo",
		"PUID":                "1000",
		"PGID":                "1000",
		"PREVIOUS":            "latest",
		"STORAGE_CLASS":       "ceph",
		"SNAPSHOT_CLASS":      "csi-cephfsplugin-snapclass",
	})
	require.NoError(t, err)
	assert.Contains(t, renderedVolsync, "name: demo-manual")
	assert.Contains(t, renderedVolsync, "namespace: default")

	rawVolsync, err := GetVolsyncTemplate("replicationdestination.yaml.j2")
	require.NoError(t, err)
	assert.Contains(t, rawVolsync, "{{ ENV.APP }}")

	rawTalos, err := GetTalosTemplate("talos/controlplane.yaml")
	require.NoError(t, err)
	assert.Contains(t, rawTalos, "machine:")

	bootstrapValues, err := GetBootstrapTemplate("values.yaml.gotmpl")
	require.NoError(t, err)
	assert.Contains(t, bootstrapValues, "readFile")

	bootstrapFile, err := GetBootstrapFile("resources.yaml")
	require.NoError(t, err)
	assert.Contains(t, bootstrapFile, "kind: Secret")

	brewfile, err := GetBrewfile()
	require.NoError(t, err)
	assert.Contains(t, brewfile, "brew")
}

func TestTemplateSubstitutionHelpers(t *testing.T) {
	loopTemplate := `{% for namespace in ["external-secrets", "flux-system", "network"] %}name: {{ namespace }}
{% endfor %}`
	expanded := expandNamespaceLoop(loopTemplate)
	assert.Contains(t, expanded, "name: external-secrets")
	assert.Contains(t, expanded, "name: flux-system")
	assert.Contains(t, expanded, "name: network")

	require.NoError(t, ValidateTemplateSubstitution("resources.yaml.j2", loopTemplate, expanded))
	require.Error(t, ValidateTemplateSubstitution("bad.j2", "", "{{ ENV.NAME }}"))
}

func TestRenderGoTemplateAndHelmfileValues(t *testing.T) {
	rootDir := t.TempDir()
	collector := metrics.NewPerformanceCollector()

	releaseDir := filepath.Join(rootDir, "kubernetes", "apps", "media", "seerr", "app")
	require.NoError(t, os.MkdirAll(releaseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(releaseDir, "helmrelease.yaml"), []byte("spec:\n  values:\n    enabled: true\n"), 0o644))

	rendered, err := RenderGoTemplate("values.yaml.gotmpl", rootDir, map[string]interface{}{}, collector)
	require.Error(t, err)
	assert.Empty(t, rendered)

	values, err := RenderHelmfileValues("seerr", rootDir, collector)
	require.NoError(t, err)
	assert.Contains(t, values, "enabled: true")
}

func TestTemplateRenderer(t *testing.T) {
	rootDir := t.TempDir()
	logger := common.NewColorLogger()
	renderer := NewTemplateRenderer(rootDir, logger, metrics.NewPerformanceCollector())

	gotmpl, err := renderer.RenderTemplate("demo.gotmpl", `{{ index .Values "name" }}`, nil, map[string]interface{}{"name": "demo"})
	require.NoError(t, err)
	assert.Equal(t, "demo", gotmpl)

	jinja, err := renderer.RenderTemplate("volsync/replicationdestination.yaml.j2", "", map[string]string{
		"APP":                 "demo",
		"NS":                  "default",
		"ACCESS_MODES":        "[ReadWriteOnce]",
		"CACHE_ACCESS_MODES":  "[ReadWriteOnce]",
		"CACHE_CAPACITY":      "1Gi",
		"CACHE_STORAGE_CLASS": "fast",
		"CAPACITY":            "5Gi",
		"CLAIM":               "demo",
		"PUID":                "1000",
		"PGID":                "1000",
		"PREVIOUS":            "latest",
		"STORAGE_CLASS":       "ceph",
		"SNAPSHOT_CLASS":      "snap",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, jinja, "demo-manual")

	replaced, err := renderer.RenderTemplate("plain.txt", "hello world", map[string]string{"NAME": "world"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello world", replaced)

	merged, err := renderer.RenderTalosConfigWithMerge("talos/controlplane.yaml", "talos/nodes/192.168.122.10.yaml", nil)
	require.NoError(t, err)
	assert.Contains(t, string(merged), "type: controlplane")
	assert.Contains(t, string(merged), "hostname: k8s-0")

	require.NoError(t, renderer.ValidateTemplate("ok.gotmpl", `{{ index .Values "name" }}`))
	require.Error(t, renderer.ValidateTemplate("bad.gotmpl", `{{ if }}`))

	err = renderer.ValidateTemplate("resources.yaml", `{% if true %}static{% endif %}`)
	require.NoError(t, err)
}

func TestBootstrapTemplateHelpers(t *testing.T) {
	rendered, err := RenderBootstrapTemplate("resources.yaml", map[string]string{
		"BOOTSTRAP_RESOURCES_NAMESPACE": "flux-system",
		"SECRET_DOMAIN":                 "example.com",
	})
	require.NoError(t, err)
	assert.Contains(t, rendered, "external-secrets")
	assert.Contains(t, rendered, "network")
	assert.Contains(t, rendered, "cloudflare-tunnel-id-secret")

	_, err = RenderBootstrapTemplate("missing.yaml", nil)
	require.Error(t, err)

	_, err = GetBootstrapTemplate("missing.yaml")
	require.Error(t, err)

	_, err = GetBootstrapFile("missing.yaml")
	require.Error(t, err)

	rawTalos, err := RenderTalosTemplate("talos/controlplane.yaml", map[string]string{
		"KUBERNETES_VERSION": "v1.30.0",
		"TALOS_VERSION":      "v1.9.0",
	})
	require.NoError(t, err)
	assert.Contains(t, rawTalos, "cluster:")
}
