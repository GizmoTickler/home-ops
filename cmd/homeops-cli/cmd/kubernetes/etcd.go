package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/cmdutil"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/ui"
)

const (
	etcdNamespace          = "kube-system"
	etcdSnapshotRemotePath = "/var/lib/etcd/homeops-etcd-snapshot.db"
	etcdDefaultStaleAfter  = 48 * time.Hour
)

type etcdNodeClient interface {
	Connect() error
	Close() error
	ExecuteCommand(command string) (string, error)
	StreamCommand(ctx context.Context, command string, stdout io.Writer) error
}

var (
	etcdNowFn     = time.Now
	etcdPodExecFn = func(ctx context.Context, pod string, command ...string) ([]byte, error) {
		args := []string{"exec", "-n", etcdNamespace, pod, "--"}
		args = append(args, command...)
		return kubectlOutputCtxFn(ctx, args...)
	}
	etcdNewNodeClientFn = func(cfg ssh.SSHConfig) etcdNodeClient {
		return ssh.NewSSHClient(cfg)
	}
	etcdWarnfFn = func(format string, args ...any) {
		common.NewColorLogger().Warn(format, args...)
	}
)

type etcdPod struct {
	Name string
	Node string
}

type etcdSnapshotStatus struct {
	Hash      uint64 `json:"hash"`
	Revision  int64  `json:"revision"`
	TotalKeys int64  `json:"total_keys"`
	TotalSize int64  `json:"total_size"`
}

type etcdBackupResult struct {
	Node       string `json:"node"`
	Path       string `json:"path"`
	Size       int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	Hash       uint64 `json:"snapshot_hash"`
	Revision   int64  `json:"revision"`
	TotalKeys  int64  `json:"total_keys"`
	TotalSize  int64  `json:"snapshot_size_bytes"`
	Pruned     int    `json:"pruned"`
	StatusTool string `json:"status_tool"`
}

type etcdMember struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	PeerURLs   []string `json:"peer_urls"`
	ClientURLs []string `json:"client_urls"`
	Learner    bool     `json:"learner"`
}

type etcdEndpoint struct {
	Endpoint string `json:"endpoint"`
	Healthy  bool   `json:"healthy"`
	Took     string `json:"took,omitempty"`
	Error    string `json:"error,omitempty"`
}

type etcdBackupInventory struct {
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	Age    string `json:"age,omitempty"`
	Size   int64  `json:"size_bytes,omitempty"`
	Detail string `json:"detail"`
}

type etcdStatusSummary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type etcdStatusReport struct {
	Summary   etcdStatusSummary   `json:"summary"`
	Pod       string              `json:"pod"`
	Node      string              `json:"node"`
	Members   []etcdMember        `json:"members"`
	Endpoints []etcdEndpoint      `json:"endpoints"`
	Backup    etcdBackupInventory `json:"backup"`
}

func newEtcdCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "etcd",
		Short: "Back up and inspect kubeadm-managed etcd",
		Long:  "Disaster-recovery operations for the stacked etcd static pods managed by kubeadm.",
	}
	cmd.AddCommand(newEtcdBackupCommand(), newEtcdStatusCommand())
	return cmd
}

func newEtcdBackupCommand() *cobra.Command {
	var outputDir string
	var keep int
	cmd := &cobra.Command{
		Use:          "backup",
		Short:        "Create, verify, download, and retain an etcd snapshot",
		SilenceUsage: true,
		Example: `  homeops-cli k8s etcd backup
  homeops-cli k8s etcd backup --output /secure/backups/etcd --keep 14`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdutil.ResolveStringFlagDefault(cmd, "output", &outputDir, func() string {
				return config.Get().State.EtcdBackup.Dir
			})
			cmdutil.ResolveIntFlagDefault(cmd, "keep", &keep, func() int {
				return config.Get().State.EtcdBackup.Keep
			})
			if keep < 1 {
				return fmt.Errorf("--keep must be at least 1")
			}
			result, err := runEtcdBackup(cmd.Context(), outputDir, keep)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), renderEtcdBackupResult(result))
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "snapshot directory (default: state.etcd_backup.dir)")
	cmd.Flags().IntVar(&keep, "keep", 0, "number of newest snapshots to retain (default: state.etcd_backup.keep)")
	return cmd
}

func newEtcdStatusCommand() *cobra.Command {
	var outputDir, output string
	var staleAfter time.Duration
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show etcd membership, endpoint health, and local backup freshness",
		SilenceUsage: true,
		Example: `  homeops-cli k8s etcd status
  homeops-cli k8s etcd status --stale-after 24h --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdutil.ResolveStringFlagDefault(cmd, "backup-dir", &outputDir, func() string {
				return config.Get().State.EtcdBackup.Dir
			})
			report, err := buildEtcdStatus(cmd.Context(), outputDir, staleAfter)
			if err != nil {
				return err
			}
			rendered, err := renderEtcdStatus(report, output)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			if report.Summary.Fail > 0 {
				return fmt.Errorf("etcd status found %d unhealthy endpoint(s)", report.Summary.Fail)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outputDir, "backup-dir", "", "snapshot directory to inventory (default: state.etcd_backup.dir)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().DurationVar(&staleAfter, "stale-after", etcdDefaultStaleAfter, "warn when the latest local snapshot is older than this duration")
	return cmd
}

func runEtcdBackup(ctx context.Context, outputDir string, keep int) (result etcdBackupResult, err error) {
	pod, err := findHealthyEtcdPod(ctx)
	if err != nil {
		return result, err
	}
	node, sshUser, err := resolveEtcdNodeAccess(pod)
	if err != nil {
		return result, err
	}
	client := etcdNewNodeClientFn(ssh.SSHConfig{Host: node.IP, Username: sshUser, Port: strconv.Itoa(config.Get().Cluster.NodeSSHPort)})
	if err := client.Connect(); err != nil {
		return result, fmt.Errorf("connect to etcd node %s over SSH: %w", node.Name, err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			etcdWarnfFn("Failed to close SSH connection to etcd node %s: %v", node.Name, closeErr)
		}
	}()

	if _, err = etcdPodExecFn(ctx, pod.Name, etcdctlCommand("snapshot", "save", etcdSnapshotRemotePath)...); err != nil {
		return result, fmt.Errorf("save etcd snapshot in pod %s: %w", pod.Name, err)
	}
	defer func() {
		if _, cleanupErr := client.ExecuteCommand("sudo rm -f " + common.ShellQuote(etcdSnapshotRemotePath)); cleanupErr != nil {
			etcdWarnfFn("Failed to clean up etcd snapshot on node %s: %v", node.Name, cleanupErr)
		}
	}()

	statusRaw, err := verifyEtcdSnapshot(ctx, pod.Name)
	if err != nil {
		return result, err
	}
	status, err := parseEtcdSnapshotStatus(statusRaw)
	if err != nil {
		return result, fmt.Errorf("parse etcdutl snapshot status: %w", err)
	}

	dir, err := expandHomeDir(outputDir)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, fmt.Errorf("create etcd backup directory %s: %w", dir, err)
	}
	nodePart := safeFilenamePart(pod.Node)
	stamp := etcdNowFn().UTC().Format("20060102T150405Z")
	path := filepath.Join(dir, fmt.Sprintf("etcd-snapshot-%s-%s.db", nodePart, stamp))
	size, digest, err := streamEtcdSnapshot(ctx, client, path)
	if err != nil {
		return result, fmt.Errorf("fetch etcd snapshot from node %s: %w", pod.Node, err)
	}
	if err := writeEtcdSnapshotChecksum(path, digest); err != nil {
		return result, err
	}

	pruned, err := pruneEtcdSnapshots(dir, keep)
	if err != nil {
		return result, err
	}
	return etcdBackupResult{
		Node:       pod.Node,
		Path:       path,
		Size:       size,
		SHA256:     digest,
		Hash:       status.Hash,
		Revision:   status.Revision,
		TotalKeys:  status.TotalKeys,
		TotalSize:  status.TotalSize,
		Pruned:     len(pruned),
		StatusTool: "etcdutl",
	}, nil
}

func resolveEtcdNodeAccess(pod etcdPod) (config.Node, string, error) {
	cfg := config.Get()
	node, ok := cfg.NodeByName(pod.Node)
	if !ok {
		return config.Node{}, "", fmt.Errorf("etcd pod %s runs on node %q, which is not present in cluster.nodes", pod.Name, pod.Node)
	}
	sshUser, err := cfg.ResolveSecret(config.KeyNodeSSHUser)
	if err != nil {
		return config.Node{}, "", fmt.Errorf("resolve node SSH user: %w", err)
	}
	sshUser = strings.TrimSpace(sshUser)
	if sshUser == "" {
		return config.Node{}, "", fmt.Errorf("resolved node SSH user is empty")
	}
	return node, sshUser, nil
}

func verifyEtcdSnapshot(ctx context.Context, pod string) ([]byte, error) {
	out, err := etcdPodExecFn(ctx, pod, "etcdutl", "snapshot", "status", etcdSnapshotRemotePath, "-w", "json")
	if err != nil {
		return nil, fmt.Errorf("verify etcd snapshot with etcdutl in pod %s: %w", pod, err)
	}
	return out, nil
}

func etcdctlCommand(args ...string) []string {
	base := []string{
		"etcdctl",
		"--endpoints", "https://127.0.0.1:2379",
		"--cacert", "/etc/kubernetes/pki/etcd/ca.crt",
		"--cert", "/etc/kubernetes/pki/etcd/server.crt",
		"--key", "/etc/kubernetes/pki/etcd/server.key",
	}
	return append(base, args...)
}

func streamEtcdSnapshot(ctx context.Context, client etcdNodeClient, path string) (size int64, digest string, err error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return 0, "", fmt.Errorf("open local etcd snapshot directory %s: %w", filepath.Dir(path), err)
	}
	filename := filepath.Base(path)
	file, err := root.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = root.Close()
		return 0, "", fmt.Errorf("create local etcd snapshot %s: %w", path, err)
	}
	removePartial := true
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close local etcd snapshot %s: %w", path, closeErr)
		}
		if removePartial || err != nil {
			_ = root.Remove(filename)
		}
		_ = root.Close()
	}()

	hasher := sha256.New()
	writer := &etcdSnapshotWriter{file: file, hasher: hasher}
	if err = client.StreamCommand(ctx, "sudo cat "+common.ShellQuote(etcdSnapshotRemotePath), writer); err != nil {
		return 0, "", err
	}
	if writer.written == 0 {
		return 0, "", fmt.Errorf("streamed etcd snapshot is empty")
	}
	removePartial = false
	return writer.written, hex.EncodeToString(hasher.Sum(nil)), nil
}

type etcdSnapshotWriter struct {
	file    io.Writer
	hasher  hash.Hash
	written int64
}

func (w *etcdSnapshotWriter) Write(data []byte) (int, error) {
	n, err := w.file.Write(data)
	if n > 0 {
		_, _ = w.hasher.Write(data[:n])
		w.written += int64(n)
	}
	return n, err
}

type etcdPodList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			ContainerStatuses []struct {
				Ready bool `json:"ready"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

func findHealthyEtcdPod(ctx context.Context) (etcdPod, error) {
	list, err := listEtcdPods(ctx, true)
	if err != nil || len(list.Items) == 0 {
		list, err = listEtcdPods(ctx, false)
	}
	if err != nil {
		return etcdPod{}, err
	}
	var healthy []etcdPod
	for _, item := range list.Items {
		isEtcd := item.Metadata.Labels["component"] == "etcd" || strings.HasPrefix(item.Metadata.Name, "etcd-")
		if !isEtcd || item.Status.Phase != "Running" || !etcdPodReady(item.Status.Conditions, item.Status.ContainerStatuses) {
			continue
		}
		node := item.Spec.NodeName
		if node == "" {
			node = strings.TrimPrefix(item.Metadata.Name, "etcd-")
		}
		healthy = append(healthy, etcdPod{Name: item.Metadata.Name, Node: node})
	}
	if len(healthy) == 0 {
		return etcdPod{}, fmt.Errorf("no healthy etcd static pod found in %s", etcdNamespace)
	}
	sort.Slice(healthy, func(i, j int) bool { return healthy[i].Name < healthy[j].Name })
	return healthy[0], nil
}

func listEtcdPods(ctx context.Context, labelOnly bool) (etcdPodList, error) {
	args := []string{"get", "pods", "-n", etcdNamespace}
	if labelOnly {
		args = append(args, "-l", "component=etcd")
	}
	args = append(args, "-o", "json")
	out, err := kubectlOutputCtxFn(ctx, args...)
	if err != nil {
		return etcdPodList{}, fmt.Errorf("find etcd pods: %w", err)
	}
	var list etcdPodList
	if err := json.Unmarshal(out, &list); err != nil {
		return etcdPodList{}, fmt.Errorf("parse etcd pod list: %w", err)
	}
	return list, nil
}

func etcdPodReady(conditions []struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}, containers []struct {
	Ready bool `json:"ready"`
}) bool {
	for _, condition := range conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return len(containers) > 0 && containers[0].Ready
}

func parseEtcdSnapshotStatus(raw []byte) (etcdSnapshotStatus, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return etcdSnapshotStatus{}, err
	}
	if list, ok := value.([]any); ok && len(list) > 0 {
		value = list[0]
	}
	object, ok := value.(map[string]any)
	if !ok {
		return etcdSnapshotStatus{}, fmt.Errorf("expected a JSON object")
	}
	hashValue, ok := jsonUint64(object, "hash")
	if !ok {
		return etcdSnapshotStatus{}, fmt.Errorf("snapshot status has no hash")
	}
	revision, ok := jsonInt64(object, "revision")
	if !ok {
		return etcdSnapshotStatus{}, fmt.Errorf("snapshot status has no revision")
	}
	totalKeys, ok := jsonInt64(object, "totalKey", "totalKeys", "total_keys")
	if !ok {
		return etcdSnapshotStatus{}, fmt.Errorf("snapshot status has no total key count")
	}
	totalSize, ok := jsonInt64(object, "totalSize", "total_size")
	if !ok {
		return etcdSnapshotStatus{}, fmt.Errorf("snapshot status has no total size")
	}
	return etcdSnapshotStatus{Hash: hashValue, Revision: revision, TotalKeys: totalKeys, TotalSize: totalSize}, nil
}

func jsonUint64(object map[string]any, keys ...string) (uint64, bool) {
	for _, key := range keys {
		value, exists := object[key]
		if !exists {
			continue
		}
		switch n := value.(type) {
		case float64:
			if n < 0 {
				return 0, false
			}
			return uint64(n), true
		case json.Number:
			v, err := strconv.ParseUint(n.String(), 10, 64)
			return v, err == nil
		case string:
			v, err := strconv.ParseUint(n, 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

func jsonInt64(object map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, exists := object[key]
		if !exists {
			continue
		}
		switch n := value.(type) {
		case float64:
			return int64(n), true
		case json.Number:
			v, err := n.Int64()
			return v, err == nil
		case string:
			v, err := strconv.ParseInt(n, 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

func writeEtcdSnapshotChecksum(path, digest string) error {
	sidecar := path + ".sha256"
	content := fmt.Sprintf("%s  %s\n", digest, filepath.Base(path))
	if err := os.WriteFile(sidecar, []byte(content), 0o600); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write etcd snapshot checksum %s: %w", sidecar, err)
	}
	return nil
}

func pruneEtcdSnapshots(dir string, keep int) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "etcd-snapshot-*.db"))
	if err != nil {
		return nil, fmt.Errorf("list etcd snapshots: %w", err)
	}
	type snapshotFile struct {
		path    string
		modTime time.Time
	}
	files := make([]snapshotFile, 0, len(entries))
	for _, path := range entries {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, fmt.Errorf("stat etcd snapshot %s: %w", path, statErr)
		}
		files = append(files, snapshotFile{path: path, modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path > files[j].path
		}
		return files[i].modTime.After(files[j].modTime)
	})
	if len(files) <= keep {
		return nil, nil
	}
	pruned := make([]string, 0, len(files)-keep)
	for _, file := range files[keep:] {
		if err := os.Remove(file.path); err != nil {
			return pruned, fmt.Errorf("prune etcd snapshot %s: %w", file.path, err)
		}
		if err := os.Remove(file.path + ".sha256"); err != nil && !os.IsNotExist(err) {
			return pruned, fmt.Errorf("prune etcd checksum %s: %w", file.path+".sha256", err)
		}
		pruned = append(pruned, file.path)
	}
	return pruned, nil
}

func buildEtcdStatus(ctx context.Context, backupDir string, staleAfter time.Duration) (etcdStatusReport, error) {
	pod, err := findHealthyEtcdPod(ctx)
	if err != nil {
		return etcdStatusReport{}, err
	}
	membersRaw, err := etcdPodExecFn(ctx, pod.Name, etcdctlCommand("member", "list", "-w", "json")...)
	if err != nil {
		return etcdStatusReport{}, fmt.Errorf("list etcd members through pod %s: %w", pod.Name, err)
	}
	members, err := parseEtcdMembers(membersRaw)
	if err != nil {
		return etcdStatusReport{}, err
	}
	healthRaw, err := etcdPodExecFn(ctx, pod.Name, etcdctlCommand("endpoint", "health", "--cluster", "-w", "json")...)
	if err != nil {
		return etcdStatusReport{}, fmt.Errorf("check etcd endpoint health through pod %s: %w", pod.Name, err)
	}
	endpoints, err := parseEtcdEndpoints(healthRaw)
	if err != nil {
		return etcdStatusReport{}, err
	}
	inventory, err := inspectEtcdBackups(backupDir, staleAfter)
	if err != nil {
		return etcdStatusReport{}, err
	}
	report := etcdStatusReport{Pod: pod.Name, Node: pod.Node, Members: members, Endpoints: endpoints, Backup: inventory}
	for _, endpoint := range endpoints {
		if endpoint.Healthy {
			report.Summary.OK++
		} else {
			report.Summary.Fail++
		}
	}
	if inventory.Status == "WARN" {
		report.Summary.Warn++
	} else {
		report.Summary.OK++
	}
	return report, nil
}

func parseEtcdMembers(raw []byte) ([]etcdMember, error) {
	var payload struct {
		Members []struct {
			ID         uint64   `json:"ID"`
			Name       string   `json:"name"`
			PeerURLs   []string `json:"peerURLs"`
			ClientURLs []string `json:"clientURLs"`
			Learner    bool     `json:"isLearner"`
		} `json:"members"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse etcd member list: %w", err)
	}
	members := make([]etcdMember, 0, len(payload.Members))
	for _, member := range payload.Members {
		members = append(members, etcdMember{
			ID: strconv.FormatUint(member.ID, 16), Name: member.Name,
			PeerURLs: member.PeerURLs, ClientURLs: member.ClientURLs, Learner: member.Learner,
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members, nil
}

func parseEtcdEndpoints(raw []byte) ([]etcdEndpoint, error) {
	var payload []struct {
		Endpoint string `json:"endpoint"`
		Health   bool   `json:"health"`
		Took     string `json:"took"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse etcd endpoint health: %w", err)
	}
	endpoints := make([]etcdEndpoint, 0, len(payload))
	for _, endpoint := range payload {
		endpoints = append(endpoints, etcdEndpoint{
			Endpoint: endpoint.Endpoint, Healthy: endpoint.Health, Took: endpoint.Took, Error: endpoint.Error,
		})
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Endpoint < endpoints[j].Endpoint })
	return endpoints, nil
}

func inspectEtcdBackups(dir string, staleAfter time.Duration) (etcdBackupInventory, error) {
	dir, err := expandHomeDir(dir)
	if err != nil {
		return etcdBackupInventory{}, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "etcd-snapshot-*.db"))
	if err != nil {
		return etcdBackupInventory{}, fmt.Errorf("list local etcd backups: %w", err)
	}
	if len(paths) == 0 {
		return etcdBackupInventory{Status: "WARN", Detail: "no local etcd snapshot found"}, nil
	}
	var latest os.FileInfo
	var latestPath string
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return etcdBackupInventory{}, fmt.Errorf("stat local etcd backup %s: %w", path, statErr)
		}
		if latest == nil || info.ModTime().After(latest.ModTime()) {
			latest, latestPath = info, path
		}
	}
	age := etcdNowFn().Sub(latest.ModTime())
	if age < 0 {
		age = 0
	}
	status, detail := "OK", "latest local snapshot is fresh"
	if age > staleAfter {
		status, detail = "WARN", "latest local snapshot is older than "+staleAfter.String()
	}
	return etcdBackupInventory{
		Status: status, Path: latestPath, Age: age.Round(time.Second).String(), Size: latest.Size(), Detail: detail,
	}, nil
}

func renderEtcdBackupResult(result etcdBackupResult) string {
	rows := [][]string{
		{"Node", result.Node},
		{"Path", result.Path},
		{"Size", humanBytes(result.Size)},
		{"SHA256", result.SHA256},
		{"Snapshot hash", strconv.FormatUint(result.Hash, 10)},
		{"Revision", strconv.FormatInt(result.Revision, 10)},
		{"Total keys", strconv.FormatInt(result.TotalKeys, 10)},
		{"Verified size", humanBytes(result.TotalSize)},
		{"Verified with", result.StatusTool},
		{"Pruned", strconv.Itoa(result.Pruned)},
	}
	return ui.Table([]string{"FIELD", "VALUE"}, rows)
}

func renderEtcdStatus(report etcdStatusReport, output string) (string, error) {
	switch output {
	case "", "table":
		memberRows := make([][]string, 0, len(report.Members))
		for _, member := range report.Members {
			memberRows = append(memberRows, []string{
				member.Name, member.ID, strings.Join(member.PeerURLs, ","), strings.Join(member.ClientURLs, ","), strconv.FormatBool(member.Learner),
			})
		}
		endpointRows := make([][]string, 0, len(report.Endpoints))
		for _, endpoint := range report.Endpoints {
			status := "OK"
			if !endpoint.Healthy {
				status = "FAIL"
			}
			endpointRows = append(endpointRows, []string{status, endpoint.Endpoint, endpoint.Took, endpoint.Error})
		}
		backupRows := [][]string{{report.Backup.Status, report.Backup.Path, report.Backup.Age, humanBytes(report.Backup.Size), report.Backup.Detail}}
		return fmt.Sprintf("Summary: OK=%d WARN=%d FAIL=%d\netcd pod: %s (node %s)\n\nMembers\n%s\n\nEndpoint health\n%s\n\nLocal backup\n%s",
			report.Summary.OK, report.Summary.Warn, report.Summary.Fail,
			report.Pod, report.Node,
			ui.Table([]string{"NAME", "ID", "PEER URLS", "CLIENT URLS", "LEARNER"}, memberRows),
			ui.Table([]string{"STATUS", "ENDPOINT", "TOOK", "ERROR"}, endpointRows),
			ui.Table([]string{"STATUS", "LATEST", "AGE", "SIZE", "DETAIL"}, backupRows)), nil
	case "json":
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	default:
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
}

func expandHomeDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("backup directory is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand backup directory %s: %w", path, err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Clean(path), nil
}

func safeFilenamePart(value string) string {
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func humanBytes(size int64) string {
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= float64(unit)
		if value < float64(unit) || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", size)
}
