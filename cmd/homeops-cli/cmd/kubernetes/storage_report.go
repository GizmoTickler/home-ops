package kubernetes

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ui"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	storageAvailablePVMaxAge = 24 * time.Hour

	scaleCSIOrphanVolumesMetric         = "scale_csi_orphan_volumes"
	scaleCSIOrphanSnapshotsMetric       = "scale_csi_orphan_snapshots"
	scaleCSISpentRestoreSnapshotsMetric = "scale_csi_spent_restore_snapshots"
	scaleCSITrueNASConnectionMetric     = "scale_csi_truenas_connection_status"
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

type storageVolumeSnapshotList struct {
	Items []struct {
		Spec struct {
			VolumeSnapshotClassName string `json:"volumeSnapshotClassName"`
		} `json:"spec"`
	} `json:"items"`
}

type storageServiceList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Ports []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	} `json:"items"`
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

type volSyncGap struct {
	Namespace    string `json:"namespace"`
	PVC          string `json:"pvc"`
	StorageClass string `json:"storage_class"`
	Size         string `json:"size"`
}

type storageClassRollup struct {
	StorageClass      string `json:"storage_class"`
	PVCCount          int    `json:"pvc_count"`
	PVCRequestedBytes int64  `json:"pvc_requested_bytes"`
	PVCount           int    `json:"pv_count"`
	PVCapacityBytes   int64  `json:"pv_capacity_bytes"`
}

type volumeSnapshotRollup struct {
	VolumeSnapshotClass string `json:"volume_snapshot_class"`
	Count               int    `json:"count"`
}

type scaleCSIComponentHealth struct {
	Ready   int32                `json:"ready"`
	Desired int32                `json:"desired"`
	Status  storageFindingStatus `json:"status"`
	Detail  string               `json:"detail,omitempty"`
}

type scaleCSIHealth struct {
	Controller scaleCSIComponentHealth `json:"controller"`
	Node       scaleCSIComponentHealth `json:"node"`
	Status     storageFindingStatus    `json:"status"`
}

type scaleCSIMetricsReport struct {
	Available bool                 `json:"available"`
	Service   string               `json:"service,omitempty"`
	Status    storageFindingStatus `json:"status"`
	Values    map[string]float64   `json:"values,omitempty"`
	Missing   []string             `json:"missing,omitempty"`
	Detail    string               `json:"detail,omitempty"`
}

type storageReport struct {
	Findings        int                    `json:"findings"`
	StorageClasses  []storageClassRollup   `json:"storage_class_rollup"`
	VolumeSnapshots []volumeSnapshotRollup `json:"volume_snapshots"`
	ScaleCSIHealth  scaleCSIHealth         `json:"scale_csi_health"`
	ScaleCSIMetrics scaleCSIMetricsReport  `json:"scale_csi_metrics"`
	OrphanedPVCs    []orphanedPVC          `json:"orphaned_pvcs"`
	PVIssues        []pvIssue              `json:"pv_issues"`
	VolSyncCoverage []volSyncGap           `json:"volsync_coverage_gaps"`
	Errors          []string               `json:"errors,omitempty"`
}

var storageNowFn = time.Now

func newStorageReportCommand() *cobra.Command {
	var namespace, output string
	var failOnFindings bool
	cmd := &cobra.Command{
		Use:          "storage-report",
		Short:        "Audit Kubernetes storage hygiene and backup coverage",
		SilenceUsage: true,
		Example: `  homeops-cli k8s storage-report
  homeops-cli k8s storage-report --namespace media
  homeops-cli k8s storage-report --output json --fail-on-findings`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			report := buildStorageReport(ctx, namespace)
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
	cmd.Flags().BoolVar(&failOnFindings, "fail-on-findings", false, "return a non-zero exit code when findings are present")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func buildStorageReport(ctx context.Context, namespace string) storageReport {
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
	pvOK := true
	if err := kubeutil.GetClusterJSON(ctx, kubectlOutputCtxFn, "persistentvolumes", &pvs); err != nil {
		report.Errors = append(report.Errors, err.Error())
		pvOK = false
	} else {
		report.PVIssues = detectPVIssues(pvs.Items, namespace, storageNowFn(), storageAvailablePVMaxAge)
	}

	if pvcOK && pvOK {
		var quantityErrors []string
		report.StorageClasses, quantityErrors = calculateStorageClassRollups(pvcs.Items, pvs.Items, namespace)
		report.Errors = append(report.Errors, quantityErrors...)
	}

	var snapshots storageVolumeSnapshotList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, namespace, "volumesnapshots.snapshot.storage.k8s.io", &snapshots); err != nil {
		report.Errors = append(report.Errors, err.Error())
	} else {
		report.VolumeSnapshots = calculateVolumeSnapshotRollups(snapshots)
	}

	report.ScaleCSIHealth = collectScaleCSIHealth(ctx)
	report.ScaleCSIMetrics = collectScaleCSIMetrics(ctx)
	report.Findings = len(report.OrphanedPVCs) + len(report.PVIssues) + len(report.VolSyncCoverage) + len(report.Errors)
	report.Findings += scaleCSIHealthFindings(report.ScaleCSIHealth)
	report.Findings += scaleCSIMetricsFindings(report.ScaleCSIMetrics)
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

func calculateStorageClassRollups(pvcs []storagePVC, pvs []storagePV, namespace string) ([]storageClassRollup, []string) {
	byClass := map[string]*storageClassRollup{}
	for _, name := range []string{
		constants.ScaleCSIStorageClassNVMeOF,
		constants.ScaleCSIStorageClassISCSI,
		constants.ScaleCSIStorageClassNFS,
	} {
		byClass[name] = &storageClassRollup{StorageClass: name}
	}
	var problems []string
	for _, pvc := range pvcs {
		rollup := storageClassRollupFor(byClass, pvc.Spec.StorageClassName)
		rollup.PVCCount++
		quantity, err := resource.ParseQuantity(pvc.Spec.Resources.Requests["storage"])
		if err != nil {
			problems = append(problems, fmt.Sprintf("parse PVC %s requested capacity: %v", namespacedName(pvc.Metadata.Namespace, pvc.Metadata.Name), err))
			continue
		}
		rollup.PVCRequestedBytes += quantity.Value()
	}
	for _, pv := range pvs {
		if namespace != "" && (pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != namespace) {
			continue
		}
		rollup := storageClassRollupFor(byClass, pv.Spec.StorageClassName)
		rollup.PVCount++
		quantity, err := resource.ParseQuantity(pv.Spec.Capacity["storage"])
		if err != nil {
			problems = append(problems, fmt.Sprintf("parse PV %s capacity: %v", pv.Metadata.Name, err))
			continue
		}
		rollup.PVCapacityBytes += quantity.Value()
	}

	names := make([]string, 0, len(byClass))
	for name := range byClass {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]storageClassRollup, 0, len(names))
	for _, name := range names {
		result = append(result, *byClass[name])
	}
	return result, problems
}

func storageClassRollupFor(byClass map[string]*storageClassRollup, name string) *storageClassRollup {
	if existing, ok := byClass[name]; ok {
		return existing
	}
	rollup := &storageClassRollup{StorageClass: name}
	byClass[name] = rollup
	return rollup
}

func calculateVolumeSnapshotRollups(snapshots storageVolumeSnapshotList) []volumeSnapshotRollup {
	counts := map[string]int{constants.ScaleCSIVolumeSnapshotClass: 0}
	for _, snapshot := range snapshots.Items {
		counts[snapshot.Spec.VolumeSnapshotClassName]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]volumeSnapshotRollup, 0, len(names))
	for _, name := range names {
		result = append(result, volumeSnapshotRollup{VolumeSnapshotClass: name, Count: counts[name]})
	}
	return result
}

func collectScaleCSIHealth(ctx context.Context) scaleCSIHealth {
	health := scaleCSIHealth{Status: storageOK}
	var controller deploymentJSON
	if err := getScaleCSIObject(ctx, deploymentResource, constants.ScaleCSIController, &controller); err != nil {
		health.Controller = scaleCSIComponentHealth{Status: storageFail, Detail: err.Error()}
		health.Status = storageFail
	} else {
		health.Controller = scaleCSIComponentHealth{
			Ready: controller.Status.ReadyReplicas, Desired: controller.Spec.Replicas, Status: storageOK,
		}
		if controller.Spec.Replicas <= 0 || controller.Status.ReadyReplicas != controller.Spec.Replicas {
			health.Controller.Status = storageFail
			health.Status = storageFail
		}
	}

	var node daemonSetJSON
	if err := getScaleCSIObject(ctx, daemonSetResource, constants.ScaleCSINode, &node); err != nil {
		health.Node = scaleCSIComponentHealth{Status: storageFail, Detail: err.Error()}
		health.Status = storageFail
	} else {
		health.Node = scaleCSIComponentHealth{
			Ready: node.Status.NumberReady, Desired: node.Status.DesiredNumberScheduled, Status: storageOK,
		}
		if node.Status.DesiredNumberScheduled <= 0 || node.Status.NumberReady != node.Status.DesiredNumberScheduled {
			health.Node.Status = storageFail
			health.Status = storageFail
		}
	}
	return health
}

func collectScaleCSIMetrics(ctx context.Context) scaleCSIMetricsReport {
	report := scaleCSIMetricsReport{Status: storageWarn}
	var services storageServiceList
	if err := kubeutil.GetJSON(ctx, kubectlOutputCtxFn, constants.NSScaleCSI, "services", &services); err != nil {
		report.Detail = "metrics Service discovery failed: " + err.Error()
		return report
	}
	service, port, err := findScaleCSIMetricsService(services)
	if err != nil {
		report.Detail = err.Error()
		return report
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/services/%s:%d/proxy/metrics", constants.NSScaleCSI, service, port)
	raw, err := kubectlOutputCtxFn(ctx, "get", "--raw", path)
	if err != nil {
		report.Service = service
		report.Detail = "metrics endpoint unreachable: " + err.Error()
		return report
	}
	values, missing, err := parseScaleCSIMetrics(raw)
	if err != nil {
		report.Service = service
		report.Detail = err.Error()
		return report
	}
	report.Available = true
	report.Service = service
	report.Status = storageOK
	report.Values = values
	report.Missing = missing
	if len(missing) > 0 || scaleCSIMetricsFindings(report) > 0 {
		report.Status = storageWarn
	}
	if connection, ok := values[scaleCSITrueNASConnectionMetric]; ok && connection != 1 {
		report.Status = storageFail
	}
	return report
}

func findScaleCSIMetricsService(services storageServiceList) (string, int, error) {
	type candidate struct {
		name string
		port int
	}
	var candidates []candidate
	for _, service := range services.Items {
		for _, port := range service.Spec.Ports {
			if port.Port == constants.ScaleCSIControllerMetricsPort || strings.EqualFold(port.Name, "metrics") {
				candidates = append(candidates, candidate{name: service.Metadata.Name, port: port.Port})
			}
		}
	}
	if len(candidates) == 0 {
		return "", 0, fmt.Errorf("no metrics Service with port %d found in namespace %s", constants.ScaleCSIControllerMetricsPort, constants.NSScaleCSI)
	}
	sort.Slice(candidates, func(i, j int) bool {
		iController := strings.Contains(candidates[i].name, "controller")
		jController := strings.Contains(candidates[j].name, "controller")
		if iController != jController {
			return iController
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].name, candidates[0].port, nil
}

func parseScaleCSIMetrics(raw []byte) (map[string]float64, []string, error) {
	expected := []string{
		scaleCSIOrphanVolumesMetric,
		scaleCSIOrphanSnapshotsMetric,
		scaleCSISpentRestoreSnapshotsMetric,
		scaleCSITrueNASConnectionMetric,
	}
	wanted := make(map[string]bool, len(expected))
	for _, name := range expected {
		wanted[name] = true
	}
	values := map[string]float64{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.SplitN(fields[0], "{", 2)[0]
		if !wanted[name] {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, nil, fmt.Errorf("parse metric %s: %w", name, err)
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan scale-csi metrics: %w", err)
	}
	var missing []string
	for _, name := range expected {
		if _, ok := values[name]; !ok {
			missing = append(missing, name)
		}
	}
	return values, missing, nil
}

func scaleCSIHealthFindings(health scaleCSIHealth) int {
	findings := 0
	if health.Controller.Status != storageOK {
		findings++
	}
	if health.Node.Status != storageOK {
		findings++
	}
	return findings
}

func scaleCSIMetricsFindings(metrics scaleCSIMetricsReport) int {
	if !metrics.Available {
		return 0
	}
	findings := 0
	for _, name := range []string{scaleCSIOrphanVolumesMetric, scaleCSIOrphanSnapshotsMetric, scaleCSISpentRestoreSnapshotsMetric} {
		if value, ok := metrics.Values[name]; ok && value > 0 {
			findings++
		}
	}
	if value, ok := metrics.Values[scaleCSITrueNASConnectionMetric]; ok && value != 1 {
		findings++
	}
	if len(metrics.Missing) > 0 {
		findings++
	}
	return findings
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
	for _, rollup := range report.StorageClasses {
		rows = append(rows, []string{string(storageOK), "STORAGE CLASS", emptyStorageName(rollup.StorageClass),
			fmt.Sprintf("pvcs=%d requested=%s pvs=%d capacity=%s", rollup.PVCCount, humanBytes(rollup.PVCRequestedBytes), rollup.PVCount, humanBytes(rollup.PVCapacityBytes))})
	}
	if len(report.StorageClasses) == 0 {
		rows = append(rows, []string{string(storageWarn), "STORAGE CLASS", "-", "rollup unavailable"})
	}
	for _, snapshots := range report.VolumeSnapshots {
		rows = append(rows, []string{string(storageOK), "VOLUME SNAPSHOTS", emptyStorageName(snapshots.VolumeSnapshotClass),
			fmt.Sprintf("count=%d", snapshots.Count)})
	}
	if len(report.VolumeSnapshots) == 0 {
		rows = append(rows, []string{string(storageWarn), "VOLUME SNAPSHOTS", "-", "rollup unavailable"})
	}
	for _, component := range []struct {
		name   string
		health scaleCSIComponentHealth
	}{
		{constants.ScaleCSIController, report.ScaleCSIHealth.Controller},
		{constants.ScaleCSINode, report.ScaleCSIHealth.Node},
	} {
		detail := fmt.Sprintf("ready=%d desired=%d", component.health.Ready, component.health.Desired)
		if component.health.Detail != "" {
			detail = component.health.Detail
		}
		rows = append(rows, []string{string(component.health.Status), "SCALE-CSI HEALTH", component.name, detail})
	}
	if !report.ScaleCSIMetrics.Available {
		rows = append(rows, []string{string(storageWarn), "SCALE-CSI METRICS", emptyStorageName(report.ScaleCSIMetrics.Service), report.ScaleCSIMetrics.Detail})
	} else {
		for _, name := range []string{
			scaleCSIOrphanVolumesMetric,
			scaleCSIOrphanSnapshotsMetric,
			scaleCSISpentRestoreSnapshotsMetric,
			scaleCSITrueNASConnectionMetric,
		} {
			value, ok := report.ScaleCSIMetrics.Values[name]
			if !ok {
				rows = append(rows, []string{string(storageWarn), "SCALE-CSI METRICS", name, "missing"})
				continue
			}
			status := storageOK
			if (name == scaleCSITrueNASConnectionMetric && value != 1) || (name != scaleCSITrueNASConnectionMetric && value > 0) {
				status = storageWarn
			}
			if name == scaleCSITrueNASConnectionMetric && value != 1 {
				status = storageFail
			}
			rows = append(rows, []string{string(status), "SCALE-CSI METRICS", name, strconv.FormatFloat(value, 'g', -1, 64)})
		}
	}
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

func emptyStorageName(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<default>"
	}
	return value
}
