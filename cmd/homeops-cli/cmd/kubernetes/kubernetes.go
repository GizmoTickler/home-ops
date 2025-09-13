package kubernetes

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"context"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/logger"
	"homeops-cli/internal/kubernetes"
	esv1beta1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	cliconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	"homeops-cli/internal/flux"
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
	_ = cmd.MarkFlagRequired("claim")

	// Add completion for namespace flag
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func browsePVC(namespace, claim, image string) error {
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	// Check if PVC exists
	checkCmd := exec.Command("kubectl", "--namespace", namespace, "get", "persistentvolumeclaims", claim)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("PVC %s not found in namespace %s", claim, namespace)
	}

	// Check if kubectl browse-pvc plugin is installed
	if _, err := exec.LookPath("kubectl-browse-pvc"); err != nil {
		log.Warn("kubectl browse-pvc plugin not installed, installing via krew...")
		installCmd := exec.Command("kubectl", "krew", "install", "browse-pvc")
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install browse-pvc plugin: %w", err)
		}
	}

	log.Infof("Mounting PVC %s/%s to temporary container", namespace, claim)

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
	_ = cmd.MarkFlagRequired("node")

	// Add completion for node flag
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)

	return cmd
}

func nodeShell(node string) error {
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	// Check if node exists
	checkCmd := exec.Command("kubectl", "get", "nodes", node)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("node %s not found", node)
	}

	// Check if kubectl node-shell plugin is installed
	if _, err := exec.LookPath("kubectl-node-shell"); err != nil {
		log.Warn("kubectl node-shell plugin not installed, installing via krew...")
		installCmd := exec.Command("kubectl", "krew", "install", "node-shell")
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install node-shell plugin: %w", err)
		}
	}

	log.Infof("Opening shell to node %s", node)

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
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	s := scheme.Scheme
	if err := esv1beta1.AddToScheme(s); err != nil {
		return fmt.Errorf("failed to add external-secrets scheme: %w", err)
	}

	cfg, err := cliconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	cl, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	secrets := &esv1beta1.ExternalSecretList{}
	err = cl.List(context.Background(), secrets)
	if err != nil {
		return fmt.Errorf("failed to get ExternalSecrets: %w", err)
	}

	if len(secrets.Items) == 0 {
		log.Info("No ExternalSecrets found")
		return nil
	}

	log.Infof("Found %d ExternalSecrets to sync", len(secrets.Items))

	for _, secret := range secrets.Items {
		if dryRun {
			log.Infof("[DRY RUN] Would sync ExternalSecret %s/%s", secret.Namespace, secret.Name)
			continue
		}

		// Annotate to force sync
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}
		secret.Annotations["force-sync"] = timestamp

		if err := cl.Update(context.Background(), &secret); err != nil {
			log.Errorf("Failed to sync %s/%s: %v", secret.Namespace, secret.Name, err)
			continue
		}

		log.Infof("Synced ExternalSecret %s/%s", secret.Namespace, secret.Name)
	}

	log.Info("✅ ExternalSecrets sync completed")
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
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func cleansePods(namespace string, dryRun bool) error {
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	clientset, err := kubernetes.NewClient("") // Empty string for in-cluster config or KUBECONFIG env
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	phases := []string{string(corev1.PodFailed), string(corev1.PodPending), string(corev1.PodSucceeded)}
	totalDeleted := 0

	for _, phase := range phases {
		log.Infof("Cleaning pods in %s phase", phase)

		pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
			FieldSelector: fmt.Sprintf("status.phase=%s", phase),
		})
		if err != nil {
			log.Warnf("Failed to list pods in %s phase: %v", phase, err)
			continue
		}

		if len(pods.Items) == 0 {
			continue
		}

		if dryRun {
			log.Infof("[DRY RUN] Would delete %d pods in %s phase", len(pods.Items), phase)
			for _, pod := range pods.Items {
				log.Debugf("  %s/%s", pod.Namespace, pod.Name)
			}
		} else {
			deletedCount := 0
			for _, pod := range pods.Items {
				err := clientset.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
				if err != nil {
					log.Errorf("Failed to delete pod %s/%s: %v", pod.Namespace, pod.Name, err)
				} else {
					log.Infof("Deleted pod %s/%s", pod.Namespace, pod.Name)
					deletedCount++
				}
			}
			if deletedCount > 0 {
				log.Infof("Deleted %d pods in %s phase", deletedCount, phase)
				totalDeleted += deletedCount
			}
		}
	}

	if !dryRun {
		log.Infof("✅ Pod cleanup completed. Total pods deleted: %d", totalDeleted)
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
				_, _ = fmt.Scanln(&response)
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
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	log.Info("Starting ARC upgrade process")

	settings := cli.New()
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), "actions-runner-system", os.Getenv("HELM_DRIVER"), log.Infof); err != nil {
		return fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	// Uninstall runner
	log.Info("Uninstalling home-ops-runner...")
	uninstall := action.NewUninstall(actionConfig)
	if _, err := uninstall.Run("home-ops-runner"); err != nil && !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("failed to uninstall home-ops-runner: %w", err)
	}

	// Uninstall controller
	log.Info("Uninstalling actions-runner-controller...")
	if _, err := uninstall.Run("actions-runner-controller"); err != nil && !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("failed to uninstall actions-runner-controller: %w", err)
	}

	// Wait a bit for cleanup
	log.Info("Waiting for cleanup...")
	time.Sleep(5 * time.Second)

	// Reconcile controller
	log.Info("Reconciling actions-runner-controller HelmRelease...")
	runner := flux.NewRunner(log)
	if _, err := runner.Run("-n", "actions-runner-system", "reconcile", "hr", "actions-runner-controller"); err != nil {
		return fmt.Errorf("failed to reconcile actions-runner-controller: %w", err)
	}

	// Reconcile runner
	log.Info("Reconciling home-ops-runner HelmRelease...")
	if _, err := runner.Run("-n", "actions-runner-system", "reconcile", "hr", "home-ops-runner"); err != nil {
		return fmt.Errorf("failed to reconcile home-ops-runner: %w", err)
	}

	log.Info("✅ ARC upgrade completed successfully")
	return nil
}
