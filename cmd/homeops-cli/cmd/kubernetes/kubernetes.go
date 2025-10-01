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
		newViewSecretCommand(),
		newSyncCommand(),
		newForceSyncExternalSecretCommand(),
		newRenderKsCommand(),
		newApplyKsCommand(),
		newDeleteKsCommand(),
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
	_ = cmd.MarkFlagRequired("node")

	// Add completion for node flag
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)

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
		phases    string
	)

	cmd := &cobra.Command{
		Use:     "prune-pods",
		Aliases: []string{"cleanse-pods"},
		Short:   "Clean up pods with Failed/Pending/Succeeded phase",
		Long:    `Removes pods that are in Failed, Pending, or Succeeded states. Use --phase to specify which phases to clean.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cleansePods(namespace, phases, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be deleted without making changes")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Limit to specific namespace (default: all namespaces)")
	cmd.Flags().StringVar(&phases, "phase", "Failed,Succeeded,Pending", "Comma-separated list of pod phases to prune (Failed,Succeeded,Pending)")

	// Add completion for namespace flag
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func cleansePods(namespace string, phasesStr string, dryRun bool) error {
	logger := common.NewColorLogger()

	phases := strings.Split(phasesStr, ",")
	totalDeleted := 0

	for _, phase := range phases {
		phase = strings.TrimSpace(phase)
		if phase == "" {
			continue
		}

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

func newViewSecretCommand() *cobra.Command {
	var (
		namespace string
		format    string
		key       string
	)

	cmd := &cobra.Command{
		Use:   "view-secret <secret-name>",
		Short: "View decoded secret data",
		Long:  `Retrieves and decodes secret data, displaying it in the specified format`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return viewSecret(namespace, args[0], format, key)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")
	cmd.Flags().StringVarP(&format, "format", "o", "table", "Output format (table|json|yaml)")
	cmd.Flags().StringVarP(&key, "key", "k", "", "Specific key to view (optional)")

	// Add completion for namespace flag
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func viewSecret(namespace, secretName, format, key string) error {
	logger := common.NewColorLogger()

	// Get list of keys using go template
	listKeysCmd := exec.Command("kubectl", "get", "secret", secretName,
		"-n", namespace,
		"--template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}")
	listKeysOutput, err := listKeysCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretName, err)
	}

	keys := strings.Split(strings.TrimSpace(string(listKeysOutput)), "\n")
	if len(keys) == 0 || (len(keys) == 1 && keys[0] == "") {
		logger.Warn("Secret %s/%s has no data keys", namespace, secretName)
		return nil
	}

	decodedData := make(map[string]string)

	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}

		// Get the base64 encoded value for this key
		// Use printf to handle special characters in key names
		valueCmd := exec.Command("bash", "-c",
			fmt.Sprintf("kubectl get secret %s -n %s -o go-template='{{index .data \"%s\"}}'", secretName, namespace, k))
		encodedValue, err := valueCmd.Output()
		if err != nil {
			decodedData[k] = "<error reading value>"
			continue
		}

		// Decode the base64 value using base64 command
		decodeCmd := exec.Command("base64", "-d")
		decodeCmd.Stdin = strings.NewReader(string(encodedValue))
		decoded, err := decodeCmd.Output()
		if err != nil {
			decodedData[k] = "<error decoding>"
		} else {
			decodedData[k] = string(decoded)
		}
	}

	// Filter by key if specified
	if key != "" {
		if val, ok := decodedData[key]; ok {
			fmt.Println(val)
			return nil
		}
		return fmt.Errorf("key %s not found in secret", key)
	}

	// Display based on format
	switch format {
	case "json":
		fmt.Printf("{")
		first := true
		for k, v := range decodedData {
			if !first {
				fmt.Printf(",")
			}
			// Escape special characters in JSON
			escapedVal := strings.ReplaceAll(v, "\\", "\\\\")
			escapedVal = strings.ReplaceAll(escapedVal, "\"", "\\\"")
			escapedVal = strings.ReplaceAll(escapedVal, "\n", "\\n")
			escapedVal = strings.ReplaceAll(escapedVal, "\r", "\\r")
			escapedVal = strings.ReplaceAll(escapedVal, "\t", "\\t")
			fmt.Printf("\n  \"%s\": \"%s\"", k, escapedVal)
			first = false
		}
		fmt.Printf("\n}\n")
	case "yaml":
		for k, v := range decodedData {
			// Handle multiline values
			if strings.Contains(v, "\n") {
				fmt.Printf("%s: |\n", k)
				for _, line := range strings.Split(v, "\n") {
					fmt.Printf("  %s\n", line)
				}
			} else {
				fmt.Printf("%s: %s\n", k, v)
			}
		}
	default: // table
		logger.Info("Secret: %s/%s", namespace, secretName)
		fmt.Printf("\n")

		// For each key, show it with full value (no truncation)
		for k, v := range decodedData {
			// Check if value is multiline
			if strings.Contains(v, "\n") {
				// Multiline value - show key and indicate it's multiline
				lines := strings.Split(v, "\n")
				fmt.Printf("KEY: %s\n", k)
				fmt.Printf("VALUE (multiline, %d lines):\n", len(lines))
				fmt.Printf("%s\n", strings.Repeat("-", 80))
				fmt.Println(v)
				fmt.Printf("%s\n\n", strings.Repeat("-", 80))
			} else {
				// Single line value - show in compact format
				fmt.Printf("KEY: %s\n", k)
				fmt.Printf("VALUE: %s\n\n", v)
			}
		}
	}

	return nil
}

func newSyncCommand() *cobra.Command {
	var (
		resourceType string
		namespace    string
		parallel     bool
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Bulk sync Flux resources",
		Long:  `Reconcile multiple Flux resources of a specific type across all namespaces`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return syncFluxResources(resourceType, namespace, parallel, dryRun)
		},
	}

	cmd.Flags().StringVarP(&resourceType, "type", "t", "", "Resource type (gitrepo|helmrelease|kustomization|ocirepository) (required)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Limit to specific namespace (default: all namespaces)")
	cmd.Flags().BoolVar(&parallel, "parallel", false, "Run reconciliations in parallel (experimental)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List resources that would be synced without syncing them")
	_ = cmd.MarkFlagRequired("type")

	return cmd
}

func syncFluxResources(resourceType, namespace string, parallel, dryRun bool) error {
	logger := common.NewColorLogger()

	// Validate resource type
	validTypes := map[string]string{
		"gitrepo":       "gitrepository",
		"gitrepository": "gitrepository",
		"helmrelease":   "helmrelease",
		"hr":            "helmrelease",
		"kustomization": "kustomization",
		"ks":            "kustomization",
		"ocirepository": "ocirepository",
		"oci":           "ocirepository",
	}

	fullType, ok := validTypes[strings.ToLower(resourceType)]
	if !ok {
		return fmt.Errorf("invalid resource type: %s (valid: gitrepo, helmrelease, kustomization, ocirepository)", resourceType)
	}

	// Build kubectl command to list resources
	args := []string{"get", fullType, "-o", "jsonpath={range .items[*]}{.metadata.namespace},{.metadata.name}{\"\\n\"}{end}"}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	} else {
		args = append(args, "--all-namespaces")
	}

	cmd := exec.Command("kubectl", args...)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list %s resources: %w", fullType, err)
	}

	resources := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(resources) == 0 || (len(resources) == 1 && resources[0] == "") {
		logger.Info("No %s resources found", fullType)
		return nil
	}

	logger.Info("Found %d %s resources to sync", len(resources), fullType)

	// In dry-run mode, just list the resources
	if dryRun {
		logger.Info("[DRY RUN] Would sync the following resources:")
		for _, resource := range resources {
			if resource == "" {
				continue
			}
			parts := strings.Split(resource, ",")
			if len(parts) != 2 {
				continue
			}
			fmt.Printf("  - %s/%s\n", parts[0], parts[1])
		}
		return nil
	}

	// Ask for confirmation if syncing many resources
	if len(resources) > 5 {
		fmt.Printf("About to sync %d %s resources. Continue? (y/N): ", len(resources), fullType)
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			logger.Info("Sync cancelled")
			return nil
		}
	}

	successCount := 0
	failCount := 0

	for _, resource := range resources {
		if resource == "" {
			continue
		}

		parts := strings.Split(resource, ",")
		if len(parts) != 2 {
			logger.Warn("Invalid resource format: %s", resource)
			continue
		}

		ns := parts[0]
		name := parts[1]

		logger.Info("Syncing %s %s/%s", fullType, ns, name)

		syncCmd := exec.Command("flux", "reconcile", fullType, name, "-n", ns)
		if err := syncCmd.Run(); err != nil {
			logger.Error("Failed to sync %s/%s: %v", ns, name, err)
			failCount++
			if !parallel {
				continue
			}
		} else {
			successCount++
		}
	}

	if failCount > 0 {
		logger.Warn("Sync completed with errors. Success: %d, Failed: %d", successCount, failCount)
	} else {
		logger.Success("All %d resources synced successfully", successCount)
	}

	return nil
}

func newForceSyncExternalSecretCommand() *cobra.Command {
	var (
		namespace string
		all       bool
		timeout   int
	)

	cmd := &cobra.Command{
		Use:   "force-sync-externalsecret <name>",
		Short: "Force sync an ExternalSecret",
		Long:  `Annotates an ExternalSecret to trigger immediate synchronization`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && len(args) == 0 {
				return fmt.Errorf("either provide secret name or use --all flag")
			}
			secretName := ""
			if len(args) > 0 {
				secretName = args[0]
			}
			return forceSyncExternalSecret(namespace, secretName, all, timeout)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")
	cmd.Flags().BoolVar(&all, "all", false, "Sync all ExternalSecrets in namespace")
	cmd.Flags().IntVar(&timeout, "timeout", 60, "Timeout in seconds to wait for sync")

	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func forceSyncExternalSecret(namespace, secretName string, all bool, timeout int) error {
	logger := common.NewColorLogger()

	var secrets []string

	if all {
		// Get all ExternalSecrets in namespace
		cmd := exec.Command("kubectl", "get", "externalsecret",
			"--namespace", namespace,
			"-o", "jsonpath={.items[*].metadata.name}")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to list ExternalSecrets: %w", err)
		}
		secrets = strings.Fields(string(output))
		if len(secrets) == 0 {
			logger.Info("No ExternalSecrets found in namespace %s", namespace)
			return nil
		}
		logger.Info("Found %d ExternalSecrets to sync", len(secrets))
	} else {
		secrets = []string{secretName}
	}

	successCount := 0
	for _, name := range secrets {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		annotateCmd := exec.Command("kubectl", "--namespace", namespace,
			"annotate", "externalsecret", name,
			fmt.Sprintf("force-sync=%s", timestamp), "--overwrite")

		if err := annotateCmd.Run(); err != nil {
			logger.Error("Failed to annotate %s/%s: %v", namespace, name, err)
			continue
		}

		logger.Info("Triggered sync for ExternalSecret %s/%s", namespace, name)
		successCount++
	}

	logger.Success("Force-synced %d ExternalSecret(s)", successCount)
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

func newRenderKsCommand() *cobra.Command {
	var (
		outputFile string
	)

	cmd := &cobra.Command{
		Use:   "render-ks <ks.yaml>",
		Short: "Render a Kustomization locally using flux",
		Long:  `Builds and renders a Kustomization locally without applying to cluster. Provide path to ks.yaml file.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderKustomization(args[0], outputFile)
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write output to file instead of stdout")

	return cmd
}

func renderKustomization(ksPath, outputFile string) error {
	logger := common.NewColorLogger()

	// Check if ksPath is a file or directory
	info, err := os.Stat(ksPath)
	if err != nil {
		return fmt.Errorf("failed to access path %s: %w", ksPath, err)
	}

	var ksFile string
	if info.IsDir() {
		// If directory, look for ks.yaml
		ksFile = fmt.Sprintf("%s/ks.yaml", strings.TrimSuffix(ksPath, "/"))
		if _, err := os.Stat(ksFile); err != nil {
			return fmt.Errorf("no ks.yaml found in directory %s", ksPath)
		}
	} else {
		// If file, use it directly
		ksFile = ksPath
	}

	logger.Info("Rendering Kustomization from %s", ksFile)

	// Extract name from ks.yaml
	nameCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'name:' %s | head -1 | awk '{print $NF}'", ksFile))
	nameOutput, err := nameCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract name from ks.yaml: %w", err)
	}
	name := strings.TrimSpace(string(nameOutput))

	// Extract namespace from ks.yaml
	namespaceCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'namespace:' %s | head -1 | awk '{print $NF}'", ksFile))
	namespaceOutput, err := namespaceCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract namespace from ks.yaml: %w", err)
	}
	namespace := strings.TrimSpace(string(namespaceOutput))

	// Extract path from ks.yaml
	pathCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'path:' %s | awk '{print $2}'", ksFile))
	pathOutput, err := pathCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract path from ks.yaml: %w", err)
	}
	path := strings.TrimSpace(string(pathOutput))

	if name == "" || path == "" || namespace == "" {
		return fmt.Errorf("failed to extract name, namespace, and path from %s", ksFile)
	}

	// Find git repository root to resolve relative paths
	gitRootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	gitRootOutput, err := gitRootCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to find git repository root: %w (make sure you're in a git repository)", err)
	}
	gitRoot := strings.TrimSpace(string(gitRootOutput))

	// If path starts with ./, remove it and make it relative to git root
	if strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}

	// Construct full path relative to git root
	fullPath := fmt.Sprintf("%s/%s", gitRoot, path)

	// Verify the path exists
	if _, err := os.Stat(fullPath); err != nil {
		return fmt.Errorf("kustomization path does not exist: %s", fullPath)
	}

	// Build the kustomization using flux build with dry-run
	cmd := exec.Command("flux", "build", "kustomization", name,
		"--namespace", namespace,
		"--path", fullPath,
		"--kustomization-file", ksFile,
		"--dry-run")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to render kustomization: %w\n%s", err, string(output))
	}

	if outputFile != "" {
		// Write to file
		if err := os.WriteFile(outputFile, output, 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		logger.Success("Rendered Kustomization written to %s", outputFile)
	} else {
		// Print to stdout
		fmt.Println(string(output))
	}

	return nil
}

func newApplyKsCommand() *cobra.Command {
	var (
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "apply-ks <ks.yaml>",
		Short: "Apply a locally rendered Kustomization",
		Long:  `Renders a Kustomization locally and applies it to the cluster. Provide path to ks.yaml file.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyKustomization(args[0], dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Perform a dry-run without applying")

	return cmd
}

func applyKustomization(ksPath string, dryRun bool) error {
	logger := common.NewColorLogger()

	// Check if ksPath is a file or directory
	info, err := os.Stat(ksPath)
	if err != nil {
		return fmt.Errorf("failed to access path %s: %w", ksPath, err)
	}

	var ksFile string
	if info.IsDir() {
		// If directory, look for ks.yaml
		ksFile = fmt.Sprintf("%s/ks.yaml", strings.TrimSuffix(ksPath, "/"))
		if _, err := os.Stat(ksFile); err != nil {
			return fmt.Errorf("no ks.yaml found in directory %s", ksPath)
		}
	} else {
		// If file, use it directly
		ksFile = ksPath
	}

	logger.Info("Rendering Kustomization from %s", ksFile)

	// Extract name from ks.yaml
	nameCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'name:' %s | head -1 | awk '{print $NF}'", ksFile))
	nameOutput, err := nameCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract name from ks.yaml: %w", err)
	}
	name := strings.TrimSpace(string(nameOutput))

	// Extract namespace from ks.yaml
	namespaceCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'namespace:' %s | head -1 | awk '{print $NF}'", ksFile))
	namespaceOutput, err := namespaceCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract namespace from ks.yaml: %w", err)
	}
	namespace := strings.TrimSpace(string(namespaceOutput))

	// Extract path from ks.yaml
	pathCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'path:' %s | awk '{print $2}'", ksFile))
	pathOutput, err := pathCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract path from ks.yaml: %w", err)
	}
	path := strings.TrimSpace(string(pathOutput))

	if name == "" || path == "" || namespace == "" {
		return fmt.Errorf("failed to extract name, namespace, and path from %s", ksFile)
	}

	// Find git repository root to resolve relative paths
	gitRootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	gitRootOutput, err := gitRootCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to find git repository root: %w (make sure you're in a git repository)", err)
	}
	gitRoot := strings.TrimSpace(string(gitRootOutput))

	// If path starts with ./, remove it and make it relative to git root
	if strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}

	// Construct full path relative to git root
	fullPath := fmt.Sprintf("%s/%s", gitRoot, path)

	// Verify the path exists
	if _, err := os.Stat(fullPath); err != nil {
		return fmt.Errorf("kustomization path does not exist: %s", fullPath)
	}

	// Build the kustomization using flux build with dry-run
	buildCmd := exec.Command("flux", "build", "kustomization", name,
		"--namespace", namespace,
		"--path", fullPath,
		"--kustomization-file", ksFile,
		"--dry-run")

	output, err := buildCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to render kustomization: %w", err)
	}

	if dryRun {
		logger.Info("[DRY RUN] Would apply the following resources:")
		fmt.Println(string(output))
		return nil
	}

	// Apply the rendered output
	logger.Info("Applying Kustomization")
	applyCmd := exec.Command("kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(string(output))
	applyCmd.Stdout = os.Stdout
	applyCmd.Stderr = os.Stderr

	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("failed to apply kustomization: %w", err)
	}

	logger.Success("Kustomization applied successfully")
	return nil
}

func newDeleteKsCommand() *cobra.Command {
	var (
		force bool
	)

	cmd := &cobra.Command{
		Use:   "delete-ks <ks.yaml>",
		Short: "Delete resources from a locally rendered Kustomization",
		Long:  `Renders a Kustomization locally and deletes its resources from the cluster. Provide path to ks.yaml file.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Print("This will delete all resources in the Kustomization. Continue? (y/N): ")
				var response string
				_, _ = fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("deletion cancelled")
				}
			}
			return deleteKustomization(args[0])
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")

	return cmd
}

func deleteKustomization(ksPath string) error {
	logger := common.NewColorLogger()

	// Check if ksPath is a file or directory
	info, err := os.Stat(ksPath)
	if err != nil {
		return fmt.Errorf("failed to access path %s: %w", ksPath, err)
	}

	var ksFile string
	if info.IsDir() {
		// If directory, look for ks.yaml
		ksFile = fmt.Sprintf("%s/ks.yaml", strings.TrimSuffix(ksPath, "/"))
		if _, err := os.Stat(ksFile); err != nil {
			return fmt.Errorf("no ks.yaml found in directory %s", ksPath)
		}
	} else {
		// If file, use it directly
		ksFile = ksPath
	}

	logger.Info("Rendering Kustomization from %s", ksFile)

	// Extract name from ks.yaml
	nameCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'name:' %s | head -1 | awk '{print $NF}'", ksFile))
	nameOutput, err := nameCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract name from ks.yaml: %w", err)
	}
	name := strings.TrimSpace(string(nameOutput))

	// Extract namespace from ks.yaml
	namespaceCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'namespace:' %s | head -1 | awk '{print $NF}'", ksFile))
	namespaceOutput, err := namespaceCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract namespace from ks.yaml: %w", err)
	}
	namespace := strings.TrimSpace(string(namespaceOutput))

	// Extract path from ks.yaml
	pathCmd := exec.Command("bash", "-c",
		fmt.Sprintf("grep 'path:' %s | awk '{print $2}'", ksFile))
	pathOutput, err := pathCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to extract path from ks.yaml: %w", err)
	}
	path := strings.TrimSpace(string(pathOutput))

	if name == "" || path == "" || namespace == "" {
		return fmt.Errorf("failed to extract name, namespace, and path from %s", ksFile)
	}

	// Find git repository root to resolve relative paths
	gitRootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	gitRootOutput, err := gitRootCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to find git repository root: %w (make sure you're in a git repository)", err)
	}
	gitRoot := strings.TrimSpace(string(gitRootOutput))

	// If path starts with ./, remove it and make it relative to git root
	if strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}

	// Construct full path relative to git root
	fullPath := fmt.Sprintf("%s/%s", gitRoot, path)

	// Verify the path exists
	if _, err := os.Stat(fullPath); err != nil {
		return fmt.Errorf("kustomization path does not exist: %s", fullPath)
	}

	// Build the kustomization using flux build with dry-run
	buildCmd := exec.Command("flux", "build", "kustomization", name,
		"--namespace", namespace,
		"--path", fullPath,
		"--kustomization-file", ksFile,
		"--dry-run")

	output, err := buildCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to render kustomization: %w", err)
	}

	// Delete the rendered output
	logger.Info("Deleting resources from Kustomization")
	deleteCmd := exec.Command("kubectl", "delete", "-f", "-")
	deleteCmd.Stdin = strings.NewReader(string(output))
	deleteCmd.Stdout = os.Stdout
	deleteCmd.Stderr = os.Stderr

	if err := deleteCmd.Run(); err != nil {
		return fmt.Errorf("failed to delete kustomization resources: %w", err)
	}

	logger.Success("Kustomization resources deleted successfully")
	return nil
}
