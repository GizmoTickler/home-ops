package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	cmdflatcar "homeops-cli/cmd/flatcar"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	flatcarinternal "homeops-cli/internal/flatcar"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"

	"github.com/spf13/cobra"
)

const (
	rehearseDefaultTimeout = 15 * time.Minute
	rehearseTokenTTL       = 30 * time.Minute
	rehearsePollInterval   = 5 * time.Second
	rehearseCleanupTimeout = 5 * time.Minute
)

var (
	rehearseConfigFn  = config.Get
	rehearseConfirmFn = func(message string, defaultYes bool) (bool, error) {
		return ui.Confirm(message, defaultYes)
	}
	rehearseNowFn   = time.Now
	rehearseSleepFn = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	rehearseCommandFn         = runRehearseCommand
	rehearseWithVMLifecycleFn = vmlifecycle.WithVMLifecycle
	rehearseDeployNodeFn      = cmdflatcar.DeployRehearsalNode
	rehearseOrchestratorFn    = func(spec rehearseNodeSpec) rehearseKubeadmOrchestrator {
		return flatcarinternal.NewOrchestrator(flatcarinternal.OrchestratorConfig{
			SSHUser: spec.SSHUser,
			Port:    strconv.Itoa(rehearseConfigFn().Cluster.NodeSSHPort),
		})
	}
)

type rehearseNodeOptions struct {
	Plan        bool
	Keep        bool
	Timeout     time.Duration
	Output      string
	Provider    string
	ImagePath   string
	ImageVolume string
}

type rehearseNodeSpec struct {
	Node       config.Node
	InitNode   config.Node
	Provider   string
	VMID       int
	CheckImage string
	SSHUser    string
}

type rehearseStep struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Duration string `json:"duration"`
	Detail   string `json:"detail,omitempty"`
}

type rehearseReport struct {
	Node            string         `json:"node"`
	IP              string         `json:"ip"`
	VMID            int            `json:"vmid"`
	Provider        string         `json:"provider"`
	Plan            bool           `json:"plan"`
	Keep            bool           `json:"keep"`
	Steps           []rehearseStep `json:"steps"`
	Verdict         string         `json:"verdict"`
	TotalDuration   string         `json:"total_duration"`
	CleanupCommands []string       `json:"cleanup_commands,omitempty"`
}

type rehearseNodeReady struct {
	KubeletVersion string
	CNIReady       bool
}

type rehearseOperations interface {
	Preconditions(context.Context, rehearseNodeSpec) (string, error)
	CreateJoinMaterial(context.Context, rehearseNodeSpec) (*flatcarinternal.KubeadmResult, error)
	Deploy(context.Context, rehearseNodeSpec, rehearseNodeOptions, flatcarinternal.KubeadmResult) error
	WaitReady(context.Context, rehearseNodeSpec, time.Duration) (rehearseNodeReady, error)
	SmokeTest(context.Context, rehearseNodeSpec, time.Duration) error
	DrainAndDeleteNode(context.Context, rehearseNodeSpec, time.Duration) error
	DeleteVM(context.Context, rehearseNodeSpec) error
	InvalidateToken(context.Context, rehearseNodeSpec, string) error
}

type realRehearseOperations struct{}

type rehearseKubeadmOrchestrator interface {
	CreateJoinMaterial(string, time.Duration) (*flatcarinternal.KubeadmResult, error)
	DeleteBootstrapToken(string, string) error
}

func newRehearseNodeCommand() *cobra.Command {
	opts := rehearseNodeOptions{Timeout: rehearseDefaultTimeout, Output: "table"}
	cmd := &cobra.Command{
		Use:          "rehearse-node",
		Short:        "Prove disposable Flatcar deployment, kubeadm join, CNI, and DNS",
		SilenceUsage: true,
		Long: `Deploy a configured disposable Flatcar node, join it to the live kubeadm
cluster, verify node/CNI readiness and pod DNS, then drain and destroy it. The
workflow refuses production identities and existing node/VM collisions. Unless
--keep is set, teardown runs after success and after any post-deploy failure.`,
		Example: `  # Inspect every action without contacting Kubernetes or the hypervisor
  homeops-cli cluster rehearse-node --plan

  # Proxmox rehearsal from a pre-staged Flatcar image volume
  homeops-cli cluster rehearse-node --image-volume nvme-mirror:vm-900-disk-0 --yes

  # Keep a failed/successful drill node for inspection and emit JSON
  homeops-cli cluster rehearse-node --image-path /var/lib/vz/template/iso/flatcar.raw --keep --output json --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRehearseNodeCommand(cmd, opts, realRehearseOperations{})
		},
	}
	cmd.Flags().BoolVar(&opts.Plan, "plan", false, "print the complete drill plan without executing or prompting")
	cmd.Flags().BoolVar(&opts.Keep, "keep", false, "leave the test node, VM, disks, and bootstrap token for inspection")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", rehearseDefaultTimeout, "maximum wait for SSH, node readiness, and smoke pod readiness")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "table", "output format: table or json")
	cmd.Flags().StringVar(&opts.Provider, "provider", "", "hypervisor (default: hypervisors.default): proxmox, truenas, or vsphere")
	cmd.Flags().StringVar(&opts.ImagePath, "image-path", "", "[proxmox] path on Proxmox to import the Flatcar disk image from")
	cmd.Flags().StringVar(&opts.ImageVolume, "image-volume", "", "[proxmox] existing Flatcar image volume to attach")
	return cmd
}

func executeRehearseNodeCommand(cmd *cobra.Command, opts rehearseNodeOptions, operations rehearseOperations) error {
	if err := ui.ValidateOutputFormat(opts.Output); err != nil {
		return err
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	spec, err := buildRehearseNodeSpec(rehearseConfigFn(), opts.Provider)
	if err != nil {
		return err
	}

	if opts.Plan {
		report := plannedRehearseReport(spec, opts)
		return writeRehearseReport(cmd, report, opts.Output)
	}
	if spec.Provider == "proxmox" && strings.TrimSpace(opts.ImagePath) == "" && strings.TrimSpace(opts.ImageVolume) == "" {
		return fmt.Errorf("one of --image-path or --image-volume is required for Proxmox execution (use --plan to inspect without one)")
	}
	confirmed, err := rehearseConfirmFn(fmt.Sprintf("Deploy, join, test, and destroy disposable node %s (VMID %d, IP %s)?", spec.Node.Name, spec.VMID, spec.Node.IP), false)
	if err != nil {
		return fmt.Errorf("confirm node rehearsal: %w", err)
	}
	if !confirmed {
		return fmt.Errorf("node rehearsal cancelled")
	}
	spec.SSHUser = resolveRehearseSSHUser(rehearseConfigFn())

	report, runErr := runRehearseNode(cmd.Context(), spec, opts, operations)
	if err := writeRehearseReport(cmd, report, opts.Output); err != nil {
		return err
	}
	return runErr
}

func buildRehearseNodeSpec(cfg *config.Config, providerName string) (rehearseNodeSpec, error) {
	if cfg == nil || cfg.Cluster.TestNode == nil {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.test_node is required for cluster rehearse-node")
	}
	node := *cfg.Cluster.TestNode
	if strings.TrimSpace(node.Name) == "" {
		node.Name = "k8s-test"
	}
	if strings.TrimSpace(node.IP) == "" || net.ParseIP(node.IP) == nil {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.test_node.ip must be a valid IP address")
	}
	profile := node.VM.ForProvider("flatcar")
	if profile.VMID <= 0 {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.test_node.vm.vmid must be greater than zero")
	}
	if strings.TrimSpace(profile.Mac) == "" {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.test_node.vm.mac is required")
	}
	if _, err := net.ParseMAC(profile.Mac); err != nil {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.test_node.vm.mac %q is invalid: %w", profile.Mac, err)
	}
	if len(cfg.Cluster.Nodes) == 0 || strings.TrimSpace(cfg.Cluster.Nodes[0].IP) == "" {
		return rehearseNodeSpec{}, fmt.Errorf("cluster.nodes must contain an init node with an IP")
	}
	for _, production := range cfg.Cluster.Nodes {
		if node.Name == production.Name {
			return rehearseNodeSpec{}, fmt.Errorf("refusing test node name %q: it matches production cluster.nodes entry", node.Name)
		}
		if node.IP == production.IP {
			return rehearseNodeSpec{}, fmt.Errorf("refusing test node IP %s: it matches production node %s", node.IP, production.Name)
		}
		for _, vmid := range productionVMIDs(production) {
			if profile.VMID == vmid {
				return rehearseNodeSpec{}, fmt.Errorf("refusing test node VMID %d: it matches production node %s", profile.VMID, production.Name)
			}
		}
		for _, mac := range productionMACs(production) {
			if strings.EqualFold(profile.Mac, mac) {
				return rehearseNodeSpec{}, fmt.Errorf("refusing test node MAC %s: it matches production node %s", profile.Mac, production.Name)
			}
		}
	}
	if strings.TrimSpace(providerName) == "" {
		providerName = cfg.Hypervisors.Default
		if strings.TrimSpace(providerName) == "" {
			providerName = "proxmox"
		}
	}
	providerName, err := vmlifecycle.NormalizeVMProvider(providerName)
	if err != nil {
		return rehearseNodeSpec{}, err
	}
	if strings.TrimSpace(cfg.Volsync.CheckImage) == "" {
		return rehearseNodeSpec{}, fmt.Errorf("volsync.check_image is required")
	}
	return rehearseNodeSpec{
		Node:       node,
		InitNode:   cfg.Cluster.Nodes[0],
		Provider:   providerName,
		VMID:       profile.VMID,
		CheckImage: cfg.Volsync.CheckImage,
		SSHUser:    "<node-ssh-user>",
	}, nil
}

func resolveRehearseSSHUser(cfg *config.Config) string {
	if cfg != nil {
		if user := strings.TrimSpace(cfg.ResolveSecretSilent(config.KeyNodeSSHUser)); user != "" {
			return user
		}
	}
	return "core"
}

func productionVMIDs(node config.Node) []int {
	values := []int{node.VM.VMID, node.VM.Providers.Talos.VMID, node.VM.Providers.Flatcar.VMID, node.VM.Providers.VSphere.VMID}
	return nonzeroInts(values)
}

func productionMACs(node config.Node) []string {
	return nonemptyStrings([]string{node.VM.Mac, node.VM.Providers.Talos.Mac, node.VM.Providers.Flatcar.Mac, node.VM.Providers.VSphere.Mac})
}

func nonzeroInts(values []int) []int {
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value > 0 {
			out = append(out, value)
		}
	}
	return out
}

func nonemptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func plannedRehearseReport(spec rehearseNodeSpec, opts rehearseNodeOptions) rehearseReport {
	image := opts.ImagePath
	if image == "" {
		image = opts.ImageVolume
	}
	if image == "" && spec.Provider == "proxmox" {
		image = "<required at execution: --image-path or --image-volume>"
	}
	steps := []rehearseStep{
		{Name: "preconditions", Status: "SKIP", Duration: "0s", Detail: fmt.Sprintf("plan: verify apiserver; refuse node %s or VMID %d collisions", spec.Node.Name, spec.VMID)},
		{Name: "deploy", Status: "SKIP", Duration: "0s", Detail: fmt.Sprintf("plan: mint 30m token; render Ignition/join config; deploy and power on via %s (image %s)", spec.Provider, image)},
		{Name: "join-watch", Status: "SKIP", Duration: "0s", Detail: fmt.Sprintf("plan: wait up to %s for Ready and CNI; report kubelet", opts.Timeout)},
		{Name: "smoke-test", Status: "SKIP", Duration: "0s", Detail: fmt.Sprintf("plan: run %s on %s and resolve kubernetes.default", spec.CheckImage, spec.Node.Name)},
		{Name: "teardown", Status: "SKIP", Duration: "0s", Detail: plannedTeardownDetail(opts.Keep)},
	}
	return rehearseReport{
		Node: spec.Node.Name, IP: spec.Node.IP, VMID: spec.VMID, Provider: spec.Provider,
		Plan: true, Keep: opts.Keep, Steps: steps, Verdict: "PLAN", TotalDuration: "0s",
		CleanupCommands: cleanupCommands(spec, "<token-id>"),
	}
}

func plannedTeardownDetail(keep bool) string {
	if keep {
		return "plan: --keep leaves the node, VM, disks, and token for manual cleanup"
	}
	return "plan: drain/delete node; power off/delete VM and disks; invalidate token (also on failure)"
}

func runRehearseNode(ctx context.Context, spec rehearseNodeSpec, opts rehearseNodeOptions, operations rehearseOperations) (rehearseReport, error) {
	started := rehearseNowFn()
	report := rehearseReport{
		Node: spec.Node.Name, IP: spec.Node.IP, VMID: spec.VMID, Provider: spec.Provider, Keep: opts.Keep,
		Steps: []rehearseStep{
			{Name: "preconditions", Status: "SKIP", Duration: "0s", Detail: "not run"},
			{Name: "deploy", Status: "SKIP", Duration: "0s", Detail: "not run"},
			{Name: "join-watch", Status: "SKIP", Duration: "0s", Detail: "not run"},
			{Name: "smoke-test", Status: "SKIP", Duration: "0s", Detail: "not run"},
			{Name: "teardown", Status: "SKIP", Duration: "0s", Detail: "no disposable resources created"},
		},
	}

	_, runErr := timedRehearseStep(&report.Steps[0], func() (string, error) {
		return operations.Preconditions(ctx, spec)
	})
	if runErr != nil {
		return finalizeRehearseReport(report, started, runErr), runErr
	}

	var material *flatcarinternal.KubeadmResult
	resourcesMayExist := false
	_, runErr = timedRehearseStep(&report.Steps[1], func() (string, error) {
		var err error
		material, err = operations.CreateJoinMaterial(ctx, spec)
		if err != nil {
			return "", err
		}
		resourcesMayExist = true
		if err := operations.Deploy(ctx, spec, opts, *material); err != nil {
			return "", err
		}
		return "fresh 30m token minted; Ignition staged; VM powered on; kubeadm join completed", nil
	})

	if runErr == nil {
		_, runErr = timedRehearseStep(&report.Steps[2], func() (string, error) {
			ready, err := operations.WaitReady(ctx, spec, opts.Timeout)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Ready; kubelet=%s; CNI=%s", ready.KubeletVersion, passFail(ready.CNIReady)), nil
		})
	}
	if runErr == nil {
		_, runErr = timedRehearseStep(&report.Steps[3], func() (string, error) {
			if err := operations.SmokeTest(ctx, spec, opts.Timeout); err != nil {
				return "", err
			}
			return "pinned pod ran and resolved kubernetes.default; pod deleted", nil
		})
	}

	var teardownErr error
	if opts.Keep {
		report.Steps[4].Detail = "--keep set; disposable resources and token retained for inspection"
		tokenID := "<token-id>"
		if material != nil {
			tokenID = tokenIdentifier(material.BootstrapToken)
		}
		report.CleanupCommands = cleanupCommands(spec, tokenID)
	} else if resourcesMayExist {
		cleanupTimeout := rehearseCleanupTimeout
		if opts.Timeout+time.Minute > cleanupTimeout {
			cleanupTimeout = opts.Timeout + time.Minute
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		_, teardownErr = timedRehearseStep(&report.Steps[4], func() (string, error) {
			var cleanupErrors []error
			if err := operations.DrainAndDeleteNode(cleanupCtx, spec, opts.Timeout); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("node cleanup: %w", err))
			}
			if err := operations.DeleteVM(cleanupCtx, spec); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("VM cleanup: %w", err))
			}
			if material != nil {
				if err := operations.InvalidateToken(cleanupCtx, spec, material.BootstrapToken); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("token cleanup: %w", err))
				}
			}
			return "node drained/deleted; VM and disks deleted; bootstrap token invalidated", errors.Join(cleanupErrors...)
		})
	}

	combined := errors.Join(runErr, teardownErr)
	return finalizeRehearseReport(report, started, combined), combined
}

func timedRehearseStep(step *rehearseStep, fn func() (string, error)) (string, error) {
	started := rehearseNowFn()
	detail, err := fn()
	step.Duration = rehearseNowFn().Sub(started).Round(time.Millisecond).String()
	step.Detail = detail
	if err != nil {
		step.Status = "FAIL"
		step.Detail = err.Error()
		return detail, err
	}
	step.Status = "PASS"
	return detail, nil
}

func finalizeRehearseReport(report rehearseReport, started time.Time, err error) rehearseReport {
	report.TotalDuration = rehearseNowFn().Sub(started).Round(time.Millisecond).String()
	if err != nil {
		report.Verdict = "FAIL"
	} else {
		report.Verdict = "PASS"
	}
	return report
}

func passFail(value bool) string {
	if value {
		return "ready"
	}
	return "not-ready"
}

func writeRehearseReport(cmd *cobra.Command, report rehearseReport, output string) error {
	rendered, err := renderRehearseReport(report, output)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), rendered)
	return err
}

func renderRehearseReport(report rehearseReport, output string) (string, error) {
	if output == "json" {
		return ui.RenderJSON(report)
	}
	if err := ui.ValidateOutputFormat(output); err != nil {
		return "", err
	}
	rows := make([][]string, 0, len(report.Steps)+1)
	for _, step := range report.Steps {
		rows = append(rows, []string{step.Name, step.Status, step.Duration, step.Detail})
	}
	rows = append(rows, []string{"TOTAL", report.Verdict, report.TotalDuration, fmt.Sprintf("%s %s VMID=%d IP=%s", report.Provider, report.Node, report.VMID, report.IP)})
	rendered := ui.Table([]string{"STEP", "STATUS", "DURATION", "DETAIL"}, rows)
	if len(report.CleanupCommands) > 0 {
		rendered += "\n\nCleanup commands:\n  " + strings.Join(report.CleanupCommands, "\n  ")
	}
	return rendered, nil
}

func cleanupCommands(spec rehearseNodeSpec, tokenID string) []string {
	return []string{
		fmt.Sprintf("kubectl drain %s --ignore-daemonsets --delete-emptydir-data --force", spec.Node.Name),
		fmt.Sprintf("kubectl delete node %s --ignore-not-found", spec.Node.Name),
		fmt.Sprintf("homeops-cli vm %s poweroff --name %s --force", spec.Provider, spec.Node.Name),
		fmt.Sprintf("homeops-cli vm %s delete --name %s --force", spec.Provider, spec.Node.Name),
		fmt.Sprintf("ssh %s@%s 'sudo kubeadm token delete %s'", spec.SSHUser, spec.InitNode.IP, tokenID),
	}
}

func tokenIdentifier(token string) string {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "<token-id>"
	}
	return parts[0]
}

func (realRehearseOperations) Preconditions(ctx context.Context, spec rehearseNodeSpec) (string, error) {
	ready, err := rehearseCommandFn(ctx, "kubectl", "get", "--raw=/readyz")
	if err != nil {
		return "", fmt.Errorf("apiserver readiness: %w", err)
	}
	if strings.TrimSpace(ready) != "ok" {
		return "", fmt.Errorf("apiserver readiness returned %q", strings.TrimSpace(ready))
	}

	nodesJSON, err := rehearseCommandFn(ctx, "kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("list Kubernetes nodes: %w", err)
	}
	if err := refuseExistingKubernetesNode(nodesJSON, spec.Node.Name); err != nil {
		return "", err
	}

	err = rehearseWithVMLifecycleFn(spec.Provider, func(lifecycle vmprov.VMLifecycle) error {
		summaries, err := lifecycle.VMSummaries()
		if err != nil {
			return err
		}
		return refuseExistingVM(summaries, spec)
	})
	if err != nil {
		return "", fmt.Errorf("hypervisor inventory: %w", err)
	}
	return "apiserver ready; test node name and VMID are unused", nil
}

func refuseExistingKubernetesNode(inventoryJSON, name string) error {
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(inventoryJSON), &nodes); err != nil {
		return fmt.Errorf("parse Kubernetes node inventory: %w", err)
	}
	for _, node := range nodes.Items {
		if node.Metadata.Name == name {
			return fmt.Errorf("kubernetes node %q already exists", name)
		}
	}
	return nil
}

func refuseExistingVM(summaries []vmprov.VMSummary, spec rehearseNodeSpec) error {
	for _, vm := range summaries {
		if vm.Name == spec.Node.Name {
			return fmt.Errorf("VM named %q already exists (ID %s)", vm.Name, vm.ID)
		}
		if vm.ID == strconv.Itoa(spec.VMID) {
			return fmt.Errorf("VMID %d already exists (VM %q)", spec.VMID, vm.Name)
		}
	}
	return nil
}

func (realRehearseOperations) CreateJoinMaterial(_ context.Context, spec rehearseNodeSpec) (*flatcarinternal.KubeadmResult, error) {
	return rehearseOrchestratorFn(spec).CreateJoinMaterial(spec.InitNode.IP, rehearseTokenTTL)
}

func (realRehearseOperations) Deploy(ctx context.Context, spec rehearseNodeSpec, opts rehearseNodeOptions, material flatcarinternal.KubeadmResult) error {
	return rehearseDeployNodeFn(ctx, cmdflatcar.RehearsalDeployOptions{
		Node: spec.Node, Provider: spec.Provider, ImagePath: opts.ImagePath, ImageVolume: opts.ImageVolume,
		Join: material, SSHUser: spec.SSHUser, Timeout: opts.Timeout,
	})
}

func (realRehearseOperations) WaitReady(ctx context.Context, spec rehearseNodeSpec, timeout time.Duration) (rehearseNodeReady, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastDetail string
	for {
		if err := waitCtx.Err(); err != nil {
			return rehearseNodeReady{}, fmt.Errorf("wait for node %s Ready: %w (last status: %s)", spec.Node.Name, err, lastDetail)
		}
		out, err := rehearseCommandFn(waitCtx, "kubectl", "get", "node", spec.Node.Name, "-o", "json")
		if err == nil {
			ready, detail, parseErr := parseRehearseNodeReady([]byte(out))
			if parseErr != nil {
				return rehearseNodeReady{}, parseErr
			}
			lastDetail = detail
			if ready.KubeletVersion != "" && ready.CNIReady {
				return ready, nil
			}
		} else {
			lastDetail = err.Error()
		}
		if err := rehearseSleepFn(waitCtx, rehearsePollInterval); err != nil {
			continue
		}
	}
}

func parseRehearseNodeReady(data []byte) (rehearseNodeReady, string, error) {
	var node struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			NodeInfo struct {
				KubeletVersion string `json:"kubeletVersion"`
			} `json:"nodeInfo"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &node); err != nil {
		return rehearseNodeReady{}, "", fmt.Errorf("parse Kubernetes node: %w", err)
	}
	ready := false
	networkUnavailable := false
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case "Ready":
			ready = condition.Status == "True"
		case "NetworkUnavailable":
			networkUnavailable = condition.Status == "True"
		}
	}
	result := rehearseNodeReady{KubeletVersion: node.Status.NodeInfo.KubeletVersion, CNIReady: ready && !networkUnavailable}
	return result, fmt.Sprintf("ready=%t networkUnavailable=%t kubelet=%s", ready, networkUnavailable, result.KubeletVersion), nil
}

func (realRehearseOperations) SmokeTest(ctx context.Context, spec rehearseNodeSpec, timeout time.Duration) (returnErr error) {
	name := rehearsePodName(spec.Node.Name)
	overrides, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"nodeSelector": map[string]string{"kubernetes.io/hostname": spec.Node.Name},
			"tolerations": []map[string]string{
				{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"},
				{"key": "node-role.kubernetes.io/master", "operator": "Exists", "effect": "NoSchedule"},
			},
		},
	})
	if err != nil {
		return err
	}
	if _, err := rehearseCommandFn(ctx, "kubectl", "run", name, "--namespace", "default", "--image", spec.CheckImage,
		"--restart=Never", "--labels", constants.RehearseNodeLabel+"=true", "--overrides", string(overrides),
		"--command", "--", "sh", "-c", "sleep 3600"); err != nil {
		return fmt.Errorf("create smoke pod: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		_, cleanupErr := rehearseCommandFn(cleanupCtx, "kubectl", "delete", "pod", name, "--namespace", "default", "--ignore-not-found=true", "--wait=true", "--timeout=2m")
		if cleanupErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("delete smoke pod: %w", cleanupErr))
		}
	}()
	if _, err := rehearseCommandFn(ctx, "kubectl", "wait", "--namespace", "default", "--for=condition=Ready", "pod/"+name, "--timeout="+timeout.String()); err != nil {
		return fmt.Errorf("wait for smoke pod: %w", err)
	}
	if _, err := rehearseCommandFn(ctx, "kubectl", "exec", "--namespace", "default", name, "--", "nslookup", "kubernetes.default"); err != nil {
		return fmt.Errorf("smoke pod DNS lookup: %w", err)
	}
	return nil
}

var invalidPodNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func rehearsePodName(node string) string {
	clean := strings.Trim(invalidPodNameChars.ReplaceAllString(strings.ToLower(node), "-"), "-")
	name := "homeops-rehearse-" + clean
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func (realRehearseOperations) DrainAndDeleteNode(ctx context.Context, spec rehearseNodeSpec, timeout time.Duration) error {
	name, err := rehearseCommandFn(ctx, "kubectl", "get", "node", spec.Node.Name, "-o", "name", "--ignore-not-found")
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}
	var cleanupErrors []error
	if _, err := rehearseCommandFn(ctx, "kubectl", "drain", spec.Node.Name, "--ignore-daemonsets", "--delete-emptydir-data", "--force", "--timeout="+timeout.String()); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("drain: %w", err))
	}
	if _, err := rehearseCommandFn(ctx, "kubectl", "delete", "node", spec.Node.Name, "--ignore-not-found=true"); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete node: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func (realRehearseOperations) DeleteVM(_ context.Context, spec rehearseNodeSpec) error {
	return rehearseWithVMLifecycleFn(spec.Provider, func(lifecycle vmprov.VMLifecycle) error {
		summaries, err := lifecycle.VMSummaries()
		if err != nil {
			return err
		}
		var found *vmprov.VMSummary
		for i := range summaries {
			if summaries[i].Name == spec.Node.Name {
				found = &summaries[i]
				break
			}
		}
		if found == nil {
			return nil
		}
		if found.ID != "" && found.ID != strconv.Itoa(spec.VMID) {
			return fmt.Errorf("refusing to delete VM %q: expected ID %d, found %s", found.Name, spec.VMID, found.ID)
		}
		var cleanupErrors []error
		if strings.EqualFold(found.Status, "running") || strings.EqualFold(found.Status, "poweredOn") {
			if err := lifecycle.StopVM(spec.Node.Name, true); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("power off: %w", err))
			}
		}
		if err := lifecycle.DeleteVM(spec.Node.Name); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete VM and disks: %w", err))
		}
		return errors.Join(cleanupErrors...)
	})
}

func (realRehearseOperations) InvalidateToken(_ context.Context, spec rehearseNodeSpec, token string) error {
	return rehearseOrchestratorFn(spec).DeleteBootstrapToken(spec.InitNode.IP, token)
}

func runRehearseCommand(ctx context.Context, name string, args ...string) (string, error) {
	result, err := common.RunCommand(ctx, common.CommandOptions{Name: name, Args: args, Timeout: rehearseDefaultTimeout})
	if err == nil {
		return result.Stdout, nil
	}
	detail := strings.TrimSpace(strings.Join(nonemptyStrings([]string{result.Stdout, result.Stderr}), "\n"))
	if detail == "" {
		return result.Stdout, err
	}
	return result.Stdout, fmt.Errorf("%w: %s", err, detail)
}
