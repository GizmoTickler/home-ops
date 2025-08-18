package volsync

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
		wait      bool
		timeout   time.Duration
		namespace string
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "snapshot-all",
		Short: "Trigger snapshots for all eligible VolSync configured PVCs",
		Long:  `Discovers all ReplicationSources across all namespaces and triggers snapshots for them`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return snapshotAllApps(namespace, wait, timeout, dryRun)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace (if empty, searches all namespaces)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for all snapshots to complete")
	cmd.Flags().DurationVar(&timeout, "timeout", 120*time.Minute, "Timeout for each snapshot completion")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be snapshotted without actually triggering snapshots")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func snapshotAllApps(namespace string, wait bool, timeout time.Duration, dryRun bool) error {
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

	// Track results
	var successful, failed []string

	// Trigger snapshots for all ReplicationSources
	for _, rs := range replicationSources {
		logger.Info("Processing %s/%s...", rs.Namespace, rs.Name)

		err := snapshotApp(rs.Namespace, rs.Name, wait, timeout)
		if err != nil {
			logger.Error("Failed to snapshot %s/%s: %v", rs.Namespace, rs.Name, err)
			failed = append(failed, fmt.Sprintf("%s/%s", rs.Namespace, rs.Name))
		} else {
			successful = append(successful, fmt.Sprintf("%s/%s", rs.Namespace, rs.Name))
		}
	}

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
		return nil, fmt.Errorf("failed to get ReplicationSources: %w\nOutput: %s", err, outputStr)
	}

	var replicationSources []ReplicationSource
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		// Split by whitespace and take first two fields
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		replicationSources = append(replicationSources, ReplicationSource{
			Namespace: fields[0],
			Name:      fields[1],
		})
	}

	return replicationSources, nil
}

func newRestoreCommand() *cobra.Command {
	var (
		namespace string
		app       string
		previous  string
		force     bool
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
			return restoreApp(namespace, app, previous)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	cmd.Flags().StringVar(&app, "app", "", "Application name (required)")
	cmd.Flags().StringVar(&previous, "previous", "", "Previous snapshot number to restore (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force restore without confirmation")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("previous")

	// Register completion functions
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)

	return cmd
}

func restoreApp(namespace, app, previous string) error {
	logger := common.NewColorLogger()

	logger.Info("Starting restore process for %s/%s from snapshot %s", namespace, app, previous)

	// Get controller type
	controller := "deployment"
	checkStatefulSet := exec.Command("kubectl", "--namespace", namespace, "get", "statefulset", app)
	if err := checkStatefulSet.Run(); err == nil {
		controller = "statefulset"
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
		"ACCESS_MODES":       "{.spec.kopia.accessModes}",
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
		"--timeout=120m")

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

		err := restoreApp(rs.Namespace, rs.Name, previous)
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
