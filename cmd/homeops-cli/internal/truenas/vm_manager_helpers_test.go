package truenas

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMManagerBuildConfigAndIdentifiers(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)

	vmConfig := manager.buildVMConfig(VMConfig{
		Name:   "k8s-0",
		Memory: 16384,
		VCPUs:  4,
	})
	assert.Equal(t, "k8s-0", vmConfig["name"])
	assert.Equal(t, "Talos Linux VM - k8s-0", vmConfig["description"])
	assert.Equal(t, 16384, vmConfig["memory"])
	assert.Equal(t, 4, vmConfig["vcpus"])
	assert.Equal(t, "UEFI", vmConfig["bootloader"])

	mac := manager.generateRandomMAC()
	assert.Regexp(t, `^00:0c:29:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$`, mac)

	serial := manager.generateRandomSerial()
	assert.Len(t, serial, 8)
	assert.Equal(t, strings.ToUpper(serial), serial)
}

func TestVMManagerZVolHelpers(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)

	paths := manager.getZVolPaths(VMConfig{Name: "k8s-0", StoragePool: "flashstor"})
	assert.Equal(t, "flashstor/VM/k8s-0-boot", paths["boot"])
	assert.Equal(t, "flashstor/VM/k8s-0-openebs", paths["openebs"])

	paths = manager.getZVolPaths(VMConfig{Name: "k8s-0", StoragePool: "flashstor/VM"})
	assert.Equal(t, "flashstor/VM/k8s-0-boot", paths["boot"])
	assert.Equal(t, "flashstor/VM/k8s-0-openebs", paths["openebs"])

	paths = manager.getZVolPaths(VMConfig{
		Name:        "k8s-0",
		StoragePool: "flashstor",
		BootZVol:    "custom/boot",
		OpenEBSZVol: "custom/openebs",
	})
	assert.Equal(t, "custom/boot", paths["boot"])
	assert.Equal(t, "custom/openebs", paths["openebs"])
}

func TestVMManagerCreateVMDevicesAndPatternDiscovery(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	var createdDevices []map[string]interface{}

	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.device.create":
			args, ok := params.([]interface{})
			require.True(t, ok)
			device, ok := args[0].(map[string]interface{})
			require.True(t, ok)
			createdDevices = append(createdDevices, device)
			return mustJSON(map[string]any{"result": true}), nil
		case "pool.dataset.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{
					{"name": "flashstor/VM/k8s-0-boot", "type": "VOLUME"},
					{"name": "flashstor/VM/k8s-0-openebs", "type": "VOLUME"},
					{"name": "flashstor/VM/k8s-0-openebs", "type": "VOLUME"},
					{"name": "flashstor/VM/k8s-0-notes", "type": "FILESYSTEM"},
				},
			}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	err := manager.createVMDevices(42, VMConfig{
		Name:          "k8s-0",
		StoragePool:   "flashstor",
		NetworkBridge: "br0",
		BootZVol:      "flashstor/VM/k8s-0-boot",
		OpenEBSZVol:   "flashstor/VM/k8s-0-openebs",
		SpicePassword: "spice-secret",
		UseSpice:      true,
	})
	require.NoError(t, err)
	require.Len(t, createdDevices, 5)

	assert.Equal(t, float64(1006), asFloat(createdDevices[0]["order"]))
	assert.Equal(t, float64(1002), asFloat(createdDevices[1]["order"]))
	assert.Equal(t, float64(1001), asFloat(createdDevices[2]["order"]))
	assert.Equal(t, float64(1004), asFloat(createdDevices[3]["order"]))
	assert.Equal(t, float64(1003), asFloat(createdDevices[4]["order"]))

	discovered := manager.discoverZVolsByPattern("flashstor", "k8s-0")
	assert.Equal(t, []string{"flashstor/VM/k8s-0-boot", "flashstor/VM/k8s-0-openebs"}, discovered)
}

func TestVMManagerCreateVMDevicesRequiresSpicePassword(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		return mustJSON(map[string]any{"result": true}), nil
	}

	err := manager.createVMDevices(42, VMConfig{
		Name:          "k8s-0",
		StoragePool:   "flashstor",
		NetworkBridge: "br0",
		UseSpice:      true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SPICE password is required")
}

func TestVMManagerCreateVMDevicesWithoutSpice(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	var createdDevices []map[string]interface{}
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.device.create":
			args := params.([]interface{})
			device := args[0].(map[string]interface{})
			createdDevices = append(createdDevices, device)
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return mustJSON(map[string]any{"result": true}), nil
		}
	}

	err := manager.createVMDevices(42, VMConfig{
		Name:          "k8s-0",
		StoragePool:   "flashstor",
		NetworkBridge: "br0",
		BootZVol:      "flashstor/VM/k8s-0-boot",
		OpenEBSZVol:   "flashstor/VM/k8s-0-openebs",
		UseSpice:      false,
	})
	require.NoError(t, err)
	require.Len(t, createdDevices, 4)
}

func TestVMManagerStopVMUsesForceMode(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	var methods []string
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "vm.query":
			return mustJSON(map[string]interface{}{
				"result": []map[string]interface{}{
					{"id": 7, "name": "vm1"},
				},
			}), nil
		case "vm.device.query", "vm.stop", "vm.poweroff":
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	require.NoError(t, manager.StopVM("vm1", false))
	require.NoError(t, manager.StopVM("vm1", true))
	assert.Contains(t, methods, "vm.stop")
	assert.Contains(t, methods, "vm.poweroff")
}

func TestVMManagerDeleteVMAbortsIfVMStillExistsAfterDeleteRequest(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	oldSleep := sleepForOperation
	sleepForOperation = func(time.Duration) {}
	t.Cleanup(func() {
		sleepForOperation = oldSleep
	})

	var datasetDeletes int
	vmQueryCalls := 0
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			vmQueryCalls++
			return mustJSON(map[string]interface{}{
				"result": []map[string]interface{}{
					{"id": 11, "name": "vm1"},
				},
			}), nil
		case "vm.device.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{
					{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/vm1-boot"}},
				},
			}), nil
		case "vm.delete":
			return mustJSON(map[string]any{"result": true}), nil
		case "pool.dataset.delete":
			datasetDeletes++
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	err := manager.DeleteVM("vm1", true, "flashstor")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to delete backing ZVols")
	assert.Equal(t, 0, datasetDeletes)
	assert.GreaterOrEqual(t, vmQueryCalls, 3)
}

func TestVMManagerDeleteVMDeletesZVolsAfterVerifiedDelete(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	oldSleep := sleepForOperation
	sleepForOperation = func(time.Duration) {}
	t.Cleanup(func() {
		sleepForOperation = oldSleep
	})

	vmQueryCalls := 0
	var deletedDatasets []string
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			vmQueryCalls++
			if vmQueryCalls == 1 {
				return mustJSON(map[string]interface{}{
					"result": []map[string]interface{}{
						{"id": 11, "name": "vm1"},
					},
				}), nil
			}
			return mustJSON(map[string]interface{}{"result": []map[string]interface{}{}}), nil
		case "vm.device.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{
					{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/vm1-openebs"}},
					{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/vm1-boot"}},
				},
			}), nil
		case "vm.delete":
			return mustJSON(map[string]any{"result": true}), nil
		case "pool.dataset.delete":
			args, ok := params.([]interface{})
			require.True(t, ok)
			require.Len(t, args, 2)
			name, ok := args[0].(string)
			require.True(t, ok)
			deletedDatasets = append(deletedDatasets, name)
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	err := manager.DeleteVM("vm1", true, "flashstor")
	require.NoError(t, err)
	assert.Equal(t, []string{"flashstor/VM/vm1-boot", "flashstor/VM/vm1-openebs"}, deletedDatasets)
}

func TestVMManagerCleanupOrphanedZVolsDeletesPatternMatches(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	var deletedDatasets []string
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "pool.dataset.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{
					{"name": "flashstor/VM/vm1-openebs", "type": "VOLUME"},
					{"name": "flashstor/VM/vm1-boot", "type": "VOLUME"},
					{"name": "flashstor/VM/vm1-notes", "type": "FILESYSTEM"},
				},
			}), nil
		case "pool.dataset.delete":
			args := params.([]interface{})
			deletedDatasets = append(deletedDatasets, args[0].(string))
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	require.NoError(t, manager.CleanupOrphanedZVols("vm1", "flashstor"))
	assert.Equal(t, []string{"flashstor/VM/vm1-boot", "flashstor/VM/vm1-openebs"}, deletedDatasets)
}

func TestExtractZVolPathFromDevice(t *testing.T) {
	path, ok := extractZVolPathFromDevice(map[string]interface{}{
		"attributes": map[string]interface{}{
			"dtype": "DISK",
			"path":  "/dev/zvol/flashstor/VM/k8s-0-boot",
		},
	})
	require.True(t, ok)
	assert.Equal(t, "flashstor/VM/k8s-0-boot", path)

	_, ok = extractZVolPathFromDevice(map[string]interface{}{
		"attributes": map[string]interface{}{
			"dtype": "CDROM",
			"path":  "/dev/zvol/flashstor/VM/k8s-0-boot",
		},
	})
	assert.False(t, ok)
}

func TestUniqueSortedStrings(t *testing.T) {
	assert.Equal(t,
		[]string{"a", "b", "c"},
		uniqueSortedStrings([]string{"c", "a", "b", "a", "", "  "}),
	)
	assert.Nil(t, uniqueSortedStrings(nil))
}

func asFloat(v interface{}) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}
