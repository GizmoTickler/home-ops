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
		{name: "wrong VLAN octet", entries: validEntries, old: "192.168.203.20/24", replacement: "192.168.202.20/24", wantErr: "must be within 192.168.203.0/24"},
		{name: "missing VLAN", entries: validEntries[:len(validEntries)-len("          - vlan: 1204\n            mac: \"BC:24:11:FB:16:76\"\n            ip: 192.168.204.20/24\n")], wantErr: "must contain exactly VLANs 1201, 1202, 1203, and 1204"},
		{name: "duplicate VLAN", entries: validEntries, old: "vlan: 1204", replacement: "vlan: 1203", wantErr: "must contain exactly VLANs 1201, 1202, 1203, and 1204"},
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
