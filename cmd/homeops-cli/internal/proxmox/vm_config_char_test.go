package proxmox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	homeopscfg "homeops-cli/internal/config"

	"github.com/stretchr/testify/require"
)

func dumpProxmoxDefaultConfig() string {
	cfg := GetDefaultVMConfig()
	var b strings.Builder
	fmt.Fprintf(&b, "Memory=%d\n", cfg.Memory)
	fmt.Fprintf(&b, "Cores=%d\n", cfg.Cores)
	fmt.Fprintf(&b, "Sockets=%d\n", cfg.Sockets)
	fmt.Fprintf(&b, "CPUType=%q\n", cfg.CPUType)
	fmt.Fprintf(&b, "NUMAEnabled=%t\n", cfg.NUMAEnabled)
	fmt.Fprintf(&b, "BootDiskSize=%d\n", cfg.BootDiskSize)
	fmt.Fprintf(&b, "BootStorage=%q\n", cfg.BootStorage)
	fmt.Fprintf(&b, "OpenEBSSize=%d\n", cfg.OpenEBSSize)
	fmt.Fprintf(&b, "OpenEBSStorage=%q\n", cfg.OpenEBSStorage)
	fmt.Fprintf(&b, "Node=%q\n", cfg.Node)
	fmt.Fprintf(&b, "ISOStorage=%q\n", cfg.ISOStorage)
	fmt.Fprintf(&b, "NetworkBridge=%q\n", cfg.NetworkBridge)
	fmt.Fprintf(&b, "NetworkMTU=%d\n", cfg.NetworkMTU)
	fmt.Fprintf(&b, "NetworkQueues=%d\n", cfg.NetworkQueues)
	fmt.Fprintf(&b, "VLANID=%d\n", cfg.VLANID)
	fmt.Fprintf(&b, "SCSIController=%q\n", cfg.SCSIController)
	fmt.Fprintf(&b, "WatchdogModel=%q\n", cfg.WatchdogModel)
	fmt.Fprintf(&b, "WatchdogAction=%q\n", cfg.WatchdogAction)
	return b.String()
}

func assertProxmoxGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- test-local golden path
	require.NoError(t, err)
	require.Equal(t, string(want), got, "golden %s drifted; behavior is NOT preserved", name)
}

func TestProxmoxDefaultVMConfigCharacterizeDefault(t *testing.T) {
	homeopscfg.ResetForTesting()
	t.Cleanup(homeopscfg.ResetForTesting)
	assertProxmoxGolden(t, "proxmox_default_vm_config_char_default.txt", dumpProxmoxDefaultConfig())
}

func TestProxmoxDefaultVMConfigCharacterizeRepo(t *testing.T) {
	restore := homeopscfg.SetForTesting(proxmoxRepoScenarioConfig())
	t.Cleanup(restore)
	assertProxmoxGolden(t, "proxmox_default_vm_config_char_repo.txt", dumpProxmoxDefaultConfig())
}

func proxmoxRepoScenarioConfig() *homeopscfg.Config {
	return &homeopscfg.Config{
		Hypervisors: homeopscfg.HypervisorsConfig{
			Default: "proxmox",
			Proxmox: homeopscfg.ProxmoxConfig{
				SnippetsDir: "/var/lib/vz/snippets",
				VM: homeopscfg.VMDefaults{
					MemoryMB:      98304,
					Cores:         16,
					BootDiskGB:    100,
					OpenEBSDiskGB: 800,
					NetworkBridge: "vmbr0",
					NetworkMTU:    9000,
					VLANID:        999,
				},
			},
		},
	}
}
