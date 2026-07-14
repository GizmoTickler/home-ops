package yaml

import (
	"strings"
	"testing"
)

const (
	fuzzMaxDottedPathLength = 4096
	fuzzMaxDottedPathDepth  = 128
)

func FuzzProcessorGetSetValue(f *testing.F) {
	for _, seed := range []string{
		"",
		".",
		"..",
		"a",
		"a.b",
		"a.b.c",
		"a.b.0.c",
		"metadata.name",
		"spec.replicas",
		"spec.template.labels.app",
		"spec..replicas",
		strings.Repeat("a.", 256),
		strings.Repeat("deep.", 128) + "leaf",
		"emoji.☃",
		"a\x00b.c",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		processor := newTestProcessor()

		getData := fuzzYAMLData()
		_, _ = processor.GetValue(getData, path)

		setData := fuzzYAMLData()
		err := processor.SetValue(setData, path, "fuzz-value")
		if fuzzPathMalformed(path) && err == nil {
			t.Fatalf("SetValue accepted malformed path %q", path)
		}
	})
}

func fuzzYAMLData() map[string]interface{} {
	return map[string]interface{}{
		"a": map[string]interface{}{
			"b": map[string]interface{}{
				"c": "value",
			},
		},
		"metadata": map[string]interface{}{
			"name": "demo",
		},
		"spec": map[string]interface{}{
			"replicas": 1,
			"template": map[string]interface{}{
				"labels": map[string]interface{}{
					"app": "demo",
				},
			},
		},
		"scalar": "not-a-map",
	}
}

func fuzzPathMalformed(path string) bool {
	if path == "" || len(path) > fuzzMaxDottedPathLength {
		return true
	}
	parts := strings.Split(path, ".")
	if len(parts) > fuzzMaxDottedPathDepth {
		return true
	}
	for _, part := range parts {
		if part == "" {
			return true
		}
	}
	return false
}
