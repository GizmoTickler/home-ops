package proxmox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"homeops-cli/internal/common"

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMManagerDeployVM(t *testing.T) {
	t.Run("uses next VMID and powers on", func(t *testing.T) {
		createTask := &fakeTaskHandle{}
		startTask := &fakeTaskHandle{}
		startVM := &fakeVMHandle{name: "demo", vmid: 555, startTask: startTask}

		var createdVMID int
		var createdOptions []proxmox.VirtualMachineOption
		manager := &VMManager{
			client: &Client{ctx: context.Background()},
			logger: common.NewColorLogger(),
			listVMsFn: func() (proxmox.VirtualMachines, error) {
				return proxmox.VirtualMachines{}, nil
			},
			getNextVMIDFn: func() (int, error) {
				return 555, nil
			},
			createVMTaskFn: func(vmid int, options ...proxmox.VirtualMachineOption) (taskHandle, error) {
				createdVMID = vmid
				createdOptions = append([]proxmox.VirtualMachineOption{}, options...)
				return createTask, nil
			},
			getVMHandleFn: func(vmid int) (vmHandle, error) {
				assert.Equal(t, 555, vmid)
				return startVM, nil
			},
		}

		err := manager.DeployVM(VMConfig{
			Name:          "demo",
			Memory:        4096,
			Cores:         2,
			Sockets:       1,
			BootDiskSize:  32,
			BootStorage:   "local-lvm",
			NetworkBridge: "vmbr0",
			PowerOn:       true,
		})
		require.NoError(t, err)
		assert.Equal(t, 555, createdVMID)
		assert.Equal(t, 1, createTask.waits)
		assert.Equal(t, 1, startVM.startCalls)
		assert.Equal(t, 1, startTask.waits)
		assert.Contains(t, optionMap(createdOptions)["name"], "demo")
	})

	t.Run("fails on duplicate name", func(t *testing.T) {
		manager := &VMManager{
			client: &Client{ctx: context.Background()},
			logger: common.NewColorLogger(),
			listVMsFn: func() (proxmox.VirtualMachines, error) {
				return proxmox.VirtualMachines{
					&proxmox.VirtualMachine{Name: "demo", VMID: proxmox.StringOrUint64(201)},
				}, nil
			},
		}

		err := manager.DeployVM(VMConfig{Name: "demo"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("fails when create task wait fails", func(t *testing.T) {
		manager := &VMManager{
			client: &Client{ctx: context.Background()},
			logger: common.NewColorLogger(),
			listVMsFn: func() (proxmox.VirtualMachines, error) {
				return proxmox.VirtualMachines{}, nil
			},
			getNextVMIDFn: func() (int, error) {
				return 777, nil
			},
			createVMTaskFn: func(vmid int, options ...proxmox.VirtualMachineOption) (taskHandle, error) {
				return &fakeTaskHandle{waitErr: fmt.Errorf("create failed")}, nil
			},
		}

		err := manager.DeployVM(VMConfig{
			Name:          "demo",
			Memory:        4096,
			Cores:         2,
			Sockets:       1,
			BootDiskSize:  32,
			BootStorage:   "local-lvm",
			NetworkBridge: "vmbr0",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VM creation task failed")
	})
}

func TestVMManagerListAndInfoFormatting(t *testing.T) {
	manager := &VMManager{
		client: &Client{ctx: context.Background()},
		logger: common.NewColorLogger(),
		listVMsFn: func() (proxmox.VirtualMachines, error) {
			return proxmox.VirtualMachines{
				&proxmox.VirtualMachine{
					VMID:   proxmox.StringOrUint64(200),
					Name:   "cp-0",
					Status: "running",
					MaxMem: 8 * 1024 * 1024 * 1024,
					CPUs:   4,
					Uptime: 123,
				},
			}, nil
		},
		findVMByNameFn: func(name string) (*proxmox.VirtualMachine, error) {
			return &proxmox.VirtualMachine{
				VMID:   proxmox.StringOrUint64(200),
				Name:   name,
				Node:   "pve",
				Status: "running",
				Mem:    4 * 1024 * 1024 * 1024,
				MaxMem: 8 * 1024 * 1024 * 1024,
				CPUs:   4,
				Uptime: 123,
				VirtualMachineConfig: &proxmox.VirtualMachineConfig{
					Name:    name,
					Cores:   4,
					Sockets: 1,
					Bios:    "ovmf",
					SCSIHW:  "virtio-scsi-single",
				},
			}, nil
		},
	}

	output := captureStdout(t, func() {
		require.NoError(t, manager.ListVMs())
		require.NoError(t, manager.GetVMInfo("cp-0"))
	})

	assert.Contains(t, output, "VMID")
	assert.Contains(t, output, "cp-0")
	assert.Contains(t, output, "VM Information for: cp-0")
	assert.Contains(t, output, "SCSI HW: virtio-scsi-single")
}

func TestProxmoxFormattingHelpers(t *testing.T) {
	assert.Equal(t, "No virtual machines found.\n", formatVMList(nil))
	assert.Equal(t, "local:iso/talos.iso", GetISOPath("local", "talos.iso"))
}

func TestVMManagerListAndStartErrorPaths(t *testing.T) {
	t.Run("list empty and list error", func(t *testing.T) {
		manager := &VMManager{
			client: &Client{ctx: context.Background()},
			logger: common.NewColorLogger(),
			listVMsFn: func() (proxmox.VirtualMachines, error) {
				return proxmox.VirtualMachines{}, nil
			},
		}

		output := captureStdout(t, func() {
			require.NoError(t, manager.ListVMs())
		})
		assert.Contains(t, output, "No virtual machines found.")

		manager.listVMsFn = func() (proxmox.VirtualMachines, error) {
			return nil, fmt.Errorf("boom")
		}
		err := manager.ListVMs()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list VMs")
	})

	t.Run("start task wait failure", func(t *testing.T) {
		manager := &VMManager{
			client: &Client{ctx: context.Background()},
			logger: common.NewColorLogger(),
			lookupVMHandle: func(name string) (vmHandle, error) {
				return &fakeVMHandle{
					name:      name,
					vmid:      100,
					startTask: &fakeTaskHandle{waitErr: fmt.Errorf("start failed")},
				}, nil
			},
		}

		err := manager.StartVM("cp-0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "start task failed")
	})
}

func TestProxmoxClientGuardsAndHelpers(t *testing.T) {
	client := &Client{ctx: context.Background()}

	_, err := client.GetNextVMID()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")

	err = client.Connect("pve")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")

	manager := &VMManager{}
	require.NoError(t, manager.Close())
}

func TestGetVMNamesAndUploadISO(t *testing.T) {
	originalGetCredentials := getCredentialsFn
	originalNewVMManager := newVMManagerFn
	t.Cleanup(func() {
		getCredentialsFn = originalGetCredentials
		newVMManagerFn = originalNewVMManager
	})

	getCredentialsFn = func() (string, string, string, string, error) {
		return "host", "token", "secret", "pve", nil
	}

	manager := &VMManager{
		client: &Client{ctx: context.Background()},
		logger: common.NewColorLogger(),
		listVMsFn: func() (proxmox.VirtualMachines, error) {
			return proxmox.VirtualMachines{
				&proxmox.VirtualMachine{Name: "cp-0"},
				&proxmox.VirtualMachine{Name: "cp-1"},
			}, nil
		},
		uploadISOTaskFn: func(storageName, isoURL, filename string) (taskHandle, error) {
			assert.Equal(t, "local", storageName)
			assert.Equal(t, "https://example.test/talos.iso", isoURL)
			assert.Equal(t, "talos.iso", filename)
			return &fakeTaskHandle{}, nil
		},
	}

	newVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (*VMManager, error) {
		assert.Equal(t, "host", host)
		assert.Equal(t, "token", tokenID)
		assert.Equal(t, "secret", secret)
		assert.Equal(t, "pve", nodeName)
		assert.True(t, insecure)
		return manager, nil
	}

	names, err := GetVMNames()
	require.NoError(t, err)
	assert.Equal(t, []string{"cp-0", "cp-1"}, names)

	require.NoError(t, manager.UploadISOFromURL("https://example.test/talos.iso", "talos.iso", "local"))
}

func TestGetVMNamesAndUploadISOErrors(t *testing.T) {
	originalGetCredentials := getCredentialsFn
	originalNewVMManager := newVMManagerFn
	t.Cleanup(func() {
		getCredentialsFn = originalGetCredentials
		newVMManagerFn = originalNewVMManager
	})

	getCredentialsFn = func() (string, string, string, string, error) {
		return "", "", "", "", fmt.Errorf("missing creds")
	}
	_, err := GetVMNames()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing creds")

	manager := &VMManager{
		client: &Client{ctx: context.Background()},
		logger: common.NewColorLogger(),
		uploadISOTaskFn: func(storageName, isoURL, filename string) (taskHandle, error) {
			return nil, fmt.Errorf("upload init failed")
		},
	}
	err = manager.UploadISOFromURL("https://example.test/talos.iso", "talos.iso", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initiate ISO download")

	manager.uploadISOTaskFn = func(storageName, isoURL, filename string) (taskHandle, error) {
		return &fakeTaskHandle{waitErr: fmt.Errorf("download failed")}, nil
	}
	err = manager.UploadISOFromURL("https://example.test/talos.iso", "talos.iso", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ISO download failed")
}

func optionMap(options []proxmox.VirtualMachineOption) map[string]string {
	result := make(map[string]string, len(options))
	for _, opt := range options {
		result[opt.Name] = fmt.Sprint(opt.Value)
	}
	return result
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
