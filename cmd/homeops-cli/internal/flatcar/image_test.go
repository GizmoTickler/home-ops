package flatcar

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveImageURL(t *testing.T) {
	url, err := ResolveImageURL(DefaultImageConfig("current"))
	require.NoError(t, err)
	assert.Equal(t, "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_qemu_uefi_image.img", url)

	url, err = ResolveImageURL(ImageConfig{Channel: "beta", Version: "4152.2.0", Format: "qcow2"})
	require.NoError(t, err)
	assert.Equal(t, "https://beta.release.flatcar-linux.net/amd64-usr/4152.2.0/flatcar_production_qemu_image.img", url)

	url, err = ResolveImageURL(ImageConfig{Format: "iso"})
	require.NoError(t, err)
	assert.Contains(t, url, "flatcar_production_iso_image.iso")
	assert.Contains(t, url, "/current/")
}

func TestSysextURL(t *testing.T) {
	assert.Equal(t,
		"https://extensions.flatcar.org/extensions/kubernetes/kubernetes-v1.36.1-x86-64.raw",
		SysextURL("v1.36.1"))
}
