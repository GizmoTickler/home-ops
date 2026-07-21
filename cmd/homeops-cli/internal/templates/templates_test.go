package templates

import (
	"os"
	"path/filepath"
	"testing"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
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
		"STORAGE_CLASS":       "scale-nvmeof",
		"SNAPSHOT_CLASS":      "scale-snapshot",
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
		"STORAGE_CLASS":       "scale-nvmeof",
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

func TestTalosTemplatesUseClusterConfig(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Cluster: config.ClusterConfig{
			Name:          "custom-cluster",
			PodCIDR:       "10.244.0.0/16",
			ServiceCIDR:   "10.96.0.0/12",
			DNSDomain:     "corp.local",
			NodeSubnet:    "10.0.0.0/24",
			NTPServers:    []string{"10.0.0.1", "10.0.0.2"},
			ExtraCertSANs: []string{"10.0.0.100", "api.internal"},
			Talos: config.TalosSettings{
				DiscoveryEndpoint:       "http://10.0.0.10:3000",
				ControlPlaneInstallDisk: "/dev/control",
				WorkerInstallDisk:       "/dev/worker",
				UserVolume: config.TalosUserVolumeSettings{
					Disk:    "/dev/local",
					MinSize: "750GB",
					MaxSize: "950GB",
				},
			},
			Nodes: []config.Node{
				{
					Name: "k8s-0",
					IP:   "192.168.122.10",
					VM: config.VMProfile{Providers: config.ProviderVMProfiles{
						Talos: config.ProviderVMProfile{Mac: "02:00:00:00:00:11"},
					}},
				},
			},
		},
	})
	defer restore()

	controlplane, err := RenderTalosTemplate("talos/controlplane.yaml", nil)
	require.NoError(t, err)
	assert.Contains(t, controlplane, "disk: /dev/control")
	assert.Contains(t, controlplane, "factory.talos.dev/installer/c682c3b7e2747ecadb2b2f9eb73336e40373cfbe6f7ec588336ece6f3a1059cf:v1.13.6")
	assert.Contains(t, controlplane, "image: ghcr.io/siderolabs/kubelet:v1.36.2")
	assert.Contains(t, controlplane, "clusterName: custom-cluster")
	assert.Contains(t, controlplane, "dnsDomain: corp.local")
	assert.Contains(t, controlplane, "- 10.244.0.0/16")
	assert.Contains(t, controlplane, "- 10.96.0.0/12")
	assert.Contains(t, controlplane, "- 10.0.0.0/24")
	assert.Contains(t, controlplane, "- 10.0.0.100")
	assert.Contains(t, controlplane, "- api.internal")

	worker, err := RenderTalosTemplate("talos/worker.yaml", nil)
	require.NoError(t, err)
	assert.Contains(t, worker, "disk: /dev/worker")

	node, err := RenderTalosTemplate("talos/nodes/192.168.122.10.yaml", nil)
	require.NoError(t, err)
	assert.Contains(t, node, "hardwareAddr: 02:00:00:00:00:11")
	assert.Contains(t, node, "hostname: k8s-0")
	assert.Contains(t, node, "- 10.0.0.1")
	assert.Contains(t, node, "- 10.0.0.2")
	assert.Contains(t, node, "endpoint: http://10.0.0.10:3000")
	assert.Contains(t, node, `match: disk.dev_path == "/dev/local"`)
	assert.Contains(t, node, "minSize: 750GB")
	assert.Contains(t, node, "maxSize: 950GB")
}

func TestBootstrapTemplateHelpers(t *testing.T) {
	restore := config.SetForTesting(&config.Config{Bootstrap: config.BootstrapSettings{OpVault: "OpsVault"}})
	defer restore()

	rendered, err := RenderBootstrapTemplate("resources.yaml", map[string]string{
		"BOOTSTRAP_RESOURCES_NAMESPACE": "flux-system",
		"SECRET_DOMAIN":                 "example.com",
	})
	require.NoError(t, err)
	assert.Contains(t, rendered, "external-secrets")
	assert.Contains(t, rendered, "network")
	assert.Contains(t, rendered, "cloudflare-tunnel-id-secret")

	store, err := RenderBootstrapTemplate("clustersecretstore.yaml", nil)
	require.NoError(t, err)
	assert.Contains(t, store, "OpsVault: 1")

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
