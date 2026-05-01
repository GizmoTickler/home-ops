package volsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/ui"

	"github.com/spf13/cobra"
)

var (
	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return volsyncCommandOutput("kubectl", args...)
	}
	kubectlRunFn = func(args ...string) error {
		return volsyncCommandRun("kubectl", args...)
	}
	kubectlCombinedOutputFn = func(args ...string) ([]byte, error) {
		return volsyncCommandCombinedOutput("kubectl", args...)
	}
	fluxCombinedOutputFn = func(args ...string) ([]byte, error) {
		return volsyncCommandCombinedOutput("flux", args...)
	}
	fluxRunFn = func(args ...string) error {
		return volsyncCommandRun("flux", args...)
	}
	commandOutputFn = func(name string, args ...string) ([]byte, error) {
		return volsyncCommandOutput(name, args...)
	}
	commandRunFn = func(name string, args ...string) error {
		return volsyncCommandRun(name, args...)
	}
	commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
		return volsyncCommandCombinedOutput(name, args...)
	}
	kubectlApplyYAMLFn = func(yaml string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), volsyncDefaultCommandTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "--filename", "-")
		cmd.Stdin = bytes.NewReader([]byte(yaml))
		output, err := cmd.CombinedOutput()
		redactedOutput := common.RedactCommandOutput(string(output))
		return []byte(redactedOutput), redactCommandError(err, redactedOutput, "", ctx.Err())
	}
	selectNamespaceFn       = ui.SelectNamespace
	chooseOptionFn          = ui.Choose
	filterOptionFn          = ui.Filter
	confirmActionFn         = ui.Confirm
	renderVolsyncTemplateFn = templates.RenderVolsyncTemplate
	volsyncSleep            = time.Sleep
	volsyncNow              = time.Now
	snapshotAppFn           = snapshotApp
	restoreAppFn            = restoreApp
)

const (
	volsyncDefaultCommandTimeout = 2 * time.Minute
	volsyncWaitTimeoutBuffer     = 30 * time.Second
)

var kubectlTimeoutArgPattern = regexp.MustCompile(`^--timeout=(.+)$`)

func volsyncCommandOutput(name string, args ...string) ([]byte, error) {
	result, err := runVolsyncCommand(name, args...)
	return []byte(result.Stdout), redactCommandError(err, result.Stdout, result.Stderr, nil)
}

func volsyncCommandCombinedOutput(name string, args ...string) ([]byte, error) {
	result, err := runVolsyncCommand(name, args...)
	return []byte(result.Stdout + result.Stderr), redactCommandError(err, result.Stdout, result.Stderr, nil)
}

func volsyncCommandRun(name string, args ...string) error {
	result, err := runVolsyncCommand(name, args...)
	return redactCommandError(err, result.Stdout, result.Stderr, nil)
}

func runVolsyncCommand(name string, args ...string) (common.CommandResult, error) {
	return common.RunCommand(context.Background(), common.CommandOptions{
		Name:    name,
		Args:    args,
		Timeout: volsyncCommandTimeout(args...),
	})
}

func redactCommandError(err error, stdout, stderr string, ctxErr error) error {
	if ctxErr != nil {
		err = ctxErr
	}
	if err == nil {
		return nil
	}

	output := strings.TrimSpace(strings.Join(nonEmptyStrings(stdout, stderr), "\n"))
	if output == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, output)
}

func nonEmptyStrings(values ...string) []string {
	var nonEmpty []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	return nonEmpty
}

func volsyncCommandTimeout(args ...string) time.Duration {
	timeout := volsyncDefaultCommandTimeout
	for _, arg := range args {
		matches := kubectlTimeoutArgPattern.FindStringSubmatch(arg)
		if len(matches) != 2 {
			continue
		}
		parsed, err := time.ParseDuration(matches[1])
		if err != nil {
			continue
		}
		if parsed+volsyncWaitTimeoutBuffer > timeout {
			timeout = parsed + volsyncWaitTimeoutBuffer
		}
	}
	return timeout
}

// VolsyncConfig holds configuration for volsync operations
type VolsyncConfig struct {
	NFSServer string
	NFSPath   string
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volsync",
		Short: "Manage VolSync backup and restore operations",
		Long:  `Commands for managing VolSync snapshots, restores, and Kopia repository operations`,
	}

	cmd.AddCommand(
		newStateCommand(),
		newSuspendCommand(),
		newResumeCommand(),
		newSnapshotCommand(),
		newSnapshotAllCommand(),
		newSnapshotsCommand(),
		newRestoreCommand(),
		newRestoreAllCommand(),
	)

	return cmd
}

func newStateCommand() *cobra.Command {
	var state string

	cmd := &cobra.Command{
		Use:   "state",
		Short: "Suspend or resume VolSync",
		Long:  `Suspend or resume VolSync kustomization and HelmRelease`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if state != "suspend" && state != "resume" {
				return fmt.Errorf("state must be 'suspend' or 'resume'")
			}
			return changeVolsyncState(state)
		},
	}

	cmd.Flags().StringVar(&state, "action", "", "Action to perform: suspend or resume (required)")
	_ = cmd.MarkFlagRequired("action")

	return cmd
}

func changeVolsyncState(state string) error {
	logger := common.NewColorLogger()

	logger.Info("Setting VolSync state to: %s", state)

	// Suspend/resume kustomization
	if output, err := fluxCombinedOutputFn("--namespace", constants.NSVolsyncSystem, state, "kustomization", "volsync"); err != nil {
		return fmt.Errorf("failed to %s kustomization: %w\n%s", state, err, output)
	}

	// Suspend/resume helmrelease
	if output, err := fluxCombinedOutputFn("--namespace", constants.NSVolsyncSystem, state, "helmrelease", "volsync"); err != nil {
		return fmt.Errorf("failed to %s helmrelease: %w\n%s", state, err, output)
	}

	// Scale deployment
	replicas := "1"
	if state == "suspend" {
		replicas = "0"
	}

	if output, err := kubectlCombinedOutputFn("--namespace", constants.NSVolsyncSystem, "scale", "deployment", "volsync", "--replicas", replicas); err != nil {
		return fmt.Errorf("failed to scale deployment: %w\n%s", err, output)
	}

	logger.Success("VolSync state changed to: %s", state)
	return nil
}

func newSuspendCommand() *cobra.Command {
	var (
		namespace string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "suspend [name]",
		Short: "Suspend VolSync ReplicationSource or ReplicationDestination",
		Long:  `Suspends a specific ReplicationSource/ReplicationDestination or all in a namespace. If namespace is not specified, presents an interactive selector.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && len(args) == 0 {
				return fmt.Errorf("either provide resource name or use --all flag")
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return suspendVolsyncResource(namespace, name, all)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes namespace (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&all, "all", false, "Suspend all ReplicationSources and ReplicationDestinations in namespace")

	return cmd
}

func suspendVolsyncResource(namespace, name string, all bool) error {
	namespace, err := resolveNamespace(namespace)
	if err != nil {
		return err
	}

	if all {
		return setAllVolsyncResourcesSuspended(namespace, true)
	}

	return setVolsyncResourceSuspended(namespace, name, true)
}

func suspendAllVolsyncResources(namespace string) error {
	return setAllVolsyncResourcesSuspended(namespace, true)
}

func newResumeCommand() *cobra.Command {
	var (
		namespace string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "resume [name]",
		Short: "Resume VolSync ReplicationSource or ReplicationDestination",
		Long:  `Resumes a specific ReplicationSource/ReplicationDestination or all in a namespace. If namespace is not specified, presents an interactive selector.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && len(args) == 0 {
				return fmt.Errorf("either provide resource name or use --all flag")
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return resumeVolsyncResource(namespace, name, all)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes namespace (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&all, "all", false, "Resume all ReplicationSources and ReplicationDestinations in namespace")

	return cmd
}

func resumeVolsyncResource(namespace, name string, all bool) error {
	namespace, err := resolveNamespace(namespace)
	if err != nil {
		return err
	}

	if all {
		return setAllVolsyncResourcesSuspended(namespace, false)
	}

	return setVolsyncResourceSuspended(namespace, name, false)
}

func resumeAllVolsyncResources(namespace string) error {
	return setAllVolsyncResourcesSuspended(namespace, false)
}

func resolveNamespace(namespace string) (string, error) {
	if namespace != "" {
		return namespace, nil
	}

	selectedNS, err := selectNamespaceFn("Select namespace:", false)
	if err != nil {
		if ui.IsCancellation(err) {
			return "", nil
		}
		return "", err
	}
	return selectedNS, nil
}

func setVolsyncResourceSuspended(namespace, name string, suspend bool) error {
	logger := common.NewColorLogger()
	if namespace == "" {
		return nil
	}

	desiredState := boolWord(suspend, "suspended", "resumed")
	resourceKinds := []string{"replicationsource", "replicationdestination"}
	var lastErr error

	for _, kind := range resourceKinds {
		if err := patchVolsyncResource(namespace, kind, name, suspend); err == nil {
			logger.Success("%s %s %s/%s", capitalize(desiredState), resourceDisplayName(kind), namespace, name)
			return nil
		} else {
			lastErr = err
		}
	}

	if lastErr != nil {
		return fmt.Errorf("resource %s not found as ReplicationSource or ReplicationDestination in namespace %s: %w", name, namespace, lastErr)
	}
	return fmt.Errorf("resource %s not found as ReplicationSource or ReplicationDestination in namespace %s", name, namespace)
}

func setAllVolsyncResourcesSuspended(namespace string, suspend bool) error {
	logger := common.NewColorLogger()
	if namespace == "" {
		return nil
	}

	desiredState := boolWord(suspend, "suspend", "resume")
	completedState := boolWord(suspend, "suspended", "resumed")
	resourceKinds := []string{"replicationsource", "replicationdestination"}

	for _, kind := range resourceKinds {
		names, err := listVolsyncResources(namespace, kind)
		if err != nil {
			logger.Warn("Failed to list %ss: %v", resourceDisplayName(kind), err)
			continue
		}

		for _, name := range names {
			if err := patchVolsyncResource(namespace, kind, name, suspend); err != nil {
				logger.Error("Failed to %s %s %s: %v", desiredState, resourceDisplayName(kind), name, err)
			} else {
				logger.Info("%s %s %s", capitalize(completedState), resourceDisplayName(kind), name)
			}
		}
	}

	logger.Success("%s all VolSync resources in namespace %s", capitalize(completedState), namespace)
	return nil
}

func listVolsyncResources(namespace, kind string) ([]string, error) {
	output, err := kubectlOutputFn("get", kind, "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

func patchVolsyncResource(namespace, kind, name string, suspend bool) error {
	patchPayload := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	return kubectlRunFn("patch", kind, name, "-n", namespace, "--type=merge", "-p", patchPayload)
}

func resourceDisplayName(kind string) string {
	switch kind {
	case "replicationsource":
		return "ReplicationSource"
	case "replicationdestination":
		return "ReplicationDestination"
	default:
		return kind
	}
}

func boolWord(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func newSnapshotCommand() *cobra.Command {
	var (
		namespace string
		app       string
		wait      bool
		timeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Trigger a snapshot for an application",
		Long:  `Manually triggers a VolSync snapshot for the specified application. If app is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return snapshotApp(namespace, app, wait, timeout)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&app, "app", "", "Application name (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for snapshot to complete")
	cmd.Flags().DurationVar(&timeout, "timeout", 120*time.Minute, "Timeout for snapshot completion")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)

	return cmd
}

func snapshotApp(namespace, app string, wait bool, timeout time.Duration) error {
	logger := common.NewColorLogger()

	// If namespace is not provided, prompt for selection
	if namespace == "" {
		selectedNS, err := selectNamespaceFn("Select namespace:", false)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return err
		}
		namespace = selectedNS
	}

	// If app is not provided, prompt for selection
	if app == "" {
		output, err := commandOutputFn("kubectl", "get", "replicationsources", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return fmt.Errorf("failed to get ReplicationSources in namespace %s: %w", namespace, err)
		}

		apps := strings.Fields(string(output))
		if len(apps) == 0 {
			return fmt.Errorf("no ReplicationSources found in namespace %s", namespace)
		}

		// Use interactive selector
		selectedApp, err := chooseOptionFn(fmt.Sprintf("Select application to snapshot in %s:", namespace), apps)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("application selection failed: %w", err)
		}
		app = selectedApp
	}

	// Check if ReplicationSource exists
	if err := commandRunFn("kubectl", "--namespace", namespace, "get", "replicationsources", app); err != nil {
		return fmt.Errorf("ReplicationSource %s not found in namespace %s", app, namespace)
	}

	logger.Info("Triggering snapshot for %s/%s", namespace, app)

	// Trigger manual snapshot
	timestamp := fmt.Sprintf("%d", volsyncNow().Unix())
	patchJSON := fmt.Sprintf(`{"spec":{"trigger":{"manual":"%s"}}}`, timestamp)

	if output, err := commandCombinedOutputFn("kubectl", "--namespace", namespace, "patch", "replicationsources", app,
		"--type", "merge", "-p", patchJSON); err != nil {
		return fmt.Errorf("failed to trigger snapshot: %w\n%s", err, output)
	}

	if !wait {
		logger.Success("Snapshot triggered for %s/%s", namespace, app)
		return nil
	}

	// Wait for job to appear
	jobName := fmt.Sprintf("volsync-src-%s", app)
	logger.Info("Waiting for job %s to start...", jobName)

	startTime := time.Now()
	for {
		if time.Since(startTime) > timeout {
			return fmt.Errorf("timeout waiting for job to start")
		}

		if err := commandRunFn("kubectl", "--namespace", namespace, "get", fmt.Sprintf("job/%s", jobName)); err == nil {
			break
		}

		volsyncSleep(5 * time.Second)
	}

	// Wait for job to complete
	logger.Info("Waiting for snapshot to complete (timeout: %s)...", timeout)

	if err := commandRunFn("kubectl", "--namespace", namespace, "wait",
		fmt.Sprintf("job/%s", jobName),
		"--for=condition=complete",
		fmt.Sprintf("--timeout=%ds", int(timeout.Seconds()))); err != nil {
		return fmt.Errorf("snapshot job failed or timed out: %w", err)
	}

	logger.Success("Snapshot completed successfully for %s/%s", namespace, app)
	return nil
}

func newSnapshotAllCommand() *cobra.Command {
	var (
		wait        bool
		timeout     time.Duration
		namespace   string
		dryRun      bool
		concurrency int
	)

	cmd := &cobra.Command{
		Use:   "snapshot-all",
		Short: "Trigger snapshots for all eligible VolSync configured PVCs",
		Long:  `Discovers all ReplicationSources across all namespaces and triggers snapshots for them`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return snapshotAllApps(namespace, wait, timeout, dryRun, concurrency)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace (if empty, searches all namespaces)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for all snapshots to complete")
	cmd.Flags().DurationVar(&timeout, "timeout", 120*time.Minute, "Timeout for each snapshot completion")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be snapshotted without actually triggering snapshots")
	cmd.Flags().IntVar(&concurrency, "concurrency", 3, "Number of parallel snapshots to run (default: 3)")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func snapshotAllApps(namespace string, wait bool, timeout time.Duration, dryRun bool, concurrency int) error {
	logger := common.NewColorLogger()

	// Validate concurrency to prevent deadlock from zero-length semaphore channel
	if concurrency <= 0 {
		concurrency = 1
	}

	// Discover all ReplicationSources
	replicationSources, err := discoverReplicationSources(namespace)
	if err != nil {
		return fmt.Errorf("failed to discover ReplicationSources: %w", err)
	}

	if len(replicationSources) == 0 {
		logger.Info("No ReplicationSources found")
		return nil
	}

	logger.Info("Found %d ReplicationSources to snapshot", len(replicationSources))

	if dryRun {
		logger.Info("Dry run mode - showing what would be snapshotted:")
		for _, rs := range replicationSources {
			logger.Info("  - %s/%s", rs.Namespace, rs.Name)
		}
		return nil
	}

	// Track results with thread-safe access
	var successful, failed []string
	var mutex sync.Mutex

	// Create a semaphore to limit concurrency
	semaphore := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	logger.Info("Processing %d snapshots with concurrency limit of %d", len(replicationSources), concurrency)

	// Trigger snapshots for all ReplicationSources in parallel
	for _, rs := range replicationSources {
		wg.Add(1)
		go func(rs ReplicationSource) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			logger.Info("Processing %s/%s...", rs.Namespace, rs.Name)

			err := snapshotAppFn(rs.Namespace, rs.Name, wait, timeout)

			// Thread-safe result tracking
			mutex.Lock()
			if err != nil {
				logger.Error("Failed to snapshot %s/%s: %v", rs.Namespace, rs.Name, err)
				failed = append(failed, fmt.Sprintf("%s/%s", rs.Namespace, rs.Name))
			} else {
				logger.Success("✓ Completed snapshot for %s/%s", rs.Namespace, rs.Name)
				successful = append(successful, fmt.Sprintf("%s/%s", rs.Namespace, rs.Name))
			}
			mutex.Unlock()
		}(rs)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Report results
	logger.Info("Snapshot operation completed:")
	logger.Success("Successful: %d", len(successful))
	if len(successful) > 0 {
		for _, app := range successful {
			logger.Success("  ✓ %s", app)
		}
	}

	if len(failed) > 0 {
		logger.Error("Failed: %d", len(failed))
		for _, app := range failed {
			logger.Error("  ✗ %s", app)
		}
		return fmt.Errorf("%d snapshots failed", len(failed))
	}

	return nil
}

type ReplicationSource struct {
	Name      string
	Namespace string
}

func discoverReplicationSources(namespace string) ([]ReplicationSource, error) {
	var output []byte
	var err error
	if namespace == "" {
		// Search all namespaces
		output, err = kubectlCombinedOutputFn("get", "replicationsources", "--all-namespaces",
			"--output=custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name", "--no-headers")
	} else {
		// Validate namespace exists first
		if err := kubectlRunFn("get", "namespace", namespace); err != nil {
			return nil, fmt.Errorf("namespace '%s' does not exist or is not accessible", namespace)
		}

		// Search specific namespace
		output, err = kubectlCombinedOutputFn("--namespace", namespace, "get", "replicationsources",
			"--output=custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name", "--no-headers")
	}

	if err != nil {
		// If no ReplicationSources found, kubectl returns an error
		outputStr := string(output)
		if strings.Contains(outputStr, "No resources found") ||
			strings.Contains(err.Error(), "No resources found") ||
			strings.Contains(outputStr, "the server doesn't have a resource type") {
			return []ReplicationSource{}, nil
		}
		return nil, fmt.Errorf("failed to get ReplicationSources in namespace '%s': %w\nOutput: %s", namespace, err, outputStr)
	}

	return parseReplicationSourcesOutput(string(output))
}

func parseReplicationSourcesOutput(output string) ([]ReplicationSource, error) {
	var replicationSources []ReplicationSource
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for lineNum, line := range lines {
		if line == "" {
			continue
		}

		// Split by whitespace and take first two fields
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed kubectl output at line %d: expected at least 2 fields, got %d: %s", lineNum+1, len(fields), line)
		}

		// Validate namespace and name are not empty
		if fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("empty namespace or name at line %d: namespace='%s', name='%s'", lineNum+1, fields[0], fields[1])
		}

		replicationSources = append(replicationSources, ReplicationSource{
			Namespace: fields[0],
			Name:      fields[1],
		})
	}

	return replicationSources, nil
}

// detectController determines the controller type for an application
func detectController(namespace, app string) (string, error) {
	// Validate inputs
	if namespace == "" {
		return "", fmt.Errorf("namespace cannot be empty")
	}
	if app == "" {
		return "", fmt.Errorf("app name cannot be empty")
	}

	// Validate namespace exists
	if err := kubectlRunFn("get", "namespace", namespace); err != nil {
		return "", fmt.Errorf("namespace '%s' does not exist or is not accessible", namespace)
	}

	// List of controller types to check in order of preference
	controllers := []string{"deployment", "statefulset", "daemonset", "replicaset"}

	var lastError error
	for _, controller := range controllers {
		if err := kubectlRunFn("--namespace", namespace, "get", controller, app); err == nil {
			return controller, nil
		} else {
			lastError = err
		}
	}

	// Log the detection attempt but still return deployment as fallback
	// This allows the calling code to proceed but with awareness that controller may not exist
	return "deployment", fmt.Errorf("no existing controller found for app '%s' in namespace '%s' (last error: %w), defaulting to deployment", app, namespace, lastError)
}

func newRestoreCommand() *cobra.Command {
	var (
		namespace      string
		app            string
		previous       string
		force          bool
		restoreTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore an application from a VolSync snapshot",
		Long:  `Restores an application's PVC from a previous VolSync snapshot. If app or snapshot is not specified, presents interactive selectors.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return restoreApp(namespace, app, previous, force, restoreTimeout)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&app, "app", "", "Application name (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&previous, "previous", "", "Previous snapshot number to restore (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Force restore without confirmation")
	cmd.Flags().DurationVar(&restoreTimeout, "restore-timeout", 120*time.Minute, "Timeout for restore job completion")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)

	return cmd
}

func restoreApp(namespace, app, previous string, force bool, restoreTimeout time.Duration) error {
	logger := common.NewColorLogger()

	// If namespace is not provided, prompt for selection
	if namespace == "" {
		selectedNS, err := selectNamespaceFn("Select namespace:", false)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return err
		}
		namespace = selectedNS
	}

	// If app is not provided, prompt for selection
	if app == "" {
		output, err := commandOutputFn("kubectl", "get", "replicationsources", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return fmt.Errorf("failed to get ReplicationSources in namespace %s: %w", namespace, err)
		}

		apps := strings.Fields(string(output))
		if len(apps) == 0 {
			return fmt.Errorf("no ReplicationSources found in namespace %s", namespace)
		}

		// Use interactive selector
		selectedApp, err := chooseOptionFn(fmt.Sprintf("Select application to restore in %s:", namespace), apps)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("application selection failed: %w", err)
		}
		app = selectedApp
	}

	// If previous snapshot is not provided, list and prompt for selection
	if previous == "" {
		// Try to get snapshots via Snapshot CRs first
		output, err := commandOutputFn("kubectl", "get", "snapshots", "-n", namespace,
			"-l", fmt.Sprintf("app=%s", app),
			"-o", "jsonpath={.items[*].spec.snapshotNum}")

		var snapshots []string
		if err == nil {
			snapshots = strings.Fields(string(output))
		}

		// If no snapshots found via CRs, try querying Kopia directly
		if len(snapshots) == 0 {
			logger.Info("Listing snapshots from Kopia repository...")

			// Find kopia pod
			kopiaPod, err := findKopiaPod()
			if err != nil {
				return fmt.Errorf("failed to find kopia pod: %w", err)
			}

			// Get all snapshots from kopia
			output, err := commandCombinedOutputFn("kubectl", "--namespace", constants.NSVolsyncSystem, "exec", kopiaPod, "--", "kopia", "snapshot", "list", "--all")
			if err != nil {
				return fmt.Errorf("failed to get snapshots from kopia: %w", err)
			}

			// Parse snapshots
			appSnapshots, err := parseKopiaSnapshots(string(output), app)
			if err != nil {
				return fmt.Errorf("failed to parse snapshots: %w", err)
			}

			if len(appSnapshots) > 0 {
				// Flatten all snapshots for this app
				for _, snap := range appSnapshots {
					snapshots = append(snapshots, snap.AllSnapshots...)
				}
			}
		}

		if len(snapshots) == 0 {
			return fmt.Errorf("no snapshots found for app %s in namespace %s", app, namespace)
		}

		// Use Filter for better search experience
		selectedSnapshot, err := filterOptionFn("Search for snapshot:", snapshots)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("snapshot selection failed: %w", err)
		}

		previous = snapshotIDFromSelection(selectedSnapshot)
	}

	// Add confirmation for restore
	if !force {
		confirmed, err := confirmActionFn(fmt.Sprintf("Restore %s from snapshot %s? Data will be overwritten!", app, previous), false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			logger.Info("Restore cancelled")
			return fmt.Errorf("restore cancelled")
		}
	}

	logger.Info("Starting restore process for %s/%s from snapshot %s", namespace, app, previous)

	// Get controller type by checking what exists
	controller, err := detectController(namespace, app)
	controllerFound := err == nil
	if err != nil {
		// Log the warning but continue since detectController returns a fallback controller type
		logger.Warn("Controller detection: %v", err)
	}

	// Step 1: Suspend Flux resources
	logger.Info("Suspending Flux resources...")

	if err := fluxRunFn("--namespace", namespace, "suspend", "kustomization", app); err != nil {
		logger.Warn("Failed to suspend kustomization: %v", err)
	}

	if err := fluxRunFn("--namespace", namespace, "suspend", "helmrelease", app); err != nil {
		logger.Warn("Failed to suspend helmrelease: %v", err)
	}

	// Step 2: Scale down application (only if controller found)
	if controllerFound {
		logger.Info("Scaling down %s/%s...", controller, app)

		if output, err := commandCombinedOutputFn("kubectl", "--namespace", namespace, "scale", fmt.Sprintf("%s/%s", controller, app), "--replicas=0"); err != nil {
			return fmt.Errorf("failed to scale down: %w\n%s", err, output)
		}

		// Wait for pods to be deleted
		logger.Info("Waiting for pods to terminate...")
		if err := commandRunFn("kubectl", "--namespace", namespace, "wait", "pod",
			"--for=delete", fmt.Sprintf("--selector=app.kubernetes.io/name=%s", app),
			"--timeout=5m"); err != nil {
			logger.Warn("Some pods may still be terminating: %v", err)
		}
	} else {
		logger.Info("Skipping scale down as no controller was detected")
	}

	// Step 3: Get ReplicationSource details
	logger.Info("Getting restore configuration...")

	// Get claim name
	output, err := commandOutputFn("kubectl", "--namespace", namespace, "get",
		fmt.Sprintf("replicationsources/%s", app),
		"--output=jsonpath={.spec.sourcePVC}")
	if err != nil {
		return fmt.Errorf("failed to get PVC name: %w", err)
	}
	claim := strings.TrimSpace(string(output))

	// Get other required fields for Snapshot copyMethod
	fields := map[string]string{
		"CACHE_STORAGE_CLASS": "{.spec.kopia.cacheStorageClassName}",
		"CACHE_CAPACITY":      "{.spec.kopia.cacheCapacity}",
		"CAPACITY":            "{.spec.kopia.cacheCapacity}",
		"PUID":                "{.spec.kopia.moverSecurityContext.runAsUser}",
		"PGID":                "{.spec.kopia.moverSecurityContext.runAsGroup}",
		"STORAGE_CLASS":       "{.spec.kopia.storageClassName}",
		"SNAPSHOT_CLASS":      "{.spec.kopia.volumeSnapshotClassName}",
	}

	env := map[string]string{
		"NS":       namespace,
		"APP":      app,
		"PREVIOUS": previous,
		"CLAIM":    claim,
	}

	for envKey, jsonPath := range fields {
		output, err := commandOutputFn("kubectl", "--namespace", namespace, "get",
			fmt.Sprintf("replicationsources/%s", app),
			fmt.Sprintf("--output=jsonpath=%s", jsonPath))
		if err != nil {
			return fmt.Errorf("failed to get %s: %w", envKey, err)
		}
		env[envKey] = strings.TrimSpace(string(output))
	}

	// Handle ACCESS_MODES separately to convert from JSON to YAML format
	accessModesOutput, err := commandOutputFn("kubectl", "--namespace", namespace, "get",
		fmt.Sprintf("replicationsources/%s", app),
		"--output=jsonpath={.spec.kopia.accessModes}")
	if err != nil {
		return fmt.Errorf("failed to get ACCESS_MODES: %w", err)
	}
	accessModesStr := strings.TrimSpace(string(accessModesOutput))
	// Convert from ["ReadWriteOnce"] to ["ReadWriteOnce"] format for YAML
	if strings.HasPrefix(accessModesStr, "[") && strings.HasSuffix(accessModesStr, "]") {
		env["ACCESS_MODES"] = accessModesStr
	} else {
		// Fallback to default if parsing fails
		env["ACCESS_MODES"] = "[\"ReadWriteOnce\"]"
	}

	// Handle CACHE_ACCESS_MODES separately
	cacheAccessModesOutput, err := commandOutputFn("kubectl", "--namespace", namespace, "get",
		fmt.Sprintf("replicationsources/%s", app),
		"--output=jsonpath={.spec.kopia.cacheAccessModes}")
	if err != nil {
		return fmt.Errorf("failed to get CACHE_ACCESS_MODES: %w", err)
	}
	cacheAccessModesStr := strings.TrimSpace(string(cacheAccessModesOutput))
	if strings.HasPrefix(cacheAccessModesStr, "[") && strings.HasSuffix(cacheAccessModesStr, "]") {
		env["CACHE_ACCESS_MODES"] = cacheAccessModesStr
	} else {
		// Fallback to default if parsing fails
		env["CACHE_ACCESS_MODES"] = "[\"ReadWriteOnce\"]"
	}

	// Step 4: Create ReplicationDestination
	logger.Info("Creating ReplicationDestination...")

	// Create the ReplicationDestination YAML using embedded template
	rdYAML, err := renderVolsyncTemplateFn("replicationdestination.yaml.j2", env)
	if err != nil {
		return fmt.Errorf("failed to render ReplicationDestination template: %w", err)
	}

	if output, err := kubectlApplyYAMLFn(rdYAML); err != nil {
		return fmt.Errorf("failed to create ReplicationDestination: %w\n%s", err, output)
	}

	// Step 5: Wait for restore job to complete
	jobName := fmt.Sprintf("volsync-dst-%s-manual", app)
	logger.Info("Waiting for restore job %s to complete...", jobName)

	// Wait for job to appear with timeout (use 1/4 of restoreTimeout or 5 minutes max for job appearance)
	jobAppearTimeout := restoreTimeout / 4
	if jobAppearTimeout > 5*time.Minute {
		jobAppearTimeout = 5 * time.Minute
	}
	jobAppearStart := time.Now()
	jobAppeared := false
	for time.Since(jobAppearStart) < jobAppearTimeout {
		if err := commandRunFn("kubectl", "--namespace", namespace, "get", fmt.Sprintf("job/%s", jobName)); err == nil {
			jobAppeared = true
			break
		}
		volsyncSleep(5 * time.Second)
	}
	if !jobAppeared {
		return fmt.Errorf("restore job %s did not appear within %v", jobName, jobAppearTimeout)
	}

	// Wait for completion
	if err := commandRunFn("kubectl", "--namespace", namespace, "wait",
		fmt.Sprintf("job/%s", jobName),
		"--for=condition=complete",
		fmt.Sprintf("--timeout=%ds", int(restoreTimeout.Seconds()))); err != nil {
		return fmt.Errorf("restore job failed: %w", err)
	}

	// Step 6: Clean up ReplicationDestination
	logger.Info("Cleaning up...")
	if err := commandRunFn("kubectl", "--namespace", namespace, "delete", "replicationdestination", fmt.Sprintf("%s-manual", app)); err != nil {
		logger.Warn("Failed to delete ReplicationDestination: %v", err)
	}

	// Step 7: Resume Flux resources
	logger.Info("Resuming application...")

	if err := fluxRunFn("--namespace", namespace, "resume", "kustomization", app); err != nil {
		logger.Warn("Failed to resume kustomization: %v", err)
	}

	if err := fluxRunFn("--namespace", namespace, "resume", "helmrelease", app); err != nil {
		logger.Warn("Failed to resume helmrelease: %v", err)
	}

	if err := fluxRunFn("--namespace", namespace, "reconcile", "kustomization", app, "--with-source"); err != nil {
		logger.Warn("Failed to reconcile kustomization: %v", err)
	}

	if err := fluxRunFn("--namespace", namespace, "reconcile", "helmrelease", app, "--force"); err != nil {
		logger.Warn("Failed to reconcile helmrelease: %v", err)
	}

	// Wait for pods to be ready
	logger.Info("Waiting for application to be ready...")
	if err := commandRunFn("kubectl", "--namespace", namespace, "wait", "pod",
		"--for=condition=ready", fmt.Sprintf("--selector=app.kubernetes.io/name=%s", app),
		"--timeout=5m"); err != nil {
		logger.Warn("Application may still be starting: %v", err)
	}

	logger.Success("Restore completed successfully for %s/%s", namespace, app)
	return nil
}

// renderReplicationDestination function removed - now using embedded templates

func newRestoreAllCommand() *cobra.Command {
	var (
		namespace string
		previous  string
		force     bool
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "restore-all",
		Short: "Restore all applications from VolSync snapshots in a namespace",
		Long:  `Discovers all ReplicationSources in a namespace and restores them from specified snapshots`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// If namespace is not provided, prompt for selection
			if namespace == "" {
				selectedNS, err := selectNamespaceFn("Select namespace:", false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil // User cancelled - exit cleanly
					}
					return err
				}
				namespace = selectedNS
			}

			if !force && !dryRun {
				message := fmt.Sprintf("This will restore ALL applications in namespace '%s' from snapshot %s. Data will be overwritten. Continue?", namespace, previous)
				confirmed, err := confirmActionFn(message, false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
					return fmt.Errorf("restore cancelled")
				}
			}
			return restoreAllApps(namespace, previous, dryRun)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&previous, "previous", "", "Previous snapshot number to restore (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force restore without confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be restored without actually triggering restores")
	_ = cmd.MarkFlagRequired("previous")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func restoreAllApps(namespace, previous string, dryRun bool) error {
	logger := common.NewColorLogger()

	// Discover all ReplicationSources in the specified namespace
	replicationSources, err := discoverReplicationSources(namespace)
	if err != nil {
		return fmt.Errorf("failed to discover ReplicationSources: %w", err)
	}

	if len(replicationSources) == 0 {
		logger.Info("No ReplicationSources found in namespace %s", namespace)
		return nil
	}

	logger.Info("Found %d ReplicationSources to restore in namespace %s", len(replicationSources), namespace)

	if dryRun {
		logger.Info("Dry run mode - showing what would be restored:")
		for _, rs := range replicationSources {
			logger.Info("  - %s/%s (snapshot: %s)", rs.Namespace, rs.Name, previous)
		}
		return nil
	}

	// Track results
	var successful, failed []string

	// Restore all ReplicationSources
	for _, rs := range replicationSources {
		logger.Info("Processing restore for %s/%s...", rs.Namespace, rs.Name)

		err := restoreAppFn(rs.Namespace, rs.Name, previous, false, 120*time.Minute)
		if err != nil {
			logger.Error("Failed to restore %s/%s: %v", rs.Namespace, rs.Name, err)
			failed = append(failed, fmt.Sprintf("%s/%s", rs.Namespace, rs.Name))
		} else {
			logger.Success("✓ Successfully restored %s/%s", rs.Namespace, rs.Name)
			successful = append(successful, fmt.Sprintf("%s/%s", rs.Namespace, rs.Name))
		}
	}

	// Report results
	logger.Info("\nRestore Summary:")
	logger.Info("  Successful: %d", len(successful))
	for _, app := range successful {
		logger.Info("    ✓ %s", app)
	}

	if len(failed) > 0 {
		logger.Info("  Failed: %d", len(failed))
		for _, app := range failed {
			logger.Error("    ✗ %s", app)
		}
		return fmt.Errorf("%d restore(s) failed", len(failed))
	}

	logger.Success("All restores completed successfully!")
	return nil
}

func newSnapshotsCommand() *cobra.Command {
	var (
		app    string
		format string
	)

	cmd := &cobra.Command{
		Use:   "snapshots",
		Short: "List snapshots for applications",
		Long:  `Lists all available snapshots for applications from the Kopia repository`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listSnapshots(app, format)
		},
	}

	cmd.Flags().StringVar(&app, "app", "", "Filter snapshots for specific application")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table, json, yaml")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)

	return cmd
}

func listSnapshots(appFilter, format string) error {
	logger := common.NewColorLogger()

	// Find kopia pod in volsync-system namespace
	kopiaPod, err := findKopiaPod()
	if err != nil {
		return fmt.Errorf("failed to find kopia pod: %w", err)
	}

	// Get all snapshots from kopia
	output, err := commandCombinedOutputFn("kubectl", "--namespace", constants.NSVolsyncSystem, "exec", kopiaPod, "--", "kopia", "snapshot", "list", "--all")
	if err != nil {
		return fmt.Errorf("failed to get snapshots: %w\nOutput: %s", err, output)
	}

	snapshots, err := parseKopiaSnapshots(string(output), appFilter)
	if err != nil {
		return fmt.Errorf("failed to parse snapshots: %w", err)
	}

	if len(snapshots) == 0 {
		if appFilter != "" {
			logger.Info("No snapshots found for app: %s", appFilter)
		} else {
			logger.Info("No snapshots found")
		}
		return nil
	}

	switch format {
	case "table":
		displaySnapshotsTable(snapshots, logger)
	case "json":
		displaySnapshotsJSON(snapshots)
	case "yaml":
		displaySnapshotsYAML(snapshots)
	default:
		return fmt.Errorf("unsupported format: %s (supported: table, json, yaml)", format)
	}

	return nil
}

type AppSnapshot struct {
	App           string   `json:"app" yaml:"app"`
	Namespace     string   `json:"namespace" yaml:"namespace"`
	Count         int      `json:"count" yaml:"count"`
	LatestTime    string   `json:"latest_time" yaml:"latest_time"`
	LatestID      string   `json:"latest_id" yaml:"latest_id"`
	Size          string   `json:"size" yaml:"size"`
	RetentionTags string   `json:"retention_tags" yaml:"retention_tags"`
	AllSnapshots  []string `json:"all_snapshots" yaml:"all_snapshots"`
}

func findKopiaPod() (string, error) {
	output, err := kubectlOutputFn("--namespace", constants.NSVolsyncSystem, "get", "pods", "-l", "app.kubernetes.io/name=kopia", "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		return "", err
	}
	podName := strings.TrimSpace(string(output))
	if podName == "" {
		return "", fmt.Errorf("no kopia pod found")
	}
	return podName, nil
}

func isKopiaSnapshotLine(fields []string) bool {
	if len(fields) < 6 {
		return false
	}
	if _, err := time.Parse("2006-01-02", fields[0]); err != nil {
		return false
	}
	if _, err := time.Parse("15:04:05", fields[1]); err != nil {
		return false
	}
	return true
}

func snapshotIDFromSelection(selected string) string {
	selected = strings.TrimSpace(selected)
	if strings.Contains(selected, "(") && strings.HasSuffix(selected, ")") {
		start := strings.LastIndex(selected, "(")
		end := strings.LastIndex(selected, ")")
		if start != -1 && end != -1 && end > start {
			return selected[start+1 : end]
		}
	}
	return selected
}

func parseKopiaSnapshots(output, appFilter string) ([]AppSnapshot, error) {
	var snapshots []AppSnapshot
	lines := strings.Split(output, "\n")

	var currentApp AppSnapshot
	var inSnapshotBlock bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Check if this is an app header line (e.g., "fusion@default:/data")
		if strings.Contains(line, "@") && strings.Contains(line, ":/data") {
			// Save previous app if exists
			if inSnapshotBlock && currentApp.App != "" {
				snapshots = append(snapshots, currentApp)
			}

			// Parse new app
			parts := strings.Split(line, "@")
			if len(parts) >= 2 {
				app := parts[0]
				nsParts := strings.Split(parts[1], ":")
				if len(nsParts) >= 1 {
					namespace := nsParts[0]

					// Filter by app if specified
					if appFilter != "" && app != appFilter {
						inSnapshotBlock = false
						continue
					}

					currentApp = AppSnapshot{
						App:          app,
						Namespace:    namespace,
						AllSnapshots: []string{},
					}
					inSnapshotBlock = true
				}
			}
		} else if inSnapshotBlock && strings.HasPrefix(line, "+ ") {
			// This indicates additional identical snapshots
			// Parse the count from "+ X identical snapshots until..."
			if strings.Contains(line, "identical snapshots") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if additionalCount, err := strconv.Atoi(fields[1]); err == nil {
						currentApp.Count += additionalCount
					}
				}
			}
		} else if inSnapshotBlock {
			// This is a snapshot line
			fields := strings.Fields(line)
			if isKopiaSnapshotLine(fields) {
				// Format: DATE TIME TZ SNAPSHOT_ID SIZE SIZE_UNIT (tags...)
				timestamp := strings.Join(fields[0:3], " ")
				snapshotID := fields[3]
				size := fields[4]
				sizeUnit := fields[5]

				fullSize := size + " " + sizeUnit

				// Extract retention tags (latest-1..3, hourly-1, etc.)
				retentionStart := strings.Index(line, "(")
				retentionEnd := strings.Index(line, ")")
				var retentionTags string
				if retentionStart > 0 && retentionEnd > retentionStart {
					retentionTags = line[retentionStart+1 : retentionEnd]
				}

				// If this is the first snapshot for this app, set as latest
				if currentApp.Count == 0 {
					currentApp.LatestTime = timestamp
					currentApp.LatestID = snapshotID
					currentApp.Size = fullSize
					currentApp.RetentionTags = retentionTags
				}

				currentApp.Count++
				currentApp.AllSnapshots = append(currentApp.AllSnapshots, fmt.Sprintf("%s (%s)", timestamp, snapshotID))
			}
		}
	}

	// Add the last app
	if inSnapshotBlock && currentApp.App != "" {
		snapshots = append(snapshots, currentApp)
	}

	return snapshots, nil
}

func displaySnapshotsTable(snapshots []AppSnapshot, logger *common.ColorLogger) {
	logger.Info("VolSync Snapshots Summary:")
	logger.Info("")

	// Header
	fmt.Printf("%-15s %-12s %-8s %-20s %-10s %s\n", "APP", "NAMESPACE", "COUNT", "LATEST", "SIZE", "RETENTION")
	fmt.Printf("%-15s %-12s %-8s %-20s %-10s %s\n", "---", "---------", "-----", "------", "----", "---------")

	// Rows
	for _, snap := range snapshots {
		// Safely truncate LatestTime to avoid index out of bounds
		displayTime := snap.LatestTime
		if len(displayTime) > 19 {
			displayTime = displayTime[0:19]
		}
		fmt.Printf("%-15s %-12s %-8d %-20s %-10s %s\n",
			snap.App,
			snap.Namespace,
			snap.Count,
			displayTime,
			snap.Size,
			snap.RetentionTags)
	}

	fmt.Printf("\nTotal applications: %d\n", len(snapshots))
}

func displaySnapshotsJSON(snapshots []AppSnapshot) {
	output, _ := json.MarshalIndent(snapshots, "", "  ")
	fmt.Println(string(output))
}

func displaySnapshotsYAML(snapshots []AppSnapshot) {
	for i, snap := range snapshots {
		fmt.Printf("- app: %s\n", snap.App)
		fmt.Printf("  namespace: %s\n", snap.Namespace)
		fmt.Printf("  count: %d\n", snap.Count)
		fmt.Printf("  latest_time: %s\n", snap.LatestTime)
		fmt.Printf("  latest_id: %s\n", snap.LatestID)
		fmt.Printf("  size: %s\n", snap.Size)
		fmt.Printf("  retention_tags: %s\n", snap.RetentionTags)
		fmt.Printf("  all_snapshots:\n")
		for _, snapshot := range snap.AllSnapshots {
			fmt.Printf("    - %s\n", snapshot)
		}
		if i < len(snapshots)-1 {
			fmt.Printf("\n")
		}
	}
}
