package constants

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConstantValues(t *testing.T) {
	assert.Equal(t, "PROXMOX_HOST", EnvProxmoxHost)
	assert.Equal(t, "PROXMOX_NODE", EnvProxmoxNode)
	assert.Equal(t, "KUBECONFIG", EnvKubeconfig)
	assert.Equal(t, "TALOSCONFIG", EnvTalosconfig)
	assert.Equal(t, "HOMEOPS_NO_INTERACTIVE", EnvHomeOpsNoInteract)

	assert.Equal(t, "flux-system", NSFluxSystem)
	assert.Equal(t, "kube-system", NSKubeSystem)
	assert.Equal(t, "network", NSNetwork)

	assert.Equal(t, "https://factory.talos.dev", TalosFactoryBaseURL)

	assert.Equal(t, "30s", DefaultKubectlTimeout)
	assert.Equal(t, 120000, DefaultCommandTimeout)
	assert.Equal(t, 3, MaxRetryAttempts)
	assert.Greater(t, BootstrapFluxMaxWait, BootstrapCheckIntervalSlow)
	assert.Greater(t, BootstrapNodeMaxWait, BootstrapCheckIntervalNormal)
}
