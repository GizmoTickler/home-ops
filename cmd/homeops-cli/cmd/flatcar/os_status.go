package flatcar

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/ui"
)

const osStatusInspectCommand = "printf '__HOMEOPS_OS_RELEASE__\\n'; cat /etc/os-release 2>/dev/null; " +
	"printf '__HOMEOPS_UPDATE_ENGINE__\\n'; " +
	"if command -v update_engine_client >/dev/null 2>&1; then " +
	"update_output=$(update_engine_client -status 2>&1); update_rc=$?; " +
	"if [ \"$update_rc\" -ne 0 ] && command -v sudo >/dev/null 2>&1; then " +
	"update_output=$(sudo -n update_engine_client -status 2>&1); update_rc=$?; fi; " +
	"printf '%s\\n' \"$update_output\"; " +
	"else update_rc=127; printf 'CURRENT_OP=UPDATE_STATUS_UNAVAILABLE\\n'; fi; " +
	"printf '__HOMEOPS_UPDATE_RC__=%s\\n' \"$update_rc\"; " +
	"if [ -e /run/reboot-required ]; then reboot_required=true; else reboot_required=false; fi; " +
	"printf '__HOMEOPS_REBOOT_REQUIRED__=%s\\n' \"$reboot_required\"; " +
	"printf '__HOMEOPS_KERNEL__='; uname -r 2>/dev/null; " +
	"printf '__HOMEOPS_UP_SINCE__='; uptime -s 2>/dev/null || true; " +
	"printf '__HOMEOPS_UPTIME_SECONDS__='; awk '{print $1}' /proc/uptime 2>/dev/null || true; " +
	"printf '__HOMEOPS_END__\\n'"

var (
	osStatusNowFn         = time.Now
	osStatusNodeCommandFn = func(ctx context.Context, node versionconfig.Node, sshUser, command string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		client := ssh.NewSSHClient(ssh.SSHConfig{Host: node.IP, Username: sshUser, Port: strconv.Itoa(versionconfig.Get().Cluster.NodeSSHPort)})
		if err := client.Connect(); err != nil {
			return "", err
		}
		defer func() { _ = client.Close() }()
		return client.ExecuteCommand(command)
	}
)

type updateEngineStatus struct {
	CurrentOp  string  `json:"current_op"`
	NewVersion string  `json:"new_version,omitempty"`
	Progress   float64 `json:"progress,omitempty"`
}

type flatcarOSNodeStatus struct {
	Node         string `json:"node"`
	IP           string `json:"ip,omitempty"`
	Version      string `json:"flatcar_version,omitempty"`
	UpdateStatus string `json:"update_status,omitempty"`
	NewVersion   string `json:"new_version,omitempty"`
	RebootNeeded bool   `json:"reboot_needed"`
	Kernel       string `json:"kernel,omitempty"`
	UpSince      string `json:"up_since,omitempty"`
	Error        string `json:"error,omitempty"`
}

type flatcarOSStatusReport struct {
	Nodes    []flatcarOSNodeStatus `json:"nodes"`
	Warnings []string              `json:"warnings,omitempty"`
	Errors   []string              `json:"errors,omitempty"`
}

func newOSStatusCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:          "os-status",
		Short:        "Show Flatcar OS and update-engine status across nodes",
		SilenceUsage: true,
		Example: "  homeops-cli flatcar os-status\n" +
			"  homeops-cli flatcar os-status --output json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}
			report, err := buildFlatcarOSStatus(cmd.Context())
			rendered, renderErr := renderFlatcarOSStatus(report, output)
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
	return cmd
}

func buildFlatcarOSStatus(ctx context.Context) (flatcarOSStatusReport, error) {
	cfg := versionconfig.Get()
	nodes := cfg.Cluster.Nodes
	if len(nodes) == 0 {
		return flatcarOSStatusReport{}, fmt.Errorf("cluster.nodes has no Flatcar nodes")
	}
	sshUser := flatcarSSHUser()
	report := flatcarOSStatusReport{Nodes: make([]flatcarOSNodeStatus, 0, len(nodes))}
	for _, node := range nodes {
		raw, err := osStatusNodeCommandFn(ctx, node, sshUser, osStatusInspectCommand)
		if err != nil {
			detail := fmt.Sprintf("%s: %v", node.Name, err)
			report.Errors = append(report.Errors, detail)
			report.Nodes = append(report.Nodes, flatcarOSNodeStatus{
				Node: node.Name, IP: node.IP, UpdateStatus: "SSH ERROR", Error: err.Error(),
			})
			continue
		}
		status := parseFlatcarOSStatus(node, raw, osStatusNowFn())
		report.Nodes = append(report.Nodes, status)
	}
	sort.Slice(report.Nodes, func(i, j int) bool { return report.Nodes[i].Node < report.Nodes[j].Node })
	report.Warnings = flatcarOSStatusWarnings(report.Nodes)
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("os-status failed over SSH on %d node(s)", len(report.Errors))
	}
	return report, nil
}

// CollectOSStatusJSON returns the read-only OS status report as JSON for
// in-process diagnostic collectors such as the Kubernetes support bundle.
func CollectOSStatusJSON(ctx context.Context) ([]byte, error) {
	report, err := buildFlatcarOSStatus(ctx)
	rendered, marshalErr := ui.RenderJSON(report)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return []byte(rendered), err
}

func parseFlatcarOSStatus(node versionconfig.Node, raw string, now time.Time) flatcarOSNodeStatus {
	status := flatcarOSNodeStatus{Node: node.Name, IP: node.IP}
	osRelease := parseKeyValueOutput(sectionBetween(raw, "__HOMEOPS_OS_RELEASE__\n", "__HOMEOPS_UPDATE_ENGINE__\n"))
	status.Version = firstNonEmpty(osRelease["VERSION"], osRelease["VERSION_ID"])

	updateRaw := sectionBetween(raw, "__HOMEOPS_UPDATE_ENGINE__\n", "__HOMEOPS_UPDATE_RC__=")
	update := parseUpdateEngineStatus(updateRaw)
	status.UpdateStatus = update.CurrentOp
	status.NewVersion = update.NewVersion

	fields := parseMarkerValues(raw)
	status.RebootNeeded = strings.EqualFold(fields["REBOOT_REQUIRED"], "true") || updateNeedsReboot(update.CurrentOp)
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
	if status.UpdateStatus == "" {
		status.UpdateStatus = "UPDATE_STATUS_UNKNOWN"
	}
	return status
}

func sectionBetween(raw, start, end string) string {
	startIndex := strings.Index(raw, start)
	if startIndex < 0 {
		return ""
	}
	startIndex += len(start)
	endIndex := strings.Index(raw[startIndex:], end)
	if endIndex < 0 {
		return raw[startIndex:]
	}
	return raw[startIndex : startIndex+endIndex]
}

func parseKeyValueOutput(raw string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		index := strings.IndexByte(line, '=')
		if index < 0 {
			index = strings.IndexByte(line, ':')
		}
		if index <= 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(line[:index]))
		value := strings.TrimSpace(line[index+1:])
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		} else {
			value = strings.Trim(value, "'\"")
		}
		values[key] = value
	}
	return values
}

func parseUpdateEngineStatus(raw string) updateEngineStatus {
	values := parseKeyValueOutput(raw)
	status := updateEngineStatus{
		CurrentOp: firstNonEmpty(values["CURRENT_OP"], values["STATUS"], values["CURRENT_OPERATION"]),
		NewVersion: firstNonEmpty(
			values["NEW_VERSION"],
			values["TARGET_VERSION"],
			values["VERSION"],
		),
	}
	if status.NewVersion == "0.0.0" || status.NewVersion == "0.0.0.0" {
		status.NewVersion = ""
	}
	if progress, err := strconv.ParseFloat(strings.TrimSuffix(values["PROGRESS"], "%"), 64); err == nil {
		if strings.HasSuffix(values["PROGRESS"], "%") {
			progress /= 100
		}
		status.Progress = progress
	}
	return status
}

func parseMarkerValues(raw string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "__HOMEOPS_") {
			continue
		}
		line = strings.TrimPrefix(line, "__HOMEOPS_")
		index := strings.Index(line, "__=")
		if index <= 0 {
			continue
		}
		values[line[:index]] = strings.TrimSpace(line[index+3:])
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func updateNeedsReboot(currentOp string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(currentOp))
	return normalized == "UPDATE_STATUS_UPDATED_NEED_REBOOT" ||
		normalized == "UPDATED_NEED_REBOOT"
}

func flatcarOSStatusWarnings(nodes []flatcarOSNodeStatus) []string {
	versionNodes := map[string][]string{}
	var rebootNodes []string
	for _, node := range nodes {
		if node.Error == "" && node.Version != "" && node.Version != "unknown" {
			versionNodes[node.Version] = append(versionNodes[node.Version], node.Node)
		}
		if node.RebootNeeded {
			rebootNodes = append(rebootNodes, node.Node)
		}
	}
	var warnings []string
	if len(versionNodes) > 1 {
		parts := make([]string, 0, len(versionNodes))
		for version, names := range versionNodes {
			sort.Strings(names)
			parts = append(parts, fmt.Sprintf("%s (%s)", version, strings.Join(names, ", ")))
		}
		sort.Strings(parts)
		warnings = append(warnings, "version skew: "+strings.Join(parts, "; "))
	}
	if len(rebootNodes) > 0 {
		sort.Strings(rebootNodes)
		warnings = append(warnings, "reboot needed: "+strings.Join(rebootNodes, ", "))
	}
	return warnings
}

func renderFlatcarOSStatus(report flatcarOSStatusReport, output string) (string, error) {
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
			[]string{"NODE", "FLATCAR VERSION", "UPDATE STATUS", "NEW VERSION", "REBOOT NEEDED", "KERNEL", "UP SINCE"},
			rows,
		), nil
	case "json":
		return ui.RenderJSON(report)
	default:
		return "", ui.ValidateOutputFormat(output)
	}
}

func displayOSStatusValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
