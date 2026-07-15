package kubernetes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/testutil"
)

type fakeEtcdUploadClient struct {
	commands  []string
	uploads   [][2]string
	responses map[string]string
	errors    map[string]error
	connected bool
	closed    bool
}

func (f *fakeEtcdUploadClient) Connect() error { f.connected = true; return nil }
func (f *fakeEtcdUploadClient) Close() error   { f.closed = true; return nil }
func (f *fakeEtcdUploadClient) ExecuteCommand(command string) (string, error) {
	f.commands = append(f.commands, command)
	return f.responses[command], f.errors[command]
}
func (f *fakeEtcdUploadClient) UploadFile(_ context.Context, localPath, remotePath string) error {
	f.uploads = append(f.uploads, [2]string{localPath, remotePath})
	return nil
}

func configureEtcdUpload(t *testing.T, client *fakeEtcdUploadClient) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "etcd-snapshot-k8s-0-20260715T010203Z.db")
	require.NoError(t, os.WriteFile(path, []byte("snapshot"), 0o600))
	require.NoError(t, os.WriteFile(path+".sha256", []byte("abc  snapshot\n"), 0o600))
	restore := config.SetForTesting(&config.Config{
		Hypervisors: config.HypervisorsConfig{TrueNAS: config.TrueNASConfig{SSHUser: "backup"}},
		State: config.StateConfig{EtcdBackup: config.EtcdBackupConfig{Upload: config.EtcdBackupUploadConfig{
			HostRef: config.KeyTrueNASHost, SSHUser: "backup", SSHPort: 2222, SSHKey: "~/.ssh/keys/nas01-ssh", Dir: "/mnt/tank/etcd", Keep: 2,
		}}},
		Secrets: map[string]string{config.KeyTrueNASHost: "literal://nas.example"},
	})
	t.Cleanup(restore)
	testutil.Swap(t, &etcdNewUploadClientFn, func(cfg ssh.SSHConfig) etcdUploadClient {
		assert.Equal(t, ssh.SSHConfig{Host: "nas.example", Username: "backup", Port: "2222", KeyPath: "~/.ssh/keys/nas01-ssh"}, cfg)
		return client
	})
	return path
}

func TestUploadEtcdSnapshotConstructsCommandsAndUploadsBothFiles(t *testing.T) {
	client := &fakeEtcdUploadClient{responses: map[string]string{}, errors: map[string]error{}}
	path := configureEtcdUpload(t, client)
	remote := "/mnt/tank/etcd/" + filepath.Base(path)
	client.responses[etcdRemoteHashCommand(remote)] = "abc  " + remote + "\n"
	client.responses[etcdRemoteListCommand("/mnt/tank/etcd")] = filepath.Base(path) + "\t8\t1786755723.0000000000\n"

	result, err := uploadEtcdSnapshot(context.Background(), path, "abc")
	require.NoError(t, err)
	assert.Equal(t, remote, result.Path)
	assert.Equal(t, "nas.example", result.Host)
	assert.True(t, client.connected)
	assert.True(t, client.closed)
	assert.Equal(t, [][2]string{{path, remote}, {path + ".sha256", remote + ".sha256"}}, client.uploads)
	assert.Equal(t, []string{
		"sudo mkdir -p -- '/mnt/tank/etcd'",
		"sudo sha256sum -- '" + remote + "'",
		etcdRemoteListCommand("/mnt/tank/etcd"),
	}, client.commands)
}

func TestUploadEtcdSnapshotHashMismatchRemovesRemotePair(t *testing.T) {
	client := &fakeEtcdUploadClient{responses: map[string]string{}, errors: map[string]error{}}
	path := configureEtcdUpload(t, client)
	remote := "/mnt/tank/etcd/" + filepath.Base(path)
	client.responses[etcdRemoteHashCommand(remote)] = "def  " + remote

	_, err := uploadEtcdSnapshot(context.Background(), path, "abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash mismatch")
	assert.Contains(t, err.Error(), "bad copy removed")
	assert.Equal(t, etcdRemoteRemoveCommand(remote, remote+".sha256"), client.commands[len(client.commands)-1])
}

func TestRemoteEtcdPruneNeverTouchesUnknownFiles(t *testing.T) {
	listing := strings.Join([]string{
		"etcd-snapshot-k8s-0-20260715T030000Z.db\t30\t300.0",
		"notes.db\t20\t250.0",
		"etcd-snapshot-k8s-0-not-a-time.db\t20\t200.0",
		"etcd-snapshot-k8s-0-20260715T020000Z.db.sha256\t20\t200.0",
		"etcd-snapshot-k8s-0-20260715T020000Z.db\t20\t200.0",
		"etcd-snapshot-k8s-0-20260715T010000Z.db\t10\t100.0",
	}, "\n")
	snapshots := parseRemoteEtcdSnapshotListing(listing)
	require.Len(t, snapshots, 3)
	client := &fakeEtcdUploadClient{responses: map[string]string{}, errors: map[string]error{}}

	pruned, err := pruneRemoteEtcdSnapshots(client, "/backup", snapshots, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, pruned)
	assert.Equal(t, []string{etcdRemoteRemoveCommand(
		"/backup/etcd-snapshot-k8s-0-20260715T010000Z.db",
		"/backup/etcd-snapshot-k8s-0-20260715T010000Z.db.sha256",
	)}, client.commands)
	assert.NotContains(t, strings.Join(client.commands, "\n"), "notes.db")
}

func TestInspectRemoteEtcdBackupsParsesLatestForStatus(t *testing.T) {
	client := &fakeEtcdUploadClient{responses: map[string]string{}, errors: map[string]error{}}
	configureEtcdUpload(t, client)
	now := time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC)
	testutil.Swap(t, &etcdNowFn, func() time.Time { return now })
	client.responses[etcdRemoteListCommand("/mnt/tank/etcd")] = strings.Join([]string{
		"unknown.txt\t999\t1784088000.0",
		"etcd-snapshot-k8s-0-20260715T010000Z.db\t1024\t" + formatUnixFloat(now.Add(-3*time.Hour)),
		"etcd-snapshot-k8s-0-20260714T010000Z.db\t512\t" + formatUnixFloat(now.Add(-27*time.Hour)),
	}, "\n")

	inventory := inspectRemoteEtcdBackups(context.Background(), 24*time.Hour)
	assert.Equal(t, "OK", inventory.Status)
	assert.Equal(t, "etcd-snapshot-k8s-0-20260715T010000Z.db", inventory.Path)
	assert.Equal(t, "3h0m0s", inventory.Age)
	assert.Equal(t, int64(1024), inventory.Size)
	report := etcdStatusReport{Remote: &inventory}
	rendered, err := renderEtcdStatus(report, "table")
	require.NoError(t, err)
	assert.Contains(t, rendered, "Remote backup")
	assert.Contains(t, rendered, inventory.Path)
}

func TestShouldUploadEtcdBackupFlagOrAuto(t *testing.T) {
	for _, tc := range []struct {
		flag, auto, want bool
	}{{false, false, false}, {true, false, true}, {false, true, true}, {true, true, true}} {
		assert.Equal(t, tc.want, shouldUploadEtcdBackup(tc.flag, tc.auto))
	}
}

func TestInspectRemoteEtcdBackupsReportsListingFailureAsWarning(t *testing.T) {
	client := &fakeEtcdUploadClient{responses: map[string]string{}, errors: map[string]error{}}
	configureEtcdUpload(t, client)
	client.errors[etcdRemoteListCommand("/mnt/tank/etcd")] = errors.New("offline")
	inventory := inspectRemoteEtcdBackups(context.Background(), time.Hour)
	assert.Equal(t, "WARN", inventory.Status)
	assert.Contains(t, inventory.Detail, "offline")
}

func formatUnixFloat(value time.Time) string {
	return strconv.FormatFloat(float64(value.Unix()), 'f', 1, 64)
}
