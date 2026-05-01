package volsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangeVolsyncState(t *testing.T) {
	oldFluxCombinedOutput := fluxCombinedOutputFn
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	t.Cleanup(func() {
		fluxCombinedOutputFn = oldFluxCombinedOutput
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
	})

	t.Run("suspend scales to zero", func(t *testing.T) {
		var calls []string
		fluxCombinedOutputFn = func(args ...string) ([]byte, error) {
			calls = append(calls, "flux "+strings.Join(args, " "))
			return []byte("ok"), nil
		}
		kubectlCombinedOutputFn = func(args ...string) ([]byte, error) {
			calls = append(calls, "kubectl "+strings.Join(args, " "))
			return []byte("ok"), nil
		}

		require.NoError(t, changeVolsyncState("suspend"))
		assert.Equal(t, []string{
			"flux --namespace volsync-system suspend kustomization volsync",
			"flux --namespace volsync-system suspend helmrelease volsync",
			"kubectl --namespace volsync-system scale deployment volsync --replicas 0",
		}, calls)
	})

	t.Run("returns flux error output", func(t *testing.T) {
		fluxCombinedOutputFn = func(args ...string) ([]byte, error) {
			if len(args) >= 4 && args[3] == "helmrelease" {
				return []byte("boom"), errors.New("failed")
			}
			return []byte("ok"), nil
		}
		kubectlCombinedOutputFn = func(args ...string) ([]byte, error) {
			return []byte("ok"), nil
		}

		err := changeVolsyncState("resume")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resume helmrelease")
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestDefaultCommandFnsRedactSensitiveFailureOutput(t *testing.T) {
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	oldFluxCombinedOutput := fluxCombinedOutputFn
	oldCommandRun := commandRunFn
	oldApplyYAML := kubectlApplyYAMLFn
	t.Cleanup(func() {
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
		fluxCombinedOutputFn = oldFluxCombinedOutput
		commandRunFn = oldCommandRun
		kubectlApplyYAMLFn = oldApplyYAML
	})

	binDir := t.TempDir()
	writeFailingCommand(t, binDir, "kubectl", "password=SENTINEL_PASSWORD_VALUE", "api_key=SENTINEL_API_KEY_VALUE")
	writeFailingCommand(t, binDir, "flux", "client_secret=SENTINEL_CLIENT_SECRET_VALUE", "token: SENTINEL_TOKEN_VALUE")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	output, err := kubectlCombinedOutputFn("synthetic")
	require.Error(t, err)
	outputText := string(output)
	assert.Contains(t, outputText, "password=<redacted>")
	assert.Contains(t, outputText, "api_key=<redacted>")
	assert.NotContains(t, outputText, "SENTINEL_PASSWORD_VALUE")
	assert.NotContains(t, outputText, "SENTINEL_API_KEY_VALUE")

	output, err = fluxCombinedOutputFn("synthetic")
	require.Error(t, err)
	outputText = string(output)
	assert.Contains(t, outputText, "client_secret=<redacted>")
	assert.Contains(t, outputText, "token: <redacted>")
	assert.NotContains(t, outputText, "SENTINEL_CLIENT_SECRET_VALUE")
	assert.NotContains(t, outputText, "SENTINEL_TOKEN_VALUE")

	err = commandRunFn("kubectl", "synthetic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key=<redacted>")
	assert.NotContains(t, err.Error(), "SENTINEL_API_KEY_VALUE")

	output, err = kubectlApplyYAMLFn("apiVersion: v1\nkind: ConfigMap\n")
	require.Error(t, err)
	outputText = string(output)
	assert.Contains(t, outputText, "password=<redacted>")
	assert.NotContains(t, outputText, "SENTINEL_PASSWORD_VALUE")
	assert.Contains(t, err.Error(), "password=<redacted>")
	assert.NotContains(t, err.Error(), "SENTINEL_PASSWORD_VALUE")
}

func writeFailingCommand(t *testing.T, dir, name, stdout, stderr string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\nprintf '%%s\\n' %q >&2\nexit 7\n", stdout, stderr)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
}

func TestResolveNamespace(t *testing.T) {
	oldSelectNamespace := selectNamespaceFn
	t.Cleanup(func() { selectNamespaceFn = oldSelectNamespace })

	ns, err := resolveNamespace("media")
	require.NoError(t, err)
	assert.Equal(t, "media", ns)

	selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
		assert.Equal(t, "Select namespace:", prompt)
		assert.False(t, allowAll)
		return "downloads", nil
	}
	ns, err = resolveNamespace("")
	require.NoError(t, err)
	assert.Equal(t, "downloads", ns)

	selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
		return "", errors.New("cancelled by user")
	}
	ns, err = resolveNamespace("")
	require.NoError(t, err)
	assert.Equal(t, "", ns)
}

func TestSetVolsyncResourceSuspended(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	t.Cleanup(func() { kubectlRunFn = oldKubectlRun })

	t.Run("tries both resource kinds", func(t *testing.T) {
		var calls []string
		kubectlRunFn = func(args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			if len(args) >= 2 && args[1] == "replicationsource" {
				return errors.New("not found")
			}
			return nil
		}

		require.NoError(t, setVolsyncResourceSuspended("media", "paperless", true))
		assert.Equal(t, []string{
			`patch replicationsource paperless -n media --type=merge -p {"spec":{"suspend":true}}`,
			`patch replicationdestination paperless -n media --type=merge -p {"spec":{"suspend":true}}`,
		}, calls)
	})

	t.Run("returns last patch error context", func(t *testing.T) {
		kubectlRunFn = func(args ...string) error {
			return errors.New("api unavailable")
		}

		err := setVolsyncResourceSuspended("media", "paperless", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resource paperless not found")
		assert.Contains(t, err.Error(), "api unavailable")
	})
}

func TestSuspendResumeWrappers(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldSelectNamespace := selectNamespaceFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		selectNamespaceFn = oldSelectNamespace
	})

	var calls []string
	kubectlRunFn = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
		return "media", nil
	}

	require.NoError(t, suspendVolsyncResource("", "paperless", false))
	require.NoError(t, resumeVolsyncResource("media", "paperless", false))
	assert.Contains(t, calls[0], `patch replicationsource paperless -n media --type=merge -p {"spec":{"suspend":true}}`)
	assert.Contains(t, calls[1], `patch replicationsource paperless -n media --type=merge -p {"spec":{"suspend":false}}`)

	calls = nil
	oldList := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = oldList })
	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return []byte("paperless"), nil
	}
	require.NoError(t, suspendAllVolsyncResources("media"))
	require.NoError(t, resumeAllVolsyncResources("media"))
	assert.Len(t, calls, 4)
}

func TestSetAllVolsyncResourcesSuspended(t *testing.T) {
	oldKubectlOutput := kubectlOutputFn
	oldKubectlRun := kubectlRunFn
	t.Cleanup(func() {
		kubectlOutputFn = oldKubectlOutput
		kubectlRunFn = oldKubectlRun
	})

	var patches []string
	kubectlOutputFn = func(args ...string) ([]byte, error) {
		switch args[1] {
		case "replicationsource":
			return []byte("app-a app-b"), nil
		case "replicationdestination":
			return []byte("app-a"), nil
		default:
			return nil, fmt.Errorf("unexpected args: %v", args)
		}
	}
	kubectlRunFn = func(args ...string) error {
		patches = append(patches, strings.Join(args, " "))
		return nil
	}

	require.NoError(t, setAllVolsyncResourcesSuspended("media", true))
	assert.Equal(t, []string{
		`patch replicationsource app-a -n media --type=merge -p {"spec":{"suspend":true}}`,
		`patch replicationsource app-b -n media --type=merge -p {"spec":{"suspend":true}}`,
		`patch replicationdestination app-a -n media --type=merge -p {"spec":{"suspend":true}}`,
	}, patches)
}

func TestSnapshotApp(t *testing.T) {
	oldSelectNamespace := selectNamespaceFn
	oldChoose := chooseOptionFn
	oldOutput := commandOutputFn
	oldRun := commandRunFn
	oldCombinedOutput := commandCombinedOutputFn
	oldNow := volsyncNow
	oldSleep := volsyncSleep
	t.Cleanup(func() {
		selectNamespaceFn = oldSelectNamespace
		chooseOptionFn = oldChoose
		commandOutputFn = oldOutput
		commandRunFn = oldRun
		commandCombinedOutputFn = oldCombinedOutput
		volsyncNow = oldNow
		volsyncSleep = oldSleep
	})

	t.Run("interactive snapshot without wait", func(t *testing.T) {
		selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
			return "media", nil
		}
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			assert.Equal(t, []string{"paperless"}, options)
			return "paperless", nil
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			require.Equal(t, "kubectl", name)
			return []byte("paperless"), nil
		}
		commandRunFn = func(name string, args ...string) error {
			require.Equal(t, "kubectl", name)
			assert.Equal(t, []string{"--namespace", "media", "get", "replicationsources", "paperless"}, args)
			return nil
		}
		commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			require.Equal(t, "kubectl", name)
			assert.Equal(t, "paperless", args[4])
			return []byte("patched"), nil
		}
		volsyncNow = func() time.Time { return time.Unix(1234, 0) }

		require.NoError(t, snapshotApp("", "", false, time.Minute))
	})

	t.Run("waits for job completion", func(t *testing.T) {
		var runCalls [][]string
		var sleeps []time.Duration
		jobChecks := 0

		commandRunFn = func(name string, args ...string) error {
			runCalls = append(runCalls, append([]string{name}, args...))
			switch {
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "get" && args[3] == "replicationsources":
				return nil
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "get" && strings.HasPrefix(args[3], "job/volsync-src-paperless"):
				jobChecks++
				if jobChecks < 2 {
					return errors.New("not ready")
				}
				return nil
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "wait":
				return nil
			default:
				return fmt.Errorf("unexpected run args: %v", args)
			}
		}
		commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, []string{"--namespace", "media", "patch", "replicationsources", "paperless", "--type", "merge", "-p", `{"spec":{"trigger":{"manual":"42"}}}`}, args)
			return []byte("patched"), nil
		}
		volsyncNow = func() time.Time { return time.Unix(42, 0) }
		volsyncSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		require.NoError(t, snapshotApp("media", "paperless", true, 30*time.Second))
		assert.Equal(t, []time.Duration{5 * time.Second}, sleeps)
	})

	t.Run("cancelled app selection exits cleanly", func(t *testing.T) {
		selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
			return "media", nil
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte("paperless"), nil
		}
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			return "", errors.New("cancelled by user")
		}

		require.NoError(t, snapshotApp("", "", false, time.Minute))
	})
}

func TestSnapshotAllApps(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	oldSnapshotApp := snapshotAppFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
		snapshotAppFn = oldSnapshotApp
	})

	kubectlRunFn = func(args ...string) error { return nil }
	kubectlCombinedOutputFn = func(args ...string) ([]byte, error) {
		return []byte("media paperless\nmedia audiobookshelf\n"), nil
	}

	t.Run("dry run succeeds", func(t *testing.T) {
		snapshotAppFn = func(namespace, app string, wait bool, timeout time.Duration) error {
			t.Fatalf("snapshotApp should not be called in dry-run mode")
			return nil
		}
		require.NoError(t, snapshotAllApps("media", true, time.Minute, true, 0))
	})

	t.Run("aggregates failures", func(t *testing.T) {
		var seen []string
		snapshotAppFn = func(namespace, app string, wait bool, timeout time.Duration) error {
			seen = append(seen, namespace+"/"+app)
			if app == "audiobookshelf" {
				return errors.New("boom")
			}
			return nil
		}

		err := snapshotAllApps("media", false, time.Minute, false, 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "1 snapshots failed")
		assert.ElementsMatch(t, []string{"media/paperless", "media/audiobookshelf"}, seen)
	})
}

func TestRestoreAllApps(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	oldRestoreApp := restoreAppFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
		restoreAppFn = oldRestoreApp
	})

	kubectlRunFn = func(args ...string) error { return nil }
	kubectlCombinedOutputFn = func(args ...string) ([]byte, error) {
		return []byte("media paperless\nmedia audiobookshelf\n"), nil
	}

	t.Run("dry run does not restore", func(t *testing.T) {
		restoreAppFn = func(namespace, app, previous string, force bool, restoreTimeout time.Duration) error {
			t.Fatalf("restoreApp should not be called in dry-run mode")
			return nil
		}
		require.NoError(t, restoreAllApps("media", "17", true))
	})

	t.Run("aggregates restore failures", func(t *testing.T) {
		restoreAppFn = func(namespace, app, previous string, force bool, restoreTimeout time.Duration) error {
			if app == "audiobookshelf" {
				return errors.New("restore failed")
			}
			return nil
		}

		err := restoreAllApps("media", "17", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "1 restore(s) failed")
	})
}

func TestRestoreApp(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldCommandRun := commandRunFn
	oldCommandOutput := commandOutputFn
	oldCommandCombinedOutput := commandCombinedOutputFn
	oldFluxRun := fluxRunFn
	oldRenderTemplate := renderVolsyncTemplateFn
	oldApplyYAML := kubectlApplyYAMLFn
	oldSleep := volsyncSleep
	oldFilter := filterOptionFn
	oldConfirm := confirmActionFn
	oldKubectlOutput := kubectlOutputFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		commandRunFn = oldCommandRun
		commandOutputFn = oldCommandOutput
		commandCombinedOutputFn = oldCommandCombinedOutput
		fluxRunFn = oldFluxRun
		renderVolsyncTemplateFn = oldRenderTemplate
		kubectlApplyYAMLFn = oldApplyYAML
		volsyncSleep = oldSleep
		filterOptionFn = oldFilter
		confirmActionFn = oldConfirm
		kubectlOutputFn = oldKubectlOutput
	})

	t.Run("successful restore with explicit snapshot", func(t *testing.T) {
		kubectlRunFn = func(args ...string) error {
			switch {
			case len(args) == 3 && args[0] == "get" && args[1] == "namespace" && args[2] == "media":
				return nil
			case len(args) >= 5 && args[0] == "--namespace" && args[1] == "media" && args[2] == "get" && args[3] == "deployment" && args[4] == "paperless":
				return nil
			default:
				return fmt.Errorf("unexpected kubectlRun args: %v", args)
			}
		}
		commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			switch {
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "scale":
				return []byte("scaled"), nil
			default:
				return nil, fmt.Errorf("unexpected combined args: %v", args)
			}
		}
		commandRunFn = func(name string, args ...string) error {
			switch {
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "wait" && args[3] == "pod":
				return nil
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "get" && strings.HasPrefix(args[3], "job/volsync-dst-paperless-manual"):
				return nil
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "wait" && strings.HasPrefix(args[3], "job/volsync-dst-paperless-manual"):
				return nil
			case len(args) >= 4 && args[0] == "--namespace" && args[2] == "delete" && args[3] == "replicationdestination":
				return nil
			default:
				return fmt.Errorf("unexpected run args: %v", args)
			}
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := strings.Join(args, " ")
			switch key {
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.sourcePVC}`:
				return []byte("paperless"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.cacheStorageClassName}`:
				return []byte("ceph-block"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.cacheCapacity}`:
				return []byte("10Gi"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.moverSecurityContext.runAsUser}`:
				return []byte("1000"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.moverSecurityContext.runAsGroup}`:
				return []byte("1000"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.storageClassName}`:
				return []byte("ceph-block"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.volumeSnapshotClassName}`:
				return []byte("csi-ceph-blockpool"), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.accessModes}`:
				return []byte(`["ReadWriteOnce"]`), nil
			case `--namespace media get replicationsources/paperless --output=jsonpath={.spec.kopia.cacheAccessModes}`:
				return []byte(`["ReadWriteOnce"]`), nil
			default:
				return nil, fmt.Errorf("unexpected output args: %s", key)
			}
		}
		fluxRunFn = func(args ...string) error { return nil }
		renderVolsyncTemplateFn = func(name string, env map[string]string) (string, error) {
			assert.Equal(t, "replicationdestination.yaml.j2", name)
			assert.Equal(t, "media", env["NS"])
			assert.Equal(t, "paperless", env["APP"])
			assert.Equal(t, "17", env["PREVIOUS"])
			assert.Equal(t, "paperless", env["CLAIM"])
			return "kind: ReplicationDestination", nil
		}
		kubectlApplyYAMLFn = func(yaml string) ([]byte, error) {
			assert.Contains(t, yaml, "ReplicationDestination")
			return []byte("applied"), nil
		}
		volsyncSleep = func(time.Duration) {}

		require.NoError(t, restoreApp("media", "paperless", "17", true, time.Minute))
	})

	t.Run("cancelled confirmation exits with error", func(t *testing.T) {
		filterOptionFn = func(prompt string, options []string) (string, error) {
			return "17", nil
		}
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			return false, errors.New("cancelled by user")
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte("17"), nil
		}

		err := restoreApp("media", "paperless", "", false, time.Minute)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "confirmation failed")
	})
}

func TestListSnapshots(t *testing.T) {
	oldKubectlOutput := kubectlOutputFn
	oldCommandCombinedOutput := commandCombinedOutputFn
	t.Cleanup(func() {
		kubectlOutputFn = oldKubectlOutput
		commandCombinedOutputFn = oldCommandCombinedOutput
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return []byte("kopia-0"), nil
	}
	commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte(`paperless@media:/data
2025-08-18 10:00:00 UTC abcd1234 10.0 GB (latest-1)
`), nil
	}

	require.NoError(t, listSnapshots("paperless", "json"))

	err := listSnapshots("paperless", "xml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported format")
}

func TestDiscoverReplicationSources(t *testing.T) {
	oldKubectlRun := kubectlRunFn
	oldKubectlCombinedOutput := kubectlCombinedOutputFn
	t.Cleanup(func() {
		kubectlRunFn = oldKubectlRun
		kubectlCombinedOutputFn = oldKubectlCombinedOutput
	})

	t.Run("namespace validation failure", func(t *testing.T) {
		kubectlRunFn = func(args ...string) error {
			return errors.New("missing namespace")
		}
		_, err := discoverReplicationSources("missing")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "namespace 'missing' does not exist")
	})

	t.Run("no resources found returns empty slice", func(t *testing.T) {
		kubectlRunFn = func(args ...string) error { return nil }
		kubectlCombinedOutputFn = func(args ...string) ([]byte, error) {
			return []byte("No resources found"), errors.New("No resources found")
		}

		sources, err := discoverReplicationSources("media")
		require.NoError(t, err)
		assert.Empty(t, sources)
	})
}
