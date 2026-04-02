package yaml

import (
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/metrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestProcessor() *Processor {
	return NewProcessor(nil, metrics.NewPerformanceCollector())
}

func TestProcessorParseGetSetAndWrite(t *testing.T) {
	processor := newTestProcessor()

	data, err := processor.ParseString("metadata:\n  name: demo\nspec:\n  replicas: 1\n")
	require.NoError(t, err)

	value, err := processor.GetValue(data, "metadata.name")
	require.NoError(t, err)
	assert.Equal(t, "demo", value)

	require.NoError(t, processor.SetValue(data, "spec.replicas", 3))
	require.NoError(t, processor.SetValue(data, "spec.template.labels.app", "demo"))

	out, err := processor.ToString(data)
	require.NoError(t, err)
	assert.Contains(t, out, "replicas: 3")
	assert.Contains(t, out, "app: demo")

	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, processor.WriteFile(file, data))

	parsedFile, err := processor.ParseFile(file)
	require.NoError(t, err)

	replicas, err := processor.GetValue(parsedFile, "spec.replicas")
	require.NoError(t, err)
	assert.EqualValues(t, 3, replicas)
}

func TestProcessorPathAndSchemaValidation(t *testing.T) {
	processor := newTestProcessor()

	data := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "demo"},
		"spec":     "not-a-map",
	}

	require.NoError(t, processor.ValidateSchema(data, []string{"metadata", "spec"}))
	require.Error(t, processor.ValidateSchema(data, []string{"missing"}))

	_, err := processor.GetValue(data, "metadata.missing")
	require.Error(t, err)

	err = processor.SetValue(data, "spec.replicas", 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-object")
}

func TestProcessorMergeAndDeepCopy(t *testing.T) {
	processor := newTestProcessor()

	base := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"app": "demo"},
		},
		"list": []interface{}{"a"},
	}
	overlay := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{"tier": "backend"},
		},
		"list": []interface{}{"b"},
	}

	merged := processor.Merge(base, overlay)
	labels := merged["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
	assert.Equal(t, "demo", labels["app"])
	assert.Equal(t, "backend", labels["tier"])
	assert.Equal(t, []interface{}{"b"}, merged["list"])

	labels["app"] = "changed"
	assert.Equal(t, "demo", base["metadata"].(map[string]interface{})["labels"].(map[string]interface{})["app"])
}

func TestProcessorMachineTypeAndMergeHelpers(t *testing.T) {
	processor := newTestProcessor()
	dir := t.TempDir()

	basePath := filepath.Join(dir, "base.yaml")
	patchPath := filepath.Join(dir, "patch.yaml")
	machinePath := filepath.Join(dir, "machine.yaml")

	require.NoError(t, os.WriteFile(basePath, []byte("machine:\n  type: controlplane\ncluster:\n  name: old\n"), 0o644))
	require.NoError(t, os.WriteFile(patchPath, []byte("cluster:\n  name: new\n"), 0o644))
	require.NoError(t, os.WriteFile(machinePath, []byte("machine:\n  type: worker\n"), 0o644))

	machineType, err := processor.GetMachineType(machinePath)
	require.NoError(t, err)
	assert.Equal(t, "worker", machineType)

	mergedFiles, err := processor.MergeYAMLFiles(basePath, patchPath)
	require.NoError(t, err)
	assert.Contains(t, string(mergedFiles), "name: new")

	mergedContent, err := processor.MergeYAML([]byte("a: 1\nnested:\n  x: old\n"), []byte("nested:\n  x: new\n"))
	require.NoError(t, err)
	assert.Contains(t, string(mergedContent), "x: new")

	multiDoc, err := processor.MergeYAMLMultiDocument(
		[]byte("machine:\n  type: controlplane\n"),
		[]byte("---\nmachine:\n  network:\n    hostname: k8s-0\n---\nkind: Extra\nmetadata:\n  name: extra\n"),
	)
	require.NoError(t, err)
	assert.Contains(t, string(multiDoc), "type: controlplane")
	assert.Contains(t, string(multiDoc), "hostname: k8s-0")
	assert.Contains(t, string(multiDoc), "kind: Extra")
}
