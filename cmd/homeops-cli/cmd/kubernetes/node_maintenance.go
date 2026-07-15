package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/cmdutil"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"
)

const (
	nodeMaintenanceDefaultDrainTimeout = 5 * time.Minute
	nodeMaintenanceDefaultTimeout      = 10 * time.Minute
	nodeMaintenancePollInterval        = 5 * time.Second
	nodeMaintenanceNooutOwned          = "owned"
	nodeMaintenanceNooutPreexisting    = "preexisting"
)

var (
	nodeMaintenanceNowFn    = time.Now
	nodeMaintenanceConfigFn = func() *config.Config {
		return config.Get()
	}
	nodeMaintenanceKubectlOutputFn = func(ctx context.Context, args ...string) ([]byte, error) {
		return kubectlOutputCtxFn(ctx, args...)
	}
	nodeMaintenanceKubectlRunFn = func(ctx context.Context, args ...string) error {
		return commandRunCtxFn(ctx, "kubectl", args...)
	}
	nodeMaintenanceConfirmFn = func(message string, defaultYes bool) (bool, error) {
		return confirmActionFn(message, defaultYes)
	}
	nodeMaintenanceSleepFn = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	nodeMaintenanceVMPowerFn = powerNodeVM
)

type nodeMaintenanceOptions struct {
	Action       string
	Node         string
	DrainTimeout time.Duration
	Timeout      time.Duration
	ShutdownVM   bool
	StartVM      bool
	Output       string
}

type nodeMaintenanceStep struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Duration string `json:"duration"`
	Detail   string `json:"detail,omitempty"`
}

type nodeMaintenanceFinal struct {
	NodeReady bool   `json:"node_ready"`
	Cordoned  bool   `json:"cordoned"`
	Ceph      string `json:"ceph"`
}

type nodeMaintenanceReport struct {
	Action string                `json:"action"`
	Node   string                `json:"node"`
	Steps  []nodeMaintenanceStep `json:"steps"`
	Final  nodeMaintenanceFinal  `json:"final"`
}

type nodeMaintenanceRuntime struct {
	configNode config.Node
	node       kubernetesNodeState
	ceph       bool
}

type kubernetesNodeState struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
	Status struct {
		Conditions []conditionJSON `json:"conditions"`
	} `json:"status"`
}

type cephOSDDump struct {
	Flags string `json:"flags"`
}

type cephStatusJSON struct {
	Health struct {
		Status string `json:"status"`
	} `json:"health"`
}

type nodeMaintenanceRollback struct {
	name string
	run  func() error
}

func newNodeMaintenanceCommand() *cobra.Command {
	nodeCmd := &cobra.Command{
		Use:   "node",
		Short: "Kubernetes node operations",
	}
	maintenanceCmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Safely enter or exit host maintenance",
		Long:  "Coordinate Kubernetes draining, Ceph noout, and optional hypervisor power operations for a configured cluster node.",
	}
	maintenanceCmd.AddCommand(newNodeMaintenanceEnterCommand(), newNodeMaintenanceExitCommand())
	nodeCmd.AddCommand(maintenanceCmd)
	return nodeCmd
}

func newNodeMaintenanceEnterCommand() *cobra.Command {
	opts := nodeMaintenanceOptions{Action: "enter"}
	cmd := &cobra.Command{
		Use:          "enter <node>",
		Short:        "Cordon and drain a node for host maintenance",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  homeops-cli k8s node maintenance enter k8s-0
  homeops-cli k8s node maintenance enter k8s-1 --shutdown-vm --drain-timeout 10m --yes
  homeops-cli k8s node maintenance enter k8s-2 --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Node = args[0]
			resolveNodeMaintenanceTimeouts(cmd, &opts)
			return executeNodeMaintenanceCommand(cmd, opts)
		},
	}
	cmd.Flags().DurationVar(&opts.DrainTimeout, "drain-timeout", 0, "maximum time allowed for kubectl drain (default: cluster.maintenance.drain_timeout)")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", 0, "maximum time allowed for VM and readiness waits (default: cluster.maintenance.timeout)")
	cmd.Flags().Lookup("drain-timeout").DefValue = ""
	cmd.Flags().Lookup("timeout").DefValue = ""
	cmd.Flags().BoolVar(&opts.ShutdownVM, "shutdown-vm", false, "power off the configured node VM after draining")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "table", "output format: table or json")
	return cmd
}

func newNodeMaintenanceExitCommand() *cobra.Command {
	opts := nodeMaintenanceOptions{Action: "exit"}
	cmd := &cobra.Command{
		Use:          "exit <node>",
		Short:        "Return a node to service after host maintenance",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  homeops-cli k8s node maintenance exit k8s-0
  homeops-cli k8s node maintenance exit k8s-1 --start-vm --timeout 15m
  homeops-cli k8s node maintenance exit k8s-2 --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Node = args[0]
			resolveNodeMaintenanceTimeouts(cmd, &opts)
			return executeNodeMaintenanceCommand(cmd, opts)
		},
	}
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", 0, "maximum time allowed for node and Ceph readiness waits (default: cluster.maintenance.timeout)")
	cmd.Flags().Lookup("timeout").DefValue = ""
	cmd.Flags().BoolVar(&opts.StartVM, "start-vm", false, "power on the configured node VM before uncordoning")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "table", "output format: table or json")
	return cmd
}

func resolveNodeMaintenanceTimeouts(cmd *cobra.Command, opts *nodeMaintenanceOptions) {
	cmdutil.ResolveDurationFlagDefault(cmd, "drain-timeout", &opts.DrainTimeout, func() time.Duration {
		return configuredMaintenanceDuration(nodeMaintenanceConfigFn().Cluster.Maintenance.DrainTimeout, nodeMaintenanceDefaultDrainTimeout)
	})
	cmdutil.ResolveDurationFlagDefault(cmd, "timeout", &opts.Timeout, func() time.Duration {
		return configuredMaintenanceDuration(nodeMaintenanceConfigFn().Cluster.Maintenance.Timeout, nodeMaintenanceDefaultTimeout)
	})
}

func configuredMaintenanceDuration(value string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func executeNodeMaintenanceCommand(cmd *cobra.Command, opts nodeMaintenanceOptions) error {
	if opts.Output != "table" && opts.Output != "json" {
		return fmt.Errorf("unsupported output format %q (table, json)", opts.Output)
	}
	if opts.Timeout <= 0 || (opts.Action == "enter" && opts.DrainTimeout <= 0) {
		return fmt.Errorf("maintenance timeouts must be greater than zero")
	}
	printNodeMaintenancePlan(cmd.ErrOrStderr(), opts)
	confirmed, err := nodeMaintenanceConfirmFn(fmt.Sprintf("Proceed with maintenance %s for %s?", opts.Action, opts.Node), false)
	if err != nil {
		return fmt.Errorf("confirm node maintenance: %w", err)
	}
	if !confirmed {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Maintenance cancelled.")
		return nil
	}
	report, runErr := runNodeMaintenance(cmd.Context(), opts)
	rendered, renderErr := renderNodeMaintenanceReport(report, opts.Output)
	if renderErr != nil {
		return renderErr
	}
	if rendered != "" {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
	}
	return runErr
}

func printNodeMaintenancePlan(out io.Writer, opts nodeMaintenanceOptions) {
	_, _ = fmt.Fprintf(out, "Maintenance %s plan for %s:\n", opts.Action, opts.Node)
	if opts.Action == "enter" {
		_, _ = fmt.Fprintf(out, "  1. Validate the configured node is Ready\n  2. Set Ceph noout when Rook is present\n  3. Cordon the node\n  4. Drain the node (timeout %s)\n", opts.DrainTimeout)
		if opts.ShutdownVM {
			_, _ = fmt.Fprintln(out, "  5. Power off the configured VM and wait for poweroff")
		} else {
			_, _ = fmt.Fprintln(out, "  5. Leave the VM powered on (--shutdown-vm not set)")
		}
		return
	}
	if opts.StartVM {
		_, _ = fmt.Fprintln(out, "  1. Power on the configured VM and wait for node Ready")
	} else {
		_, _ = fmt.Fprintln(out, "  1. Leave VM power unchanged (--start-vm not set)")
	}
	_, _ = fmt.Fprintf(out, "  2. Uncordon the node\n  3. Restore Ceph noout only when this workflow set it\n  4. Wait for HEALTH_OK or HEALTH_WARN (timeout %s)\n  5. Report final node and Ceph status\n", opts.Timeout)
}

func runNodeMaintenance(ctx context.Context, opts nodeMaintenanceOptions) (nodeMaintenanceReport, error) {
	report := nodeMaintenanceReport{Action: opts.Action, Node: opts.Node, Final: nodeMaintenanceFinal{Ceph: "not checked"}}
	if opts.Action == "enter" {
		return runNodeMaintenanceEnter(ctx, opts, report)
	}
	return runNodeMaintenanceExit(ctx, opts, report)
}

func runNodeMaintenanceEnter(ctx context.Context, opts nodeMaintenanceOptions, report nodeMaintenanceReport) (nodeMaintenanceReport, error) {
	runtimeState := nodeMaintenanceRuntime{}
	rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancelRollback()
	var rollbacks []nodeMaintenanceRollback
	if err := runNodeMaintenanceStep(&report, "validate-node", func() (string, error) {
		node, state, err := validateMaintenanceNode(ctx, opts.Node, true)
		runtimeState.configNode, runtimeState.node = node, state
		if err != nil {
			return "", err
		}
		return "configured node is Ready", nil
	}); err != nil {
		return finalizeNodeMaintenance(ctx, report, opts.Node, "not checked", err)
	}

	cephPresent, err := rookCephPresent(ctx)
	if err != nil {
		appendNodeMaintenanceFailure(&report, "ceph-noout", err)
		return rollbackNodeMaintenance(ctx, report, rollbacks, opts.Node, err)
	}
	runtimeState.ceph = cephPresent
	if !cephPresent {
		appendNodeMaintenanceSkip(&report, "ceph-noout", nodeMaintenanceRookConfig().Namespace+" namespace absent")
	} else if err := runNodeMaintenanceStep(&report, "ceph-noout", func() (string, error) {
		noout, getErr := cephNooutEnabled(ctx)
		if getErr != nil {
			return "", getErr
		}
		annotation := maintenanceNooutOwnership(runtimeState.node.Metadata.Annotations)
		if noout {
			ownership := nodeMaintenanceNooutPreexisting
			detail := "noout was already set; it will be preserved on exit"
			if annotation == nodeMaintenanceNooutOwned {
				return "noout is already owned by this maintenance workflow", nil
			}
			if annotation == nodeMaintenanceNooutPreexisting {
				return "noout was already set; its pre-existing ownership is already recorded", nil
			}
			if err := annotateMaintenanceNoout(ctx, opts.Node, ownership); err != nil {
				return "", err
			}
			rollbacks = append(rollbacks, nodeMaintenanceRollback{name: "remove-ceph-ownership-record", run: func() error {
				return removeMaintenanceNooutAnnotation(rollbackCtx, opts.Node)
			}})
			return detail, nil
		}
		if err := runCephCommand(ctx, "osd", "set", "noout"); err != nil {
			return "", err
		}
		if err := annotateMaintenanceNoout(ctx, opts.Node, nodeMaintenanceNooutOwned); err != nil {
			_ = runCephCommand(rollbackCtx, "osd", "unset", "noout")
			return "", err
		}
		rollbacks = append(rollbacks, nodeMaintenanceRollback{name: "unset-ceph-noout", run: func() error {
			unsetErr := runCephCommand(rollbackCtx, "osd", "unset", "noout")
			annotationErr := removeMaintenanceNooutAnnotation(rollbackCtx, opts.Node)
			if unsetErr != nil {
				return unsetErr
			}
			return annotationErr
		}})
		return "set noout and recorded ownership on the node", nil
	}); err != nil {
		return rollbackNodeMaintenance(ctx, report, rollbacks, opts.Node, err)
	}

	if runtimeState.node.Spec.Unschedulable {
		appendNodeMaintenanceSkip(&report, "cordon", "node is already cordoned")
	} else if err := runNodeMaintenanceStep(&report, "cordon", func() (string, error) {
		if err := nodeMaintenanceKubectlRunFn(ctx, "cordon", opts.Node); err != nil {
			return "", fmt.Errorf("cordon %s: %w", opts.Node, err)
		}
		rollbacks = append(rollbacks, nodeMaintenanceRollback{name: "uncordon", run: func() error {
			return nodeMaintenanceKubectlRunFn(rollbackCtx, "uncordon", opts.Node)
		}})
		return "node cordoned", nil
	}); err != nil {
		return rollbackNodeMaintenance(ctx, report, rollbacks, opts.Node, err)
	}

	if err := runNodeMaintenanceStep(&report, "drain", func() (string, error) {
		drainTimeout := "--timeout=" + opts.DrainTimeout.String()
		if err := nodeMaintenanceKubectlRunFn(ctx, "drain", opts.Node, "--ignore-daemonsets", "--delete-emptydir-data", drainTimeout); err != nil {
			return "", fmt.Errorf("drain %s: %w", opts.Node, err)
		}
		return "workloads evicted", nil
	}); err != nil {
		return rollbackNodeMaintenance(ctx, report, rollbacks, opts.Node, err)
	}

	if !opts.ShutdownVM {
		appendNodeMaintenanceSkip(&report, "shutdown-vm", "--shutdown-vm not set")
	} else if err := runNodeMaintenanceStep(&report, "shutdown-vm", func() (string, error) {
		if err := nodeMaintenanceVMPowerFn(ctx, nodeMaintenanceConfigFn(), runtimeState.configNode, false, opts.Timeout); err != nil {
			return "", err
		}
		return maintenanceVMDetail(runtimeState.configNode, false), nil
	}); err != nil {
		return rollbackNodeMaintenance(ctx, report, rollbacks, opts.Node, err)
	}

	cephFinal := "absent"
	if runtimeState.ceph {
		cephFinal = "noout"
	}
	return finalizeNodeMaintenance(ctx, report, opts.Node, cephFinal, nil)
}

func runNodeMaintenanceExit(ctx context.Context, opts nodeMaintenanceOptions, report nodeMaintenanceReport) (nodeMaintenanceReport, error) {
	var configNode config.Node
	var nodeState kubernetesNodeState
	if err := runNodeMaintenanceStep(&report, "validate-node", func() (string, error) {
		var err error
		configNode, nodeState, err = validateMaintenanceNode(ctx, opts.Node, false)
		if err != nil {
			return "", err
		}
		return "configured node found", nil
	}); err != nil {
		return finalizeNodeMaintenance(ctx, report, opts.Node, "not checked", err)
	}

	if !opts.StartVM {
		appendNodeMaintenanceSkip(&report, "start-vm", "--start-vm not set")
		appendNodeMaintenanceSkip(&report, "wait-node-ready", "VM start not requested")
	} else {
		if err := runNodeMaintenanceStep(&report, "start-vm", func() (string, error) {
			if err := nodeMaintenanceVMPowerFn(ctx, nodeMaintenanceConfigFn(), configNode, true, opts.Timeout); err != nil {
				return "", err
			}
			return maintenanceVMDetail(configNode, true), nil
		}); err != nil {
			return finalizeNodeMaintenance(ctx, report, opts.Node, "not checked", err)
		}
		if err := runNodeMaintenanceStep(&report, "wait-node-ready", func() (string, error) {
			if err := waitForMaintenanceNodeReady(ctx, opts.Node, opts.Timeout); err != nil {
				return "", err
			}
			return "kubelet reports Ready", nil
		}); err != nil {
			return finalizeNodeMaintenance(ctx, report, opts.Node, "not checked", err)
		}
		nodeState, _ = getMaintenanceNode(ctx, opts.Node)
	}

	if !nodeState.Spec.Unschedulable {
		appendNodeMaintenanceSkip(&report, "uncordon", "node is already schedulable")
	} else if err := runNodeMaintenanceStep(&report, "uncordon", func() (string, error) {
		if err := nodeMaintenanceKubectlRunFn(ctx, "uncordon", opts.Node); err != nil {
			return "", fmt.Errorf("uncordon %s: %w", opts.Node, err)
		}
		return "node uncordoned", nil
	}); err != nil {
		return finalizeNodeMaintenance(ctx, report, opts.Node, "not checked", err)
	}

	cephPresent, err := rookCephPresent(ctx)
	if err != nil {
		appendNodeMaintenanceFailure(&report, "ceph-noout", err)
		return finalizeNodeMaintenance(ctx, report, opts.Node, "unknown", err)
	}
	cephFinal := "absent"
	if !cephPresent {
		detail := nodeMaintenanceRookConfig().Namespace + " namespace absent"
		appendNodeMaintenanceSkip(&report, "ceph-noout", detail)
		appendNodeMaintenanceSkip(&report, "wait-ceph-health", detail)
	} else {
		ownership := maintenanceNooutOwnership(nodeState.Metadata.Annotations)
		if ownership != nodeMaintenanceNooutOwned {
			detail := "no ownership record; preserving noout"
			if ownership == nodeMaintenanceNooutPreexisting {
				detail = "noout predated maintenance; preserving it"
				if removeErr := removeMaintenanceNooutAnnotation(ctx, opts.Node); removeErr != nil {
					appendNodeMaintenanceFailure(&report, "ceph-noout", removeErr)
					return finalizeNodeMaintenance(ctx, report, opts.Node, "unknown", removeErr)
				}
			}
			appendNodeMaintenanceSkip(&report, "ceph-noout", detail)
		} else if err := runNodeMaintenanceStep(&report, "ceph-noout", func() (string, error) {
			noout, getErr := cephNooutEnabled(ctx)
			if getErr != nil {
				return "", getErr
			}
			if noout {
				if err := runCephCommand(ctx, "osd", "unset", "noout"); err != nil {
					return "", err
				}
			}
			if err := removeMaintenanceNooutAnnotation(ctx, opts.Node); err != nil {
				return "", err
			}
			return "removed maintenance-owned noout", nil
		}); err != nil {
			return finalizeNodeMaintenance(ctx, report, opts.Node, "unknown", err)
		}

		if err := runNodeMaintenanceStep(&report, "wait-ceph-health", func() (string, error) {
			health, waitErr := waitForCephHealth(ctx, opts.Timeout)
			cephFinal = health
			if waitErr != nil {
				return "", waitErr
			}
			return health, nil
		}); err != nil {
			return finalizeNodeMaintenance(ctx, report, opts.Node, cephFinal, err)
		}
	}
	return finalizeNodeMaintenance(ctx, report, opts.Node, cephFinal, nil)
}

func validateMaintenanceNode(ctx context.Context, name string, requireReady bool) (config.Node, kubernetesNodeState, error) {
	node, ok := nodeMaintenanceConfigFn().NodeByName(name)
	if !ok {
		return config.Node{}, kubernetesNodeState{}, fmt.Errorf("node %q is not present in cluster.nodes", name)
	}
	state, err := getMaintenanceNode(ctx, name)
	if err != nil {
		return config.Node{}, kubernetesNodeState{}, err
	}
	if requireReady && nodeConditionStatus(state.Status.Conditions, "Ready") != "True" {
		return config.Node{}, kubernetesNodeState{}, fmt.Errorf("node %s is not Ready", name)
	}
	return node, state, nil
}

func getMaintenanceNode(ctx context.Context, name string) (kubernetesNodeState, error) {
	var state kubernetesNodeState
	raw, err := nodeMaintenanceKubectlOutputFn(ctx, "get", "node", name, "-o", "json")
	if err != nil {
		return state, fmt.Errorf("get node %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("parse node %s: %w", name, err)
	}
	return state, nil
}

func rookCephPresent(ctx context.Context) (bool, error) {
	rook := nodeMaintenanceRookConfig()
	raw, err := nodeMaintenanceKubectlOutputFn(ctx, "get", "namespace", rook.Namespace, "--ignore-not-found", "-o", "name")
	if err != nil {
		return false, fmt.Errorf("detect %s namespace: %w", rook.Namespace, err)
	}
	return strings.TrimSpace(string(raw)) != "", nil
}

func runCephCommand(ctx context.Context, args ...string) error {
	rook := nodeMaintenanceRookConfig()
	kubectlArgs := []string{"-n", rook.Namespace, "exec", "deploy/" + rook.ToolboxDeployment, "--", "ceph"}
	kubectlArgs = append(kubectlArgs, args...)
	if err := nodeMaintenanceKubectlRunFn(ctx, kubectlArgs...); err != nil {
		return fmt.Errorf("ceph %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func cephCommandOutput(ctx context.Context, args ...string) ([]byte, error) {
	rook := nodeMaintenanceRookConfig()
	kubectlArgs := []string{"-n", rook.Namespace, "exec", "deploy/" + rook.ToolboxDeployment, "--", "ceph"}
	kubectlArgs = append(kubectlArgs, args...)
	raw, err := nodeMaintenanceKubectlOutputFn(ctx, kubectlArgs...)
	if err != nil {
		return nil, fmt.Errorf("ceph %s: %w", strings.Join(args, " "), err)
	}
	return raw, nil
}

func cephNooutEnabled(ctx context.Context) (bool, error) {
	raw, err := cephCommandOutput(ctx, "osd", "dump", "--format", "json")
	if err != nil {
		return false, err
	}
	var dump cephOSDDump
	if err := json.Unmarshal(raw, &dump); err != nil {
		return false, fmt.Errorf("parse ceph osd dump: %w", err)
	}
	for _, flag := range strings.FieldsFunc(dump.Flags, func(r rune) bool { return r == ',' || r == ' ' }) {
		if flag == "noout" {
			return true, nil
		}
	}
	return false, nil
}

func annotateMaintenanceNoout(ctx context.Context, node, ownership string) error {
	if err := nodeMaintenanceKubectlRunFn(ctx, "annotate", "node", node, constants.CephNooutAnnotation+"="+ownership, "--overwrite"); err != nil {
		return fmt.Errorf("record Ceph noout ownership on node %s: %w", node, err)
	}
	return nil
}

func removeMaintenanceNooutAnnotation(ctx context.Context, node string) error {
	if err := nodeMaintenanceKubectlRunFn(ctx, "annotate", "node", node, constants.CephNooutAnnotation+"-", constants.LegacyCephNooutAnnotation+"-"); err != nil {
		return fmt.Errorf("remove Ceph noout ownership from node %s: %w", node, err)
	}
	return nil
}

func maintenanceNooutOwnership(annotations map[string]string) string {
	if ownership := annotations[constants.CephNooutAnnotation]; ownership != "" {
		return ownership
	}
	return annotations[constants.LegacyCephNooutAnnotation]
}

func nodeMaintenanceRookConfig() config.RookConfig {
	rook := nodeMaintenanceConfigFn().Cluster.Rook
	if rook.Namespace == "" {
		rook.Namespace = constants.NSRookCeph
	}
	if rook.ToolboxDeployment == "" {
		rook.ToolboxDeployment = constants.DefaultRookToolboxDeployment
	}
	return rook
}

func waitForMaintenanceNodeReady(ctx context.Context, node string, timeout time.Duration) error {
	deadline := nodeMaintenanceNowFn().Add(timeout)
	for {
		state, err := getMaintenanceNode(ctx, node)
		if err == nil && nodeConditionStatus(state.Status.Conditions, "Ready") == "True" {
			return nil
		}
		if !nodeMaintenanceNowFn().Before(deadline) {
			return fmt.Errorf("node %s did not become Ready within %s", node, timeout)
		}
		if err := nodeMaintenanceSleepFn(ctx, nodeMaintenancePollInterval); err != nil {
			return err
		}
	}
}

func waitForCephHealth(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := nodeMaintenanceNowFn().Add(timeout)
	last := "unknown"
	for {
		raw, err := cephCommandOutput(ctx, "status", "--format", "json")
		if err == nil {
			var status cephStatusJSON
			if jsonErr := json.Unmarshal(raw, &status); jsonErr != nil {
				return last, fmt.Errorf("parse ceph status: %w", jsonErr)
			}
			last = status.Health.Status
			if last == "HEALTH_OK" || last == "HEALTH_WARN" {
				return last, nil
			}
		}
		if !nodeMaintenanceNowFn().Before(deadline) {
			return last, fmt.Errorf("ceph did not reach HEALTH_OK or HEALTH_WARN within %s (last: %s)", timeout, last)
		}
		if err := nodeMaintenanceSleepFn(ctx, nodeMaintenancePollInterval); err != nil {
			return last, err
		}
	}
}

func powerNodeVM(ctx context.Context, cfg *config.Config, node config.Node, start bool, timeout time.Duration) error {
	providerName, err := vmlifecycle.NormalizeVMProvider(cfg.Hypervisors.Default)
	if err != nil {
		return err
	}
	profileProvider := "flatcar"
	if providerName == "vsphere" {
		profileProvider = "vsphere"
	}
	profile := node.VM.ForProvider(profileProvider)
	return vmlifecycle.WithVMLifecycle(providerName, func(lifecycle vmprov.VMLifecycle) error {
		if providerName == "proxmox" {
			if profile.VMID <= 0 {
				return fmt.Errorf("cluster node %s has no configured Proxmox VMID", node.Name)
			}
			vms, listErr := lifecycle.VMSummaries()
			if listErr != nil {
				return fmt.Errorf("list Proxmox VMs: %w", listErr)
			}
			expectedID := strconv.Itoa(profile.VMID)
			matched := false
			for _, vm := range vms {
				if vm.ID == expectedID && vm.Name == node.Name {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("configured VM %s (VMID %d) was not found on Proxmox", node.Name, profile.VMID)
			}
		}
		if start {
			if err := lifecycle.StartVM(node.Name); err != nil {
				return fmt.Errorf("start VM %s: %w", node.Name, err)
			}
		} else if err := lifecycle.StopVM(node.Name, false); err != nil {
			return fmt.Errorf("stop VM %s: %w", node.Name, err)
		}
		return waitForVMPowerState(ctx, lifecycle, node.Name, start, timeout)
	})
}

func waitForVMPowerState(ctx context.Context, lifecycle vmprov.VMLifecycle, name string, poweredOn bool, timeout time.Duration) error {
	deadline := nodeMaintenanceNowFn().Add(timeout)
	wanted := "stopped"
	if poweredOn {
		wanted = "running"
	}
	for {
		vms, err := lifecycle.VMSummaries()
		if err == nil {
			for _, vm := range vms {
				status := strings.ToLower(vm.Status)
				if vm.Name == name && ((poweredOn && (status == "running" || status == "poweredon" || status == "powered on")) || (!poweredOn && (status == "stopped" || status == "poweredoff" || status == "powered off"))) {
					return nil
				}
			}
		}
		if !nodeMaintenanceNowFn().Before(deadline) {
			return fmt.Errorf("VM %s did not reach %s within %s", name, wanted, timeout)
		}
		if err := nodeMaintenanceSleepFn(ctx, nodeMaintenancePollInterval); err != nil {
			return err
		}
	}
}

func maintenanceVMDetail(node config.Node, started bool) string {
	action := "powered off"
	if started {
		action = "powered on"
	}
	providerName := nodeMaintenanceConfigFn().Hypervisors.Default
	profileProvider := "flatcar"
	if providerName == "vsphere" || providerName == "esxi" {
		profileProvider = "vsphere"
	}
	vmid := node.VM.ForProvider(profileProvider).VMID
	if vmid > 0 {
		return fmt.Sprintf("VM %s (VMID %d) %s", node.Name, vmid, action)
	}
	return fmt.Sprintf("VM %s %s", node.Name, action)
}

func runNodeMaintenanceStep(report *nodeMaintenanceReport, name string, run func() (string, error)) error {
	started := nodeMaintenanceNowFn()
	detail, err := run()
	status := "DONE"
	if err != nil {
		status = "FAILED"
		detail = err.Error()
	}
	report.Steps = append(report.Steps, nodeMaintenanceStep{Name: name, Status: status, Duration: nodeMaintenanceNowFn().Sub(started).String(), Detail: detail})
	return err
}

func appendNodeMaintenanceSkip(report *nodeMaintenanceReport, name, detail string) {
	report.Steps = append(report.Steps, nodeMaintenanceStep{Name: name, Status: "SKIPPED", Duration: "0s", Detail: detail})
}

func appendNodeMaintenanceFailure(report *nodeMaintenanceReport, name string, err error) {
	report.Steps = append(report.Steps, nodeMaintenanceStep{Name: name, Status: "FAILED", Duration: "0s", Detail: err.Error()})
}

func rollbackNodeMaintenance(ctx context.Context, report nodeMaintenanceReport, rollbacks []nodeMaintenanceRollback, node string, cause error) (nodeMaintenanceReport, error) {
	for i := len(rollbacks) - 1; i >= 0; i-- {
		name := "rollback-" + rollbacks[i].name
		_ = runNodeMaintenanceStep(&report, name, func() (string, error) {
			if err := rollbacks[i].run(); err != nil {
				return "", err
			}
			return "rollback completed", nil
		})
	}
	return finalizeNodeMaintenance(ctx, report, node, "unknown", fmt.Errorf("maintenance enter failed: %w", cause))
}

func finalizeNodeMaintenance(ctx context.Context, report nodeMaintenanceReport, node, ceph string, runErr error) (nodeMaintenanceReport, error) {
	report.Final.Ceph = ceph
	state, err := getMaintenanceNode(ctx, node)
	if err == nil {
		report.Final.NodeReady = nodeConditionStatus(state.Status.Conditions, "Ready") == "True"
		report.Final.Cordoned = state.Spec.Unschedulable
	} else if runErr == nil {
		runErr = fmt.Errorf("read final node status: %w", err)
	}
	return report, runErr
}

func renderNodeMaintenanceReport(report nodeMaintenanceReport, output string) (string, error) {
	if output == "json" {
		raw, err := json.MarshalIndent(report, "", "  ")
		return string(raw), err
	}
	if output != "" && output != "table" {
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
	rows := make([][]string, 0, len(report.Steps))
	for _, step := range report.Steps {
		rows = append(rows, []string{step.Name, step.Status, step.Duration, step.Detail})
	}
	final := fmt.Sprintf("Final: node=%s ready=%t cordoned=%t ceph=%s", report.Node, report.Final.NodeReady, report.Final.Cordoned, report.Final.Ceph)
	return ui.Table([]string{"STEP", "STATUS", "DURATION", "DETAIL"}, rows) + "\n" + final, nil
}
