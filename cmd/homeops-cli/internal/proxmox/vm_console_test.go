package proxmox

import (
	"context"
	"testing"

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/common"
)

func TestConsoleURLs(t *testing.T) {
	manager := &VMManager{
		client:   &Client{ctx: context.Background()},
		logger:   common.NewColorLogger(),
		host:     "pve.example.net",
		nodeName: "pve",
		findVMByNameFn: func(name string) (*proxmox.VirtualMachine, error) {
			require.Equal(t, "dev0", name)
			return &proxmox.VirtualMachine{Name: "dev0", VMID: proxmox.StringOrUint64(104)}, nil
		},
	}

	novnc, xtermjs, err := manager.ConsoleURLs("dev0")
	require.NoError(t, err)
	assert.Equal(t, "https://pve.example.net:8006/?console=kvm&novnc=1&vmid=104&vmname=dev0&node=pve", novnc)
	assert.Equal(t, "https://pve.example.net:8006/?console=kvm&xtermjs=1&vmid=104&vmname=dev0&node=pve", xtermjs)

	url, err := manager.ConsoleURL("dev0")
	require.NoError(t, err)
	assert.Equal(t, novnc, url)
}

func TestConsoleURLUnknownVM(t *testing.T) {
	manager := &VMManager{
		client: &Client{ctx: context.Background()},
		logger: common.NewColorLogger(),
		findVMByNameFn: func(name string) (*proxmox.VirtualMachine, error) {
			return nil, assert.AnError
		},
	}
	_, err := manager.ConsoleURL("ghost")
	require.Error(t, err)
}

func TestImportTemplate(t *testing.T) {
	createTask := &fakeTaskHandle{}
	var converted []string
	var createdOptions []proxmox.VirtualMachineOption

	manager := &VMManager{
		client: &Client{ctx: context.Background()},
		logger: common.NewColorLogger(),
		listVMsFn: func() (proxmox.VirtualMachines, error) {
			return proxmox.VirtualMachines{}, nil
		},
		getNextVMIDFn: func() (int, error) { return 9000, nil },
		createVMTaskFn: func(vmid int, options ...proxmox.VirtualMachineOption) (taskHandle, error) {
			createdOptions = append([]proxmox.VirtualMachineOption{}, options...)
			return createTask, nil
		},
		getVMHandleFn: func(vmid int) (vmHandle, error) {
			t.Fatalf("template import must not power on the VM (got start for vmid %d)", vmid)
			return nil, nil
		},
		verifyStorageFn:     func(string) error { return nil },
		convertToTemplateFn: func(name string) error { converted = append(converted, name); return nil },
	}

	err := manager.ImportTemplate(VMConfig{
		Name:          "ubuntu-tpl",
		Memory:        2048,
		Cores:         2,
		Sockets:       1,
		BootDiskSize:  10,
		BootStorage:   "local-lvm",
		ImageDiskPath: "/var/lib/vz/template/cache/noble.qcow2",
		NetworkBridge: "vmbr0",
		PowerOn:       true, // must be forced off by ImportTemplate
		CloudInit:     &CloudInitConfig{User: "ubuntu", IPConfig: "ip=dhcp"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ubuntu-tpl"}, converted)
	assert.Equal(t, 1, createTask.waits)
	assert.NotEmpty(t, createdOptions)
}

func TestImportTemplateDeployFailure(t *testing.T) {
	manager := &VMManager{
		client: &Client{ctx: context.Background()},
		logger: common.NewColorLogger(),
		listVMsFn: func() (proxmox.VirtualMachines, error) {
			return proxmox.VirtualMachines{
				&proxmox.VirtualMachine{Name: "ubuntu-tpl", VMID: proxmox.StringOrUint64(9000)},
			}, nil
		},
		convertToTemplateFn: func(name string) error {
			t.Fatal("must not convert when deploy fails")
			return nil
		},
	}
	err := manager.ImportTemplate(VMConfig{Name: "ubuntu-tpl"})
	require.ErrorContains(t, err, "already exists")
}
