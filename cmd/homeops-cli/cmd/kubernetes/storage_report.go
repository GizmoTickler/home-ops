package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/config"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ui"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	storageDefaultCephWarnPercent = 80.0
	storageAvailablePVMaxAge      = 24 * time.Hour
)

type storageFindingStatus string

const (
	storageOK   storageFindingStatus = "OK"
	storageWarn storageFindingStatus = "WARN"
	storageFail storageFindingStatus = "FAIL"
)

type storagePVC struct {
	Metadata struct {
		Name              string                    `json:"name"`
		Namespace         string                    `json:"namespace"`
		CreationTimestamp string                    `json:"creationTimestamp"`
		Labels            map[string]string         `json:"labels"`
		OwnerReferences   []kubeutil.OwnerReference `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		StorageClassName string `json:"storageClassName"`
		VolumeName       string `json:"volumeName"`
		Resources        struct {
			Requests map[string]string `json:"requests"`
		} `json:"resources"`
		DataSourceRef *struct {
			APIGroup string `json:"apiGroup"`
			Kind     string `json:"kind"`
			Name     string `json:"name"`
		} `json:"dataSourceRef"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type storagePVCList struct {
	Items []storagePVC `json:"items"`
}

type storagePodList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Volumes []struct {
				PersistentVolumeClaim *struct {
					ClaimName string `json:"claimName"`
				} `json:"persistentVolumeClaim"`
			} `json:"volumes"`
		} `json:"spec"`
	} `json:"items"`
}

type storageWorkloadList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Template struct {
				Spec struct {
					Volumes []struct {
						PersistentVolumeClaim *struct {
							ClaimName string `json:"claimName"`
						} `json:"persistentVolumeClaim"`
					} `json:"volumes"`
				} `json:"spec"`
			} `json:"template"`
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec struct {
							Volumes []struct {
								PersistentVolumeClaim *struct {
									ClaimName string `json:"claimName"`
								} `json:"persistentVolumeClaim"`
							} `json:"volumes"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	} `json:"items"`
}

type storageStatefulSetList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Template struct {
				Spec struct {
					Volumes []struct {
						PersistentVolumeClaim *struct {
							ClaimName string `json:"claimName"`
						} `json:"persistentVolumeClaim"`
					} `json:"volumes"`
				} `json:"spec"`
			} `json:"template"`
			VolumeClaimTemplates []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"volumeClaimTemplates"`
		} `json:"spec"`
	} `json:"items"`
}

type storageReplicationList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			SourcePVC      string `json:"sourcePVC"`
			DestinationPVC string `json:"destinationPVC"`
		} `json:"spec"`
	} `json:"items"`
}

type storagePV struct {
	Metadata metadataJSON `json:"metadata"`
	Spec     struct {
		StorageClassName string            `json:"storageClassName"`
		Capacity         map[string]string `json:"capacity"`
		ClaimRef         *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"claimRef"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type storagePVList struct {
	Items []storagePV `json:"items"`
}

type byteCount int64

func (b *byteCount) UnmarshalJSON(raw []byte) error {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err == nil {
			*b = byteCount(value)
			return nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("capacity value must be a number or numeric string")
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("parse capacity %q: %w", text, err)
	}
	*b = byteCount(value)
	return nil
}

type cephCapacityItem struct {
	Metadata metadataJSON `json:"metadata"`
	Status   struct {
		Ceph struct {
			Health   string `json:"health"`
			Capacity struct {
				BytesUsed      byteCount `json:"bytesUsed"`
				BytesTotal     byteCount `json:"bytesTotal"`
				BytesAvailable byteCount `json:"bytesAvailable"`
			} `json:"capacity"`
		} `json:"ceph"`
	} `json:"status"`
}

type cephCapacityList struct {
	Items []cephCapacityItem `json:"items"`
}

type orphanedPVC struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	StorageClass string `json:"storage_class"`
	Size         string `json:"size"`
	Phase        string `json:"phase"`
	Age          string `json:"age"`
}

type pvIssue struct {
	Name         string               `json:"name"`
	StorageClass string               `json:"storage_class"`
	Size         string               `json:"size"`
	Phase        string               `json:"phase"`
	Age          string               `json:"age"`
	Claim        string               `json:"claim,omitempty"`
	Status       storageFindingStatus `json:"status"`
}

type cephCapacity struct {
	Namespace   string               `json:"namespace"`
	Name        string               `json:"name"`
	Health      string               `json:"health"`
	UsedBytes   int64                `json:"used_bytes"`
	TotalBytes  int64                `json:"total_bytes"`
	UsedPercent float64              `json:"used_percent"`
	Status      storageFindingStatus `json:"status"`
}

type storageProvisioning struct {
	StorageClass string               `json:"storage_class"`
	Requested    int64                `json:"requested_bytes"`
	CephCapacity int64                `json:"ceph_capacity_bytes"`
	Ratio        float64              `json:"ratio"`
	Status       storageFindingStatus `json:"status"`
}

type volSyncGap struct {
	Namespace    string `json:"namespace"`
	PVC          string `json:"pvc"`
	StorageClass string `json:"storage_class"`
	Size         string `json:"size"`
}

type storageReport struct {
	Findings        int                   `json:"findings"`
	OrphanedPVCs    []orphanedPVC         `json:"orphaned_pvcs"`
	PVIssues        []pvIssue             `json:"pv_issues"`
	CephCapacity    []cephCapacity        `json:"ceph_capacity"`
	Provisioning    []storageProvisioning `json:"provisioned_vs_capacity"`
	VolSyncCoverage []volSyncGap          `json:"volsync_coverage_gaps"`
	Errors          []string              `json:"errors,omitempty"`
}

var (
	storageNowFn    = time.Now
	storageConfigFn = config.Get
)

func newStorageReportCommand() *cobra.Command {
	var namespace, output string
	var cephWarnPercent float64
	var failOnFindings bool
	cmd := &cobra.Command{
		Use:          "storage-report",
		Short:        "Audit Kubernetes storage hygiene and backup coverage",
		SilenceUsage: true,
		Example: `  homeops-cli k8s storage-report
  homeops-cli k8s storage-report --namespace media --ceph-warn-percent 75
  homeops-cli k8s storage-report --output json --fail-on-findings`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}
			if cephWarnPercent < 0 || cephWarnPercent > 100 {
				return fmt.Errorf("--ceph-warn-percent must be between 0 and 100")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			report := buildStorageReport(ctx, namespace, cephWarnPercent)
			rendered, err := renderStorageReport(report, output)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			if failOnFindings && report.Findings > 0 {
				return fmt.Errorf("storage report found %d issue(s)", report.Findings)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to inspect (default: all namespaces)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().Float64Var(&cephWarnPercent, "ceph-warn-percent", storageDefaultCephWarnPercent, "warn when Ceph raw capacity usage reaches this percentage")
	cmd.Flags().BoolVar(&failOnFindings, "fail-on-findings", false, "return a non-zero exit code when findings are present")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func buildStorageReport(ctx context.Context, namespace string, cephWarnPercent float64) storageReport {
	var report storageReport
	var pvcs storagePVCList
	pvcOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "persistentvolumeclaims", &pvcs); err != nil {
		report.Errors = append(report.Errors, err.Error())
		pvcOK = false
	}
	var pods storagePodList
	podsOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "pods", &pods); err != nil {
		report.Errors = append(report.Errors, err.Error())
		podsOK = false
	}
	var statefulSets storageStatefulSetList
	statefulSetsOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "statefulsets.apps", &statefulSets); err != nil {
		report.Errors = append(report.Errors, err.Error())
		statefulSetsOK = false
	}
	var workloads storageWorkloadList
	workloadsOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "deployments.apps,replicasets.apps,daemonsets.apps,jobs.batch,cronjobs.batch", &workloads); err != nil {
		report.Errors = append(report.Errors, err.Error())
		workloadsOK = false
	}
	var sources, destinations storageReplicationList
	sourcesOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "replicationsources.volsync.backube", &sources); err != nil {
		report.Errors = append(report.Errors, err.Error())
		sourcesOK = false
	}
	destinationsOK := true
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "replicationdestinations.volsync.backube", &destinations); err != nil {
		report.Errors = append(report.Errors, err.Error())
		destinationsOK = false
	}

	if pvcOK && podsOK && statefulSetsOK && workloadsOK && sourcesOK && destinationsOK {
		report.OrphanedPVCs = detectOrphanedPVCs(pvcs.Items, pods, statefulSets, workloads, sources, destinations, storageNowFn())
	}
	if pvcOK && sourcesOK {
		report.VolSyncCoverage = detectVolSyncGaps(pvcs.Items, sources)
	}

	var pvs storagePVList
	if err := kubeutil.GetClusterJSON(ctx, kubectlOutputCtxFn, "persistentvolumes", &pvs); err != nil {
		report.Errors = append(report.Errors, err.Error())
	} else {
		report.PVIssues = detectPVIssues(pvs.Items, namespace, storageNowFn(), storageAvailablePVMaxAge)
	}

	var clusters cephCapacityList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, storageConfigFn().Cluster.Rook.Namespace, cephClusterResource, &clusters); err != nil {
		report.Errors = append(report.Errors, err.Error())
	} else {
		report.CephCapacity = parseCephCapacities(clusters.Items, cephWarnPercent)
	}
	totalCeph := int64(0)
	for _, capacity := range report.CephCapacity {
		totalCeph += capacity.TotalBytes
	}
	if pvcOK {
		report.Provisioning = calculateProvisioning(pvcs.Items, totalCeph)
	}
	report.Findings = len(report.OrphanedPVCs) + len(report.PVIssues) + len(report.VolSyncCoverage) + len(report.Errors)
	if len(report.CephCapacity) == 0 {
		report.Findings++
	}
	for _, capacity := range report.CephCapacity {
		if capacity.Status != storageOK {
			report.Findings++
		}
	}
	for _, provisioned := range report.Provisioning {
		if provisioned.Status != storageOK {
			report.Findings++
		}
	}
	return report
}

func detectOrphanedPVCs(pvcs []storagePVC, pods storagePodList, statefulSets storageStatefulSetList, workloads storageWorkloadList, sources, destinations storageReplicationList, now time.Time) []orphanedPVC {
	used := map[string]bool{}
	statefulSetPrefixes := map[string]bool{}
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				used[namespacedName(pod.Metadata.Namespace, volume.PersistentVolumeClaim.ClaimName)] = true
			}
		}
	}
	for _, list := range []storageReplicationList{sources, destinations} {
		for _, item := range list.Items {
			for _, claim := range []string{item.Spec.SourcePVC, item.Spec.DestinationPVC} {
				if claim != "" {
					used[namespacedName(item.Metadata.Namespace, claim)] = true
				}
			}
		}
	}
	for _, sts := range statefulSets.Items {
		markStorageVolumesUsed(used, sts.Metadata.Namespace, sts.Spec.Template.Spec.Volumes)
		for _, claim := range sts.Spec.VolumeClaimTemplates {
			statefulSetPrefixes[namespacedName(sts.Metadata.Namespace, claim.Metadata.Name+"-"+sts.Metadata.Name)] = true
		}
	}
	for _, workload := range workloads.Items {
		markStorageVolumesUsed(used, workload.Metadata.Namespace, workload.Spec.Template.Spec.Volumes)
		markStorageVolumesUsed(used, workload.Metadata.Namespace, workload.Spec.JobTemplate.Spec.Template.Spec.Volumes)
	}
	var result []orphanedPVC
	for _, pvc := range pvcs {
		key := namespacedName(pvc.Metadata.Namespace, pvc.Metadata.Name)
		if used[key] || matchesStatefulSetClaim(key, statefulSetPrefixes) || kubeutil.HasWorkloadOwner(pvc.Metadata.OwnerReferences) ||
			kubeutil.IsVolSyncPlumbingPVC(pvc.Metadata.Name, pvc.Metadata.Labels, pvc.Metadata.OwnerReferences) ||
			isReplicationDestinationDataSource(pvc) {
			continue
		}
		result = append(result, orphanedPVC{
			Namespace: pvc.Metadata.Namespace, Name: pvc.Metadata.Name, StorageClass: pvc.Spec.StorageClassName,
			Size: pvc.Spec.Resources.Requests["storage"], Phase: pvc.Status.Phase,
			Age: resourceAge(pvc.Metadata.CreationTimestamp, now),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return namespacedName(result[i].Namespace, result[i].Name) < namespacedName(result[j].Namespace, result[j].Name)
	})
	return result
}

func markStorageVolumesUsed(used map[string]bool, namespace string, volumes []struct {
	PersistentVolumeClaim *struct {
		ClaimName string `json:"claimName"`
	} `json:"persistentVolumeClaim"`
}) {
	for _, volume := range volumes {
		if volume.PersistentVolumeClaim != nil {
			used[namespacedName(namespace, volume.PersistentVolumeClaim.ClaimName)] = true
		}
	}
}

func matchesStatefulSetClaim(key string, prefixes map[string]bool) bool {
	for prefix := range prefixes {
		if strings.HasPrefix(key, prefix+"-") {
			return true
		}
	}
	return false
}

func isReplicationDestinationDataSource(pvc storagePVC) bool {
	return pvc.Spec.DataSourceRef != nil && pvc.Spec.DataSourceRef.Kind == "ReplicationDestination" &&
		strings.Contains(pvc.Spec.DataSourceRef.APIGroup, "volsync.backube")
}

func detectPVIssues(pvs []storagePV, namespace string, now time.Time, availableAge time.Duration) []pvIssue {
	var result []pvIssue
	for _, pv := range pvs {
		if namespace != "" && (pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != namespace) {
			continue
		}
		age := parsedAge(pv.Metadata.CreationTimestamp, now)
		status := storageWarn
		problem := pv.Status.Phase == "Released" || pv.Status.Phase == "Failed" ||
			(pv.Status.Phase == "Available" && age > availableAge)
		if !problem {
			continue
		}
		if pv.Status.Phase == "Failed" {
			status = storageFail
		}
		claim := ""
		if pv.Spec.ClaimRef != nil {
			claim = namespacedName(pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
		}
		result = append(result, pvIssue{Name: pv.Metadata.Name, StorageClass: pv.Spec.StorageClassName,
			Size: pv.Spec.Capacity["storage"], Phase: pv.Status.Phase, Age: displayAge(age), Claim: claim, Status: status})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func parseCephCapacities(items []cephCapacityItem, warnPercent float64) []cephCapacity {
	result := make([]cephCapacity, 0, len(items))
	for _, item := range items {
		used, total := int64(item.Status.Ceph.Capacity.BytesUsed), int64(item.Status.Ceph.Capacity.BytesTotal)
		percent := 0.0
		if total > 0 {
			percent = float64(used) / float64(total) * 100
		}
		status := storageOK
		if item.Status.Ceph.Health == "HEALTH_ERR" || total <= 0 {
			status = storageFail
		} else if item.Status.Ceph.Health != "HEALTH_OK" || percent >= warnPercent {
			status = storageWarn
		}
		result = append(result, cephCapacity{Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
			Health: item.Status.Ceph.Health, UsedBytes: used, TotalBytes: total, UsedPercent: percent, Status: status})
	}
	sort.Slice(result, func(i, j int) bool {
		return namespacedName(result[i].Namespace, result[i].Name) < namespacedName(result[j].Namespace, result[j].Name)
	})
	return result
}

func calculateProvisioning(pvcs []storagePVC, cephCapacity int64) []storageProvisioning {
	requested := map[string]int64{}
	for _, pvc := range pvcs {
		quantity, err := resource.ParseQuantity(pvc.Spec.Resources.Requests["storage"])
		if err != nil {
			continue
		}
		requested[pvc.Spec.StorageClassName] += quantity.Value()
	}
	classes := make([]string, 0, len(requested))
	for class := range requested {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	result := make([]storageProvisioning, 0, len(classes))
	for _, class := range classes {
		ratio := 0.0
		status := storageOK
		if cephCapacity > 0 {
			ratio = float64(requested[class]) / float64(cephCapacity)
			if ratio > 1 {
				status = storageWarn
			}
		} else {
			status = storageWarn
		}
		result = append(result, storageProvisioning{StorageClass: class, Requested: requested[class],
			CephCapacity: cephCapacity, Ratio: ratio, Status: status})
	}
	return result
}

func detectVolSyncGaps(pvcs []storagePVC, sources storageReplicationList) []volSyncGap {
	covered := map[string]bool{}
	for _, source := range sources.Items {
		if source.Spec.SourcePVC != "" {
			covered[namespacedName(source.Metadata.Namespace, source.Spec.SourcePVC)] = true
		}
	}
	var result []volSyncGap
	for _, pvc := range pvcs {
		key := namespacedName(pvc.Metadata.Namespace, pvc.Metadata.Name)
		if covered[key] || kubeutil.IsSystemNamespace(pvc.Metadata.Namespace) ||
			kubeutil.IsVolSyncPlumbingPVC(pvc.Metadata.Name, pvc.Metadata.Labels, pvc.Metadata.OwnerReferences) ||
			kubeutil.IsPodOwnedPVC(pvc.Metadata.OwnerReferences) {
			continue
		}
		result = append(result, volSyncGap{Namespace: pvc.Metadata.Namespace, PVC: pvc.Metadata.Name,
			StorageClass: pvc.Spec.StorageClassName, Size: pvc.Spec.Resources.Requests["storage"]})
	}
	sort.Slice(result, func(i, j int) bool {
		return namespacedName(result[i].Namespace, result[i].PVC) < namespacedName(result[j].Namespace, result[j].PVC)
	})
	return result
}

func renderStorageReport(report storageReport, output string) (string, error) {
	if output == "json" {
		return ui.RenderJSON(report)
	}
	if output != "" {
		if err := ui.ValidateOutputFormat(output); err != nil {
			return "", err
		}
	}
	var rows [][]string
	for _, pvc := range report.OrphanedPVCs {
		rows = append(rows, []string{string(storageWarn), "ORPHANED PVCs", namespacedName(pvc.Namespace, pvc.Name),
			fmt.Sprintf("class=%s size=%s phase=%s age=%s", pvc.StorageClass, pvc.Size, pvc.Phase, pvc.Age)})
	}
	if len(report.OrphanedPVCs) == 0 {
		rows = append(rows, []string{string(storageOK), "ORPHANED PVCs", "-", "none"})
	}
	for _, pv := range report.PVIssues {
		rows = append(rows, []string{string(pv.Status), "PV ISSUES", pv.Name,
			fmt.Sprintf("class=%s size=%s phase=%s age=%s claim=%s", pv.StorageClass, pv.Size, pv.Phase, pv.Age, pv.Claim)})
	}
	if len(report.PVIssues) == 0 {
		rows = append(rows, []string{string(storageOK), "PV ISSUES", "-", "none"})
	}
	for _, capacity := range report.CephCapacity {
		rows = append(rows, []string{string(capacity.Status), "CEPH CAPACITY", namespacedName(capacity.Namespace, capacity.Name),
			fmt.Sprintf("health=%s used=%s/%s (%.1f%%)", capacity.Health, humanBytes(capacity.UsedBytes), humanBytes(capacity.TotalBytes), capacity.UsedPercent)})
	}
	if len(report.CephCapacity) == 0 {
		rows = append(rows, []string{string(storageWarn), "CEPH CAPACITY", "-", "no CephCluster capacity reported"})
	}
	for _, item := range report.Provisioning {
		rows = append(rows, []string{string(item.Status), "PROVISIONED vs CAPACITY", emptyName(item.StorageClass),
			fmt.Sprintf("requested=%s ceph_raw=%s ratio=%.2fx", humanBytes(item.Requested), humanBytes(item.CephCapacity), item.Ratio)})
	}
	if len(report.Provisioning) == 0 {
		rows = append(rows, []string{string(storageOK), "PROVISIONED vs CAPACITY", "-", "no PVC requests"})
	}
	for _, gap := range report.VolSyncCoverage {
		rows = append(rows, []string{string(storageWarn), "VOLSYNC COVERAGE", namespacedName(gap.Namespace, gap.PVC),
			fmt.Sprintf("class=%s size=%s has no ReplicationSource", gap.StorageClass, gap.Size)})
	}
	if len(report.VolSyncCoverage) == 0 {
		rows = append(rows, []string{string(storageOK), "VOLSYNC COVERAGE", "-", "all eligible PVCs have a ReplicationSource"})
	}
	for _, detail := range report.Errors {
		rows = append(rows, []string{string(storageFail), "API ERRORS", "-", detail})
	}
	return fmt.Sprintf("Findings: %d\n%s", report.Findings, ui.Table([]string{"STATUS", "GROUP", "NAME", "DETAIL"}, rows)), nil
}

func resourceAge(raw string, now time.Time) string { return displayAge(parsedAge(raw, now)) }

func parsedAge(raw string, now time.Time) time.Duration {
	created, err := time.Parse(time.RFC3339, raw)
	if err != nil || now.Before(created) {
		return 0
	}
	return now.Sub(created)
}

func displayAge(age time.Duration) string {
	if age >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	}
	return age.Round(time.Minute).String()
}

func emptyName(value string) string {
	if value == "" {
		return "<default>"
	}
	return value
}
