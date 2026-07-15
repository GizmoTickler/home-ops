package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cmdflatcar "homeops-cli/cmd/flatcar"
	"homeops-cli/internal/config"
	flatcarinternal "homeops-cli/internal/flatcar"
	vmprov "homeops-cli/internal/provider"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBootstrapToken = "abcdef.0123456789abcdef"

func testRehearseConfig() *config.Config {
	return &config.Config{
		Cluster: config.ClusterConfig{
			NodeSSHPort: 22,
			Nodes: []config.Node{{
				Name: "k8s-0", IP: "192.0.2.10",
				VM: config.VMProfile{VMID: 200, Mac: "02:00:00:00:00:10"},
			}},
			TestNode: &config.Node{
				Name: "k8s-test", IP: "192.0.2.99",
				VM: config.VMProfile{VMID: 299, Mac: "02:00:00:00:00:99", Ceph: config.CephDisk{Mode: "none"}},
			},
		},
		Hypervisors: config.HypervisorsConfig{Default: "proxmox"},
		Volsync:     config.VolsyncConfig{CheckImage: "docker.io/library/alpine:3.22"},
	}
}

func TestBuildRehearseNodeSpecRefusals(t *testing.T) {
	t.Run("missing test node", func(t *testing.T) {
		cfg := testRehearseConfig()
		cfg.Cluster.TestNode = nil
		_, err := buildRehearseNodeSpec(cfg, "proxmox")
		require.ErrorContains(t, err, "cluster.test_node is required")
	})

	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{name: "production name collision", mutate: func(cfg *config.Config) { cfg.Cluster.TestNode.Name = "k8s-0" }, want: "matches production"},
		{name: "production IP collision", mutate: func(cfg *config.Config) { cfg.Cluster.TestNode.IP = "192.0.2.10" }, want: "matches production"},
		{name: "production VMID collision", mutate: func(cfg *config.Config) { cfg.Cluster.TestNode.VM.VMID = 200 }, want: "matches production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRehearseConfig()
			tc.mutate(cfg)
			_, err := buildRehearseNodeSpec(cfg, "proxmox")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLivePreconditionCollisionRefusals(t *testing.T) {
	err := refuseExistingKubernetesNode(`{"items":[{"metadata":{"name":"k8s-test"}}]}`, "k8s-test")
	require.ErrorContains(t, err, `kubernetes node "k8s-test" already exists`)

	spec, err := buildRehearseNodeSpec(testRehearseConfig(), "proxmox")
	require.NoError(t, err)
	err = refuseExistingVM([]vmprov.VMSummary{{Name: "other", ID: "299"}}, spec)
	require.ErrorContains(t, err, "VMID 299 already exists")
	err = refuseExistingVM([]vmprov.VMSummary{{Name: "k8s-test", ID: "398"}}, spec)
	require.ErrorContains(t, err, `VM named "k8s-test" already exists`)
}

type fakeRehearseOperations struct {
	calls            []string
	failAt           string
	invalidatedToken string
}

func (f *fakeRehearseOperations) record(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (f *fakeRehearseOperations) Preconditions(context.Context, rehearseNodeSpec) (string, error) {
	return "healthy", f.record("preconditions")
}

func (f *fakeRehearseOperations) CreateJoinMaterial(context.Context, rehearseNodeSpec) (*flatcarinternal.KubeadmResult, error) {
	if err := f.record("token"); err != nil {
		return nil, err
	}
	return &flatcarinternal.KubeadmResult{
		BootstrapToken: testBootstrapToken,
		CACertHash:     "sha256:" + strings.Repeat("1", 64),
		CertificateKey: strings.Repeat("2", 64),
	}, nil
}

func (f *fakeRehearseOperations) Deploy(context.Context, rehearseNodeSpec, rehearseNodeOptions, flatcarinternal.KubeadmResult) error {
	return f.record("deploy")
}

func (f *fakeRehearseOperations) WaitReady(context.Context, rehearseNodeSpec, time.Duration) (rehearseNodeReady, error) {
	if err := f.record("ready"); err != nil {
		return rehearseNodeReady{}, err
	}
	return rehearseNodeReady{KubeletVersion: "v1.36.2", CNIReady: true}, nil
}

func (f *fakeRehearseOperations) SmokeTest(context.Context, rehearseNodeSpec, time.Duration) error {
	return f.record("smoke")
}

func (f *fakeRehearseOperations) DrainAndDeleteNode(context.Context, rehearseNodeSpec, time.Duration) error {
	return f.record("drain-node")
}

func (f *fakeRehearseOperations) DeleteVM(context.Context, rehearseNodeSpec) error {
	return f.record("delete-vm")
}

func (f *fakeRehearseOperations) InvalidateToken(_ context.Context, _ rehearseNodeSpec, token string) error {
	f.invalidatedToken = token
	return f.record("invalidate-token")
}

func testRehearseSpec(t *testing.T) rehearseNodeSpec {
	t.Helper()
	spec, err := buildRehearseNodeSpec(testRehearseConfig(), "proxmox")
	require.NoError(t, err)
	return spec
}

func TestRunRehearseNodeSequenceAndTokenInvalidation(t *testing.T) {
	fake := &fakeRehearseOperations{}
	report, err := runRehearseNode(context.Background(), testRehearseSpec(t), rehearseNodeOptions{Timeout: time.Minute}, fake)
	require.NoError(t, err)
	assert.Equal(t, []string{"preconditions", "token", "deploy", "ready", "smoke", "drain-node", "delete-vm", "invalidate-token"}, fake.calls)
	assert.Equal(t, testBootstrapToken, fake.invalidatedToken)
	assert.Equal(t, "PASS", report.Verdict)
	for _, step := range report.Steps {
		assert.Equal(t, "PASS", step.Status, step.Name)
	}
}

func TestRunRehearseNodeTeardownOnFailure(t *testing.T) {
	fake := &fakeRehearseOperations{failAt: "smoke"}
	report, err := runRehearseNode(context.Background(), testRehearseSpec(t), rehearseNodeOptions{Timeout: time.Minute}, fake)
	require.ErrorContains(t, err, "smoke failed")
	assert.Equal(t, []string{"preconditions", "token", "deploy", "ready", "smoke", "drain-node", "delete-vm", "invalidate-token"}, fake.calls)
	assert.Equal(t, "FAIL", report.Verdict)
	assert.Equal(t, "FAIL", report.Steps[3].Status)
	assert.Equal(t, "PASS", report.Steps[4].Status)
}

func TestRunRehearseNodeKeepSkipsAllTeardown(t *testing.T) {
	fake := &fakeRehearseOperations{}
	report, err := runRehearseNode(context.Background(), testRehearseSpec(t), rehearseNodeOptions{Timeout: time.Minute, Keep: true}, fake)
	require.NoError(t, err)
	assert.Equal(t, []string{"preconditions", "token", "deploy", "ready", "smoke"}, fake.calls)
	assert.Equal(t, "SKIP", report.Steps[4].Status)
	assert.Contains(t, report.Steps[4].Detail, "--keep")
	require.Len(t, report.CleanupCommands, 5)
	assert.Contains(t, report.CleanupCommands[4], "kubeadm token delete abcdef")
}

func TestRehearseNodePlanRenderingDoesNotExecuteOrConfirm(t *testing.T) {
	oldConfig := rehearseConfigFn
	oldConfirm := rehearseConfirmFn
	rehearseConfigFn = testRehearseConfig
	rehearseConfirmFn = func(string, bool) (bool, error) {
		t.Fatal("plan must not prompt")
		return false, nil
	}
	t.Cleanup(func() {
		rehearseConfigFn = oldConfig
		rehearseConfirmFn = oldConfirm
	})

	fake := &fakeRehearseOperations{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, executeRehearseNodeCommand(cmd, rehearseNodeOptions{Plan: true, Timeout: time.Minute, Output: "table"}, fake))
	assert.Empty(t, fake.calls)
	assert.Contains(t, out.String(), "preconditions")
	assert.Contains(t, out.String(), "SKIP")
	assert.Contains(t, out.String(), "PLAN")
	assert.Contains(t, out.String(), "required at execution")
	assert.Contains(t, out.String(), "Cleanup commands")
}

func TestParseRehearseNodeReady(t *testing.T) {
	ready, detail, err := parseRehearseNodeReady([]byte(`{"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"NetworkUnavailable","status":"False"}],"nodeInfo":{"kubeletVersion":"v1.36.2"}}}`))
	require.NoError(t, err)
	assert.True(t, ready.CNIReady)
	assert.Equal(t, "v1.36.2", ready.KubeletVersion)
	assert.Contains(t, detail, "networkUnavailable=false")
}

type fakeLifecycle struct {
	vmprov.VMLifecycle
	summaries   []vmprov.VMSummary
	summaryErr  error
	stopped     bool
	deleted     bool
	deleteError error
}

func (f *fakeLifecycle) VMSummaries() ([]vmprov.VMSummary, error) {
	return f.summaries, f.summaryErr
}

func (f *fakeLifecycle) StopVM(string, bool) error {
	f.stopped = true
	return nil
}

func (f *fakeLifecycle) DeleteVM(string) error {
	f.deleted = true
	return f.deleteError
}

func (f *fakeLifecycle) Close() error { return nil }

func swapRehearseRuntime(t *testing.T) {
	t.Helper()
	oldCommand := rehearseCommandFn
	oldLifecycle := rehearseWithVMLifecycleFn
	oldDeploy := rehearseDeployNodeFn
	oldOrchestrator := rehearseOrchestratorFn
	t.Cleanup(func() {
		rehearseCommandFn = oldCommand
		rehearseWithVMLifecycleFn = oldLifecycle
		rehearseDeployNodeFn = oldDeploy
		rehearseOrchestratorFn = oldOrchestrator
	})
}

func TestRealPreconditionsAndNodeReadiness(t *testing.T) {
	swapRehearseRuntime(t)
	spec := testRehearseSpec(t)
	lifecycle := &fakeLifecycle{}
	rehearseWithVMLifecycleFn = func(_ string, fn func(vmprov.VMLifecycle) error) error { return fn(lifecycle) }
	rehearseCommandFn = func(_ context.Context, _ string, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "get --raw=/readyz":
			return "ok\n", nil
		case "get nodes -o json":
			return `{"items":[{"metadata":{"name":"k8s-0"}}]}`, nil
		case "get node k8s-test -o json":
			return `{"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"NetworkUnavailable","status":"False"}],"nodeInfo":{"kubeletVersion":"v1.36.2"}}}`, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", strings.Join(args, " "))
		}
	}

	detail, err := (realRehearseOperations{}).Preconditions(context.Background(), spec)
	require.NoError(t, err)
	assert.Contains(t, detail, "apiserver ready")
	ready, err := (realRehearseOperations{}).WaitReady(context.Background(), spec, time.Second)
	require.NoError(t, err)
	assert.True(t, ready.CNIReady)
	assert.Equal(t, "v1.36.2", ready.KubeletVersion)
}

func TestRealSmokeAndNodeCleanupCommands(t *testing.T) {
	swapRehearseRuntime(t)
	spec := testRehearseSpec(t)
	var commands []string
	rehearseCommandFn = func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if len(args) >= 3 && args[0] == "get" && args[1] == "node" {
			return "node/" + spec.Node.Name, nil
		}
		return "ok", nil
	}

	require.NoError(t, (realRehearseOperations{}).SmokeTest(context.Background(), spec, time.Minute))
	require.NoError(t, (realRehearseOperations{}).DrainAndDeleteNode(context.Background(), spec, time.Minute))
	joined := strings.Join(commands, "\n")
	assert.Contains(t, joined, "kubectl run homeops-rehearse-k8s-test")
	assert.Contains(t, joined, "nodeSelector")
	assert.Contains(t, joined, "kubectl exec")
	assert.Contains(t, joined, "nslookup kubernetes.default")
	assert.Contains(t, joined, "kubectl delete pod")
	assert.Contains(t, joined, "kubectl drain k8s-test")
	assert.Contains(t, joined, "kubectl delete node k8s-test")
}

func TestRealVMDeletionAssertsIdentityAndPowersOff(t *testing.T) {
	swapRehearseRuntime(t)
	spec := testRehearseSpec(t)
	lifecycle := &fakeLifecycle{summaries: []vmprov.VMSummary{{Name: spec.Node.Name, ID: "299", Status: "running"}}}
	rehearseWithVMLifecycleFn = func(_ string, fn func(vmprov.VMLifecycle) error) error { return fn(lifecycle) }
	require.NoError(t, (realRehearseOperations{}).DeleteVM(context.Background(), spec))
	assert.True(t, lifecycle.stopped)
	assert.True(t, lifecycle.deleted)

	lifecycle = &fakeLifecycle{summaries: []vmprov.VMSummary{{Name: spec.Node.Name, ID: "300", Status: "running"}}}
	err := (realRehearseOperations{}).DeleteVM(context.Background(), spec)
	require.ErrorContains(t, err, "refusing to delete")
	assert.False(t, lifecycle.deleted)
}

type fakeKubeadmOrchestrator struct {
	createdIP    string
	createdTTL   time.Duration
	deletedIP    string
	deletedToken string
}

func (f *fakeKubeadmOrchestrator) CreateJoinMaterial(ip string, ttl time.Duration) (*flatcarinternal.KubeadmResult, error) {
	f.createdIP, f.createdTTL = ip, ttl
	return &flatcarinternal.KubeadmResult{BootstrapToken: testBootstrapToken}, nil
}

func (f *fakeKubeadmOrchestrator) DeleteBootstrapToken(ip, token string) error {
	f.deletedIP, f.deletedToken = ip, token
	return nil
}

func TestRealOperationSeamsPassDeployAndTokenInputs(t *testing.T) {
	swapRehearseRuntime(t)
	spec := testRehearseSpec(t)
	orchestrator := &fakeKubeadmOrchestrator{}
	rehearseOrchestratorFn = func(rehearseNodeSpec) rehearseKubeadmOrchestrator { return orchestrator }
	var deployed bool
	rehearseDeployNodeFn = func(_ context.Context, options cmdflatcar.RehearsalDeployOptions) error {
		deployed = true
		assert.Equal(t, spec.Node.Name, options.Node.Name)
		assert.Equal(t, "volume", options.ImageVolume)
		return nil
	}

	material, err := (realRehearseOperations{}).CreateJoinMaterial(context.Background(), spec)
	require.NoError(t, err)
	require.NoError(t, (realRehearseOperations{}).Deploy(context.Background(), spec, rehearseNodeOptions{Timeout: time.Minute, ImageVolume: "volume"}, *material))
	require.NoError(t, (realRehearseOperations{}).InvalidateToken(context.Background(), spec, testBootstrapToken))
	assert.True(t, deployed)
	assert.Equal(t, spec.InitNode.IP, orchestrator.createdIP)
	assert.Equal(t, rehearseTokenTTL, orchestrator.createdTTL)
	assert.Equal(t, testBootstrapToken, orchestrator.deletedToken)
}

func TestExecuteRehearseNodeConfirmedJSON(t *testing.T) {
	oldConfig := rehearseConfigFn
	oldConfirm := rehearseConfirmFn
	rehearseConfigFn = testRehearseConfig
	rehearseConfirmFn = func(string, bool) (bool, error) { return true, nil }
	t.Cleanup(func() {
		rehearseConfigFn = oldConfig
		rehearseConfirmFn = oldConfirm
	})
	fake := &fakeRehearseOperations{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, executeRehearseNodeCommand(cmd, rehearseNodeOptions{Timeout: time.Minute, Output: "json", ImageVolume: "volume"}, fake))
	assert.Contains(t, out.String(), `"verdict": "PASS"`)
	assert.Equal(t, testBootstrapToken, fake.invalidatedToken)
}
