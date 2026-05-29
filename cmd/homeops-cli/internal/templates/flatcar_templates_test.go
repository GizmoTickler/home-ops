package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderFlatcarTemplate(t *testing.T) {
	rendered, err := RenderFlatcarTemplate("butane/controlplane.bu", map[string]string{
		"NODE_NAME":          "k8s-0",
		"KUBERNETES_VERSION": "v1.36.1",
		"KUBERNETES_MINOR":   "v1.36",
	})
	require.NoError(t, err)
	assert.Contains(t, rendered, "variant: flatcar")
	assert.Contains(t, rendered, "inline: \"k8s-0\"")
	assert.Contains(t, rendered, "kubernetes-v1.36.1-x86-64.raw")
	assert.NotContains(t, rendered, "{{ ENV.NODE_NAME }}")
}

func TestGetFlatcarTemplateRaw(t *testing.T) {
	raw, err := GetFlatcarTemplate("kubeadm/init-config.yaml")
	require.NoError(t, err)
	assert.Contains(t, raw, "{{ ENV.NODE_IP }}")

	_, err = GetFlatcarTemplate("does/not/exist.yaml")
	require.Error(t, err)
}

func TestListFlatcarFiles(t *testing.T) {
	files, err := ListFlatcarFiles("files")
	require.NoError(t, err)
	assert.Contains(t, files, "files/containerd-config.toml")
	assert.Contains(t, files, "files/sysctl-99-homeops.conf")

	manifests, err := ListFlatcarFiles("manifests")
	require.NoError(t, err)
	assert.Contains(t, manifests, "manifests/kube-vip.yaml")
}
