package kubernetes

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncSecrets(t *testing.T) {
	oldOutput := commandOutputFn
	oldRun := commandRunFn
	oldNow := nowFn
	t.Cleanup(func() {
		commandOutputFn = oldOutput
		commandRunFn = oldRun
		nowFn = oldNow
	})

	commandOutputFn = func(name string, args ...string) ([]byte, error) {
		require.Equal(t, "kubectl", name)
		return []byte("media,paperless\ndefault,homepage\n"), nil
	}
	nowFn = func() time.Time { return time.Unix(42, 0) }

	var calls []string
	commandRunFn = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}

	require.NoError(t, syncSecrets(false))
	assert.Equal(t, []string{
		"kubectl --namespace media annotate externalsecret paperless force-sync=42 --overwrite",
		"kubectl --namespace default annotate externalsecret homepage force-sync=42 --overwrite",
	}, calls)

	calls = nil
	require.NoError(t, syncSecrets(true))
	assert.Empty(t, calls)
}

func TestCleansePods(t *testing.T) {
	oldSelectNamespace := selectNamespaceFn
	oldChooseMulti := chooseMultiOptionFn
	oldOutput := commandOutputFn
	oldCombinedOutput := commandCombinedOutputFn
	t.Cleanup(func() {
		selectNamespaceFn = oldSelectNamespace
		chooseMultiOptionFn = oldChooseMulti
		commandOutputFn = oldOutput
		commandCombinedOutputFn = oldCombinedOutput
	})

	t.Run("interactive dry run", func(t *testing.T) {
		selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
			assert.True(t, allowAll)
			return "media", nil
		}
		chooseMultiOptionFn = func(prompt string, options []string, limit int) ([]string, error) {
			return []string{"Failed", "Completed"}, nil
		}
		var calls []string
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return []byte("pod/paperless pod/homepage"), nil
		}

		require.NoError(t, cleansePods("", "", true))
		assert.Len(t, calls, 2)
		assert.Contains(t, calls[0], "status.phase=Failed")
		assert.Contains(t, calls[1], "status.phase=Succeeded")
	})

	t.Run("non dry run deletes pods", func(t *testing.T) {
		commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "kubectl", name)
			return []byte("pod \"paperless\" deleted\npod \"homepage\" deleted\n"), nil
		}
		require.NoError(t, cleansePods("media", "failed", false))
	})
}

func TestViewSecret(t *testing.T) {
	oldOutput := commandOutputFn
	oldFilter := filterOptionFn
	oldDecode := decodeBase64Fn
	oldSelectNamespace := selectNamespaceFn
	t.Cleanup(func() {
		commandOutputFn = oldOutput
		filterOptionFn = oldFilter
		decodeBase64Fn = oldDecode
		selectNamespaceFn = oldSelectNamespace
	})

	t.Run("interactive selection and key output", func(t *testing.T) {
		filterOptionFn = func(prompt string, options []string) (string, error) {
			assert.Equal(t, []string{"app-secret"}, options)
			return "app-secret", nil
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secrets -n media -o jsonpath={.items[*].metadata.name}":
				return []byte("app-secret"), nil
			case "kubectl get secret app-secret -n media --template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}":
				return []byte("username\npassword\n"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.username}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("admin"))), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.password}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("secret"))), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}
		decodeBase64Fn = func(value string) ([]byte, error) {
			return base64.StdEncoding.DecodeString(value)
		}

		stdout, _, err := testutil.CaptureOutput(func() {
			require.NoError(t, viewSecret("media", "", "table", "password"))
		})
		require.NoError(t, err)
		assert.Contains(t, stdout, "KEY: password")
		assert.Contains(t, stdout, "DECODED_BYTES: 6")
		assert.Contains(t, stdout, "SHA256_PREFIX: "+secretFingerprintPrefix([]byte("secret")))
		assert.NotContains(t, stdout, "VALUE:")
		assert.NotContains(t, stdout, "secret")
	})

	t.Run("json output is safe and includes decode fallback metadata", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secret app-secret -n media --template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}":
				return []byte("config\ntoken\n"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.config}":
				return []byte("%%%"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.token}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("abc123"))), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}
		decodeBase64Fn = func(value string) ([]byte, error) {
			if value == "%%%" {
				return nil, errors.New("bad base64")
			}
			return base64.StdEncoding.DecodeString(value)
		}

		stdout, _, err := testutil.CaptureOutput(func() {
			require.NoError(t, viewSecret("media", "app-secret", "json", ""))
		})
		require.NoError(t, err)
		assert.Contains(t, stdout, "\"token\"")
		assert.Contains(t, stdout, "\"decodedBytes\": 6")
		assert.Contains(t, stdout, "\"sha256Prefix\": \""+secretFingerprintPrefix([]byte("abc123"))+"\"")
		assert.Contains(t, stdout, "\"decodeError\": \"bad base64\"")
		assert.NotContains(t, stdout, "abc123")
	})

	t.Run("yaml output is safe and uses bracket fallback for dotted keys", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secret app-secret -n media --template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}":
				return []byte("tls.crt\n"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.tls.crt}":
				return nil, fmt.Errorf("unsupported dotted key")
			case "kubectl get secret app-secret -n media -o jsonpath={.data['tls.crt']}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("line1\nline2"))), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}
		decodeBase64Fn = func(value string) ([]byte, error) {
			return base64.StdEncoding.DecodeString(value)
		}

		stdout, _, err := testutil.CaptureOutput(func() {
			require.NoError(t, viewSecret("media", "app-secret", "yaml", ""))
		})
		require.NoError(t, err)
		assert.Contains(t, stdout, "tls.crt:")
		assert.Contains(t, stdout, "decodedBytes: 11")
		assert.Contains(t, stdout, "sha256Prefix: "+secretFingerprintPrefix([]byte("line1\nline2")))
		assert.NotContains(t, stdout, "line1")
		assert.NotContains(t, stdout, "line2")
	})

	t.Run("table output renders safe metadata only", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secret app-secret -n media --template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}":
				return []byte("token\n"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.token}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("abc123"))), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}
		decodeBase64Fn = func(value string) ([]byte, error) {
			return base64.StdEncoding.DecodeString(value)
		}

		stdout, _, err := testutil.CaptureOutput(func() {
			require.NoError(t, viewSecret("media", "app-secret", "table", ""))
		})
		require.NoError(t, err)
		assert.Contains(t, stdout, "KEY: token")
		assert.Contains(t, stdout, "DECODED_BYTES: 6")
		assert.Contains(t, stdout, "SHA256_PREFIX: "+secretFingerprintPrefix([]byte("abc123")))
		assert.NotContains(t, stdout, "VALUE:")
		assert.NotContains(t, stdout, "abc123")
	})

	t.Run("unsafe reveal requires both intent flags", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(newViewSecretCommand(), "app-secret", "--unsafe-reveal-values")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--i-understand-this-prints-secrets")

		_, err = testutil.ExecuteCommand(newViewSecretCommand(), "app-secret", "--i-understand-this-prints-secrets")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--unsafe-reveal-values")
	})

	t.Run("unsafe reveal refuses non terminal stdout without force", func(t *testing.T) {
		_, _, err := testutil.CaptureOutput(func() {
			_, execErr := testutil.ExecuteCommand(
				newViewSecretCommand(),
				"app-secret",
				"--unsafe-reveal-values",
				"--i-understand-this-prints-secrets",
			)
			require.Error(t, execErr)
			assert.Contains(t, execErr.Error(), "stdout is not a terminal")
			assert.Contains(t, execErr.Error(), "--unsafe-force-non-tty")
		})
		require.NoError(t, err)
	})

	t.Run("unsafe reveal with force prints fake key value", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secret app-secret -n media --template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}":
				return []byte("token\n"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.token}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("fake-token-value"))), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}
		decodeBase64Fn = func(value string) ([]byte, error) {
			return base64.StdEncoding.DecodeString(value)
		}

		stdout, _, err := testutil.CaptureOutput(func() {
			_, execErr := testutil.ExecuteCommand(
				newViewSecretCommand(),
				"app-secret",
				"--namespace", "media",
				"--key", "token",
				"--unsafe-reveal-values",
				"--i-understand-this-prints-secrets",
				"--unsafe-force-non-tty",
			)
			require.NoError(t, execErr)
		})
		require.NoError(t, err)
		assert.Equal(t, "fake-token-value\n", stdout)
	})

	t.Run("empty default namespace falls back to namespace selection", func(t *testing.T) {
		selectNamespaceFn = func(prompt string, allowAll bool) (string, error) {
			assert.Equal(t, "Select namespace:", prompt)
			assert.False(t, allowAll)
			return "media", nil
		}
		filterOptionFn = func(prompt string, options []string) (string, error) {
			assert.Equal(t, []string{"app-secret"}, options)
			return "app-secret", nil
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secrets -n default -o jsonpath={.items[*].metadata.name}":
				return []byte(""), nil
			case "kubectl get secrets -n media -o jsonpath={.items[*].metadata.name}":
				return []byte("app-secret"), nil
			case "kubectl get secret app-secret -n media --template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}":
				return []byte("token\n"), nil
			case "kubectl get secret app-secret -n media -o jsonpath={.data.token}":
				return []byte(base64.StdEncoding.EncodeToString([]byte("abc123"))), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}
		decodeBase64Fn = func(value string) ([]byte, error) {
			return base64.StdEncoding.DecodeString(value)
		}

		require.NoError(t, viewSecret("default", "", "table", "token"))
	})

	t.Run("empty non-default namespace returns actionable error", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			switch key {
			case "kubectl get secrets -n media -o jsonpath={.items[*].metadata.name}":
				return []byte(""), nil
			default:
				return nil, fmt.Errorf("unexpected args: %s", key)
			}
		}

		err := viewSecret("media", "", "table", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no secrets found in namespace media")
		assert.Contains(t, err.Error(), "--namespace")
	})
}

func secretFingerprintPrefix(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum)[:12]
}

func TestSyncFluxResources(t *testing.T) {
	oldChoose := chooseOptionFn
	oldConfirm := confirmActionFn
	oldOutput := commandOutputFn
	oldRun := commandRunFn
	t.Cleanup(func() {
		chooseOptionFn = oldChoose
		confirmActionFn = oldConfirm
		commandOutputFn = oldOutput
		commandRunFn = oldRun
	})

	t.Run("dry run with interactive type selection", func(t *testing.T) {
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			return "helmrelease - Helm releases", nil
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte("media,paperless\ndefault,homepage\n"), nil
		}
		require.NoError(t, syncFluxResources("", "", false, true))
	})

	t.Run("parallel reconcile with confirmation", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			var lines []string
			for i := 0; i < 6; i++ {
				lines = append(lines, fmt.Sprintf("ns%d,app%d", i, i))
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			assert.Contains(t, message, "About to sync 6 gitrepository resources")
			return true, nil
		}
		var calls []string
		var mu sync.Mutex
		commandRunFn = func(name string, args ...string) error {
			mu.Lock()
			calls = append(calls, name+" "+strings.Join(args, " "))
			mu.Unlock()
			return nil
		}

		require.NoError(t, syncFluxResources("gitrepo", "", true, false))
		assert.Len(t, calls, 6)
		for i := 0; i < 6; i++ {
			assert.Contains(t, calls, fmt.Sprintf("flux reconcile gitrepository app%d -n ns%d", i, i))
		}
	})

	t.Run("invalid type returns validation error", func(t *testing.T) {
		err := syncFluxResources("invalid-type", "", false, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid resource type")
	})

	t.Run("confirmation can cancel large sync", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			var lines []string
			for i := 0; i < 6; i++ {
				lines = append(lines, fmt.Sprintf("ns%d,app%d", i, i))
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			return false, nil
		}
		commandRunFn = func(name string, args ...string) error {
			t.Fatalf("unexpected reconcile call: %s %s", name, strings.Join(args, " "))
			return nil
		}

		require.NoError(t, syncFluxResources("gitrepo", "", false, false))
	})
}

func TestForceSyncExternalSecret(t *testing.T) {
	oldOutput := commandOutputFn
	oldRun := commandRunFn
	oldNow := nowFn
	t.Cleanup(func() {
		commandOutputFn = oldOutput
		commandRunFn = oldRun
		nowFn = oldNow
	})

	nowFn = func() time.Time { return time.Unix(77, 0) }
	commandOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte("paperless homepage"), nil
	}

	var calls []string
	commandRunFn = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}

	require.NoError(t, forceSyncExternalSecret("media", "", true, 60))
	assert.Equal(t, []string{
		"kubectl --namespace media annotate externalsecret paperless force-sync=77 --overwrite",
		"kubectl --namespace media annotate externalsecret homepage force-sync=77 --overwrite",
	}, calls)
}

func TestUpgradeARC(t *testing.T) {
	oldCombined := commandCombinedOutputFn
	oldRun := commandRunFn
	oldSleep := sleepFn
	t.Cleanup(func() {
		commandCombinedOutputFn = oldCombined
		commandRunFn = oldRun
		sleepFn = oldSleep
	})

	commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "actions-runner-controller" {
			return []byte("release: not found"), errors.New("failed")
		}
		return []byte("ok"), nil
	}
	var calls []string
	commandRunFn = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	sleepFn = func(time.Duration) {}

	require.NoError(t, upgradeARC())
	assert.Equal(t, []string{
		"flux -n actions-runner-system reconcile hr actions-runner-controller",
		"flux -n actions-runner-system reconcile hr home-ops-runner",
	}, calls)
}

func TestBrowsePVC(t *testing.T) {
	oldKubectlOutput := kubectlOutputFn
	oldChoose := chooseOptionFn
	oldKubectlRun := kubectlRunFn
	oldLookPath := lookPathFn
	oldInstallPlugin := installKubectlPluginFn
	oldKubectlInteractive := kubectlRunInteractiveFn
	oldSleep := sleepFn
	t.Cleanup(func() {
		kubectlOutputFn = oldKubectlOutput
		chooseOptionFn = oldChoose
		kubectlRunFn = oldKubectlRun
		lookPathFn = oldLookPath
		installKubectlPluginFn = oldInstallPlugin
		kubectlRunInteractiveFn = oldKubectlInteractive
		sleepFn = oldSleep
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "get pvc -n media -o jsonpath={.items[*].metadata.name}":
			return []byte("paperless media"), nil
		case "get pods -n media -o jsonpath={.items[*].metadata.name}":
			return []byte(""), nil
		default:
			return nil, fmt.Errorf("unexpected kubectl output args: %s", strings.Join(args, " "))
		}
	}
	chooseOptionFn = func(prompt string, options []string) (string, error) {
		assert.Equal(t, []string{"paperless", "media"}, options)
		return "paperless", nil
	}

	var runCalls []string
	kubectlRunFn = func(args ...string) error {
		runCalls = append(runCalls, strings.Join(args, " "))
		return nil
	}

	lookPathFn = func(file string) (string, error) {
		if file == "kubectl-browse-pvc" {
			return "", errors.New("missing")
		}
		return "/usr/bin/" + file, nil
	}

	var installedPlugin string
	installKubectlPluginFn = func(plugin string) error {
		installedPlugin = plugin
		return nil
	}

	var interactiveArgs []string
	kubectlRunInteractiveFn = func(args ...string) error {
		interactiveArgs = args
		return nil
	}
	sleepFn = func(time.Duration) {}

	require.NoError(t, browsePVC("media", "", "alpine:latest"))
	assert.Equal(t, "browse-pvc", installedPlugin)
	assert.Equal(t, []string{"--namespace media get persistentvolumeclaims paperless"}, runCalls)
	assert.Equal(t, []string{"browse-pvc", "--namespace", "media", "--image", "alpine:latest", "paperless"}, interactiveArgs)
}

func TestNodeShell(t *testing.T) {
	oldKubectlOutput := kubectlOutputFn
	oldChoose := chooseOptionFn
	oldKubectlRun := kubectlRunFn
	oldLookPath := lookPathFn
	oldInstallPlugin := installKubectlPluginFn
	oldKubectlInteractive := kubectlRunInteractiveFn
	t.Cleanup(func() {
		kubectlOutputFn = oldKubectlOutput
		chooseOptionFn = oldChoose
		kubectlRunFn = oldKubectlRun
		lookPathFn = oldLookPath
		installKubectlPluginFn = oldInstallPlugin
		kubectlRunInteractiveFn = oldKubectlInteractive
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		assert.Equal(t, []string{"get", "nodes", "-o", "jsonpath={.items[*].metadata.name}"}, args)
		return []byte("cp01 wk01"), nil
	}
	chooseOptionFn = func(prompt string, options []string) (string, error) {
		assert.Equal(t, []string{"cp01", "wk01"}, options)
		return "wk01", nil
	}

	var runCalls []string
	kubectlRunFn = func(args ...string) error {
		runCalls = append(runCalls, strings.Join(args, " "))
		return nil
	}
	lookPathFn = func(file string) (string, error) {
		assert.Equal(t, "kubectl-node-shell", file)
		return "/usr/bin/kubectl-node-shell", nil
	}
	installKubectlPluginFn = func(plugin string) error {
		t.Fatalf("unexpected plugin install: %s", plugin)
		return nil
	}

	var interactiveArgs []string
	kubectlRunInteractiveFn = func(args ...string) error {
		interactiveArgs = args
		return nil
	}

	require.NoError(t, nodeShell(""))
	assert.Equal(t, []string{"get nodes wk01"}, runCalls)
	assert.Equal(t, []string{"node-shell", "-n", "kube-system", "-x", "wk01"}, interactiveArgs)
}

func TestKustomizationFlows(t *testing.T) {
	oldSpinOutput := spinWithOutputFn
	oldSpinFunc := spinWithFuncFn
	oldFilter := filterOptionFn
	oldFluxBuild := fluxBuildKustomizationFn
	oldApply := kubectlApplyManifestFn
	oldDelete := kubectlDeleteManifestFn
	t.Cleanup(func() {
		spinWithOutputFn = oldSpinOutput
		spinWithFuncFn = oldSpinFunc
		filterOptionFn = oldFilter
		fluxBuildKustomizationFn = oldFluxBuild
		kubectlApplyManifestFn = oldApply
		kubectlDeleteManifestFn = oldDelete
	})

	repoDir := t.TempDir()
	appDir := filepath.Join(repoDir, "kubernetes", "apps", "network", "envoy", "app")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	ksPath := filepath.Join(repoDir, "kubernetes", "apps", "network", "envoy", "ks.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(ksPath), 0o755))
	require.NoError(t, os.WriteFile(ksPath, []byte(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: envoy
spec:
  path: ./kubernetes/apps/network/envoy/app
  targetNamespace: network
`), 0o644))
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())
	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(repoDir))

	spinWithOutputFn = func(title string, command string, args ...string) (string, error) {
		return "kind: ConfigMap", nil
	}
	spinWithFuncFn = func(title string, fn func() error) error { return fn() }
	fluxBuildKustomizationFn = func(name, namespace, path, ksFile string) ([]byte, error) {
		return []byte("kind: ConfigMap"), nil
	}

	var applied, deleted string
	kubectlApplyManifestFn = func(manifest string) error {
		applied = manifest
		return nil
	}
	kubectlDeleteManifestFn = func(manifest string) error {
		deleted = manifest
		return nil
	}

	require.NoError(t, renderKustomization(ksPath, "", ""))
	require.NoError(t, applyKustomization(ksPath, "", false))
	require.NoError(t, deleteKustomization(ksPath, ""))
	assert.Equal(t, "kind: ConfigMap", applied)
	assert.Equal(t, "kind: ConfigMap", deleted)

	filterCalls := 0
	filterOptionFn = func(prompt string, options []string) (string, error) {
		filterCalls++
		return options[0], nil
	}
	selectedPath, selectedName, err := selectKustomizationFile()
	require.NoError(t, err)
	expectedPath, err := filepath.EvalSymlinks(ksPath)
	require.NoError(t, err)
	actualPath, err := filepath.EvalSymlinks(selectedPath)
	require.NoError(t, err)
	assert.Equal(t, expectedPath, actualPath)
	assert.Equal(t, "envoy", selectedName)
	assert.Equal(t, 1, filterCalls)
}

func TestSelectKustomizationFileMultipleDocuments(t *testing.T) {
	oldFilter := filterOptionFn
	t.Cleanup(func() {
		filterOptionFn = oldFilter
	})

	repoDir := t.TempDir()
	appDir := filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana", "app")
	instanceDir := filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana", "instance")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.MkdirAll(instanceDir, 0o755))

	ksPath := filepath.Join(repoDir, "kubernetes", "apps", "observability", "grafana", "ks.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(ksPath), 0o755))
	require.NoError(t, os.WriteFile(ksPath, []byte(`---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: grafana
spec:
  path: ./kubernetes/apps/observability/grafana/app
  targetNamespace: observability
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: grafana-instance
spec:
  path: ./kubernetes/apps/observability/grafana/instance
  targetNamespace: observability
`), 0o644))

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(repoDir))

	filterCalls := 0
	filterOptionFn = func(prompt string, options []string) (string, error) {
		filterCalls++
		switch filterCalls {
		case 1:
			assert.Equal(t, "Search for Kustomization:", prompt)
			require.Len(t, options, 1)
			return options[0], nil
		case 2:
			assert.Equal(t, "Select Kustomization document:", prompt)
			assert.Contains(t, options, "grafana (observability)")
			assert.Contains(t, options, "grafana-instance (observability)")
			return "grafana-instance (observability)", nil
		default:
			t.Fatalf("unexpected filter prompt: %s", prompt)
			return "", nil
		}
	}

	selectedPath, selectedName, err := selectKustomizationFile()
	require.NoError(t, err)
	expectedPath, err := filepath.EvalSymlinks(ksPath)
	require.NoError(t, err)
	actualPath, err := filepath.EvalSymlinks(selectedPath)
	require.NoError(t, err)
	assert.Equal(t, expectedPath, actualPath)
	assert.Equal(t, "grafana-instance", selectedName)
	assert.Equal(t, 2, filterCalls)
}

func TestKubernetesCommandWrappers(t *testing.T) {
	oldChoose := chooseOptionFn
	oldConfirm := confirmActionFn
	oldOutput := commandOutputFn
	oldRun := commandRunFn
	oldCombinedOutput := commandCombinedOutputFn
	oldSpinOutput := spinWithOutputFn
	oldSpinFunc := spinWithFuncFn
	oldFluxBuild := fluxBuildKustomizationFn
	oldDelete := kubectlDeleteManifestFn
	oldSleep := sleepFn
	t.Cleanup(func() {
		chooseOptionFn = oldChoose
		confirmActionFn = oldConfirm
		commandOutputFn = oldOutput
		commandRunFn = oldRun
		commandCombinedOutputFn = oldCombinedOutput
		spinWithOutputFn = oldSpinOutput
		spinWithFuncFn = oldSpinFunc
		fluxBuildKustomizationFn = oldFluxBuild
		kubectlDeleteManifestFn = oldDelete
		sleepFn = oldSleep
	})

	repoDir := t.TempDir()
	appDir := filepath.Join(repoDir, "kubernetes", "apps", "network", "envoy", "app")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	ksPath := filepath.Join(repoDir, "kubernetes", "apps", "network", "envoy", "ks.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(ksPath), 0o755))
	require.NoError(t, os.WriteFile(ksPath, []byte(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: envoy
spec:
  path: ./kubernetes/apps/network/envoy/app
  targetNamespace: network
`), 0o644))
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())
	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()
	require.NoError(t, os.Chdir(repoDir))

	t.Run("sync command dry run", func(t *testing.T) {
		chooseOptionFn = func(prompt string, options []string) (string, error) {
			return "gitrepository - Git repositories", nil
		}
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte("flux-system,flux-system\n"), nil
		}
		cmd := newSyncCommand()
		_, err := testutil.ExecuteCommand(cmd, "--dry-run")
		require.NoError(t, err)
	})

	t.Run("force sync externalsecret command", func(t *testing.T) {
		commandOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte("paperless"), nil
		}
		commandRunFn = func(name string, args ...string) error { return nil }
		cmd := newForceSyncExternalSecretCommand()
		_, err := testutil.ExecuteCommand(cmd, "--namespace", "media", "--all")
		require.NoError(t, err)
	})

	t.Run("upgrade arc command force", func(t *testing.T) {
		commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
			return []byte("ok"), nil
		}
		commandRunFn = func(name string, args ...string) error { return nil }
		sleepFn = func(time.Duration) {}
		cmd := newUpgradeARCCommand()
		_, err := testutil.ExecuteCommand(cmd, "--force")
		require.NoError(t, err)
	})

	t.Run("upgrade arc command can be cancelled", func(t *testing.T) {
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			assert.Contains(t, message, "uninstall and reinstall ARC")
			return false, nil
		}
		cmd := newUpgradeARCCommand()
		_, err := testutil.ExecuteCommand(cmd)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upgrade cancelled")
	})

	t.Run("render ks command writes file", func(t *testing.T) {
		spinWithOutputFn = func(title string, command string, args ...string) (string, error) {
			return "kind: ConfigMap", nil
		}
		outFile := filepath.Join(t.TempDir(), "rendered.yaml")
		cmd := newRenderKsCommand()
		_, err := testutil.ExecuteCommand(cmd, ksPath, "--output", outFile)
		require.NoError(t, err)
		content, err := os.ReadFile(outFile)
		require.NoError(t, err)
		assert.Equal(t, "kind: ConfigMap", string(content))
	})

	t.Run("apply ks command dry run", func(t *testing.T) {
		spinWithOutputFn = func(title string, command string, args ...string) (string, error) {
			return "kind: ConfigMap", nil
		}
		cmd := newApplyKsCommand()
		_, err := testutil.ExecuteCommand(cmd, ksPath, "--dry-run")
		require.NoError(t, err)
	})

	t.Run("delete ks command force", func(t *testing.T) {
		fluxBuildKustomizationFn = func(name, namespace, path, ksFile string) ([]byte, error) {
			return []byte("kind: ConfigMap"), nil
		}
		spinWithFuncFn = func(title string, fn func() error) error { return fn() }
		kubectlDeleteManifestFn = func(manifest string) error { return nil }
		cmd := newDeleteKsCommand()
		_, err := testutil.ExecuteCommand(cmd, ksPath, "--force")
		require.NoError(t, err)
	})

	t.Run("delete ks command can be cancelled", func(t *testing.T) {
		confirmActionFn = func(message string, defaultYes bool) (bool, error) {
			assert.Contains(t, message, "delete all resources in the Kustomization")
			return false, nil
		}
		cmd := newDeleteKsCommand()
		_, err := testutil.ExecuteCommand(cmd, ksPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deletion cancelled")
	})
}
