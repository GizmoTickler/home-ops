package bootstrap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/metrics"

	"homeops-cli/internal/common"
)

func TestNewCommandUsesWorkingDirectoryAndRunsBootstrap(t *testing.T) {
	oldWorkingDirectory := bootstrapWorkingDirectory
	oldRunWithSpinner := bootstrapRunWithSpinner
	oldResetTerminal := bootstrapResetTerminal
	oldSleep := bootstrapSleep
	oldValidatePrereqs := bootstrapValidatePrereqs
	oldApplyTalosConfig := bootstrapApplyTalosConfig
	oldBootstrapTalos := bootstrapBootstrapTalos
	oldFetchKubeconfig := bootstrapFetchKubeconfig
	oldValidateKubeconfig := bootstrapValidateKubeconfig
	oldWaitForNodes := bootstrapWaitForNodes
	oldApplyNamespaces := bootstrapApplyNamespaces

	t.Cleanup(func() {
		bootstrapWorkingDirectory = oldWorkingDirectory
		bootstrapRunWithSpinner = oldRunWithSpinner
		bootstrapResetTerminal = oldResetTerminal
		bootstrapSleep = oldSleep
		bootstrapValidatePrereqs = oldValidatePrereqs
		bootstrapApplyTalosConfig = oldApplyTalosConfig
		bootstrapBootstrapTalos = oldBootstrapTalos
		bootstrapFetchKubeconfig = oldFetchKubeconfig
		bootstrapValidateKubeconfig = oldValidateKubeconfig
		bootstrapWaitForNodes = oldWaitForNodes
		bootstrapApplyNamespaces = oldApplyNamespaces
	})

	bootstrapWorkingDirectory = func() string { return "/repo/home-ops" }
	bootstrapRunWithSpinner = func(_ string, _ bool, _ interface {
		Info(string, ...interface{})
		SetQuiet(bool)
	}, fn func() error) error {
		return fn()
	}
	bootstrapResetTerminal = func() {}
	bootstrapSleep = func(time.Duration) {}
	bootstrapValidatePrereqs = func(*BootstrapConfig) error { return nil }
	bootstrapApplyTalosConfig = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapBootstrapTalos = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapFetchKubeconfig = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapValidateKubeconfig = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapWaitForNodes = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapApplyNamespaces = func(*BootstrapConfig, *common.ColorLogger) error { return nil }

	cmd := NewCommand()
	if got := cmd.Flags().Lookup("root-dir").DefValue; got != "/repo/home-ops" {
		t.Fatalf("unexpected default root-dir: got %q want %q", got, "/repo/home-ops")
	}

	cmd.SetArgs([]string{
		"--kubeconfig", "/tmp/kubeconfig",
		"--talosconfig", "/tmp/talosconfig",
		"--k8s-version", "v1.34.0",
		"--talos-version", "v1.11.0",
		"--skip-preflight",
		"--skip-crds",
		"--skip-resources",
		"--skip-helmfile",
		"--dry-run",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command execution failed: %v", err)
	}
}

func TestPromptBootstrapOptions(t *testing.T) {
	t.Run("applies selected options", func(t *testing.T) {
		oldChoose := bootstrapChoose
		oldChooseMulti := bootstrapChooseMulti
		t.Cleanup(func() {
			bootstrapChoose = oldChoose
			bootstrapChooseMulti = oldChooseMulti
		})

		bootstrapChoose = func(_ string, _ []string) (string, error) {
			return "Dry-Run - Preview what would be done without making changes", nil
		}
		bootstrapChooseMulti = func(_ string, _ []string, _ int) ([]string, error) {
			return []string{"Skip Preflight Checks", "Skip Helmfile", "Enable Verbose Mode"}, nil
		}

		config := &BootstrapConfig{}
		if err := promptBootstrapOptions(config, common.NewColorLogger()); err != nil {
			t.Fatalf("promptBootstrapOptions returned error: %v", err)
		}

		if !config.DryRun || !config.SkipPreflight || !config.SkipHelmfile || !config.Verbose {
			t.Fatalf("unexpected config after prompt: %+v", config)
		}
	})

	t.Run("returns cancelled when mode selection fails", func(t *testing.T) {
		oldChoose := bootstrapChoose
		t.Cleanup(func() { bootstrapChoose = oldChoose })
		bootstrapChoose = func(_ string, _ []string) (string, error) { return "", errors.New("cancelled") }

		err := promptBootstrapOptions(&BootstrapConfig{}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "bootstrap cancelled") {
			t.Fatalf("expected bootstrap cancelled error, got %v", err)
		}
	})

	t.Run("returns cancelled when option selection fails", func(t *testing.T) {
		oldChoose := bootstrapChoose
		oldChooseMulti := bootstrapChooseMulti
		t.Cleanup(func() {
			bootstrapChoose = oldChoose
			bootstrapChooseMulti = oldChooseMulti
		})

		bootstrapChoose = func(_ string, _ []string) (string, error) {
			return "Real Bootstrap - Actually perform the bootstrap", nil
		}
		bootstrapChooseMulti = func(_ string, _ []string, _ int) ([]string, error) {
			return nil, errors.New("cancelled")
		}

		err := promptBootstrapOptions(&BootstrapConfig{}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "options selection cancelled") {
			t.Fatalf("expected options selection cancelled error, got %v", err)
		}
	})
}

func TestRunPreflightChecksUsesInjectedChecks(t *testing.T) {
	oldPreflightChecks := bootstrapPreflightChecks
	t.Cleanup(func() { bootstrapPreflightChecks = oldPreflightChecks })

	bootstrapPreflightChecks = []func(*BootstrapConfig, *common.ColorLogger) *PreflightResult{
		func(*BootstrapConfig, *common.ColorLogger) *PreflightResult {
			return &PreflightResult{Name: "tools", Status: "PASS", Message: "ok"}
		},
		func(*BootstrapConfig, *common.ColorLogger) *PreflightResult {
			return &PreflightResult{Name: "network", Status: "WARN", Message: "slow"}
		},
		func(*BootstrapConfig, *common.ColorLogger) *PreflightResult {
			return &PreflightResult{Name: "talos", Status: "FAIL", Message: "missing nodes"}
		},
	}

	err := runPreflightChecks(&BootstrapConfig{}, common.NewColorLogger())
	if err == nil || !strings.Contains(err.Error(), "talos") {
		t.Fatalf("expected failing preflight result, got %v", err)
	}
}

func TestCommandBuildersWithContext(t *testing.T) {
	ctx := context.Background()

	talosCmd := buildTalosctlCmdContext(ctx, "/tmp/talosconfig", "config", "info")
	if got := strings.Join(talosCmd.Args, " "); got != "talosctl --talosconfig /tmp/talosconfig config info" {
		t.Fatalf("unexpected talosctl context args: %s", got)
	}

	kubectlCmd := buildKubectlCmdContext(ctx, &BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, "get", "nodes")
	if got := strings.Join(kubectlCmd.Args, " "); got != "kubectl get nodes --kubeconfig /tmp/kubeconfig" {
		t.Fatalf("unexpected kubectl context args: %s", got)
	}
}

func TestPreflightCheckHelpers(t *testing.T) {
	t.Run("tool availability reports missing binaries", func(t *testing.T) {
		oldLookPath := bootstrapLookPath
		t.Cleanup(func() { bootstrapLookPath = oldLookPath })

		bootstrapLookPath = func(file string) (string, error) {
			if file == "helmfile" {
				return "", errors.New("missing")
			}
			return "/usr/bin/" + file, nil
		}

		result := checkToolAvailability(&BootstrapConfig{}, common.NewColorLogger())
		if result.Status != "FAIL" || !strings.Contains(result.Message, "helmfile") {
			t.Fatalf("unexpected tool availability result: %+v", result)
		}
	})

	t.Run("environment files pass with versions and talosconfig", func(t *testing.T) {
		talosconfig := filepath.Join(t.TempDir(), "talosconfig")
		if err := os.WriteFile(talosconfig, []byte("config"), 0600); err != nil {
			t.Fatalf("failed to write talosconfig: %v", err)
		}

		result := checkEnvironmentFiles(&BootstrapConfig{
			K8sVersion:   "v1.34.0",
			TalosVersion: "v1.11.0",
			TalosConfig:  talosconfig,
		}, common.NewColorLogger())
		if result.Status != "PASS" {
			t.Fatalf("unexpected environment file result: %+v", result)
		}
	})

	t.Run("network connectivity uses injected transport", func(t *testing.T) {
		oldHTTPDo := bootstrapHTTPDo
		t.Cleanup(func() { bootstrapHTTPDo = oldHTTPDo })

		bootstrapHTTPDo = func(req *http.Request) (*http.Response, error) {
			if req.Method != "HEAD" || req.URL.String() != "https://github.com" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
			}
			return &http.Response{Body: io.NopCloser(strings.NewReader(""))}, nil
		}

		result := checkNetworkConnectivity(&BootstrapConfig{}, common.NewColorLogger())
		if result.Status != "PASS" {
			t.Fatalf("unexpected network result: %+v", result)
		}
	})

	t.Run("dns resolution uses injected resolver", func(t *testing.T) {
		oldLookupHost := bootstrapLookupHost
		t.Cleanup(func() { bootstrapLookupHost = oldLookupHost })

		bootstrapLookupHost = func(_ context.Context, host string) ([]string, error) {
			if host != "github.com" {
				t.Fatalf("unexpected host lookup: %s", host)
			}
			return []string{"140.82.121.3"}, nil
		}

		result := checkDNSResolution(&BootstrapConfig{}, common.NewColorLogger())
		if result.Status != "PASS" {
			t.Fatalf("unexpected dns result: %+v", result)
		}
	})

	t.Run("1password auth and machine rendering use seams", func(t *testing.T) {
		oldEnsureOPAuth := bootstrapEnsureOPAuth
		oldRenderMachineConfig := bootstrapRenderMachineConfig
		oldGetTalosNodes := bootstrapGetTalosNodes
		t.Cleanup(func() {
			bootstrapEnsureOPAuth = oldEnsureOPAuth
			bootstrapRenderMachineConfig = oldRenderMachineConfig
			bootstrapGetTalosNodes = oldGetTalosNodes
		})

		bootstrapEnsureOPAuth = func() error { return nil }
		bootstrapRenderMachineConfig = func(_, _, _ string, _ *common.ColorLogger) ([]byte, error) {
			return []byte("version: v1alpha1"), nil
		}
		bootstrapGetTalosNodes = func(string) ([]string, error) { return []string{"10.0.0.10"}, nil }

		authResult := check1PasswordAuthPreflight(&BootstrapConfig{}, common.NewColorLogger())
		if authResult.Status != "PASS" {
			t.Fatalf("unexpected auth result: %+v", authResult)
		}

		renderResult := checkMachineConfigRendering(&BootstrapConfig{}, common.NewColorLogger())
		if renderResult.Status != "PASS" {
			t.Fatalf("unexpected render result: %+v", renderResult)
		}

		nodeResult := checkTalosNodes(&BootstrapConfig{TalosConfig: "/tmp/talosconfig"}, common.NewColorLogger())
		if nodeResult.Status != "PASS" {
			t.Fatalf("unexpected talos node result: %+v", nodeResult)
		}
	})
}

func TestGetTalosNodes(t *testing.T) {
	oldTalosctlOutput := bootstrapTalosctlOutput
	t.Cleanup(func() { bootstrapTalosctlOutput = oldTalosctlOutput })

	bootstrapTalosctlOutput = func(_ string, _ ...string) ([]byte, error) {
		return []byte(`{"nodes":["10.0.0.10","10.0.0.11"]}`), nil
	}

	nodes, err := getTalosNodes("/tmp/talosconfig")
	if err != nil {
		t.Fatalf("getTalosNodes returned error: %v", err)
	}
	if strings.Join(nodes, ",") != "10.0.0.10,10.0.0.11" {
		t.Fatalf("unexpected nodes: %v", nodes)
	}
}

func TestGetTalosNodesWithRetry(t *testing.T) {
	oldGetTalosNodes := bootstrapGetTalosNodes
	oldSleep := bootstrapSleep
	t.Cleanup(func() {
		bootstrapGetTalosNodes = oldGetTalosNodes
		bootstrapSleep = oldSleep
	})

	attempts := 0
	var sleeps []time.Duration
	bootstrapGetTalosNodes = func(string) ([]string, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("not ready")
		}
		return []string{"10.0.0.10"}, nil
	}
	bootstrapSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

	nodes, err := getTalosNodesWithRetry("/tmp/talosconfig", common.NewColorLogger(), 3)
	if err != nil {
		t.Fatalf("getTalosNodesWithRetry returned error: %v", err)
	}
	if len(nodes) != 1 || nodes[0] != "10.0.0.10" {
		t.Fatalf("unexpected nodes: %v", nodes)
	}
	if len(sleeps) != 2 {
		t.Fatalf("expected 2 retry sleeps, got %v", sleeps)
	}
}

func TestApplyNodeConfigWithRetrySeam(t *testing.T) {
	t.Run("retries until success", func(t *testing.T) {
		oldApplyNodeConfig := bootstrapApplyNodeConfig
		oldSleep := bootstrapSleep
		t.Cleanup(func() {
			bootstrapApplyNodeConfig = oldApplyNodeConfig
			bootstrapSleep = oldSleep
		})

		attempts := 0
		var sleeps []time.Duration
		bootstrapApplyNodeConfig = func(string, []byte) error {
			attempts++
			if attempts < 3 {
				return errors.New("connection refused")
			}
			return nil
		}
		bootstrapSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		if err := applyNodeConfigWithRetry("10.0.0.10", []byte("config"), common.NewColorLogger(), 3); err != nil {
			t.Fatalf("applyNodeConfigWithRetry returned error: %v", err)
		}
		if len(sleeps) != 2 {
			t.Fatalf("expected 2 retry sleeps, got %v", sleeps)
		}
	})

	t.Run("stops on already configured errors", func(t *testing.T) {
		oldApplyNodeConfig := bootstrapApplyNodeConfig
		t.Cleanup(func() { bootstrapApplyNodeConfig = oldApplyNodeConfig })

		bootstrapApplyNodeConfig = func(string, []byte) error { return errors.New("certificate required") }
		err := applyNodeConfigWithRetry("10.0.0.10", []byte("config"), common.NewColorLogger(), 3)
		if err == nil || !strings.Contains(err.Error(), "certificate required") {
			t.Fatalf("expected already-configured error, got %v", err)
		}
	})
}

func TestApplyTalosConfig(t *testing.T) {
	oldGetTalosNodes := bootstrapGetTalosNodes
	oldGetMachineType := bootstrapGetMachineType
	oldRenderMachineConfig := bootstrapRenderMachineConfig
	oldApplyNodeConfigTry := bootstrapApplyNodeConfigTry
	oldRunWithSpinner := bootstrapRunWithSpinner
	oldSleep := bootstrapSleep
	t.Cleanup(func() {
		bootstrapGetTalosNodes = oldGetTalosNodes
		bootstrapGetMachineType = oldGetMachineType
		bootstrapRenderMachineConfig = oldRenderMachineConfig
		bootstrapApplyNodeConfigTry = oldApplyNodeConfigTry
		bootstrapRunWithSpinner = oldRunWithSpinner
		bootstrapSleep = oldSleep
	})

	bootstrapGetTalosNodes = func(string) ([]string, error) { return []string{"10.0.0.10"}, nil }
	bootstrapGetMachineType = func(nodeTemplate string) (string, error) {
		if !strings.Contains(nodeTemplate, "10.0.0.10") {
			t.Fatalf("unexpected node template: %s", nodeTemplate)
		}
		return "controlplane", nil
	}
	bootstrapRenderMachineConfig = func(base, patch, machineType string, _ *common.ColorLogger) ([]byte, error) {
		if base != "controlplane.yaml" || machineType != "controlplane" || !strings.Contains(patch, "10.0.0.10") {
			t.Fatalf("unexpected render args: %s %s %s", base, patch, machineType)
		}
		return []byte("rendered"), nil
	}
	bootstrapApplyNodeConfigTry = func(node string, config []byte, _ *common.ColorLogger, retries int) error {
		if node != "10.0.0.10" || string(config) != "rendered" || retries != 3 {
			t.Fatalf("unexpected apply args: %s %q %d", node, string(config), retries)
		}
		return nil
	}
	bootstrapRunWithSpinner = func(_ string, _ bool, _ interface {
		Info(string, ...interface{})
		SetQuiet(bool)
	}, fn func() error) error {
		return fn()
	}
	bootstrapSleep = func(time.Duration) {}

	if err := applyTalosConfig(&BootstrapConfig{DryRun: false}, common.NewColorLogger()); err != nil {
		t.Fatalf("applyTalosConfig returned error: %v", err)
	}
}

func TestRenderMachineConfigFromEmbeddedSeam(t *testing.T) {
	oldGetTalosTemplate := bootstrapGetTalosTemplate
	oldResolveSecrets := bootstrapResolveSecrets
	oldMergeTalosConfigs := bootstrapMergeTalosConfigs
	t.Cleanup(func() {
		bootstrapGetTalosTemplate = oldGetTalosTemplate
		bootstrapResolveSecrets = oldResolveSecrets
		bootstrapMergeTalosConfigs = oldMergeTalosConfigs
	})

	bootstrapGetTalosTemplate = func(path string) (string, error) {
		switch path {
		case "talos/controlplane.yaml":
			return "version: v1alpha1\nmachine:\n  type: controlplane\n", nil
		case "talos/nodes/10.0.0.10.yaml":
			return "machine:\n  network:\n    hostname: node1\n---\nkind: UserVolumeConfig\nmetadata:\n  name: extra\n", nil
		default:
			return "", errors.New("unexpected template")
		}
	}
	bootstrapResolveSecrets = func(content string, _ *common.ColorLogger) (string, error) { return content, nil }
	bootstrapMergeTalosConfigs = func(baseConfig, patchConfig []byte) ([]byte, error) {
		if !strings.Contains(string(baseConfig), "controlplane") || !strings.Contains(string(patchConfig), "hostname: node1") {
			t.Fatalf("unexpected merge inputs: %q %q", string(baseConfig), string(patchConfig))
		}
		return []byte("merged-machine-config"), nil
	}

	rendered, err := renderMachineConfigFromEmbedded("controlplane.yaml", "nodes/10.0.0.10.yaml", "controlplane", common.NewColorLogger())
	if err != nil {
		t.Fatalf("renderMachineConfigFromEmbedded returned error: %v", err)
	}
	if !strings.Contains(string(rendered), "merged-machine-config") || !strings.Contains(string(rendered), "UserVolumeConfig") {
		t.Fatalf("unexpected rendered config: %q", string(rendered))
	}
}

func TestValidateEtcdRunning(t *testing.T) {
	oldTalosctlCombined := bootstrapTalosctlCombined
	oldTalosctlOutput := bootstrapTalosctlOutput
	oldNow := bootstrapNow
	oldSleep := bootstrapSleep
	oldCheckInterval := bootstrapCheckIntervalNormal
	oldStallTimeout := bootstrapStallTimeout
	oldMaxWait := bootstrapExtSecMaxWait
	t.Cleanup(func() {
		bootstrapTalosctlCombined = oldTalosctlCombined
		bootstrapTalosctlOutput = oldTalosctlOutput
		bootstrapNow = oldNow
		bootstrapSleep = oldSleep
		bootstrapCheckIntervalNormal = oldCheckInterval
		bootstrapStallTimeout = oldStallTimeout
		bootstrapExtSecMaxWait = oldMaxWait
	})

	current := time.Unix(0, 0)
	bootstrapNow = func() time.Time { return current }
	bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
	bootstrapCheckIntervalNormal = time.Second
	bootstrapStallTimeout = 5 * time.Second
	bootstrapExtSecMaxWait = 20 * time.Second

	bootstrapTalosctlCombined = func(_ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "etcd status") && current.Equal((time.Unix(0, 0))) {
			return nil, errors.New("not ready")
		}
		return []byte("ok"), nil
	}
	bootstrapTalosctlOutput = func(_ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "service etcd") {
			return []byte("STATE    Running"), nil
		}
		return nil, errors.New("unexpected args")
	}

	if err := validateEtcdRunning("/tmp/talosconfig", "10.0.0.10", common.NewColorLogger()); err != nil {
		t.Fatalf("validateEtcdRunning returned error: %v", err)
	}
}

func TestBootstrapTalos(t *testing.T) {
	t.Run("returns already bootstrapped when etcd responds", func(t *testing.T) {
		oldGetRandomController := bootstrapGetRandomController
		oldTalosctlCombined := bootstrapTalosctlCombined
		t.Cleanup(func() {
			bootstrapGetRandomController = oldGetRandomController
			bootstrapTalosctlCombined = oldTalosctlCombined
		})

		bootstrapGetRandomController = func(string) (string, error) { return "10.0.0.10", nil }
		bootstrapTalosctlCombined = func(_ string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "etcd status") {
				return []byte("running"), nil
			}
			return nil, errors.New("unexpected call")
		}

		if err := bootstrapTalos(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("bootstrapTalos returned error: %v", err)
		}
	})

	t.Run("retries bootstrap until validation succeeds", func(t *testing.T) {
		oldGetRandomController := bootstrapGetRandomController
		oldTalosctlCombined := bootstrapTalosctlCombined
		oldValidateEtcd := bootstrapValidateEtcd
		oldSleep := bootstrapSleep
		t.Cleanup(func() {
			bootstrapGetRandomController = oldGetRandomController
			bootstrapTalosctlCombined = oldTalosctlCombined
			bootstrapValidateEtcd = oldValidateEtcd
			bootstrapSleep = oldSleep
		})

		bootstrapGetRandomController = func(string) (string, error) { return "10.0.0.10", nil }
		stage := 0
		var sleeps []time.Duration
		bootstrapTalosctlCombined = func(_ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "etcd status"):
				return nil, errors.New("not bootstrapped")
			case strings.Contains(joined, "bootstrap"):
				stage++
				return []byte("bootstrapped"), nil
			default:
				return nil, errors.New("unexpected call")
			}
		}
		bootstrapValidateEtcd = func(_ string, _ string, _ *common.ColorLogger) error {
			if stage < 2 {
				return errors.New("still starting")
			}
			return nil
		}
		bootstrapSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		if err := bootstrapTalos(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("bootstrapTalos returned error: %v", err)
		}
		if len(sleeps) != 1 || sleeps[0] != 10*time.Second {
			t.Fatalf("expected single 10s retry sleep, got %v", sleeps)
		}
	})
}

func TestGetRandomController(t *testing.T) {
	oldTalosctlOutput := bootstrapTalosctlOutput
	t.Cleanup(func() { bootstrapTalosctlOutput = oldTalosctlOutput })

	bootstrapTalosctlOutput = func(_ string, _ ...string) ([]byte, error) {
		return []byte(`{"endpoints":["10.0.0.10","10.0.0.11"]}`), nil
	}

	controller, err := getRandomController("/tmp/talosconfig")
	if err != nil {
		t.Fatalf("getRandomController returned error: %v", err)
	}
	if controller != "10.0.0.10" {
		t.Fatalf("unexpected controller: %s", controller)
	}
}

func TestFetchKubeconfigAndPatch(t *testing.T) {
	t.Run("fetches kubeconfig and patches endpoint", func(t *testing.T) {
		oldGetRandomController := bootstrapGetRandomController
		oldTalosctlCombined := bootstrapTalosctlCombined
		oldSaveKubeconfig := bootstrapSaveKubeconfig
		oldPatchKubeconfig := bootstrapPatchKubeconfig
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalNormal
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapKubeconfigMaxWait
		t.Cleanup(func() {
			bootstrapGetRandomController = oldGetRandomController
			bootstrapTalosctlCombined = oldTalosctlCombined
			bootstrapSaveKubeconfig = oldSaveKubeconfig
			bootstrapPatchKubeconfig = oldPatchKubeconfig
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalNormal = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapKubeconfigMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalNormal = time.Second
		bootstrapStallTimeout = 5 * time.Second
		bootstrapKubeconfigMaxWait = 20 * time.Second

		tmpDir := t.TempDir()
		kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
		bootstrapGetRandomController = func(string) (string, error) { return "10.0.0.10", nil }
		bootstrapTalosctlCombined = func(_ string, args ...string) ([]byte, error) {
			if !strings.Contains(strings.Join(args, " "), "kubeconfig --nodes 10.0.0.10") {
				t.Fatalf("unexpected talosctl args: %v", args)
			}
			if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://10.0.0.2:6443\n"), 0600); err != nil {
				t.Fatalf("failed to write kubeconfig: %v", err)
			}
			return []byte("ok"), nil
		}
		bootstrapSaveKubeconfig = func(content []byte, _ *common.ColorLogger) error {
			if !strings.Contains(string(content), "kind: Config") {
				t.Fatalf("unexpected kubeconfig content: %q", string(content))
			}
			return nil
		}
		bootstrapPatchKubeconfig = func(path, nodeIP string, _ *common.ColorLogger) error {
			if path != kubeconfigPath || nodeIP != "10.0.0.10" {
				t.Fatalf("unexpected patch args: %s %s", path, nodeIP)
			}
			return nil
		}

		if err := fetchKubeconfig(&BootstrapConfig{KubeConfig: kubeconfigPath}, common.NewColorLogger()); err != nil {
			t.Fatalf("fetchKubeconfig returned error: %v", err)
		}
	})

	t.Run("patches kubeconfig server", func(t *testing.T) {
		kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
		if err := os.WriteFile(kubeconfigPath, []byte(`apiVersion: v1
kind: Config
clusters:
- name: home
  cluster:
    server: https://192.168.0.15:6443
`), 0600); err != nil {
			t.Fatalf("failed to write kubeconfig: %v", err)
		}

		if err := patchKubeconfigForBootstrap(kubeconfigPath, "10.0.0.10", common.NewColorLogger()); err != nil {
			t.Fatalf("patchKubeconfigForBootstrap returned error: %v", err)
		}

		content, err := os.ReadFile(kubeconfigPath)
		if err != nil {
			t.Fatalf("failed reading kubeconfig: %v", err)
		}
		if !strings.Contains(string(content), "https://10.0.0.10:6443") {
			t.Fatalf("expected kubeconfig server to be patched, got %q", string(content))
		}
	})
}

func TestApplyCRDsFromHelmfile(t *testing.T) {
	oldGetBootstrapFile := bootstrapGetBootstrapFile
	oldHelmfileTemplateOutput := bootstrapHelmfileTemplateOutput
	oldCombinedIn := bootstrapKubectlCombinedIn
	oldWaitCRDs := bootstrapWaitCRDs
	t.Cleanup(func() {
		bootstrapGetBootstrapFile = oldGetBootstrapFile
		bootstrapHelmfileTemplateOutput = oldHelmfileTemplateOutput
		bootstrapKubectlCombinedIn = oldCombinedIn
		bootstrapWaitCRDs = oldWaitCRDs
	})

	bootstrapGetBootstrapFile = func(name string) (string, error) {
		if name != "helmfile.d/00-crds.yaml" {
			t.Fatalf("unexpected bootstrap file: %s", name)
		}
		return "releases: []\n", nil
	}
	bootstrapHelmfileTemplateOutput = func(_ string, _ *BootstrapConfig, helmfilePath string) ([]byte, error) {
		if _, err := os.Stat(helmfilePath); err != nil {
			t.Fatalf("expected helmfile path to exist: %v", err)
		}
		return []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ignored
`), nil
	}
	bootstrapKubectlCombinedIn = func(_ *BootstrapConfig, input io.Reader, args ...string) ([]byte, error) {
		body, err := io.ReadAll(input)
		if err != nil {
			t.Fatalf("failed reading CRD apply input: %v", err)
		}
		if strings.Contains(string(body), "ConfigMap") || !strings.Contains(string(body), "CustomResourceDefinition") {
			t.Fatalf("unexpected CRD apply manifest: %q", string(body))
		}
		return []byte("applied"), nil
	}
	bootstrapWaitCRDs = func(*BootstrapConfig, *common.ColorLogger) error { return nil }

	if err := applyCRDsFromHelmfile(&BootstrapConfig{RootDir: "/repo/home-ops"}, common.NewColorLogger()); err != nil {
		t.Fatalf("applyCRDsFromHelmfile returned error: %v", err)
	}
}

func TestExecuteHelmfileSync(t *testing.T) {
	oldGetBootstrapTemplate := bootstrapGetBootstrapTemplate
	oldGetBootstrapFile := bootstrapGetBootstrapFile
	oldRunHelmfileSyncCmd := bootstrapRunHelmfileSyncCmd
	t.Cleanup(func() {
		bootstrapGetBootstrapTemplate = oldGetBootstrapTemplate
		bootstrapGetBootstrapFile = oldGetBootstrapFile
		bootstrapRunHelmfileSyncCmd = oldRunHelmfileSyncCmd
	})

	bootstrapGetBootstrapTemplate = func(name string) (string, error) {
		if name != "values.yaml.gotmpl" {
			t.Fatalf("unexpected bootstrap template: %s", name)
		}
		return "clusterName: home-ops\n", nil
	}
	bootstrapGetBootstrapFile = func(name string) (string, error) {
		if name != "helmfile.d/01-apps.yaml" {
			t.Fatalf("unexpected bootstrap file: %s", name)
		}
		return "releases: []\n", nil
	}
	bootstrapRunHelmfileSyncCmd = func(tempDir, helmfilePath string, config *BootstrapConfig) error {
		if config.RootDir != "/repo/home-ops" {
			t.Fatalf("unexpected config root dir: %s", config.RootDir)
		}
		if _, err := os.Stat(filepath.Join(tempDir, "templates", "values.yaml.gotmpl")); err != nil {
			t.Fatalf("expected values template to exist: %v", err)
		}
		if _, err := os.Stat(helmfilePath); err != nil {
			t.Fatalf("expected helmfile path to exist: %v", err)
		}
		return nil
	}

	if err := executeHelmfileSync(&BootstrapConfig{RootDir: "/repo/home-ops"}, common.NewColorLogger()); err != nil {
		t.Fatalf("executeHelmfileSync returned error: %v", err)
	}
}

func TestFixExistingCRDMetadata(t *testing.T) {
	oldKubectlOutput := bootstrapKubectlOutput
	oldKubectlCombined := bootstrapKubectlCombined
	t.Cleanup(func() {
		bootstrapKubectlOutput = oldKubectlOutput
		bootstrapKubectlCombined = oldKubectlCombined
	})

	var calls []string
	bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "get crds -o json" {
			t.Fatalf("unexpected kubectl output args: %v", args)
		}
		return []byte(`{
  "items": [
    {
      "metadata": {
        "name": "clustersecretstores.external-secrets.io",
        "labels": {},
        "annotations": {}
      }
    },
    {
      "metadata": {
        "name": "certificates.cert-manager.io",
        "labels": {"app.kubernetes.io/managed-by": "Helm"},
        "annotations": {"meta.helm.sh/release-name": "cert-manager"}
      }
    }
  ]
}`), nil
	}
	bootstrapKubectlCombined = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte("ok"), nil
	}

	if err := fixExistingCRDMetadata(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
		t.Fatalf("fixExistingCRDMetadata returned error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected annotate and label calls, got %v", calls)
	}
}

func TestValidateKubeconfig(t *testing.T) {
	t.Run("succeeds when api server and nodes are reachable", func(t *testing.T) {
		oldKubectlRun := bootstrapKubectlRun
		t.Cleanup(func() { bootstrapKubectlRun = oldKubectlRun })

		var calls []string
		bootstrapKubectlRun = func(_ *BootstrapConfig, args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			return nil
		}

		err := validateKubeconfig(&BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("validateKubeconfig returned error: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 kubectl calls, got %d (%v)", len(calls), calls)
		}
	})

	t.Run("fails when connectivity stalls", func(t *testing.T) {
		oldKubectlRun := bootstrapKubectlRun
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalNormal
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapKubeconfigMaxWait
		t.Cleanup(func() {
			bootstrapKubectlRun = oldKubectlRun
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalNormal = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapKubeconfigMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalNormal = time.Second
		bootstrapStallTimeout = 3 * time.Second
		bootstrapKubeconfigMaxWait = 10 * time.Second
		bootstrapKubectlRun = func(_ *BootstrapConfig, _ ...string) error { return errors.New("connection refused") }

		err := validateKubeconfig(&BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "cluster connectivity stalled") {
			t.Fatalf("expected stall error, got %v", err)
		}
	})
}

func TestResolve1PasswordReferencesRetry(t *testing.T) {
	oldInjectSecrets := bootstrapInjectSecrets
	oldEnsureOPAuth := bootstrapEnsureOPAuth
	t.Cleanup(func() {
		bootstrapInjectSecrets = oldInjectSecrets
		bootstrapEnsureOPAuth = oldEnsureOPAuth
	})

	attempts := 0
	bootstrapInjectSecrets = func(content string) (string, error) {
		attempts++
		if attempts == 1 {
			return "", errors.New("not authenticated")
		}
		return strings.ReplaceAll(content, "op://vault/item/field", "secret-value"), nil
	}
	bootstrapEnsureOPAuth = func() error { return nil }

	resolved, err := resolve1PasswordReferences("token: op://vault/item/field", common.NewColorLogger())
	if err != nil {
		t.Fatalf("resolve1PasswordReferences returned error: %v", err)
	}
	if !strings.Contains(resolved, "secret-value") {
		t.Fatalf("unexpected resolved output: %q", resolved)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 inject attempts, got %d", attempts)
	}
}

func TestApplyNamespaces(t *testing.T) {
	t.Run("creates all bootstrap namespaces", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldCombinedIn := bootstrapKubectlCombinedIn
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapKubectlCombinedIn = oldCombinedIn
		})

		var generated []string
		var applied []string
		bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			if len(args) < 3 {
				t.Fatalf("unexpected kubectl output args: %v", args)
			}
			ns := args[2]
			generated = append(generated, ns)
			return []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + ns + "\n"), nil
		}
		bootstrapKubectlCombinedIn = func(_ *BootstrapConfig, input io.Reader, args ...string) ([]byte, error) {
			body, err := io.ReadAll(input)
			if err != nil {
				t.Fatalf("failed to read apply input: %v", err)
			}
			applied = append(applied, string(body))
			if strings.Join(args, " ") != "apply --filename -" {
				t.Fatalf("unexpected apply args: %v", args)
			}
			return []byte("namespace applied"), nil
		}

		err := applyNamespaces(&BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("applyNamespaces returned error: %v", err)
		}
		if len(generated) != 18 {
			t.Fatalf("expected 18 generated namespaces, got %d", len(generated))
		}
		if len(applied) != len(generated) {
			t.Fatalf("expected %d applied manifests, got %d", len(generated), len(applied))
		}
		if !strings.Contains(applied[0], "kind: Namespace") {
			t.Fatalf("expected namespace manifest, got %q", applied[0])
		}
	})

	t.Run("ignores already exists responses", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldCombinedIn := bootstrapKubectlCombinedIn
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapKubectlCombinedIn = oldCombinedIn
		})

		bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			return []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + args[2] + "\n"), nil
		}
		bootstrapKubectlCombinedIn = func(_ *BootstrapConfig, _ io.Reader, _ ...string) ([]byte, error) {
			return []byte("AlreadyExists"), errors.New("conflict")
		}

		if err := applyNamespaces(&BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, common.NewColorLogger()); err != nil {
			t.Fatalf("applyNamespaces should ignore already-existing namespaces, got %v", err)
		}
	})
}

func TestWaitHelpersSuccessPaths(t *testing.T) {
	t.Run("waitForNodesAvailable succeeds when nodes appear", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalSlow
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapNodeMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalSlow = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapNodeMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalSlow = time.Second
		bootstrapStallTimeout = 5 * time.Second
		bootstrapNodeMaxWait = 20 * time.Second

		calls := 0
		bootstrapKubectlOutput = func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(""), nil
			}
			return []byte("node1 node2"), nil
		}

		if err := waitForNodesAvailable(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForNodesAvailable returned error: %v", err)
		}
	})

	t.Run("waitForNodesReadyFalse succeeds when all nodes are False", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalSlow
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapNodeMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalSlow = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapNodeMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalSlow = time.Second
		bootstrapStallTimeout = 5 * time.Second
		bootstrapNodeMaxWait = 20 * time.Second

		bootstrapKubectlOutput = func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
			return []byte("node1:False\nnode2:False"), nil
		}

		if err := waitForNodesReadyFalse(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForNodesReadyFalse returned error: %v", err)
		}
	})

	t.Run("waitForExternalSecretsWebhook succeeds with endpoints", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalNormal
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapExtSecMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalNormal = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapExtSecMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalNormal = time.Second
		bootstrapStallTimeout = 5 * time.Second
		bootstrapExtSecMaxWait = 20 * time.Second

		bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "get deployment external-secrets-webhook"):
				return []byte("1/1:True"), nil
			case strings.Contains(joined, "get endpoints external-secrets-webhook"):
				return []byte("10.0.0.25"), nil
			default:
				return nil, errors.New("unexpected command")
			}
		}

		if err := waitForExternalSecretsWebhook(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForExternalSecretsWebhook returned error: %v", err)
		}
	})

	t.Run("waitForFluxController succeeds when deployment reaches ready", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalNormal
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapFluxMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalNormal = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapFluxMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalNormal = time.Second
		bootstrapStallTimeout = 5 * time.Second
		bootstrapFluxMaxWait = 20 * time.Second

		calls := 0
		bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			calls++
			if !strings.Contains(strings.Join(args, " "), "source-controller") {
				t.Fatalf("unexpected controller args: %v", args)
			}
			if calls == 1 {
				return []byte("0/1"), nil
			}
			return []byte("1/1"), nil
		}

		if err := waitForFluxController(&BootstrapConfig{}, common.NewColorLogger(), "source-controller"); err != nil {
			t.Fatalf("waitForFluxController returned error: %v", err)
		}
	})
}

func TestSaveRenderedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rendered.yaml")
	if err := saveRenderedConfig("kind: Config\n", path, common.NewColorLogger()); err != nil {
		t.Fatalf("saveRenderedConfig returned error: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed reading saved config: %v", err)
	}
	if string(content) != "kind: Config\n" {
		t.Fatalf("unexpected saved content: %q", string(content))
	}
}

func TestApplyClusterSecretStore(t *testing.T) {
	validSecretStore := `apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: onepassword
spec:
  provider:
    onepassword:
      auth:
        secretRef:
          connectTokenSecretRef:
            name: onepassword-connect-token
            key: token
`

	t.Run("validates in dry-run mode", func(t *testing.T) {
		oldGetBootstrapFile := bootstrapGetBootstrapFile
		oldResolveSecrets := bootstrapResolveSecrets
		t.Cleanup(func() {
			bootstrapGetBootstrapFile = oldGetBootstrapFile
			bootstrapResolveSecrets = oldResolveSecrets
		})

		bootstrapGetBootstrapFile = func(name string) (string, error) {
			if name != "clustersecretstore.yaml" {
				t.Fatalf("unexpected file request: %s", name)
			}
			return validSecretStore, nil
		}
		bootstrapResolveSecrets = func(content string, _ *common.ColorLogger) (string, error) {
			return content, nil
		}

		err := applyClusterSecretStore(&BootstrapConfig{DryRun: true}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("applyClusterSecretStore dry-run returned error: %v", err)
		}
	})

	t.Run("applies rendered manifest to external-secrets namespace", func(t *testing.T) {
		oldGetBootstrapFile := bootstrapGetBootstrapFile
		oldResolveSecrets := bootstrapResolveSecrets
		oldCombinedIn := bootstrapKubectlCombinedIn
		t.Cleanup(func() {
			bootstrapGetBootstrapFile = oldGetBootstrapFile
			bootstrapResolveSecrets = oldResolveSecrets
			bootstrapKubectlCombinedIn = oldCombinedIn
		})

		bootstrapGetBootstrapFile = func(string) (string, error) { return validSecretStore, nil }
		bootstrapResolveSecrets = func(content string, _ *common.ColorLogger) (string, error) {
			return strings.ReplaceAll(content, "onepassword-connect-token", "resolved-token"), nil
		}
		bootstrapKubectlCombinedIn = func(_ *BootstrapConfig, input io.Reader, args ...string) ([]byte, error) {
			body, err := io.ReadAll(input)
			if err != nil {
				t.Fatalf("failed to read cluster secret store input: %v", err)
			}
			if !strings.Contains(string(body), "resolved-token") {
				t.Fatalf("expected resolved content, got %q", string(body))
			}

			got := strings.Join(args, " ")
			want := "apply --namespace " + constants.NSExternalSecret + " --server-side --force-conflicts --filename - --wait=true"
			if got != want {
				t.Fatalf("unexpected kubectl args: got %q want %q", got, want)
			}
			return []byte("applied"), nil
		}

		err := applyClusterSecretStore(&BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("applyClusterSecretStore returned error: %v", err)
		}
	})
}

func TestValidateClusterSecretStoreTemplate(t *testing.T) {
	oldGetBootstrapFile := bootstrapGetBootstrapFile
	t.Cleanup(func() { bootstrapGetBootstrapFile = oldGetBootstrapFile })

	bootstrapGetBootstrapFile = func(string) (string, error) {
		return `apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: onepassword
spec:
  provider:
    onepassword:
      connectHost: op://vault/item/host
`, nil
	}

	if err := validateClusterSecretStoreTemplate(common.NewColorLogger()); err != nil {
		t.Fatalf("validateClusterSecretStoreTemplate returned error: %v", err)
	}
}

func TestApplyResources(t *testing.T) {
	resourcesYAML := `apiVersion: v1
kind: Secret
metadata:
  name: onepassword-secret
---
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-tunnel-id-secret
`

	t.Run("validates in dry-run mode", func(t *testing.T) {
		oldGetBootstrapFile := bootstrapGetBootstrapFile
		oldResolveSecrets := bootstrapResolveSecrets
		t.Cleanup(func() {
			bootstrapGetBootstrapFile = oldGetBootstrapFile
			bootstrapResolveSecrets = oldResolveSecrets
		})

		bootstrapGetBootstrapFile = func(string) (string, error) { return resourcesYAML, nil }
		bootstrapResolveSecrets = func(content string, _ *common.ColorLogger) (string, error) { return content, nil }

		if err := applyResources(&BootstrapConfig{DryRun: true}, common.NewColorLogger()); err != nil {
			t.Fatalf("applyResources dry-run returned error: %v", err)
		}
	})

	t.Run("applies resolved resources", func(t *testing.T) {
		oldGetBootstrapFile := bootstrapGetBootstrapFile
		oldResolveSecrets := bootstrapResolveSecrets
		oldCombinedIn := bootstrapKubectlCombinedIn
		t.Cleanup(func() {
			bootstrapGetBootstrapFile = oldGetBootstrapFile
			bootstrapResolveSecrets = oldResolveSecrets
			bootstrapKubectlCombinedIn = oldCombinedIn
		})

		bootstrapGetBootstrapFile = func(string) (string, error) { return resourcesYAML, nil }
		bootstrapResolveSecrets = func(content string, _ *common.ColorLogger) (string, error) {
			return strings.ReplaceAll(content, "cloudflare-tunnel-id-secret", "resolved-cloudflare-secret"), nil
		}
		bootstrapKubectlCombinedIn = func(_ *BootstrapConfig, input io.Reader, args ...string) ([]byte, error) {
			body, err := io.ReadAll(input)
			if err != nil {
				t.Fatalf("failed to read resources input: %v", err)
			}
			if !strings.Contains(string(body), "resolved-cloudflare-secret") {
				t.Fatalf("expected resolved resources, got %q", string(body))
			}
			return []byte("applied"), nil
		}

		if err := applyResources(&BootstrapConfig{KubeConfig: "/tmp/kubeconfig"}, common.NewColorLogger()); err != nil {
			t.Fatalf("applyResources returned error: %v", err)
		}
	})
}

func TestApplyCRDs(t *testing.T) {
	t.Run("runs gateway and helmfile stages", func(t *testing.T) {
		oldApplyGateway := bootstrapApplyGatewayCRDs
		oldApplyHelmfile := bootstrapApplyCRDsHelmfile
		t.Cleanup(func() {
			bootstrapApplyGatewayCRDs = oldApplyGateway
			bootstrapApplyCRDsHelmfile = oldApplyHelmfile
		})

		var order []string
		bootstrapApplyGatewayCRDs = func(*BootstrapConfig, *common.ColorLogger) error {
			order = append(order, "gateway")
			return nil
		}
		bootstrapApplyCRDsHelmfile = func(*BootstrapConfig, *common.ColorLogger) error {
			order = append(order, "helmfile")
			return nil
		}

		if err := applyCRDs(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("applyCRDs returned error: %v", err)
		}
		if strings.Join(order, ",") != "gateway,helmfile" {
			t.Fatalf("unexpected CRD stage order: %v", order)
		}
	})

	t.Run("returns gateway stage errors", func(t *testing.T) {
		oldApplyGateway := bootstrapApplyGatewayCRDs
		t.Cleanup(func() { bootstrapApplyGatewayCRDs = oldApplyGateway })

		bootstrapApplyGatewayCRDs = func(*BootstrapConfig, *common.ColorLogger) error {
			return errors.New("bad gateway manifests")
		}

		err := applyCRDs(&BootstrapConfig{}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "failed to apply Gateway API CRDs") {
			t.Fatalf("expected gateway CRD failure, got %v", err)
		}
	})
}

func TestSeparateCRDsFromManifests(t *testing.T) {
	crds, others, err := separateCRDsFromManifests(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
`)
	if err != nil {
		t.Fatalf("separateCRDsFromManifests returned error: %v", err)
	}
	if len(crds) != 1 || len(others) != 1 {
		t.Fatalf("unexpected manifest split: crds=%d others=%d", len(crds), len(others))
	}
}

func TestWaitForCRDsEstablished(t *testing.T) {
	t.Run("succeeds when all CRDs become established", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalFast
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapCRDMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalFast = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapCRDMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalFast = time.Second
		bootstrapStallTimeout = 5 * time.Second
		bootstrapCRDMaxWait = 20 * time.Second

		calls := 0
		bootstrapKubectlOutput = func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("widgets.example.com:False"), nil
			}
			return []byte("widgets.example.com:True"), nil
		}

		if err := waitForCRDsEstablished(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForCRDsEstablished returned error: %v", err)
		}
	})

	t.Run("fails when establishment stalls", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalFast
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapCRDMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalFast = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapCRDMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalFast = time.Second
		bootstrapStallTimeout = 3 * time.Second
		bootstrapCRDMaxWait = 10 * time.Second
		bootstrapKubectlOutput = func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
			return []byte("widgets.example.com:False"), nil
		}

		err := waitForCRDsEstablished(&BootstrapConfig{}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "CRD establishment stalled") {
			t.Fatalf("expected CRD stall error, got %v", err)
		}
	})
}

func TestIsExternalSecretsInstalled(t *testing.T) {
	oldKubectlRun := bootstrapKubectlRun
	oldSleep := bootstrapSleep
	oldCheckInterval := bootstrapCheckIntervalNormal
	t.Cleanup(func() {
		bootstrapKubectlRun = oldKubectlRun
		bootstrapSleep = oldSleep
		bootstrapCheckIntervalNormal = oldCheckInterval
	})

	attempts := 0
	bootstrapCheckIntervalNormal = time.Millisecond
	bootstrapSleep = func(time.Duration) {}
	bootstrapKubectlRun = func(_ *BootstrapConfig, args ...string) error {
		attempts++
		if !strings.Contains(strings.Join(args, " "), "external-secrets-webhook") {
			t.Fatalf("unexpected kubectl args: %v", args)
		}
		if attempts < 2 {
			return errors.New("not found")
		}
		return nil
	}

	if !isExternalSecretsInstalled(&BootstrapConfig{}, common.NewColorLogger()) {
		t.Fatal("expected external-secrets deployment to be detected")
	}
}

func TestDynamicValuesTemplate(t *testing.T) {
	oldRenderHelmValues := bootstrapRenderHelmValues
	t.Cleanup(func() { bootstrapRenderHelmValues = oldRenderHelmValues })

	var releases []string
	bootstrapRenderHelmValues = func(release, rootDir string, _ *metrics.PerformanceCollector) (string, error) {
		releases = append(releases, release)
		if rootDir != "/repo/home-ops" {
			t.Fatalf("unexpected root dir: %s", rootDir)
		}
		return "key: value\n", nil
	}

	if err := testDynamicValuesTemplate(&BootstrapConfig{RootDir: "/repo/home-ops"}, common.NewColorLogger()); err != nil {
		t.Fatalf("testDynamicValuesTemplate returned error: %v", err)
	}
	if len(releases) != 7 {
		t.Fatalf("expected 7 releases to be rendered, got %d", len(releases))
	}
}

func TestWaitForFluxReconciliation(t *testing.T) {
	t.Run("returns after controllers and cluster reconcile", func(t *testing.T) {
		oldWaitController := bootstrapWaitFluxController
		oldWaitGitRepo := bootstrapWaitGitRepository
		oldWaitFluxKS := bootstrapWaitFluxKS
		t.Cleanup(func() {
			bootstrapWaitFluxController = oldWaitController
			bootstrapWaitGitRepository = oldWaitGitRepo
			bootstrapWaitFluxKS = oldWaitFluxKS
		})

		var controllers []string
		bootstrapWaitFluxController = func(_ *BootstrapConfig, _ *common.ColorLogger, controller string) error {
			controllers = append(controllers, controller)
			return nil
		}
		bootstrapWaitGitRepository = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapWaitFluxKS = func(_ *BootstrapConfig, _ *common.ColorLogger, ks string) error {
			if ks != "cluster" {
				t.Fatalf("unexpected Flux kustomization: %s", ks)
			}
			return nil
		}

		if err := waitForFluxReconciliation(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForFluxReconciliation returned error: %v", err)
		}
		if strings.Join(controllers, ",") != "source-controller,kustomize-controller,helm-controller" {
			t.Fatalf("unexpected controller wait order: %v", controllers)
		}
	})

	t.Run("treats git repository lag as warning path", func(t *testing.T) {
		oldWaitController := bootstrapWaitFluxController
		oldWaitGitRepo := bootstrapWaitGitRepository
		t.Cleanup(func() {
			bootstrapWaitFluxController = oldWaitController
			bootstrapWaitGitRepository = oldWaitGitRepo
		})

		bootstrapWaitFluxController = func(*BootstrapConfig, *common.ColorLogger, string) error { return nil }
		bootstrapWaitGitRepository = func(*BootstrapConfig, *common.ColorLogger) error {
			return errors.New("still fetching")
		}

		if err := waitForFluxReconciliation(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("git repository lag should be non-fatal, got %v", err)
		}
	})
}

func TestWaitForNodes(t *testing.T) {
	t.Run("returns early when nodes are already ready", func(t *testing.T) {
		oldWaitAvailable := bootstrapWaitNodesAvailable
		oldCheckReady := bootstrapCheckNodesReady
		oldWaitReadyFalse := bootstrapWaitNodesReadyFalse
		t.Cleanup(func() {
			bootstrapWaitNodesAvailable = oldWaitAvailable
			bootstrapCheckNodesReady = oldCheckReady
			bootstrapWaitNodesReadyFalse = oldWaitReadyFalse
		})

		bootstrapWaitNodesAvailable = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapCheckNodesReady = func(*BootstrapConfig, *common.ColorLogger) (bool, error) { return true, nil }
		bootstrapWaitNodesReadyFalse = func(*BootstrapConfig, *common.ColorLogger) error {
			t.Fatal("waitForNodesReadyFalse should not run when nodes are already ready")
			return nil
		}

		if err := waitForNodes(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForNodes returned error: %v", err)
		}
	})

	t.Run("waits for Ready=False when cluster is not fully ready", func(t *testing.T) {
		oldWaitAvailable := bootstrapWaitNodesAvailable
		oldCheckReady := bootstrapCheckNodesReady
		oldWaitReadyFalse := bootstrapWaitNodesReadyFalse
		t.Cleanup(func() {
			bootstrapWaitNodesAvailable = oldWaitAvailable
			bootstrapCheckNodesReady = oldCheckReady
			bootstrapWaitNodesReadyFalse = oldWaitReadyFalse
		})

		bootstrapWaitNodesAvailable = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapCheckNodesReady = func(*BootstrapConfig, *common.ColorLogger) (bool, error) { return false, nil }
		bootstrapWaitNodesReadyFalse = func(*BootstrapConfig, *common.ColorLogger) error { return nil }

		if err := waitForNodes(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForNodes returned error: %v", err)
		}
	})
}

func TestCheckIfNodesReady(t *testing.T) {
	oldOutput := bootstrapKubectlOutput
	t.Cleanup(func() { bootstrapKubectlOutput = oldOutput })

	bootstrapKubectlOutput = func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
		return []byte("node1:True\nnode2:True\n"), nil
	}

	ready, err := checkIfNodesReady(&BootstrapConfig{}, common.NewColorLogger())
	if err != nil {
		t.Fatalf("checkIfNodesReady returned error: %v", err)
	}
	if !ready {
		t.Fatal("expected all nodes to be ready")
	}
}

func TestAPIServerConnectivity(t *testing.T) {
	t.Run("passes through success", func(t *testing.T) {
		oldCombined := bootstrapKubectlCombined
		t.Cleanup(func() { bootstrapKubectlCombined = oldCombined })
		bootstrapKubectlCombined = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			if strings.Join(args, " ") != "cluster-info --request-timeout=10s" {
				t.Fatalf("unexpected kubectl args: %v", args)
			}
			return []byte("ok"), nil
		}

		if err := testAPIServerConnectivity(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("testAPIServerConnectivity returned error: %v", err)
		}
	})

	t.Run("wraps cluster-info failures", func(t *testing.T) {
		oldCombined := bootstrapKubectlCombined
		t.Cleanup(func() { bootstrapKubectlCombined = oldCombined })
		bootstrapKubectlCombined = func(_ *BootstrapConfig, _ ...string) ([]byte, error) {
			return []byte("connection refused"), errors.New("boom")
		}

		err := testAPIServerConnectivity(&BootstrapConfig{}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "cluster-info failed") {
			t.Fatalf("expected cluster-info failure, got %v", err)
		}
	})
}

func TestWaitForGitRepositoryReady(t *testing.T) {
	t.Run("succeeds when repository reports ready", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalNormal
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapFluxMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalNormal = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapFluxMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalNormal = time.Second
		bootstrapStallTimeout = 4 * time.Second
		bootstrapFluxMaxWait = 20 * time.Second

		bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "gitrepository flux-system") {
				return []byte("True:Succeeded:main/abc123"), nil
			}
			return []byte(""), nil
		}

		if err := waitForGitRepositoryReady(&BootstrapConfig{}, common.NewColorLogger()); err != nil {
			t.Fatalf("waitForGitRepositoryReady returned error: %v", err)
		}
	})
}

func TestWaitForFluxKustomizationReady(t *testing.T) {
	t.Run("succeeds when kustomization reports ready", func(t *testing.T) {
		oldOutput := bootstrapKubectlOutput
		oldNow := bootstrapNow
		oldSleep := bootstrapSleep
		oldCheckInterval := bootstrapCheckIntervalNormal
		oldStallTimeout := bootstrapStallTimeout
		oldMaxWait := bootstrapFluxMaxWait
		t.Cleanup(func() {
			bootstrapKubectlOutput = oldOutput
			bootstrapNow = oldNow
			bootstrapSleep = oldSleep
			bootstrapCheckIntervalNormal = oldCheckInterval
			bootstrapStallTimeout = oldStallTimeout
			bootstrapFluxMaxWait = oldMaxWait
		})

		current := time.Unix(0, 0)
		bootstrapNow = func() time.Time { return current }
		bootstrapSleep = func(d time.Duration) { current = current.Add(d) }
		bootstrapCheckIntervalNormal = time.Second
		bootstrapStallTimeout = 4 * time.Second
		bootstrapFluxMaxWait = 20 * time.Second

		bootstrapKubectlOutput = func(_ *BootstrapConfig, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "kustomization cluster") {
				return []byte("True:ReconciliationSucceeded:main/abc123"), nil
			}
			return []byte(""), nil
		}

		if err := waitForFluxKustomizationReady(&BootstrapConfig{}, common.NewColorLogger(), "cluster"); err != nil {
			t.Fatalf("waitForFluxKustomizationReady returned error: %v", err)
		}
	})
}

func TestApplyCRDsFromHelmfileDryRun(t *testing.T) {
	if err := applyCRDsFromHelmfile(&BootstrapConfig{DryRun: true}, common.NewColorLogger()); err != nil {
		t.Fatalf("applyCRDsFromHelmfile dry-run returned error: %v", err)
	}
}

func TestSyncHelmReleases(t *testing.T) {
	t.Run("dry-run validates dependent templates", func(t *testing.T) {
		oldApplySecretStore := bootstrapApplySecretStore
		oldValidateSecretStore := bootstrapValidateSecretStore
		oldTestDynamicValues := bootstrapTestDynamicValues
		t.Cleanup(func() {
			bootstrapApplySecretStore = oldApplySecretStore
			bootstrapValidateSecretStore = oldValidateSecretStore
			bootstrapTestDynamicValues = oldTestDynamicValues
		})

		var order []string
		bootstrapApplySecretStore = func(*BootstrapConfig, *common.ColorLogger) error {
			order = append(order, "apply")
			return nil
		}
		bootstrapValidateSecretStore = func(*common.ColorLogger) error {
			order = append(order, "validate")
			return nil
		}
		bootstrapTestDynamicValues = func(*BootstrapConfig, *common.ColorLogger) error {
			order = append(order, "dynamic-values")
			return nil
		}

		err := syncHelmReleases(&BootstrapConfig{DryRun: true}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("syncHelmReleases dry-run returned error: %v", err)
		}

		got := strings.Join(order, ",")
		if got != "apply,validate,dynamic-values" {
			t.Fatalf("unexpected dry-run order: %s", got)
		}
	})

	t.Run("retries retryable helm errors", func(t *testing.T) {
		oldFixCRDMetadata := bootstrapFixCRDMetadata
		oldExecuteHelmfileSync := bootstrapExecuteHelmfileSync
		oldExternalSecretsUp := bootstrapExternalSecretsUp
		oldTestAPIConnectivity := bootstrapTestAPIConnectivity
		oldSleep := bootstrapSleep
		t.Cleanup(func() {
			bootstrapFixCRDMetadata = oldFixCRDMetadata
			bootstrapExecuteHelmfileSync = oldExecuteHelmfileSync
			bootstrapExternalSecretsUp = oldExternalSecretsUp
			bootstrapTestAPIConnectivity = oldTestAPIConnectivity
			bootstrapSleep = oldSleep
		})

		attempts := 0
		apiChecks := 0
		var sleeps []time.Duration
		bootstrapFixCRDMetadata = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapExecuteHelmfileSync = func(*BootstrapConfig, *common.ColorLogger) error {
			attempts++
			if attempts == 1 {
				return errors.New("timeout talking to Kubernetes API")
			}
			return nil
		}
		bootstrapExternalSecretsUp = func(*BootstrapConfig, *common.ColorLogger) bool { return false }
		bootstrapTestAPIConnectivity = func(*BootstrapConfig, *common.ColorLogger) error {
			apiChecks++
			return nil
		}
		bootstrapSleep = func(d time.Duration) { sleeps = append(sleeps, d) }

		err := syncHelmReleases(&BootstrapConfig{}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("syncHelmReleases returned error: %v", err)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 helmfile attempts, got %d", attempts)
		}
		if apiChecks != 1 {
			t.Fatalf("expected 1 API recheck, got %d", apiChecks)
		}
		if len(sleeps) != 1 || sleeps[0] != 30*time.Second {
			t.Fatalf("unexpected retry sleeps: %v", sleeps)
		}
	})

	t.Run("returns immediately on non-retryable helm error", func(t *testing.T) {
		oldFixCRDMetadata := bootstrapFixCRDMetadata
		oldExecuteHelmfileSync := bootstrapExecuteHelmfileSync
		t.Cleanup(func() {
			bootstrapFixCRDMetadata = oldFixCRDMetadata
			bootstrapExecuteHelmfileSync = oldExecuteHelmfileSync
		})

		bootstrapFixCRDMetadata = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapExecuteHelmfileSync = func(*BootstrapConfig, *common.ColorLogger) error {
			return errors.New("rendered chart references unknown field")
		}

		err := syncHelmReleases(&BootstrapConfig{}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "helmfile sync failed") {
			t.Fatalf("expected non-retryable helmfile failure, got %v", err)
		}
	})

	t.Run("applies cluster secret store after external-secrets becomes ready", func(t *testing.T) {
		oldFixCRDMetadata := bootstrapFixCRDMetadata
		oldExecuteHelmfileSync := bootstrapExecuteHelmfileSync
		oldExternalSecretsUp := bootstrapExternalSecretsUp
		oldWaitExternalSecrets := bootstrapWaitExternalSecrets
		oldApplySecretStore := bootstrapApplySecretStore
		t.Cleanup(func() {
			bootstrapFixCRDMetadata = oldFixCRDMetadata
			bootstrapExecuteHelmfileSync = oldExecuteHelmfileSync
			bootstrapExternalSecretsUp = oldExternalSecretsUp
			bootstrapWaitExternalSecrets = oldWaitExternalSecrets
			bootstrapApplySecretStore = oldApplySecretStore
		})

		applied := 0
		bootstrapFixCRDMetadata = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapExecuteHelmfileSync = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapExternalSecretsUp = func(*BootstrapConfig, *common.ColorLogger) bool { return true }
		bootstrapWaitExternalSecrets = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
		bootstrapApplySecretStore = func(*BootstrapConfig, *common.ColorLogger) error {
			applied++
			return nil
		}

		err := syncHelmReleases(&BootstrapConfig{}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("syncHelmReleases returned error: %v", err)
		}
		if applied != 1 {
			t.Fatalf("expected cluster secret store to be applied once, got %d", applied)
		}
	})
}

func TestRunBootstrapLoadsVersionsFromSeam(t *testing.T) {
	oldRunWithSpinner := bootstrapRunWithSpinner
	oldResetTerminal := bootstrapResetTerminal
	oldSleep := bootstrapSleep
	oldRunPreflightChecks := bootstrapRunPreflightChecks
	oldApplyTalosConfig := bootstrapApplyTalosConfig
	oldBootstrapTalos := bootstrapBootstrapTalos
	oldFetchKubeconfig := bootstrapFetchKubeconfig
	oldValidateKubeconfig := bootstrapValidateKubeconfig
	oldWaitForNodes := bootstrapWaitForNodes
	oldApplyNamespaces := bootstrapApplyNamespaces
	oldApplyResources := bootstrapApplyResources
	oldApplyCRDs := bootstrapApplyCRDs
	oldSyncHelmReleases := bootstrapSyncHelmReleases
	oldWaitForFlux := bootstrapWaitForFlux
	oldGetVersions := bootstrapGetVersions

	t.Cleanup(func() {
		bootstrapRunWithSpinner = oldRunWithSpinner
		bootstrapResetTerminal = oldResetTerminal
		bootstrapSleep = oldSleep
		bootstrapRunPreflightChecks = oldRunPreflightChecks
		bootstrapApplyTalosConfig = oldApplyTalosConfig
		bootstrapBootstrapTalos = oldBootstrapTalos
		bootstrapFetchKubeconfig = oldFetchKubeconfig
		bootstrapValidateKubeconfig = oldValidateKubeconfig
		bootstrapWaitForNodes = oldWaitForNodes
		bootstrapApplyNamespaces = oldApplyNamespaces
		bootstrapApplyResources = oldApplyResources
		bootstrapApplyCRDs = oldApplyCRDs
		bootstrapSyncHelmReleases = oldSyncHelmReleases
		bootstrapWaitForFlux = oldWaitForFlux
		bootstrapGetVersions = oldGetVersions
	})

	bootstrapRunWithSpinner = func(_ string, _ bool, _ interface {
		Info(string, ...interface{})
		SetQuiet(bool)
	}, fn func() error) error {
		return fn()
	}
	bootstrapResetTerminal = func() {}
	bootstrapSleep = func(time.Duration) {}
	bootstrapRunPreflightChecks = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapApplyTalosConfig = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapBootstrapTalos = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapFetchKubeconfig = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapValidateKubeconfig = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapWaitForNodes = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapApplyNamespaces = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapApplyResources = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapApplyCRDs = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapSyncHelmReleases = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapWaitForFlux = func(*BootstrapConfig, *common.ColorLogger) error { return nil }
	bootstrapGetVersions = func(rootDir string) *versionconfig.VersionConfig {
		if rootDir != "/repo/home-ops" {
			t.Fatalf("unexpected root dir for versions: %s", rootDir)
		}
		return &versionconfig.VersionConfig{
			KubernetesVersion: "v1.34.1",
			TalosVersion:      "v1.11.1",
		}
	}

	config := &BootstrapConfig{
		RootDir:      "/repo/home-ops",
		KubeConfig:   "/tmp/kubeconfig",
		TalosConfig:  "/tmp/talosconfig",
		K8sVersion:   "",
		TalosVersion: "",
		Verbose:      true,
	}

	if err := runBootstrap(config); err != nil {
		t.Fatalf("runBootstrap returned error: %v", err)
	}
	if config.K8sVersion != "v1.34.1" || config.TalosVersion != "v1.11.1" {
		t.Fatalf("expected versions to be loaded from seam, got %+v", config)
	}
}
