package volsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/templates"

	"github.com/spf13/cobra"
)

// VolsyncConfig holds configuration for volsync operations
type VolsyncConfig struct {
	NFSServer string
	NFSPath   string
}

// getEnvOrDefault returns environment variable value or default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volsync",
		Short: "Manage VolSync backup and restore operations",
		Long:  `Commands for managing VolSync snapshots, restores, and Kopia repository operations`,
	}

	cmd.AddCommand(
		newStateCommand(),
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
	cmd := exec.Command("flux", "--namespace", "volsync-system", state, "kustomization", "volsync")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to %s kustomization: %w\n%s", state, err, output)
	}

	// Suspend/resume helmrelease
	cmd = exec.Command("flux", "--namespace", "volsync-system", state, "helmrelease", "volsync")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to %s helmrelease: %w\n%s", state, err, output)
	}

	// Scale deployment
	replicas := "1"
	if state == "suspend" {
		replicas = "0"
	}

	cmd = exec.Command("kubectl", "--namespace", "volsync-system", "scale", "deployment", "volsync", "--replicas", replicas)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to scale deployment: %w\n%s", err, output)
	}

	logger.Success("VolSync state changed to: %s", state)
	return nil
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
		Long:  `Manually triggers a VolSync snapshot for the specified application`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return snapshotApp(namespace, app, wait, timeout)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	cmd.Flags().StringVar(&app, "app", "", "Application name (required)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for snapshot to complete")
	cmd.Flags().DurationVar(&timeout, "timeout", 120*time.Minute, "Timeout for snapshot completion")
	_ = cmd.MarkFlagRequired("app")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)

	return cmd
}

func snapshotApp(namespace, app string, wait bool, timeout time.Duration) error {
	logger := common.NewColorLogger()

	// Check if ReplicationSource exists
	checkCmd := exec.Command("kubectl", "--namespace", namespace, "get", "replicationsources", app)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("ReplicationSource %s not found in namespace %s", app, namespace)
	}

	logger.Info("Triggering snapshot for %s/%s", namespace, app)

	// Trigger manual snapshot
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	patchJSON := fmt.Sprintf(`{"spec":{"trigger":{"manual":"%s"}}}`, timestamp)

	cmd := exec.Command("kubectl", "--namespace", namespace, "patch", "replicationsources", app,
		"--type", "merge", "-p", patchJSON)

	if output, err := cmd.CombinedOutput(); err != nil {
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

		checkJob := exec.Command("kubectl", "--namespace", namespace, "get", fmt.Sprintf("job/%s", jobName))
		if err := checkJob.Run(); err == nil {
			break
		}

		time.Sleep(5 * time.Second)
	}

	// Wait for job to complete
	logger.Info("Waiting for snapshot to complete (timeout: %s)...", timeout)

	waitCmd := exec.Command("kubectl", "--namespace", namespace, "wait",
		fmt.Sprintf("job/%s", jobName),
		"--for=condition=complete",
		fmt.Sprintf("--timeout=%ds", int(timeout.Seconds())))

	if err := waitCmd.Run(); err != nil {
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

			err := snapshotApp(rs.Namespace, rs.Name, wait, timeout)
			
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
	var cmd *exec.Cmd
	if namespace == "" {
		// Search all namespaces
		cmd = exec.Command("kubectl", "get", "replicationsources", "--all-namespaces",
			"--output=custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name", "--no-headers")
	} else {
		// Validate namespace exists first
		checkNsCmd := exec.Command("kubectl", "get", "namespace", namespace)
		if err := checkNsCmd.Run(); err != nil {
			return nil, fmt.Errorf("namespace '%s' does not exist or is not accessible", namespace)
		}
		
		// Search specific namespace
		cmd = exec.Command("kubectl", "--namespace", namespace, "get", "replicationsources",
			"--output=custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name", "--no-headers")
	}

	output, err := cmd.CombinedOutput()
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

	var replicationSources []ReplicationSource
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

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
	checkNsCmd := exec.Command("kubectl", "get", "namespace", namespace)
	if err := checkNsCmd.Run(); err != nil {
		return "", fmt.Errorf("namespace '%s' does not exist or is not accessible", namespace)
	}
	
	// List of controller types to check in order of preference
	controllers := []string{"deployment", "statefulset", "daemonset", "replicaset"}
	
	var lastError error
	for _, controller := range controllers {
		cmd := exec.Command("kubectl", "--namespace", namespace, "get", controller, app)
		if err := cmd.Run(); err == nil {
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
		Long:  `Restores an application's PVC from a previous VolSync snapshot`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Printf("This will restore %s from snapshot %s. Data will be overwritten. Continue? (y/N): ", app, previous)
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("restore cancelled")
				}
			}
			return restoreApp(namespace, app, previous, restoreTimeout)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	cmd.Flags().StringVar(&app, "app", "", "Application name (required)")
	cmd.Flags().StringVar(&previous, "previous", "", "Previous snapshot number to restore (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force restore without confirmation")
	cmd.Flags().DurationVar(&restoreTimeout, "restore-timeout", 120*time.Minute, "Timeout for restore job completion")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("previous")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)

	return cmd
}

func restoreApp(namespace, app, previous string, restoreTimeout time.Duration) error {
	logger := common.NewColorLogger()

	logger.Info("Starting restore process for %s/%s from snapshot %s", namespace, app, previous)

	// Get controller type by checking what exists
	controller, err := detectController(namespace, app)
	if err != nil {
		// Log the warning but continue since detectController returns a fallback controller type
		logger.Warn("Controller detection: %v", err)
	}

	// Step 1: Suspend Flux resources
	logger.Info("Suspending Flux resources...")

	cmd := exec.Command("flux", "--namespace", namespace, "suspend", "kustomization", app)
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to suspend kustomization: %v", err)
	}

	cmd = exec.Command("flux", "--namespace", namespace, "suspend", "helmrelease", app)
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to suspend helmrelease: %v", err)
	}

	// Step 2: Scale down application
	logger.Info("Scaling down %s/%s...", controller, app)

	cmd = exec.Command("kubectl", "--namespace", namespace, "scale", fmt.Sprintf("%s/%s", controller, app), "--replicas=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to scale down: %w\n%s", err, output)
	}

	// Wait for pods to be deleted
	logger.Info("Waiting for pods to terminate...")
	cmd = exec.Command("kubectl", "--namespace", namespace, "wait", "pod",
		"--for=delete", fmt.Sprintf("--selector=app.kubernetes.io/name=%s", app),
		"--timeout=5m")
	if err := cmd.Run(); err != nil {
		logger.Warn("Some pods may still be terminating: %v", err)
	}

	// Step 3: Get ReplicationSource details
	logger.Info("Getting restore configuration...")

	// Get claim name
	cmd = exec.Command("kubectl", "--namespace", namespace, "get",
		fmt.Sprintf("replicationsources/%s", app),
		"--output=jsonpath={.spec.sourcePVC}")
	claimOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get PVC name: %w", err)
	}
	claim := strings.TrimSpace(string(claimOutput))

	// Get other required fields
	fields := map[string]string{
		"STORAGE_CLASS_NAME": "{.spec.kopia.storageClassName}",
		"PUID":               "{.spec.kopia.moverSecurityContext.runAsUser}",
		"PGID":               "{.spec.kopia.moverSecurityContext.runAsGroup}",
	}

	env := map[string]string{
		"NS":       namespace,
		"APP":      app,
		"PREVIOUS": previous,
		"CLAIM":    claim,
	}

	for envKey, jsonPath := range fields {
		cmd = exec.Command("kubectl", "--namespace", namespace, "get",
			fmt.Sprintf("replicationsources/%s", app),
			fmt.Sprintf("--output=jsonpath=%s", jsonPath))
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get %s: %w", envKey, err)
		}
		env[envKey] = strings.TrimSpace(string(output))
	}

	// Handle ACCESS_MODES separately to convert from JSON to YAML format
	cmd = exec.Command("kubectl", "--namespace", namespace, "get",
		fmt.Sprintf("replicationsources/%s", app),
		"--output=jsonpath={.spec.kopia.accessModes}")
	accessModesOutput, err := cmd.Output()
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

	// Step 4: Create ReplicationDestination
	logger.Info("Creating ReplicationDestination...")

	// Create the ReplicationDestination YAML using embedded template
	rdYAML, err := templates.RenderVolsyncTemplate("replicationdestination.yaml.j2", env)
	if err != nil {
		return fmt.Errorf("failed to render ReplicationDestination template: %w", err)
	}

	cmd = exec.Command("kubectl", "apply", "--server-side", "--filename", "-")
	cmd.Stdin = bytes.NewReader([]byte(rdYAML))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create ReplicationDestination: %w\n%s", err, output)
	}

	// Step 5: Wait for restore job to complete
	jobName := fmt.Sprintf("volsync-dst-%s-manual", app)
	logger.Info("Waiting for restore job %s to complete...", jobName)

	// Wait for job to appear
	for i := 0; i < 60; i++ {
		checkJob := exec.Command("kubectl", "--namespace", namespace, "get", fmt.Sprintf("job/%s", jobName))
		if err := checkJob.Run(); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Wait for completion
	cmd = exec.Command("kubectl", "--namespace", namespace, "wait",
		fmt.Sprintf("job/%s", jobName),
		"--for=condition=complete",
		fmt.Sprintf("--timeout=%ds", int(restoreTimeout.Seconds())))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restore job failed: %w", err)
	}

	// Step 6: Clean up ReplicationDestination
	logger.Info("Cleaning up...")
	cmd = exec.Command("kubectl", "--namespace", namespace, "delete", "replicationdestination", fmt.Sprintf("%s-manual", app))
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to delete ReplicationDestination: %v", err)
	}

	// Step 7: Resume Flux resources
	logger.Info("Resuming application...")

	cmd = exec.Command("flux", "--namespace", namespace, "resume", "kustomization", app)
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to resume kustomization: %v", err)
	}

	cmd = exec.Command("flux", "--namespace", namespace, "resume", "helmrelease", app)
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to resume helmrelease: %v", err)
	}

	cmd = exec.Command("flux", "--namespace", namespace, "reconcile", "helmrelease", app, "--force")
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to reconcile helmrelease: %v", err)
	}

	// Wait for pods to be ready
	logger.Info("Waiting for application to be ready...")
	cmd = exec.Command("kubectl", "--namespace", namespace, "wait", "pod",
		"--for=condition=ready", fmt.Sprintf("--selector=app.kubernetes.io/name=%s", app),
		"--timeout=5m")
	if err := cmd.Run(); err != nil {
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
			if !force && !dryRun {
				fmt.Printf("This will restore ALL applications in namespace '%s' from snapshot %s. Data will be overwritten. Continue? (y/N): ", namespace, previous)
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("restore cancelled")
				}
			}
			return restoreAllApps(namespace, previous, dryRun)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace (required)")
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

		err := restoreApp(rs.Namespace, rs.Name, previous, 120*time.Minute)
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
	cmd := exec.Command("kubectl", "--namespace", "volsync-system", "exec", kopiaPod, "--", "kopia", "snapshot", "list", "--all")
	output, err := cmd.CombinedOutput()
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
	App           string    `json:"app" yaml:"app"`
	Namespace     string    `json:"namespace" yaml:"namespace"`
	Count         int       `json:"count" yaml:"count"`
	LatestTime    string    `json:"latest_time" yaml:"latest_time"`
	LatestID      string    `json:"latest_id" yaml:"latest_id"`
	Size          string    `json:"size" yaml:"size"`
	RetentionTags string    `json:"retention_tags" yaml:"retention_tags"`
	AllSnapshots  []string  `json:"all_snapshots" yaml:"all_snapshots"`
}

func findKopiaPod() (string, error) {
	cmd := exec.Command("kubectl", "--namespace", "volsync-system", "get", "pods", "-l", "app.kubernetes.io/name=kopia", "-o", "jsonpath={.items[0].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	podName := strings.TrimSpace(string(output))
	if podName == "" {
		return "", fmt.Errorf("no kopia pod found")
	}
	return podName, nil
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
		} else if inSnapshotBlock && strings.Contains(line, "EDT") && strings.Contains(line, "k") {
			// This is a snapshot line
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				// Extract timestamp, snapshot ID, size
				timestamp := strings.Join(fields[0:4], " ") // "2025-08-20 11:44:44 EDT"
				snapshotID := fields[4]                     // "k5b070ce6951b490d1641ea00ecc2fb0b"
				size := fields[5]                           // "190.3"
				sizeUnit := fields[6]                       // "MB"
				
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
				
				// Extract the "until" timestamp if present
				untilIdx := strings.Index(line, "until ")
				if untilIdx > 0 {
					untilTime := line[untilIdx+6:]
					currentApp.AllSnapshots = append(currentApp.AllSnapshots, fmt.Sprintf("+ %d more until %s", currentApp.Count-1, untilTime))
				}
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
		fmt.Printf("%-15s %-12s %-8d %-20s %-10s %s\n",
			snap.App,
			snap.Namespace,
			snap.Count,
			snap.LatestTime[0:19], // Trim to just date and time
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
