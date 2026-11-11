package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

func testReplaceConfig() *config.Config {
	cfg := testRehearseConfig()
	cfg.Cluster.OS = "fcos"
	cfg.Cluster.Nodes = []config.Node{
		{Name: "k8s-0", IP: "192.0.2.10", VM: config.VMProfile{VMID: 200, Mac: "02:00:00:00:00:10"}},
		{Name: "k8s-1", IP: "192.0.2.11", VM: config.VMProfile{VMID: 201, Mac: "02:00:00:00:00:11"}},
		{Name: "k8s-2", IP: "192.0.2.12", VM: config.VMProfile{VMID: 202, Mac: "02:00:00:00:00:12"}},
	}
	return cfg
}

func testReplaceSpec(t *testing.T, node string) rehearseNodeSpec {
	t.Helper()
	spec, err := buildReplaceNodeSpec(testReplaceConfig(), replaceNodeOptions{Node: node})
	require.NoError(t, err)
	return spec
}

func swapReplaceConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	old := rehearseConfigFn
	rehearseConfigFn = func() *config.Config { return cfg }
	t.Cleanup(func() { rehearseConfigFn = old })
}

func TestBuildReplaceNodeSpecRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		node   string
		mutate func(*config.Config)
		opts   replaceNodeOptions
		want   string
	}{
		{name: "test node", node: "k8s-test", want: "rehearsal test node"},
		{name: "unknown name", node: "k8s-9", want: "not a production cluster.nodes entry"},
		{name: "empty", node: " ", want: "--node is required"},
		{name: "flatcar OS", node: "k8s-2", mutate: func(c *config.Config) { c.Cluster.OS = "" }, want: "configured OS is flatcar"},
		{name: "non-proxmox", node: "k8s-2", mutate: func(c *config.Config) { c.Hypervisors.Default = "vsphere" }, want: "Proxmox only"},
		{name: "only node", node: "k8s-0", mutate: func(c *config.Config) { c.Cluster.Nodes = c.Cluster.Nodes[:1] }, want: "needs another control plane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testReplaceConfig()
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			opts := tc.opts
			opts.Node = tc.node
			_, err := buildReplaceNodeSpec(cfg, opts)
			require.ErrorContains(t, err, tc.want)
		})
	}

	cfg := testReplaceConfig()
	cfg.Cluster.OS = ""
	_, err := buildReplaceNodeSpec(cfg, replaceNodeOptions{Node: "k8s-2", AllowSameOS: true})
	require.NoError(t, err, "--allow-same-os permits a flatcar rebuild")

	// rehearse-node's refusal of production identities is untouched.
	cfg = testReplaceConfig()
	cfg.Cluster.TestNode.Name = "k8s-2"
	_, err = buildRehearseNodeSpec(cfg, "proxmox")
	require.ErrorContains(t, err, "matches production")
}

func TestReplaceInitNodeSkipsTarget(t *testing.T) {
	spec := testReplaceSpec(t, "k8s-0")
	assert.Equal(t, "k8s-1", spec.InitNode.Name)
	assert.Equal(t, 200, spec.VMID)
	assert.Equal(t, "k8s-0", testReplaceSpec(t, "k8s-2").InitNode.Name)

	// Join material and etcd membership changes go through the init node.
	swapRehearseRuntime(t)
	orchestrator := &fakeKubeadmOrchestrator{}
	rehearseOrchestratorFn = func(rehearseNodeSpec) rehearseKubeadmOrchestrator { return orchestrator }
	_, err := (realReplaceOperations{}).CreateJoinMaterial(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.11", orchestrator.createdIP)

	var pods []string
	rehearseCommandFn = func(_ context.Context, _ string, args ...string) (string, error) {
		if i := slices.Index(args, "exec"); i >= 0 {
			pods = append(pods, args[i+1])
			return `{"members":[{"ID":10,"name":"k8s-0","peerURLs":["https://192.0.2.10:2380"]}]}`, nil
		}
		return "", nil
	}
	require.NoError(t, (realReplaceOperations{}).RemoveFromCluster(context.Background(), spec))
	assert.Equal(t, []string{"etcd-k8s-1", "etcd-k8s-1"}, pods)
}

func etcdFixture(names ...string) (string, string) {
	var members, health []string
	for i, name := range names {
		ip := "192.0.2." + strconv.Itoa(10+i)
		client := "https://" + ip + ":2379"
		members = append(members, fmt.Sprintf(`{"ID":%d,"name":%q,"peerURLs":["https://%s:2380"],"clientURLs":[%q]}`, i+1, name, ip, client))
		health = append(health, fmt.Sprintf(`{"endpoint":%q,"health":true}`, client))
	}
	return `{"members":[` + strings.Join(members, ",") + `]}`, "[" + strings.Join(health, ",") + "]"
}

const replaceNodesJSON = `{"items":[
 {"metadata":{"name":"k8s-0","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"k8s-1","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"k8s-2","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"False"}]}},
 {"metadata":{"name":"k8s-test","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`

func TestReplacePreconditionsQuorum(t *testing.T) {
	cfg := testReplaceConfig()
	swapReplaceConfig(t, cfg)
	for _, tc := range []struct {
		name    string
		members []string
		unhealt bool
		resumed bool
		want    string
	}{
		{name: "three members leave two", members: []string{"k8s-0", "k8s-1", "k8s-2"}, want: "only 2 healthy etcd members besides k8s-2"},
		{name: "unhealthy member", members: []string{"k8s-0", "k8s-1", "k8s-2", "k8s-test"}, unhealt: true, want: "not healthy"},
		{name: "four healthy members", members: []string{"k8s-0", "k8s-1", "k8s-2", "k8s-test"}},
		// A resumed run: an earlier attempt removed k8s-2's member (etcdFixture
		// gives the third slot k8s-2's IP, so a placeholder name stands in for
		// a fourth member at another address).
		{name: "resumed after member removal", members: []string{"k8s-0", "k8s-1", "gone", "k8s-test"}, resumed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			swapRehearseRuntime(t)
			spec := testReplaceSpec(t, "k8s-2")
			memberJSON, healthJSON := etcdFixture(tc.members...)
			if tc.resumed {
				// Drop the member at k8s-2's address: it was already removed.
				memberJSON = strings.Replace(memberJSON, `{"ID":3,"name":"gone","peerURLs":["https://192.0.2.12:2380"],"clientURLs":["https://192.0.2.12:2379"]},`, "", 1)
				healthJSON = strings.Replace(healthJSON, `{"endpoint":"https://192.0.2.12:2379","health":true},`, "", 1)
				require.NotContains(t, memberJSON, "192.0.2.12", "fixture must no longer hold the target")
			}
			if tc.unhealt {
				healthJSON = strings.Replace(healthJSON, `"health":true`, `"health":false`, 1)
			}
			rehearseCommandFn = func(_ context.Context, _ string, args ...string) (string, error) {
				joined := strings.Join(args, " ")
				switch {
				case joined == "get --raw=/readyz":
					return "ok", nil
				case joined == "get nodes -o json":
					return replaceNodesJSON, nil
				case strings.Contains(joined, "member list"):
					return memberJSON, nil
				case strings.Contains(joined, "endpoint health"):
					return healthJSON, nil
				}
				return "", fmt.Errorf("unexpected %s", joined)
			}
			vmLookups := 0
			rehearseWithVMLifecycleFn = func(_ string, fn func(vmprov.VMLifecycle) error) error {
				vmLookups++
				return fn(&fakeLifecycle{summaries: []vmprov.VMSummary{{Name: "k8s-2", ID: "202"}}})
			}
			pve := newFakePVE()
			swapPVE(t, pve)

			pre, err := (realReplaceOperations{}).Preconditions(context.Background(), spec)
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				assert.Zero(t, vmLookups, "refusal happens before touching the hypervisor")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 4, pre.EtcdMembers, "expected membership after the rejoin: the others plus the rebuilt node")
			assert.Empty(t, pve.commands, "preconditions never touch pve directly (no scratch disk to inspect)")
		})
	}
}

func TestReplaceNodeChecks(t *testing.T) {
	spec := testReplaceSpec(t, "k8s-2")
	nodes, err := parseReplaceNodes(replaceNodesJSON)
	require.NoError(t, err)
	require.NoError(t, checkReplaceNodes(nodes, spec), "the target itself may be NotReady")
	require.ErrorContains(t, checkReplaceNodes(nodes[:2], spec), `"k8s-2" does not exist`)
	nodes[1].Ready = false
	require.ErrorContains(t, checkReplaceNodes(nodes, spec), "k8s-1 is not Ready")

	require.NoError(t, checkReplaceVMs([]vmprov.VMSummary{{Name: "k8s-2", ID: "202"}, {Name: "x", ID: "9202"}}, spec))
	require.ErrorContains(t, checkReplaceVMs([]vmprov.VMSummary{{Name: "other", ID: "202"}}, spec), "expected k8s-2")
	require.ErrorContains(t, checkReplaceVMs(nil, spec), "does not exist")
}

// --- run-level sequence with fake operations ---

type fakeReplaceOperations struct {
	calls  []string
	failAt string
	token  string
}

func (f *fakeReplaceOperations) record(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (f *fakeReplaceOperations) Preconditions(context.Context, rehearseNodeSpec) (replacePreflight, error) {
	return replacePreflight{EtcdMembers: 4}, f.record("preconditions")
}
func (f *fakeReplaceOperations) Drain(context.Context, rehearseNodeSpec, time.Duration) error {
	return f.record("drain")
}
func (f *fakeReplaceOperations) RemoveFromCluster(context.Context, rehearseNodeSpec) error {
	return f.record("remove-member")
}
func (f *fakeReplaceOperations) DestroyVM(context.Context, rehearseNodeSpec) error {
	return f.record("destroy-vm")
}
func (f *fakeReplaceOperations) ForgetHostKey(context.Context, rehearseNodeSpec) {
	_ = f.record("forget-host-key")
}
func (f *fakeReplaceOperations) CreateJoinMaterial(context.Context, rehearseNodeSpec) (*flatcarinternal.KubeadmResult, error) {
	if err := f.record("join-material"); err != nil {
		return nil, err
	}
	return &flatcarinternal.KubeadmResult{BootstrapToken: testBootstrapToken}, nil
}
func (f *fakeReplaceOperations) Deploy(context.Context, rehearseNodeSpec, replaceNodeOptions, flatcarinternal.KubeadmResult) error {
	if err := f.record("deploy"); err != nil {
		return err
	}
	return f.record("join")
}
func (f *fakeReplaceOperations) WaitReady(context.Context, rehearseNodeSpec, time.Duration) (rehearseNodeReady, error) {
	return rehearseNodeReady{KubeletVersion: "v1.36.2", CNIReady: true}, f.record("wait-ready")
}
func (f *fakeReplaceOperations) SmokeTest(context.Context, rehearseNodeSpec, time.Duration) error {
	return f.record("smoke-test")
}
func (f *fakeReplaceOperations) Uncordon(context.Context, rehearseNodeSpec) error {
	return f.record("uncordon")
}
func (f *fakeReplaceOperations) PostChecks(context.Context, rehearseNodeSpec, replacePreflight) (string, error) {
	return "ok", f.record("post-checks")
}
func (f *fakeReplaceOperations) InvalidateToken(_ context.Context, _ rehearseNodeSpec, token string) error {
	f.token = token
	return f.record("invalidate-token")
}

func TestRunReplaceNodeStepOrder(t *testing.T) {
	swapReplaceConfig(t, testReplaceConfig())
	fake := &fakeReplaceOperations{}
	report, err := runReplaceNode(context.Background(), testReplaceSpec(t, "k8s-2"), replaceNodeOptions{Timeout: time.Minute}, fake)
	require.NoError(t, err)
	assert.Equal(t, []string{"preconditions", "drain", "remove-member", "destroy-vm", "forget-host-key",
		"join-material", "deploy", "join", "wait-ready", "smoke-test", "uncordon", "post-checks", "invalidate-token"}, fake.calls)
	assert.Equal(t, testBootstrapToken, fake.token)
	assert.Equal(t, "PASS", report.Verdict)
	for _, step := range report.Steps {
		assert.Equal(t, "PASS", step.Status, step.Name)
	}
	assert.Empty(t, report.RecoveryCommands)
}

func TestRunReplaceNodeStopsAtFailures(t *testing.T) {
	swapReplaceConfig(t, testReplaceConfig())
	fake := &fakeReplaceOperations{failAt: "preconditions"}
	_, err := runReplaceNode(context.Background(), testReplaceSpec(t, "k8s-2"), replaceNodeOptions{Timeout: time.Minute}, fake)
	require.Error(t, err)
	assert.Equal(t, []string{"preconditions"}, fake.calls, "a failed precondition mutates nothing")

	fake = &fakeReplaceOperations{failAt: "destroy-vm"}
	_, err = runReplaceNode(context.Background(), testReplaceSpec(t, "k8s-2"), replaceNodeOptions{Timeout: time.Minute}, fake)
	require.ErrorContains(t, err, "destroy-vm failed")
	assert.Equal(t, []string{"preconditions", "drain", "remove-member", "destroy-vm"}, fake.calls, "nothing is deployed after a failed teardown")

	fake = &fakeReplaceOperations{failAt: "deploy"}
	report, err := runReplaceNode(context.Background(), testReplaceSpec(t, "k8s-2"), replaceNodeOptions{Timeout: time.Minute}, fake)
	require.ErrorContains(t, err, "deploy failed")
	assert.Equal(t, []string{"preconditions", "drain", "remove-member", "destroy-vm", "forget-host-key",
		"join-material", "deploy", "invalidate-token"}, fake.calls, "the bootstrap token is invalidated even on failure")
	assert.Empty(t, report.RecoveryCommands)

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	require.NoError(t, writeReplaceReport(cmd, report, "table"))
	assert.NotContains(t, out.String(), "Manual recovery")
}

// --- VM teardown against a simulated Proxmox host ---

type fakePVE struct {
	commands []string
	failOn   string
}

func newFakePVE() *fakePVE { return &fakePVE{} }

func swapPVE(t *testing.T, pve *fakePVE) {
	t.Helper()
	old := replacePVECommandFn
	replacePVECommandFn = pve.run
	t.Cleanup(func() { replacePVECommandFn = old })
}

func (f *fakePVE) run(_ context.Context, command string) (string, error) {
	f.commands = append(f.commands, command)
	if command == f.failOn {
		return "", errors.New("simulated failure")
	}
	return "", nil
}

func TestReplaceDestroyVMCommands(t *testing.T) {
	spec := testReplaceSpec(t, "k8s-2")
	pve := newFakePVE()
	swapPVE(t, pve)
	require.NoError(t, (realReplaceOperations{}).DestroyVM(context.Background(), spec))
	assert.Equal(t, []string{"qm stop 202", "qm destroy 202 --destroy-unreferenced-disks 0"}, pve.commands)
	for _, c := range pve.commands {
		assert.NotContains(t, c, "scsi4", "no scratch-disk handling remains")
	}

	pve = newFakePVE()
	pve.failOn = "qm stop 202"
	swapPVE(t, pve)
	require.ErrorContains(t, (realReplaceOperations{}).DestroyVM(context.Background(), spec), "simulated failure")
	assert.Equal(t, []string{"qm stop 202"}, pve.commands, "a VM that did not stop is never destroyed")
}

// --- command layer ---

func TestReplaceNodePlanExecutesNothing(t *testing.T) {
	swapReplaceConfig(t, testReplaceConfig())
	oldConfirm := rehearseConfirmFn
	rehearseConfirmFn = func(string, bool) (bool, error) {
		t.Fatal("plan must not prompt")
		return false, nil
	}
	t.Cleanup(func() { rehearseConfirmFn = oldConfirm })
	pve := newFakePVE()
	swapPVE(t, pve)
	swapRehearseRuntime(t)
	rehearseCommandFn = func(context.Context, string, ...string) (string, error) {
		t.Fatal("plan must not run commands")
		return "", nil
	}

	fake := &fakeReplaceOperations{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, executeReplaceNodeCommand(cmd, replaceNodeOptions{Node: "k8s-2", Plan: true, Timeout: time.Minute, Output: "table"}, fake))
	assert.Empty(t, fake.calls)
	assert.Empty(t, pve.commands)
	text := out.String()
	assert.Contains(t, text, "PLAN")
	assert.Contains(t, text, "qm stop 202; qm destroy 202 --destroy-unreferenced-disks 0")
	assert.NotContains(t, text, "scsi4")
	assert.NotContains(t, text, "scratch")
	assert.NotContains(t, text, "9202")
	assert.Contains(t, text, "required at execution: --image-path or --image-volume")
	for _, name := range replaceStepNames {
		assert.Contains(t, text, name)
	}

	out.Reset()
	require.NoError(t, executeReplaceNodeCommand(cmd, replaceNodeOptions{Node: "k8s-2", Plan: true, Timeout: time.Minute, Output: "json", ImageVolume: "vm-ssd:vm-900-disk-0"}, fake))
	assert.Contains(t, out.String(), "pinned volume vm-ssd:vm-900-disk-0")
	assert.Empty(t, fake.calls)
}

func TestReplaceNodeRequiresPinnedImage(t *testing.T) {
	swapReplaceConfig(t, testReplaceConfig())
	prompts := 0
	oldConfirm := rehearseConfirmFn
	rehearseConfirmFn = func(string, bool) (bool, error) { prompts++; return false, nil }
	t.Cleanup(func() { rehearseConfirmFn = oldConfirm })
	fake := &fakeReplaceOperations{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	err := executeReplaceNodeCommand(cmd, replaceNodeOptions{Node: "k8s-2", Timeout: time.Minute, Output: "table"}, fake)
	require.ErrorContains(t, err, "pin the OS image")
	assert.Zero(t, prompts)

	err = executeReplaceNodeCommand(cmd, replaceNodeOptions{Node: "k8s-2", Timeout: time.Minute, Output: "table", ImageVolume: "v", ImageStreamLatest: true}, fake)
	require.ErrorContains(t, err, "cannot be combined")

	for _, opts := range []replaceNodeOptions{{ImageStreamLatest: true}, {ImagePath: "/img.raw"}} {
		opts.Node, opts.Timeout, opts.Output = "k8s-2", time.Minute, "table"
		err = executeReplaceNodeCommand(cmd, opts, fake)
		require.ErrorContains(t, err, "cancelled", "reaches the confirmation prompt")
	}
	assert.Equal(t, 2, prompts)
	assert.Empty(t, fake.calls)
}

func TestReplaceDeployPassesImageAndPowersOn(t *testing.T) {
	swapRehearseRuntime(t)
	spec := testReplaceSpec(t, "k8s-2")
	called := false
	rehearseDeployNodeFn = func(_ context.Context, options cmdflatcar.RehearsalDeployOptions) error {
		called = true
		assert.Equal(t, "k8s-2", options.Node.Name)
		assert.Equal(t, "vm-ssd:vm-900-disk-0", options.ImageVolume)
		assert.Nil(t, options.Boot, "no pre-boot disk work: the VM is created powered on")
		return nil
	}
	err := (realReplaceOperations{}).Deploy(context.Background(), spec, replaceNodeOptions{Timeout: time.Minute, ImageVolume: "vm-ssd:vm-900-disk-0"},
		flatcarinternal.KubeadmResult{})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestReplacePostChecks(t *testing.T) {
	swapReplaceConfig(t, testReplaceConfig())
	swapRehearseRuntime(t)
	spec := testReplaceSpec(t, "k8s-2")
	memberJSON, healthJSON := etcdFixture("k8s-0", "k8s-1", "k8s-2", "k8s-test")
	osImage := "Fedora CoreOS 44.20260901.3.0"
	ciliumReady := "True"
	rehearseCommandFn = func(_ context.Context, _ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "member list"):
			return memberJSON, nil
		case strings.Contains(joined, "endpoint health"):
			return healthJSON, nil
		case joined == "get node k8s-2 -o json":
			return fmt.Sprintf(`{"metadata":{"name":"k8s-2"},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"osImage":%q}}}`, osImage), nil
		case strings.Contains(joined, "k8s-app=cilium"):
			assert.Contains(t, joined, "spec.nodeName=k8s-2")
			return fmt.Sprintf(`{"items":[{"status":{"conditions":[{"type":"Ready","status":%q}]}}]}`, ciliumReady), nil
		}
		return "", fmt.Errorf("unexpected %s", joined)
	}
	ops := realReplaceOperations{}
	_, err := ops.PostChecks(context.Background(), spec, replacePreflight{EtcdMembers: 4})
	require.NoError(t, err)
	_, err = ops.PostChecks(context.Background(), spec, replacePreflight{EtcdMembers: 5})
	require.ErrorContains(t, err, "expected 5")
	osImage = "Flatcar Container Linux by Kinvolk 4459.2.1"
	_, err = ops.PostChecks(context.Background(), spec, replacePreflight{EtcdMembers: 4})
	require.ErrorContains(t, err, "expected Fedora CoreOS")
	osImage = "Fedora CoreOS 44"
	ciliumReady = "False"
	_, err = ops.PostChecks(context.Background(), spec, replacePreflight{EtcdMembers: 4})
	require.ErrorContains(t, err, "no Ready cilium agent")
}

func TestReplaceRemoveFromClusterRetriesThroughVIPFailover(t *testing.T) {
	cfg := testReplaceConfig()
	swapReplaceConfig(t, cfg)
	swapRehearseRuntime(t)
	oldRetries, oldDelay := replaceAPIRetries, replaceAPIRetryDelay
	t.Cleanup(func() { replaceAPIRetries, replaceAPIRetryDelay = oldRetries, oldDelay })
	replaceAPIRetries, replaceAPIRetryDelay = 5, time.Millisecond
	spec := testReplaceSpec(t, "k8s-2")
	deletes := 0
	rehearseCommandFn = func(_ context.Context, _ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "member list") {
			return `{"members":[]}`, nil
		}
		if strings.HasPrefix(joined, "delete node k8s-2") {
			deletes++
			if deletes < 3 {
				return "", fmt.Errorf("Unable to connect to the server: connection reset by peer")
			}
			return "", nil
		}
		return "", fmt.Errorf("unexpected %s", joined)
	}
	require.NoError(t, (realReplaceOperations{}).RemoveFromCluster(context.Background(), spec))
	assert.Equal(t, 3, deletes, "the node delete is retried until the API answers again")

	deletes = -100 // never succeeds within the retry budget
	require.ErrorContains(t, (realReplaceOperations{}).RemoveFromCluster(context.Background(), spec), "delete node")
}
