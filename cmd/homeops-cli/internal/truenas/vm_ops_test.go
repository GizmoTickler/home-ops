package truenas

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opsTestManager wires a VMManager whose RPC layer answers vm.query /
// vm.device.query for one VM ("web0", ID 7, boot+openebs zvols) and records
// every other call for assertions.
type recordedCall struct {
	method string
	params interface{}
}

func opsTestManager(t *testing.T, state string, extra func(method string, params interface{}) (json.RawMessage, error)) (*VMManager, *[]recordedCall) {
	t.Helper()
	manager := NewVMManager("nas", "key", 443, true)
	calls := &[]recordedCall{}
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		*calls = append(*calls, recordedCall{method, params})
		switch method {
		case "vm.query":
			return mustJSON(map[string]any{"result": []map[string]any{
				{"id": 7, "name": "web0", "memory": 4096, "vcpus": 2, "status": map[string]any{"state": state}},
			}}), nil
		case "vm.device.query":
			return mustJSON(map[string]any{"result": []map[string]any{
				{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/web0-boot"}},
				{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/web0-openebs"}},
				{"attributes": map[string]any{"dtype": "DISPLAY", "type": "SPICE", "bind": "0.0.0.0", "port": float64(5900), "web": true, "web_port": float64(5901)}},
			}}), nil
		}
		if extra != nil {
			return extra(method, params)
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	return manager, calls
}

func methodCalls(calls []recordedCall, method string) []recordedCall {
	var out []recordedCall
	for _, c := range calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func TestSetVMResources(t *testing.T) {
	t.Run("updates memory and vcpus", func(t *testing.T) {
		manager, calls := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			if method == "vm.update" {
				return mustJSON(map[string]any{"result": true}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		require.NoError(t, manager.SetVMResources("web0", 8192, 4))
		updates := methodCalls(*calls, "vm.update")
		require.Len(t, updates, 1)
		args := updates[0].params.([]interface{})
		assert.Equal(t, 7, args[0])
		assert.Equal(t, map[string]interface{}{"memory": 8192, "vcpus": 4}, args[1])
	})

	t.Run("memory only", func(t *testing.T) {
		manager, calls := opsTestManager(t, "RUNNING", func(method string, params interface{}) (json.RawMessage, error) {
			return mustJSON(map[string]any{"result": true}), nil
		})
		require.NoError(t, manager.SetVMResources("web0", 8192, 0))
		args := methodCalls(*calls, "vm.update")[0].params.([]interface{})
		assert.Equal(t, map[string]interface{}{"memory": 8192}, args[1])
	})

	t.Run("nothing to change", func(t *testing.T) {
		manager, _ := opsTestManager(t, "STOPPED", nil)
		err := manager.SetVMResources("web0", 0, 0)
		require.ErrorContains(t, err, "nothing to change")
	})

	t.Run("unknown VM", func(t *testing.T) {
		manager, _ := opsTestManager(t, "STOPPED", nil)
		err := manager.SetVMResources("nope", 1024, 0)
		require.ErrorContains(t, err, "not found")
	})
}

func TestResolveVMDisk(t *testing.T) {
	zvols := []string{"flashstor/VM/web0-boot", "flashstor/VM/web0-openebs"}

	got, err := resolveVMDisk(zvols, "")
	require.NoError(t, err)
	assert.Equal(t, "flashstor/VM/web0-boot", got)

	got, err = resolveVMDisk(zvols, "openebs")
	require.NoError(t, err)
	assert.Equal(t, "flashstor/VM/web0-openebs", got)

	got, err = resolveVMDisk(zvols, "flashstor/VM/web0-boot")
	require.NoError(t, err)
	assert.Equal(t, "flashstor/VM/web0-boot", got)

	// single-disk VM: "boot" selects the only disk even without -boot suffix
	got, err = resolveVMDisk([]string{"flashstor/VM/solo"}, "boot")
	require.NoError(t, err)
	assert.Equal(t, "flashstor/VM/solo", got)

	_, err = resolveVMDisk(zvols, "missing")
	require.ErrorContains(t, err, "no disk matching")

	_, err = resolveVMDisk([]string{"a-data", "b-data"}, "data")
	require.ErrorContains(t, err, "ambiguous")
}

func TestResizeVMDisk(t *testing.T) {
	t.Run("grow by delta", func(t *testing.T) {
		manager, calls := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			switch method {
			case "pool.dataset.query":
				return mustJSON(map[string]any{"result": []map[string]any{
					{"id": "flashstor/VM/web0-boot", "type": "VOLUME", "volsize": map[string]any{"parsed": float64(40 << 30)}},
				}}), nil
			case "pool.dataset.update":
				return mustJSON(map[string]any{"result": true}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		require.NoError(t, manager.ResizeVMDisk("web0", "boot", "+20G"))
		updates := methodCalls(*calls, "pool.dataset.update")
		require.Len(t, updates, 1)
		args := updates[0].params.([]interface{})
		assert.Equal(t, "flashstor/VM/web0-boot", args[0])
		assert.Equal(t, map[string]interface{}{"volsize": int64(60 << 30)}, args[1])
	})

	t.Run("absolute must grow", func(t *testing.T) {
		manager, _ := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			if method == "pool.dataset.query" {
				return mustJSON(map[string]any{"result": []map[string]any{
					{"id": "flashstor/VM/web0-boot", "type": "VOLUME", "volsize": map[string]any{"parsed": float64(40 << 30)}},
				}}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		err := manager.ResizeVMDisk("web0", "boot", "20G")
		require.ErrorContains(t, err, "only grow")
	})
}

func TestRestartVM(t *testing.T) {
	manager, calls := opsTestManager(t, "RUNNING", func(method string, params interface{}) (json.RawMessage, error) {
		if method == "vm.restart" {
			return mustJSON(map[string]any{"result": nil}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	})
	require.NoError(t, manager.RestartVM("web0"))
	restarts := methodCalls(*calls, "vm.restart")
	require.Len(t, restarts, 1)
	assert.Equal(t, []interface{}{7}, restarts[0].params)
}

func TestSnapshotVM(t *testing.T) {
	manager, calls := opsTestManager(t, "RUNNING", func(method string, params interface{}) (json.RawMessage, error) {
		if method == "pool.snapshot.create" {
			return mustJSON(map[string]any{"result": true}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	})
	require.NoError(t, manager.SnapshotVM("web0", "pre-upgrade"))
	creates := methodCalls(*calls, "pool.snapshot.create")
	require.Len(t, creates, 2)
	first := creates[0].params.([]interface{})[0].(map[string]interface{})
	assert.Equal(t, "flashstor/VM/web0-boot", first["dataset"])
	assert.Equal(t, "pre-upgrade", first["name"])
}

func TestListVMSnapshots(t *testing.T) {
	manager, _ := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
		if method == "pool.snapshot.query" {
			filters := params.([]interface{})[0].([]interface{})[0].([]interface{})
			ds := filters[2].(string)
			return mustJSON(map[string]any{"result": []map[string]any{
				{"id": ds + "@pre-upgrade", "dataset": ds, "snapshot_name": "pre-upgrade",
					"properties": map[string]any{"creation": map[string]any{"value": "2026-06-12 10:00:00"}}},
			}}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	})
	require.NoError(t, manager.ListVMSnapshots("web0"))
}

func TestRollbackVM(t *testing.T) {
	t.Run("running VM refuses", func(t *testing.T) {
		manager, _ := opsTestManager(t, "RUNNING", nil)
		err := manager.RollbackVM("web0", "pre-upgrade")
		require.ErrorContains(t, err, "stop it before rolling back")
	})

	t.Run("rolls back every zvol", func(t *testing.T) {
		manager, calls := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			if method == "pool.snapshot.rollback" {
				return mustJSON(map[string]any{"result": nil}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		require.NoError(t, manager.RollbackVM("web0", "pre-upgrade"))
		rollbacks := methodCalls(*calls, "pool.snapshot.rollback")
		require.Len(t, rollbacks, 2)
		args := rollbacks[0].params.([]interface{})
		assert.Equal(t, "flashstor/VM/web0-boot@pre-upgrade", args[0])
		assert.Equal(t, map[string]interface{}{"force": true}, args[1])
	})
}

func TestDeleteVMSnapshot(t *testing.T) {
	t.Run("deletes where present", func(t *testing.T) {
		manager, calls := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			switch method {
			case "pool.snapshot.query":
				filters := params.([]interface{})[0].([]interface{})[0].([]interface{})
				ds := filters[2].(string)
				if ds != "flashstor/VM/web0-boot" {
					return mustJSON(map[string]any{"result": []map[string]any{}}), nil
				}
				return mustJSON(map[string]any{"result": []map[string]any{
					{"id": ds + "@old", "dataset": ds, "snapshot_name": "old"},
				}}), nil
			case "pool.snapshot.delete":
				return mustJSON(map[string]any{"result": true}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		require.NoError(t, manager.DeleteVMSnapshot("web0", "old"))
		deletes := methodCalls(*calls, "pool.snapshot.delete")
		require.Len(t, deletes, 1)
		assert.Equal(t, []interface{}{"flashstor/VM/web0-boot@old"}, deletes[0].params)
	})

	t.Run("missing snapshot errors", func(t *testing.T) {
		manager, _ := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			if method == "pool.snapshot.query" {
				return mustJSON(map[string]any{"result": []map[string]any{}}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		err := manager.DeleteVMSnapshot("web0", "ghost")
		require.ErrorContains(t, err, "not found on any zvol")
	})
}

func TestCloneVMTrueNAS(t *testing.T) {
	t.Run("clones by id", func(t *testing.T) {
		manager, calls := opsTestManager(t, "STOPPED", func(method string, params interface{}) (json.RawMessage, error) {
			if method == "vm.clone" {
				return mustJSON(map[string]any{"result": true}), nil
			}
			return nil, fmt.Errorf("unexpected method %s", method)
		})
		require.NoError(t, manager.CloneVM("web0", "web1"))
		clones := methodCalls(*calls, "vm.clone")
		require.Len(t, clones, 1)
		assert.Equal(t, []interface{}{7, "web1"}, clones[0].params)
	})

	t.Run("refuses existing target", func(t *testing.T) {
		manager, _ := opsTestManager(t, "STOPPED", nil)
		err := manager.CloneVM("web0", "web0")
		require.ErrorContains(t, err, "already exists")
	})
}

func TestVMDisplayInfo(t *testing.T) {
	manager, _ := opsTestManager(t, "RUNNING", nil)
	info, err := manager.VMDisplayInfo("web0")
	require.NoError(t, err)
	assert.Equal(t, "SPICE", info.Type)
	assert.Equal(t, "0.0.0.0", info.Bind)
	assert.Equal(t, 5900, info.Port)
	assert.Equal(t, 5901, info.WebPort)
}

func TestVMDisplayInfoMissing(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		switch method {
		case "vm.query":
			return mustJSON(map[string]any{"result": []map[string]any{{"id": 7, "name": "web0"}}}), nil
		case "vm.device.query":
			return mustJSON(map[string]any{"result": []map[string]any{
				{"attributes": map[string]any{"dtype": "DISK", "path": "/dev/zvol/flashstor/VM/web0-boot"}},
			}}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	_, err := manager.VMDisplayInfo("web0")
	require.ErrorContains(t, err, "no display device")
}

func TestGetZvolSize(t *testing.T) {
	client := NewWorkingClient("nas", "key", 443, true)
	client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		require.Equal(t, "pool.dataset.query", method)
		return mustJSON(map[string]any{"result": []map[string]any{
			{"id": "flashstor/VM/web0-boot", "type": "VOLUME", "volsize": map[string]any{"parsed": float64(40 << 30)}},
		}}), nil
	}
	size, err := client.GetZvolSize("flashstor/VM/web0-boot")
	require.NoError(t, err)
	assert.Equal(t, int64(40<<30), size)
}

func TestGetZvolSizeNotVolume(t *testing.T) {
	client := NewWorkingClient("nas", "key", 443, true)
	client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		return mustJSON(map[string]any{"result": []map[string]any{
			{"id": "flashstor/VM", "type": "FILESYSTEM"},
		}}), nil
	}
	_, err := client.GetZvolSize("flashstor/VM")
	require.ErrorContains(t, err, "not a VOLUME")
}
