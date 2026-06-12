package truenas

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSSHRunner struct {
	commands []string
	uploads  map[string][]byte
	execErr  error
}

func (f *fakeSSHRunner) ExecuteCommand(command string) (string, error) {
	f.commands = append(f.commands, command)
	return "", f.execErr
}

func (f *fakeSSHRunner) UploadBytes(content []byte, remotePath string) error {
	if f.uploads == nil {
		f.uploads = map[string][]byte{}
	}
	f.uploads[remotePath] = content
	return nil
}

func cloudVMManager(t *testing.T) (*VMManager, *[]recordedCall) {
	t.Helper()
	manager := NewVMManager("nas", "key", 443, true)
	calls := &[]recordedCall{}
	started := false
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		*calls = append(*calls, recordedCall{method, params})
		switch method {
		case "vm.query":
			return mustJSON(map[string]any{"result": []map[string]any{}}), nil
		case "pool.dataset.query":
			return mustJSON(map[string]any{"result": []map[string]any{
				{"name": "tank", "type": "FILESYSTEM"},
				{"name": "tank/VM", "type": "FILESYSTEM"},
			}}), nil
		case "pool.dataset.create":
			return mustJSON(map[string]any{"result": true}), nil
		case "vm.create":
			return mustJSON(map[string]any{"result": map[string]any{"id": 88, "name": "dev0"}}), nil
		case "vm.device.create":
			return mustJSON(map[string]any{"result": true}), nil
		case "vm.start":
			started = true
			return mustJSON(map[string]any{"result": nil}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	_ = started
	return manager, calls
}

func TestCreateCloudImageVM(t *testing.T) {
	manager, calls := cloudVMManager(t)
	sshExec := &fakeSSHRunner{}

	err := manager.CreateCloudImageVM(CloudImageVMConfig{
		Name:          "dev0",
		MemoryMB:      4096,
		VCPUs:         2,
		DiskGB:        40,
		Pool:          "tank/VM",
		ImageRef:      "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		ImageDir:      "/mnt/tank/images",
		SeedISO:       []byte("ISO"),
		NetworkBridge: "br0",
		PowerOn:       true,
	}, sshExec)
	require.NoError(t, err)

	// image staged and converted over SSH, with quoted paths
	require.Len(t, sshExec.commands, 2)
	assert.Contains(t, sshExec.commands[0], "wget -q -O '/mnt/tank/images/noble-server-cloudimg-amd64.img'")
	assert.Contains(t, sshExec.commands[1], "qemu-img convert -O raw '/mnt/tank/images/noble-server-cloudimg-amd64.img' '/dev/zvol/tank/VM/dev0-boot'")

	// seed ISO uploaded
	assert.Equal(t, []byte("ISO"), sshExec.uploads["/mnt/tank/images/dev0-seed.iso"])

	// zvol created, VM created, three devices, started
	var methods []string
	for _, c := range *calls {
		methods = append(methods, c.method)
	}
	assert.Contains(t, methods, "pool.dataset.create")
	assert.Contains(t, methods, "vm.create")
	assert.Contains(t, methods, "vm.start")
	deviceCreates := methodCalls(*calls, "vm.device.create")
	require.Len(t, deviceCreates, 3)

	var dtypes []string
	var cdromPath string
	for _, dc := range deviceCreates {
		device := dc.params.([]interface{})[0].(map[string]interface{})
		attrs := device["attributes"].(map[string]interface{})
		dtype := attrs["dtype"].(string)
		dtypes = append(dtypes, dtype)
		if dtype == "CDROM" {
			cdromPath = attrs["path"].(string)
		}
	}
	assert.ElementsMatch(t, []string{"DISK", "NIC", "CDROM"}, dtypes)
	assert.Equal(t, "/mnt/tank/images/dev0-seed.iso", cdromPath)
}

func TestCreateCloudImageVMLocalPath(t *testing.T) {
	manager, _ := cloudVMManager(t)
	sshExec := &fakeSSHRunner{}

	err := manager.CreateCloudImageVM(CloudImageVMConfig{
		Name:          "dev1",
		MemoryMB:      2048,
		VCPUs:         1,
		DiskGB:        20,
		Pool:          "tank/VM",
		ImageRef:      "/mnt/tank/images/custom.qcow2",
		ImageDir:      "/mnt/tank/images",
		SeedISO:       []byte("ISO"),
		NetworkBridge: "br0",
	}, sshExec)
	require.NoError(t, err)
	// no staging command for NAS-local images, only the convert
	require.Len(t, sshExec.commands, 1)
	assert.True(t, strings.Contains(sshExec.commands[0], "qemu-img convert"))
}

func TestCreateCloudImageVMValidation(t *testing.T) {
	manager, _ := cloudVMManager(t)

	err := manager.CreateCloudImageVM(CloudImageVMConfig{Name: "x", ImageDir: "/mnt/x"}, &fakeSSHRunner{})
	require.ErrorContains(t, err, "no storage pool")

	err = manager.CreateCloudImageVM(CloudImageVMConfig{Name: "x", Pool: "tank"}, &fakeSSHRunner{})
	require.ErrorContains(t, err, "no image staging dir")
}

func TestCreateCloudImageVMDuplicate(t *testing.T) {
	manager := NewVMManager("nas", "key", 443, true)
	manager.client.callFn = func(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
		if method == "vm.query" {
			return mustJSON(map[string]any{"result": []map[string]any{{"id": 1, "name": "dev0"}}}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	err := manager.CreateCloudImageVM(CloudImageVMConfig{
		Name: "dev0", Pool: "tank/VM", ImageDir: "/mnt/tank/images",
	}, &fakeSSHRunner{})
	require.ErrorContains(t, err, "already exists")
}
