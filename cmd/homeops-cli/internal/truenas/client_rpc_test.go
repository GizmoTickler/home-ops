package truenas

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkingClientQueryAndMutationHelpers(t *testing.T) {
	client := NewWorkingClient("nas", "key", 443, true)
	client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			return mustJSON(map[string]interface{}{
				"result": []map[string]interface{}{
					{"id": 1, "name": "vm1"},
				},
			}), nil
		case "vm.device.query":
			return mustJSON(map[string]interface{}{
				"result": []map[string]interface{}{
					{"dtype": "DISK"},
				},
			}), nil
		case "vm.create":
			return mustJSON(map[string]interface{}{
				"result": map[string]interface{}{"id": 2, "name": "vm2"},
			}), nil
		case "pool.dataset.query":
			return mustJSON(map[string]interface{}{
				"result": []map[string]interface{}{
					{"name": "pool/ds", "pool": "pool"},
				},
			}), nil
		case "pool.dataset.create":
			return mustJSON(Dataset{Name: "pool/new", Pool: "pool"}), nil
		case "pool.dataset.delete", "vm.start", "vm.stop", "vm.poweroff", "vm.delete":
			return mustJSON(map[string]any{"result": true}), nil
		case "vm.bootloader_options":
			return mustJSON(map[string]string{"UEFI": "UEFI"}), nil
		case "vm.cpu_model_choices":
			return mustJSON(map[string]string{"host": "host"}), nil
		case "vm.random_mac":
			return mustJSON("00:11:22:33:44:55"), nil
		case "vm.get_available_memory":
			return mustJSON(1024), nil
		case "vm.maximum_supported_vcpus":
			return mustJSON(16), nil
		case "vm.device.disk_choices":
			return mustJSON([]string{"disk1"}), nil
		case "vm.device.nic_attach_choices":
			return mustJSON([]string{"nic1"}), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	vms, err := client.QueryVMs(nil)
	require.NoError(t, err)
	require.Len(t, vms, 1)
	assert.Equal(t, "vm1", vms[0].Name)
	assert.Len(t, vms[0].Devices, 1)

	vm, err := client.CreateVM(map[string]interface{}{"name": "vm2"})
	require.NoError(t, err)
	assert.Equal(t, "vm2", vm.Name)

	require.NoError(t, client.StartVM(1))
	require.NoError(t, client.StopVM(1))
	require.NoError(t, client.PowerOffVM(1))
	require.NoError(t, client.DeleteVM(1))

	devices, err := client.QueryVMDevices(1)
	require.NoError(t, err)
	require.Len(t, devices, 1)

	datasets, err := client.QueryDatasets(nil)
	require.NoError(t, err)
	require.Len(t, datasets, 1)

	dataset, err := client.CreateDataset(DatasetCreateRequest{Name: "pool/new"})
	require.NoError(t, err)
	assert.Equal(t, "pool/new", dataset.Name)
	require.NoError(t, client.DeleteDataset("pool/new", true))

	_, err = client.GetVMBootloaderOptions()
	require.NoError(t, err)
	_, err = client.GetVMCPUModelChoices()
	require.NoError(t, err)
	mac, err := client.GetRandomMAC()
	require.NoError(t, err)
	assert.Equal(t, "00:11:22:33:44:55", mac)
	_, err = client.GetAvailableMemory()
	require.NoError(t, err)
	_, err = client.GetMaxSupportedVCPUs()
	require.NoError(t, err)
	_, err = client.GetDeviceDiskChoices()
	require.NoError(t, err)
	_, err = client.GetDeviceNICAttachChoices()
	require.NoError(t, err)
}

func TestWorkingClientRPCErrorPaths(t *testing.T) {
	client := NewWorkingClient("nas", "key", 443, true)
	client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			return mustJSON(map[string]interface{}{"noresult": true}), nil
		case "vm.device.query":
			return mustJSON(map[string]interface{}{"result": "bad"}), nil
		case "pool.dataset.query":
			return []byte("{"), nil
		default:
			return nil, fmt.Errorf("boom")
		}
	}

	_, err := client.QueryVMs(nil)
	require.Error(t, err)

	_, err = client.QueryVMDevices(1)
	require.Error(t, err)

	_, err = client.QueryDatasets(nil)
	require.Error(t, err)

	_, err = client.CreateVM(map[string]interface{}{})
	require.Error(t, err)
}

func mustJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
