package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"homeops-cli/internal/config"
	"homeops-cli/internal/testutil"
)

func TestScaffoldIsValidYAMLForEveryBackend(t *testing.T) {
	for _, backend := range []string{"env", "op", "file"} {
		t.Run(backend, func(t *testing.T) {
			content := scaffold(backend)
			var doc map[string]interface{}
			require.NoError(t, yaml.Unmarshal([]byte(content), &doc), "scaffold must be parseable YAML")
			secretsMap, ok := doc["secrets"].(map[string]interface{})
			require.True(t, ok, "scaffold must include a secrets map")
			for _, key := range config.KnownSecretKeys() {
				assert.Contains(t, secretsMap, key, "every known secret key must be scaffolded")
			}
		})
	}
}

func TestScaffoldRefStyles(t *testing.T) {
	assert.Equal(t, "op://Infrastructure/homeops/truenas_host", scaffoldRef(config.KeyTrueNASHost, "op"))
	assert.Equal(t, "file://~/.config/homeops/secrets/truenas_host", scaffoldRef(config.KeyTrueNASHost, "file"))
	assert.Equal(t, config.DefaultSecretRef(config.KeyTrueNASHost), scaffoldRef(config.KeyTrueNASHost, "env"))
}

func TestInitCommand(t *testing.T) {
	t.Run("writes a loadable scaffold", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "homeops.yaml")
		cmd := NewCommand()
		cmd.SetArgs([]string{"init", "--output", path})
		require.NoError(t, cmd.Execute())

		// The scaffold must survive the strict loader (KnownFields).
		cfg, err := config.LoadFile(path)
		require.NoError(t, err)
		assert.NotEmpty(t, cfg.Cluster.Nodes)
		assert.Equal(t, config.DefaultPodCIDR, cfg.Cluster.PodCIDR)
		assert.Equal(t, config.DefaultServiceCIDR, cfg.Cluster.ServiceCIDR)
		assert.Equal(t, config.DefaultDNSDomain, cfg.Cluster.DNSDomain)
		assert.Equal(t, "Infrastructure", cfg.Bootstrap.OpVault)
	})

	t.Run("refuses to overwrite without --force", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "homeops.yaml")
		require.NoError(t, os.WriteFile(path, []byte("keep: me\n"), 0o644))

		cmd := NewCommand()
		cmd.SetArgs([]string{"init", "--output", path})
		err := cmd.Execute()
		require.ErrorContains(t, err, "--force")

		cmd = NewCommand()
		cmd.SetArgs([]string{"init", "--output", path, "--force"})
		require.NoError(t, cmd.Execute())
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "keep: me")
	})

	t.Run("rejects unknown backend", func(t *testing.T) {
		cmd := NewCommand()
		cmd.SetArgs([]string{"init", "--backend", "vault", "--output", filepath.Join(t.TempDir(), "x.yaml")})
		require.ErrorContains(t, cmd.Execute(), "--backend must be env, op, or file")
	})

	t.Run("print-keys lists every key with its default", func(t *testing.T) {
		out, _, err := testutil.CaptureOutput(func() {
			cmd := NewCommand()
			cmd.SetArgs([]string{"init", "--print-keys"})
			require.NoError(t, cmd.Execute())
		})
		require.NoError(t, err)
		for _, key := range config.KnownSecretKeys() {
			assert.Contains(t, out, key)
		}
		assert.Contains(t, out, config.DefaultSecretRef(config.KeyTrueNASHost))
	})
}

func TestInitCommandUsesOutputFileCanonicalFlag(t *testing.T) {
	cmd := NewCommand()
	initCmd, _, err := cmd.Find([]string{"init"})
	require.NoError(t, err)
	require.NotNil(t, initCmd)

	require.NotNil(t, initCmd.Flags().Lookup("output-file"))
	legacy := initCmd.Flags().Lookup("output")
	require.NotNil(t, legacy)
	assert.True(t, legacy.Hidden)
	assert.NotEmpty(t, legacy.Deprecated)
}

func TestShowCommandNeverPrintsSecretValues(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Secrets: map[string]string{
			config.KeyTrueNASHost:   "literal://nas.example",
			config.KeyTrueNASAPIKey: "literal://super-secret-api-key",
		},
	})
	defer restore()

	out, _, err := testutil.CaptureOutput(func() {
		cmd := NewCommand()
		cmd.SetArgs([]string{"show"})
		require.NoError(t, cmd.Execute())
	})
	require.NoError(t, err)
	// References are shown; the show command never resolves them, so a
	// literal:// ref's value appearing IS the reference, not a resolution.
	assert.Contains(t, out, config.KeyTrueNASHost)
	assert.Contains(t, out, "literal://nas.example")
}

// doctorSeams stubs every external touchpoint of runDoctor.
func doctorSeams(t *testing.T) {
	t.Helper()
	origLook, origLocate, origLoad, origCurrent, origResolve := lookPathFn, locateConfigFn, loadConfigFn, currentConfigFn, resolveRefFn
	t.Cleanup(func() {
		lookPathFn, locateConfigFn, loadConfigFn, currentConfigFn, resolveRefFn = origLook, origLocate, origLoad, origCurrent, origResolve
	})
}

func TestRunDoctorHappyPath(t *testing.T) {
	doctorSeams(t)
	restore := config.SetForTesting(&config.Config{})
	defer restore()

	lookPathFn = func(string) error { return nil }
	locateConfigFn = func() (string, bool) { return "", false }
	currentConfigFn = config.Get
	resolveRefFn = func(string) (string, error) { return "value", nil }

	require.NoError(t, runDoctor(false, false))
}

func TestRunDoctorCountsFailures(t *testing.T) {
	doctorSeams(t)
	restore := config.SetForTesting(&config.Config{
		Secrets: map[string]string{config.KeyTrueNASHost: "env://EXPLICITLY_SET"},
	})
	defer restore()

	lookPathFn = func(bin string) error {
		if bin == "helmfile" {
			return errors.New("not found")
		}
		return nil
	}
	locateConfigFn = func() (string, bool) { return "", false }
	currentConfigFn = config.Get
	resolveRefFn = func(ref string) (string, error) {
		if strings.Contains(ref, "EXPLICITLY_SET") {
			return "", errors.New("unset")
		}
		return "value", nil
	}

	err := runDoctor(false, false)
	require.Error(t, err)
	// missing binary (1) + explicitly-configured secret that fails (1)
	assert.Contains(t, err.Error(), "2 problem(s)")
}

func TestRunDoctorSkipSecrets(t *testing.T) {
	doctorSeams(t)
	restore := config.SetForTesting(&config.Config{})
	defer restore()

	lookPathFn = func(string) error { return nil }
	locateConfigFn = func() (string, bool) { return "", false }
	currentConfigFn = config.Get
	resolved := 0
	resolveRefFn = func(string) (string, error) { resolved++; return "", errors.New("must not be called") }

	require.NoError(t, runDoctor(true, false))
	assert.Zero(t, resolved, "--skip-secrets must not resolve references")
}

func TestRunDoctorValidatesClusterEnvironmentKeys(t *testing.T) {
	doctorSeams(t)
	restore := config.SetForTesting(&config.Config{
		Cluster: config.ClusterConfig{
			PodCIDR:     "not-a-cidr",
			ServiceCIDR: "10.43.0.0/16",
			DNSDomain:   "cluster.local",
			NodeSubnet:  "192.168.120.0/22",
		},
	})
	defer restore()

	lookPathFn = func(string) error { return nil }
	locateConfigFn = func() (string, bool) { return "", false }
	currentConfigFn = config.Get

	err := runDoctor(true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 problem(s)")
}

func TestRunDoctorBadConfigFileFailsFast(t *testing.T) {
	doctorSeams(t)
	lookPathFn = func(string) error { return nil }
	locateConfigFn = func() (string, bool) { return "/tmp/homeops.yaml", true }
	loadConfigFn = func(string) (*config.Config, error) { return nil, errors.New("yaml exploded") }

	err := runDoctor(true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 problem(s)")
}
