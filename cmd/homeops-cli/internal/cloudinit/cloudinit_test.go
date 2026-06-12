package cloudinit

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestUserdata(t *testing.T) {
	out, err := Userdata("ubuntu", "ssh-ed25519 KEY", "dev0")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "#cloud-config\n"))

	var doc map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "dev0", doc["hostname"])
	assert.Equal(t, false, doc["ssh_pwauth"])
	users := doc["users"].([]interface{})
	user := users[0].(map[string]interface{})
	assert.Equal(t, "ubuntu", user["name"])
	assert.Equal(t, []interface{}{"ssh-ed25519 KEY"}, user["ssh_authorized_keys"])
}

func TestUserdataNoKey(t *testing.T) {
	out, err := Userdata("rocky", "", "r0")
	require.NoError(t, err)
	assert.NotContains(t, out, "ssh_authorized_keys")
}

func TestMetadata(t *testing.T) {
	assert.Equal(t, "instance-id: dev0\nlocal-hostname: dev0\n", Metadata("dev0", "dev0"))
}

func TestNetworkConfigV2(t *testing.T) {
	t.Run("dhcp", func(t *testing.T) {
		out, err := NetworkConfigV2("", "", nil)
		require.NoError(t, err)
		var doc map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(out), &doc))
		primary := doc["ethernets"].(map[string]interface{})["primary"].(map[string]interface{})
		assert.Equal(t, true, primary["dhcp4"])
	})

	t.Run("static", func(t *testing.T) {
		out, err := NetworkConfigV2("192.168.1.50/24", "192.168.1.1", []string{"1.1.1.1"})
		require.NoError(t, err)
		var doc map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(out), &doc))
		primary := doc["ethernets"].(map[string]interface{})["primary"].(map[string]interface{})
		assert.Equal(t, false, primary["dhcp4"])
		assert.Equal(t, []interface{}{"192.168.1.50/24"}, primary["addresses"])
		route := primary["routes"].([]interface{})[0].(map[string]interface{})
		assert.Equal(t, "192.168.1.1", route["via"])
		ns := primary["nameservers"].(map[string]interface{})
		assert.Equal(t, []interface{}{"1.1.1.1"}, ns["addresses"])
	})
}

func TestVSphereMetadata(t *testing.T) {
	out, err := VSphereMetadata("dev0", "10.0.0.5/24", "10.0.0.1", []string{"10.0.0.2"})
	require.NoError(t, err)
	var meta map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &meta))
	assert.Equal(t, "dev0", meta["instance-id"])
	assert.Equal(t, "base64", meta["network.encoding"])
	network, err := base64.StdEncoding.DecodeString(meta["network"])
	require.NoError(t, err)
	assert.Contains(t, string(network), "10.0.0.5/24")
}

func TestBuildNoCloudSeedISO(t *testing.T) {
	userdata, err := Userdata("ubuntu", "ssh-ed25519 KEY", "dev0")
	require.NoError(t, err)
	network, err := NetworkConfigV2("", "", nil)
	require.NoError(t, err)

	iso, err := BuildNoCloudSeedISO(userdata, Metadata("dev0", "dev0"), network)
	require.NoError(t, err)
	require.NotEmpty(t, iso)
	// ISO9660 primary volume descriptor lives at sector 16 and starts with
	// \x01CD001; the volume label "cidata" must appear inside it.
	require.Greater(t, len(iso), 32768+7)
	assert.Equal(t, byte(1), iso[32768])
	assert.Equal(t, "CD001", string(iso[32769:32774]))
	assert.Contains(t, string(iso[32768:34816]), "cidata")
	assert.Contains(t, string(iso), "hostname: dev0")
}
