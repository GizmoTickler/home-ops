package kubernetes

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k8s",
		Short: "Kubernetes cluster management commands",
		Long:  `Commands for managing Kubernetes resources, PVCs, nodes, and secrets`,
	}

	cmd.AddCommand(
		newBrowsePVCCommand(),
		newNodeShellCommand(),
		newSyncSecretsCommand(),
		newCleansePodsCommand(),
		newUpgradeARCCommand(),
	)

	return cmd
}

func newBrowsePVCCommand() *cobra.Command {
	var (
		namespace string
		claim     string
		image     string
	)

	cmd := &cobra.Command{
		Use:   "browse-pvc",
		Short: "Mount a PVC to a temporary container for browsing",
		Long:  `Creates a temporary pod with the specified PVC mounted for interactive browsing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return browsePVC(namespace, claim, image)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	cmd.Flags().StringVar(&claim, "claim", "", "PVC name (required)")
	cmd.Flags().StringVar(&image, "image", "docker.io/library/alpine:latest", "Container image to use")
	cmd.MarkFlagRequired("claim")

	// Add completion for namespace flag
	if err := cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces); err != nil {
		// Silently ignore completion registration errors
	}

	return cmd
}

func browsePVC(namespace, claim, image string) error {
	logger := common.NewColorLogger()

	// Check if PVC exists
	checkCmd := exec.Command("kubectl", "--namespace", namespace, "get", "persistentvolumeclaims", claim)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("PVC %s not found in namespace %s", claim, namespace)
	}

	// Check if kubectl browse-pvc plugin is installed
	if _, err := exec.LookPath("kubectl-browse-pvc"); err != nil {
		logger.Warn("kubectl browse-pvc plugin not installed, installing via krew...")
		installCmd := exec.Command("kubectl", "krew", "install", "browse-pvc")
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install browse-pvc plugin: %w", err)
		}
	}

	logger.Info("Mounting PVC %s/%s to temporary container", namespace, claim)

	// Execute browse-pvc
	cmd := exec.Command("kubectl", "browse-pvc", 
		"--namespace", namespace,
		"--image", image,
		claim)
	
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func newNodeShellCommand() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "node-shell",
		Short: "Open a shell to a Kubernetes node",
		Long:  `Creates a privileged pod on the specified node for debugging`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nodeShell(node)
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Node name (required)")
	cmd.MarkFlagRequired("node")

	// Add completion for node flag - could be enhanced to get actual node names
	if err := cmd.RegisterFlagCompletionFunc("node", cobra.NoFileCompletions); err != nil {
		// Silently ignore completion registration errors
	}

	return cmd
}

func nodeShell(node string) error {
	logger := common.NewColorLogger()

	// Check if node exists
	checkCmd := exec.Command("kubectl", "get", "nodes", node)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("node %s not found", node)
	}

	// Check if kubectl node-shell plugin is installed
	if _, err := exec.LookPath("kubectl-node-shell"); err != nil {
		logger.Warn("kubectl node-shell plugin not installed, installing via krew...")
		installCmd := exec.Command("kubectl", "krew", "install", "node-shell")
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install node-shell plugin: %w", err)
		}
	}

	logger.Info("Opening shell to node %s", node)

	// Execute node-shell
	cmd := exec.Command("kubectl", "node-shell", "-n", "kube-system", "-x", node)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func newSyncSecretsCommand() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sync-secrets",
		Short: "Sync all ExternalSecrets",
		Long:  `Forces a sync of all ExternalSecrets across all namespaces`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return syncSecrets(dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be synced without making changes")

	return cmd
}

func syncSecrets(dryRun bool) error {
	logger := common.NewColorLogger()

	// Get all ExternalSecrets
	cmd := exec.Command("kubectl", "get", "externalsecret", "--all-namespaces", 
		"--no-headers", "--output=jsonpath={range .items[*]}{.metadata.namespace},{.metadata.name}{\"\\n\"}{end}")
	
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get ExternalSecrets: %w", err)
	}

	secrets := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(secrets) == 0 || (len(secrets) == 1 && secrets[0] == "") {
		logger.Info("No ExternalSecrets found")
		return nil
	}

	logger.Info("Found %d ExternalSecrets to sync", len(secrets))

	// Sync each secret
	for _, secret := range secrets {
		if secret == "" {
			continue
		}

		parts := strings.Split(secret, ",")
		if len(parts) != 2 {
			logger.Warn("Invalid secret format: %s", secret)
			continue
		}

		namespace := parts[0]
		name := parts[1]

		if dryRun {
			logger.Info("[DRY RUN] Would sync ExternalSecret %s/%s", namespace, name)
			continue
		}

		// Annotate to force sync
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		annotateCmd := exec.Command("kubectl", "--namespace", namespace, 
			"annotate", "externalsecret", name, 
			fmt.Sprintf("force-sync=%s", timestamp), "--overwrite")
		
		if err := annotateCmd.Run(); err != nil {
			logger.Error("Failed to sync %s/%s: %v", namespace, name, err)
			continue
		}

		logger.Info("Synced ExternalSecret %s/%s", namespace, name)
	}

	logger.Success("ExternalSecrets sync completed")
	return nil
}

func newCleansePodsCommand() *cobra.Command {
	var (
		dryRun    bool
		namespace string
	)

	cmd := &cobra.Command{
		Use:   "cleanse-pods",
		Short: "Clean up pods with Failed/Pending/Succeeded phase",
		Long:  `Removes pods that are in Failed, Pending, or Succeeded states`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cleansePods(namespace, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be deleted without making changes")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Limit to specific namespace (default: all namespaces)")

	// Add completion for namespace flag
	if err := cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces); err != nil {
		// Silently ignore completion registration errors
	}

	return cmd
}

func cleansePods(namespace string, dryRun bool) error {
	logger := common.NewColorLogger()

	phases := []string{"Failed", "Pending", "Succeeded"}
	totalDeleted := 0

	for _, phase := range phases {
		logger.Info("Cleaning pods in %s phase", phase)

		// Build kubectl command
		args := []string{"delete", "pods"}
		if namespace != "" {
			args = append(args, "--namespace", namespace)
		} else {
			args = append(args, "--all-namespaces")
		}
		args = append(args, "--field-selector", fmt.Sprintf("status.phase=%s", phase))
		
		if dryRun {
			// First get the list of pods that would be deleted
			listArgs := []string{"get", "pods"}
			if namespace != "" {
				listArgs = append(listArgs, "--namespace", namespace)
			} else {
				listArgs = append(listArgs, "--all-namespaces")
			}
			listArgs = append(listArgs, "--field-selector", fmt.Sprintf("status.phase=%s", phase), "-o", "name")
			
			listCmd := exec.Command("kubectl", listArgs...)
			output, err := listCmd.Output()
			if err != nil {
				logger.Warn("Failed to list pods in %s phase: %v", phase, err)
				continue
			}

			pods := strings.Split(strings.TrimSpace(string(output)), "\n")
			if len(pods) > 0 && pods[0] != "" {
				logger.Info("[DRY RUN] Would delete %d pods in %s phase", len(pods), phase)
				for _, pod := range pods {
					if pod != "" {
						logger.Debug("  %s", pod)
					}
				}
			}
		} else {
			args = append(args, "--ignore-not-found=true")
			
			cmd := exec.Command("kubectl", args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				logger.Error("Failed to delete pods in %s phase: %v", phase, err)
				continue
			}

			// Count deleted pods from output
			lines := strings.Split(string(output), "\n")
			deleted := 0
			for _, line := range lines {
				if strings.Contains(line, "deleted") {
					deleted++
				}
			}
			
			if deleted > 0 {
				logger.Info("Deleted %d pods in %s phase", deleted, phase)
				totalDeleted += deleted
			}
		}
	}

	if !dryRun {
		logger.Success("Pod cleanup completed. Total pods deleted: %d", totalDeleted)
	}
	
	return nil
}

func newUpgradeARCCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "upgrade-arc",
		Short: "Upgrade the Actions Runner Controller",
		Long:  `Uninstalls and reinstalls the Actions Runner Controller to upgrade it`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Print("This will uninstall and reinstall ARC. Continue? (y/N): ")
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("upgrade cancelled")
				}
			}
			return upgradeARC()
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force upgrade without confirmation")

	return cmd
}

func upgradeARC() error {
	logger := common.NewColorLogger()

	logger.Info("Starting ARC upgrade process")

	// Uninstall runner
	logger.Info("Uninstalling home-ops-runner...")
	cmd := exec.Command("helm", "-n", "actions-runner-system", "uninstall", "home-ops-runner")
	if output, err := cmd.CombinedOutput(); err != nil {
		// It might not exist, which is okay
		if !strings.Contains(string(output), "not found") {
			return fmt.Errorf("failed to uninstall home-ops-runner: %w\n%s", err, output)
		}
	}

	// Uninstall controller
	logger.Info("Uninstalling actions-runner-controller...")
	cmd = exec.Command("helm", "-n", "actions-runner-system", "uninstall", "actions-runner-controller")
	if output, err := cmd.CombinedOutput(); err != nil {
		// It might not exist, which is okay
		if !strings.Contains(string(output), "not found") {
			return fmt.Errorf("failed to uninstall actions-runner-controller: %w\n%s", err, output)
		}
	}

	// Wait a bit for cleanup
	logger.Info("Waiting for cleanup...")
	time.Sleep(5 * time.Second)

	// Reconcile controller
	logger.Info("Reconciling actions-runner-controller HelmRelease...")
	cmd = exec.Command("flux", "-n", "actions-runner-system", "reconcile", "hr", "actions-runner-controller")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to reconcile actions-runner-controller: %w", err)
	}

	// Reconcile runner
	logger.Info("Reconciling home-ops-runner HelmRelease...")
	cmd = exec.Command("flux", "-n", "actions-runner-system", "reconcile", "hr", "home-ops-runner")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to reconcile home-ops-runner: %w", err)
	}

	logger.Success("ARC upgrade completed successfully")
	return nil
}
