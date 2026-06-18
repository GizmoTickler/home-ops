package vmlifecycle

import (
	"os"
	"path/filepath"
	"testing"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stubUnavailable1PasswordCLI(t *testing.T) {
	t.Helper()

	scriptDir := t.TempDir()
	opPath := filepath.Join(scriptDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		expected     string
	}{
		{
			name:         "env variable exists",
			key:          "TEST_VAR",
			defaultValue: "default",
			envValue:     "custom",
			expected:     "custom",
		},
		{
			name:         "env variable does not exist",
			key:          "NON_EXISTENT_VAR",
			defaultValue: "default",
			envValue:     "",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				cleanup := testutil.SetEnv(t, tt.key, tt.envValue)
				defer cleanup()
			}

			result := GetEnvOrDefault(tt.key, tt.defaultValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeVMProvider(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  string
		expectErr bool
	}{
		{name: "default empty", input: "", expected: "proxmox"},
		{name: "proxmox", input: "proxmox", expected: "proxmox"},
		{name: "truenas", input: "truenas", expected: "truenas"},
		{name: "vsphere", input: "vsphere", expected: "vsphere"},
		{name: "esxi alias", input: "esxi", expected: "vsphere"},
		{name: "case insensitive", input: "TrueNAS", expected: "truenas"},
		{name: "invalid", input: "nope", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeVMProvider(tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGetVMNamesForProvider(t *testing.T) {
	oldTrueNAS := GetTrueNASVMNamesFn
	oldProxmox := GetProxmoxVMNamesFn
	oldESXi := GetESXiVMNamesFn
	t.Cleanup(func() {
		GetTrueNASVMNamesFn = oldTrueNAS
		GetProxmoxVMNamesFn = oldProxmox
		GetESXiVMNamesFn = oldESXi
	})

	GetTrueNASVMNamesFn = func() ([]string, error) { return []string{"tn-1"}, nil }
	GetProxmoxVMNamesFn = func() ([]string, error) { return []string{"px-1"}, nil }
	GetESXiVMNamesFn = func() ([]string, error) { return []string{"esx-1"}, nil }

	names, err := GetVMNamesForProvider("truenas")
	require.NoError(t, err)
	assert.Equal(t, []string{"tn-1"}, names)

	names, err = GetVMNamesForProvider("proxmox")
	require.NoError(t, err)
	assert.Equal(t, []string{"px-1"}, names)

	names, err = GetVMNamesForProvider("esxi")
	require.NoError(t, err)
	assert.Equal(t, []string{"esx-1"}, names)
}

func TestChooseVMNameForProvider(t *testing.T) {
	oldChoose := ChooseVMFunc
	oldProxmox := GetProxmoxVMNamesFn
	t.Cleanup(func() {
		ChooseVMFunc = oldChoose
		GetProxmoxVMNamesFn = oldProxmox
	})

	GetProxmoxVMNamesFn = func() ([]string, error) { return []string{"vm-a", "vm-b"}, nil }
	ChooseVMFunc = func(prompt string, options []string) (string, error) {
		assert.Equal(t, "Select VM to start:", prompt)
		assert.Equal(t, []string{"vm-a", "vm-b"}, options)
		return "vm-b", nil
	}

	selected, err := ChooseVMNameForProvider("", "proxmox", "start")
	require.NoError(t, err)
	assert.Equal(t, "vm-b", selected)

	selected, err = ChooseVMNameForProvider("already-set", "proxmox", "start")
	require.NoError(t, err)
	assert.Equal(t, "already-set", selected)
}

func TestSecretAndHostResolution(t *testing.T) {
	oldSecret := ResolveSecretKeyFn
	t.Cleanup(func() {
		ResolveSecretKeyFn = oldSecret
	})

	t.Run("spice password prefers 1password then env", func(t *testing.T) {
		ResolveSecretKeyFn = func(ref string) string {
			if ref == versionconfig.KeyTrueNASSpicePassword {
				return "op-secret"
			}
			return ""
		}
		assert.Equal(t, "op-secret", GetSpicePassword())

		ResolveSecretKeyFn = func(string) string { return "" }
		cleanup := testutil.SetEnv(t, "SPICE_PASSWORD", "env-secret")
		defer cleanup()
		assert.Equal(t, "env-secret", GetSpicePassword())
	})

	t.Run("vsphere credentials resolve from secret source", func(t *testing.T) {
		ResolveSecretKeyFn = func(ref string) string {
			switch ref {
			case versionconfig.KeyVSphereHost:
				return "esxi.local"
			case versionconfig.KeyVSphereUsername:
				return "root"
			case versionconfig.KeyVSpherePassword:
				return "secret"
			default:
				return ""
			}
		}

		host, username, password, err := GetVSphereCredentials()
		require.NoError(t, err)
		assert.Equal(t, "esxi.local", host)
		assert.Equal(t, "root", username)
		assert.Equal(t, "secret", password)
	})

	t.Run("vsphere host falls back to env", func(t *testing.T) {
		ResolveSecretKeyFn = func(string) string { return "" }
		cleanup := testutil.SetEnv(t, "VSPHERE_HOST", "env-esxi.local")
		defer cleanup()

		host, err := GetVSphereHost()
		require.NoError(t, err)
		assert.Equal(t, "env-esxi.local", host)
	})
}

func TestValidateVMName(t *testing.T) {
	tests := []struct {
		name    string
		vmName  string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid name with underscores",
			vmName:  "test_vm_name",
			wantErr: false,
		},
		{
			name:    "valid simple name",
			vmName:  "testvm",
			wantErr: false,
		},
		{
			name:    "invalid name with dashes",
			vmName:  "test-vm-name",
			wantErr: true,
			errMsg:  "cannot contain dashes",
		},
		{
			name:    "empty name",
			vmName:  "",
			wantErr: true,
			errMsg:  "VM name cannot be empty",
		},
		{
			name:    "name with spaces",
			vmName:  "test vm name",
			wantErr: false, // Spaces are not actually validated in the function
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVMName(tt.vmName)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetTrueNASCredentials(t *testing.T) {
	stubUnavailable1PasswordCLI(t)

	t.Run("function returns values", func(t *testing.T) {
		t.Setenv(constants.EnvTrueNASHost, "test-host")
		t.Setenv(constants.EnvTrueNASAPIKey, "test-key")

		host, apiKey, err := GetTrueNASCredentials()

		assert.NoError(t, err)
		assert.Equal(t, "test-host", host)
		assert.Equal(t, "test-key", apiKey)
	})

	t.Run("function signature exists", func(t *testing.T) {
		host, apiKey, err := GetTrueNASCredentials()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "TrueNAS credentials")
		assert.Empty(t, host)
		assert.Empty(t, apiKey)
	})
}
