package volsync

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
)

// VolsyncConfig holds configuration for volsync operations
type VolsyncConfig struct {
	NFSServer string
	NFSPath   string
}

// getVolsyncConfig returns configuration with defaults from environment variables
func getVolsyncConfig() *VolsyncConfig {
	return &VolsyncConfig{
		NFSServer: getEnvOrDefault("VOLSYNC_NFS_SERVER", "192.168.120.10"),
		NFSPath:   getEnvOrDefault("VOLSYNC_NFS_PATH", "/mnt/flashstor/Volsync"),
	}
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
		Long:  `Commands for managing VolSync snapshots, restores, and Restic repository operations`,
	}

	cmd.AddCommand(
		newStateCommand(),
		newSnapshotCommand(),
		newRestoreCommand(),
		newUnlockCommand(),
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
	cmd.MarkFlagRequired("action")

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
	cmd.MarkFlagRequired("app")

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
				fmt.Scanln(&response)
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
	cmd.MarkFlagRequired("app")
	cmd.MarkFlagRequired("previous")

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
		"ACCESS_MODES":        "{.spec.restic.accessModes}",
		"STORAGE_CLASS_NAME":  "{.spec.restic.storageClassName}",
		"PUID":                "{.spec.restic.moverSecurityContext.runAsUser}",
		"PGID":                "{.spec.restic.moverSecurityContext.runAsGroup}",
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
	
	// Render the template
	templatePath := filepath.Join(".taskfiles", "volsync", "resources", "replicationdestination.yaml.j2")
	if !common.FileExists(templatePath) {
		return fmt.Errorf("template not found: %s", templatePath)
	}

	// Create the ReplicationDestination YAML
	rdYAML := renderReplicationDestination(env)
	
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

func renderReplicationDestination(env map[string]string) string {
	// This is a simplified version - in production you'd use minijinja
	template := `---
apiVersion: volsync.backube/v1alpha1
kind: ReplicationDestination
metadata:
  name: %s-manual
  namespace: %s
spec:
  trigger:
    manual: restore-once
  restic:
    repository: %s-volsync-secret
    destinationPVC: %s
    copyMethod: Direct
    storageClassName: %s
    accessModes: %s
    previous: %s
    moverSecurityContext:
      runAsUser: %s
      runAsGroup: %s
      fsGroup: %s
    enableFileDeletion: true
    cleanupCachePVC: true
    cleanupTempPVC: true
`
	return fmt.Sprintf(template,
		env["APP"], env["NS"], env["APP"], env["CLAIM"],
		env["STORAGE_CLASS_NAME"], env["ACCESS_MODES"], env["PREVIOUS"],
		env["PUID"], env["PGID"], env["PGID"])
}

func newUnlockCommand() *cobra.Command {
	var (
		namespace string
		app       string
		nfsServer string
		nfsPath   string
	)

	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Unlock a Restic repository",
		Long:  `Removes stale locks from a Restic repository when a backup job was interrupted`,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := &VolsyncConfig{
				NFSServer: nfsServer,
				NFSPath:   nfsPath,
			}
			if nfsServer == "" {
				config.NFSServer = getEnvOrDefault("VOLSYNC_NFS_SERVER", "192.168.120.10")
			}
			if nfsPath == "" {
				config.NFSPath = getEnvOrDefault("VOLSYNC_NFS_PATH", "/mnt/flashstor/Volsync")
			}
			return unlockRepositoryWithConfig(namespace, app, config)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	cmd.Flags().StringVar(&app, "app", "", "Application name (required)")
	cmd.Flags().StringVar(&nfsServer, "nfs-server", "", "NFS server address (default: 192.168.120.10 or VOLSYNC_NFS_SERVER env var)")
	cmd.Flags().StringVar(&nfsPath, "nfs-path", "", "NFS path (default: /mnt/flashstor/Volsync or VOLSYNC_NFS_PATH env var)")
	cmd.MarkFlagRequired("app")

	return cmd
}

func unlockRepositoryWithConfig(namespace, app string, config *VolsyncConfig) error {
	logger := common.NewColorLogger()
	logger.Info("Unlocking repository for %s/%s...", namespace, app)

	// Create unlock job YAML
	unlockYAML := fmt.Sprintf(`---
apiVersion: batch/v1
kind: Job
metadata:
  name: volsync-unlock-%s
  namespace: %s
spec:
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      automountServiceAccountToken: false
      restartPolicy: OnFailure
      containers:
        - name: unlock
          image: docker.io/restic/restic:latest
          args: ["unlock", "--remove-all"]
          envFrom:
            - secretRef:
                name: %s-volsync-secret
          volumeMounts:
            - name: repository
              mountPath: /repository
          resources: {}
      volumes:
        - name: repository
          nfs:
            server: %s
            path: %s
`, app, namespace, app, config.NFSServer, config.NFSPath)

	// Apply the job
	cmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-")
	cmd.Stdin = bytes.NewReader([]byte(unlockYAML))
	
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create unlock job: %w\n%s", err, output)
	}

	// Wait for job to appear
	jobName := fmt.Sprintf("volsync-unlock-%s", app)
	logger.Info("Waiting for unlock job to start...")

	for i := 0; i < 30; i++ {
		checkJob := exec.Command("kubectl", "--namespace", namespace, "get", fmt.Sprintf("job/%s", jobName))
		if err := checkJob.Run(); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Wait for job to complete
	logger.Info("Waiting for unlock to complete...")
	cmd = exec.Command("kubectl", "--namespace", namespace, "wait", 
		fmt.Sprintf("job/%s", jobName), 
		"--for=condition=complete", 
		"--timeout=5m")
	
	if err := cmd.Run(); err != nil {
		logger.Warn("Unlock job may have failed: %v", err)
	}

	// Get job logs
	cmd = exec.Command("stern", "--namespace", namespace, fmt.Sprintf("job/%s", jobName), "--no-follow")
	output, _ := cmd.Output()
	if len(output) > 0 {
		logger.Info("Unlock job output:\n%s", string(output))
	}

	// Delete the job
	cmd = exec.Command("kubectl", "--namespace", namespace, "delete", "job", jobName)
	if err := cmd.Run(); err != nil {
		logger.Warn("Failed to delete unlock job: %v", err)
	}

	logger.Success("Repository unlock completed for %s/%s", namespace, app)
	return nil
}
