package volsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ui"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	verifyLabelKey     = "homeops.io/volsync-verify"
	verifyRunLabelKey  = "homeops.io/volsync-verify-run"
	verifyPollInterval = 2 * time.Second
	verifyCleanupLimit = 2 * time.Minute
)

var (
	verifyOutputFn = func(ctx context.Context, args ...string) ([]byte, error) {
		return commandOutputCtxFn(ctx, "kubectl", args...)
	}
	verifyRunFn = func(ctx context.Context, args ...string) error {
		result, err := runVolsyncCommandCtx(ctx, "kubectl", args...)
		return redactCommandError(err, result.Stdout, result.Stderr, ctx.Err())
	}
	verifyApplyYAMLFn          = applyVerifyYAML
	verifyBuildRestoreConfigFn = buildVerifyRestoreConfig
	verifySleepFn              = sleepVerifyContext
)

type verifyOptions struct {
	Namespace string
	App       string
	Timeout   time.Duration
	Check     bool
	Force     bool
	Output    string
}

type verifyReport struct {
	App              string `json:"app"`
	Namespace        string `json:"namespace"`
	SnapshotRestored string `json:"snapshot_restored"`
	ScratchPVC       string `json:"scratch_pvc"`
	Bytes            string `json:"bytes"`
	Duration         string `json:"duration"`
	IntegrityChecked bool   `json:"integrity_checked"`
	CheckOutput      string `json:"check_output,omitempty"`
}

type verifyPVC struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		AccessModes      []string `json:"accessModes"`
		StorageClassName string   `json:"storageClassName"`
		VolumeMode       string   `json:"volumeMode"`
		Resources        struct {
			Requests map[string]string `json:"requests"`
		} `json:"resources"`
	} `json:"spec"`
}

type verifyRestoreConfig struct {
	PVC   verifyPVC
	Kopia map[string]any
}

type verifyDestinationStatus struct {
	Status struct {
		LastManualSync    string `json:"lastManualSync"`
		LastSyncDuration  string `json:"lastSyncDuration"`
		LatestMoverStatus struct {
			Result string `json:"result"`
			Logs   string `json:"logs"`
		} `json:"latestMoverStatus"`
	} `json:"status"`
}

type verifyMoverResourceList struct {
	Items []verifyMoverResource `json:"items"`
}

type verifyMoverResource struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name            string            `json:"name"`
		Labels          map[string]string `json:"labels"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		BackoffLimit *int64 `json:"backoffLimit"`
	} `json:"spec"`
	Status struct {
		Failed     int64 `json:"failed"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			RestartCount int64  `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"waiting"`
				Terminated *struct {
					ExitCode int64  `json:"exitCode"`
					Reason   string `json:"reason"`
					Message  string `json:"message"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type verifyMoverFailure struct {
	kind    string
	name    string
	reason  string
	details string
}

type verifyOperation struct {
	namespace string
	app       string
	name      string
	logger    *common.ColorLogger
}

func newVerifyCommand() *cobra.Command {
	options := verifyOptions{}
	cmd := &cobra.Command{
		Use:          "verify --app <name>",
		Short:        "Restore the latest backup into a throwaway PVC",
		SilenceUsage: true,
		Long: `Proves that the latest Kopia backup is restorable by creating an
ownerless scratch PVC and a manually triggered ReplicationDestination. The
application PVC and ReplicationSource are never modified. All scratch resources
are deleted after success, failure, timeout, or interruption.

Use --check to mount the restored PVC read-only in a short-lived Alpine pod and
verify that it contains at least one regular file. --force only permits a new
run when scratch objects for the same app already exist; confirmation is still
required unless the global --yes flag is set.`,
		Example: `  homeops-cli volsync verify --app paperless -n self-hosted --yes
  homeops-cli volsync verify --app paperless -n self-hosted --check --timeout 20m
  homeops-cli volsync verify --app paperless -n self-hosted --output json --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.Output != "table" && options.Output != "json" {
				return fmt.Errorf("unsupported output format %q (table, json)", options.Output)
			}
			return runVolsyncVerify(cmd.Context(), options, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "", "Kubernetes namespace (prompts when omitted)")
	cmd.Flags().StringVar(&options.App, "app", "", "application ReplicationSource name (required)")
	cmd.Flags().DurationVar(&options.Timeout, "timeout", 15*time.Minute, "restore verification timeout")
	cmd.Flags().BoolVar(&options.Check, "check", false, "mount the scratch PVC read-only and verify it is non-empty")
	cmd.Flags().BoolVar(&options.Force, "force", false, "allow a run while same-app verify scratch objects exist")
	cmd.Flags().StringVarP(&options.Output, "output", "o", "table", "output format: table or json")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)
	return cmd
}

func runVolsyncVerify(ctx context.Context, options verifyOptions, out io.Writer) error {
	if options.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	namespace, cancelled, err := promptForNamespace(options.Namespace)
	if err != nil || cancelled {
		return err
	}
	options.Namespace = namespace
	report, err := executeVolsyncVerify(ctx, options, true)
	if err != nil {
		return err
	}
	return writeVerifyReport(out, options.Output, report)
}

// executeVolsyncVerify is the shared single-app implementation used by both
// verify and verify-all. Callers decide whether this invocation needs its own
// confirmation; cleanup remains local to every invocation.
func executeVolsyncVerify(ctx context.Context, options verifyOptions, confirm bool) (verifyReport, error) {
	existing, err := findExistingVerifications(ctx, options.Namespace, options.App)
	if err != nil {
		return verifyReport{}, err
	}
	if len(existing) > 0 && !options.Force {
		return verifyReport{}, fmt.Errorf("verification already in progress for %s/%s: %s (use --force to override)", options.Namespace, options.App, strings.Join(existing, ", "))
	}

	name := verifyObjectName(options.App, volsyncNow())
	trigger := fmt.Sprintf("verify-%d", volsyncNow().UnixNano())
	config, err := verifyBuildRestoreConfigFn(ctx, options.Namespace, options.App)
	if err != nil {
		return verifyReport{}, err
	}
	if confirm {
		if confirmed, err := confirmActionFn(verifyConfirmationMessage(options.Namespace, name, options.Check), false); err != nil {
			return verifyReport{}, fmt.Errorf("confirmation failed: %w", err)
		} else if !confirmed {
			return verifyReport{}, fmt.Errorf("verification cancelled")
		}
	}

	op := &verifyOperation{namespace: options.Namespace, app: options.App, name: name, logger: common.NewColorLogger()}
	defer op.cleanup()

	pvcYAML, err := buildVerifyPVC(name, options.Namespace, options.App, config.PVC)
	if err != nil {
		return verifyReport{}, err
	}
	if _, err := verifyApplyYAMLFn(ctx, pvcYAML); err != nil {
		return verifyReport{}, fmt.Errorf("create scratch PVC %s: %w", name, err)
	}

	destinationYAML, err := buildVerifyDestination(name, options.Namespace, options.App, trigger, config)
	if err != nil {
		return verifyReport{}, err
	}
	if _, err := verifyApplyYAMLFn(ctx, destinationYAML); err != nil {
		return verifyReport{}, fmt.Errorf("create verification ReplicationDestination %s: %w", name, err)
	}

	started := volsyncNow()
	status, err := waitForVerifyDestination(ctx, options.Namespace, name, trigger, options.Timeout)
	if err != nil {
		return verifyReport{}, err
	}
	duration := strings.TrimSpace(status.Status.LastSyncDuration)
	if duration == "" {
		duration = volsyncNow().Sub(started).Round(time.Second).String()
	}

	checkOutput := ""
	if options.Check {
		checkOutput, err = op.runIntegrityCheck(ctx, options.Timeout)
		if err != nil {
			return verifyReport{}, err
		}
	}

	report := verifyReport{
		App:              options.App,
		Namespace:        options.Namespace,
		SnapshotRestored: "latest",
		ScratchPVC:       name,
		Bytes:            restoredBytes(status.Status.LatestMoverStatus.Logs),
		Duration:         duration,
		IntegrityChecked: options.Check,
		CheckOutput:      strings.TrimSpace(checkOutput),
	}
	return report, nil
}

func buildVerifyRestoreConfig(ctx context.Context, namespace, app string) (verifyRestoreConfig, error) {
	output, err := verifyOutputFn(ctx, "get", "replicationsource", app, "--namespace", namespace, "-o", "json")
	if err != nil {
		return verifyRestoreConfig{}, fmt.Errorf("ReplicationSource %s/%s not found; nothing to verify against: %w", namespace, app, err)
	}
	var source unstructured.Unstructured
	if err := json.Unmarshal(output, &source.Object); err != nil {
		return verifyRestoreConfig{}, fmt.Errorf("parse ReplicationSource %s/%s: %w", namespace, app, err)
	}
	claim, found, err := unstructured.NestedString(source.Object, "spec", "sourcePVC")
	if err != nil {
		return verifyRestoreConfig{}, fmt.Errorf("read ReplicationSource %s/%s sourcePVC: %w", namespace, app, err)
	}
	if !found || strings.TrimSpace(claim) == "" {
		return verifyRestoreConfig{}, fmt.Errorf("ReplicationSource %s/%s has no sourcePVC; nothing to verify against", namespace, app)
	}
	kopia, found, err := unstructured.NestedMap(source.Object, "spec", "kopia")
	if err != nil {
		return verifyRestoreConfig{}, fmt.Errorf("read ReplicationSource %s/%s Kopia configuration: %w", namespace, app, err)
	}
	if !found || len(kopia) == 0 {
		return verifyRestoreConfig{}, fmt.Errorf("ReplicationSource %s/%s has no Kopia configuration; nothing to verify against", namespace, app)
	}

	var pvc verifyPVC
	output, err = verifyOutputFn(ctx, "get", "pvc", claim, "--namespace", namespace, "-o", "json")
	if err != nil {
		return verifyRestoreConfig{}, fmt.Errorf("get source PVC %s/%s: %w", namespace, claim, err)
	}
	if err := json.Unmarshal(output, &pvc); err != nil {
		return verifyRestoreConfig{}, fmt.Errorf("parse source PVC %s/%s: %w", namespace, claim, err)
	}
	if pvc.Spec.Resources.Requests["storage"] == "" {
		return verifyRestoreConfig{}, fmt.Errorf("source PVC %s/%s capacity is empty", namespace, claim)
	}
	return verifyRestoreConfig{PVC: pvc, Kopia: kopia}, nil
}

func buildVerifyPVC(name, namespace, app string, source verifyPVC) (string, error) {
	storage := source.Spec.Resources.Requests["storage"]
	if storage == "" {
		return "", fmt.Errorf("source PVC capacity is empty")
	}
	accessModes := source.Spec.AccessModes
	if len(accessModes) == 0 {
		accessModes = []string{"ReadWriteOnce"}
	}
	spec := map[string]any{
		"accessModes": accessModes,
		"resources":   map[string]any{"requests": map[string]string{"storage": storage}},
	}
	if source.Spec.StorageClassName != "" {
		spec["storageClassName"] = source.Spec.StorageClassName
	}
	if source.Spec.VolumeMode != "" {
		spec["volumeMode"] = source.Spec.VolumeMode
	}
	return marshalVerifyObject("v1", "PersistentVolumeClaim", name, namespace, app, spec)
}

func buildVerifyDestination(name, namespace, app, trigger string, config verifyRestoreConfig) (string, error) {
	storage := config.PVC.Spec.Resources.Requests["storage"]
	if storage == "" {
		return "", fmt.Errorf("source PVC capacity is empty")
	}

	// NestedMap returns a deep copy, so the live ReplicationSource remains the
	// source of truth for mover volumes, repository credentials, cache settings,
	// security context, identities, and future Kopia fields. Verify only changes
	// the fields needed to target and clean up its ownerless scratch PVC.
	kopia, ok := runtime.DeepCopyJSONValue(config.Kopia).(map[string]any)
	if !ok {
		return "", fmt.Errorf("copy ReplicationSource Kopia configuration")
	}
	// Backup-side knobs exist only in the ReplicationSource schema; the API
	// server rejects them on a ReplicationDestination (verified live with
	// parallelism).
	for _, sourceOnly := range []string{"parallelism", "compression", "retain", "actions", "sourcePathOverride"} {
		delete(kopia, sourceOnly)
	}
	kopia["capacity"] = storage
	kopia["cleanupCachePVC"] = true
	kopia["cleanupTempPVC"] = true
	kopia["copyMethod"] = "Direct"
	kopia["destinationPVC"] = name
	kopia["enableFileDeletion"] = true
	kopia["previous"] = 0
	if config.PVC.Spec.StorageClassName == "" {
		delete(kopia, "storageClassName")
	} else {
		kopia["storageClassName"] = config.PVC.Spec.StorageClassName
	}
	sourceIdentity, _ := kopia["sourceIdentity"].(map[string]any)
	if sourceIdentity == nil {
		sourceIdentity = map[string]any{}
	}
	sourceIdentity["sourceName"] = app
	kopia["sourceIdentity"] = sourceIdentity

	spec := map[string]any{
		"trigger": map[string]string{"manual": trigger},
		"kopia":   kopia,
	}
	return marshalVerifyObject("volsync.backube/v1alpha1", "ReplicationDestination", name, namespace, app, spec)
}

func marshalVerifyObject(apiVersion, kind, name, namespace, app string, spec map[string]any) (string, error) {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]string{
				verifyLabelKey:    app,
				verifyRunLabelKey: name,
			},
		},
		"spec": spec,
	}
	output, err := yaml.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("marshal %s: %w", kind, err)
	}
	return string(output), nil
}

func applyVerifyYAML(ctx context.Context, manifest string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "--filename", "-")
	cmd.Stdin = bytes.NewBufferString(manifest)
	output, err := cmd.CombinedOutput()
	redacted := common.RedactCommandOutput(string(output))
	return []byte(redacted), redactCommandError(err, redacted, "", ctx.Err())
}

func findExistingVerifications(ctx context.Context, namespace, app string) ([]string, error) {
	output, err := verifyOutputFn(ctx, "get", "replicationdestination,pvc,pod", "--namespace", namespace, "-o", "name")
	if err != nil {
		return nil, fmt.Errorf("check existing verification resources: %w", err)
	}
	prefix := verifyObjectPrefix(app)
	var existing []string
	for _, resource := range strings.Fields(string(output)) {
		name := resource
		if slash := strings.LastIndex(resource, "/"); slash >= 0 {
			name = resource[slash+1:]
		}
		if strings.HasPrefix(name, prefix) {
			existing = append(existing, resource)
		}
	}
	return existing, nil
}

func verifyObjectPrefix(app string) string {
	const maxAppLength = 28
	app = strings.Trim(strings.ToLower(app), "-")
	if len(app) > maxAppLength {
		app = strings.TrimRight(app[:maxAppLength], "-")
	}
	return "volsync-verify-" + app + "-"
}

func verifyObjectName(app string, now time.Time) string {
	return verifyObjectPrefix(app) + strconv.FormatInt(now.UnixNano(), 10)
}

func verifyConfirmationMessage(namespace, name string, check bool) string {
	resources := "a ReplicationDestination and scratch PVC"
	if check {
		resources += " plus a read-only check pod"
	}
	return fmt.Sprintf("Create %s named %s in namespace %s, restore the latest snapshot, then clean everything up?", resources, name, namespace)
}

func waitForVerifyDestination(ctx context.Context, namespace, name, trigger string, timeout time.Duration) (verifyDestinationStatus, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		output, err := verifyOutputFn(waitCtx, "get", "replicationdestination", name, "--namespace", namespace, "-o", "json")
		if err != nil {
			if waitCtx.Err() != nil {
				return verifyDestinationStatus{}, fmt.Errorf("verification timed out after %s: %w", timeout, waitCtx.Err())
			}
			return verifyDestinationStatus{}, fmt.Errorf("read ReplicationDestination status: %w", err)
		}
		var status verifyDestinationStatus
		if err := json.Unmarshal(output, &status); err != nil {
			return status, fmt.Errorf("parse ReplicationDestination status: %w", err)
		}
		result := status.Status.LatestMoverStatus.Result
		if strings.EqualFold(result, "Failed") {
			return status, fmt.Errorf("verification restore failed: %s", strings.TrimSpace(status.Status.LatestMoverStatus.Logs))
		}
		if status.Status.LastManualSync == trigger && strings.EqualFold(result, "Successful") {
			return status, nil
		}
		failure, err := inspectVerifyMoverFailure(waitCtx, namespace, name)
		if err != nil {
			if waitCtx.Err() != nil {
				return status, fmt.Errorf("verification timed out after %s: %w", timeout, waitCtx.Err())
			}
			return status, fmt.Errorf("inspect verification mover: %w", err)
		}
		if failure != nil {
			logs := verifyMoverLogs(waitCtx, namespace, *failure)
			context := strings.TrimSpace(strings.Join([]string{failure.reason, failure.details, logs}, ": "))
			return status, fmt.Errorf("verification mover %s/%s failed: %s", strings.ToLower(failure.kind), failure.name, context)
		}
		if err := verifySleepFn(waitCtx, verifyPollInterval); err != nil {
			return status, fmt.Errorf("verification timed out after %s: %w", timeout, err)
		}
	}
}

func inspectVerifyMoverFailure(ctx context.Context, namespace, destination string) (*verifyMoverFailure, error) {
	output, err := verifyOutputFn(ctx, "get", "jobs,pods", "--namespace", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var resources verifyMoverResourceList
	if err := json.Unmarshal(output, &resources); err != nil {
		return nil, fmt.Errorf("parse mover resources: %w", err)
	}

	jobNames := map[string]struct{}{}
	for i := range resources.Items {
		item := &resources.Items[i]
		if !strings.EqualFold(item.Kind, "Job") || !isVerifyMoverJob(item, destination) {
			continue
		}
		jobNames[item.Metadata.Name] = struct{}{}
		for _, condition := range item.Status.Conditions {
			if strings.EqualFold(condition.Type, "Failed") && strings.EqualFold(condition.Status, "True") {
				return &verifyMoverFailure{kind: item.Kind, name: item.Metadata.Name, reason: condition.Reason, details: condition.Message}, nil
			}
		}
		if item.Spec.BackoffLimit != nil && item.Status.Failed >= *item.Spec.BackoffLimit && item.Status.Failed > 0 {
			return &verifyMoverFailure{
				kind:    item.Kind,
				name:    item.Metadata.Name,
				reason:  "BackoffLimitReached",
				details: fmt.Sprintf("%d failed pod attempts (backoff limit %d)", item.Status.Failed, *item.Spec.BackoffLimit),
			}, nil
		}
	}

	for i := range resources.Items {
		item := &resources.Items[i]
		if !strings.EqualFold(item.Kind, "Pod") || !isVerifyMoverPod(item, destination, jobNames) {
			continue
		}
		for _, container := range item.Status.ContainerStatuses {
			if container.State.Waiting != nil && isTerminalMoverWaitReason(container.State.Waiting.Reason) {
				return &verifyMoverFailure{kind: item.Kind, name: item.Metadata.Name, reason: container.State.Waiting.Reason, details: container.State.Waiting.Message}, nil
			}
			if container.RestartCount > 1 && container.State.Terminated != nil && container.State.Terminated.ExitCode != 0 {
				return &verifyMoverFailure{
					kind: item.Kind,
					name: item.Metadata.Name,
					reason: fmt.Sprintf("container %s repeatedly exited with code %d (%d restarts)",
						container.Name, container.State.Terminated.ExitCode, container.RestartCount),
					details: strings.TrimSpace(container.State.Terminated.Reason + ": " + container.State.Terminated.Message),
				}, nil
			}
		}
	}
	return nil, nil
}

func isVerifyMoverJob(item *verifyMoverResource, destination string) bool {
	for _, owner := range item.Metadata.OwnerReferences {
		if strings.EqualFold(owner.Kind, "ReplicationDestination") && owner.Name == destination {
			return true
		}
	}
	for _, value := range item.Metadata.Labels {
		if value == destination {
			return true
		}
	}
	return strings.HasPrefix(item.Metadata.Name, "volsync-dst-"+destination)
}

func isVerifyMoverPod(item *verifyMoverResource, destination string, jobNames map[string]struct{}) bool {
	for _, owner := range item.Metadata.OwnerReferences {
		if strings.EqualFold(owner.Kind, "Job") {
			if _, ok := jobNames[owner.Name]; ok {
				return true
			}
		}
	}
	for _, label := range []string{item.Metadata.Labels["batch.kubernetes.io/job-name"], item.Metadata.Labels["job-name"]} {
		if _, ok := jobNames[label]; ok {
			return true
		}
	}
	return strings.HasPrefix(item.Metadata.Name, "volsync-dst-"+destination)
}

func isTerminalMoverWaitReason(reason string) bool {
	switch strings.ToLower(reason) {
	case "crashloopbackoff", "createcontainerconfigerror", "errimagepull", "imagepullbackoff", "runcontainererror":
		return true
	default:
		return false
	}
}

func verifyMoverLogs(ctx context.Context, namespace string, failure verifyMoverFailure) string {
	output, err := verifyOutputFn(ctx, "logs", strings.ToLower(failure.kind)+"/"+failure.name, "--namespace", namespace, "--all-containers=true", "--tail=50")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func sleepVerifyContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (op *verifyOperation) runIntegrityCheck(ctx context.Context, timeout time.Duration) (string, error) {
	podYAML, err := buildVerifyCheckPod(op.name, op.namespace, op.app, config.Get().Volsync.CheckImage)
	if err != nil {
		return "", err
	}
	if _, err := verifyApplyYAMLFn(ctx, podYAML); err != nil {
		return "", fmt.Errorf("create integrity check pod: %w", err)
	}
	if err := verifyRunFn(ctx, "wait", "pod/"+op.name, "--namespace", op.namespace, "--for=condition=Ready", "--timeout="+timeout.String()); err != nil {
		return "", fmt.Errorf("wait for integrity check pod: %w", err)
	}
	// lost+found is root-owned 0700 on ext4 volumes; the non-root check pod
	// cannot descend into it and that must not fail the verification.
	checkScript := `set -eu; files="$(find /data -mindepth 1 -name lost+found -prune -o -type f -print | head -n 10)"; if [ -z "$files" ]; then echo "no regular files found" >&2; exit 1; fi; printf '%s\n' "$files"; du -sh -x /data 2>/dev/null || true`
	output, err := verifyOutputFn(ctx, "exec", op.name, "--namespace", op.namespace, "--", "sh", "-c", checkScript)
	if err != nil {
		return "", fmt.Errorf("integrity check failed: %w", err)
	}
	return string(output), nil
}

func buildVerifyCheckPod(name, namespace, app, checkImage string) (string, error) {
	spec := map[string]any{
		"restartPolicy": "Never",
		"containers": []map[string]any{{
			"name":    "check",
			"image":   checkImage,
			"command": []string{"sh", "-c", "sleep 3600"},
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false,
				"capabilities":             map[string]any{"drop": []string{"ALL"}},
				"readOnlyRootFilesystem":   true,
				"runAsNonRoot":             true,
				"runAsUser":                1000,
			},
			"volumeMounts": []map[string]any{{"name": "data", "mountPath": "/data", "readOnly": true}},
		}},
		"volumes": []map[string]any{{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": name, "readOnly": true}}},
	}
	return marshalVerifyObject("v1", "Pod", name, namespace, app, spec)
}

func (op *verifyOperation) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), verifyCleanupLimit)
	defer cancel()
	for _, resource := range []string{"pod", "replicationdestination", "pvc"} {
		if err := verifyRunFn(ctx, "delete", resource, op.name, "--namespace", op.namespace, "--ignore-not-found", "--wait=true"); err != nil {
			op.logger.Warn("VERIFY CLEANUP FAILED: could not delete %s/%s in %s: %v", resource, op.name, op.namespace, err)
		}
	}
}

var restoredBytesPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*([kmgtpe]?i?b|bytes?)`)

func restoredBytes(logs string) string {
	lower := strings.ToLower(logs)
	if !strings.Contains(lower, "restored") && !strings.Contains(lower, "processed") && !strings.Contains(lower, "total") {
		return "unknown"
	}
	matches := restoredBytesPattern.FindAllStringSubmatch(logs, -1)
	if len(matches) == 0 {
		return "unknown"
	}
	match := matches[len(matches)-1]
	return strings.TrimSpace(match[1] + " " + match[2])
}

func writeVerifyReport(out io.Writer, output string, report verifyReport) error {
	if output == "json" {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	rows := [][]string{{report.Namespace, report.App, report.SnapshotRestored, report.Bytes, report.Duration, strconv.FormatBool(report.IntegrityChecked)}}
	if _, err := fmt.Fprintln(out, ui.Table([]string{"NAMESPACE", "APP", "SNAPSHOT", "BYTES", "DURATION", "CHECKED"}, rows)); err != nil {
		return err
	}
	if report.CheckOutput != "" {
		_, err := fmt.Fprintf(out, "\nIntegrity check:\n%s\n", report.CheckOutput)
		return err
	}
	return nil
}
