package vsphere

import (
	"os"
	"testing"

	"homeops-cli/internal/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMConfigHelpers(t *testing.T) {
	config, exists := GetK8sNodeConfig("k8s-0")
	require.True(t, exists)
	assert.Equal(t, "local-nvme1", config.BootDatastore)

	defaultConfig := GetDefaultVMConfig("demo")
	assert.Equal(t, "demo", defaultConfig.Name)
	assert.True(t, defaultConfig.PowerOn)
	assert.True(t, defaultConfig.EnableSRIOV)

	k8sConfig := GetK8sVMConfig("k8s-0")
	assert.Equal(t, "00:a0:98:28:c8:83", k8sConfig.MacAddress)
	assert.Equal(t, DefaultISOPath(), k8sConfig.ISO)
	assert.Equal(t, DefaultISODatastore, k8sConfig.ISODatastore)
	assert.Equal(t, BuildISOPath(DefaultISODatastore, DefaultISOFilename), DefaultISOPath())
	assert.Equal(t, "[datastore1] vmware-amd64.iso", BuildISOPath("datastore1", "vmware-amd64.iso"))
}

func TestVMConfigValidate(t *testing.T) {
	config := VMConfig{
		Name:      "vm",
		Memory:    1024,
		VCPUs:     2,
		DiskSize:  20,
		Datastore: "local",
		Network:   "vl999",
		ISO:       "[datastore1] vmware-amd64.iso",
	}
	require.NoError(t, config.Validate())

	bad := config
	bad.Name = ""
	require.Error(t, bad.Validate())
	bad = config
	bad.Memory = 0
	require.Error(t, bad.Validate())
	bad = config
	bad.VCPUs = 0
	require.Error(t, bad.Validate())
	bad = config
	bad.DiskSize = 0
	require.Error(t, bad.Validate())
	bad = config
	bad.Datastore = ""
	require.Error(t, bad.Validate())
	bad = config
	bad.Network = ""
	require.Error(t, bad.Validate())
	bad = config
	bad.ISO = ""
	require.Error(t, bad.Validate())
}

func TestVSphereClientHelpers(t *testing.T) {
	originalGetSecrets := get1PasswordSecretsBatch
	t.Cleanup(func() { get1PasswordSecretsBatch = originalGetSecrets })
	get1PasswordSecretsBatch = func([]string) map[string]string { return map[string]string{} }

	client := NewClient("host", "user", "pass", true)
	require.NoError(t, client.Close())

	t.Setenv(constants.EnvVSphereHost, "")
	t.Setenv(constants.EnvVSphereUsername, "")
	t.Setenv(constants.EnvVSpherePassword, "")
	_, err := GetVMNames()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vSphere credentials not found")

	loggerClient := &ESXiSSHClient{
		host:     "host",
		username: "user",
		logger:   client.logger,
		keyFile:  filepathForTempKey(t),
	}
	loggerClient.Close()
	_, err = os.Stat(loggerClient.keyFile)
	assert.True(t, os.IsNotExist(err))

	err = loggerClient.CreateK8sVM(VMConfig{Name: "unknown"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no predefined configuration")
}

func filepathForTempKey(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp("", "vsphere-key-*")
	require.NoError(t, err)
	path := file.Name()
	require.NoError(t, file.Close())
	return path
}
