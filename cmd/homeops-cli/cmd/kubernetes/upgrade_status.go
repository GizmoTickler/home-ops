package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ui"
)

const systemUpgradePlanResource = "plans.upgrade.cattle.io"

type upgradeStatusSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type upgradeStatusPlan struct {
	Namespace   string   `json:"namespace"`
	Name        string   `json:"name"`
	Target      string   `json:"target,omitempty"`
	Channel     string   `json:"channel,omitempty"`
	Concurrency int64    `json:"concurrency"`
	LastApplied string   `json:"last_applied,omitempty"`
	Applying    []string `json:"applying,omitempty"`
}

type upgradeStatusNode struct {
	Name             string `json:"name"`
	KubeletVersion   string `json:"kubelet_version"`
	ContainerRuntime string `json:"container_runtime"`
	OS               string `json:"os"`
	Target           string `json:"target,omitempty"`
	Status           string `json:"status"`
}

type upgradeStatusJob struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Plan      string `json:"plan,omitempty"`
	Node      string `json:"node,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}

type upgradeStatusSkew struct {
	Node       string `json:"node"`
	APIServer  string `json:"apiserver_version"`
	Kubelet    string `json:"kubelet_version"`
	MinorDelta int    `json:"minor_delta"`
	Detail     string `json:"detail"`
}

type upgradeStatusReport struct {
	Summary          upgradeStatusSummary `json:"summary"`
	APIServerVersion string               `json:"apiserver_version"`
	Plans            []upgradeStatusPlan  `json:"plans"`
	Nodes            []upgradeStatusNode  `json:"nodes"`
	Jobs             []upgradeStatusJob   `json:"jobs"`
	Skew             []upgradeStatusSkew  `json:"skew"`
}

type systemUpgradePlanList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Version     string `json:"version"`
			Channel     string `json:"channel"`
			Concurrency int64  `json:"concurrency"`
		} `json:"spec"`
		Status struct {
			LatestVersion string   `json:"latestVersion"`
			LatestHash    string   `json:"latestHash"`
			Applying      []string `json:"applying"`
		} `json:"status"`
	} `json:"items"`
}

type upgradeStatusNodeList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Status   struct {
			NodeInfo struct {
				KubeletVersion          string `json:"kubeletVersion"`
				ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
				OSImage                 string `json:"osImage"`
			} `json:"nodeInfo"`
		} `json:"status"`
	} `json:"items"`
}

type upgradeJobJSON struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Active     int32           `json:"active"`
		Failed     int32           `json:"failed"`
		Conditions []conditionJSON `json:"conditions"`
	} `json:"status"`
}

type upgradeJobList struct {
	Items []upgradeJobJSON `json:"items"`
}

type upgradePodJSON struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase             string                  `json:"phase"`
		Reason            string                  `json:"reason"`
		Message           string                  `json:"message"`
		Conditions        []conditionJSON         `json:"conditions"`
		ContainerStatuses []upgradeContainerState `json:"containerStatuses"`
		InitStatuses      []upgradeContainerState `json:"initContainerStatuses"`
	} `json:"status"`
}

type upgradeContainerState struct {
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			Reason   string `json:"reason"`
			Message  string `json:"message"`
			ExitCode int32  `json:"exitCode"`
		} `json:"terminated"`
	} `json:"state"`
}

type upgradePodList struct {
	Items []upgradePodJSON `json:"items"`
}

type kubectlVersionJSON struct {
	ServerVersion struct {
		GitVersion string `json:"gitVersion"`
	} `json:"serverVersion"`
}

func newUpgradeStatusCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:          "upgrade-status",
		Short:        "Show System Upgrade Controller plans, node versions, jobs, and skew",
		SilenceUsage: true,
		Example: `  homeops-cli k8s upgrade-status
  homeops-cli k8s upgrade-status --output json
  watch homeops-cli k8s upgrade-status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			return runUpgradeStatus(ctx, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	return cmd
}

func runUpgradeStatus(ctx context.Context, output string, out io.Writer) error {
	report, err := buildUpgradeStatusReport(ctx)
	if err != nil {
		return err
	}
	rendered, err := renderUpgradeStatusReport(report, output)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, rendered)
	if report.Summary.Fail > 0 {
		return fmt.Errorf("upgrade-status found %d failed System Upgrade Controller job(s)", report.Summary.Fail)
	}
	return nil
}

func buildUpgradeStatusReport(ctx context.Context) (upgradeStatusReport, error) {
	plansRaw, err := kubectlOutputCtxFn(ctx, "get", systemUpgradePlanResource, "-A", "-o", "json")
	if err != nil {
		return upgradeStatusReport{}, fmt.Errorf("list System Upgrade Controller plans: %w", err)
	}
	plans, err := parseSystemUpgradePlans(plansRaw)
	if err != nil {
		return upgradeStatusReport{}, err
	}
	var nodeList upgradeStatusNodeList
	if err := kubeutil.GetClusterJSON(ctx, kubectlOutputCtxFn, "nodes", &nodeList); err != nil {
		return upgradeStatusReport{}, fmt.Errorf("list Kubernetes nodes: %w", err)
	}
	versionRaw, err := kubectlOutputCtxFn(ctx, "version", "-o", "json")
	if err != nil {
		return upgradeStatusReport{}, fmt.Errorf("read apiserver version: %w", err)
	}
	var version kubectlVersionJSON
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return upgradeStatusReport{}, fmt.Errorf("parse kubectl version JSON: %w", err)
	}
	if strings.TrimSpace(version.ServerVersion.GitVersion) == "" {
		return upgradeStatusReport{}, fmt.Errorf("kubectl version JSON has no serverVersion.gitVersion")
	}
	var jobs upgradeJobList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "jobs", &jobs); err != nil {
		return upgradeStatusReport{}, fmt.Errorf("list upgrade jobs: %w", err)
	}
	var pods upgradePodList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, "", "pods", &pods); err != nil {
		return upgradeStatusReport{}, fmt.Errorf("list pods for upgrade job reasons: %w", err)
	}

	report := upgradeStatusReport{Plans: plans, APIServerVersion: version.ServerVersion.GitVersion}
	target := selectKubernetesPlanTarget(plans)
	for _, item := range nodeList.Items {
		node := upgradeStatusNode{
			Name: item.Metadata.Name, KubeletVersion: item.Status.NodeInfo.KubeletVersion,
			ContainerRuntime: item.Status.NodeInfo.ContainerRuntimeVersion, OS: item.Status.NodeInfo.OSImage,
			Target: target,
		}
		node.Status = classifyNodeUpgradeStatus(node.KubeletVersion, target)
		report.Nodes = append(report.Nodes, node)
		switch node.Status {
		case "UpToDate":
			report.Summary.Pass++
		case "Pending", "Unknown":
			report.Summary.Warn++
		}
		if delta, skewed := kubernetesMinorSkew(version.ServerVersion.GitVersion, node.KubeletVersion); skewed {
			report.Skew = append(report.Skew, upgradeStatusSkew{
				Node: node.Name, APIServer: version.ServerVersion.GitVersion, Kubelet: node.KubeletVersion,
				MinorDelta: delta, Detail: fmt.Sprintf("apiserver/kubelet skew is %d minor versions (>1)", delta),
			})
			report.Summary.Warn++
		}
	}
	report.Jobs = collectUpgradeJobs(jobs.Items, pods.Items)
	for _, job := range report.Jobs {
		if job.Status == "Failed" {
			report.Summary.Fail++
		}
	}
	return report, nil
}

func parseSystemUpgradePlans(raw []byte) ([]upgradeStatusPlan, error) {
	var list systemUpgradePlanList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse System Upgrade Controller plans: %w", err)
	}
	plans := make([]upgradeStatusPlan, 0, len(list.Items))
	for _, item := range list.Items {
		target := strings.TrimSpace(item.Spec.Version)
		if target == "" {
			target = strings.TrimSpace(item.Status.LatestVersion)
		}
		plans = append(plans, upgradeStatusPlan{
			Namespace: item.Metadata.Namespace, Name: item.Metadata.Name, Target: target,
			Channel: item.Spec.Channel, Concurrency: item.Spec.Concurrency,
			LastApplied: item.Status.LatestHash, Applying: append([]string(nil), item.Status.Applying...),
		})
	}
	return plans, nil
}

func selectKubernetesPlanTarget(plans []upgradeStatusPlan) string {
	for _, plan := range plans {
		name := strings.ToLower(plan.Name)
		if strings.Contains(name, "kubeadm") || strings.Contains(name, "kubernetes") {
			if _, err := parseKubernetesVersion(plan.Target); err == nil {
				return plan.Target
			}
		}
	}
	for _, plan := range plans {
		version, err := parseKubernetesVersion(plan.Target)
		if err == nil && version.Major > 0 && version.Major < 100 {
			return plan.Target
		}
	}
	return ""
}

type kubernetesVersion struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
}

func parseKubernetesVersion(value string) (kubernetesVersion, error) {
	original := strings.TrimSpace(value)
	value = strings.TrimPrefix(original, "v")
	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		value = value[:plus]
	}
	prerelease := ""
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		prerelease = value[dash+1:]
		value = value[:dash]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return kubernetesVersion{}, fmt.Errorf("invalid Kubernetes version %q", original)
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return kubernetesVersion{}, fmt.Errorf("invalid Kubernetes version %q", original)
		}
		numbers[i] = n
	}
	if prerelease == "" && strings.HasSuffix(original, "-") {
		return kubernetesVersion{}, fmt.Errorf("invalid Kubernetes version %q", original)
	}
	return kubernetesVersion{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Prerelease: prerelease}, nil
}

func compareKubernetesVersions(left, right string) (int, error) {
	a, err := parseKubernetesVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseKubernetesVersion(right)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	return comparePrerelease(a.Prerelease, b.Prerelease), nil
}

func comparePrerelease(left, right string) int {
	if left == right {
		return 0
	}
	if left == "" {
		return 1
	}
	if right == "" {
		return -1
	}
	a, b := strings.Split(left, "."), strings.Split(right, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] == b[i] {
			continue
		}
		an, aErr := strconv.Atoi(a[i])
		bn, bErr := strconv.Atoi(b[i])
		switch {
		case aErr == nil && bErr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		case a[i] < b[i]:
			return -1
		default:
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}

func classifyNodeUpgradeStatus(current, target string) string {
	if strings.TrimSpace(target) == "" {
		return "Unknown"
	}
	comparison, err := compareKubernetesVersions(current, target)
	if err != nil {
		return "Unknown"
	}
	switch comparison {
	case 0:
		return "UpToDate"
	case -1:
		return "Pending"
	default:
		return "Unknown"
	}
}

func kubernetesMinorSkew(apiserver, kubelet string) (int, bool) {
	a, err := parseKubernetesVersion(apiserver)
	if err != nil {
		return 0, false
	}
	b, err := parseKubernetesVersion(kubelet)
	if err != nil {
		return 0, false
	}
	if a.Major != b.Major {
		return 1000 + absInt(a.Major-b.Major), true
	}
	delta := absInt(a.Minor - b.Minor)
	return delta, delta > 1
}

func collectUpgradeJobs(jobs []upgradeJobJSON, pods []upgradePodJSON) []upgradeStatusJob {
	byJob := make(map[string][]upgradePodJSON)
	for _, pod := range pods {
		jobName := pod.Metadata.Labels["job-name"]
		if jobName != "" {
			key := pod.Metadata.Namespace + "/" + jobName
			byJob[key] = append(byJob[key], pod)
		}
	}
	var result []upgradeStatusJob
	for _, job := range jobs {
		planName := job.Metadata.Labels["upgrade.cattle.io/plan"]
		if planName == "" {
			continue
		}
		failed := job.Status.Failed > 0 || conditionTrue(job.Status.Conditions, "Failed")
		active := job.Status.Active > 0
		if !failed && !active {
			continue
		}
		jobPods := byJob[job.Metadata.Namespace+"/"+job.Metadata.Name]
		status := "Active"
		if failed {
			status = "Failed"
		}
		node := job.Metadata.Labels["upgrade.cattle.io/node"]
		if node == "" && len(jobPods) > 0 {
			node = jobPods[0].Spec.NodeName
		}
		result = append(result, upgradeStatusJob{
			Namespace: job.Metadata.Namespace, Name: job.Metadata.Name, Plan: planName,
			Node: node, Status: status, Reason: classifyUpgradeJobFailure(job, jobPods),
		})
	}
	return result
}

func classifyUpgradeJobFailure(job upgradeJobJSON, pods []upgradePodJSON) string {
	var details []string
	for _, condition := range job.Status.Conditions {
		if condition.Status == "True" || condition.Type == "Failed" {
			details = appendDetail(details, condition.Reason, condition.Message)
		}
	}
	for _, pod := range pods {
		details = appendDetail(details, pod.Status.Reason, pod.Status.Message)
		for _, condition := range pod.Status.Conditions {
			if condition.Status != "True" {
				details = appendDetail(details, condition.Reason, condition.Message)
			}
		}
		for _, status := range append(pod.Status.InitStatuses, pod.Status.ContainerStatuses...) {
			if status.State.Waiting != nil {
				details = appendDetail(details, status.State.Waiting.Reason, status.State.Waiting.Message)
			}
			if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
				details = appendDetail(details, status.State.Terminated.Reason, status.State.Terminated.Message)
			}
		}
	}
	joined := strings.Join(details, "; ")
	lower := strings.ToLower(joined)
	switch {
	case strings.Contains(lower, "imagepull") || strings.Contains(lower, "pull image") || strings.Contains(lower, "pulling image"):
		return prefixUpgradeReason("image pull", joined)
	case strings.Contains(lower, "drain") || strings.Contains(lower, "evict") || strings.Contains(lower, "disruption budget"):
		return prefixUpgradeReason("drain stuck", joined)
	case joined != "":
		return joined
	case job.Status.Failed > 0:
		return "job failed without a reported reason"
	default:
		return "upgrade job is active"
	}
}

func appendDetail(details []string, reason, message string) []string {
	value := strings.Trim(strings.Join(nonEmptyKubernetesStrings(reason, message), ": "), " :")
	if value == "" {
		return details
	}
	return append(details, value)
}

func prefixUpgradeReason(category, detail string) string {
	if detail == "" {
		return category
	}
	return category + ": " + detail
}

func conditionTrue(conditions []conditionJSON, kind string) bool {
	for _, condition := range conditions {
		if condition.Type == kind && condition.Status == "True" {
			return true
		}
	}
	return false
}

func renderUpgradeStatusReport(report upgradeStatusReport, output string) (string, error) {
	switch output {
	case "", "table":
		planRows := make([][]string, 0, len(report.Plans))
		for _, plan := range report.Plans {
			planRows = append(planRows, []string{namespacedName(plan.Namespace, plan.Name), valueOrDash(plan.Target), valueOrDash(plan.Channel), strconv.FormatInt(plan.Concurrency, 10), valueOrDash(plan.LastApplied), strings.Join(plan.Applying, ",")})
		}
		nodeRows := make([][]string, 0, len(report.Nodes))
		for _, node := range report.Nodes {
			nodeRows = append(nodeRows, []string{node.Status, node.Name, node.KubeletVersion, node.Target, node.ContainerRuntime, node.OS})
		}
		jobRows := make([][]string, 0, len(report.Jobs))
		for _, job := range report.Jobs {
			jobRows = append(jobRows, []string{job.Status, namespacedName(job.Namespace, job.Name), job.Plan, job.Node, job.Reason})
		}
		if len(jobRows) == 0 {
			jobRows = append(jobRows, []string{"OK", "-", "-", "-", "no active or failed SUC jobs"})
		}
		skewRows := make([][]string, 0, len(report.Skew))
		for _, skew := range report.Skew {
			skewRows = append(skewRows, []string{"WARN", skew.Node, skew.APIServer, skew.Kubelet, strconv.Itoa(skew.MinorDelta), skew.Detail})
		}
		if len(skewRows) == 0 {
			skewRows = append(skewRows, []string{"OK", "-", report.APIServerVersion, "-", "0", "no apiserver/kubelet skew over one minor"})
		}
		return fmt.Sprintf("Summary: PASS=%d WARN=%d FAIL=%d\nApiserver: %s\n\nPlans\n%s\n\nNodes\n%s\n\nActive/failed SUC jobs\n%s\n\nVersion skew\n%s",
			report.Summary.Pass, report.Summary.Warn, report.Summary.Fail, report.APIServerVersion,
			ui.Table([]string{"PLAN", "TARGET", "CHANNEL", "CONCURRENCY", "LAST APPLIED", "APPLYING"}, planRows),
			ui.Table([]string{"STATUS", "NODE", "KUBELET", "TARGET", "CONTAINERD", "OS"}, nodeRows),
			ui.Table([]string{"STATUS", "JOB", "PLAN", "NODE", "REASON"}, jobRows),
			ui.Table([]string{"STATUS", "NODE", "APISERVER", "KUBELET", "MINOR DELTA", "DETAIL"}, skewRows)), nil
	case "json":
		return ui.RenderJSON(report)
	default:
		return "", ui.ValidateOutputFormat(output)
	}
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
