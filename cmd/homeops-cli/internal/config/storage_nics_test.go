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

func TestLoadFileValidatesNFSECMP(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "homeops.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  nfs_ecmp:
    server: 192.168.120.10
    vlans: [1201, 1202, 1203, 1204]
`), 0o600))

		cfg, err := LoadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "192.168.120.10", cfg.Cluster.NFSEcmp.Server)
		assert.Equal(t, []int{1201, 1202, 1203, 1204}, cfg.Cluster.NFSEcmp.VLANs)
	})

	cases := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "missing server",
			config:  "    vlans: [1201, 1202, 1203, 1204]",
			wantErr: "cluster.nfs_ecmp.server",
		},
		{
			name:    "IPv6 server",
			config:  "    server: 2001:db8::10\n    vlans: [1201, 1202, 1203, 1204]",
			wantErr: "must be an IPv4 address",
		},
		{
			name:    "missing VLANs",
			config:  "    server: 192.168.120.10",
			wantErr: "must contain at least one storage VLAN",
		},
		{
			name:    "unsupported VLAN",
			config:  "    server: 192.168.120.10\n    vlans: [1205]",
			wantErr: "is not a supported storage VLAN",
		},
		{
			name:    "duplicate VLAN",
			config:  "    server: 192.168.120.10\n    vlans: [1201, 1201]",
			wantErr: "duplicates cluster.nfs_ecmp.vlans[0]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "homeops.yaml")
			content := "cluster:\n  nfs_ecmp:\n" + tc.config + "\n"
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			_, err := LoadFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
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

func TestLoadFileRejectsDuplicateBaseNICMACs(t *testing.T) {
	cases := []struct {
		name    string
		nodes   string
		wantErr string
	}{
		{
			name: "within one VM across bridges case folded",
			nodes: `
    - name: k8s-0
      vm:
        mac_iot: "02:00:00:00:10:01"
        mac_vpn: "02:00:00:00:10:01"
`,
			wantErr: `cluster.nodes[k8s-0].vm.mac_vpn: "02:00:00:00:10:01" duplicates cluster.nodes[k8s-0].vm.mac_iot`,
		},
		{
			name: "across VMs on one bridge case folded",
			nodes: `
    - name: k8s-0
      vm:
        mac_iot: "AA:BB:CC:DD:EE:01"
    - name: k8s-1
      vm:
        mac_iot: "aa:bb:cc:dd:ee:01"
`,
			wantErr: `cluster.nodes[k8s-1].vm.mac_iot: "aa:bb:cc:dd:ee:01" duplicates cluster.nodes[k8s-0].vm.mac_iot`,
		},
		{
			name: "across VMs and base NIC roles",
			nodes: `
    - name: k8s-0
      vm:
        mac: "02:00:00:00:10:02"
    - name: k8s-1
      vm:
        mac_vpn: "02:00:00:00:10:02"
`,
			wantErr: `cluster.nodes[k8s-1].vm.mac_vpn: "02:00:00:00:10:02" duplicates cluster.nodes[k8s-0].vm.mac`,
		},
		{
			name: "rehearsal node uses its exact config path",
			nodes: `
    - name: k8s-0
      vm:
        mac_iot: "02:00:00:00:10:03"
  test_node:
    name: k8s-test
    vm:
      mac_vpn: "02:00:00:00:10:03"
`,
			wantErr: `cluster.test_node.vm.mac_vpn: "02:00:00:00:10:03" duplicates cluster.nodes[k8s-0].vm.mac_iot`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "homeops.yaml")
			content := "cluster:\n  nodes:" + tc.nodes
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			_, err := LoadFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadFileRejectsCrossNodeProviderMACCollisions(t *testing.T) {
	cases := []struct {
		name    string
		nodes   string
		wantErr string
	}{
		{
			name: "R11 provider MAC versus provider MAC case folded",
			nodes: `
    - name: k8s-0
      vm:
        providers:
          talos:
            mac: "AA:BB:CC:DD:EE:11"
    - name: k8s-1
      vm:
        providers:
          talos:
            mac: "aa:bb:cc:dd:ee:11"
`,
			wantErr: `cluster.nodes[k8s-1].vm.providers.talos.mac: "aa:bb:cc:dd:ee:11" duplicates cluster.nodes[k8s-0].vm.providers.talos.mac`,
		},
		{
			name: "R12 provider MAC versus another node base MAC case folded",
			nodes: `
    - name: k8s-0
      vm:
        providers:
          talos:
            mac: "AA:BB:CC:DD:EE:12"
    - name: k8s-1
      vm:
        mac: "aa:bb:cc:dd:ee:12"
`,
			wantErr: `cluster.nodes[k8s-1].vm.mac: "aa:bb:cc:dd:ee:12" duplicates cluster.nodes[k8s-0].vm.providers.talos.mac`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "homeops.yaml")
			content := "cluster:\n  nodes:" + tc.nodes
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			_, err := LoadFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadFileAllowsSameNodeProviderMACReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homeops.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cluster:
  nodes:
    - name: k8s-0
      vm:
        mac: "AA:BB:CC:DD:EE:13"
        providers:
          talos:
            mac: "aa:bb:cc:dd:ee:13"
          flatcar:
            mac: "AA:BB:CC:DD:EE:13"
`), 0o600))

	_, err := LoadFile(path)
	require.NoError(t, err)
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
