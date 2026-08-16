package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const validStorageNICsYAML = `
cluster:
  nodes:
    - name: k8s-0
      ip: 192.168.122.10
      vm:
        storage_nics:
          - vlan: 1204
            mac: "BC:24:11:FB:16:76"
            ip: 192.168.204.20/24
          - vlan: 1201
            mac: "BC:24:11:3B:E0:50"
            ip: 192.168.201.20/24
          - vlan: 1203
            mac: "BC:24:11:FF:50:81"
            ip: 192.168.203.20/24
          - vlan: 1202
            mac: "BC:24:11:ED:F6:B6"
            ip: 192.168.202.20/24
`

func TestLoadFileParsesAndRoundTripsStorageNICs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validStorageNICsYAML), 0o600))

	cfg, err := LoadFile(path)
	require.NoError(t, err)
	node, ok := cfg.NodeByName("k8s-0")
	require.True(t, ok)
	require.Len(t, node.VM.StorageNICs, 4)
	assert.Equal(t, StorageNIC{VLAN: 1204, MAC: "BC:24:11:FB:16:76", IP: "192.168.204.20/24"}, node.VM.StorageNICs[0])

	rendered, err := yaml.Marshal(node.VM)
	require.NoError(t, err)
	var roundTripped VMProfile
	require.NoError(t, yaml.Unmarshal(rendered, &roundTripped))
	assert.Equal(t, node.VM.StorageNICs, roundTripped.StorageNICs)
}

func TestLoadFileRejectsInvalidStorageNICs(t *testing.T) {
	validEntries := `
          - vlan: 1201
            mac: "BC:24:11:3B:E0:50"
            ip: 192.168.201.20/24
          - vlan: 1202
            mac: "BC:24:11:ED:F6:B6"
            ip: 192.168.202.20/24
          - vlan: 1203
            mac: "BC:24:11:FF:50:81"
            ip: 192.168.203.20/24
          - vlan: 1204
            mac: "BC:24:11:FB:16:76"
            ip: 192.168.204.20/24
`
	cases := []struct {
		name        string
		entries     string
		old         string
		replacement string
		wantErr     string
	}{
		{name: "malformed MAC", entries: validEntries, old: "BC:24:11:3B:E0:50", replacement: "not-a-mac", wantErr: "not a valid 6-byte MAC"},
		{name: "Cisco dot MAC", entries: validEntries, old: "BC:24:11:3B:E0:50", replacement: "bc24.113b.e050", wantErr: "colon-separated 6-octet MAC"},
		{name: "dash-separated MAC", entries: validEntries, old: "BC:24:11:3B:E0:50", replacement: "BC-24-11-3B-E0-50", wantErr: "colon-separated 6-octet MAC"},
		{name: "wrong VLAN octet", entries: validEntries, old: "192.168.203.20/24", replacement: "192.168.202.20/24", wantErr: "must be within 192.168.203.0/24"},
		{name: "inconsistent host octet", entries: validEntries, old: "192.168.203.20/24", replacement: "192.168.203.21/24", wantErr: "must use one host octet across all storage VLANs"},
		{name: "missing VLAN", entries: validEntries[:len(validEntries)-len("          - vlan: 1204\n            mac: \"BC:24:11:FB:16:76\"\n            ip: 192.168.204.20/24\n")], wantErr: "must contain exactly VLANs 1201, 1202, 1203, and 1204"},
		{name: "duplicate VLAN", entries: validEntries, old: "vlan: 1204", replacement: "vlan: 1203", wantErr: "must contain exactly VLANs 1201, 1202, 1203, and 1204"},
		{name: "duplicate MAC within node", entries: validEntries, old: "BC:24:11:ED:F6:B6", replacement: "bc:24:11:3b:e0:50", wantErr: "duplicates cluster.nodes[k8s-0].vm.storage_nics[0].mac"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := tc.entries
			if tc.old != "" {
				entries = replaceOnce(entries, tc.old, tc.replacement)
			}
			content := "cluster:\n  nodes:\n    - name: k8s-0\n      vm:\n        storage_nics:" + entries
			path := filepath.Join(t.TempDir(), "homeops.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			_, err := LoadFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadFileRejectsStorageMACUsedByBaseNIC(t *testing.T) {
	base := `
cluster:
  nodes:
    - name: k8s-0
      vm:
        mac: "02:00:00:00:00:10"
        mac_iot: "02:00:00:00:00:11"
        mac_vpn: "02:00:00:00:00:12"
        storage_nics:
          - {vlan: 1201, mac: "02:00:00:00:12:01", ip: 192.168.201.20/24}
          - {vlan: 1202, mac: "02:00:00:00:12:02", ip: 192.168.202.20/24}
          - {vlan: 1203, mac: "02:00:00:00:12:03", ip: 192.168.203.20/24}
          - {vlan: 1204, mac: "02:00:00:00:12:04", ip: 192.168.204.20/24}
`
	cases := []struct {
		name        string
		baseMAC     string
		storageMAC  string
		wantBaseKey string
	}{
		{name: "net0", baseMAC: "02:00:00:00:00:10", storageMAC: "02:00:00:00:12:01", wantBaseKey: ".mac"},
		{name: "net1", baseMAC: "02:00:00:00:00:11", storageMAC: "02:00:00:00:12:01", wantBaseKey: ".mac_iot"},
		{name: "net2", baseMAC: "02:00:00:00:00:12", storageMAC: "02:00:00:00:12:01", wantBaseKey: ".mac_vpn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := replaceOnce(base, tc.storageMAC, tc.baseMAC)
			path := filepath.Join(t.TempDir(), "homeops.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			_, err := LoadFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "duplicates base NIC")
			assert.Contains(t, err.Error(), tc.wantBaseKey)
		})
	}

	t.Run("inherited cross-node net0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "homeops.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  nodes:
    - name: k8s-0
      vm:
        storage_nics:
          - {vlan: 1201, mac: "00:a0:98:1a:f3:72", ip: 192.168.201.20/24}
          - {vlan: 1202, mac: "02:00:00:00:12:02", ip: 192.168.202.20/24}
          - {vlan: 1203, mac: "02:00:00:00:12:03", ip: 192.168.203.20/24}
          - {vlan: 1204, mac: "02:00:00:00:12:04", ip: 192.168.204.20/24}
`), 0o600))

		_, err := LoadFile(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicates base NIC cluster.nodes[k8s-1]")
	})
}

func TestLoadFileRejectsCrossNodeStorageIdentityCollisions(t *testing.T) {
	base := `
cluster:
  nodes:
    - name: k8s-0
      vm:
        storage_nics:
          - {vlan: 1201, mac: "02:00:00:00:10:01", ip: 192.168.201.20/24}
          - {vlan: 1202, mac: "02:00:00:00:10:02", ip: 192.168.202.20/24}
          - {vlan: 1203, mac: "02:00:00:00:10:03", ip: 192.168.203.20/24}
          - {vlan: 1204, mac: "02:00:00:00:10:04", ip: 192.168.204.20/24}
    - name: k8s-1
      vm:
        storage_nics:
          - {vlan: 1201, mac: "02:00:00:00:11:01", ip: 192.168.201.21/24}
          - {vlan: 1202, mac: "02:00:00:00:11:02", ip: 192.168.202.21/24}
          - {vlan: 1203, mac: "02:00:00:00:11:03", ip: 192.168.203.21/24}
          - {vlan: 1204, mac: "02:00:00:00:11:04", ip: 192.168.204.21/24}
`
	t.Run("duplicate MAC", func(t *testing.T) {
		content := replaceOnce(base, "02:00:00:00:11:01", "02:00:00:00:10:01")
		path := filepath.Join(t.TempDir(), "homeops.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

		_, err := LoadFile(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicates cluster.nodes[k8s-0].vm.storage_nics[0].mac")
	})

	t.Run("duplicate host octet", func(t *testing.T) {
		content := replaceOnce(base, ".201.21/24", ".201.20/24")
		content = replaceOnce(content, ".202.21/24", ".202.20/24")
		content = replaceOnce(content, ".203.21/24", ".203.20/24")
		content = replaceOnce(content, ".204.21/24", ".204.20/24")
		path := filepath.Join(t.TempDir(), "homeops.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

		_, err := LoadFile(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host octet 20 is already used by node k8s-0")
	})
}

func TestLoadFileRejectsPresentButEmptyStorageNICBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte("cluster:\n  nodes:\n    - name: k8s-0\n      vm:\n        storage_nics: []\n"), 0o600))

	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must contain exactly VLANs 1201, 1202, 1203, and 1204")
}

func TestLoadFileWithoutStorageNICsKeepsBlockAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte("cluster:\n  nodes:\n    - name: k8s-0\n      ip: 192.168.122.10\n"), 0o600))

	cfg, err := LoadFile(path)
	require.NoError(t, err)
	node, ok := cfg.NodeByName("k8s-0")
	require.True(t, ok)
	assert.Nil(t, node.VM.StorageNICs)
}

func TestRepositoryHomeopsStorageNICMatrix(t *testing.T) {
	cfg, err := LoadFile(filepath.Join("..", "..", "..", "..", "homeops.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 16, cfg.Hypervisors.Proxmox.VM.NetworkQueueOverrides.Net0)
	assert.Zero(t, cfg.Hypervisors.Proxmox.VM.NetworkQueueOverrides.Net1)
	assert.Equal(t, 8, cfg.Hypervisors.Proxmox.VM.NetworkQueueOverrides.Net2)

	want := map[string][]StorageNIC{
		"k8s-0": {
			{VLAN: 1201, MAC: "BC:24:11:3B:E0:50", IP: "192.168.201.20/24"},
			{VLAN: 1202, MAC: "BC:24:11:ED:F6:B6", IP: "192.168.202.20/24"},
			{VLAN: 1203, MAC: "BC:24:11:FF:50:81", IP: "192.168.203.20/24"},
			{VLAN: 1204, MAC: "BC:24:11:FB:16:76", IP: "192.168.204.20/24"},
		},
		"k8s-1": {
			{VLAN: 1201, MAC: "BC:24:11:6B:64:25", IP: "192.168.201.21/24"},
			{VLAN: 1202, MAC: "BC:24:11:A4:6E:42", IP: "192.168.202.21/24"},
			{VLAN: 1203, MAC: "BC:24:11:8C:C8:43", IP: "192.168.203.21/24"},
			{VLAN: 1204, MAC: "BC:24:11:D1:C4:BE", IP: "192.168.204.21/24"},
		},
		"k8s-2": {
			{VLAN: 1201, MAC: "BC:24:11:B3:CD:67", IP: "192.168.201.22/24"},
			{VLAN: 1202, MAC: "BC:24:11:41:2D:40", IP: "192.168.202.22/24"},
			{VLAN: 1203, MAC: "BC:24:11:F6:D9:1D", IP: "192.168.203.22/24"},
			{VLAN: 1204, MAC: "BC:24:11:63:50:11", IP: "192.168.204.22/24"},
		},
	}
	for name, expected := range want {
		node, ok := cfg.NodeByName(name)
		require.True(t, ok, name)
		assert.Equal(t, expected, node.VM.StorageNICs, name)
	}
}

func replaceOnce(s, old, replacement string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + replacement + s[i+len(old):]
		}
	}
	return s
}
