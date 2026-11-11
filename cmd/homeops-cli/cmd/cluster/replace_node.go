package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	cmdflatcar "homeops-cli/cmd/flatcar"
	"homeops-cli/internal/config"
	flatcarinternal "homeops-cli/internal/flatcar"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"

	"github.com/spf13/cobra"
)

const (
	replaceDefaultTimeout = 20 * time.Minute
	// Healthy etcd members, not counting the node being replaced, that must
	// exist before its member is removed.
	replaceMinOtherHealthyMembers = 3
)

// replacePVECommandFn runs one command on the Proxmox host as root. Swappable
// for tests.
var replacePVECommandFn = func(_ context.Context, command string) (string, error) {
	return cmdflatcar.RunPVERootCommand(command)
}

var replaceNodeNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

type replaceNodeOptions struct {
	Node              string
	Plan              bool
	Timeout           time.Duration
	Output            string
	ImagePath         string
	ImageVolume       string
	ImageStreamLatest bool
	AllowSameOS       bool
}

// replacePreflight is what the preconditions measured; the post-checks compare
// against it.
type replacePreflight struct {
	EtcdMembers int
}

type replaceReport struct {
	Node             string         `json:"node"`
	IP               string         `json:"ip"`
	VMID             int            `json:"vmid"`
	OS               string         `json:"os"`
	InitNode         string         `json:"init_node"`
	Image            string         `json:"image"`
	Plan             bool           `json:"plan"`
	Steps            []rehearseStep `json:"steps"`
	Verdict          string         `json:"verdict"`
	TotalDuration    string         `json:"total_duration"`
	RecoveryCommands []string       `json:"recovery_commands,omitempty"`
}

type replaceOperations interface {
	Preconditions(context.Context, rehearseNodeSpec) (replacePreflight, error)
	Drain(context.Context, rehearseNodeSpec, time.Duration) error
	RemoveFromCluster(context.Context, rehearseNodeSpec) error
	DestroyVM(context.Context, rehearseNodeSpec) error
	ForgetHostKey(context.Context, rehearseNodeSpec)
	CreateJoinMaterial(context.Context, rehearseNodeSpec) (*flatcarinternal.KubeadmResult, error)
	Deploy(context.Context, rehearseNodeSpec, replaceNodeOptions, flatcarinternal.KubeadmResult) error
	WaitReady(context.Context, rehearseNodeSpec, time.Duration) (rehearseNodeReady, error)
	SmokeTest(context.Context, rehearseNodeSpec, time.Duration) error
	Uncordon(context.Context, rehearseNodeSpec) error
	PostChecks(context.Context, rehearseNodeSpec, replacePreflight) (string, error)
	InvalidateToken(context.Context, rehearseNodeSpec, string) error
}

// realReplaceOperations reuses the rehearsal's join-material, readiness,
// smoke-test and token operations; only the production-specific steps are new.
type realReplaceOperations struct {
	realRehearseOperations
}

func newReplaceNodeCommand() *cobra.Command {
	opts := replaceNodeOptions{Timeout: replaceDefaultTimeout, Output: "table"}
	cmd := &cobra.Command{
		Use:          "replace-node",
		Short:        "Rebuild one production control-plane node in place onto its configured OS",
		SilenceUsage: true,
		Long: `Rebuild an existing production control-plane node (a cluster.nodes entry)
in place: same name, IPs, MACs, VMID and SR-IOV VFs, on the OS configured for
it (nodes[].os / cluster.os). The node is drained, its etcd member and Node
object removed, and its VM destroyed together with every disk attached to it
(nothing is preserved: the node carries no data beyond what it rejoins with).
The new VM joins with fresh join material minted on another healthy control
plane.

It refuses the rehearsal test node and any name outside cluster.nodes, and
requires every other node Ready and at least 3 healthy etcd members besides the
target. The FCOS image must be pinned (--image-path/--image-volume) unless
--image-stream-latest is given.`,
		Example: `  # Inspect every step without contacting Kubernetes or Proxmox
  homeops-cli cluster replace-node --node k8s-2 --plan

  # Rebuild k8s-2 from a pinned, pre-staged FCOS image volume
  homeops-cli cluster replace-node --node k8s-2 --image-volume vm-ssd:vm-900-disk-0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeReplaceNodeCommand(cmd, opts, realReplaceOperations{})
		},
	}
	cmd.Flags().StringVar(&opts.Node, "node", "", "production node to rebuild (a cluster.nodes name)")
	cmd.Flags().BoolVar(&opts.Plan, "plan", false, "print the complete replacement plan without executing or prompting")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", replaceDefaultTimeout, "maximum wait for drain, SSH, node readiness, and smoke pod readiness")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "table", "output format: table or json")
	cmd.Flags().StringVar(&opts.ImagePath, "image-path", "", "path on Proxmox to import the pinned OS disk image from")
	cmd.Flags().StringVar(&opts.ImageVolume, "image-volume", "", "existing pinned OS image volume to attach")
	cmd.Flags().BoolVar(&opts.ImageStreamLatest, "image-stream-latest", false, "without a pinned image, stage whatever the FCOS stable stream serves at run time")
	cmd.Flags().BoolVar(&opts.AllowSameOS, "allow-same-os", false, "allow rebuilding a node whose configured OS is flatcar")
	_ = cmd.MarkFlagRequired("node")
	return cmd
}

func executeReplaceNodeCommand(cmd *cobra.Command, opts replaceNodeOptions, operations replaceOperations) error {
	if err := ui.ValidateOutputFormat(opts.Output); err != nil {
		return err
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	cfg := rehearseConfigFn()
	spec, err := buildReplaceNodeSpec(cfg, opts)
	if err != nil {
		return err
	}
	if opts.Plan {
		return writeReplaceReport(cmd, plannedReplaceReport(cfg, spec, opts), opts.Output)
	}
	if err := validateReplaceImage(opts); err != nil {
		return err
	}
	confirmed, err := rehearseConfirmFn(fmt.Sprintf(
		"Rebuild PRODUCTION control-plane node %s (VMID %d, IP %s) onto %s? It is drained, removed from etcd, and its VM destroyed with all of its disks",
		spec.Node.Name, spec.VMID, spec.Node.IP, cfg.OSForNode(spec.Node)), false)
	if err != nil {
		return fmt.Errorf("confirm node replacement: %w", err)
	}
	if !confirmed {
		return fmt.Errorf("node replacement cancelled")
	}
	spec.SSHUser = resolveRehearseSSHUser(cfg)

	report, runErr := runReplaceNode(cmd.Context(), spec, opts, operations)
	if err := writeReplaceReport(cmd, report, opts.Output); err != nil {
		return err
	}
	return runErr
}

// buildReplaceNodeSpec resolves the production node and its join-material
// source. Only cluster.nodes entries qualify, never the rehearsal test node.
func buildReplaceNodeSpec(cfg *config.Config, opts replaceNodeOptions) (rehearseNodeSpec, error) {
	name := strings.TrimSpace(opts.Node)
	if name == "" {
		return rehearseNodeSpec{}, fmt.Errorf("--node is required")
	}
	if cfg == nil {
		return rehearseNodeSpec{}, fmt.Errorf("configuration is not loaded")
	}
	if test := cfg.Cluster.TestNode; test != nil && (name == test.Name || (strings.TrimSpace(test.Name) == "" && name == "k8s-test")) {
		return rehearseNodeSpec{}, fmt.Errorf("refusing %q: it is the rehearsal test node (use cluster rehearse-node)", name)
	}
	idx := slices.IndexFunc(cfg.Cluster.Nodes, func(n config.Node) bool { return n.Name == name })
	if idx < 0 {
		return rehearseNodeSpec{}, fmt.Errorf("refusing %q: not a production cluster.nodes entry", name)
	}
	node := cfg.Cluster.Nodes[idx]
	if !replaceNodeNamePattern.MatchString(node.Name) {
		return rehearseNodeSpec{}, fmt.Errorf("node name %q is not a plain DNS label", node.Name)
	}
	if net.ParseIP(strings.TrimSpace(node.IP)) == nil {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.nodes %s ip must be a valid IP address", node.Name)
	}
	profile := node.VM.ForProvider("flatcar")
	if profile.VMID <= 0 {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.nodes %s vm.vmid must be greater than zero", node.Name)
	}
	if _, err := net.ParseMAC(profile.Mac); err != nil {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.nodes %s vm.mac %q is invalid: %w", node.Name, profile.Mac, err)
	}
	// The join material (and the etcd pod used for membership changes) must
	// come from a control plane that survives the replacement.
	initIdx := slices.IndexFunc(cfg.Cluster.Nodes, func(n config.Node) bool {
		return n.Name != name && net.ParseIP(strings.TrimSpace(n.IP)) != nil
	})
	if initIdx < 0 {
		return rehearseNodeSpec{}, fmt.Errorf("replacing %s needs another control plane in cluster.nodes to mint join material", name)
	}

	if osFamily := cfg.OSForNode(node); osFamily == config.OSFlatcar && !opts.AllowSameOS {
		return rehearseNodeSpec{}, fmt.Errorf("refusing %s: its configured OS is flatcar; set nodes[].os (or cluster.os) to fcos, or pass --allow-same-os to rebuild it on flatcar", name)
	}

	providerName := cfg.Hypervisors.Default
	if strings.TrimSpace(providerName) == "" {
		providerName = "proxmox"
	}
	providerName, err := vmlifecycle.NormalizeVMProvider(providerName)
	if err != nil {
		return rehearseNodeSpec{}, err
	}
	if providerName != "proxmox" {
		return rehearseNodeSpec{}, fmt.Errorf("cluster replace-node supports Proxmox only (the VM teardown uses qm); hypervisors.default is %s", providerName)
	}
	if strings.TrimSpace(cfg.Volsync.CheckImage) == "" {
		return rehearseNodeSpec{}, fmt.Errorf("volsync.check_image is required")
	}
	return rehearseNodeSpec{
		Node:       node,
		InitNode:   cfg.Cluster.Nodes[initIdx],
		Provider:   providerName,
		VMID:       profile.VMID,
		CheckImage: cfg.Volsync.CheckImage,
		SSHUser:    "<node-ssh-user>",
	}, nil
}

// validateReplaceImage requires a pinned image: re-resolving the stream at run
// time could rebuild each node onto a different (possibly incompatible) kernel.
func validateReplaceImage(opts replaceNodeOptions) error {
	pinned := strings.TrimSpace(opts.ImagePath) != "" || strings.TrimSpace(opts.ImageVolume) != ""
	switch {
	case pinned && opts.ImageStreamLatest:
		return fmt.Errorf("--image-stream-latest cannot be combined with --image-path or --image-volume")
	case !pinned && !opts.ImageStreamLatest:
		return fmt.Errorf("pin the OS image with --image-path or --image-volume so every rebuild uses the same one (or pass --image-stream-latest to stage whatever the stream serves now)")
	}
	return nil
}

func replaceImageDescription(opts replaceNodeOptions) string {
	switch {
	case strings.TrimSpace(opts.ImageVolume) != "":
		return "pinned volume " + opts.ImageVolume
	case strings.TrimSpace(opts.ImagePath) != "":
		return "pinned path " + opts.ImagePath
	case opts.ImageStreamLatest:
		return "FCOS stable stream, resolved at run time (--image-stream-latest)"
	}
	return "<required at execution: --image-path or --image-volume (or --image-stream-latest)>"
}

// The qm commands of the VM teardown. Plan and execution share them so --plan
// prints exactly what runs.
func qmStopCommand(vmid int) string { return fmt.Sprintf("qm stop %d", vmid) }
func qmDestroyCommand(vmid int) string {
	return fmt.Sprintf("qm destroy %d --destroy-unreferenced-disks 0", vmid)
}

func replaceDrainArgs(spec rehearseNodeSpec, timeout time.Duration) []string {
	return []string{"drain", spec.Node.Name, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=" + timeout.String()}
}

func expectedOSImage(osFamily string) string {
	if osFamily == config.OSFCOS {
		return "Fedora CoreOS"
	}
	return "Flatcar Container Linux"
}

var replaceStepNames = []string{
	"preconditions", "drain", "remove-member", "destroy-vm", "forget-host-key",
	"join-material", "deploy", "wait-ready", "smoke-test",
	"uncordon", "post-checks", "invalidate-token",
}

func newReplaceSteps(detail string) []rehearseStep {
	steps := make([]rehearseStep, len(replaceStepNames))
	for i, name := range replaceStepNames {
		steps[i] = rehearseStep{Name: name, Status: "SKIP", Duration: "0s", Detail: detail}
	}
	return steps
}

func newReplaceReport(cfg *config.Config, spec rehearseNodeSpec, opts replaceNodeOptions) replaceReport {
	return replaceReport{
		Node: spec.Node.Name, IP: spec.Node.IP, VMID: spec.VMID,
		OS: cfg.OSForNode(spec.Node), InitNode: spec.InitNode.Name, Image: replaceImageDescription(opts),
	}
}

func plannedReplaceReport(cfg *config.Config, spec rehearseNodeSpec, opts replaceNodeOptions) replaceReport {
	report := newReplaceReport(cfg, spec, opts)
	report.Plan = true
	report.Verdict = "PLAN"
	report.TotalDuration = "0s"
	report.Steps = newReplaceSteps("")
	v, n := spec.VMID, spec.Node.Name
	details := []string{
		fmt.Sprintf("node %s exists in Kubernetes and cluster.nodes; every other node Ready; all etcd members healthy with >=%d healthy besides %s; VM %d named %s exists",
			n, replaceMinOtherHealthyMembers, n, v, n),
		fmt.Sprintf("kubectl cordon %s; kubectl %s", n, strings.Join(replaceDrainArgs(spec, opts.Timeout), " ")),
		fmt.Sprintf("remove the etcd member named %s / peer %s via etcd-%s; kubectl delete node %s",
			n, "https://"+net.JoinHostPort(spec.Node.IP, "2380"), spec.InitNode.Name, n),
		fmt.Sprintf("%s; %s (deletes every disk attached to VM %d)", qmStopCommand(v), qmDestroyCommand(v), v),
		fmt.Sprintf("ssh-keygen -R %s", spec.Node.IP),
		fmt.Sprintf("mint 30m bootstrap token + certificate key on %s (%s)", spec.InitNode.Name, spec.InitNode.IP),
		fmt.Sprintf("render %s Ignition/join config; create and start VM %d via %s (image: %s); then kubeadm join control plane", report.OS, v, spec.Provider, report.Image),
		fmt.Sprintf("wait up to %s for %s Ready and CNI", opts.Timeout, n),
		fmt.Sprintf("run %s on %s and resolve %s", spec.CheckImage, n, rehearseKubernetesFQDN()),
		fmt.Sprintf("kubectl uncordon %s", n),
		fmt.Sprintf("etcd member count back to the precondition count, all healthy; %s Ready with osImage %q; cilium agent Ready on %s",
			n, expectedOSImage(report.OS), n),
		fmt.Sprintf("kubeadm token delete on %s (also on failure)", spec.InitNode.Name),
	}
	for i := range report.Steps {
		report.Steps[i].Detail = "plan: " + details[i]
	}
	return report
}

// runReplaceNode executes the replacement in order and stops at the first
// failure.
func runReplaceNode(ctx context.Context, spec rehearseNodeSpec, opts replaceNodeOptions, ops replaceOperations) (replaceReport, error) {
	started := rehearseNowFn()
	cfg := rehearseConfigFn()
	report := newReplaceReport(cfg, spec, opts)
	report.Steps = newReplaceSteps("not run")
	step := func(name string) *rehearseStep {
		return &report.Steps[slices.Index(replaceStepNames, name)]
	}
	finish := func(err error) (replaceReport, error) {
		report.TotalDuration = rehearseNowFn().Sub(started).Round(time.Millisecond).String()
		report.Verdict = "PASS"
		if err != nil {
			report.Verdict = "FAIL"
		}
		return report, err
	}

	var pre replacePreflight
	if _, err := timedRehearseStep(step("preconditions"), func() (string, error) {
		var err error
		pre, err = ops.Preconditions(ctx, spec)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d etcd members healthy; others Ready; VM %d present; init node %s", pre.EtcdMembers, spec.VMID, spec.InitNode.Name), nil
	}); err != nil {
		return finish(err)
	}
	if _, err := timedRehearseStep(step("drain"), func() (string, error) {
		return "cordoned and drained", ops.Drain(ctx, spec, opts.Timeout)
	}); err != nil {
		report.RecoveryCommands = []string{fmt.Sprintf("kubectl uncordon %s", spec.Node.Name)}
		return finish(err)
	}
	if _, err := timedRehearseStep(step("remove-member"), func() (string, error) {
		return "etcd member removed; Node object deleted", ops.RemoveFromCluster(ctx, spec)
	}); err != nil {
		return finish(err)
	}

	if _, err := timedRehearseStep(step("destroy-vm"), func() (string, error) {
		return fmt.Sprintf("VM %d stopped and destroyed", spec.VMID), ops.DestroyVM(ctx, spec)
	}); err != nil {
		return finish(err)
	}
	_, _ = timedRehearseStep(step("forget-host-key"), func() (string, error) {
		ops.ForgetHostKey(ctx, spec)
		return "removed " + spec.Node.IP + " from known_hosts", nil
	})

	var material *flatcarinternal.KubeadmResult
	runErr := func() error {
		_, err := timedRehearseStep(step("join-material"), func() (string, error) {
			var err error
			material, err = ops.CreateJoinMaterial(ctx, spec)
			if err != nil {
				return "", err
			}
			return "fresh 30m token minted on " + spec.InitNode.Name, nil
		})
		if err != nil {
			return err
		}
		if _, err := timedRehearseStep(step("deploy"), func() (string, error) {
			return "Ignition staged; VM created and booted; kubeadm join completed", ops.Deploy(ctx, spec, opts, *material)
		}); err != nil {
			return err
		}
		if _, err := timedRehearseStep(step("wait-ready"), func() (string, error) {
			ready, err := ops.WaitReady(ctx, spec, opts.Timeout)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Ready; kubelet=%s; CNI=%s", ready.KubeletVersion, passFail(ready.CNIReady)), nil
		}); err != nil {
			return err
		}
		if _, err := timedRehearseStep(step("smoke-test"), func() (string, error) {
			return "pinned pod ran and resolved kubernetes.default; pod deleted", ops.SmokeTest(ctx, spec, opts.Timeout)
		}); err != nil {
			return err
		}
		if _, err := timedRehearseStep(step("uncordon"), func() (string, error) {
			return "schedulable", ops.Uncordon(ctx, spec)
		}); err != nil {
			return err
		}
		_, err = timedRehearseStep(step("post-checks"), func() (string, error) {
			return ops.PostChecks(ctx, spec, pre)
		})
		return err
	}()

	var tokenErr error
	if material != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rehearseCleanupTimeout)
		defer cancel()
		_, tokenErr = timedRehearseStep(step("invalidate-token"), func() (string, error) {
			return "bootstrap token deleted", ops.InvalidateToken(cleanupCtx, spec, material.BootstrapToken)
		})
		if tokenErr != nil {
			report.RecoveryCommands = append(report.RecoveryCommands, fmt.Sprintf("ssh %s@%s 'sudo kubeadm token delete %s'", spec.SSHUser, spec.InitNode.IP, tokenIdentifier(material.BootstrapToken)))
		}
	}
	return finish(errors.Join(runErr, tokenErr))
}

func writeReplaceReport(cmd *cobra.Command, report replaceReport, output string) error {
	var rendered string
	var err error
	if output == "json" {
		rendered, err = ui.RenderJSON(report)
	} else {
		rows := make([][]string, 0, len(report.Steps)+1)
		for _, s := range report.Steps {
			rows = append(rows, []string{s.Name, s.Status, s.Duration, s.Detail})
		}
		rows = append(rows, []string{"TOTAL", report.Verdict, report.TotalDuration,
			fmt.Sprintf("%s VMID=%d IP=%s os=%s init=%s", report.Node, report.VMID, report.IP, report.OS, report.InitNode)})
		rendered = ui.Table([]string{"STEP", "STATUS", "DURATION", "DETAIL"}, rows)
		if len(report.RecoveryCommands) > 0 {
			rendered += "\n\nManual recovery (nothing below was run):\n  " + strings.Join(report.RecoveryCommands, "\n  ")
		}
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), rendered)
	return err
}

// --- live operations ---

type replaceNodeState struct {
	Name         string
	Ready        bool
	ControlPlane bool
	OSImage      string
}

func parseReplaceNodes(data string) ([]replaceNodeState, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(data), &list); err != nil {
		return nil, fmt.Errorf("parse Kubernetes node inventory: %w", err)
	}
	out := make([]replaceNodeState, 0, len(list.Items))
	for _, raw := range list.Items {
		node, err := parseReplaceNode([]byte(raw))
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, nil
}

func parseReplaceNode(data []byte) (replaceNodeState, error) {
	var node struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			NodeInfo struct {
				OSImage string `json:"osImage"`
			} `json:"nodeInfo"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &node); err != nil {
		return replaceNodeState{}, fmt.Errorf("parse Kubernetes node: %w", err)
	}
	state := replaceNodeState{Name: node.Metadata.Name, OSImage: node.Status.NodeInfo.OSImage}
	_, state.ControlPlane = node.Metadata.Labels["node-role.kubernetes.io/control-plane"]
	for _, c := range node.Status.Conditions {
		if c.Type == "Ready" {
			state.Ready = c.Status == "True"
		}
	}
	return state, nil
}

// checkReplaceNodes requires the target to exist, every other node to be
// Ready, and the init node to be a Ready control plane.
func checkReplaceNodes(nodes []replaceNodeState, spec rehearseNodeSpec) error {
	foundTarget, foundInit := false, false
	for _, node := range nodes {
		switch {
		case node.Name == spec.Node.Name:
			foundTarget = true
			continue
		case !node.Ready:
			return fmt.Errorf("node %s is not Ready; replace one node at a time on an otherwise healthy cluster", node.Name)
		}
		if node.Name == spec.InitNode.Name {
			if !node.ControlPlane {
				return fmt.Errorf("init node %s is not a control plane", node.Name)
			}
			foundInit = true
		}
	}
	if !foundTarget {
		return fmt.Errorf("kubernetes node %q does not exist", spec.Node.Name)
	}
	if !foundInit {
		return fmt.Errorf("init node %s is not a Kubernetes node", spec.InitNode.Name)
	}
	return nil
}

type replaceEtcdMember struct {
	ID         uint64
	Name       string
	PeerURLs   []string
	ClientURLs []string
	Healthy    bool
}

// replaceEtcdMembers lists members through the init node's etcd pod and marks
// each healthy when one of its client URLs passes endpoint health.
func replaceEtcdMembers(ctx context.Context, spec rehearseNodeSpec) ([]replaceEtcdMember, error) {
	pod := "etcd-" + spec.InitNode.Name
	raw, err := rehearseCommandFn(ctx, "kubectl", rehearsalEtcdctl(pod, "member", "list", "-w", "json")...)
	if err != nil {
		return nil, fmt.Errorf("etcd member list: %w", err)
	}
	var list struct {
		Members []struct {
			ID         uint64   `json:"ID"`
			Name       string   `json:"name"`
			PeerURLs   []string `json:"peerURLs"`
			ClientURLs []string `json:"clientURLs"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("parse etcd member list: %w", err)
	}
	// endpoint health exits non-zero when any endpoint is unhealthy; its JSON
	// still says which, so parse it before giving up.
	healthRaw, healthErr := rehearseCommandFn(ctx, "kubectl", rehearsalEtcdctl(pod, "endpoint", "health", "--cluster", "-w", "json")...)
	var health []struct {
		Endpoint string `json:"endpoint"`
		Health   bool   `json:"health"`
	}
	if err := json.Unmarshal([]byte(healthRaw), &health); err != nil {
		if healthErr != nil {
			return nil, fmt.Errorf("etcd endpoint health: %w", healthErr)
		}
		return nil, fmt.Errorf("parse etcd endpoint health: %w", err)
	}
	healthy := map[string]bool{}
	for _, h := range health {
		if h.Health {
			healthy[h.Endpoint] = true
		}
	}
	members := make([]replaceEtcdMember, 0, len(list.Members))
	for _, m := range list.Members {
		member := replaceEtcdMember{ID: m.ID, Name: m.Name, PeerURLs: m.PeerURLs, ClientURLs: m.ClientURLs}
		for _, u := range m.ClientURLs {
			member.Healthy = member.Healthy || healthy[u]
		}
		members = append(members, member)
	}
	return members, nil
}

func isTargetEtcdMember(member replaceEtcdMember, spec rehearseNodeSpec) bool {
	return member.Name == spec.Node.Name || slices.Contains(member.PeerURLs, "https://"+net.JoinHostPort(spec.Node.IP, "2380"))
}

// checkReplaceQuorum requires every member healthy, exactly one member for the
// target, and enough healthy members besides it that quorum survives both the
// removal and one further failure during the rebuild.
func checkReplaceQuorum(members []replaceEtcdMember, spec rehearseNodeSpec) error {
	targets, others := 0, 0
	for _, m := range members {
		if !m.Healthy {
			return fmt.Errorf("etcd member %s (%x) is not healthy", m.Name, m.ID)
		}
		if isTargetEtcdMember(m, spec) {
			targets++
		} else {
			others++
		}
	}
	// Zero is a resumed run: an earlier attempt already removed the member.
	if targets > 1 {
		return fmt.Errorf("expected at most one etcd member for %s, found %d", spec.Node.Name, targets)
	}
	if others < replaceMinOtherHealthyMembers {
		return fmt.Errorf("only %d healthy etcd members besides %s (need at least %d so quorum survives its removal)", others, spec.Node.Name, replaceMinOtherHealthyMembers)
	}
	return nil
}

func (realReplaceOperations) Preconditions(ctx context.Context, spec rehearseNodeSpec) (replacePreflight, error) {
	ready, err := rehearseCommandFn(ctx, "kubectl", "get", "--raw=/readyz")
	if err != nil {
		return replacePreflight{}, fmt.Errorf("apiserver readiness: %w", err)
	}
	if strings.TrimSpace(ready) != "ok" {
		return replacePreflight{}, fmt.Errorf("apiserver readiness returned %q", strings.TrimSpace(ready))
	}
	nodesJSON, err := rehearseCommandFn(ctx, "kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		return replacePreflight{}, fmt.Errorf("list Kubernetes nodes: %w", err)
	}
	nodes, err := parseReplaceNodes(nodesJSON)
	if err != nil {
		return replacePreflight{}, err
	}
	if err := checkReplaceNodes(nodes, spec); err != nil {
		return replacePreflight{}, err
	}
	members, err := replaceEtcdMembers(ctx, spec)
	if err != nil {
		return replacePreflight{}, err
	}
	if err := checkReplaceQuorum(members, spec); err != nil {
		return replacePreflight{}, err
	}
	err = rehearseWithVMLifecycleFn(spec.Provider, func(lifecycle vmprov.VMLifecycle) error {
		summaries, err := lifecycle.VMSummaries()
		if err != nil {
			return err
		}
		return checkReplaceVMs(summaries, spec)
	})
	if err != nil {
		return replacePreflight{}, fmt.Errorf("hypervisor inventory: %w", err)
	}
	// Expected membership once the node rejoins: every other member plus the
	// rebuilt node, whether or not an earlier attempt already removed it.
	expected := 1
	for _, m := range members {
		if !isTargetEtcdMember(m, spec) {
			expected++
		}
	}
	return replacePreflight{EtcdMembers: expected}, nil
}

func checkReplaceVMs(summaries []vmprov.VMSummary, spec rehearseNodeSpec) error {
	found := false
	for _, vm := range summaries {
		if vm.ID == strconv.Itoa(spec.VMID) {
			if vm.Name != spec.Node.Name {
				return fmt.Errorf("VMID %d is VM %q, expected %s", spec.VMID, vm.Name, spec.Node.Name)
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("VM %s with VMID %d does not exist on %s", spec.Node.Name, spec.VMID, spec.Provider)
	}
	return nil
}

func pveRun(ctx context.Context, command string) (string, error) {
	out, err := replacePVECommandFn(ctx, command)
	if err != nil {
		return out, fmt.Errorf("pve: %s: %w", command, err)
	}
	return out, nil
}

func (realReplaceOperations) Drain(ctx context.Context, spec rehearseNodeSpec, timeout time.Duration) error {
	if _, err := rehearseCommandFn(ctx, "kubectl", "cordon", spec.Node.Name); err != nil {
		return fmt.Errorf("cordon: %w", err)
	}
	if _, err := rehearseCommandFn(ctx, "kubectl", replaceDrainArgs(spec, timeout)...); err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	return nil
}

// replaceAPIRetries x replaceAPIRetryDelay bounds the wait for a VIP failover.
var (
	replaceAPIRetries    = 18
	replaceAPIRetryDelay = 10 * time.Second
)

func (realReplaceOperations) RemoveFromCluster(ctx context.Context, spec rehearseNodeSpec) error {
	if err := removeRehearsalEtcdMember(ctx, spec); err != nil {
		return fmt.Errorf("remove etcd member: %w", err)
	}
	// Removing the member can take down the API server the control-plane VIP
	// points at (when the target holds the kube-vip lease) until the VIP fails
	// over; retry through that window instead of failing mid-rebuild.
	var err error
	for attempt := 0; attempt < replaceAPIRetries; attempt++ {
		if _, err = rehearseCommandFn(ctx, "kubectl", "delete", "node", spec.Node.Name, "--ignore-not-found=true"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("delete node: %w", ctx.Err())
		case <-time.After(replaceAPIRetryDelay):
		}
	}
	return fmt.Errorf("delete node: %w", err)
}

// DestroyVM stops the node's VM and destroys it with every disk attached to
// it. Disks of other VMs are untouched (--destroy-unreferenced-disks 0).
func (realReplaceOperations) DestroyVM(ctx context.Context, spec rehearseNodeSpec) error {
	if _, err := pveRun(ctx, qmStopCommand(spec.VMID)); err != nil {
		return err
	}
	_, err := pveRun(ctx, qmDestroyCommand(spec.VMID))
	return err
}

func (realReplaceOperations) ForgetHostKey(_ context.Context, spec rehearseNodeSpec) {
	forgetRehearsalHostKey(spec.Node.IP)
}

func (realReplaceOperations) Deploy(ctx context.Context, spec rehearseNodeSpec, opts replaceNodeOptions, material flatcarinternal.KubeadmResult) error {
	return rehearseDeployNodeFn(ctx, cmdflatcar.RehearsalDeployOptions{
		Node: spec.Node, Provider: spec.Provider, ImagePath: opts.ImagePath, ImageVolume: opts.ImageVolume,
		Join: material, SSHUser: spec.SSHUser, Timeout: opts.Timeout,
	})
}

func (realReplaceOperations) Uncordon(ctx context.Context, spec rehearseNodeSpec) error {
	_, err := rehearseCommandFn(ctx, "kubectl", "uncordon", spec.Node.Name)
	return err
}

func (realReplaceOperations) PostChecks(ctx context.Context, spec rehearseNodeSpec, pre replacePreflight) (string, error) {
	members, err := replaceEtcdMembers(ctx, spec)
	if err != nil {
		return "", err
	}
	for _, m := range members {
		if !m.Healthy {
			return "", fmt.Errorf("etcd member %s (%x) is not healthy", m.Name, m.ID)
		}
	}
	if len(members) != pre.EtcdMembers {
		return "", fmt.Errorf("etcd has %d members, expected %d", len(members), pre.EtcdMembers)
	}
	raw, err := rehearseCommandFn(ctx, "kubectl", "get", "node", spec.Node.Name, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("get node: %w", err)
	}
	node, err := parseReplaceNode([]byte(raw))
	if err != nil {
		return "", err
	}
	want := expectedOSImage(rehearseConfigFn().OSForNode(spec.Node))
	if !node.Ready {
		return "", fmt.Errorf("node %s is not Ready", spec.Node.Name)
	}
	if !strings.Contains(node.OSImage, want) {
		return "", fmt.Errorf("node %s runs %q, expected %s", spec.Node.Name, node.OSImage, want)
	}
	pods, err := rehearseCommandFn(ctx, "kubectl", "-n", "kube-system", "get", "pods", "-l", "k8s-app=cilium",
		"--field-selector", "spec.nodeName="+spec.Node.Name, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("list cilium agents: %w", err)
	}
	if !anyPodReady(pods) {
		return "", fmt.Errorf("no Ready cilium agent on %s", spec.Node.Name)
	}
	return fmt.Sprintf("%d etcd members healthy; %s Ready on %s; cilium agent Ready", len(members), spec.Node.Name, node.OSImage), nil
}

func anyPodReady(data string) bool {
	var pods struct {
		Items []struct {
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(data), &pods) != nil {
		return false
	}
	for _, pod := range pods.Items {
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				return true
			}
		}
	}
	return false
}
