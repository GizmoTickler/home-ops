package vsphere

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestESXiSSHClientHelpers(t *testing.T) {
	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	sshPath := filepath.Join(scriptDir, "ssh")
	require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nprintf 'PRIVATE-KEY'\n"), 0o755))
	require.NoError(t, os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf 'SSH-OK'\n"), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	client, err := NewESXiSSHClient("esxi.local", "root")
	require.NoError(t, err)
	assert.NotEmpty(t, client.keyFile)

	output, err := client.ExecuteCommand("echo test")
	require.NoError(t, err)
	assert.Contains(t, output, "SSH-OK")

	vmx := client.generateK8sVMX(GetK8sVMConfig("k8s-0"), "/vmfs/volumes/local/k8s-0", "/vmfs/volumes/openebs")
	assert.Contains(t, vmx, `displayName = "k8s-0"`)
	assert.Contains(t, vmx, `pciPassthru31.id = "00000:004:00.0"`)

	keyFile := client.keyFile
	client.Close()
	_, err = os.Stat(keyFile)
	assert.True(t, os.IsNotExist(err))
}

func TestESXiSSHClientExecuteCommandError(t *testing.T) {
	originalSSHCombinedOutput := sshCombinedOutputFn
	t.Cleanup(func() { sshCombinedOutputFn = originalSSHCombinedOutput })

	sshCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "ssh", name)
		require.Equal(t, "echo test", args[len(args)-1])
		return []byte("permission denied token: synthetic-test-fixture"), errors.New("exit status 255")
	}

	client := &ESXiSSHClient{
		host:     "esxi.local",
		username: "root",
		keyFile:  "/tmp/key",
		logger:   common.NewColorLogger(),
	}

	output, err := client.ExecuteCommand("echo test")
	require.Error(t, err)
	assert.Contains(t, output, "permission denied")
	assert.NotContains(t, output, "synthetic-test-fixture")
	assert.Contains(t, err.Error(), "SSH command failed")
	assert.NotContains(t, err.Error(), "synthetic-test-fixture")
}

func TestESXiSSHClientCreateK8sVM(t *testing.T) {
	originalSSHCombinedOutput := sshCombinedOutputFn
	t.Cleanup(func() { sshCombinedOutputFn = originalSSHCombinedOutput })

	t.Run("success", func(t *testing.T) {
		var commands []string
		sshCombinedOutputFn = func(_ string, args ...string) ([]byte, error) {
			command := args[len(args)-1]
			commands = append(commands, command)
			if strings.Contains(command, "vim-cmd solo/registervm") {
				return []byte("Registered virtual machine: 123\n"), nil
			}
			return []byte("ok"), nil
		}

		client := &ESXiSSHClient{
			host:     "esxi.local",
			username: "root",
			keyFile:  "/tmp/key",
			logger:   common.NewColorLogger(),
		}

		err := client.CreateK8sVM(GetK8sVMConfig("k8s-0"))
		require.NoError(t, err)
		require.Len(t, commands, 7)
		assert.Contains(t, commands[0], "mkdir -p '/vmfs/volumes/local-nvme1/k8s-0'")
		assert.Contains(t, commands[1], "mkdir -p '/vmfs/volumes/truenas-iscsi/k8s-0'")
		assert.Contains(t, commands[2], "vmkfstools -c 250G -d thin '/vmfs/volumes/local-nvme1/k8s-0/k8s-0.vmdk'")
		assert.Contains(t, commands[4], "cat > '/vmfs/volumes/local-nvme1/k8s-0/k8s-0.vmx' << 'VMXEOF'")
		assert.Contains(t, commands[5], "vim-cmd solo/registervm '/vmfs/volumes/local-nvme1/k8s-0/k8s-0.vmx'")
		assert.Equal(t, "vim-cmd vmsvc/power.on 123", commands[6])
	})

	t.Run("fails on command error", func(t *testing.T) {
		sshCombinedOutputFn = func(_ string, args ...string) ([]byte, error) {
			command := args[len(args)-1]
			if strings.Contains(command, "vmkfstools -c 250G") {
				return []byte("boot disk failure"), errors.New("exit status 1")
			}
			return []byte("ok"), nil
		}

		client := &ESXiSSHClient{
			host:     "esxi.local",
			username: "root",
			keyFile:  "/tmp/key",
			logger:   common.NewColorLogger(),
		}

		err := client.CreateK8sVM(GetK8sVMConfig("k8s-0"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create boot disk")
	})

	t.Run("fails on invalid register output", func(t *testing.T) {
		sshCombinedOutputFn = func(_ string, args ...string) ([]byte, error) {
			command := args[len(args)-1]
			if strings.Contains(command, "vim-cmd solo/registervm") {
				return []byte("registered without id password: synthetic-test-fixture"), nil
			}
			return []byte("ok"), nil
		}

		client := &ESXiSSHClient{
			host:     "esxi.local",
			username: "root",
			keyFile:  "/tmp/key",
			logger:   common.NewColorLogger(),
		}

		err := client.CreateK8sVM(GetK8sVMConfig("k8s-0"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse registered VM ID")
		assert.NotContains(t, err.Error(), "synthetic-test-fixture")
	})
}
