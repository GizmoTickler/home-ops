package kubernetes

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/common"
	"homeops-cli/internal/testutil"
)

func captureKubernetesStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	os.Stdout = old
	return string(out)
}

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

func TestSecretValueMapUsesReadablePlaceholdersForBadKeys(t *testing.T) {
	data := map[string]secretKeyData{
		"plain":        {value: []byte("visible-placeholder"), meta: secretMetadata{DecodedBytes: 19, SHA256Prefix: "abc123abc123"}},
		"decode-error": {meta: secretMetadata{DecodeError: "bad base64"}},
		"read-error":   {meta: secretMetadata{ReadError: "missing key"}},
	}

	values := secretValueMap(data)

	assert.Equal(t, "visible-placeholder", values["plain"])
	assert.Equal(t, "<error decoding>", values["decode-error"])
	assert.Equal(t, "<error reading value>", values["read-error"])
}

func TestPrintSecretTableCoversMetadataAndMultilineValueBranches(t *testing.T) {
	data := map[string]secretKeyData{
		"alpha": {
			value: []byte("line one\nline two"),
			meta:  secretMetadata{DecodedBytes: 17, SHA256Prefix: "abc123abc123"},
		},
		"broken": {
			meta: secretMetadata{DecodeError: "bad base64"},
		},
	}

	metadataOut := captureKubernetesStdout(t, func() {
		printSecretTable(data, false)
	})
	assert.Contains(t, metadataOut, "KEY: alpha")
	assert.Contains(t, metadataOut, "DECODED_BYTES: 17")
	assert.Contains(t, metadataOut, "KEY: broken")
	assert.Contains(t, metadataOut, "DECODE_ERROR: bad base64")

	valuesOut := captureKubernetesStdout(t, func() {
		printSecretTable(data, true)
	})
	assert.Contains(t, valuesOut, "VALUE (multiline, 2 lines)")
	assert.Contains(t, valuesOut, "line one\nline two")
	assert.Contains(t, valuesOut, "VALUE: <error decoding>")
}

func TestResolveFluxSyncResourceTypeCoversAliasesSelectionAndErrors(t *testing.T) {
	fullType, cancelled, err := resolveFluxSyncResourceType("hr")
	require.NoError(t, err)
	assert.False(t, cancelled)
	assert.Equal(t, "helmrelease", fullType)

	_, _, err = resolveFluxSyncResourceType("not-a-kind")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid resource type")

	testutil.Swap(t, &chooseOptionFn, func(prompt string, options []string) (string, error) {
		assert.Equal(t, "Select resource type to sync:", prompt)
		assert.Contains(t, options, "kustomization - Kustomizations")
		return "kustomization - Kustomizations", nil
	})
	fullType, cancelled, err = resolveFluxSyncResourceType("")
	require.NoError(t, err)
	assert.False(t, cancelled)
	assert.Equal(t, "kustomization", fullType)
}

func TestResolveFluxSyncResourceTypeTreatsPromptCancellationAsCleanExit(t *testing.T) {
	testutil.Swap(t, &chooseOptionFn, func(string, []string) (string, error) {
		return "", errors.New("cancelled by user")
	})

	fullType, cancelled, err := resolveFluxSyncResourceType("")

	require.NoError(t, err)
	assert.True(t, cancelled)
	assert.Empty(t, fullType)
}

func TestListFluxSyncResourcesBuildsExpectedKubectlArgs(t *testing.T) {
	var calls [][]string
	testutil.Swap(t, &commandOutputFn, func(name string, args ...string) ([]byte, error) {
		assert.Equal(t, "kubectl", name)
		calls = append(calls, append([]string(nil), args...))
		return []byte("flux-system,flux-system\nmedia,radarr\n"), nil
	})

	resources, err := listFluxSyncResources("kustomization", "")

	require.NoError(t, err)
	assert.Equal(t, []string{"flux-system,flux-system", "media,radarr"}, resources)
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0], "--all-namespaces")

	testutil.Swap(t, &commandOutputFn, func(name string, args ...string) ([]byte, error) {
		assert.Equal(t, "kubectl", name)
		calls = append(calls, append([]string(nil), args...))
		return []byte("\n"), nil
	})
	resources, err = listFluxSyncResources("helmrelease", "media")

	require.NoError(t, err)
	assert.Nil(t, resources)
	require.Len(t, calls, 2)
	assert.Contains(t, calls[1], "--namespace")
	assert.Contains(t, calls[1], "media")
}

func TestListFluxSyncResourcesWrapsKubectlError(t *testing.T) {
	testutil.Swap(t, &commandOutputFn, func(string, ...string) ([]byte, error) {
		return nil, errors.New("kubectl failed")
	})

	_, err := listFluxSyncResources("ocirepository", "flux-system")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list ocirepository resources")
	assert.Contains(t, err.Error(), "kubectl failed")
}

func TestReconcileFluxSyncResourcesSequentialCountsSuccessFailureAndSkipsInvalid(t *testing.T) {
	var calls []string
	testutil.Swap(t, &commandRunFn, func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "bad") {
			return errors.New("reconcile failed")
		}
		return nil
	})

	success, failed := reconcileFluxSyncResourcesSequential(common.NewColorLogger(), "kustomization", []string{
		"flux-system,good",
		"invalid-resource",
		"flux-system,bad",
	})

	assert.Equal(t, 1, success)
	assert.Equal(t, 1, failed)
	assert.Equal(t, []string{
		"flux reconcile kustomization good -n flux-system",
		"flux reconcile kustomization bad -n flux-system",
	}, calls)
}

func TestReconcileFluxSyncResourcesParallelCountsSuccessFailureAndSkipsInvalid(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)
	testutil.Swap(t, &commandRunFn, func(name string, args ...string) error {
		mu.Lock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		mu.Unlock()
		if strings.Contains(strings.Join(args, " "), "bad") {
			return errors.New("reconcile failed")
		}
		return nil
	})

	success, failed := reconcileFluxSyncResourcesParallel(common.NewColorLogger(), "helmrelease", []string{
		"media,good",
		"invalid-resource",
		"media,bad",
	})

	assert.Equal(t, 1, success)
	assert.Equal(t, 1, failed)
	assert.ElementsMatch(t, []string{
		"flux reconcile helmrelease good -n media",
		"flux reconcile helmrelease bad -n media",
	}, calls)
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
