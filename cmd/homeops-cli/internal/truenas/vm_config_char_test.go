package truenas

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	homeopscfg "homeops-cli/internal/config"

	"github.com/stretchr/testify/require"
)

func dumpTrueNASDefaultConfigs() string {
	var b strings.Builder
	for _, name := range []string{"k8s-0", "k8s-1", "k8s-2"} {
		cfg := GetDefaultVMConfig(name)
		fmt.Fprintf(&b, "== %s ==\n", name)
		fmt.Fprintf(&b, "Name=%q\n", cfg.Name)
		fmt.Fprintf(&b, "Memory=%d\n", cfg.Memory)
		fmt.Fprintf(&b, "VCPUs=%d\n", cfg.VCPUs)
		fmt.Fprintf(&b, "DiskSize=%d\n", cfg.DiskSize)
		fmt.Fprintf(&b, "OpenEBSSize=%d\n", cfg.OpenEBSSize)
		fmt.Fprintf(&b, "TalosISO=%q\n", cfg.TalosISO)
		fmt.Fprintf(&b, "NetworkBridge=%q\n", cfg.NetworkBridge)
		fmt.Fprintf(&b, "StoragePool=%q\n", cfg.StoragePool)
		fmt.Fprintf(&b, "MacAddress=%q\n", cfg.MacAddress)
	}
	return b.String()
}

func assertTrueNASGolden(t *testing.T, name, got string) {
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

func TestTrueNASDefaultVMConfigCharacterizeDefault(t *testing.T) {
	homeopscfg.ResetForTesting()
	t.Cleanup(homeopscfg.ResetForTesting)
	assertTrueNASGolden(t, "truenas_default_vm_config_char_default.txt", dumpTrueNASDefaultConfigs())
}

func TestTrueNASDefaultVMConfigCharacterizeRepo(t *testing.T) {
	restore := homeopscfg.SetForTesting(&homeopscfg.Config{
		Hypervisors: homeopscfg.HypervisorsConfig{
			TrueNAS: homeopscfg.TrueNASConfig{
				ISODir:    "/mnt/flashstor/ISO",
				ISOFile:   "metal-amd64.iso",
				SpiceHost: "192.168.120.10",
			},
		},
	})
	t.Cleanup(restore)
	assertTrueNASGolden(t, "truenas_default_vm_config_char_repo.txt", dumpTrueNASDefaultConfigs())
}
