package volsync

import (
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
	assert.Equal(t, "volsync", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	// Check that all subcommands are registered
	subCommands := []string{
		"state",
		"snapshot",
		"snapshot-all",
		"restore",
		"restore-all",
		"snapshots",
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
	assert.Contains(t, output, "volsync")
	assert.Contains(t, output, "snapshot")
	assert.Contains(t, output, "restore")
}

func TestSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	tests := []string{
		"state",
		"snapshot",
		"snapshot-all",
		"restore",
		"restore-all",
		"snapshots",
	}

	for _, subCmd := range tests {
		t.Run(subCmd, func(t *testing.T) {
			output, err := testutil.ExecuteCommand(cmd, subCmd, "--help")
			assert.NoError(t, err)
			assert.NotEmpty(t, output)
		})
	}
}

func TestStateCommandRequiresValidAction(t *testing.T) {
	cmd := NewCommand()
	_, err := testutil.ExecuteCommand(cmd, "state", "--action", "invalid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be 'suspend' or 'resume'")
}

func TestSuspendRequiresArgOrAllFlag(t *testing.T) {
	cmd := NewCommand()
	_, err := testutil.ExecuteCommand(cmd, "suspend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either provide resource name or use --all flag")
}

func TestResumeRequiresArgOrAllFlag(t *testing.T) {
	cmd := NewCommand()
	_, err := testutil.ExecuteCommand(cmd, "resume")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either provide resource name or use --all flag")
}

func TestRestoreAllRequiresPreviousFlag(t *testing.T) {
	cmd := NewCommand()
	_, err := testutil.ExecuteCommand(cmd, "restore-all")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous")
}

func TestPromptForNamespace(t *testing.T) {
	oldSelect := selectNamespaceFn
	t.Cleanup(func() { selectNamespaceFn = oldSelect })

	t.Run("returns input when namespace is set", func(t *testing.T) {
		selectNamespaceFn = func(string, bool) (string, error) {
			t.Fatal("selectNamespaceFn must not be called when namespace is set")
			return "", nil
		}

		ns, cancelled, err := promptForNamespace("media")
		require.NoError(t, err)
		assert.False(t, cancelled)
		assert.Equal(t, "media", ns)
	})

	t.Run("prompts when namespace is empty", func(t *testing.T) {
		selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
			assert.Equal(t, "Select namespace:", prompt)
			assert.False(t, allowAll)
			return "downloads", nil
		}

		ns, cancelled, err := promptForNamespace("")
		require.NoError(t, err)
		assert.False(t, cancelled)
		assert.Equal(t, "downloads", ns)
	})

	t.Run("returns cancelled on cancellation error", func(t *testing.T) {
		selectNamespaceFn = func(string, bool) (string, error) {
			return "", errors.New("cancelled by user")
		}

		ns, cancelled, err := promptForNamespace("")
		require.NoError(t, err)
		assert.True(t, cancelled)
		assert.Empty(t, ns)
	})

	t.Run("propagates non-cancellation errors", func(t *testing.T) {
		selectNamespaceFn = func(string, bool) (string, error) {
			return "", errors.New("kubectl unreachable")
		}

		_, cancelled, err := promptForNamespace("")
		require.Error(t, err)
		assert.False(t, cancelled)
		assert.Contains(t, err.Error(), "kubectl unreachable")
	})
}

func TestFetchReplicationSourceNames(t *testing.T) {
	oldOutput := commandOutputFn
	t.Cleanup(func() { commandOutputFn = oldOutput })

	t.Run("parses whitespace separated names", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			require.Equal(t, "kubectl", name)
			assert.Contains(t, args, "media")
			return []byte("paperless audiobookshelf\n"), nil
		}

		apps, err := fetchReplicationSourceNames("media")
		require.NoError(t, err)
		assert.Equal(t, []string{"paperless", "audiobookshelf"}, apps)
	})

	t.Run("returns error when kubectl fails", func(t *testing.T) {
		commandOutputFn = func(string, ...string) ([]byte, error) {
			return nil, errors.New("connection refused")
		}

		_, err := fetchReplicationSourceNames("media")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get ReplicationSources in namespace media")
	})

	t.Run("returns error when no ReplicationSources exist", func(t *testing.T) {
		commandOutputFn = func(string, ...string) ([]byte, error) {
			return []byte(""), nil
		}

		_, err := fetchReplicationSourceNames("empty-ns")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no ReplicationSources found")
	})
}

func TestResolveReplicationSourceApp(t *testing.T) {
	oldChoose := chooseOptionFn
	oldOutput := commandOutputFn
	t.Cleanup(func() {
		chooseOptionFn = oldChoose
		commandOutputFn = oldOutput
	})

	t.Run("returns input when app is set", func(t *testing.T) {
		chooseOptionFn = func(string, []string) (string, error) {
			t.Fatal("chooseOptionFn must not be called when app is set")
			return "", nil
		}

		app, cancelled, err := resolveReplicationSourceApp("media", "paperless", "snapshot")
		require.NoError(t, err)
		assert.False(t, cancelled)
		assert.Equal(t, "paperless", app)
	})

	t.Run("prompts when app is empty", func(t *testing.T) {
		commandOutputFn = func(string, ...string) ([]byte, error) {
			return []byte("paperless audiobookshelf"), nil
		}
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			assert.Contains(t, prompt, "snapshot")
			assert.Contains(t, prompt, "media")
			assert.Equal(t, []string{"paperless", "audiobookshelf"}, options)
			return "audiobookshelf", nil
		}

		app, cancelled, err := resolveReplicationSourceApp("media", "", "snapshot")
		require.NoError(t, err)
		assert.False(t, cancelled)
		assert.Equal(t, "audiobookshelf", app)
	})

	t.Run("returns cancelled on cancellation", func(t *testing.T) {
		commandOutputFn = func(string, ...string) ([]byte, error) {
			return []byte("paperless"), nil
		}
		chooseOptionFn = func(string, []string) (string, error) {
			return "", errors.New("cancelled by user")
		}

		app, cancelled, err := resolveReplicationSourceApp("media", "", "restore")
		require.NoError(t, err)
		assert.True(t, cancelled)
		assert.Empty(t, app)
	})

	t.Run("wraps non-cancellation errors", func(t *testing.T) {
		commandOutputFn = func(string, ...string) ([]byte, error) {
			return []byte("paperless"), nil
		}
		chooseOptionFn = func(string, []string) (string, error) {
			return "", errors.New("display unavailable")
		}

		_, _, err := resolveReplicationSourceApp("media", "", "restore")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "application selection failed")
		assert.Contains(t, err.Error(), "display unavailable")
	})

	t.Run("surfaces fetch errors", func(t *testing.T) {
		commandOutputFn = func(string, ...string) ([]byte, error) {
			return nil, errors.New("api error")
		}

		_, _, err := resolveReplicationSourceApp("media", "", "snapshot")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get ReplicationSources")
	})
}

func TestWaitForJobToAppear(t *testing.T) {
	oldRun := commandRunFn
	oldSleep := volsyncSleep
	t.Cleanup(func() {
		commandRunFn = oldRun
		volsyncSleep = oldSleep
	})

	t.Run("returns nil when job appears on first try", func(t *testing.T) {
		var sleeps []time.Duration
		commandRunFn = func(name string, args ...string) error {
			require.Equal(t, "kubectl", name)
			assert.Equal(t, []string{"--namespace", "media", "get", "job/volsync-src-paperless"}, args)
			return nil
		}
		volsyncSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		err := waitForJobToAppear("media", "volsync-src-paperless", time.Second)
		require.NoError(t, err)
		assert.Empty(t, sleeps)
	})

	t.Run("polls until job appears", func(t *testing.T) {
		var sleeps []time.Duration
		attempts := 0
		commandRunFn = func(name string, args ...string) error {
			attempts++
			if attempts < 3 {
				return errors.New("not found")
			}
			return nil
		}
		volsyncSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		err := waitForJobToAppear("media", "volsync-src-paperless", time.Hour)
		require.NoError(t, err)
		assert.Equal(t, 3, attempts)
		assert.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second}, sleeps)
	})

	t.Run("returns error when timeout exceeded", func(t *testing.T) {
		// Use 1ns timeout so the helper does one attempt then errors out.
		commandRunFn = func(name string, args ...string) error {
			return errors.New("not found")
		}
		volsyncSleep = func(time.Duration) {
			t.Fatal("should not sleep when timeout already exceeded")
		}

		err := waitForJobToAppear("media", "volsync-src-paperless", time.Nanosecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not appear within")
		assert.Contains(t, err.Error(), "volsync-src-paperless")
	})
}

func TestSnapshotAppValidatesReplicationSourceExists(t *testing.T) {
	oldRun := commandRunFn
	t.Cleanup(func() { commandRunFn = oldRun })

	commandRunFn = func(name string, args ...string) error {
		// Simulate missing ReplicationSource
		return errors.New("not found")
	}

	err := snapshotApp("media", "missing", false, time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReplicationSource missing not found in namespace media")
}

func TestRestoreAppRejectsCancelledRestore(t *testing.T) {
	oldConfirm := confirmActionFn
	t.Cleanup(func() { confirmActionFn = oldConfirm })

	confirmActionFn = func(message string, defaultYes bool) (bool, error) {
		assert.Contains(t, message, "Data will be overwritten")
		return false, nil
	}

	err := restoreApp("media", "paperless", "17", false, time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore cancelled")
}

func TestSnapshotAllAppsClampsZeroConcurrency(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	oldSnapshotApp := snapshotAppFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
		snapshotAppFn = oldSnapshotApp
	})

	kubectlRunFn = func(...string) error { return nil }
	kubectlCombinedOutputFn = func(...string) ([]byte, error) {
		return []byte("media paperless\n"), nil
	}
	called := false
	snapshotAppFn = func(namespace, app string, wait bool, timeout time.Duration) error {
		called = true
		return nil
	}

	// Concurrency=0 must be clamped to 1 to avoid a deadlock on a zero-length semaphore.
	require.NoError(t, snapshotAllApps("media", false, time.Minute, false, 0))
	assert.True(t, called, "snapshotApp should still be called even when concurrency=0 is clamped")
}

func TestSnapshotAllAppsRejectsUnreachableNamespace(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
	})

	kubectlRunFn = func(args ...string) error {
		// `kubectl get namespace <ns>` is the validation call.
		if len(args) >= 3 && args[0] == "get" && args[1] == "namespace" {
			return errors.New("namespace not found")
		}
		return nil
	}
	kubectlCombinedOutputFn = func(...string) ([]byte, error) {
		t.Fatal("kubectlCombinedOutputFn must not be called when namespace validation fails")
		return nil, nil
	}

	err := snapshotAllApps("missing", false, time.Minute, false, 1)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "does not exist")
}

func TestParseReplicationSourcesOutputRejectsMalformedRows(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "missing app name",
			input:   "namespace\n",
			wantErr: "expected at least 2 fields",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseReplicationSourcesOutput(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSnapshotIDFromSelectionEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain id", "abcd1234", "abcd1234"},
		{"trim whitespace", "  abcd1234  ", "abcd1234"},
		{"with parens", "2025-01-01 00:00:00 UTC (snap42)", "snap42"},
		{"no closing paren", "value (incomplete", "value (incomplete"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, snapshotIDFromSelection(tc.in))
		})
	}
}

func TestVolsyncCommandTimeoutMatchesKubectlWaitFlag(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		expected time.Duration
	}{
		{
			name:     "no timeout flag uses default",
			args:     []string{"get", "pods"},
			expected: volsyncDefaultCommandTimeout,
		},
		{
			name:     "expands timeout to wait+buffer when larger",
			args:     []string{"wait", "job/foo", "--timeout=10m"},
			expected: 10*time.Minute + volsyncWaitTimeoutBuffer,
		},
		{
			name:     "ignores invalid timeout values",
			args:     []string{"wait", "job/foo", "--timeout=abc"},
			expected: volsyncDefaultCommandTimeout,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, volsyncCommandTimeout(tc.args...))
		})
	}
}

func TestRedactCommandError(t *testing.T) {
	t.Run("returns nil when no error and no context error", func(t *testing.T) {
		assert.NoError(t, redactCommandError(nil, "stdout", "stderr", nil))
	})

	t.Run("uses context error when present", func(t *testing.T) {
		ctxErr := errors.New("context cancelled")
		err := redactCommandError(errors.New("ignored"), "stdout", "", ctxErr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context cancelled")
	})

	t.Run("includes trimmed output in error message", func(t *testing.T) {
		baseErr := errors.New("exit 1")
		err := redactCommandError(baseErr, "  some output\n", "", nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, baseErr))
		assert.Contains(t, err.Error(), "some output")
	})

	t.Run("returns base error when output is empty", func(t *testing.T) {
		baseErr := errors.New("exit 1")
		err := redactCommandError(baseErr, "", "", nil)
		require.Error(t, err)
		assert.Equal(t, baseErr, err)
	})
}

func TestNonEmptyStrings(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"all empty", []string{"", "  "}, nil},
		{"trimmed values", []string{"  hello  ", " world "}, []string{"hello", "world"}},
		{"mixed", []string{"", "hello", " "}, []string{"hello"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nonEmptyStrings(tc.in...)
			require.Equal(t, len(tc.want), len(got))
			for i := range tc.want {
				assert.Equal(t, tc.want[i], got[i])
			}
		})
	}
}

// Sanity check: all helpers used by snapshot/restore are exported via package-level vars
// so tests can stub them. Catches regressions where a helper switches to direct exec/foo.
func TestPackageStubsAreSettable(t *testing.T) {
	originals := []any{
		kubectlOutputFn,
		kubectlRunFn,
		kubectlCombinedOutputFn,
		fluxCombinedOutputFn,
		fluxRunFn,
		commandOutputFn,
		commandRunFn,
		commandCombinedOutputFn,
		kubectlApplyYAMLFn,
		selectNamespaceFn,
		chooseOptionFn,
		filterOptionFn,
		confirmActionFn,
		renderVolsyncTemplateFn,
		snapshotAppFn,
		restoreAppFn,
	}

	for i, v := range originals {
		assert.NotNil(t, v, "package-level stub %d must be non-nil", i)
	}
}
