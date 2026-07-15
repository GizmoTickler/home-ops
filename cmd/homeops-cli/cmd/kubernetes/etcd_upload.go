package kubernetes

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
)

type etcdUploadClient interface {
	Connect() error
	Close() error
	ExecuteCommand(command string) (string, error)
	UploadFile(ctx context.Context, localPath, remotePath string) error
}

type remoteEtcdSnapshot struct {
	Name    string
	Size    int64
	ModTime time.Time
}

var (
	etcdSnapshotFilenameRE = regexp.MustCompile(`^etcd-snapshot-[A-Za-z0-9_-]+-(\d{8}T\d{6}Z)\.db$`)
	etcdNewUploadClientFn  = func(cfg ssh.SSHConfig) etcdUploadClient { return ssh.NewSSHClient(cfg) }
)

func shouldUploadEtcdBackup(flag, automatic bool) bool { return flag || automatic }

func uploadEtcdSnapshot(ctx context.Context, localPath, localHash string) (result etcdRemoteBackupResult, err error) {
	cfg := config.Get()
	upload := cfg.State.EtcdBackup.Upload
	host, err := cfg.ResolveSecret(upload.HostRef)
	if err != nil {
		return result, fmt.Errorf("resolve etcd backup upload host %q: %w", upload.HostRef, err)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return result, fmt.Errorf("resolved etcd backup upload host %q is empty", upload.HostRef)
	}
	if strings.TrimSpace(upload.SSHUser) == "" {
		return result, fmt.Errorf("state.etcd_backup.upload.ssh_user is empty")
	}
	if upload.Keep < 1 {
		return result, fmt.Errorf("state.etcd_backup.upload.keep must be at least 1")
	}

	client := etcdNewUploadClientFn(ssh.SSHConfig{Host: host, Username: upload.SSHUser, Port: "22", KeyPath: upload.SSHKey})
	if err := client.Connect(); err != nil {
		return result, fmt.Errorf("connect to etcd backup host %s over SSH: %w", host, err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			etcdWarnfFn("Failed to close SSH connection to etcd backup host %s: %v", host, closeErr)
		}
	}()

	remotePath := filepath.Join(upload.Dir, filepath.Base(localPath))
	remoteChecksum := remotePath + ".sha256"
	if _, err := client.ExecuteCommand(etcdRemoteMkdirCommand(upload.Dir)); err != nil {
		return result, fmt.Errorf("create remote etcd backup directory %s: %w", upload.Dir, err)
	}
	if err := client.UploadFile(ctx, localPath, remotePath); err != nil {
		return result, fmt.Errorf("upload etcd snapshot to %s: %w", remotePath, err)
	}
	if err := client.UploadFile(ctx, localPath+".sha256", remoteChecksum); err != nil {
		_, _ = client.ExecuteCommand(etcdRemoteRemoveCommand(remotePath, remoteChecksum))
		return result, fmt.Errorf("upload etcd snapshot checksum to %s: %w", remoteChecksum, err)
	}
	remoteHashRaw, err := client.ExecuteCommand(etcdRemoteHashCommand(remotePath))
	if err != nil {
		return result, fmt.Errorf("verify remote etcd snapshot %s: %w", remotePath, err)
	}
	remoteHash := strings.Fields(remoteHashRaw)
	if len(remoteHash) == 0 || !strings.EqualFold(remoteHash[0], localHash) {
		_, removeErr := client.ExecuteCommand(etcdRemoteRemoveCommand(remotePath, remoteChecksum))
		if removeErr != nil {
			return result, fmt.Errorf("remote etcd snapshot hash mismatch for %s (local %s, remote %q); removing bad copy: %v", remotePath, localHash, strings.TrimSpace(remoteHashRaw), removeErr)
		}
		return result, fmt.Errorf("remote etcd snapshot hash mismatch for %s (local %s, remote %q); bad copy removed", remotePath, localHash, strings.TrimSpace(remoteHashRaw))
	}
	listing, err := client.ExecuteCommand(etcdRemoteListCommand(upload.Dir))
	if err != nil {
		return result, fmt.Errorf("list remote etcd snapshots in %s: %w", upload.Dir, err)
	}
	pruned, err := pruneRemoteEtcdSnapshots(client, upload.Dir, parseRemoteEtcdSnapshotListing(listing), upload.Keep)
	if err != nil {
		return result, err
	}
	return etcdRemoteBackupResult{Host: host, Path: remotePath, Pruned: pruned}, nil
}

func inspectRemoteEtcdBackups(ctx context.Context, staleAfter time.Duration) etcdBackupInventory {
	cfg := config.Get()
	upload := cfg.State.EtcdBackup.Upload
	host, err := cfg.ResolveSecret(upload.HostRef)
	if err != nil {
		return etcdBackupInventory{Status: "WARN", Detail: fmt.Sprintf("remote backup host is unavailable: %v", err)}
	}
	client := etcdNewUploadClientFn(ssh.SSHConfig{Host: strings.TrimSpace(host), Username: upload.SSHUser, Port: "22", KeyPath: upload.SSHKey})
	if err := client.Connect(); err != nil {
		return etcdBackupInventory{Status: "WARN", Detail: fmt.Sprintf("remote backup SSH failed: %v", err)}
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			etcdWarnfFn("Failed to close SSH connection to etcd backup host %s: %v", host, closeErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return etcdBackupInventory{Status: "WARN", Detail: "remote backup inventory canceled: " + err.Error()}
	}
	listing, err := client.ExecuteCommand(etcdRemoteListCommand(upload.Dir))
	if err != nil {
		return etcdBackupInventory{Status: "WARN", Detail: fmt.Sprintf("remote backup listing failed: %v", err)}
	}
	snapshots := parseRemoteEtcdSnapshotListing(listing)
	if len(snapshots) == 0 {
		return etcdBackupInventory{Status: "WARN", Detail: "no remote etcd snapshot found"}
	}
	latest := snapshots[0]
	age := etcdNowFn().Sub(latest.ModTime)
	if age < 0 {
		age = 0
	}
	status, detail := "OK", "latest remote snapshot is fresh"
	if age > staleAfter {
		status, detail = "WARN", "latest remote snapshot is older than "+staleAfter.String()
	}
	return etcdBackupInventory{Status: status, Path: latest.Name, Age: age.Round(time.Second).String(), Size: latest.Size, Detail: detail}
}

func etcdRemoteMkdirCommand(dir string) string {
	return "sudo mkdir -p -- " + common.ShellQuote(dir)
}

func etcdRemoteHashCommand(path string) string {
	return "sudo sha256sum -- " + common.ShellQuote(path)
}

func etcdRemoteListCommand(dir string) string {
	quoted := common.ShellQuote(dir)
	return fmt.Sprintf("if [ -d %s ]; then sudo find %s -maxdepth 1 -type f -printf '%%f\\t%%s\\t%%T@\\n'; fi", quoted, quoted)
}

func etcdRemoteRemoveCommand(paths ...string) string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, common.ShellQuote(path))
	}
	return "sudo rm -f -- " + strings.Join(quoted, " ")
}

func parseRemoteEtcdSnapshotListing(output string) []remoteEtcdSnapshot {
	var snapshots []remoteEtcdSnapshot
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 3 || !etcdSnapshotFilenameRE.MatchString(fields[0]) {
			continue
		}
		size, sizeErr := strconv.ParseInt(fields[1], 10, 64)
		seconds, timeErr := strconv.ParseFloat(fields[2], 64)
		if sizeErr != nil || timeErr != nil || size < 0 {
			continue
		}
		snapshots = append(snapshots, remoteEtcdSnapshot{Name: fields[0], Size: size, ModTime: time.Unix(0, int64(seconds*float64(time.Second))).UTC()})
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].ModTime.Equal(snapshots[j].ModTime) {
			return snapshots[i].Name > snapshots[j].Name
		}
		return snapshots[i].ModTime.After(snapshots[j].ModTime)
	})
	return snapshots
}

func pruneRemoteEtcdSnapshots(client etcdUploadClient, dir string, snapshots []remoteEtcdSnapshot, keep int) (int, error) {
	if keep < 1 {
		return 0, fmt.Errorf("remote etcd snapshot retention must be at least 1")
	}
	if len(snapshots) <= keep {
		return 0, nil
	}
	for _, snapshot := range snapshots[keep:] {
		if !etcdSnapshotFilenameRE.MatchString(snapshot.Name) {
			continue
		}
		path := filepath.Join(dir, snapshot.Name)
		if _, err := client.ExecuteCommand(etcdRemoteRemoveCommand(path, path+".sha256")); err != nil {
			return 0, fmt.Errorf("prune remote etcd snapshot %s: %w", path, err)
		}
	}
	return len(snapshots) - keep, nil
}
