package flatcar

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/ui"
)

// fcosOSStatusInspectCommand is the read-only probe run on each FCOS node. It
// mirrors osStatusInspectCommand but reads the rpm-ostree deployment list
// (`rpm-ostree status --json`, unprivileged over D-Bus) instead of
// update_engine, and adds the greenboot health-check result.
const fcosOSStatusInspectCommand = "printf '__HOMEOPS_OS_RELEASE__\\n'; cat /etc/os-release 2>/dev/null; " +
	"printf '__HOMEOPS_RPM_OSTREE__\\n'; " +
	"if command -v rpm-ostree >/dev/null 2>&1; then " +
	"rpmostree_output=$(rpm-ostree status --json 2>/dev/null); rpmostree_rc=$?; " +
	"printf '%s\\n' \"$rpmostree_output\"; " +
	"else rpmostree_rc=127; fi; " +
	"printf '__HOMEOPS_RPM_OSTREE_RC__=%s\\n' \"$rpmostree_rc\"; " +
	"if [ -e /run/reboot-required ]; then reboot_required=true; else reboot_required=false; fi; " +
	"printf '__HOMEOPS_REBOOT_REQUIRED__=%s\\n' \"$reboot_required\"; " +
	"printf '__HOMEOPS_GREENBOOT__='; systemctl is-active greenboot-healthcheck.service 2>/dev/null || true; " +
	"printf '__HOMEOPS_KERNEL__='; uname -r 2>/dev/null; " +
	"printf '__HOMEOPS_UP_SINCE__='; uptime -s 2>/dev/null || true; " +
	"printf '__HOMEOPS_UPTIME_SECONDS__='; awk '{print $1}' /proc/uptime 2>/dev/null || true; " +
	"printf '__HOMEOPS_END__\\n'"

// rpm-ostree deployment states reported in UPDATE STATUS.
const (
	fcosUpdateIdle        = "idle"
	fcosUpdateStaged      = "staged"
	fcosUpdatePending     = "pending"
	fcosUpdateUnavailable = "RPM_OSTREE_UNAVAILABLE"
)

// rpmOstreeStatus is the subset of `rpm-ostree status --json` os-status needs.
type rpmOstreeStatus struct {
	Deployments []rpmOstreeDeployment `json:"deployments"`
}

type rpmOstreeDeployment struct {
	Version           string   `json:"version"`
	Checksum          string   `json:"checksum"`
	Booted            bool     `json:"booted"`
	Staged            bool     `json:"staged"`
	Pinned            bool     `json:"pinned"`
	RequestedPackages []string `json:"requested-packages"`
}

type fcosOSNodeStatus struct {
	Node            string   `json:"node"`
	IP              string   `json:"ip,omitempty"`
	Version         string   `json:"fcos_version,omitempty"`
	UpdateStatus    string   `json:"update_status,omitempty"`
	NewVersion      string   `json:"new_version,omitempty"`
	RollbackVersion string   `json:"rollback_version,omitempty"`
	RebootNeeded    bool     `json:"reboot_needed"`
	LayeredPackages []string `json:"layered_packages,omitempty"`
	Greenboot       string   `json:"greenboot,omitempty"`
	Kernel          string   `json:"kernel,omitempty"`
	UpSince         string   `json:"up_since,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type fcosOSStatusReport struct {
	Nodes    []fcosOSNodeStatus `json:"nodes"`
	Warnings []string           `json:"warnings,omitempty"`
	Errors   []string           `json:"errors,omitempty"`
}

func newFCOSOSStatusCommand() *cobra.Command {
	var output string
	var nodeNames []string
	cmd := &cobra.Command{
		Use:          "os-status",
		Short:        "Show Fedora CoreOS rpm-ostree deployment status across nodes",
		SilenceUsage: true,
		Long: `SSH to every node configured os: fcos (or the --nodes given) and report the
booted rpm-ostree deployment, any staged/pending deployment (the next OS version
a reboot will activate), the rollback deployment, layered packages and the
greenboot health-check result. Read-only.`,
		Example: "  homeops-cli fcos os-status\n" +
			"  homeops-cli fcos os-status --output json\n" +
			"  homeops-cli fcos os-status --nodes k8s-test",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}
			report, err := buildFCOSOSStatus(cmd.Context(), nodeNames)
			rendered, renderErr := renderFCOSOSStatus(report, output)
			if renderErr != nil {
				return renderErr
			}
			if rendered != "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().StringSliceVar(&nodeNames, "nodes", nil, "nodes to query (default: every cluster node configured os: fcos)")
	return cmd
}

// fcosStatusNodes resolves the nodes to query: the explicit names (production
// or test node), else every production node configured os: fcos.
func fcosStatusNodes(names []string) ([]versionconfig.Node, error) {
	cfg := versionconfig.Get()
	if len(names) > 0 {
		nodes := make([]versionconfig.Node, 0, len(names))
		for _, name := range names {
			node, ok := cfg.ProvisioningNodeByName(name)
			if !ok {
				return nil, fmt.Errorf("unknown node %q (known: %s)", name, strings.Join(cfg.NodeNames(), ", "))
			}
			nodes = append(nodes, node)
		}
		return nodes, nil
	}
	var nodes []versionconfig.Node
	for _, node := range cfg.Cluster.Nodes {
		if cfg.OSForNode(node) == versionconfig.OSFCOS {
			nodes = append(nodes, node)
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no cluster nodes are configured os: fcos (set cluster.os or cluster.nodes[].os, or pass --nodes)")
	}
	return nodes, nil
}

func buildFCOSOSStatus(ctx context.Context, names []string) (fcosOSStatusReport, error) {
	nodes, err := fcosStatusNodes(names)
	if err != nil {
		return fcosOSStatusReport{}, err
	}
	sshUser := flatcarSSHUser()
	report := fcosOSStatusReport{Nodes: make([]fcosOSNodeStatus, 0, len(nodes))}
	for _, node := range nodes {
		raw, err := osStatusNodeCommandFn(ctx, node, sshUser, fcosOSStatusInspectCommand)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", node.Name, err))
			report.Nodes = append(report.Nodes, fcosOSNodeStatus{
				Node: node.Name, IP: node.IP, UpdateStatus: "SSH ERROR", Error: err.Error(),
			})
			continue
		}
		report.Nodes = append(report.Nodes, parseFCOSOSStatus(node, raw, osStatusNowFn()))
	}
	sort.Slice(report.Nodes, func(i, j int) bool { return report.Nodes[i].Node < report.Nodes[j].Node })
	report.Warnings = fcosOSStatusWarnings(report.Nodes)
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("os-status failed over SSH on %d node(s)", len(report.Errors))
	}
	return report, nil
}

func parseFCOSOSStatus(node versionconfig.Node, raw string, now time.Time) fcosOSNodeStatus {
	status := fcosOSNodeStatus{Node: node.Name, IP: node.IP}
	osRelease := parseKeyValueOutput(sectionBetween(raw, "__HOMEOPS_OS_RELEASE__\n", "__HOMEOPS_RPM_OSTREE__\n"))
	fields := parseMarkerValues(raw)

	deployments, err := parseRPMOstreeStatus(sectionBetween(raw, "__HOMEOPS_RPM_OSTREE__\n", "__HOMEOPS_RPM_OSTREE_RC__="))
	if err != nil || fields["RPM_OSTREE_RC"] != "0" {
		status.UpdateStatus = fcosUpdateUnavailable
		if err != nil {
			status.Error = err.Error()
		}
	} else {
		applyRPMOstreeDeployments(&status, deployments)
	}
	if status.Version == "" {
		// Fall back to os-release so the version column is still useful.
		status.Version = firstNonEmpty(osRelease["OSTREE_VERSION"], osRelease["VERSION_ID"])
	}

	status.RebootNeeded = status.RebootNeeded || strings.EqualFold(fields["REBOOT_REQUIRED"], "true")
	status.Greenboot = fields["GREENBOOT"]
	status.Kernel = fields["KERNEL"]
	status.UpSince = fields["UP_SINCE"]
	if status.UpSince == "" {
		uptimeFields := strings.Fields(fields["UPTIME_SECONDS"])
		if len(uptimeFields) > 0 {
			if seconds, err := strconv.ParseFloat(uptimeFields[0], 64); err == nil && seconds >= 0 {
				status.UpSince = now.Add(-time.Duration(seconds * float64(time.Second))).UTC().Format(time.RFC3339)
			}
		}
	}
	if status.Version == "" {
		status.Version = "unknown"
	}
	return status
}

// parseRPMOstreeStatus decodes `rpm-ostree status --json` output.
func parseRPMOstreeStatus(raw string) ([]rpmOstreeDeployment, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty rpm-ostree status output")
	}
	var status rpmOstreeStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return nil, fmt.Errorf("parse rpm-ostree status --json: %w", err)
	}
	if len(status.Deployments) == 0 {
		return nil, fmt.Errorf("rpm-ostree reports no deployments")
	}
	return status.Deployments, nil
}

// applyRPMOstreeDeployments derives the os-status fields from the deployment
// list. rpm-ostree orders it newest-first: a deployment ahead of the booted one
// is what the next boot activates — "staged" when it is still waiting for
// ostree-finalize-staged at shutdown, "pending" when already finalized. The
// deployment after the booted one is the rollback target.
func applyRPMOstreeDeployments(status *fcosOSNodeStatus, deployments []rpmOstreeDeployment) {
	bootedIndex := -1
	for i, deployment := range deployments {
		if deployment.Booted {
			bootedIndex = i
			break
		}
	}
	if bootedIndex < 0 {
		status.UpdateStatus = fcosUpdateUnavailable
		status.Error = "rpm-ostree reports no booted deployment"
		return
	}
	booted := deployments[bootedIndex]
	status.Version = booted.Version
	status.LayeredPackages = append([]string(nil), booted.RequestedPackages...)
	status.UpdateStatus = fcosUpdateIdle
	if bootedIndex > 0 {
		next := deployments[0]
		status.NewVersion = next.Version
		status.RebootNeeded = true
		status.UpdateStatus = fcosUpdatePending
		if next.Staged {
			status.UpdateStatus = fcosUpdateStaged
		}
	}
	if bootedIndex+1 < len(deployments) {
		status.RollbackVersion = deployments[bootedIndex+1].Version
	}
}

func fcosOSStatusWarnings(nodes []fcosOSNodeStatus) []string {
	// Version skew + reboot-needed reuse the Flatcar rules verbatim.
	shared := make([]flatcarOSNodeStatus, 0, len(nodes))
	var greenbootFailed, unavailable []string
	for _, node := range nodes {
		shared = append(shared, flatcarOSNodeStatus{
			Node: node.Node, Version: node.Version, RebootNeeded: node.RebootNeeded, Error: node.Error,
		})
		if node.Greenboot == "failed" {
			greenbootFailed = append(greenbootFailed, node.Node)
		}
		if node.UpdateStatus == fcosUpdateUnavailable {
			unavailable = append(unavailable, node.Node)
		}
	}
	warnings := flatcarOSStatusWarnings(shared)
	if len(greenbootFailed) > 0 {
		sort.Strings(greenbootFailed)
		warnings = append(warnings, "greenboot health check failed (boot is red; rollback may follow): "+strings.Join(greenbootFailed, ", "))
	}
	if len(unavailable) > 0 {
		sort.Strings(unavailable)
		warnings = append(warnings, "rpm-ostree status unavailable: "+strings.Join(unavailable, ", "))
	}
	return warnings
}

func renderFCOSOSStatus(report fcosOSStatusReport, output string) (string, error) {
	switch output {
	case "", "table":
		rows := make([][]string, 0, len(report.Nodes))
		for _, node := range report.Nodes {
			rows = append(rows, []string{
				node.Node,
				displayOSStatusValue(node.Version),
				displayOSStatusValue(node.UpdateStatus),
				displayOSStatusValue(node.NewVersion),
				strconv.FormatBool(node.RebootNeeded),
				displayOSStatusValue(node.Greenboot),
				displayOSStatusValue(node.Kernel),
				displayOSStatusValue(node.UpSince),
			})
		}
		var prefix strings.Builder
		if len(report.Warnings) == 0 {
			prefix.WriteString("Warnings: none\n")
		} else {
			prefix.WriteString("Warnings:\n")
			for _, warning := range report.Warnings {
				fmt.Fprintf(&prefix, "- WARN: %s\n", warning)
			}
		}
		for _, reportError := range report.Errors {
			fmt.Fprintf(&prefix, "- SSH error: %s\n", reportError)
		}
		return prefix.String() + ui.Table(
			[]string{"NODE", "FCOS VERSION", "UPDATE STATUS", "NEW VERSION", "REBOOT NEEDED", "GREENBOOT", "KERNEL", "UP SINCE"},
			rows,
		), nil
	case "json":
		return ui.RenderJSON(report)
	default:
		return "", ui.ValidateOutputFormat(output)
	}
}
