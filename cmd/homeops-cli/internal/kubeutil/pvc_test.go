package kubeutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"homeops-cli/internal/constants"
)

func TestPVCExclusions(t *testing.T) {
	assert.True(t, IsVolSyncPlumbingPVC("volsync-radarr-cache", nil, nil))
	assert.True(t, IsVolSyncPlumbingPVC("temporary", map[string]string{"app.kubernetes.io/created-by": "VolSync"}, nil))
	assert.True(t, IsVolSyncPlumbingPVC("temporary", nil, []OwnerReference{{APIVersion: "volsync.backube/v1alpha1", Kind: "ReplicationSource"}}))
	assert.False(t, IsVolSyncPlumbingPVC("radarr", nil, nil))

	assert.True(t, IsPodOwnedPVC([]OwnerReference{{Kind: "Pod"}}))
	assert.False(t, IsPodOwnedPVC([]OwnerReference{{Kind: "StatefulSet"}}))
	assert.True(t, HasWorkloadOwner([]OwnerReference{{Kind: "StatefulSet"}}))
	assert.False(t, HasWorkloadOwner([]OwnerReference{{Kind: "ConfigMap"}}))
}

func TestSystemNamespaces(t *testing.T) {
	for _, namespace := range []string{constants.NSKubeSystem, constants.NSFluxSystem, constants.NSScaleCSI, constants.NSVolsyncSystem} {
		assert.True(t, IsSystemNamespace(namespace), namespace)
	}
	assert.False(t, IsSystemNamespace("volsync"))
	assert.False(t, IsSystemNamespace("media"))
}
