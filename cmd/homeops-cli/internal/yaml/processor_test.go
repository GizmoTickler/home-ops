package yaml

import (
	"fmt"
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

func TestProcessorMergeInvariants(t *testing.T) {
	processor := newTestProcessor()

	cases := []struct {
		name    string
		base    map[string]interface{}
		overlay map[string]interface{}
		want    map[string]interface{}
	}{
		{
			name: "overlay wins and base-only keys survive",
			base: map[string]interface{}{
				"apiVersion": "v1",
				"metadata": map[string]interface{}{
					"name":   "demo",
					"labels": map[string]interface{}{"app": "demo", "tier": "frontend"},
				},
				"spec": map[string]interface{}{"replicas": 1, "strategy": "RollingUpdate"},
			},
			overlay: map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{"tier": "backend"},
				},
				"spec": map[string]interface{}{"replicas": 3},
			},
			want: map[string]interface{}{
				"apiVersion": "v1",
				"metadata": map[string]interface{}{
					"name":   "demo",
					"labels": map[string]interface{}{"app": "demo", "tier": "backend"},
				},
				"spec": map[string]interface{}{"replicas": 3, "strategy": "RollingUpdate"},
			},
		},
		{
			name: "overlay scalar replaces base map",
			base: map[string]interface{}{
				"spec": map[string]interface{}{"nested": "value"},
				"keep": true,
			},
			overlay: map[string]interface{}{
				"spec": "disabled",
			},
			want: map[string]interface{}{
				"spec": "disabled",
				"keep": true,
			},
		},
		{
			name: "overlay slice replaces base slice",
			base: map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{"name": "app", "image": "old"},
				},
			},
			overlay: map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{"name": "app", "image": "new"},
				},
			},
			want: map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{"name": "app", "image": "new"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertMergeDoesNotMutateInputs(t, processor, tc.base, tc.overlay)
			assert.Equal(t, tc.want, processor.Merge(tc.base, tc.overlay))
		})
	}

	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprintf("generated-%02d", i), func(t *testing.T) {
			m := generatedYAMLConfig(i)
			assertMergeDoesNotMutateInputs(t, processor, m, m)
			assert.Equal(t, m, processor.Merge(m, m), "idempotence: Merge(m, m) must deep-equal m")

			empty := map[string]interface{}{}
			assertMergeDoesNotMutateInputs(t, processor, m, empty)
			assert.Equal(t, m, processor.Merge(m, empty), "right identity: Merge(m, empty) must deep-equal m")

			assertMergeDoesNotMutateInputs(t, processor, empty, m)
			assert.Equal(t, m, processor.Merge(empty, m), "left identity: Merge(empty, m) must deep-equal m")
		})
	}
}

func TestProcessorRoundTripInvariants(t *testing.T) {
	processor := newTestProcessor()

	cases := []map[string]interface{}{
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "homeops-cli",
				"namespace": "default",
				"labels":    map[string]interface{}{"app.kubernetes.io/name": "homeops-cli"},
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "homeops-cli"}},
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "app",
								"image": "ghcr.io/example/homeops-cli:v1",
								"env": []interface{}{
									map[string]interface{}{"name": "PORT", "value": "8080"},
									map[string]interface{}{"name": "READ_ONLY", "value": "true"},
								},
							},
						},
					},
				},
			},
		},
		{
			"cluster": map[string]interface{}{
				"name":              "home",
				"control_plane_vip": "192.168.123.253",
				"nodes": []interface{}{
					map[string]interface{}{"name": "k8s-0", "ip": "192.168.122.10", "vm": map[string]interface{}{"vmid": 200}},
					map[string]interface{}{"name": "k8s-1", "ip": "192.168.122.11", "vm": map[string]interface{}{"vmid": 201}},
				},
			},
			"features": map[string]interface{}{
				"flatcar": true,
				"talos":   false,
			},
		},
	}

	for i := 0; i < 8; i++ {
		cases = append(cases, generatedYAMLConfig(i))
	}

	for i, data := range cases {
		t.Run(fmt.Sprintf("case-%02d", i), func(t *testing.T) {
			out, err := processor.ToString(data)
			require.NoError(t, err, "case %d ToString", i)

			parsed, err := processor.ParseString(out)
			require.NoError(t, err, "case %d ParseString", i)
			assert.Equal(t, data, parsed, "ParseString(ToString(data)) must deep-equal data")
		})
	}
}

func assertMergeDoesNotMutateInputs(t *testing.T, processor *Processor, base, overlay map[string]interface{}) {
	t.Helper()
	baseBefore := testDeepCopy(base).(map[string]interface{})
	overlayBefore := testDeepCopy(overlay).(map[string]interface{})

	_ = processor.Merge(base, overlay)

	assert.Equal(t, baseBefore, base, "Merge must not mutate base")
	assert.Equal(t, overlayBefore, overlay, "Merge must not mutate overlay")
}

func generatedYAMLConfig(i int) map[string]interface{} {
	labels := map[string]interface{}{
		"app":       "app-" + string(rune('a'+i%26)),
		"component": "controller",
		"shard":     i,
	}
	nested := map[string]interface{}{
		"enabled": i%2 == 0,
		"limits": map[string]interface{}{
			"cpu":    i + 1,
			"memory": (i + 1) * 128,
		},
	}
	for depth := 0; depth < i%4; depth++ {
		nested = map[string]interface{}{
			"level": depth,
			"next":  nested,
		}
	}
	return map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "generated",
			"labels": labels,
		},
		"spec": map[string]interface{}{
			"replicas": i%5 + 1,
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "app", "args": []interface{}{"--index", i}},
					},
				},
			},
			"nested": nested,
		},
	}
}

func testDeepCopy(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[key] = testDeepCopy(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = testDeepCopy(item)
		}
		return out
	default:
		return v
	}
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
