package kubernetes

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ui"
)

const (
	fluxKustomizationResource = "kustomizations.kustomize.toolkit.fluxcd.io"
	fluxHelmReleaseResource   = "helmreleases.helm.toolkit.fluxcd.io"
	cephClusterResource       = "cephclusters.ceph.rook.io"
	certificateResource       = "certificates.cert-manager.io"

	doctorGroupFlux  = "flux"
	doctorGroupPods  = "pods"
	doctorGroupCeph  = "ceph"
	doctorGroupCerts = "certificates"
	doctorGroupNodes = "nodes"

	doctorRecentOOMWindow = 24 * time.Hour
	doctorCertExpiryWarn  = 14 * 24 * time.Hour

	// doctorDefaultPendingGrace is how long a pod may sit in Pending before
	// doctor treats it as a failure. Short-lived Pending pods (e.g. VolSync
	// mover pods waiting for their snapshot clones to schedule) are ignored.
	doctorDefaultPendingGrace = 10 * time.Minute
)

type doctorStatus string

const (
	statusPass doctorStatus = "PASS"
	statusWarn doctorStatus = "WARN"
	statusFail doctorStatus = "FAIL"
)

type doctorSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type doctorCheck struct {
	Group     string       `json:"group"`
	Name      string       `json:"name"`
	Status    doctorStatus `json:"status"`
	Detail    string       `json:"detail"`
	Namespace string       `json:"namespace,omitempty"`
	Kind      string       `json:"kind,omitempty"`
}

type doctorReport struct {
	Summary doctorSummary `json:"summary"`
	Checks  []doctorCheck `json:"checks"`
}

func (r *doctorReport) add(group, kind, namespace, name string, status doctorStatus, detail string) {
	r.Checks = append(r.Checks, doctorCheck{
		Group:     group,
		Kind:      kind,
		Namespace: namespace,
		Name:      namespacedName(namespace, name),
		Status:    status,
		Detail:    detail,
	})
}

func (r *doctorReport) finalize() {
	r.Summary = doctorSummary{}
	for _, c := range r.Checks {
		switch c.Status {
		case statusFail:
			r.Summary.Fail++
		case statusWarn:
			r.Summary.Warn++
		default:
			r.Summary.Pass++
		}
	}
}

func (r doctorReport) hasFail() bool {
	return r.Summary.Fail > 0
}

func namespacedName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

func newDoctorCommand() *cobra.Command {
	var namespace, output string
	pendingGrace := doctorDefaultPendingGrace
	cmd := &cobra.Command{
		Use:          "doctor",
		Short:        "Run read-only Kubernetes cluster triage",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			return runDoctorContext(ctx, namespace, output, pendingGrace, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to inspect (default: all namespaces)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().DurationVar(&pendingGrace, "pending-grace", doctorDefaultPendingGrace, "ignore Pending pods younger than this duration")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func runDoctor(namespace, output string, pendingGrace time.Duration, out io.Writer) error {
	return runDoctorContext(context.Background(), namespace, output, pendingGrace, out)
}

func runDoctorContext(ctx context.Context, namespace, output string, pendingGrace time.Duration, out io.Writer) error {
	report := buildDoctorReportContext(ctx, namespace, pendingGrace)
	rendered, err := renderDoctorReport(report, output)
	if err != nil {
		return err
	}
	if rendered != "" {
		_, _ = fmt.Fprintln(out, rendered)
	}
	if report.hasFail() {
		return fmt.Errorf("doctor found %d failing check(s)", report.Summary.Fail)
	}
	return nil
}

func buildDoctorReport(namespace string, pendingGrace time.Duration) doctorReport {
	return buildDoctorReportContext(context.Background(), namespace, pendingGrace)
}

func buildDoctorReportContext(ctx context.Context, namespace string, pendingGrace time.Duration) doctorReport {
	var report doctorReport
	report.addFlux(ctx, namespace)
	report.addNodes(ctx)
	report.addPods(ctx, namespace, pendingGrace)
	report.addCeph(ctx, namespace)
	report.addCertificates(ctx, namespace)
	report.finalize()
	return report
}

func renderDoctorReport(report doctorReport, output string) (string, error) {
	switch output {
	case "", "table":
		rows := make([][]string, 0, len(report.Checks))
		for _, c := range report.Checks {
			rows = append(rows, []string{string(c.Status), c.Group, c.Kind, c.Name, c.Detail})
		}
		summary := fmt.Sprintf("Summary: PASS=%d WARN=%d FAIL=%d", report.Summary.Pass, report.Summary.Warn, report.Summary.Fail)
		return summary + "\n" + ui.Table([]string{"STATUS", "GROUP", "KIND", "NAME", "DETAIL"}, rows), nil
	case "json":
		return ui.RenderJSON(report)
	default:
		return "", ui.ValidateOutputFormat(output)
	}
}

type metadataJSON struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp string            `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels"`
}

type conditionJSON struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func readyCondition(conditions []conditionJSON) (conditionJSON, bool) {
	for _, c := range conditions {
		if c.Type == "Ready" {
			return c, true
		}
	}
	return conditionJSON{}, false
}

func conditionDetail(c conditionJSON) string {
	parts := []string{}
	if c.Reason != "" {
		parts = append(parts, c.Reason)
	}
	if c.Message != "" {
		parts = append(parts, c.Message)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Ready=%s", c.Status)
	}
	return strings.Join(parts, ": ")
}

type fluxObjectList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Suspend bool `json:"suspend"`
		} `json:"spec"`
		Status struct {
			Conditions []conditionJSON `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func (r *doctorReport) addFlux(ctx context.Context, namespace string) {
	totalProblems := len(r.Checks)
	r.addFluxResource(ctx, namespace, "Kustomization", fluxKustomizationResource)
	r.addFluxResource(ctx, namespace, "HelmRelease", fluxHelmReleaseResource)
	if len(r.Checks) == totalProblems {
		r.add(doctorGroupFlux, "Flux", "", "all", statusPass, "all Kustomizations and HelmReleases are ready and not suspended")
	}
}

func (r *doctorReport) addFluxResource(ctx context.Context, namespace, kind, resource string) {
	var list fluxObjectList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, resource, &list); err != nil {
		r.add(doctorGroupFlux, kind, "", resource, statusFail, err.Error())
		return
	}
	for _, item := range list.Items {
		if item.Spec.Suspend {
			r.add(doctorGroupFlux, kind, item.Metadata.Namespace, item.Metadata.Name, statusWarn, "suspended")
			continue
		}
		ready, ok := readyCondition(item.Status.Conditions)
		if !ok {
			r.add(doctorGroupFlux, kind, item.Metadata.Namespace, item.Metadata.Name, statusWarn, "Ready condition missing")
			continue
		}
		if ready.Status != "True" {
			r.add(doctorGroupFlux, kind, item.Metadata.Namespace, item.Metadata.Name, statusFail, conditionDetail(ready))
		}
	}
}

type nodeList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
		Status struct {
			Conditions []conditionJSON `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func (r *doctorReport) addNodes(ctx context.Context) {
	before := len(r.Checks)
	var list nodeList
	if err := kubeutil.GetClusterJSON(ctx, kubectlOutputCtxFn, "nodes", &list); err != nil {
		r.add(doctorGroupNodes, "Node", "", "nodes", statusFail, err.Error())
		return
	}
	for _, node := range list.Items {
		ready, ok := readyCondition(node.Status.Conditions)
		if !ok {
			r.add(doctorGroupNodes, "Node", "", node.Metadata.Name, statusFail, "Ready condition missing")
		} else if ready.Status != "True" {
			r.add(doctorGroupNodes, "Node", "", node.Metadata.Name, statusFail, conditionDetail(ready))
		}
		for _, pressureType := range []string{"MemoryPressure", "DiskPressure", "PIDPressure"} {
			if nodeConditionStatus(node.Status.Conditions, pressureType) == "True" {
				r.add(doctorGroupNodes, "Node", "", node.Metadata.Name, statusWarn, pressureType+"=True")
			}
		}
		if node.Spec.Unschedulable {
			r.add(doctorGroupNodes, "Node", "", node.Metadata.Name, statusWarn, "unschedulable")
		}
	}
	if len(r.Checks) == before {
		r.add(doctorGroupNodes, "Node", "", "all", statusPass, "all nodes are Ready, schedulable, and pressure-free")
	}
}

func nodeConditionStatus(conditions []conditionJSON, conditionType string) string {
	for _, c := range conditions {
		if c.Type == conditionType {
			return c.Status
		}
	}
	return ""
}

type podList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Status   struct {
			Phase             string          `json:"phase"`
			Conditions        []conditionJSON `json:"conditions"`
			ContainerStatuses []struct {
				Name      string `json:"name"`
				State     state  `json:"state"`
				LastState state  `json:"lastState"`
			} `json:"containerStatuses"`
			InitContainerStatuses []struct {
				Name      string `json:"name"`
				State     state  `json:"state"`
				LastState state  `json:"lastState"`
			} `json:"initContainerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type state struct {
	Waiting *struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"waiting"`
	Terminated *struct {
		Reason     string `json:"reason"`
		Message    string `json:"message"`
		FinishedAt string `json:"finishedAt"`
	} `json:"terminated"`
}

func (r *doctorReport) addPods(ctx context.Context, namespace string, pendingGrace time.Duration) {
	before := len(r.Checks)
	var list podList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "pods", &list); err != nil {
		r.add(doctorGroupPods, "Pod", "", "pods", statusFail, err.Error())
		return
	}
	for _, pod := range list.Items {
		if pod.Status.Phase == "Pending" {
			if isWithinGrace(pod.Metadata.CreationTimestamp, pendingGrace) {
				continue
			}
			detail := "Pending"
			for _, c := range pod.Status.Conditions {
				if c.Type == "PodScheduled" && c.Status == "False" {
					detail = conditionDetail(c)
					break
				}
			}
			r.add(doctorGroupPods, "Pod", pod.Metadata.Namespace, pod.Metadata.Name, statusFail, detail)
			continue
		}
		if status, detail, ok := podContainerProblem(pod.Status.ContainerStatuses); ok {
			r.add(doctorGroupPods, "Pod", pod.Metadata.Namespace, pod.Metadata.Name, status, detail)
			continue
		}
		if status, detail, ok := podContainerProblem(pod.Status.InitContainerStatuses); ok {
			r.add(doctorGroupPods, "Pod", pod.Metadata.Namespace, pod.Metadata.Name, status, detail)
		}
	}
	if len(r.Checks) == before {
		r.add(doctorGroupPods, "Pod", "", "all", statusPass, "no CrashLoopBackOff/ImagePullBackOff/Pending/recent OOMKilled pods")
	}
}

func podContainerProblem(statuses []struct {
	Name      string `json:"name"`
	State     state  `json:"state"`
	LastState state  `json:"lastState"`
}) (doctorStatus, string, bool) {
	for _, c := range statuses {
		if c.State.Waiting != nil {
			switch c.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff":
				return statusFail, fmt.Sprintf("%s: %s", c.Name, c.State.Waiting.Reason), true
			}
		}
		if c.LastState.Terminated != nil && c.LastState.Terminated.Reason == "OOMKilled" {
			if isRecentTimestamp(c.LastState.Terminated.FinishedAt, doctorRecentOOMWindow) {
				return statusWarn, fmt.Sprintf("%s: OOMKilled recently", c.Name), true
			}
		}
	}
	return "", "", false
}

func isRecentTimestamp(raw string, within time.Duration) bool {
	if raw == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return nowFn().Sub(ts) <= within
}

func isWithinGrace(raw string, grace time.Duration) bool {
	if raw == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return nowFn().Sub(ts) <= grace
}

type cephClusterList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Status   struct {
			Ceph struct {
				Health string `json:"health"`
			} `json:"ceph"`
		} `json:"status"`
	} `json:"items"`
}

func (r *doctorReport) addCeph(ctx context.Context, namespace string) {
	before := len(r.Checks)
	var list cephClusterList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, cephClusterResource, &list); err != nil {
		r.add(doctorGroupCeph, "CephCluster", "", "cephclusters", statusFail, err.Error())
		return
	}
	for _, item := range list.Items {
		switch item.Status.Ceph.Health {
		case "HEALTH_OK":
			r.add(doctorGroupCeph, "CephCluster", item.Metadata.Namespace, item.Metadata.Name, statusPass, "HEALTH_OK")
		case "HEALTH_WARN":
			r.add(doctorGroupCeph, "CephCluster", item.Metadata.Namespace, item.Metadata.Name, statusWarn, "HEALTH_WARN")
		case "HEALTH_ERR":
			r.add(doctorGroupCeph, "CephCluster", item.Metadata.Namespace, item.Metadata.Name, statusFail, "HEALTH_ERR")
		default:
			r.add(doctorGroupCeph, "CephCluster", item.Metadata.Namespace, item.Metadata.Name, statusWarn, "unknown health "+item.Status.Ceph.Health)
		}
	}
	if len(r.Checks) == before {
		r.add(doctorGroupCeph, "CephCluster", "", "all", statusPass, "no CephCluster resources found")
	}
}

type certificateList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Status   struct {
			NotAfter   string          `json:"notAfter"`
			Conditions []conditionJSON `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func (r *doctorReport) addCertificates(ctx context.Context, namespace string) {
	before := len(r.Checks)
	var list certificateList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, certificateResource, &list); err != nil {
		r.add(doctorGroupCerts, "Certificate", "", "certificates", statusFail, err.Error())
		return
	}
	for _, cert := range list.Items {
		ready, ok := readyCondition(cert.Status.Conditions)
		if !ok || ready.Status != "True" {
			detail := "Ready condition missing"
			if ok {
				detail = conditionDetail(ready)
			}
			r.add(doctorGroupCerts, "Certificate", cert.Metadata.Namespace, cert.Metadata.Name, statusFail, detail)
			continue
		}
		if cert.Status.NotAfter == "" {
			r.add(doctorGroupCerts, "Certificate", cert.Metadata.Namespace, cert.Metadata.Name, statusWarn, "notAfter missing")
			continue
		}
		notAfter, err := time.Parse(time.RFC3339, cert.Status.NotAfter)
		if err != nil {
			r.add(doctorGroupCerts, "Certificate", cert.Metadata.Namespace, cert.Metadata.Name, statusWarn, "parse notAfter: "+err.Error())
			continue
		}
		remaining := notAfter.Sub(nowFn())
		switch {
		case remaining <= 0:
			r.add(doctorGroupCerts, "Certificate", cert.Metadata.Namespace, cert.Metadata.Name, statusFail, "expired "+notAfter.Format(time.RFC3339))
		case remaining <= doctorCertExpiryWarn:
			r.add(doctorGroupCerts, "Certificate", cert.Metadata.Namespace, cert.Metadata.Name, statusWarn, "expires "+notAfter.Format(time.RFC3339))
		}
	}
	if len(r.Checks) == before {
		r.add(doctorGroupCerts, "Certificate", "", "all", statusPass, "all certificates are Ready and not expiring within 14 days")
	}
}
