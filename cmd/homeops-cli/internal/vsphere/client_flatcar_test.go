package vsphere

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/types"
)

// extraConfigMap flattens ExtraConfig OptionValues into a key->value map for asserts.
func extraConfigMap(opts []types.BaseOptionValue) map[string]string {
	m := map[string]string{}
	for _, o := range opts {
		if ov, ok := o.(*types.OptionValue); ok {
			if s, ok := ov.Value.(string); ok {
				m[ov.Key] = s
			}
		}
	}
	return m
}

func TestBuildExtraConfigGuestinfo(t *testing.T) {
	t.Run("emits base64 guestinfo when Ignition data is set", func(t *testing.T) {
		m := extraConfigMap(buildExtraConfig(VMConfig{IgnitionData: "eyJpZ24iOjF9"}))
		assert.Equal(t, "eyJpZ24iOjF9", m["guestinfo.ignition.config.data"])
		assert.Equal(t, "base64", m["guestinfo.ignition.config.data.encoding"])
		assert.Equal(t, "TRUE", m["disk.EnableUUID"]) // baseline still present
	})

	t.Run("omits guestinfo when no Ignition data (Talos path unaffected)", func(t *testing.T) {
		m := extraConfigMap(buildExtraConfig(VMConfig{ExposeCounters: true}))
		_, hasData := m["guestinfo.ignition.config.data"]
		_, hasEnc := m["guestinfo.ignition.config.data.encoding"]
		assert.False(t, hasData)
		assert.False(t, hasEnc)
		assert.Equal(t, "45", m["monitor.phys_bits_used"]) // existing behavior preserved
	})
}

func TestBuildFlatcarCloneSpec(t *testing.T) {
	pool := types.ManagedObjectReference{Type: "ResourcePool", Value: "pool-1"}
	ds := types.ManagedObjectReference{Type: "Datastore", Value: "ds-1"}
	cfg := VMConfig{
		Name:         "k8s-0",
		VCPUs:        8,
		Memory:       16384,
		PowerOn:      true,
		IgnitionData: "eyJpZ24iOjF9",
		TemplateName: "flatcar-tmpl",
	}

	spec := buildFlatcarCloneSpec(cfg, pool, ds)

	require.NotNil(t, spec.Location.Pool)
	require.NotNil(t, spec.Location.Datastore)
	assert.Equal(t, pool, *spec.Location.Pool)
	assert.Equal(t, ds, *spec.Location.Datastore)
	assert.True(t, spec.PowerOn)

	require.NotNil(t, spec.Config)
	assert.Equal(t, int32(8), spec.Config.NumCPUs)
	assert.Equal(t, int64(16384), spec.Config.MemoryMB)

	m := extraConfigMap(spec.Config.ExtraConfig)
	assert.Equal(t, "eyJpZ24iOjF9", m["guestinfo.ignition.config.data"])
	assert.Equal(t, "base64", m["guestinfo.ignition.config.data.encoding"])
}

func TestCloneFlatcarVMGuards(t *testing.T) {
	c := &Client{} // guards return before touching the finder/logger

	_, err := c.CloneFlatcarVM(VMConfig{IgnitionData: "x"}) // missing template
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template name")

	_, err = c.CloneFlatcarVM(VMConfig{TemplateName: "flatcar-ova"}) // missing Ignition
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ignition")
}

func TestBuildFlatcarCloneSpecOmitsZeroCPUMem(t *testing.T) {
	pool := types.ManagedObjectReference{Type: "ResourcePool", Value: "p"}
	ds := types.ManagedObjectReference{Type: "Datastore", Value: "d"}
	// No VCPUs/Memory → don't override the template's hardware (leave at 0).
	spec := buildFlatcarCloneSpec(VMConfig{Name: "n", IgnitionData: "x"}, pool, ds)
	require.NotNil(t, spec.Config)
	assert.Equal(t, int32(0), spec.Config.NumCPUs)
	assert.Equal(t, int64(0), spec.Config.MemoryMB)
}
