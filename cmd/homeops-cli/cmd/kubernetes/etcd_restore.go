package kubernetes

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/ui"
)

const (
	etcdRestoreManifestDir = "/etc/kubernetes/manifests"
	etcdRestoreDataDir     = "/var/lib/etcd"
)

type etcdRestoreNodeClient interface {
	etcdNodeClient
	UploadFile(ctx context.Context, localPath, remotePath string) error
}

type etcdRestoreOptions struct {
	Snapshot string
	Plan     bool
	Execute  bool
}

type etcdRestorePlan struct {
	Cluster        string
	Snapshot       string
	Timestamp      string
	HoldingDir     string
	RemoteSnapshot string
	MemberBackup   string
	ClusterToken   string
	InitialCluster string
	Nodes          []config.Node
}

type etcdRestoreConnectedNode struct {
	Node     config.Node
	Client   etcdRestoreNodeClient
	ImageRef string
}

// errNoLocalSnapshotVerifier means the workstation has no way to run
// etcdutl locally. DR posture: degrade to the mandatory sha256 check with a
// loud warning instead of blocking a disaster recovery on local tooling.
var errNoLocalSnapshotVerifier = errors.New("etcdutl is not installed and neither podman nor docker is available for in-container verification")

type etcdRestorePreflight struct {
	Plan                  etcdRestorePlan
	SnapshotStatus        etcdSnapshotStatus
	SnapshotVerifySkipped bool
	SnapshotAge           time.Duration
	CurrentRevision       int64
	CurrentRevisionKnown  bool
	CurrentRevisionError  error
	Nodes                 []etcdRestoreConnectedNode
}

type etcdRestoreResult struct {
	Members  []etcdMember
	Revision int64
}

var (
	etcdRestoreNowFn           = time.Now
	etcdRestoreNewNodeClientFn = func(cfg ssh.SSHConfig) etcdRestoreNodeClient {
		return ssh.NewSSHClient(cfg)
	}
	etcdRestoreSnapshotStatusFn  = verifyLocalEtcdRestoreSnapshot
	etcdRestoreCurrentRevisionFn = currentEtcdClusterRevision
	etcdRestoreInputFn           = ui.Input
	etcdRestoreAssumeYesFn       = commandAssumeYes
	etcdRestoreLookPathFn        = common.LookPath
	etcdRestoreRunCommandFn      = common.RunCommand
)

func newEtcdRestoreCommand() *cobra.Command {
	var opts etcdRestoreOptions
	cmd := &cobra.Command{
		Use:          "restore <snapshot-file>",
		Short:        "Plan or execute a full stacked-etcd disaster recovery",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Long: `Restore every kubeadm stacked-etcd member from one verified snapshot.

This command defaults to plan-only mode. Cluster mutation requires --execute
and typing the configured cluster.name exactly. Global --yes bypasses the typed
confirmation only when --execute is also present. A failed execution deliberately
leaves static pods parked so an operator can inspect the partial restore.`,
		Example: `  # Safe default: print every node, file, command, and expected effect
  homeops-cli k8s etcd restore ~/.config/homeops/state/etcd/etcd-snapshot-k8s-0-20260714T123456Z.db

  # Execute after checksum/status/SSH/live-revision preflight and typed confirmation
  homeops-cli k8s etcd restore /secure/etcd/snapshot.db --execute

  # Non-interactive execution requires both flags
  homeops-cli k8s etcd restore /secure/etcd/snapshot.db --execute --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Snapshot = args[0]
			if opts.Plan && opts.Execute {
				return fmt.Errorf("--plan and --execute are mutually exclusive")
			}
			plan, err := buildEtcdRestorePlan(opts.Snapshot, etcdRestoreNowFn())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), renderEtcdRestorePlan(plan))
			if !opts.Execute {
				return nil
			}

			preflight, err := preflightEtcdRestore(cmd.Context(), plan)
			if err != nil {
				return fmt.Errorf("etcd restore preflight failed before any cluster mutation: %w", err)
			}
			defer closeEtcdRestoreNodes(preflight.Nodes)
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), renderEtcdRestorePreflight(preflight))

			if !etcdRestoreAssumeYesFn(cmd) {
				entered, inputErr := etcdRestoreInputFn(
					fmt.Sprintf("TYPE %q TO RESTORE ETCD (all apiservers will stop)", plan.Cluster),
					plan.Cluster,
				)
				if inputErr != nil {
					return fmt.Errorf("confirm etcd restore: %w", inputErr)
				}
				if entered != plan.Cluster {
					return fmt.Errorf("confirmation did not exactly match cluster name %q; no changes made", plan.Cluster)
				}
			} else {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "DESTRUCTIVE CONFIRMATION BYPASSED: --execute --yes")
			}

			result, err := executeEtcdRestore(cmd.Context(), preflight, cmd.ErrOrStderr())
			if err != nil {
				if parkErr := enforceEtcdRestoreParkedState(preflight.Nodes, plan, cmd.ErrOrStderr()); parkErr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "SAFETY PARK INCOMPLETE: %v\n", parkErr)
				}
				printEtcdRestoreRecovery(cmd.ErrOrStderr(), plan, err)
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), renderEtcdRestoreResult(result))
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.Plan, "plan", false, "print the complete restore plan without changing anything (default when --execute is absent)")
	cmd.Flags().BoolVar(&opts.Execute, "execute", false, "execute the destructive restore after all preflight checks and confirmation")
	return cmd
}

func buildEtcdRestorePlan(snapshot string, now time.Time) (etcdRestorePlan, error) {
	cfg := config.Get()
	if strings.TrimSpace(cfg.Cluster.Name) == "" {
		return etcdRestorePlan{}, fmt.Errorf("cluster.name is required for destructive restore confirmation")
	}
	if len(cfg.Cluster.Nodes) == 0 {
		return etcdRestorePlan{}, fmt.Errorf("cluster.nodes has no control-plane nodes")
	}
	for i, node := range cfg.Cluster.Nodes {
		if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.IP) == "" {
			return etcdRestorePlan{}, fmt.Errorf("cluster.nodes[%d] requires both name and ip", i)
		}
	}
	abs, err := filepath.Abs(snapshot)
	if err != nil {
		return etcdRestorePlan{}, fmt.Errorf("resolve snapshot path %s: %w", snapshot, err)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	return etcdRestorePlan{
		Cluster:        cfg.Cluster.Name,
		Snapshot:       filepath.Clean(abs),
		Timestamp:      stamp,
		HoldingDir:     "/etc/kubernetes/homeops-etcd-restore-" + stamp,
		RemoteSnapshot: etcdRestoreDataDir + "/homeops-restore-" + stamp + ".db",
		MemberBackup:   etcdRestoreDataDir + "/member.pre-homeops-restore-" + stamp,
		ClusterToken:   "restored-" + stamp,
		InitialCluster: buildEtcdInitialCluster(cfg.Cluster.Nodes),
		Nodes:          append([]config.Node(nil), cfg.Cluster.Nodes...),
	}, nil
}

func buildEtcdInitialCluster(nodes []config.Node) string {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		parts = append(parts, fmt.Sprintf("%s=https://%s:2380", node.Name, node.IP))
	}
	return strings.Join(parts, ",")
}

func renderEtcdRestorePlan(plan etcdRestorePlan) string {
	nodeRows := make([][]string, 0, len(plan.Nodes))
	for i, node := range plan.Nodes {
		role := "control-plane member"
		if i == 0 {
			role += " / init node / health probe"
		}
		nodeRows = append(nodeRows, []string{node.Name, node.IP, role})
	}
	stepRows := [][]string{
		{"0", "Preflight", "Verify local file + .sha256 + etcdutl status; connect every node; read etcd images; compare snapshot age/revision to live etcd"},
		{"1", "Park static pods", fmt.Sprintf("ALL nodes: move %s/{kube-apiserver,etcd}.yaml to %s and wait for both containers to stop", etcdRestoreManifestDir, plan.HoldingDir)},
		{"2", "Preserve old data", fmt.Sprintf("ALL nodes: move %s/member to %s (never delete it)", etcdRestoreDataDir, plan.MemberBackup)},
		{"3", "Upload + restore", fmt.Sprintf("Stream %s to %s on ALL nodes; run the parked manifest's etcd image once with ctr and token %s", plan.Snapshot, plan.RemoteSnapshot, plan.ClusterToken)},
		{"4", "Start etcd", "Restore etcd.yaml on ALL nodes, wait for containers, then require etcdctl endpoint health and member list"},
		{"5", "Start apiservers", "Restore kube-apiserver.yaml on ALL nodes and require https://127.0.0.1:6443/readyz"},
		{"6", "Final report", "Print restored member list and revision; Flux/workloads may reconcile to snapshot-era state"},
	}
	commandRows := make([][]string, 0, len(plan.Nodes)*4)
	for _, node := range plan.Nodes {
		commandRows = append(commandRows,
			[]string{node.Name, "park", etcdRestoreParkCommand(plan)},
			[]string{node.Name, "upload", fmt.Sprintf("ssh stdin stream -> sudo tee %s > /dev/null", common.ShellQuote(plan.RemoteSnapshot))},
			[]string{node.Name, "preserve", etcdRestorePreserveMemberCommand(plan)},
			[]string{node.Name, "restore", etcdRestoreCtrCommand(plan, node, "<etcd-image-from-manifest>")},
			// Executed as two separate SSH commands (etcd first, then
			// apiserver) — shown as two rows so the templates are honest.
			[]string{node.Name, "resume-etcd", etcdRestoreEtcdManifestCommand(plan)},
			[]string{node.Name, "resume-apiserver", etcdRestoreAPIServerManifestCommand(plan)},
		)
	}
	return fmt.Sprintf("ETCD DISASTER-RECOVERY PLAN (NO CHANGES MADE WITHOUT --execute)\nCluster: %s\nSnapshot: %s\nChecksum: %s.sha256\nInitial cluster: %s\nHolding directory: %s\nMember backup: %s\n\nNodes\n%s\n\nSteps and expected effects\n%s\n\nExact remote command templates\n%s",
		plan.Cluster, plan.Snapshot, plan.Snapshot, plan.InitialCluster, plan.HoldingDir, plan.MemberBackup,
		ui.Table([]string{"NODE", "IP", "ROLE"}, nodeRows),
		ui.Table([]string{"STEP", "ACTION", "EXPECTED EFFECT"}, stepRows),
		ui.Table([]string{"NODE", "PHASE", "COMMAND"}, commandRows))
}

func preflightEtcdRestore(ctx context.Context, plan etcdRestorePlan) (etcdRestorePreflight, error) {
	info, err := os.Stat(plan.Snapshot)
	if err != nil {
		return etcdRestorePreflight{}, fmt.Errorf("snapshot file %s: %w", plan.Snapshot, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return etcdRestorePreflight{}, fmt.Errorf("snapshot file %s must be a non-empty regular file", plan.Snapshot)
	}
	if err := verifyEtcdRestoreChecksum(plan.Snapshot); err != nil {
		return etcdRestorePreflight{}, err
	}

	cfg := config.Get()
	sshUser, err := cfg.ResolveSecret(config.KeyNodeSSHUser)
	if err != nil {
		return etcdRestorePreflight{}, fmt.Errorf("resolve node SSH user: %w", err)
	}
	sshUser = strings.TrimSpace(sshUser)
	if sshUser == "" {
		return etcdRestorePreflight{}, fmt.Errorf("resolved node SSH user is empty")
	}

	preflight := etcdRestorePreflight{Plan: plan, SnapshotAge: nonNegativeDuration(etcdRestoreNowFn().Sub(info.ModTime()))}
	for _, node := range plan.Nodes {
		client := etcdRestoreNewNodeClientFn(kubeutil.NodeSSHConfig(node, sshUser))
		if err := client.Connect(); err != nil {
			closeEtcdRestoreNodes(preflight.Nodes)
			return etcdRestorePreflight{}, fmt.Errorf("control-plane node %s (%s) is not SSH-reachable: %w", node.Name, node.IP, err)
		}
		manifest, err := client.ExecuteCommand("sudo cat " + common.ShellQuote(etcdRestoreManifestDir+"/etcd.yaml"))
		if err != nil {
			_ = client.Close()
			closeEtcdRestoreNodes(preflight.Nodes)
			return etcdRestorePreflight{}, fmt.Errorf("read etcd static-pod manifest on %s: %w", node.Name, err)
		}
		image, err := extractEtcdImageRef([]byte(manifest))
		if err != nil {
			_ = client.Close()
			closeEtcdRestoreNodes(preflight.Nodes)
			return etcdRestorePreflight{}, fmt.Errorf("inspect etcd image on %s: %w", node.Name, err)
		}
		preflight.Nodes = append(preflight.Nodes, etcdRestoreConnectedNode{Node: node, Client: client, ImageRef: image})
	}
	statusRaw, err := etcdRestoreSnapshotStatusFn(ctx, plan.Snapshot, preflight.Nodes[0].ImageRef)
	switch {
	case errors.Is(err, errNoLocalSnapshotVerifier):
		// sha256 sidecar verification already passed; structural etcdutl
		// verification is unavailable on this workstation. Warn, don't block DR.
		preflight.SnapshotVerifySkipped = true
	case err != nil:
		closeEtcdRestoreNodes(preflight.Nodes)
		return etcdRestorePreflight{}, fmt.Errorf("verify snapshot with etcdutl: %w", err)
	default:
		preflight.SnapshotStatus, err = parseEtcdSnapshotStatus(statusRaw)
		if err != nil {
			closeEtcdRestoreNodes(preflight.Nodes)
			return etcdRestorePreflight{}, fmt.Errorf("parse verified snapshot status: %w", err)
		}
	}
	preflight.CurrentRevision, err = etcdRestoreCurrentRevisionFn(ctx)
	if err != nil {
		preflight.CurrentRevisionError = err
	} else {
		preflight.CurrentRevisionKnown = true
	}
	return preflight, nil
}

func verifyEtcdRestoreChecksum(snapshot string) error {
	sidecar := snapshot + ".sha256"
	file, err := os.Open(sidecar) // #nosec G304 -- sidecar is derived from the user-selected snapshot path
	if err != nil {
		return fmt.Errorf("checksum sidecar %s: %w", sidecar, err)
	}
	scanner := bufio.NewScanner(file)
	var expected string
	if scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 {
			expected = fields[0]
		}
	}
	scanErr := scanner.Err()
	_ = file.Close()
	if scanErr != nil {
		return fmt.Errorf("read checksum sidecar %s: %w", sidecar, scanErr)
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("checksum sidecar %s does not contain a SHA-256 digest", sidecar)
	}
	snapshotFile, err := os.Open(snapshot) // #nosec G304 -- user explicitly selected this restore snapshot
	if err != nil {
		return fmt.Errorf("open snapshot %s: %w", snapshot, err)
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, snapshotFile)
	closeErr := snapshotFile.Close()
	if copyErr != nil {
		return fmt.Errorf("hash snapshot %s: %w", snapshot, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close snapshot %s: %w", snapshot, closeErr)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("snapshot checksum mismatch: sidecar=%s actual=%s", strings.ToLower(expected), actual)
	}
	return nil
}

func verifyLocalEtcdRestoreSnapshot(ctx context.Context, snapshot, imageRef string) ([]byte, error) {
	if _, err := etcdRestoreLookPathFn("etcdutl"); err == nil {
		result, runErr := etcdRestoreRunCommandFn(ctx, common.CommandOptions{
			Name: "etcdutl", Args: []string{"snapshot", "status", snapshot, "-w", "json"},
			Timeout: 2 * time.Minute,
		})
		if runErr != nil {
			return nil, fmt.Errorf("local etcdutl: %w: %s", runErr, strings.TrimSpace(result.Stderr))
		}
		return []byte(result.Stdout), nil
	}
	for _, runtimeName := range []string{"podman", "docker"} {
		if _, err := etcdRestoreLookPathFn(runtimeName); err != nil {
			continue
		}
		result, runErr := etcdRestoreRunCommandFn(ctx, common.CommandOptions{
			Name:    runtimeName,
			Args:    []string{"run", "--rm", "--entrypoint", "etcdutl", "-v", snapshot + ":/snapshot.db:ro", imageRef, "snapshot", "status", "/snapshot.db", "-w", "json"},
			Timeout: 5 * time.Minute,
		})
		if runErr != nil {
			return nil, fmt.Errorf("%s etcdutl container: %w: %s", runtimeName, runErr, strings.TrimSpace(result.Stderr))
		}
		return []byte(result.Stdout), nil
	}
	return nil, errNoLocalSnapshotVerifier
}

func extractEtcdImageRef(manifest []byte) (string, error) {
	var pod struct {
		Spec struct {
			Containers []struct {
				Name  string `yaml:"name"`
				Image string `yaml:"image"`
			} `yaml:"containers"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(manifest, &pod); err != nil {
		return "", fmt.Errorf("parse manifest YAML: %w", err)
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == "etcd" && strings.TrimSpace(container.Image) != "" {
			return strings.TrimSpace(container.Image), nil
		}
	}
	return "", fmt.Errorf("manifest has no named etcd container with an image")
}

func currentEtcdClusterRevision(ctx context.Context) (int64, error) {
	pod, err := findHealthyEtcdPod(ctx)
	if err != nil {
		return 0, err
	}
	raw, err := etcdPodExecFn(ctx, pod.Name, etcdctlCommand("endpoint", "status", "-w", "json")...)
	if err != nil {
		return 0, err
	}
	return parseEtcdEndpointRevision(raw)
}

func parseEtcdEndpointRevision(raw []byte) (int64, error) {
	var payload []struct {
		Status struct {
			Header struct {
				Revision int64 `json:"revision"`
			} `json:"header"`
		} `json:"Status"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, fmt.Errorf("parse etcd endpoint status: %w", err)
	}
	var revision int64
	for _, endpoint := range payload {
		if endpoint.Status.Header.Revision > revision {
			revision = endpoint.Status.Header.Revision
		}
	}
	if revision == 0 {
		return 0, fmt.Errorf("etcd endpoint status contains no revision")
	}
	return revision, nil
}

func renderEtcdRestorePreflight(preflight etcdRestorePreflight) string {
	snapshotRevision := strconv.FormatInt(preflight.SnapshotStatus.Revision, 10)
	if preflight.SnapshotVerifySkipped {
		snapshotRevision = "UNKNOWN — etcdutl verification skipped (no local etcdutl/podman/docker); sha256 sidecar verified"
	}
	revisionComparison := "live cluster revision unavailable: " + errorString(preflight.CurrentRevisionError)
	if preflight.CurrentRevisionKnown {
		if preflight.SnapshotVerifySkipped {
			revisionComparison = fmt.Sprintf("live=%d (snapshot revision unknown; rollback delta cannot be computed)", preflight.CurrentRevision)
		} else {
			delta := preflight.CurrentRevision - preflight.SnapshotStatus.Revision
			revisionComparison = fmt.Sprintf("snapshot=%d live=%d rollback_delta=%d", preflight.SnapshotStatus.Revision, preflight.CurrentRevision, delta)
		}
	}
	nodes := make([]string, 0, len(preflight.Nodes))
	for _, node := range preflight.Nodes {
		nodes = append(nodes, node.Node.Name+"=reachable ("+node.ImageRef+")")
	}
	return fmt.Sprintf("\n!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!\nETCD RESTORE PREFLIGHT PASSED — DESTRUCTIVE EXECUTION IS ARMED\nSnapshot age: %s\nSnapshot revision: %s\nRevision comparison: %s\nSSH/image checks: %s\nA restore rolls Kubernetes state back to the snapshot revision.\n!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!",
		preflight.SnapshotAge.Round(time.Second), snapshotRevision, revisionComparison, strings.Join(nodes, ", "))
}

func executeEtcdRestore(ctx context.Context, preflight etcdRestorePreflight, progress io.Writer) (etcdRestoreResult, error) {
	plan := preflight.Plan
	if err := runEtcdRestoreNodePhase(progress, "1/8", "park static pods", preflight.Nodes, func(node etcdRestoreConnectedNode) error {
		_, err := node.Client.ExecuteCommand(etcdRestoreParkCommand(plan))
		return err
	}); err != nil {
		return etcdRestoreResult{}, err
	}
	_, _ = fmt.Fprintln(progress, "[2/8] discover etcd image references from parked manifests")
	for i := range preflight.Nodes {
		node := &preflight.Nodes[i]
		manifest, err := node.Client.ExecuteCommand("sudo cat " + common.ShellQuote(plan.HoldingDir+"/etcd.yaml"))
		if err != nil {
			return etcdRestoreResult{}, fmt.Errorf("phase discover parked etcd image failed on %s: %w", node.Node.Name, err)
		}
		imageRef, err := extractEtcdImageRef([]byte(manifest))
		if err != nil {
			return etcdRestoreResult{}, fmt.Errorf("phase discover parked etcd image failed on %s: %w", node.Node.Name, err)
		}
		node.ImageRef = imageRef
	}
	if err := runEtcdRestoreNodePhase(progress, "3/8", "preserve old member directories", preflight.Nodes, func(node etcdRestoreConnectedNode) error {
		_, err := node.Client.ExecuteCommand(etcdRestorePreserveMemberCommand(plan))
		return err
	}); err != nil {
		return etcdRestoreResult{}, err
	}
	if err := runEtcdRestoreNodePhase(progress, "4/8", "stream snapshot to nodes", preflight.Nodes, func(node etcdRestoreConnectedNode) error {
		return node.Client.UploadFile(ctx, plan.Snapshot, plan.RemoteSnapshot)
	}); err != nil {
		return etcdRestoreResult{}, err
	}
	if err := runEtcdRestoreNodePhase(progress, "5/8", "restore etcd data with ctr", preflight.Nodes, func(node etcdRestoreConnectedNode) error {
		_, err := node.Client.ExecuteCommand(etcdRestoreCtrCommand(plan, node.Node, node.ImageRef))
		return err
	}); err != nil {
		return etcdRestoreResult{}, err
	}
	if err := runEtcdRestoreNodePhase(progress, "6/8", "restore etcd manifests", preflight.Nodes, func(node etcdRestoreConnectedNode) error {
		_, err := node.Client.ExecuteCommand(etcdRestoreEtcdManifestCommand(plan))
		return err
	}); err != nil {
		return etcdRestoreResult{}, err
	}
	_, _ = fmt.Fprintln(progress, "[7/8] wait for etcd health and member list on init node")
	membersRaw, err := preflight.Nodes[0].Client.ExecuteCommand(etcdRestoreHealthCommand())
	if err != nil {
		return etcdRestoreResult{}, fmt.Errorf("phase wait for etcd health failed on %s: %w", preflight.Nodes[0].Node.Name, err)
	}
	members, err := parseEtcdMembers([]byte(membersRaw))
	if err != nil {
		return etcdRestoreResult{}, fmt.Errorf("parse restored etcd member list: %w", err)
	}
	if len(members) != len(preflight.Nodes) {
		return etcdRestoreResult{}, fmt.Errorf("restored etcd member list has %d member(s), expected %d", len(members), len(preflight.Nodes))
	}
	if err := runEtcdRestoreNodePhase(progress, "8/8", "restore apiserver manifests and wait for readyz", preflight.Nodes, func(node etcdRestoreConnectedNode) error {
		_, err := node.Client.ExecuteCommand(etcdRestoreAPIServerManifestCommand(plan))
		return err
	}); err != nil {
		return etcdRestoreResult{}, err
	}
	revisionRaw, err := preflight.Nodes[0].Client.ExecuteCommand(etcdRestoreRevisionCommand())
	if err != nil {
		return etcdRestoreResult{}, fmt.Errorf("read final etcd revision on %s: %w", preflight.Nodes[0].Node.Name, err)
	}
	revision, err := parseEtcdEndpointRevision([]byte(revisionRaw))
	if err != nil {
		return etcdRestoreResult{}, err
	}
	return etcdRestoreResult{Members: members, Revision: revision}, nil
}

func runEtcdRestoreNodePhase(progress io.Writer, number, name string, nodes []etcdRestoreConnectedNode, run func(etcdRestoreConnectedNode) error) error {
	_, _ = fmt.Fprintf(progress, "[%s] %s\n", number, name)
	for _, node := range nodes {
		_, _ = fmt.Fprintf(progress, "  - %s (%s) ...\n", node.Node.Name, node.Node.IP)
		if err := run(node); err != nil {
			return fmt.Errorf("phase %s failed on %s: %w", name, node.Node.Name, err)
		}
	}
	return nil
}

func etcdRestoreParkCommand(plan etcdRestorePlan) string {
	return fmt.Sprintf("sudo mkdir -p %s && sudo mv %s %s && sudo mv %s %s && for i in $(seq 1 60); do if [ -z \"$(sudo crictl ps --name etcd -q)$(sudo crictl ps --name kube-apiserver -q)\" ]; then exit 0; fi; sleep 2; done; echo 'timed out waiting for etcd/apiserver containers to stop' >&2; exit 1",
		common.ShellQuote(plan.HoldingDir),
		common.ShellQuote(etcdRestoreManifestDir+"/kube-apiserver.yaml"), common.ShellQuote(plan.HoldingDir+"/kube-apiserver.yaml"),
		common.ShellQuote(etcdRestoreManifestDir+"/etcd.yaml"), common.ShellQuote(plan.HoldingDir+"/etcd.yaml"))
}

func etcdRestorePreserveMemberCommand(plan etcdRestorePlan) string {
	return fmt.Sprintf("test -d %s && test ! -e %s && sudo mv %s %s",
		common.ShellQuote(etcdRestoreDataDir+"/member"), common.ShellQuote(plan.MemberBackup),
		common.ShellQuote(etcdRestoreDataDir+"/member"), common.ShellQuote(plan.MemberBackup))
}

func etcdRestoreCtrCommand(plan etcdRestorePlan, node config.Node, imageRef string) string {
	runtimeID := safeFilenamePart("restore-" + plan.Timestamp + "-" + node.Name)
	return fmt.Sprintf("sudo ctr -n k8s.io run --rm --mount type=bind,src=/var/lib/etcd,dst=/var/lib/etcd,options=rbind:rw %s %s etcdutl snapshot restore %s --name %s --initial-cluster %s --initial-cluster-token %s --initial-advertise-peer-urls %s --data-dir /var/lib/etcd",
		common.ShellQuote(imageRef), common.ShellQuote(runtimeID), common.ShellQuote(plan.RemoteSnapshot),
		common.ShellQuote(node.Name), common.ShellQuote(plan.InitialCluster), common.ShellQuote(plan.ClusterToken),
		common.ShellQuote("https://"+node.IP+":2380"))
}

func etcdRestoreEtcdManifestCommand(plan etcdRestorePlan) string {
	return fmt.Sprintf("sudo mv %s %s && for i in $(seq 1 60); do if [ -n \"$(sudo crictl ps --name etcd -q)\" ]; then exit 0; fi; sleep 2; done; echo 'timed out waiting for etcd container to start' >&2; exit 1",
		common.ShellQuote(plan.HoldingDir+"/etcd.yaml"), common.ShellQuote(etcdRestoreManifestDir+"/etcd.yaml"))
}

func etcdRestoreAPIServerManifestCommand(plan etcdRestorePlan) string {
	return fmt.Sprintf("sudo mv %s %s && for i in $(seq 1 90); do if curl --fail --silent --show-error --insecure https://127.0.0.1:6443/readyz >/dev/null; then exit 0; fi; sleep 2; done; echo 'timed out waiting for kube-apiserver /readyz' >&2; exit 1",
		common.ShellQuote(plan.HoldingDir+"/kube-apiserver.yaml"), common.ShellQuote(etcdRestoreManifestDir+"/kube-apiserver.yaml"))
}

func etcdRestoreHealthCommand() string {
	return "id=$(sudo crictl ps --name etcd -q | head -n1); test -n \"$id\" && " +
		"for i in $(seq 1 90); do if sudo crictl exec \"$id\" " + strings.Join(etcdctlCommand("endpoint", "health", "--cluster"), " ") +
		" >/dev/null 2>&1; then sudo crictl exec \"$id\" " + strings.Join(etcdctlCommand("member", "list", "-w", "json"), " ") +
		"; exit $?; fi; sleep 2; done; echo 'timed out waiting for restored etcd health' >&2; exit 1"
}

func etcdRestoreRevisionCommand() string {
	return "id=$(sudo crictl ps --name etcd -q | head -n1); test -n \"$id\" && sudo crictl exec \"$id\" " +
		strings.Join(etcdctlCommand("endpoint", "status", "-w", "json"), " ")
}

func renderEtcdRestoreResult(result etcdRestoreResult) string {
	rows := make([][]string, 0, len(result.Members))
	for _, member := range result.Members {
		rows = append(rows, []string{member.Name, member.ID, strings.Join(member.PeerURLs, ","), strings.Join(member.ClientURLs, ",")})
	}
	return fmt.Sprintf("ETCD RESTORE COMPLETE\nCluster revision: %d\n\nMembers\n%s\n\nFlux and other controllers will converge workloads from snapshot-era state; inspect reconciliation before declaring recovery complete.",
		result.Revision, ui.Table([]string{"NAME", "ID", "PEER URLS", "CLIENT URLS"}, rows))
}

func printEtcdRestoreRecovery(out io.Writer, plan etcdRestorePlan, cause error) {
	_, _ = fmt.Fprintf(out, `
!!!!!!!!!!!!!!!!!!!! ETCD RESTORE ABORTED !!!!!!!!!!!!!!!!!!!!
Failure: %v

NO AUTOMATIC ROLLBACK WAS ATTEMPTED. Some or all nodes may be parked.
Holding directory on every node: %s
Preserved original member directory on every mutated node: %s
Uploaded snapshot: %s

Before moving any manifests back, inspect every node and determine which ctr
restores completed. Do not mix restored and original etcd members. The parked
files are %s/etcd.yaml and %s/kube-apiserver.yaml. Preserve the member backup;
never delete it during diagnosis.
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
`, cause, plan.HoldingDir, plan.MemberBackup, plan.RemoteSnapshot, plan.HoldingDir, plan.HoldingDir)
}

// enforceEtcdRestoreParkedState is the only post-failure action. It never
// changes restored or preserved etcd data; it only removes any static-pod
// manifests that a later phase may have put back. This preserves the explicit
// operator-inspection state without attempting a data rollback.
func enforceEtcdRestoreParkedState(nodes []etcdRestoreConnectedNode, plan etcdRestorePlan, progress io.Writer) error {
	_, _ = fmt.Fprintln(progress, "Enforcing parked static-pod state on every reachable node (no etcd data rollback) ...")
	var failures []string
	command := fmt.Sprintf("sudo mkdir -p %s && if [ -f %s ]; then test ! -e %s && sudo mv %s %s; fi && if [ -f %s ]; then test ! -e %s && sudo mv %s %s; fi",
		common.ShellQuote(plan.HoldingDir),
		common.ShellQuote(etcdRestoreManifestDir+"/kube-apiserver.yaml"), common.ShellQuote(plan.HoldingDir+"/kube-apiserver.yaml"),
		common.ShellQuote(etcdRestoreManifestDir+"/kube-apiserver.yaml"), common.ShellQuote(plan.HoldingDir+"/kube-apiserver.yaml"),
		common.ShellQuote(etcdRestoreManifestDir+"/etcd.yaml"), common.ShellQuote(plan.HoldingDir+"/etcd.yaml"),
		common.ShellQuote(etcdRestoreManifestDir+"/etcd.yaml"), common.ShellQuote(plan.HoldingDir+"/etcd.yaml"))
	for _, node := range nodes {
		if _, err := node.Client.ExecuteCommand(command); err != nil {
			failures = append(failures, node.Node.Name+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("could not park %d node(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func closeEtcdRestoreNodes(nodes []etcdRestoreConnectedNode) {
	for _, node := range nodes {
		if err := node.Client.Close(); err != nil {
			etcdWarnfFn("Failed to close SSH connection to etcd restore node %s: %v", node.Node.Name, err)
		}
	}
}

func commandAssumeYes(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if flag := cmd.Flags().Lookup("yes"); flag != nil {
		value, err := cmd.Flags().GetBool("yes")
		return err == nil && value
	}
	if flag := cmd.InheritedFlags().Lookup("yes"); flag != nil {
		value, err := cmd.InheritedFlags().GetBool("yes")
		return err == nil && value
	}
	return false
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

func errorString(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}
