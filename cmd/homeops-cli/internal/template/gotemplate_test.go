package template

import (
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/metrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTemplateRenderer(rootDir string) *GoTemplateRenderer {
	return NewGoTemplateRenderer(rootDir, metrics.NewPerformanceCollector())
}

func TestGoTemplateRendererRenderTemplateAndHelpers(t *testing.T) {
	rootDir := t.TempDir()
	renderer := newTemplateRenderer(rootDir)

	configPath := filepath.Join(rootDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("name: demo\n"), 0o644))

	templateContent := `{{ upper "hello" }} {{ trim " world " }} {{ index (fromYaml (readFile "config.yaml")) "name" }} {{ exec "printf" (list "ok") }}`
	rendered, err := renderer.RenderTemplate(templateContent, TemplateData{
		RootDir: rootDir,
		Values:  map[string]interface{}{"name": "demo"},
	})
	require.NoError(t, err)
	assert.Equal(t, "HELLO world demo ok", rendered)

	assert.Equal(t, "  a\n\n  b", renderer.indentText(2, "a\n\nb"))
	assert.Equal(t, "unchanged", renderer.indentText(0, "unchanged"))
}

func TestGoTemplateRendererRenderFileValidateAndVariables(t *testing.T) {
	rootDir := t.TempDir()
	renderer := newTemplateRenderer(rootDir)

	templatePath := filepath.Join(rootDir, "values.gotmpl")
	require.NoError(t, os.WriteFile(templatePath, []byte(`{{ index .Values "name" }}`), 0o644))

	rendered, err := renderer.RenderFile(templatePath, TemplateData{
		RootDir: rootDir,
		Values:  map[string]interface{}{"name": "demo"},
	})
	require.NoError(t, err)
	assert.Equal(t, "demo", rendered)

	require.NoError(t, renderer.ValidateTemplate(`{{ index .Values "name" }}`))
	require.Error(t, renderer.ValidateTemplate(`{{ if }}`))

	variables, err := renderer.GetTemplateVariables("{{ .Values.name }} {{ .RootDir }}")
	require.NoError(t, err)
	assert.Contains(t, variables, "Values")
	assert.Contains(t, variables, "RootDir")
}

func TestGoTemplateRendererReleaseNamespaceHelpers(t *testing.T) {
	rootDir := t.TempDir()
	renderer := newTemplateRenderer(rootDir)

	releaseDir := filepath.Join(rootDir, "kubernetes", "apps", "media", "seerr", "app")
	require.NoError(t, os.MkdirAll(releaseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(releaseDir, "helmrelease.yaml"), []byte("spec:\n  values:\n    enabled: true\n"), 0o644))

	namespace, err := renderer.findReleaseNamespace("seerr")
	require.NoError(t, err)
	assert.Equal(t, "media", namespace)

	rendered, err := renderer.RenderHelmfileValues(`{{ .Release.Namespace }}:{{ .Release.Name }}`, "seerr")
	require.NoError(t, err)
	assert.Equal(t, "media:seerr", rendered)

	_, err = renderer.findReleaseNamespace("missing")
	require.Error(t, err)
}
