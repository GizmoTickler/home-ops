package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestApplyFlatcarDefaults(t *testing.T) {
	c := &VersionConfig{}
	applyFlatcarDefaults(c)
	assert.Equal(t, defaultFlatcarVersion, c.FlatcarVersion)
	assert.Equal(t, defaultKubeVipVersion, c.KubeVipVersion)
	assert.Equal(t, defaultPauseImage, c.PauseImage)

	// Existing values are not overridden.
	c2 := &VersionConfig{FlatcarVersion: "4152.2.0", KubeVipVersion: "v0.9.0", PauseImage: "custom"}
	applyFlatcarDefaults(c2)
	assert.Equal(t, "4152.2.0", c2.FlatcarVersion)
	assert.Equal(t, "v0.9.0", c2.KubeVipVersion)
	assert.Equal(t, "custom", c2.PauseImage)
}

func TestGetDefaultVersionsHasFlatcarDefaults(t *testing.T) {
	v := getDefaultVersions()
	assert.NotEmpty(t, v.FlatcarVersion)
	assert.NotEmpty(t, v.KubeVipVersion)
	assert.NotEmpty(t, v.PauseImage)
}
