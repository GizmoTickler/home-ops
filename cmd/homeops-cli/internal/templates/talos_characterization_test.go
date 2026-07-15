package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/metrics"

	"github.com/stretchr/testify/require"
)

func talosGoldenCompare(t *testing.T, rel string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", "talos", rel)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- fixed testdata path
	require.NoError(t, err, "missing golden %s (run with UPDATE_GOLDEN=1)", path)
	if string(got) != string(want) {
		t.Fatalf("rendered Talos output for %s drifted from golden (byte mismatch)", rel)
	}
}

func TestTalosRenderCharacterization(t *testing.T) {
	configs := map[string]*config.Config{
		"repo-mirror": {Cluster: config.ClusterConfig{Name: "home-ops-cluster"}},
		"no-config":   {},
	}
	for label, cfg := range configs {
		t.Run(label, func(t *testing.T) {
			restore := config.SetForTesting(cfg)
			defer restore()

			renderer := NewTemplateRenderer(".", common.NewColorLogger(), metrics.NewPerformanceCollector())
			env := map[string]string{
				"SCHEMATIC_ID":       "",
				"KUBERNETES_VERSION": "v1.36.1",
				"TALOS_VERSION":      "v1.13.6",
			}

			for _, base := range []string{"talos/controlplane.yaml", "talos/worker.yaml"} {
				for _, node := range []string{"192.168.122.10", "192.168.122.11", "192.168.122.12"} {
					got, err := renderer.RenderTalosConfigWithMerge(base, "talos/nodes/"+node+".yaml", env)
					require.NoError(t, err)
					rel := strings.TrimPrefix(base, "talos/") + "-" + node + ".yaml"
					talosGoldenCompare(t, rel, got)
				}
			}
		})
	}
}
