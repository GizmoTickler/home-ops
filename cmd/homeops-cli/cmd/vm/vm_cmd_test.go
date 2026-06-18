package vm

import (
	"testing"

	"homeops-cli/internal/constants"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/testutil"
	"homeops-cli/internal/vmlifecycle"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMLifecycleProviderCapabilityError(t *testing.T) {
	calls, _ := injectFakeVMLifecycle(t)

	stubUnavailable1PasswordCLI(t)
	t.Setenv(constants.EnvTrueNASHost, "")
	t.Setenv(constants.EnvTrueNASAPIKey, "")

	_, err := testutil.ExecuteCommand(newStartVMCommand(), "--provider", "truenas", "--name", "tn-vm")
	require.Error(t, err)
	assert.Empty(t, *calls, "provider operation should not run when config-level prerequisites are missing")
	assert.Contains(t, err.Error(), "TrueNAS VM lifecycle commands require")
	assert.Contains(t, err.Error(), "homeops-cli vm start --provider truenas --name <vm-name>")
	assert.Contains(t, err.Error(), constants.EnvTrueNASHost)
	assert.Contains(t, err.Error(), constants.EnvTrueNASAPIKey)
}

func TestPowerOffVMDispatchesForceStop(t *testing.T) {
	calls, _ := injectFakeVMLifecycle(t)

	require.NoError(t, powerOffVM("tn-vm", "truenas"))
	require.NoError(t, powerOffVM("px-vm", "proxmox"))

	assert.Equal(t, []string{
		"stop-truenas:tn-vm:true",
		"stop-proxmox:px-vm:true",
	}, *calls)
}

func TestProviderLifecycleDispatch(t *testing.T) {
	calls, closed := injectFakeVMLifecycle(t)

	require.NoError(t, startVMWithProvider("tn-vm", "truenas"))
	require.NoError(t, startVMWithProvider("px-vm", "proxmox"))
	require.NoError(t, startVMWithProvider("esx-vm", "vsphere"))
	require.NoError(t, stopVMWithProvider("tn-vm", "truenas"))
	require.NoError(t, stopVMWithProvider("px-vm", "proxmox"))
	require.NoError(t, stopVMWithProvider("esx-vm", "vsphere"))
	require.NoError(t, infoVMWithProvider("tn-vm", "truenas"))
	require.NoError(t, infoVMWithProvider("px-vm", "proxmox"))
	require.NoError(t, infoVMWithProvider("esx-vm", "vsphere"))
	require.NoError(t, deleteVMWithConfirmation("tn-vm", "truenas", true))
	require.NoError(t, deleteVMWithConfirmation("px-vm", "proxmox", true))
	require.NoError(t, deleteVMWithConfirmation("esx-vm", "vsphere", true))
	require.NoError(t, powerOnVM("tn-vm", "truenas"))
	require.NoError(t, powerOnVM("px-vm", "proxmox"))
	require.NoError(t, powerOnVM("esx-vm", "vsphere"))
	require.NoError(t, powerOffVM("esx-vm", "vsphere"))
	require.NoError(t, listVMs("proxmox", "table"))

	assert.Equal(t, []string{
		"start-truenas:tn-vm",
		"start-proxmox:px-vm",
		"start-vsphere:esx-vm",
		"stop-truenas:tn-vm:false",
		"stop-proxmox:px-vm:false",
		"stop-vsphere:esx-vm:false",
		"info-truenas:tn-vm",
		"info-proxmox:px-vm",
		"info-vsphere:esx-vm",
		"delete-truenas:tn-vm",
		"delete-proxmox:px-vm",
		"delete-vsphere:esx-vm",
		"start-truenas:tn-vm",
		"start-proxmox:px-vm",
		"start-vsphere:esx-vm",
		"stop-vsphere:esx-vm:true",
		"list-proxmox",
	}, *calls)
	assert.Equal(t, 17, *closed, "every lifecycle instance must be closed")
}

func TestVMLifecycleCommandWrappers(t *testing.T) {
	oldEnsureProvider := vmlifecycle.EnsureVMLifecycleProviderFn
	oldFactory := vmlifecycle.NewVMLifecycleFn
	t.Cleanup(func() {
		vmlifecycle.EnsureVMLifecycleProviderFn = oldEnsureProvider
		vmlifecycle.NewVMLifecycleFn = oldFactory
	})

	calls := &[]string{}
	vmlifecycle.EnsureVMLifecycleProviderFn = func(provider, action string) error {
		*calls = append(*calls, "check:"+provider+":"+action)
		return nil
	}
	vmlifecycle.NewVMLifecycleFn = func(normalizedProvider string) (vmprov.VMLifecycle, error) {
		return &fakeVMLifecycle{provider: normalizedProvider, calls: calls}, nil
	}

	_, err := testutil.ExecuteCommand(newStartVMCommand(), "--provider", "proxmox", "--name", "px-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newStopVMCommand(), "--provider", "proxmox", "--name", "px-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newDeleteVMCommand(), "--provider", "proxmox", "--name", "px-vm", "--force")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newInfoVMCommand(), "--provider", "proxmox", "--name", "px-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newPowerOnVMCommand(), "--provider", "vsphere", "--name", "esx-vm")
	require.NoError(t, err)
	_, err = testutil.ExecuteCommand(newPowerOffVMCommand(), "--provider", "vsphere", "--name", "esx-vm")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"check:proxmox:start",
		"start-proxmox:px-vm",
		"check:proxmox:stop",
		"stop-proxmox:px-vm:false",
		"check:proxmox:delete",
		"delete-proxmox:px-vm",
		"check:proxmox:info",
		"info-proxmox:px-vm",
		"check:vsphere:poweron",
		"start-vsphere:esx-vm",
		"check:vsphere:poweroff",
		"stop-vsphere:esx-vm:true",
	}, *calls)
}

func TestDeleteAndCleanupConfirmationFlows(t *testing.T) {
	oldConfirm := confirmActionFn
	oldFactory := vmlifecycle.NewVMLifecycleFn
	oldTrueNASFactory := vmlifecycle.NewTrueNASVMManagerFn
	oldTrueNASCreds := vmlifecycle.GetTrueNASCredentialsFn
	t.Cleanup(func() {
		confirmActionFn = oldConfirm
		vmlifecycle.NewVMLifecycleFn = oldFactory
		vmlifecycle.NewTrueNASVMManagerFn = oldTrueNASFactory
		vmlifecycle.GetTrueNASCredentialsFn = oldTrueNASCreds
	})

	t.Run("delete vm confirmation uses provider-specific warning", func(t *testing.T) {
		var message string
		confirmActionFn = func(msg string, defaultYes bool) (bool, error) {
			message = msg
			return true, nil
		}
		calls := &[]string{}
		vmlifecycle.NewVMLifecycleFn = func(normalizedProvider string) (vmprov.VMLifecycle, error) {
			return &fakeVMLifecycle{provider: normalizedProvider, calls: calls}, nil
		}

		require.NoError(t, deleteVMWithConfirmation("tn-vm", "truenas", false))
		assert.Contains(t, message, "all its ZVols on TrueNAS")
		assert.Equal(t, []string{"delete-truenas:tn-vm"}, *calls)
	})

	t.Run("cleanup zvol command force wrapper", func(t *testing.T) {
		manager := &fakeTrueNASVMManager{}
		vmlifecycle.GetTrueNASCredentialsFn = func() (string, string, error) {
			return "truenas.local", "api-key", nil
		}
		vmlifecycle.NewTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) vmlifecycle.TrueNASVMManager {
			return manager
		}

		_, err := testutil.ExecuteCommand(newCleanupZVolsCommand(), "--vm-name", "tn-vm", "--force")
		require.NoError(t, err)
		assert.Equal(t, []string{"tn-vm:flashstor"}, manager.cleanupPairs)
	})
}

func TestHypervisorWrapperFlows(t *testing.T) {
	oldTrueNASFactory := vmlifecycle.NewTrueNASVMManagerFn
	oldProxmoxFactory := vmlifecycle.NewProxmoxVMManagerFn
	oldProxmoxCreds := vmlifecycle.GetProxmoxCredentialsFn
	oldVSphereCreds := vmlifecycle.GetVSphereCredsFn
	oldVSphereVMManagerFactory := vmlifecycle.NewVSphereVMManagerFn
	t.Cleanup(func() {
		vmlifecycle.NewTrueNASVMManagerFn = oldTrueNASFactory
		vmlifecycle.NewProxmoxVMManagerFn = oldProxmoxFactory
		vmlifecycle.GetProxmoxCredentialsFn = oldProxmoxCreds
		vmlifecycle.GetVSphereCredsFn = oldVSphereCreds
		vmlifecycle.NewVSphereVMManagerFn = oldVSphereVMManagerFactory
	})

	t.Run("truenas wrappers", func(t *testing.T) {
		stubUnavailable1PasswordCLI(t)
		manager := &fakeTrueNASVMManager{}
		vmlifecycle.NewTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) vmlifecycle.TrueNASVMManager {
			assert.Equal(t, "truenas.local", host)
			assert.Equal(t, "api-key", apiKey)
			assert.Equal(t, 443, port)
			assert.True(t, useSSL)
			return manager
		}
		cleanup := testutil.SetEnvs(t, map[string]string{
			constants.EnvTrueNASHost:   "truenas.local",
			constants.EnvTrueNASAPIKey: "api-key",
			"STORAGE_POOL":             "flashstor",
		})
		defer cleanup()

		require.NoError(t, listVMs("truenas", "table"))
		require.NoError(t, startVMWithProvider("tn-vm", "truenas"))
		require.NoError(t, powerOffVM("tn-vm", "truenas"))
		require.NoError(t, deleteVMWithConfirmation("tn-vm", "truenas", true))
		require.NoError(t, infoVMWithProvider("tn-vm", "truenas"))
		require.NoError(t, cleanupOrphanedZVols("tn-vm", "flashstor"))

		assert.Equal(t, 6, manager.connectCalls)
		assert.Equal(t, 6, manager.closeCalls)
		assert.Equal(t, 1, manager.listCalls)
		assert.Equal(t, []string{"tn-vm"}, manager.started)
		assert.Equal(t, []string{"tn-vm:true"}, manager.stopped)
		assert.Equal(t, []string{"tn-vm:true:flashstor"}, manager.deleted)
		assert.Equal(t, []string{"tn-vm"}, manager.infoNames)
		assert.Equal(t, []string{"tn-vm:flashstor"}, manager.cleanupPairs)
	})

	t.Run("proxmox wrappers", func(t *testing.T) {
		manager := &fakeProxmoxVMManager{}
		vmlifecycle.GetProxmoxCredentialsFn = func() (string, string, string, string, error) {
			return "px.local", "token", "secret", "pve", nil
		}
		vmlifecycle.NewProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (vmlifecycle.ProxmoxVMManager, error) {
			assert.Equal(t, "px.local", host)
			assert.Equal(t, "token", tokenID)
			assert.Equal(t, "secret", secret)
			assert.Equal(t, "pve", nodeName)
			assert.False(t, insecure)
			return manager, nil
		}

		require.NoError(t, listVMs("proxmox", "table"))
		require.NoError(t, startVMWithProvider("px-vm", "proxmox"))
		require.NoError(t, powerOffVM("px-vm", "proxmox"))
		require.NoError(t, deleteVMWithConfirmation("px-vm", "proxmox", true))
		require.NoError(t, infoVMWithProvider("px-vm", "proxmox"))

		assert.Equal(t, 5, manager.closeCalls)
		assert.Equal(t, 1, manager.listCalls)
		assert.Equal(t, []string{"px-vm"}, manager.started)
		assert.Equal(t, []string{"px-vm:true"}, manager.stopped)
		assert.Equal(t, []string{"px-vm"}, manager.deleted)
		assert.Equal(t, []string{"px-vm"}, manager.infoNames)
	})

	t.Run("vsphere wrappers", func(t *testing.T) {
		vmlifecycle.GetVSphereCredsFn = func() (string, string, string, error) {
			return "esxi.local", "root", "secret", nil
		}
		calls := &[]string{}
		var constructed int
		vmlifecycle.NewVSphereVMManagerFn = func(host, username, password string, insecure bool) (vmprov.VMLifecycle, error) {
			assert.Equal(t, "esxi.local", host)
			assert.Equal(t, "root", username)
			assert.Equal(t, "secret", password)
			assert.False(t, insecure)
			constructed++
			return &fakeVMLifecycle{provider: "vsphere", calls: calls}, nil
		}

		require.NoError(t, listVMs("vsphere", "table"))
		require.NoError(t, infoVMWithProvider("esx-vm", "vsphere"))
		require.NoError(t, powerOnVM("esx-vm", "vsphere"))
		require.NoError(t, powerOffVM("esx-vm", "vsphere"))
		require.NoError(t, deleteVMWithConfirmation("esx-vm", "vsphere", true))

		assert.Equal(t, 5, constructed, "each lifecycle op constructs and closes a manager")
		assert.Equal(t, []string{
			"list-vsphere",
			"info-vsphere:esx-vm",
			"start-vsphere:esx-vm",
			"stop-vsphere:esx-vm:true",
			"delete-vsphere:esx-vm",
		}, *calls)
	})
}
