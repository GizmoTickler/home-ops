package volsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"

	"github.com/spf13/cobra"
)

const (
	volsyncHourlySchedule = "0 * * * *"
	migrationPollInterval = 2 * time.Second
)

var migrationSleepFn = sleepVerifyContext

type migrateOptions struct {
	Namespace       string
	App             string
	TargetClass     string
	TargetSnapClass string
	Timeout         time.Duration
	Yes             bool
}

type migrationPlan struct {
	Namespace        string
	App              string
	PVC              string
	CurrentClass     string
	TargetClass      string
	TargetSnapClass  string
	Capacity         string
	ControllerKind   string
	ControllerName   string
	OriginalReplicas int
	OriginalSchedule string
}

type migrationReplicationSource struct {
	Spec struct {
		SourcePVC string `json:"sourcePVC"`
		Trigger   struct {
			Manual   string `json:"manual"`
			Schedule string `json:"schedule"`
		} `json:"trigger"`
	} `json:"spec"`
	Status struct {
		LastManualSync    string `json:"lastManualSync"`
		LatestMoverStatus struct {
			Result string `json:"result"`
			Logs   string `json:"logs"`
		} `json:"latestMoverStatus"`
	} `json:"status"`
}

type migrationKustomization struct {
	Spec struct {
		PostBuild struct {
			Substitute map[string]string `json:"substitute"`
		} `json:"postBuild"`
	} `json:"spec"`
}

type migrationDataSourceRef struct {
	APIGroup string `json:"apiGroup"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

type migrationPVC struct {
	Metadata struct {
		Name              string `json:"name"`
		DeletionTimestamp string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		StorageClassName string                  `json:"storageClassName"`
		VolumeName       string                  `json:"volumeName"`
		DataSourceRef    *migrationDataSourceRef `json:"dataSourceRef"`
		Resources        struct {
			Requests map[string]string `json:"requests"`
		} `json:"resources"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type migrationOwnerReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type migrationPodList struct {
	Items []migrationPod `json:"items"`
}

type migrationPod struct {
	Metadata struct {
		Name            string                    `json:"name"`
		OwnerReferences []migrationOwnerReference `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Volumes []struct {
			PersistentVolumeClaim *struct {
				ClaimName string `json:"claimName"`
			} `json:"persistentVolumeClaim"`
		} `json:"volumes"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

type migrationWorkload struct {
	Metadata struct {
		OwnerReferences []migrationOwnerReference `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas int `json:"readyReplicas"`
	} `json:"status"`
}

type migrationJobList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Active         int    `json:"active"`
			Succeeded      int    `json:"succeeded"`
			CompletionTime string `json:"completionTime"`
			Conditions     []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func newMigrateCommand() *cobra.Command {
	options := migrateOptions{}
	cmd := &cobra.Command{
		Use:          "migrate <app>",
		Short:        "Migrate an application PVC to scale-csi from its latest Kopia backup",
		SilenceUsage: true,
		Long: `Migrates an application's VolSync-managed PVC to another StorageClass.
The command requires the namespace-local Flux Kustomization to already contain
the target storage and snapshot classes, takes a fresh backup, drains every PVC
consumer, and recreates the claim through the component-managed restore flow.

Defaults target scale-nvmeof with scale-snapshot. Once PVC deletion is issued,
recovery only moves forward; the command never scales a workload onto a
Terminating PVC.`,
		Example: `  homeops-cli volsync migrate paperless -n self-hosted
  homeops-cli volsync migrate seerr -n media --timeout 30m --yes`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completion.ValidApplications,
		RunE: func(cmd *cobra.Command, args []string) error {
			options.App = args[0]
			return runVolsyncMigrate(cmd.Context(), options, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "", "Kubernetes namespace (prompts when omitted)")
	cmd.Flags().StringVar(&options.TargetClass, "to-class", constants.ScaleCSIStorageClassNVMeOF, "target StorageClass")
	cmd.Flags().StringVar(&options.TargetSnapClass, "to-snapclass", constants.ScaleCSIVolumeSnapshotClass, "target VolumeSnapshotClass")
	cmd.Flags().DurationVar(&options.Timeout, "timeout", 20*time.Minute, "overall migration timeout")
	cmd.Flags().BoolVar(&options.Yes, "yes", false, "confirm the destructive cutover without prompting")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func runVolsyncMigrate(parent context.Context, options migrateOptions, out io.Writer) (returnErr error) {
	if strings.TrimSpace(options.App) == "" {
		return fmt.Errorf("app name cannot be empty")
	}
	if strings.TrimSpace(options.TargetClass) == "" || strings.TrimSpace(options.TargetSnapClass) == "" {
		return fmt.Errorf("target storage and snapshot classes cannot be empty")
	}
	if options.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}

	namespace, cancelled, err := promptForNamespace(options.Namespace)
	if err != nil || cancelled {
		return err
	}
	options.Namespace = namespace
	logger := common.NewColorLogger()

	logger.Info("PREFLIGHT: validating %s/%s", namespace, options.App)
	plan, alreadyMigrated, err := preflightMigration(options)
	if err != nil {
		return err
	}
	if alreadyMigrated {
		logger.Success("PVC %s/%s is already migrated to %s", namespace, options.App, options.TargetClass)
		return nil
	}
	logger.Success("PREFLIGHT: backup, Flux substitutions, target classes, PVC, and controller are ready")

	runCtx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()

	trigger := fmt.Sprintf("migrate-%d", volsyncNow().Unix())
	logger.Info("FRESH BACKUP: triggering %s for %s/%s", trigger, namespace, options.App)
	if err := patchReplicationSourceManualTrigger(namespace, options.App, trigger); err != nil {
		return fmt.Errorf("trigger fresh migration backup: %w", err)
	}
	scheduleNeedsRestore := true
	defer func() {
		if !scheduleNeedsRestore {
			return
		}
		if err := ensureReplicationSourceSchedule(namespace, options.App, migrationSchedule(plan)); err != nil {
			logger.Warn("Failed to restore ReplicationSource schedule: %v", err)
			if returnErr == nil {
				returnErr = fmt.Errorf("restore ReplicationSource schedule: %w", err)
			} else {
				returnErr = fmt.Errorf("%w; additionally failed to restore ReplicationSource schedule: %v", returnErr, err)
			}
		}
	}()
	if _, err := waitForMigrationBackup(runCtx, namespace, options.App, trigger, logger); err != nil {
		return err
	}
	logger.Success("FRESH BACKUP: %s completed successfully", trigger)

	if !options.Yes {
		confirmed, err := confirmActionFn(migrationConfirmation(plan), false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			return fmt.Errorf("migration cancelled")
		}
	}

	scaledDown := false
	destinationDeletionIssued := false
	pvcDeletionIssued := false
	targetPVCReady := false
	defer func() {
		if returnErr == nil || !scaledDown || pvcDeletionIssued || targetPVCReady {
			return
		}
		logger.Warn("CUTOVER failed before PVC deletion; restoring %s/%s to %d replica(s)", plan.ControllerKind, plan.ControllerName, plan.OriginalReplicas)
		if destinationDeletionIssued {
			rollbackCtx, cancelRollback := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancelRollback()
			if err := reconcileMigrationKustomization(rollbackCtx, plan.Namespace, plan.App, logger); err != nil {
				returnErr = fmt.Errorf("%w; rollback Kustomization reconcile failed: %v", returnErr, err)
			}
		}
		if err := scaleMigrationController(plan, plan.OriginalReplicas); err != nil {
			returnErr = fmt.Errorf("%w; rollback scale-up failed: %v", returnErr, err)
		}
	}()

	logger.Info("CUTOVER: deleting finished %s Jobs", options.App)
	if err := deleteFinishedMigrationJobs(namespace, options.App); err != nil {
		return err
	}

	logger.Info("CUTOVER: scaling %s/%s from %d to 0", plan.ControllerKind, plan.ControllerName, plan.OriginalReplicas)
	if err := scaleMigrationController(plan, 0); err != nil {
		return err
	}
	scaledDown = true
	if err := waitForPVCConsumersGone(runCtx, namespace, plan.PVC, logger); err != nil {
		return err
	}

	logger.Info("CUTOVER: verifying no pods reference PVC %s/%s", namespace, plan.PVC)
	if err := refusePVCDeletionWhileReferenced(namespace, plan.PVC); err != nil {
		return err
	}
	destinationDeletionIssued = true
	if err := deleteMigrationDestination(runCtx, namespace, options.App, logger); err != nil {
		return err
	}
	if err := refusePVCDeletionWhileReferenced(namespace, plan.PVC); err != nil {
		return err
	}

	logger.Info("CUTOVER: deleting PVC %s/%s; from this point recovery proceeds forward only", namespace, plan.PVC)
	pvcDeletionIssued = true
	if err := commandRunFn("kubectl", "delete", "pvc", plan.PVC, "--namespace", namespace, "--ignore-not-found", "--wait=false"); err != nil {
		logger.Warn("PVC delete returned an error after it may have reached the API server: %v", err)
	}
	if err := waitForMigrationResourceGone(runCtx, namespace, "pvc", plan.PVC, logger); err != nil {
		logger.Warn("PVC deletion has not completed yet: %v", err)
	}

	logger.Info("CUTOVER: reconciling namespace-local Flux Kustomization %s/%s", namespace, options.App)
	if err := reconcileMigrationKustomization(runCtx, namespace, options.App, logger); err != nil {
		logger.Warn("Flux reconcile request failed; continuing to watch for GitOps recovery: %v", err)
	}

	pvc, err := waitForMigratedPVC(runCtx, namespace, plan.PVC, options.TargetClass, options.App, logger)
	if err != nil {
		return migrationForwardRecoveryError(plan, err)
	}
	targetPVCReady = true

	logger.Info("CUTOVER: scaling %s/%s back to %d replica(s)", plan.ControllerKind, plan.ControllerName, plan.OriginalReplicas)
	if err := scaleMigrationController(plan, plan.OriginalReplicas); err != nil {
		return migrationForwardRecoveryError(plan, fmt.Errorf("scale controller back up: %w", err))
	}
	if err := waitForMigrationPodsReady(runCtx, plan, logger); err != nil {
		return migrationForwardRecoveryError(plan, err)
	}

	if err := ensureReplicationSourceSchedule(namespace, options.App, migrationSchedule(plan)); err != nil {
		return migrationForwardRecoveryError(plan, fmt.Errorf("restore ReplicationSource schedule: %w", err))
	}
	scheduleNeedsRestore = false

	_, err = fmt.Fprintf(out, `Migration complete: %s/%s now uses StorageClass %s (PV %s).
ReplicationSource %s/%s trigger restored to schedule %q.
Spent restore snapshot and intermediate PVC matching volsync-%s-dst-dest-* are intentionally retained; scale-csi GC removes them after the 24h age gate.
`, namespace, plan.PVC, pvc.Spec.StorageClassName, pvc.Spec.VolumeName,
		namespace, options.App, migrationSchedule(plan), options.App)
	return err
}

func preflightMigration(options migrateOptions) (migrationPlan, bool, error) {
	plan := migrationPlan{
		Namespace:       options.Namespace,
		App:             options.App,
		PVC:             options.App,
		TargetClass:     options.TargetClass,
		TargetSnapClass: options.TargetSnapClass,
	}

	var source migrationReplicationSource
	if err := getMigrationJSON("replicationsource", options.App, options.Namespace, &source); err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: ReplicationSource %s/%s does not exist: %w", options.Namespace, options.App, err)
	}
	if !strings.EqualFold(source.Status.LatestMoverStatus.Result, "Successful") {
		return plan, false, fmt.Errorf("PREFLIGHT failed: ReplicationSource %s/%s latestMoverStatus.result is %q, want Successful", options.Namespace, options.App, source.Status.LatestMoverStatus.Result)
	}
	if strings.TrimSpace(source.Spec.SourcePVC) == "" {
		return plan, false, fmt.Errorf("PREFLIGHT failed: ReplicationSource %s/%s has no spec.sourcePVC", options.Namespace, options.App)
	}
	plan.PVC = source.Spec.SourcePVC
	plan.OriginalSchedule = source.Spec.Trigger.Schedule

	var kustomization migrationKustomization
	if err := getMigrationJSON("kustomization", options.App, options.Namespace, &kustomization); err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: namespace-local Flux Kustomization %s/%s does not exist: %w", options.Namespace, options.App, err)
	}
	storageSubstitute := kustomization.Spec.PostBuild.Substitute["VOLSYNC_STORAGECLASS"]
	snapshotSubstitute := kustomization.Spec.PostBuild.Substitute["VOLSYNC_SNAPSHOTCLASS"]
	if storageSubstitute != options.TargetClass || snapshotSubstitute != options.TargetSnapClass {
		return plan, false, migrationSubstituteMismatchError(options, storageSubstitute, snapshotSubstitute)
	}

	if err := commandRunFn("kubectl", "get", "storageclass", options.TargetClass); err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: target StorageClass %q does not exist: %w", options.TargetClass, err)
	}
	if err := commandRunFn("kubectl", "get", "volumesnapshotclass", options.TargetSnapClass); err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: target VolumeSnapshotClass %q does not exist: %w", options.TargetSnapClass, err)
	}

	var pvc migrationPVC
	if err := getMigrationJSON("pvc", plan.PVC, options.Namespace, &pvc); err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: PVC %s/%s does not exist: %w", options.Namespace, plan.PVC, err)
	}
	if pvc.Status.Phase != "Bound" {
		return plan, false, fmt.Errorf("PREFLIGHT failed: PVC %s/%s phase is %q, want Bound", options.Namespace, plan.PVC, pvc.Status.Phase)
	}
	expectedDestination := options.App + "-dst"
	if pvc.Spec.DataSourceRef == nil ||
		pvc.Spec.DataSourceRef.APIGroup != "volsync.backube" ||
		pvc.Spec.DataSourceRef.Kind != "ReplicationDestination" ||
		pvc.Spec.DataSourceRef.Name != expectedDestination {
		return plan, false, fmt.Errorf(`PREFLIGHT failed: PVC %s/%s is not the component-managed VolSync claim: spec.dataSourceRef must reference ReplicationDestination %s/%s (got %s).
Refusing migration because a VolumeClaimTemplate/StatefulSet-template PVC would be recreated EMPTY by the StatefulSet controller without a VolSync restore, causing data loss`,
			options.Namespace, plan.PVC, "volsync.backube", expectedDestination, describeMigrationDataSourceRef(pvc.Spec.DataSourceRef))
	}
	plan.CurrentClass = pvc.Spec.StorageClassName
	plan.Capacity = pvc.Spec.Resources.Requests["storage"]
	if pvc.Spec.StorageClassName == options.TargetClass {
		return plan, true, nil
	}
	if strings.TrimSpace(plan.Capacity) == "" {
		return plan, false, fmt.Errorf("PREFLIGHT failed: PVC %s/%s has no requested storage capacity", options.Namespace, plan.PVC)
	}

	controllerKind, controllerName, err := identifyMigrationController(options.Namespace, plan.PVC)
	if err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: identify Deployment or StatefulSet consuming PVC %s/%s: %w", options.Namespace, plan.PVC, err)
	}
	plan.ControllerKind = controllerKind
	plan.ControllerName = controllerName

	var controller migrationWorkload
	if err := getMigrationJSON(controllerKind, controllerName, options.Namespace, &controller); err != nil {
		return plan, false, fmt.Errorf("PREFLIGHT failed: read %s/%s: %w", controllerKind, controllerName, err)
	}
	plan.OriginalReplicas = 1
	if controller.Spec.Replicas != nil {
		plan.OriginalReplicas = *controller.Spec.Replicas
	}
	return plan, false, nil
}

func describeMigrationDataSourceRef(ref *migrationDataSourceRef) string {
	if ref == nil {
		return "<none>"
	}
	return fmt.Sprintf("apiGroup=%q kind=%q name=%q", ref.APIGroup, ref.Kind, ref.Name)
}

func migrationSubstituteMismatchError(options migrateOptions, currentStorage, currentSnapshot string) error {
	return fmt.Errorf(`PREFLIGHT failed: Flux Kustomization %s/%s has stale VolSync substitutions (VOLSYNC_STORAGECLASS=%q, VOLSYNC_SNAPSHOTCLASS=%q).
Edit kubernetes/apps/%s/%s/ks.yaml to contain exactly:
spec:
  postBuild:
    substitute:
      VOLSYNC_STORAGECLASS: %s
      VOLSYNC_SNAPSHOTCLASS: %s
Then merge to git first and wait for the namespace-local Kustomization CR to update. Do not edit the live CR; Flux will overwrite it`,
		options.Namespace, options.App, currentStorage, currentSnapshot,
		options.Namespace, options.App, options.TargetClass, options.TargetSnapClass)
}

func getMigrationJSON(kind, name, namespace string, target any) error {
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	args = append(args, "-o", "json")
	output, err := commandOutputFn("kubectl", args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("parse %s %s: %w", kind, name, err)
	}
	return nil
}

func identifyMigrationController(namespace, pvc string) (string, string, error) {
	pods, err := podsReferencingPVC(namespace, pvc)
	if err != nil {
		return "", "", err
	}
	controllers := map[string]struct{}{}
	for _, pod := range pods {
		kind, name, found, err := controllerForMigrationPod(namespace, pod)
		if err != nil {
			return "", "", err
		}
		if found {
			controllers[kind+"/"+name] = struct{}{}
		}
	}
	if len(controllers) == 0 {
		return "", "", fmt.Errorf("no consuming pod is owned by a Deployment or StatefulSet")
	}
	if len(controllers) > 1 {
		names := make([]string, 0, len(controllers))
		for controller := range controllers {
			names = append(names, controller)
		}
		sort.Strings(names)
		return "", "", fmt.Errorf("PVC is consumed by multiple controllers: %s", strings.Join(names, ", "))
	}
	for controller := range controllers {
		parts := strings.SplitN(controller, "/", 2)
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("controller detection failed")
}

func controllerForMigrationPod(namespace string, pod migrationPod) (string, string, bool, error) {
	for _, owner := range pod.Metadata.OwnerReferences {
		switch strings.ToLower(owner.Kind) {
		case "deployment", "statefulset":
			return strings.ToLower(owner.Kind), owner.Name, true, nil
		case "replicaset":
			var replicaSet migrationWorkload
			if err := getMigrationJSON("replicaset", owner.Name, namespace, &replicaSet); err != nil {
				return "", "", false, fmt.Errorf("resolve ReplicaSet %s owner for pod %s: %w", owner.Name, pod.Metadata.Name, err)
			}
			for _, rsOwner := range replicaSet.Metadata.OwnerReferences {
				if strings.EqualFold(rsOwner.Kind, "Deployment") {
					return "deployment", rsOwner.Name, true, nil
				}
			}
		}
	}
	return "", "", false, nil
}

func podsReferencingPVC(namespace, pvc string) ([]migrationPod, error) {
	output, err := commandOutputFn("kubectl", "get", "pods", "--namespace", namespace, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("list pods before PVC operation: %w", err)
	}
	var list migrationPodList
	if err := json.Unmarshal(output, &list); err != nil {
		return nil, fmt.Errorf("parse pods before PVC operation: %w", err)
	}
	var consumers []migrationPod
	for _, pod := range list.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == pvc {
				consumers = append(consumers, pod)
				break
			}
		}
	}
	return consumers, nil
}

func patchReplicationSourceManualTrigger(namespace, app, trigger string) error {
	patch := fmt.Sprintf(`{"spec":{"trigger":{"manual":%q,"schedule":null}}}`, trigger)
	_, err := commandCombinedOutputFn("kubectl", "patch", "replicationsource", app, "--namespace", namespace, "--type=merge", "-p", patch)
	return err
}

func ensureReplicationSourceSchedule(namespace, app, schedule string) error {
	if strings.TrimSpace(schedule) == "" {
		schedule = volsyncHourlySchedule
	}
	var source migrationReplicationSource
	if err := getMigrationJSON("replicationsource", app, namespace, &source); err == nil &&
		source.Spec.Trigger.Schedule == schedule && source.Spec.Trigger.Manual == "" {
		return nil
	}
	patch := fmt.Sprintf(`{"spec":{"trigger":{"schedule":%q,"manual":null}}}`, schedule)
	_, err := commandCombinedOutputFn("kubectl", "patch", "replicationsource", app, "--namespace", namespace, "--type=merge", "-p", patch)
	return err
}

func migrationSchedule(plan migrationPlan) string {
	if strings.TrimSpace(plan.OriginalSchedule) == "" {
		return volsyncHourlySchedule
	}
	return plan.OriginalSchedule
}

func waitForMigrationBackup(ctx context.Context, namespace, app, trigger string, logger *common.ColorLogger) (migrationReplicationSource, error) {
	lastProgress := time.Now()
	for {
		var source migrationReplicationSource
		if err := getMigrationJSON("replicationsource", app, namespace, &source); err != nil {
			return source, fmt.Errorf("read migration backup status: %w", err)
		}
		if source.Status.LastManualSync == trigger {
			if strings.EqualFold(source.Status.LatestMoverStatus.Result, "Successful") {
				return source, nil
			}
			if strings.EqualFold(source.Status.LatestMoverStatus.Result, "Failed") {
				return source, fmt.Errorf("fresh migration backup %s failed: %s", trigger, strings.TrimSpace(source.Status.LatestMoverStatus.Logs))
			}
		}
		if time.Since(lastProgress) >= 15*time.Second {
			logger.Info("FRESH BACKUP: waiting for lastManualSync=%s (current=%q, result=%q)", trigger, source.Status.LastManualSync, source.Status.LatestMoverStatus.Result)
			lastProgress = time.Now()
		}
		if err := migrationSleepFn(ctx, migrationPollInterval); err != nil {
			return source, fmt.Errorf("fresh migration backup did not complete before timeout: %w", err)
		}
	}
}

func migrationConfirmation(plan migrationPlan) string {
	return fmt.Sprintf("Migrate %s/%s (%s) from %s to %s using %s, scale %s/%s from %d to 0, then irreversibly delete and restore the PVC?",
		plan.Namespace, plan.App, plan.Capacity, plan.CurrentClass, plan.TargetClass, plan.TargetSnapClass,
		plan.ControllerKind, plan.ControllerName, plan.OriginalReplicas)
}

func deleteFinishedMigrationJobs(namespace, app string) error {
	// This selector assumes the component's Snapshot copyMethod. Direct-mode
	// source-mover pods would not match it; the bounded consumer wait then aborts
	// the cutover cleanly instead of allowing PVC deletion.
	output, err := commandOutputFn("kubectl", "get", "jobs", "--namespace", namespace, "--selector", "app.kubernetes.io/name="+app, "-o", "json")
	if err != nil {
		return fmt.Errorf("list finished Jobs for %s/%s: %w", namespace, app, err)
	}
	var jobs migrationJobList
	if err := json.Unmarshal(output, &jobs); err != nil {
		return fmt.Errorf("parse finished Jobs for %s/%s: %w", namespace, app, err)
	}
	for _, job := range jobs.Items {
		if !migrationJobFinished(job.Status.Active, job.Status.Succeeded, job.Status.CompletionTime, job.Status.Conditions) {
			continue
		}
		if err := commandRunFn("kubectl", "delete", "job", job.Metadata.Name, "--namespace", namespace, "--ignore-not-found", "--wait=true"); err != nil {
			return fmt.Errorf("delete finished Job %s/%s: %w", namespace, job.Metadata.Name, err)
		}
	}
	return nil
}

func migrationJobFinished(active, succeeded int, completionTime string, conditions []struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}) bool {
	if active > 0 {
		return false
	}
	if succeeded > 0 || completionTime != "" {
		return true
	}
	for _, condition := range conditions {
		if strings.EqualFold(condition.Type, "Complete") && strings.EqualFold(condition.Status, "True") {
			return true
		}
	}
	return false
}

func scaleMigrationController(plan migrationPlan, replicas int) error {
	if err := commandRunFn("kubectl", "scale", plan.ControllerKind+"/"+plan.ControllerName, "--namespace", plan.Namespace, "--replicas="+strconv.Itoa(replicas)); err != nil {
		return fmt.Errorf("scale %s/%s to %d: %w", plan.ControllerKind, plan.ControllerName, replicas, err)
	}
	return nil
}

func waitForPVCConsumersGone(ctx context.Context, namespace, pvc string, logger *common.ColorLogger) error {
	lastProgress := time.Now()
	for {
		pods, err := podsReferencingPVC(namespace, pvc)
		if err != nil {
			return err
		}
		if len(pods) == 0 {
			return nil
		}
		if time.Since(lastProgress) >= 15*time.Second {
			logger.Info("CUTOVER: waiting for PVC consumers to disappear: %s", strings.Join(migrationPodNames(pods), ", "))
			lastProgress = time.Now()
		}
		if err := migrationSleepFn(ctx, migrationPollInterval); err != nil {
			return fmt.Errorf("timed out waiting for all pods referencing PVC %s/%s to disappear (%s): %w", namespace, pvc, strings.Join(migrationPodNames(pods), ", "), err)
		}
	}
}

func refusePVCDeletionWhileReferenced(namespace, pvc string) error {
	pods, err := podsReferencingPVC(namespace, pvc)
	if err != nil {
		return err
	}
	if len(pods) > 0 {
		return fmt.Errorf("refusing to delete PVC %s/%s: pod(s) still reference it: %s", namespace, pvc, strings.Join(migrationPodNames(pods), ", "))
	}
	return nil
}

func migrationPodNames(pods []migrationPod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Metadata.Name)
	}
	sort.Strings(names)
	return names
}

func deleteMigrationDestination(ctx context.Context, namespace, app string, logger *common.ColorLogger) error {
	destination := app + "-dst"
	logger.Info("CUTOVER: deleting ReplicationDestination %s/%s", namespace, destination)
	if err := commandRunFn("kubectl", "delete", "replicationdestination", destination, "--namespace", namespace, "--ignore-not-found", "--wait=false"); err != nil {
		return fmt.Errorf("delete ReplicationDestination %s/%s: %w", namespace, destination, err)
	}
	return waitForMigrationResourceGone(ctx, namespace, "replicationdestination", destination, logger)
}

func waitForMigrationResourceGone(ctx context.Context, namespace, kind, name string, logger *common.ColorLogger) error {
	lastProgress := time.Now()
	for {
		output, err := commandOutputFn("kubectl", "get", kind, name, "--namespace", namespace, "--ignore-not-found", "-o", "name")
		if err != nil {
			return fmt.Errorf("check deletion of %s %s/%s: %w", kind, namespace, name, err)
		}
		if strings.TrimSpace(string(output)) == "" {
			return nil
		}
		if time.Since(lastProgress) >= 15*time.Second {
			logger.Info("CUTOVER: waiting for %s %s/%s to be deleted", kind, namespace, name)
			lastProgress = time.Now()
		}
		if err := migrationSleepFn(ctx, migrationPollInterval); err != nil {
			return fmt.Errorf("timed out waiting for %s %s/%s deletion: %w", kind, namespace, name, err)
		}
	}
}

func reconcileMigrationKustomization(ctx context.Context, namespace, app string, logger *common.ColorLogger) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := fluxRunFn("reconcile", "kustomization", app, "--namespace", namespace, "--with-source", "--timeout="+remainingMigrationTimeout(ctx)); err == nil {
			return nil
		} else {
			lastErr = err
			logger.Warn("Flux reconcile attempt %d/3 failed: %v", attempt, err)
		}
		if attempt < 3 {
			if err := migrationSleepFn(ctx, migrationPollInterval); err != nil {
				break
			}
		}
	}
	annotation := "reconcile.fluxcd.io/requestedAt=" + time.Now().UTC().Format(time.RFC3339Nano)
	if err := commandRunFn("kubectl", "annotate", "kustomization", app, "--namespace", namespace, annotation, "--overwrite"); err != nil {
		return fmt.Errorf("flux reconcile failed (%v) and annotate fallback failed: %w", lastErr, err)
	}
	return nil
}

func remainingMigrationTimeout(ctx context.Context) string {
	deadline, ok := ctx.Deadline()
	if !ok {
		return (20 * time.Minute).String()
	}
	remaining := time.Until(deadline)
	if remaining < time.Second {
		remaining = time.Second
	}
	return remaining.Round(time.Second).String()
}

func waitForMigratedPVC(ctx context.Context, namespace, pvc, targetClass, app string, logger *common.ColorLogger) (migrationPVC, error) {
	lastProgress := time.Time{}
	for {
		var current migrationPVC
		err := getMigrationJSON("pvc", pvc, namespace, &current)
		if err == nil && current.Status.Phase == "Bound" && current.Spec.StorageClassName == targetClass {
			return current, nil
		}
		if err == nil && current.Metadata.DeletionTimestamp == "" && current.Spec.StorageClassName != "" && current.Spec.StorageClassName != targetClass {
			return current, fmt.Errorf("recreated PVC %s/%s uses StorageClass %q, want %q; the namespace-local Kustomization may still be stale", namespace, pvc, current.Spec.StorageClassName, targetClass)
		}
		if lastProgress.IsZero() || time.Since(lastProgress) >= 15*time.Second {
			state := "not created"
			if err == nil {
				state = fmt.Sprintf("phase=%s class=%s", current.Status.Phase, current.Spec.StorageClassName)
			}
			logger.Info("CUTOVER: waiting for restored PVC %s/%s (%s); %s", namespace, pvc, targetClass, state)
			logMigrationDestinationJobs(namespace, app, logger)
			lastProgress = time.Now()
		}
		if err := migrationSleepFn(ctx, migrationPollInterval); err != nil {
			return current, fmt.Errorf("timed out waiting for PVC %s/%s to become Bound on %s: %w", namespace, pvc, targetClass, err)
		}
	}
}

func logMigrationDestinationJobs(namespace, app string, logger *common.ColorLogger) {
	output, err := commandOutputFn("kubectl", "get", "jobs", "--namespace", namespace, "-o", "json")
	if err != nil {
		logger.Warn("Could not inspect VolSync destination Jobs: %v", err)
		return
	}
	var jobs migrationJobList
	if err := json.Unmarshal(output, &jobs); err != nil {
		logger.Warn("Could not parse VolSync destination Jobs: %v", err)
		return
	}
	prefix := "volsync-dst-" + app + "-dst"
	for _, job := range jobs.Items {
		if strings.HasPrefix(job.Metadata.Name, prefix) {
			logger.Info("CUTOVER: restore Job %s active=%d succeeded=%d", job.Metadata.Name, job.Status.Active, job.Status.Succeeded)
		}
	}
}

func waitForMigrationPodsReady(ctx context.Context, plan migrationPlan, logger *common.ColorLogger) error {
	if plan.OriginalReplicas == 0 {
		return nil
	}
	lastProgress := time.Time{}
	for {
		var controller migrationWorkload
		if err := getMigrationJSON(plan.ControllerKind, plan.ControllerName, plan.Namespace, &controller); err != nil {
			return fmt.Errorf("read %s/%s readiness: %w", plan.ControllerKind, plan.ControllerName, err)
		}
		if controller.Status.ReadyReplicas >= plan.OriginalReplicas {
			return nil
		}
		if lastProgress.IsZero() || time.Since(lastProgress) >= 15*time.Second {
			logger.Info("POSTFLIGHT: waiting for %s/%s pods to be Ready (%d/%d)", plan.ControllerKind, plan.ControllerName, controller.Status.ReadyReplicas, plan.OriginalReplicas)
			lastProgress = time.Now()
		}
		if err := migrationSleepFn(ctx, migrationPollInterval); err != nil {
			return fmt.Errorf("timed out waiting for pods using PVC %s/%s to become Ready: %w", plan.Namespace, plan.PVC, err)
		}
	}
}

func migrationForwardRecoveryError(plan migrationPlan, cause error) error {
	return fmt.Errorf(`migration cutover for %s/%s could not finish after PVC deletion was issued: %w
Do not scale the workload onto a Terminating PVC. Continue forward with:
  kubectl -n %s get pvc %s -w
  kubectl -n %s get replicationdestination %s-dst
  kubectl -n %s get jobs,pods | grep volsync-dst-%s-dst
  flux reconcile kustomization %s -n %s --with-source
  kubectl -n %s patch replicationsource %s --type=merge -p '{"spec":{"trigger":{"schedule":%q,"manual":null}}}'
After PVC %s is Bound on %s:
  kubectl -n %s scale %s/%s --replicas=%d`,
		plan.Namespace, plan.App, cause,
		plan.Namespace, plan.PVC,
		plan.Namespace, plan.App,
		plan.Namespace, plan.App,
		plan.App, plan.Namespace,
		plan.Namespace, plan.App,
		migrationSchedule(plan),
		plan.PVC, plan.TargetClass,
		plan.Namespace, plan.ControllerKind, plan.ControllerName, plan.OriginalReplicas)
}
