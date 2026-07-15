package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"
)

const healthyEtcdPodJSON = `{"items":[{"metadata":{"name":"etcd-k8s-0","labels":{"component":"etcd"}},"spec":{"nodeName":"k8s-0"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`

type fakeEtcdNodeClient struct {
	connectErr     error
	closeErr       error
	streamErr      error
	cleanupErr     error
	streamData     []byte
	streamCommands []string
	commands       []string
	connected      bool
	closed         bool
	cleaned        bool
}

func (f *fakeEtcdNodeClient) Connect() error {
	f.connected = true
	return f.connectErr
}

func (f *fakeEtcdNodeClient) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeEtcdNodeClient) ExecuteCommand(command string) (string, error) {
	f.commands = append(f.commands, command)
	if command == "sudo rm -f '/var/lib/etcd/homeops-etcd-snapshot.db'" {
		f.cleaned = true
		return "", f.cleanupErr
	}
	return "", errors.New("unexpected node command")
}

func (f *fakeEtcdNodeClient) StreamCommand(_ context.Context, command string, stdout io.Writer) error {
	f.streamCommands = append(f.streamCommands, command)
	for offset := 0; offset < len(f.streamData); {
		end := offset + 3
		if end > len(f.streamData) {
			end = len(f.streamData)
		}
		n, err := stdout.Write(f.streamData[offset:end])
		offset += n
		if err != nil {
			return err
		}
	}
	return f.streamErr
}

func fakeEtcdPodList(t *testing.T) {
	t.Helper()
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "get pods") {
			return []byte(healthyEtcdPodJSON), nil
		}
		return nil, errors.New("unexpected kubectl call")
	})
}

func configureEtcdNodeClient(t *testing.T, client *fakeEtcdNodeClient) {
	t.Helper()
	restore := config.SetForTesting(&config.Config{
		Secrets: map[string]string{config.KeyNodeSSHUser: "literal://core"},
	})
	t.Cleanup(restore)
	testutil.Swap(t, &etcdNewNodeClientFn, func(cfg ssh.SSHConfig) etcdNodeClient {
		assert.Equal(t, ssh.SSHConfig{Host: "192.168.122.10", Username: "core", Port: "22"}, cfg)
		return client
	})
}

func TestRunEtcdBackupStreamsFromNodeChecksumsAndPrunes(t *testing.T) {
	fakeEtcdPodList(t)
	dir := t.TempDir()
	oldest := filepath.Join(dir, "etcd-snapshot-k8s-1-20260710T120000Z.db")
	newer := filepath.Join(dir, "etcd-snapshot-k8s-2-20260711T120000Z.db")
	for i, path := range []string{oldest, newer} {
		require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
		require.NoError(t, os.WriteFile(path+".sha256", []byte("sum"), 0o600))
		require.NoError(t, os.Chtimes(path, time.Unix(int64(i+1), 0), time.Unix(int64(i+1), 0)))
	}

	snapshot := []byte("binary\x00snapshot\xff")
	client := &fakeEtcdNodeClient{streamData: snapshot}
	configureEtcdNodeClient(t, client)
	var execCalls [][]string
	testutil.Swap(t, &etcdPodExecFn, func(_ context.Context, pod string, command ...string) ([]byte, error) {
		require.Equal(t, "etcd-k8s-0", pod)
		execCalls = append(execCalls, append([]string(nil), command...))
		switch command[0] {
		case "etcdctl":
			return []byte("Snapshot saved"), nil
		case "etcdutl":
			return []byte(`{"hash":12,"revision":4242,"totalKey":99,"totalSize":1024}`), nil
		default:
			return nil, errors.New("unexpected pod exec")
		}
	})
	fixedNow := time.Date(2026, 7, 14, 12, 34, 56, 0, time.UTC)
	testutil.Swap(t, &etcdNowFn, func() time.Time { return fixedNow })

	result, err := runEtcdBackup(context.Background(), dir, 2)
	require.NoError(t, err)
	assert.Equal(t, uint64(12), result.Hash)
	assert.Equal(t, int64(4242), result.Revision)
	assert.Equal(t, int64(99), result.TotalKeys)
	assert.Equal(t, int64(1024), result.TotalSize)
	assert.Equal(t, 1, result.Pruned)
	assert.Equal(t, "etcdutl", result.StatusTool)
	assert.Equal(t, filepath.Join(dir, "etcd-snapshot-k8s-0-20260714T123456Z.db"), result.Path)
	assert.True(t, client.connected)
	assert.True(t, client.cleaned)
	assert.True(t, client.closed)
	assert.Equal(t, []string{"sudo cat '/var/lib/etcd/homeops-etcd-snapshot.db'"}, client.streamCommands)

	written, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	assert.Equal(t, snapshot, written)
	sum := sha256.Sum256(snapshot)
	digest := hex.EncodeToString(sum[:])
	assert.Equal(t, digest, result.SHA256)
	sidecar, err := os.ReadFile(result.Path + ".sha256")
	require.NoError(t, err)
	assert.Equal(t, digest+"  "+filepath.Base(result.Path)+"\n", string(sidecar))
	assert.NoFileExists(t, oldest)
	assert.NoFileExists(t, oldest+".sha256")
	assert.FileExists(t, newer)

	require.Len(t, execCalls, 2)
	assert.Equal(t, etcdctlCommand("snapshot", "save", etcdSnapshotRemotePath), execCalls[0])
	assert.Equal(t, []string{"etcdutl", "snapshot", "status", etcdSnapshotRemotePath, "-w", "json"}, execCalls[1])
}

func TestRunEtcdBackupFetchFailureRemovesPartialFileAndStillCleansNode(t *testing.T) {
	fakeEtcdPodList(t)
	client := &fakeEtcdNodeClient{
		streamData: []byte("partial"),
		streamErr:  errors.New("SSH stream broke"),
		cleanupErr: errors.New("cleanup failed"),
	}
	configureEtcdNodeClient(t, client)
	testutil.Swap(t, &etcdPodExecFn, func(_ context.Context, _ string, command ...string) ([]byte, error) {
		if command[0] == "etcdctl" {
			return nil, nil
		}
		return []byte(`{"hash":1,"revision":1,"totalKey":1,"totalSize":7}`), nil
	})
	var warnings []string
	testutil.Swap(t, &etcdWarnfFn, func(format string, args ...any) {
		warnings = append(warnings, format)
	})
	dir := t.TempDir()

	_, err := runEtcdBackup(context.Background(), dir, 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch etcd snapshot")
	assert.Contains(t, err.Error(), "SSH stream broke")
	assert.True(t, client.cleaned)
	assert.True(t, client.closed)
	assert.Contains(t, strings.Join(warnings, "\n"), "clean up etcd snapshot")
	paths, globErr := filepath.Glob(filepath.Join(dir, "etcd-snapshot-*.db"))
	require.NoError(t, globErr)
	assert.Empty(t, paths)
}

func TestRunEtcdBackupErrorsWhenPodNodeIsNotConfigured(t *testing.T) {
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"items":[{"metadata":{"name":"etcd-rogue","labels":{"component":"etcd"}},"spec":{"nodeName":"rogue"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`), nil
	})
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)
	clientCreated := false
	testutil.Swap(t, &etcdNewNodeClientFn, func(ssh.SSHConfig) etcdNodeClient {
		clientCreated = true
		return &fakeEtcdNodeClient{}
	})

	_, err := runEtcdBackup(context.Background(), t.TempDir(), 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `node "rogue", which is not present in cluster.nodes`)
	assert.False(t, clientCreated)
}

func TestVerifyEtcdSnapshotUsesEtcdutlDirectly(t *testing.T) {
	testutil.Swap(t, &etcdPodExecFn, func(_ context.Context, _ string, command ...string) ([]byte, error) {
		assert.Equal(t, []string{"etcdutl", "snapshot", "status", etcdSnapshotRemotePath, "-w", "json"}, command)
		return []byte(`{"hash":1,"revision":2,"totalKey":3,"totalSize":4}`), nil
	})
	out, err := verifyEtcdSnapshot(context.Background(), "etcd-k8s-0")
	require.NoError(t, err)
	assert.Contains(t, string(out), `"revision":2`)
}

func TestParseEtcdSnapshotStatusRequiresEtcdutlFields(t *testing.T) {
	status, err := parseEtcdSnapshotStatus([]byte(`{"hash":4294967295,"revision":42,"totalKey":9,"totalSize":2048}`))
	require.NoError(t, err)
	assert.Equal(t, etcdSnapshotStatus{Hash: 4294967295, Revision: 42, TotalKeys: 9, TotalSize: 2048}, status)

	_, err = parseEtcdSnapshotStatus([]byte(`{"revision":42,"totalKey":9,"totalSize":2048}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no hash")
}

func TestFindHealthyEtcdPodFallsBackToName(t *testing.T) {
	calls := 0
	testutil.Swap(t, &kubectlOutputCtxFn, func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"items":[]}`), nil
		}
		return []byte(`{"items":[{"metadata":{"name":"etcd-control-0"},"status":{"phase":"Running","containerStatuses":[{"ready":true}]}}]}`), nil
	})
	pod, err := findHealthyEtcdPod(context.Background())
	require.NoError(t, err)
	assert.Equal(t, etcdPod{Name: "etcd-control-0", Node: "control-0"}, pod)
	assert.Equal(t, 2, calls)
}

func TestBuildEtcdStatusUsesDirectEtcdctlArgvAndRendersJSON(t *testing.T) {
	fakeEtcdPodList(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "etcd-snapshot-k8s-0-20260710T000000Z.db")
	require.NoError(t, os.WriteFile(path, []byte("12345"), 0o600))
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, now.Add(-72*time.Hour), now.Add(-72*time.Hour)))
	testutil.Swap(t, &etcdNowFn, func() time.Time { return now })
	var execCalls [][]string
	testutil.Swap(t, &etcdPodExecFn, func(_ context.Context, _ string, command ...string) ([]byte, error) {
		execCalls = append(execCalls, append([]string(nil), command...))
		joined := strings.Join(command, " ")
		if strings.Contains(joined, "member list") {
			return []byte(`{"members":[{"ID":15,"name":"k8s-0","peerURLs":["https://10.0.0.1:2380"],"clientURLs":["https://10.0.0.1:2379"]}]}`), nil
		}
		if strings.Contains(joined, "endpoint health") {
			return []byte(`[{"endpoint":"https://10.0.0.1:2379","health":true,"took":"2ms"}]`), nil
		}
		return nil, errors.New("unexpected")
	})

	report, err := buildEtcdStatus(context.Background(), dir, 48*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "WARN", report.Backup.Status)
	assert.Equal(t, "f", report.Members[0].ID)
	assert.True(t, report.Endpoints[0].Healthy)
	rendered, err := renderEtcdStatus(report, "json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"summary":{"ok":1,"warn":1,"fail":0},"pod":"etcd-k8s-0","node":"k8s-0","members":[{"id":"f","name":"k8s-0","peer_urls":["https://10.0.0.1:2380"],"client_urls":["https://10.0.0.1:2379"],"learner":false}],"endpoints":[{"endpoint":"https://10.0.0.1:2379","healthy":true,"took":"2ms"}],"backup":{"status":"WARN","path":"`+path+`","age":"72h0m0s","size_bytes":5,"detail":"latest local snapshot is older than 48h0m0s"}}`, rendered)

	require.Len(t, execCalls, 2)
	for _, command := range execCalls {
		assert.Equal(t, "etcdctl", command[0])
		assert.Contains(t, command, "-w")
		assert.Contains(t, command, "json")
	}
}

func TestEtcdPodExecArgvNeverStartsWithUnsupportedDistrolessBinary(t *testing.T) {
	commands := [][]string{
		etcdctlCommand("snapshot", "save", etcdSnapshotRemotePath),
		{"etcdutl", "snapshot", "status", etcdSnapshotRemotePath, "-w", "json"},
		etcdctlCommand("member", "list", "-w", "json"),
		etcdctlCommand("endpoint", "health", "--cluster", "-w", "json"),
	}
	for _, command := range commands {
		require.NotEmpty(t, command)
		assert.NotContains(t, []string{"env", "sh", "base64"}, command[0])
		assert.Contains(t, []string{"etcdctl", "etcdutl"}, command[0])
	}
}

func TestInspectEtcdBackupsWarnsWhenNone(t *testing.T) {
	inventory, err := inspectEtcdBackups(t.TempDir(), 48*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "WARN", inventory.Status)
	assert.Contains(t, inventory.Detail, "no local")
}
