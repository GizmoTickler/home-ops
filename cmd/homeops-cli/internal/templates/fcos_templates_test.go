package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderFCOSTemplate(t *testing.T) {
	rendered, err := RenderFCOSTemplate("butane/controlplane.bu", map[string]string{
		"NODE_NAME":          "k8s-0",
		"KUBERNETES_VERSION": "v1.36.1",
	})
	require.NoError(t, err)
	assert.Contains(t, rendered, "variant: fcos")
	assert.Contains(t, rendered, "inline: \"k8s-0\"")
	assert.Contains(t, rendered, "k8s-sysext-v1.36.1.stamp")
	assert.NotContains(t, rendered, "{{ ENV.NODE_NAME }}")
	assert.NotContains(t, rendered, "extensions.flatcar.org")
}

func TestGetFCOSTemplateRaw(t *testing.T) {
	raw, err := GetFCOSTemplate("files/install-k8s-sysext.sh")
	require.NoError(t, err)
	assert.Contains(t, raw, "{{ ENV.KUBERNETES_VERSION }}")

	_, err = GetFCOSTemplate("does/not/exist.sh")
	require.Error(t, err)
}

func TestListFCOSFiles(t *testing.T) {
	files, err := ListFCOSFiles("files")
	require.NoError(t, err)
	assert.Contains(t, files, "files/modules-load-homeops.conf")
	assert.Contains(t, files, "files/install-k8s-sysext.sh")

	units, err := ListFCOSFiles("systemd")
	require.NoError(t, err)
	assert.Contains(t, units, "systemd/kubelet.service")
	assert.Contains(t, units, "systemd/10-kubeadm.conf")

	_, err = ListFCOSFiles("nope")
	require.Error(t, err)
}
