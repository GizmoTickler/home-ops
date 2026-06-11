package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/flatcar"
)

// fakeOrchestrator records init/join/fetch calls for the Flatcar bootstrap flow.
type fakeOrchestrator struct {
	initCalledWith  string // node0 IP passed to InitFirstControlPlane
	initConfig      string
	result          *flatcar.KubeadmResult
	initErr         error
	joinCalls       []string // node IPs joined, in order
	joinConfigs     []string // join configs, in order
	joinErr         error
	fetchCalledWith string
	fetchKubeconfig string
	fetchErr        error
}

func (f *fakeOrchestrator) InitFirstControlPlane(node0IP, initConfig string, _ []string) (*flatcar.KubeadmResult, error) {
	f.initCalledWith = node0IP
	f.initConfig = initConfig
	if f.initErr != nil {
		return nil, f.initErr
	}
	return f.result, nil
}

func (f *fakeOrchestrator) JoinControlPlane(nodeIP, joinConfig string) error {
	f.joinCalls = append(f.joinCalls, nodeIP)
	f.joinConfigs = append(f.joinConfigs, joinConfig)
	return f.joinErr
}

func (f *fakeOrchestrator) FetchAdminKubeconfig(node0IP string) (string, error) {
	f.fetchCalledWith = node0IP
	if f.fetchErr != nil {
		return "", f.fetchErr
	}
	return f.fetchKubeconfig, nil
}

// installFlatcarFlowFakes wires all the swappable seams to no-op/recording fakes
// and returns a pointer to an ordered step log plus the fake orchestrator.
func installFlatcarFlowFakes(t *testing.T, result *flatcar.KubeadmResult) (*[]string, *fakeOrchestrator) {
	t.Helper()

	steps := &[]string{}
	orch := &fakeOrchestrator{
		result:          result,
		fetchKubeconfig: "apiVersion: v1\nkind: Config\n",
	}

	// Save originals.
	oldRunWithSpinner := bootstrapRunWithSpinner
	oldGetVersions := flatcarGetVersions
	oldGetSSHUser := flatcarGetSSHUser
	oldNewOrch := flatcarNewOrchestrator
	oldPreflight := flatcarPreflight
	oldRenderInit := flatcarRenderKubeadmInit
	oldRenderJoin := flatcarRenderKubeadmJoin
	oldInstallCilium := flatcarInstallCilium
	oldSaveKubeconfig := bootstrapSaveKubeconfig
	oldPatchKubeconfig := bootstrapPatchKubeconfig
	oldValidateKubeconfig := bootstrapValidateKubeconfig
	oldWaitForNodes := bootstrapWaitForNodes
	oldApplyNamespaces := bootstrapApplyNamespaces
	oldApplyResources := bootstrapApplyResources
	oldApplyCRDs := bootstrapApplyCRDs
	oldSyncHelm := bootstrapSyncHelmReleases
	oldWaitFlux := bootstrapWaitForFlux

	t.Cleanup(func() {
		bootstrapRunWithSpinner = oldRunWithSpinner
		flatcarGetVersions = oldGetVersions
		flatcarGetSSHUser = oldGetSSHUser
		flatcarNewOrchestrator = oldNewOrch
		flatcarPreflight = oldPreflight
		flatcarRenderKubeadmInit = oldRenderInit
		flatcarRenderKubeadmJoin = oldRenderJoin
		flatcarInstallCilium = oldInstallCilium
		bootstrapSaveKubeconfig = oldSaveKubeconfig
		bootstrapPatchKubeconfig = oldPatchKubeconfig
		bootstrapValidateKubeconfig = oldValidateKubeconfig
		bootstrapWaitForNodes = oldWaitForNodes
		bootstrapApplyNamespaces = oldApplyNamespaces
		bootstrapApplyResources = oldApplyResources
		bootstrapApplyCRDs = oldApplyCRDs
		bootstrapSyncHelmReleases = oldSyncHelm
		bootstrapWaitForFlux = oldWaitFlux
	})

	// Spinner just runs the function (so step ordering is preserved).
	bootstrapRunWithSpinner = func(_ string, _ bool, _ interface {
		Info(string, ...interface{})
		SetQuiet(bool)
	}, fn func() error) error {
		return fn()
	}

	flatcarGetVersions = func(string) *versionconfig.VersionConfig {
		return &versionconfig.VersionConfig{
			KubernetesVersion: "v1.36.1",
			KubeVipVersion:    "v0.8.9",
			PauseImage:        "registry.k8s.io/pause:3.10",
		}
	}
	flatcarGetSSHUser = func() (string, error) { return "core", nil }
	flatcarNewOrchestrator = func(_ string) flatcarOrchestrator { return orch }

	flatcarPreflight = func(_ *BootstrapConfig, _ []flatcarBootstrapNode, _ *common.ColorLogger) error {
		*steps = append(*steps, "preflight")
		return nil
	}
	flatcarRenderKubeadmInit = func(env flatcar.NodeEnv) (string, error) {
		if env.NodeName != "k8s-0" {
			t.Fatalf("init render expected k8s-0, got %s", env.NodeName)
		}
		*steps = append(*steps, "render-init")
		return "INIT_CONFIG", nil
	}
	flatcarRenderKubeadmJoin = func(env flatcar.NodeEnv) (string, error) {
		// Assert join material is threaded through from the init result.
		if env.BootstrapToken != result.BootstrapToken ||
			env.CACertHash != result.CACertHash ||
			env.CertificateKey != result.CertificateKey {
			t.Fatalf("join env missing parsed material: %+v vs result %+v", env, result)
		}
		*steps = append(*steps, "render-join:"+env.NodeName)
		return "JOIN_CONFIG_" + env.NodeName, nil
	}
	flatcarInstallCilium = func(_ *BootstrapConfig, _ *common.ColorLogger) error {
		*steps = append(*steps, "cilium")
		return nil
	}
	bootstrapSaveKubeconfig = func([]byte, *common.ColorLogger) error {
		*steps = append(*steps, "save-kubeconfig")
		return nil
	}
	bootstrapPatchKubeconfig = func(string, string, *common.ColorLogger) error { return nil }
	bootstrapValidateKubeconfig = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "validate-kubeconfig")
		return nil
	}
	bootstrapWaitForNodes = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "wait-nodes")
		return nil
	}
	bootstrapApplyNamespaces = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "namespaces")
		return nil
	}
	bootstrapApplyResources = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "resources")
		return nil
	}
	bootstrapApplyCRDs = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "crds")
		return nil
	}
	bootstrapSyncHelmReleases = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "helmfile")
		return nil
	}
	bootstrapWaitForFlux = func(*BootstrapConfig, *common.ColorLogger) error {
		*steps = append(*steps, "flux")
		return nil
	}

	return steps, orch
}

func TestRunBootstrapFlatcarStepOrder(t *testing.T) {
	result := &flatcar.KubeadmResult{
		BootstrapToken: "abcdef.0123456789abcdef",
		CACertHash:     "sha256:" + strings.Repeat("a", 64),
		CertificateKey: strings.Repeat("b", 64),
	}
	steps, orch := installFlatcarFlowFakes(t, result)

	// Write kubeconfig to a temp path so fetch can write it.
	cfg := &BootstrapConfig{
		RootDir:    t.TempDir(),
		KubeConfig: t.TempDir() + "/kubeconfig",
		Provider:   "flatcar",
	}

	if err := runBootstrapFlatcar(cfg); err != nil {
		t.Fatalf("runBootstrapFlatcar returned error: %v", err)
	}

	got := strings.Join(*steps, ",")
	want := strings.Join([]string{
		"preflight",
		"render-init",
		"save-kubeconfig",
		"validate-kubeconfig",
		"render-join:k8s-1",
		"render-join:k8s-2",
		"cilium",
		"wait-nodes",
		"namespaces",
		"resources",
		"crds",
		"helmfile",
		"flux",
	}, ",")
	if got != want {
		t.Fatalf("unexpected step order:\n got: %s\nwant: %s", got, want)
	}

	// Init ran against node0 (k8s-0 -> 192.168.122.10).
	if orch.initCalledWith != "192.168.122.10" {
		t.Fatalf("init expected node0 192.168.122.10, got %s", orch.initCalledWith)
	}
	if orch.initConfig != "INIT_CONFIG" {
		t.Fatalf("unexpected init config: %s", orch.initConfig)
	}

	// Joins ran against node1 then node2, in order, via the parsed material.
	if len(orch.joinCalls) != 2 || orch.joinCalls[0] != "192.168.122.11" || orch.joinCalls[1] != "192.168.122.12" {
		t.Fatalf("unexpected join order: %v", orch.joinCalls)
	}
	if orch.joinConfigs[0] != "JOIN_CONFIG_k8s-1" || orch.joinConfigs[1] != "JOIN_CONFIG_k8s-2" {
		t.Fatalf("unexpected join configs: %v", orch.joinConfigs)
	}

	// Fetch ran against node0.
	if orch.fetchCalledWith != "192.168.122.10" {
		t.Fatalf("fetch expected node0 192.168.122.10, got %s", orch.fetchCalledWith)
	}
}

func TestRunBootstrapFlatcarFailsOnIncompleteInitMaterial(t *testing.T) {
	// Missing CertificateKey -> the flow must abort before joining.
	result := &flatcar.KubeadmResult{
		BootstrapToken: "abcdef.0123456789abcdef",
		CACertHash:     "sha256:" + strings.Repeat("a", 64),
		// CertificateKey intentionally empty
	}
	_, orch := installFlatcarFlowFakes(t, result)

	cfg := &BootstrapConfig{RootDir: t.TempDir(), KubeConfig: t.TempDir() + "/kubeconfig", Provider: "flatcar"}
	err := runBootstrapFlatcar(cfg)
	if err == nil || !strings.Contains(err.Error(), "join material") {
		t.Fatalf("expected incomplete-material error, got %v", err)
	}
	if len(orch.joinCalls) != 0 {
		t.Fatalf("join should not have run, got %v", orch.joinCalls)
	}
}

func TestRunBootstrapFlatcarPropagatesJoinError(t *testing.T) {
	result := &flatcar.KubeadmResult{
		BootstrapToken: "abcdef.0123456789abcdef",
		CACertHash:     "sha256:" + strings.Repeat("a", 64),
		CertificateKey: strings.Repeat("b", 64),
	}
	steps, orch := installFlatcarFlowFakes(t, result)
	orch.joinErr = errors.New("join boom")

	cfg := &BootstrapConfig{RootDir: t.TempDir(), KubeConfig: t.TempDir() + "/kubeconfig", Provider: "flatcar"}
	err := runBootstrapFlatcar(cfg)
	if err == nil || !strings.Contains(err.Error(), "kubeadm join failed") {
		t.Fatalf("expected join failure, got %v", err)
	}
	// Cilium must NOT have been reached after a join failure.
	for _, s := range *steps {
		if s == "cilium" {
			t.Fatalf("cilium should not run after join failure; steps=%v", *steps)
		}
	}
}

func TestRunBootstrapDispatchesToFlatcar(t *testing.T) {
	oldRunFlatcar := bootstrapRunFlatcar
	t.Cleanup(func() { bootstrapRunFlatcar = oldRunFlatcar })

	called := false
	bootstrapRunFlatcar = func(cfg *BootstrapConfig) error {
		called = true
		if !strings.EqualFold(cfg.Provider, "flatcar") {
			t.Fatalf("expected flatcar provider, got %q", cfg.Provider)
		}
		return nil
	}

	if err := runBootstrap(&BootstrapConfig{Provider: "flatcar"}); err != nil {
		t.Fatalf("runBootstrap returned error: %v", err)
	}
	if !called {
		t.Fatal("expected flatcar dispatch to be called")
	}
}

// TestKubernetesMinor moved to internal/flatcar (KubernetesMinor now lives there).

func TestFetchFlatcarKubeconfig(t *testing.T) {
	oldSave, oldPatch, oldValidate := bootstrapSaveKubeconfig, bootstrapPatchKubeconfig, bootstrapValidateKubeconfig
	var saved, validated bool
	var patchedPath, patchedIP string
	bootstrapSaveKubeconfig = func([]byte, *common.ColorLogger) error { saved = true; return nil }
	bootstrapPatchKubeconfig = func(path, ip string, _ *common.ColorLogger) error { patchedPath, patchedIP = path, ip; return nil }
	bootstrapValidateKubeconfig = func(*BootstrapConfig, *common.ColorLogger) error { validated = true; return nil }
	defer func() {
		bootstrapSaveKubeconfig, bootstrapPatchKubeconfig, bootstrapValidateKubeconfig = oldSave, oldPatch, oldValidate
	}()

	kcPath := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := &BootstrapConfig{KubeConfig: kcPath}
	orch := &fakeOrchestrator{fetchKubeconfig: "apiVersion: v1\nkind: Config\nclusters: []\n"}
	node0 := flatcarBootstrapNode{Name: "k8s-0", IP: "192.168.122.10"}

	if err := fetchFlatcarKubeconfig(cfg, orch, node0, common.NewColorLogger()); err != nil {
		t.Fatalf("fetchFlatcarKubeconfig: %v", err)
	}
	if orch.fetchCalledWith != "192.168.122.10" {
		t.Errorf("fetched from %q, want 192.168.122.10", orch.fetchCalledWith)
	}
	data, err := os.ReadFile(kcPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	if !strings.Contains(string(data), "apiVersion: v1") {
		t.Errorf("kubeconfig content missing apiVersion: %q", string(data))
	}
	if info, sErr := os.Stat(kcPath); sErr != nil {
		t.Fatalf("stat kubeconfig: %v", sErr)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("kubeconfig mode = %v, want 0600", info.Mode().Perm())
	}
	if !saved {
		t.Error("expected kubeconfig saved to 1Password")
	}
	if patchedPath != kcPath || patchedIP != "192.168.122.10" {
		t.Errorf("patch called with (%q,%q), want (%q, 192.168.122.10)", patchedPath, patchedIP, kcPath)
	}
	if !validated {
		t.Error("expected kubeconfig validated")
	}
}

func TestFetchFlatcarKubeconfigDryRun(t *testing.T) {
	orch := &fakeOrchestrator{fetchKubeconfig: "nope"}
	cfg := &BootstrapConfig{DryRun: true, KubeConfig: filepath.Join(t.TempDir(), "kc")}
	if err := fetchFlatcarKubeconfig(cfg, orch, flatcarBootstrapNode{Name: "k8s-0", IP: "1.2.3.4"}, common.NewColorLogger()); err != nil {
		t.Fatalf("dry-run fetchFlatcarKubeconfig: %v", err)
	}
	if orch.fetchCalledWith != "" {
		t.Error("dry-run must not fetch from a node")
	}
	if _, err := os.Stat(cfg.KubeConfig); !os.IsNotExist(err) {
		t.Error("dry-run must not write a kubeconfig file")
	}
}

func TestCheckFlatcarNodeReady(t *testing.T) {
	oldNewRunner := flatcarNewSSHRunner
	t.Cleanup(func() { flatcarNewSSHRunner = oldNewRunner })

	t.Run("passes when flatcar + kubelet present", func(t *testing.T) {
		flatcarNewSSHRunner = func(_, _ string) flatcarSSHRunner {
			return &fakeSSHRunner{out: "/usr/bin/kubelet\n"}
		}
		err := checkFlatcarNodeReady("core", flatcarBootstrapNode{Name: "k8s-0", IP: "192.168.122.10"}, common.NewColorLogger())
		if err != nil {
			t.Fatalf("expected pass, got %v", err)
		}
	})

	t.Run("fails when command errors", func(t *testing.T) {
		flatcarNewSSHRunner = func(_, _ string) flatcarSSHRunner {
			return &fakeSSHRunner{execErr: errors.New("not flatcar")}
		}
		err := checkFlatcarNodeReady("core", flatcarBootstrapNode{Name: "k8s-0", IP: "192.168.122.10"}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "kubelet missing") {
			t.Fatalf("expected failure, got %v", err)
		}
	})

	t.Run("fails on connect error", func(t *testing.T) {
		flatcarNewSSHRunner = func(_, _ string) flatcarSSHRunner {
			return &fakeSSHRunner{connectErr: errors.New("unreachable")}
		}
		err := checkFlatcarNodeReady("core", flatcarBootstrapNode{Name: "k8s-0", IP: "192.168.122.10"}, common.NewColorLogger())
		if err == nil || !strings.Contains(err.Error(), "ssh connect failed") {
			t.Fatalf("expected connect failure, got %v", err)
		}
	})
}

type fakeSSHRunner struct {
	connectErr error
	execErr    error
	out        string
}

func (f *fakeSSHRunner) Connect() error { return f.connectErr }
func (f *fakeSSHRunner) Close() error   { return nil }
func (f *fakeSSHRunner) ExecuteCommand(string) (string, error) {
	if f.execErr != nil {
		return "", f.execErr
	}
	return f.out, nil
}
