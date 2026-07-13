package volsync

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/ui"
)

type volsyncStatus string

const (
	volsyncPass volsyncStatus = "PASS"
	volsyncWarn volsyncStatus = "WARN"
	volsyncFail volsyncStatus = "FAIL"
)

type volsyncStatusSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type volsyncSourceStatus struct {
	App                    string        `json:"app"`
	Namespace              string        `json:"namespace"`
	SourcePVC              string        `json:"source_pvc"`
	LastSuccessfulSyncTime string        `json:"last_successful_sync_time,omitempty"`
	Age                    string        `json:"age,omitempty"`
	Result                 string        `json:"result,omitempty"`
	Status                 volsyncStatus `json:"status"`
	Detail                 string        `json:"detail"`
}

type volsyncMissingBackup struct {
	PVC       string        `json:"pvc"`
	Namespace string        `json:"namespace"`
	Status    volsyncStatus `json:"status"`
	Detail    string        `json:"detail"`
}

type volsyncStatusReport struct {
	Summary        volsyncStatusSummary   `json:"summary"`
	Sources        []volsyncSourceStatus  `json:"sources"`
	MissingBackups []volsyncMissingBackup `json:"missing_backups"`
}

func (r *volsyncStatusReport) finalize() {
	r.Summary = volsyncStatusSummary{}
	for _, s := range r.Sources {
		r.addSummary(s.Status)
	}
	for _, m := range r.MissingBackups {
		r.addSummary(m.Status)
	}
}

func (r *volsyncStatusReport) addSummary(status volsyncStatus) {
	switch status {
	case volsyncFail:
		r.Summary.Fail++
	case volsyncWarn:
		r.Summary.Warn++
	default:
		r.Summary.Pass++
	}
}

func (r volsyncStatusReport) hasFail() bool {
	return r.Summary.Fail > 0
}

func newStatusCommand() *cobra.Command {
	var namespace, output string
	var staleAfter time.Duration
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Report VolSync backup freshness and missing PVC backups",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolsyncStatus(namespace, output, staleAfter, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to inspect (default: all namespaces)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().DurationVar(&staleAfter, "stale-after", 24*time.Hour, "warn when the last successful sync is older than this duration")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func runVolsyncStatus(namespace, output string, staleAfter time.Duration, out io.Writer) error {
	report := buildVolsyncStatusReport(namespace, staleAfter)
	rendered, err := renderVolsyncStatusReport(report, output)
	if err != nil {
		return err
	}
	if rendered != "" {
		_, _ = fmt.Fprintln(out, rendered)
	}
	if report.hasFail() {
		return fmt.Errorf("volsync status found %d failing backup(s)", report.Summary.Fail)
	}
	return nil
}

func buildVolsyncStatusReport(namespace string, staleAfter time.Duration) volsyncStatusReport {
	var report volsyncStatusReport

	sources, err := listReplicationSourceStatuses(namespace, staleAfter)
	if err != nil {
		report.Sources = append(report.Sources, volsyncSourceStatus{
			App:    "replicationsources",
			Status: volsyncFail,
			Detail: err.Error(),
		})
		report.finalize()
		return report
	}
	report.Sources = sources

	missing, err := listPVCsWithoutReplicationSource(namespace, sources)
	if err != nil {
		report.MissingBackups = append(report.MissingBackups, volsyncMissingBackup{
			PVC:    "persistentvolumeclaims",
			Status: volsyncWarn,
			Detail: err.Error(),
		})
	} else {
		report.MissingBackups = missing
	}

	report.finalize()
	return report
}

type replicationSourceList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			SourcePVC string `json:"sourcePVC"`
		} `json:"spec"`
		Status struct {
			LastSyncTime      string `json:"lastSyncTime"`
			LatestMoverStatus struct {
				Result string `json:"result"`
			} `json:"latestMoverStatus"`
		} `json:"status"`
	} `json:"items"`
}

func listReplicationSourceStatuses(namespace string, staleAfter time.Duration) ([]volsyncSourceStatus, error) {
	var list replicationSourceList
	if err := volsyncKubectlGetJSON(namespace, "replicationsources", &list); err != nil {
		return nil, err
	}
	out := make([]volsyncSourceStatus, 0, len(list.Items))
	for _, item := range list.Items {
		result := strings.TrimSpace(item.Status.LatestMoverStatus.Result)
		if result == "" {
			result = "Unknown"
		}
		status := volsyncPass
		detail := "last sync successful"
		ageText := ""
		lastSync := strings.TrimSpace(item.Status.LastSyncTime)
		if lastSync == "" {
			status = volsyncFail
			detail = "never synced"
		} else {
			ts, err := time.Parse(time.RFC3339, lastSync)
			if err != nil {
				status = volsyncFail
				detail = "invalid lastSyncTime: " + err.Error()
			} else {
				age := volsyncNow().Sub(ts)
				if age < 0 {
					age = 0
				}
				ageText = age.Round(time.Second).String()
				if !strings.EqualFold(result, "Successful") {
					status = volsyncFail
					detail = "last sync result " + result
				} else if age > staleAfter {
					status = volsyncWarn
					detail = "last successful sync older than " + staleAfter.String()
				}
			}
		}
		out = append(out, volsyncSourceStatus{
			App:                    item.Metadata.Name,
			Namespace:              item.Metadata.Namespace,
			SourcePVC:              item.Spec.SourcePVC,
			LastSuccessfulSyncTime: lastSync,
			Age:                    ageText,
			Result:                 result,
			Status:                 status,
			Detail:                 detail,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace == out[j].Namespace {
			return out[i].App < out[j].App
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out, nil
}

type pvcList struct {
	Items []struct {
		Metadata struct {
			Name            string            `json:"name"`
			Namespace       string            `json:"namespace"`
			Labels          map[string]string `json:"labels"`
			OwnerReferences []struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
				Name       string `json:"name"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
	} `json:"items"`
}

func listPVCsWithoutReplicationSource(namespace string, sources []volsyncSourceStatus) ([]volsyncMissingBackup, error) {
	protectedNamespaces := map[string]bool{}
	protectedPVCs := map[string]bool{}
	for _, s := range sources {
		if s.Namespace == "" {
			continue
		}
		protectedNamespaces[s.Namespace] = true
		if s.SourcePVC != "" {
			protectedPVCs[s.Namespace+"/"+s.SourcePVC] = true
		}
	}
	if len(protectedNamespaces) == 0 {
		return nil, nil
	}

	var list pvcList
	if err := volsyncKubectlGetJSON(namespace, "pvc", &list); err != nil {
		return nil, err
	}
	var missing []volsyncMissingBackup
	for _, pvc := range list.Items {
		if !protectedNamespaces[pvc.Metadata.Namespace] {
			continue
		}
		key := pvc.Metadata.Namespace + "/" + pvc.Metadata.Name
		if isVolsyncPlumbingPVC(pvc.Metadata.Name, pvc.Metadata.Labels, pvc.Metadata.OwnerReferences) {
			continue
		}
		if !protectedPVCs[key] {
			missing = append(missing, volsyncMissingBackup{
				PVC:       pvc.Metadata.Name,
				Namespace: pvc.Metadata.Namespace,
				Status:    volsyncWarn,
				Detail:    "PVC has no ReplicationSource in a namespace that uses VolSync",
			})
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Namespace == missing[j].Namespace {
			return missing[i].PVC < missing[j].PVC
		}
		return missing[i].Namespace < missing[j].Namespace
	})
	return missing, nil
}

func isVolsyncPlumbingPVC(name string, labels map[string]string, ownerReferences []struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}) bool {
	for _, ref := range ownerReferences {
		if (ref.Kind == "ReplicationSource" || ref.Kind == "ReplicationDestination") &&
			strings.Contains(ref.APIVersion, "volsync.backube") {
			return true
		}
	}
	for _, key := range []string{
		"app.kubernetes.io/created-by",
		"app.kubernetes.io/managed-by",
		"volsync.backube/created-by",
	} {
		if strings.EqualFold(labels[key], "volsync") {
			return true
		}
	}
	return strings.HasPrefix(name, "volsync-") &&
		(strings.HasSuffix(name, "-src") ||
			strings.HasSuffix(name, "-cache"))
}

func volsyncKubectlGetJSON(namespace, resource string, dest interface{}) error {
	args := []string{"get", resource}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	} else {
		args = append(args, "-A")
	}
	args = append(args, "-o", "json")
	out, err := commandOutputFn("kubectl", args...)
	if err != nil {
		return fmt.Errorf("kubectl get %s: %w", resource, err)
	}
	if err := json.Unmarshal(out, dest); err != nil {
		return fmt.Errorf("parse kubectl %s json: %w", resource, err)
	}
	return nil
}

func renderVolsyncStatusReport(report volsyncStatusReport, output string) (string, error) {
	switch output {
	case "", "table":
		var b strings.Builder
		fmt.Fprintf(&b, "Summary: PASS=%d WARN=%d FAIL=%d\n", report.Summary.Pass, report.Summary.Warn, report.Summary.Fail)
		sourceRows := make([][]string, 0, len(report.Sources))
		for _, s := range report.Sources {
			sourceRows = append(sourceRows, []string{
				string(s.Status), s.Namespace, s.App, s.SourcePVC, s.LastSuccessfulSyncTime, s.Age, s.Result, s.Detail,
			})
		}
		b.WriteString(ui.Table([]string{"STATUS", "NAMESPACE", "APP", "PVC", "LAST SUCCESS", "AGE", "RESULT", "DETAIL"}, sourceRows))
		if len(report.MissingBackups) > 0 {
			b.WriteString("\n\nPVCs without ReplicationSource\n")
			missingRows := make([][]string, 0, len(report.MissingBackups))
			for _, m := range report.MissingBackups {
				missingRows = append(missingRows, []string{string(m.Status), m.Namespace, m.PVC, m.Detail})
			}
			b.WriteString(ui.Table([]string{"STATUS", "NAMESPACE", "PVC", "DETAIL"}, missingRows))
		}
		return b.String(), nil
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
