package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"
)

type fakeEtcdRestoreClient struct {
	name       string
	connectErr error
	closeCalls int
	operations *[]string
	failMatch  string
}

func (f *fakeEtcdRestoreClient) Connect() error {
	*f.operations = append(*f.operations, f.name+":connect")
	return f.connectErr
}

func (f *fakeEtcdRestoreClient) Close() error {
	f.closeCalls++
	*f.operations = append(*f.operations, f.name+":close")
	return nil
}

func (f *fakeEtcdRestoreClient) ExecuteCommand(command string) (string, error) {
	operation := restoreOperationName(command)
	*f.operations = append(*f.operations, f.name+":"+operation)
	if operation == f.failMatch {
		return "", errors.New("injected " + operation + " failure")
	}
	switch operation {
	case "manifest", "parked-manifest":
		return "apiVersion: v1\nspec:\n  containers:\n    - name: etcd\n      image: registry.k8s.io/etcd:3.6.4-0\n", nil
	case "health":
		return `{"members":[{"ID":1,"name":"k8s-0","peerURLs":["https://10.0.0.10:2380"],"clientURLs":["https://10.0.0.10:2379"]}]}`, nil
	case "revision":
		return `[{"Status":{"header":{"revision":4200}}}]`, nil
	default:
		return "", nil
	}
}

func (f *fakeEtcdRestoreClient) StreamCommand(context.Context, string, io.Writer) error {
	return errors.New("unexpected download stream")
}

func (f *fakeEtcdRestoreClient) UploadFile(_ context.Context, _, _ string) error {
	*f.operations = append(*f.operations, f.name+":upload")
	if f.failMatch == "upload" {
		return errors.New("injected upload failure")
	}
	return nil
}

func restoreOperationName(command string) string {
	switch {
	case strings.HasPrefix(command, "sudo cat") && strings.Contains(command, "homeops-etcd-restore"):
		return "parked-manifest"
	case strings.HasPrefix(command, "sudo cat"):
		return "manifest"
	case strings.Contains(command, "if [ -f") && strings.Contains(command, "kube-apiserver"):
		return "safety-park"
	case strings.Contains(command, "mkdir -p") && strings.Contains(command, "kube-apiserver"):
		return "park"
	case strings.Contains(command, "member.pre-homeops"):
		return "preserve"
	case strings.Contains(command, "ctr -n k8s.io run"):
		return "ctr"
	case strings.Contains(command, "timed out waiting for etcd container to start"):
		return "etcd-manifest"
	case strings.Contains(command, "member list"):
		return "health"
	case strings.Contains(command, "kube-apiserver /readyz"):
		return "apiserver"
	case strings.Contains(command, "endpoint status"):
		return "revision"
	default:
		return "unknown"
	}
}

func setEtcdRestoreTestConfig(t *testing.T, nodes []config.Node) {
	t.Helper()
	restore := config.SetForTesting(&config.Config{
		Cluster: config.ClusterConfig{Name: "fugu", NodeSSHPort: 22, Nodes: nodes},
		Secrets: map[string]string{config.KeyNodeSSHUser: "literal://core"},
	})
	t.Cleanup(restore)
}

func writeRestoreSnapshot(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	sum := sha256.Sum256(content)
	require.NoError(t, os.WriteFile(path+".sha256", []byte(hex.EncodeToString(sum[:])+"  snapshot.db\n"), 0o600))
	return path
}

func TestRenderEtcdRestorePlanIsCompleteAndNonMutating(t *testing.T) {
	setEtcdRestoreTestConfig(t, []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}, {Name: "k8s-1", IP: "10.0.0.11"}})
	now := time.Date(2026, 7, 15, 12, 34, 56, 0, time.UTC)
	plan, err := buildEtcdRestorePlan("./backup.db", now)
	require.NoError(t, err)

	rendered := renderEtcdRestorePlan(plan)
	assert.Contains(t, rendered, "NO CHANGES MADE WITHOUT --execute")
	assert.Contains(t, rendered, "k8s-0=https://10.0.0.10:2380,k8s-1=https://10.0.0.11:2380")
	assert.Contains(t, rendered, "/etc/kubernetes/homeops-etcd-restore-20260715T123456Z")
	assert.Contains(t, rendered, "ctr -n k8s.io run")
	assert.Contains(t, rendered, "kube-apiserver /readyz")

	clientCreated := false
	testutil.Swap(t, &etcdRestoreNewNodeClientFn, func(ssh.SSHConfig) etcdRestoreNodeClient {
		clientCreated = true
		return nil
	})
	cmd := newEtcdRestoreCommand()
	cmd.SetArgs([]string{"./backup.db"})
	cmd.SetOut(new(bytes.Buffer))
	require.NoError(t, cmd.Execute())
	assert.False(t, clientCreated, "plan-only mode must not open SSH or mutate anything")
}

func TestBuildEtcdInitialClusterPreservesConfiguredOrder(t *testing.T) {
	nodes := []config.Node{{Name: "cp-a", IP: "192.0.2.10"}, {Name: "cp-b", IP: "192.0.2.11"}, {Name: "cp-c", IP: "192.0.2.12"}}
	assert.Equal(t, "cp-a=https://192.0.2.10:2380,cp-b=https://192.0.2.11:2380,cp-c=https://192.0.2.12:2380", buildEtcdInitialCluster(nodes))
}

func TestPreflightEtcdRestoreRejectsMissingSnapshot(t *testing.T) {
	setEtcdRestoreTestConfig(t, []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}})
	plan, err := buildEtcdRestorePlan(filepath.Join(t.TempDir(), "missing.db"), time.Now())
	require.NoError(t, err)

	_, err = preflightEtcdRestore(context.Background(), plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot file")
}

func TestPreflightEtcdRestoreRejectsBadChecksumBeforeSSH(t *testing.T) {
	setEtcdRestoreTestConfig(t, []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}})
	path := writeRestoreSnapshot(t, []byte("snapshot"))
	require.NoError(t, os.WriteFile(path+".sha256", []byte(strings.Repeat("0", 64)+"  snapshot.db\n"), 0o600))
	plan, err := buildEtcdRestorePlan(path, time.Now())
	require.NoError(t, err)
	created := false
	testutil.Swap(t, &etcdRestoreNewNodeClientFn, func(ssh.SSHConfig) etcdRestoreNodeClient { created = true; return nil })

	_, err = preflightEtcdRestore(context.Background(), plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.False(t, created)
}

func TestPreflightEtcdRestoreRejectsUnreachableNodeAndClosesEarlierConnections(t *testing.T) {
	nodes := []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}, {Name: "k8s-1", IP: "10.0.0.11"}}
	setEtcdRestoreTestConfig(t, nodes)
	path := writeRestoreSnapshot(t, []byte("snapshot"))
	plan, err := buildEtcdRestorePlan(path, time.Now())
	require.NoError(t, err)
	var operations []string
	clients := []*fakeEtcdRestoreClient{
		{name: "k8s-0", operations: &operations},
		{name: "k8s-1", operations: &operations, connectErr: errors.New("host down")},
	}
	index := 0
	testutil.Swap(t, &etcdRestoreNewNodeClientFn, func(ssh.SSHConfig) etcdRestoreNodeClient {
		client := clients[index]
		index++
		return client
	})

	_, err = preflightEtcdRestore(context.Background(), plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "k8s-1")
	assert.Contains(t, err.Error(), "not SSH-reachable")
	assert.Equal(t, 1, clients[0].closeCalls)
}

func TestExecuteEtcdRestoreSequencesAndAbortsAtEveryPhase(t *testing.T) {
	setEtcdRestoreTestConfig(t, []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}})
	path := writeRestoreSnapshot(t, []byte("snapshot"))
	plan, err := buildEtcdRestorePlan(path, time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	phases := []string{"park", "parked-manifest", "preserve", "upload", "ctr", "etcd-manifest", "health", "apiserver", "revision"}
	for i, failPhase := range phases {
		t.Run(failPhase, func(t *testing.T) {
			var operations []string
			client := &fakeEtcdRestoreClient{name: "k8s-0", operations: &operations, failMatch: failPhase}
			preflight := etcdRestorePreflight{Plan: plan, Nodes: []etcdRestoreConnectedNode{{Node: plan.Nodes[0], Client: client, ImageRef: "registry.k8s.io/etcd:3.6.4-0"}}}

			_, err := executeEtcdRestore(context.Background(), preflight, io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), failPhase)
			require.Len(t, operations, i+1)
			assert.Equal(t, "k8s-0:"+failPhase, operations[i])
		})
	}
}

func TestExecuteEtcdRestoreSuccessReportsMembersAndRevision(t *testing.T) {
	setEtcdRestoreTestConfig(t, []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}})
	path := writeRestoreSnapshot(t, []byte("snapshot"))
	plan, err := buildEtcdRestorePlan(path, time.Now())
	require.NoError(t, err)
	var operations []string
	client := &fakeEtcdRestoreClient{name: "k8s-0", operations: &operations}
	preflight := etcdRestorePreflight{Plan: plan, Nodes: []etcdRestoreConnectedNode{{Node: plan.Nodes[0], Client: client, ImageRef: "registry.k8s.io/etcd:3.6.4-0"}}}

	result, err := executeEtcdRestore(context.Background(), preflight, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int64(4200), result.Revision)
	require.Len(t, result.Members, 1)
	assert.Equal(t, "k8s-0", result.Members[0].Name)
	assert.Equal(t, []string{"k8s-0:park", "k8s-0:parked-manifest", "k8s-0:preserve", "k8s-0:upload", "k8s-0:ctr", "k8s-0:etcd-manifest", "k8s-0:health", "k8s-0:apiserver", "k8s-0:revision"}, operations)
}

func TestExtractEtcdImageRefFromManifestFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "etcd-manifest.yaml"))
	require.NoError(t, err)
	image, err := extractEtcdImageRef(raw)
	require.NoError(t, err)
	assert.Equal(t, "registry.k8s.io/etcd:3.6.4-0", image)

	_, err = extractEtcdImageRef([]byte("spec:\n  containers:\n    - name: sidecar\n      image: example.invalid/sidecar:1\n"))
	require.Error(t, err)
}

func TestEtcdRestoreRequiresExactClusterNameUnlessExecuteYes(t *testing.T) {
	setEtcdRestoreTestConfig(t, []config.Node{{Name: "k8s-0", IP: "10.0.0.10"}})
	path := writeRestoreSnapshot(t, []byte("snapshot"))
	var operations []string
	client := &fakeEtcdRestoreClient{name: "k8s-0", operations: &operations}
	testutil.Swap(t, &etcdRestoreNewNodeClientFn, func(ssh.SSHConfig) etcdRestoreNodeClient { return client })
	testutil.Swap(t, &etcdRestoreSnapshotStatusFn, func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"hash":1,"revision":4000,"totalKey":5,"totalSize":8}`), nil
	})
	testutil.Swap(t, &etcdRestoreCurrentRevisionFn, func(context.Context) (int64, error) { return 5000, nil })
	testutil.Swap(t, &etcdRestoreInputFn, func(string, string) (string, error) { return "wrong", nil })
	testutil.Swap(t, &etcdRestoreAssumeYesFn, func(*cobra.Command) bool { return false })

	cmd := newEtcdRestoreCommand()
	cmd.SetArgs([]string{path, "--execute"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not exactly match")
	assert.NotContains(t, operations, "k8s-0:park")
}

func TestParseEtcdEndpointRevision(t *testing.T) {
	revision, err := parseEtcdEndpointRevision([]byte(`[{"Status":{"header":{"revision":17}}},{"Status":{"header":{"revision":19}}}]`))
	require.NoError(t, err)
	assert.Equal(t, int64(19), revision)
	_, err = parseEtcdEndpointRevision([]byte(`[]`))
	require.Error(t, err)
}

func TestVerifyEtcdRestoreChecksumRejectsEmptySidecar(t *testing.T) {
	path := writeRestoreSnapshot(t, []byte("snapshot"))
	require.NoError(t, os.WriteFile(path+".sha256", nil, 0o600))
	err := verifyEtcdRestoreChecksum(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain")
}

func TestVerifyLocalEtcdRestoreSnapshotUsesLocalEtcdutl(t *testing.T) {
	testutil.Swap(t, &etcdRestoreLookPathFn, func(name string) (string, error) {
		assert.Equal(t, "etcdutl", name)
		return "/usr/bin/etcdutl", nil
	})
	testutil.Swap(t, &etcdRestoreRunCommandFn, func(_ context.Context, opts common.CommandOptions) (common.CommandResult, error) {
		assert.Equal(t, "etcdutl", opts.Name)
		assert.Equal(t, []string{"snapshot", "status", "/tmp/snapshot.db", "-w", "json"}, opts.Args)
		return common.CommandResult{Stdout: `{"hash":1,"revision":2,"totalKey":3,"totalSize":4}`}, nil
	})

	raw, err := verifyLocalEtcdRestoreSnapshot(context.Background(), "/tmp/snapshot.db", "ignored")
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"revision":2`)
}

func TestVerifyLocalEtcdRestoreSnapshotFallsBackToContainerEntrypoint(t *testing.T) {
	testutil.Swap(t, &etcdRestoreLookPathFn, func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not installed")
	})
	testutil.Swap(t, &etcdRestoreRunCommandFn, func(_ context.Context, opts common.CommandOptions) (common.CommandResult, error) {
		assert.Equal(t, "docker", opts.Name)
		assert.Equal(t, []string{"run", "--rm", "--entrypoint", "etcdutl", "-v", "/tmp/snapshot.db:/snapshot.db:ro", "registry.k8s.io/etcd:3.6.4-0", "snapshot", "status", "/snapshot.db", "-w", "json"}, opts.Args)
		return common.CommandResult{Stdout: `{"hash":1,"revision":2,"totalKey":3,"totalSize":4}`}, nil
	})

	_, err := verifyLocalEtcdRestoreSnapshot(context.Background(), "/tmp/snapshot.db", "registry.k8s.io/etcd:3.6.4-0")
	require.NoError(t, err)
}

func TestEtcdRestoreCtrCommandContainsRequiredTopology(t *testing.T) {
	plan := etcdRestorePlan{
		Timestamp: "20260715T010203Z", RemoteSnapshot: "/var/lib/etcd/snapshot.db",
		InitialCluster: "cp-a=https://10.0.0.1:2380,cp-b=https://10.0.0.2:2380", ClusterToken: "restored-20260715T010203Z",
	}
	command := etcdRestoreCtrCommand(plan, config.Node{Name: "cp-a", IP: "10.0.0.1"}, "registry.k8s.io/etcd:3.6.4-0")
	for _, expected := range []string{"ctr -n k8s.io run --rm", "src=/var/lib/etcd,dst=/var/lib/etcd", "etcdutl snapshot restore", "--name 'cp-a'", "--initial-cluster 'cp-a=https://10.0.0.1:2380,cp-b=https://10.0.0.2:2380'", "--initial-advertise-peer-urls 'https://10.0.0.1:2380'"} {
		assert.Contains(t, command, expected, fmt.Sprintf("missing %s", expected))
	}
}

func TestEnforceEtcdRestoreParkedStateTouchesOnlyManifests(t *testing.T) {
	var operations []string
	clients := []etcdRestoreConnectedNode{
		{Node: config.Node{Name: "k8s-0"}, Client: &fakeEtcdRestoreClient{name: "k8s-0", operations: &operations}},
		{Node: config.Node{Name: "k8s-1"}, Client: &fakeEtcdRestoreClient{name: "k8s-1", operations: &operations}},
	}
	plan := etcdRestorePlan{HoldingDir: "/etc/kubernetes/homeops-etcd-restore-test"}

	require.NoError(t, enforceEtcdRestoreParkedState(clients, plan, io.Discard))
	assert.Equal(t, []string{"k8s-0:safety-park", "k8s-1:safety-park"}, operations)
}

func TestPreflightDegradesWhenNoLocalVerifier(t *testing.T) {
	preflight := etcdRestorePreflight{
		SnapshotVerifySkipped: true,
		CurrentRevision:       100,
		CurrentRevisionKnown:  true,
	}
	rendered := renderEtcdRestorePreflight(preflight)
	assert.Contains(t, rendered, "etcdutl verification skipped")
	assert.Contains(t, rendered, "sha256 sidecar verified")
	assert.Contains(t, rendered, "rollback delta cannot be computed")
}
