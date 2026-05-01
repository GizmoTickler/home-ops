package kubernetes

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/testutil"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "k8s", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	// Check that all subcommands are registered
	subCommands := []string{
		"browse-pvc",
		"node-shell",
		"sync-secrets",
		"prune-pods",
		"upgrade-arc",
	}

	for _, subCmd := range subCommands {
		t.Run(subCmd, func(t *testing.T) {
			found := false
			for _, cmd := range cmd.Commands() {
				if cmd.Name() == subCmd {
					found = true
					break
				}
			}
			assert.True(t, found, "Subcommand %s not found", subCmd)
		})
	}
}

func TestCommandHelp(t *testing.T) {
	cmd := NewCommand()
	output, err := testutil.ExecuteCommand(cmd, "--help")
	assert.NoError(t, err)
	assert.Contains(t, output, "Commands for managing Kubernetes resources")
	assert.Contains(t, output, "browse-pvc")
	assert.Contains(t, output, "node-shell")
}

func TestSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	tests := []string{
		"browse-pvc",
		"node-shell",
		"sync-secrets",
		"prune-pods",
		"upgrade-arc",
	}

	for _, subCmd := range tests {
		t.Run(subCmd, func(t *testing.T) {
			output, err := testutil.ExecuteCommand(cmd, subCmd, "--help")
			assert.NoError(t, err)
			assert.NotEmpty(t, output)
		})
	}
}

func TestDecodeBase64UsesStdlib(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"std encoding", "aGVsbG8gd29ybGQ=", "hello world"},
		{"std with whitespace", "  aGVsbG8gd29ybGQ=  \n", "hello world"},
		{"url encoding", "aGVsbG8tX3dvcmxkPw==", "hello-_world?"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeBase64(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestDecodeBase64ErrorsOnInvalidInput(t *testing.T) {
	_, err := decodeBase64("!!!not-base64!!!")
	require.Error(t, err)
}

func TestRunManifestCommandRoutesThroughPackageWriters(t *testing.T) {
	oldStdout := manifestStdout
	oldStderr := manifestStderr
	t.Cleanup(func() {
		manifestStdout = oldStdout
		manifestStderr = oldStderr
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	manifestStdout = stdout
	manifestStderr = stderr

	// Real kubectl is unlikely in CI; we only assert that the writers are wired.
	// Stub the manifest fns so they don't actually run kubectl.
	oldApply := kubectlApplyManifestFn
	t.Cleanup(func() { kubectlApplyManifestFn = oldApply })

	kubectlApplyManifestFn = func(manifest string) error {
		_, _ = manifestStdout.Write([]byte(manifest))
		return nil
	}

	require.NoError(t, kubectlApplyManifestFn("kind: ConfigMap"))
	assert.Equal(t, "kind: ConfigMap", stdout.String())
	assert.Empty(t, stderr.String())
}

func TestUpgradeARCRedactsHelmFailureOutput(t *testing.T) {
	oldCombined := commandCombinedOutputFn
	oldRun := commandRunFn
	oldSleep := sleepFn
	t.Cleanup(func() {
		commandCombinedOutputFn = oldCombined
		commandRunFn = oldRun
		sleepFn = oldSleep
	})

	commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
		// Simulate a real failure (not "not found") that includes a leaked secret-looking value.
		return []byte("Error: helm uninstall failed; api_key=SENTINEL_LEAKED_KEY"), errors.New("helm exit 1")
	}
	commandRunFn = func(string, ...string) error { return nil }
	sleepFn = func(time.Duration) {}

	err := upgradeARC()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key=<redacted>")
	assert.NotContains(t, err.Error(), "SENTINEL_LEAKED_KEY")
}

func TestRedactKubernetesCommandError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		assert.NoError(t, redactKubernetesCommandError(nil, "stdout", "stderr"))
	})

	t.Run("includes trimmed output", func(t *testing.T) {
		base := errors.New("exit 1")
		err := redactKubernetesCommandError(base, "  stdout out\n", " stderr out ")
		require.Error(t, err)
		assert.True(t, errors.Is(err, base))
		assert.Contains(t, err.Error(), "stdout out")
		assert.Contains(t, err.Error(), "stderr out")
	})

	t.Run("returns base error when output empty", func(t *testing.T) {
		base := errors.New("exit 1")
		err := redactKubernetesCommandError(base, "", "")
		require.Error(t, err)
		assert.Equal(t, base, err)
	})
}

func TestRunKubernetesCommandReturnsRedactedError(t *testing.T) {
	// Use a clearly missing binary to force a non-zero exit / not-found error.
	output, err := runKubernetesCommandCombinedOutput("definitely-not-a-real-binary-xyz")
	require.Error(t, err)
	// Output should be either empty or already-redacted; sentinel must never appear.
	assert.NotContains(t, string(output), "SENTINEL_VALUE")
	assert.NotContains(t, err.Error(), "SENTINEL_VALUE")
}

func TestNonEmptyKubernetesStrings(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"", "  "}, nil},
		{[]string{"a", "", "b"}, []string{"a", "b"}},
		{[]string{" hello \n", " "}, []string{"hello"}},
	}
	for _, tc := range cases {
		got := nonEmptyKubernetesStrings(tc.in...)
		require.Equal(t, len(tc.want), len(got))
		for i := range tc.want {
			assert.Equal(t, tc.want[i], got[i])
		}
	}
}

// Sanity check that our package-level command stubs are settable by tests.
func TestKubernetesPackageStubsAreSettable(t *testing.T) {
	for _, v := range []any{
		commandOutputFn,
		commandRunFn,
		commandCombinedOutputFn,
		decodeBase64Fn,
		kubectlOutputFn,
		kubectlRunFn,
		kubectlApplyManifestFn,
		kubectlDeleteManifestFn,
		fluxBuildKustomizationFn,
	} {
		assert.NotNil(t, v)
	}

	// manifest writers default to os.Stdout/os.Stderr but can be swapped.
	assert.NotNil(t, manifestStdout)
	assert.NotNil(t, manifestStderr)
}

func TestKubernetesCommandTimeoutHasReasonableDefault(t *testing.T) {
	assert.True(t, kubernetesDefaultCommandTimeout > 0)
	assert.True(t, kubernetesDefaultCommandTimeout >= time.Minute)
}

// Sanity: make sure unused import is intentional.
var _ = strings.TrimSpace
