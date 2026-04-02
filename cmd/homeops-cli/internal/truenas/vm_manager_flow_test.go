package truenas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMManagerDeployVMSuccess(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)

	existingDatasets := map[string]string{
		"flashstor":    "FILESYSTEM",
		"flashstor/VM": "FILESYSTEM",
	}
	var createdVMs []map[string]interface{}
	var createdDevices []map[string]interface{}

	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			return mustJSON(map[string]any{"result": []map[string]any{}}), nil
		case "pool.dataset.query":
			var datasets []map[string]any
			for name, typ := range existingDatasets {
				datasets = append(datasets, map[string]any{"name": name, "type": typ})
			}
			return mustJSON(map[string]any{"result": datasets}), nil
		case "pool.dataset.create":
			args := params.([]interface{})
			cfg := args[0].(map[string]interface{})
			name := cfg["name"].(string)
			typ := cfg["type"].(string)
			existingDatasets[name] = typ
			return mustJSON(map[string]any{"result": true}), nil
		case "vm.create":
			args := params.([]interface{})
			cfg := args[0].(map[string]interface{})
			createdVMs = append(createdVMs, cfg)
			return mustJSON(map[string]any{"result": map[string]any{"id": 41, "name": cfg["name"]}}), nil
		case "vm.device.create":
			args := params.([]interface{})
			device := args[0].(map[string]interface{})
			createdDevices = append(createdDevices, device)
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	err := manager.DeployVM(VMConfig{
		Name:          "cp-0",
		Memory:        8192,
		VCPUs:         4,
		DiskSize:      250,
		OpenEBSSize:   1000,
		StoragePool:   "flashstor",
		NetworkBridge: "br0",
		TalosISO:      "/isos/talos.iso",
		SpicePassword: "secret",
		UseSpice:      true,
	})
	require.NoError(t, err)
	require.Len(t, createdVMs, 1)
	assert.Equal(t, "cp-0", createdVMs[0]["name"])
	assert.Equal(t, "VOLUME", existingDatasets["flashstor/VM/cp-0-boot"])
	assert.Equal(t, "VOLUME", existingDatasets["flashstor/VM/cp-0-openebs"])
	require.Len(t, createdDevices, 5)
}

func TestVMManagerDeployVMValidation(t *testing.T) {
	t.Run("duplicate VM", func(t *testing.T) {
		manager := NewVMManager("nas", "key", 443, true)
		manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
			switch method {
			case "vm.query":
				return mustJSON(map[string]any{
					"result": []map[string]any{{"id": 1, "name": "cp-0"}},
				}), nil
			case "vm.device.query":
				return mustJSON(map[string]any{"result": []map[string]any{}}), nil
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}

		err := manager.DeployVM(VMConfig{Name: "cp-0"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("skip zvol create verifies presence", func(t *testing.T) {
		manager := NewVMManager("nas", "key", 443, true)
		manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
			switch method {
			case "vm.query":
				return mustJSON(map[string]any{"result": []map[string]any{}}), nil
			case "pool.dataset.query":
				args := params.([]interface{})
				filters := args[0].([][]interface{})
				target := filters[0][2].(string)
				if target == "flashstor/VM/cp-0-boot" {
					return mustJSON(map[string]any{"result": []map[string]any{{"name": target, "type": "VOLUME"}}}), nil
				}
				return mustJSON(map[string]any{"result": []map[string]any{}}), nil
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}

		err := manager.DeployVM(VMConfig{
			Name:           "cp-0",
			StoragePool:    "flashstor",
			SkipZVolCreate: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})
}

func TestVMManagerListAndInfoOutput(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{{
					"id":          7,
					"name":        "cp-0",
					"description": "control plane",
					"memory":      8192,
					"vcpus":       4,
					"bootloader":  "UEFI",
					"autostart":   true,
					"status":      map[string]any{"state": "RUNNING"},
				}},
			}), nil
		case "vm.device.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{
					{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/cp-0-boot"}},
				},
			}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	output := captureStdout(t, func() {
		require.NoError(t, manager.ListVMs())
		require.NoError(t, manager.GetVMInfo("cp-0"))
	})

	assert.Contains(t, output, "cp-0")
	assert.Contains(t, output, "RUNNING")
	assert.Contains(t, output, "VM Information for: cp-0")
	assert.Contains(t, output, "Devices (1)")
}

func TestVMManagerStartVM(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	var startedIDs []int
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			return mustJSON(map[string]any{
				"result": []map[string]any{{"id": 9, "name": "cp-0"}},
			}), nil
		case "vm.device.query":
			return mustJSON(map[string]any{"result": []map[string]any{}}), nil
		case "vm.start":
			args := params.([]interface{})
			startedIDs = append(startedIDs, args[0].(int))
			return mustJSON(map[string]any{"result": true}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	require.NoError(t, manager.StartVM("cp-0"))
	assert.Equal(t, []int{9}, startedIDs)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}
