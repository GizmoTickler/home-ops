package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

// KustomizationInfo contains parsed information from a ks.yaml file
type KustomizationInfo struct {
	Name      string
	Namespace string
	Path      string
	FullPath  string
	KsFile    string
	GitRoot   string
}

var (
	chooseOptionFn      = ui.Choose
	selectNamespaceFn   = ui.SelectNamespace
	chooseMultiOptionFn = ui.ChooseMulti
	filterOptionFn      = ui.Filter
	confirmActionFn     = ui.Confirm
	spinWithOutputFn    = ui.SpinWithOutput
	spinWithFuncFn      = ui.SpinWithFunc
	lookPathFn          = common.LookPath
	kubectlOutputFn     = func(args ...string) ([]byte, error) {
		return common.Output("kubectl", args...)
	}
	kubectlRunFn = func(args ...string) error {
		return common.Command("kubectl", args...).Run()
	}
	kubectlRunInteractiveFn = func(args ...string) error {
		return common.RunInteractive(os.Stdin, os.Stdout, os.Stderr, "kubectl", args...)
	}
	installKubectlPluginFn = func(plugin string) error {
		return common.Command("kubectl", "krew", "install", plugin).Run()
	}
	commandOutputFn = func(name string, args ...string) ([]byte, error) {
		return runKubernetesCommandOutput(name, args...)
	}
	commandRunFn = func(name string, args ...string) error {
		return runKubernetesCommandRun(name, args...)
	}
	commandCombinedOutputFn = func(name string, args ...string) ([]byte, error) {
		return runKubernetesCommandCombinedOutput(name, args...)
	}
	decodeBase64Fn = func(value string) ([]byte, error) {
		return decodeBase64(value)
	}
	isStdoutTerminalFn = func() bool {
		info, err := os.Stdout.Stat()
		return err == nil && (info.Mode()&os.ModeCharDevice) != 0
	}
	fluxBuildKustomizationFn = func(name, namespace, path, ksFile string) ([]byte, error) {
		return runKubernetesCommandOutput("flux", "build", "kustomization", name,
			"--namespace", namespace,
			"--path", path,
			"--kustomization-file", ksFile,
			"--dry-run")
	}

	// manifestStdout/manifestStderr let tests capture output produced by the default
	// apply/delete manifest implementations without mutating os.Stdout/os.Stderr.
	manifestStdout io.Writer = os.Stdout
	manifestStderr io.Writer = os.Stderr

	kubectlApplyManifestFn = func(manifest string) error {
		return runManifestCommand("apply", manifest)
	}
	kubectlDeleteManifestFn = func(manifest string) error {
		return runManifestCommand("delete", manifest)
	}
	nowFn   = time.Now
	sleepFn = time.Sleep
)

const kubernetesDefaultCommandTimeout = 5 * time.Minute

// runManifestCommand pipes the manifest into `kubectl <action> -f -` while routing
// output through the package-level writers so tests can intercept it.
func runManifestCommand(action, manifest string) error {
	cmd := common.Command("kubectl", action, "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	cmd.Stdout = manifestStdout
	cmd.Stderr = manifestStderr
	return cmd.Run()
}

func runKubernetesCommandOutput(name string, args ...string) ([]byte, error) {
	result, err := runKubernetesCommand(name, args...)
	return []byte(result.Stdout), redactKubernetesCommandError(err, result.Stdout, result.Stderr)
}

func runKubernetesCommandCombinedOutput(name string, args ...string) ([]byte, error) {
	result, err := runKubernetesCommand(name, args...)
	return []byte(result.Stdout + result.Stderr), redactKubernetesCommandError(err, result.Stdout, result.Stderr)
}

func runKubernetesCommandRun(name string, args ...string) error {
	result, err := runKubernetesCommand(name, args...)
	return redactKubernetesCommandError(err, result.Stdout, result.Stderr)
}

func runKubernetesCommand(name string, args ...string) (common.CommandResult, error) {
	return common.RunCommand(context.Background(), common.CommandOptions{
		Name:    name,
		Args:    args,
		Timeout: kubernetesDefaultCommandTimeout,
	})
}

// redactKubernetesCommandError wraps the underlying error with the redacted command
// output. Returns nil when there is no error to surface.
func redactKubernetesCommandError(err error, stdout, stderr string) error {
	if err == nil {
		return nil
	}
	output := strings.TrimSpace(strings.Join(nonEmptyKubernetesStrings(stdout, stderr), "\n"))
	if output == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, output)
}

func nonEmptyKubernetesStrings(values ...string) []string {
	var nonEmpty []string
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			nonEmpty = append(nonEmpty, v)
		}
	}
	return nonEmpty
}

// decodeBase64 decodes a possibly padded base64 string using the standard library.
// kubectl jsonpath output may include surrounding whitespace; trim it before decoding.
func decodeBase64(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		return decoded, nil
	}
	// Fall back to URL-encoded variant — Kubernetes generally uses StdEncoding but
	// some tooling emits the URL-safe alphabet.
	return base64.URLEncoding.DecodeString(trimmed)
}

func resolveKustomizationFilePath(ksPath string) (string, error) {
	info, err := os.Stat(ksPath)
	if err != nil {
		return "", fmt.Errorf("failed to access path %s: %w", ksPath, err)
	}

	if info.IsDir() {
		ksFile := fmt.Sprintf("%s/ks.yaml", strings.TrimSuffix(ksPath, "/"))
		if _, err := os.Stat(ksFile); err != nil {
			return "", fmt.Errorf("no ks.yaml found in directory %s", ksPath)
		}
		return ksFile, nil
	}
	return ksPath, nil
}

func findGitRoot() (string, error) {
	if gitRoot, err := common.FindGitRoot("."); err == nil {
		return gitRoot, nil
	}

	gitRootOutput, err := common.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("failed to find git repository root: %w (make sure you're in a git repository)", err)
	}
	return strings.TrimSpace(string(gitRootOutput)), nil
}

func findKustomizationFiles(appsDir string) ([]string, error) {
	var ksFiles []string

	err := filepath.WalkDir(appsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "ks.yaml" {
			ksFiles = append(ksFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find ks.yaml files: %w", err)
	}

	sort.Strings(ksFiles)
	return ksFiles, nil
}

func parseKustomizationDocuments(ksPath string) ([]KustomizationInfo, error) {
	ksFile, err := resolveKustomizationFilePath(ksPath)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(ksFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read ks.yaml: %w", err)
	}

	type kustomizationDocument struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Spec struct {
			TargetNamespace string `yaml:"targetNamespace"`
			Path            string `yaml:"path"`
		} `yaml:"spec"`
	}

	gitRoot, err := findGitRoot()
	if err != nil {
		return nil, err
	}

	var infos []KustomizationInfo
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc kustomizationDocument
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("failed to parse ks.yaml: %w", err)
		}
		if doc.Kind != "Kustomization" {
			continue
		}
		name := strings.TrimSpace(doc.Metadata.Name)
		namespace := strings.TrimSpace(doc.Spec.TargetNamespace)
		if namespace == "" {
			namespace = strings.TrimSpace(doc.Metadata.Namespace)
		}
		path := strings.TrimSpace(doc.Spec.Path)
		if name == "" || namespace == "" || path == "" {
			continue
		}

		path = strings.TrimPrefix(path, "./")
		fullPath := fmt.Sprintf("%s/%s", gitRoot, path)
		if _, err := os.Stat(fullPath); err != nil {
			return nil, fmt.Errorf("kustomization path does not exist: %s", fullPath)
		}

		infos = append(infos, KustomizationInfo{
			Name:      name,
			Namespace: namespace,
			Path:      path,
			FullPath:  fullPath,
			KsFile:    ksFile,
			GitRoot:   gitRoot,
		})
	}

	if len(infos) == 0 {
		return nil, fmt.Errorf("failed to extract any complete Kustomization documents from %s", ksFile)
	}

	return infos, nil
}

// parseKustomizationFile parses a ks.yaml file and extracts a single kustomization target.
// ksPath can be either a path to a ks.yaml file or a directory containing one.
func parseKustomizationFile(ksPath string) (*KustomizationInfo, error) {
	return parseKustomizationFileWithSelector(ksPath, "")
}

func parseKustomizationFileWithSelector(ksPath, targetName string) (*KustomizationInfo, error) {
	infos, err := parseKustomizationDocuments(ksPath)
	if err != nil {
		return nil, err
	}

	if targetName != "" {
		for _, info := range infos {
			if info.Name == targetName {
				infoCopy := info
				return &infoCopy, nil
			}
		}
		return nil, fmt.Errorf("kustomization %q not found in %s", targetName, infos[0].KsFile)
	}

	if len(infos) > 1 {
		var names []string
		for _, info := range infos {
			names = append(names, info.Name)
		}
		return nil, fmt.Errorf("multiple kustomizations found in %s: %s (use --name to select one)", infos[0].KsFile, strings.Join(names, ", "))
	}

	info := infos[0]
	return &info, nil
}

func selectKubectlResource(namespace, resourceType, prompt string) (string, error) {
	output, err := kubectlOutputFn("get", resourceType, "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return "", fmt.Errorf("failed to get %s in namespace %s: %w", resourceType, namespace, err)
	}

	resources := strings.Fields(string(output))
	if len(resources) == 0 {
		return "", fmt.Errorf("no %s found in namespace %s", strings.ToUpper(resourceType), namespace)
	}

	selected, err := chooseOptionFn(prompt, resources)
	if err != nil {
		if ui.IsCancellation(err) {
			return "", nil
		}
		return "", err
	}

	return selected, nil
}

func ensureKubectlPlugin(binaryName, pluginName string) error {
	if _, err := lookPathFn(binaryName); err == nil {
		return nil
	}

	if err := installKubectlPluginFn(pluginName); err != nil {
		return fmt.Errorf("failed to install %s plugin: %w", pluginName, err)
	}

	return nil
}

func forceDeletePodsWithPrefix(namespace, prefix string, logger *common.ColorLogger) error {
	output, err := kubectlOutputFn("get", "pods", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil
	}

	pods := strings.Fields(string(output))
	var matched []string
	for _, pod := range pods {
		if strings.HasPrefix(pod, prefix) {
			matched = append(matched, pod)
		}
	}

	if len(matched) == 0 {
		logger.Info("Temporary browse pod cleaned up successfully")
		return nil
	}

	var failed []string
	for _, pod := range matched {
		logger.Warn("Browse pod %s still exists, forcing deletion...", pod)
		if err := kubectlRunFn("delete", "pod", pod, "-n", namespace, "--force", "--grace-period=0"); err != nil {
			logger.Error("Failed to delete pod %s: %v", pod, err)
			failed = append(failed, pod)
			continue
		}
		logger.Info("Successfully deleted pod %s", pod)
	}

	if len(failed) > 0 {
		return fmt.Errorf("failed to delete browse pod(s): %s", strings.Join(failed, ", "))
	}

	return nil
}

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
		Long:  `Creates a temporary pod with the specified PVC mounted for interactive browsing. If --claim is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return browsePVC(namespace, claim, image)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	cmd.Flags().StringVar(&claim, "claim", "", "PVC name (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&image, "image", "docker.io/library/alpine:latest", "Container image to use")

	// Add completion for namespace flag
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func browsePVC(namespace, claim, image string) error {
	logger := common.NewColorLogger()

	// If claim is not provided, prompt for selection
	if claim == "" {
		selectedPVC, err := selectKubectlResource(namespace, "pvc", fmt.Sprintf("Select a PVC from namespace %s:", namespace))
		if err != nil {
			return fmt.Errorf("PVC selection failed: %w", err)
		}
		if selectedPVC == "" {
			return nil
		}
		claim = selectedPVC
	}

	// Check if PVC exists
	if err := kubectlRunFn("--namespace", namespace, "get", "persistentvolumeclaims", claim); err != nil {
		return fmt.Errorf("PVC %s not found in namespace %s", claim, namespace)
	}

	// Check if kubectl browse-pvc plugin is installed
	if _, err := lookPathFn("kubectl-browse-pvc"); err != nil {
		logger.Warn("kubectl browse-pvc plugin not installed, installing via krew...")
		if err := ensureKubectlPlugin("kubectl-browse-pvc", "browse-pvc"); err != nil {
			return err
		}
	}

	logger.Info("Mounting PVC %s/%s to temporary container", namespace, claim)

	// Execute browse-pvc
	if err := kubectlRunInteractiveFn("browse-pvc",
		"--namespace", namespace,
		"--image", image,
		claim); err != nil {
		return err
	}

	// Verify cleanup - check if any browse pods still exist and force delete them
	sleepFn(500 * time.Millisecond) // Brief delay to let k8s finalize deletion
	return forceDeletePodsWithPrefix(namespace, "browse-", logger)
}

func newNodeShellCommand() *cobra.Command {
	var node string

	cmd := &cobra.Command{
		Use:   "node-shell",
		Short: "Open a shell to a Kubernetes node",
		Long:  `Creates a privileged pod on the specified node for debugging. If --node is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nodeShell(node)
		},
	}

	cmd.Flags().StringVar(&node, "node", "", "Node name (optional - will prompt if not provided)")

	// Add completion for node flag
	_ = cmd.RegisterFlagCompletionFunc("node", completion.ValidNodeNames)

	return cmd
}

func nodeShell(node string) error {
	logger := common.NewColorLogger()

	// If node is not provided, prompt for selection
	if node == "" {
		output, err := kubectlOutputFn("get", "nodes", "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return fmt.Errorf("failed to get nodes: %w", err)
		}

		nodes := strings.Fields(string(output))
		if len(nodes) == 0 {
			return fmt.Errorf("no nodes found in cluster")
		}

		// Use interactive selector
		selectedNode, err := chooseOptionFn("Select a node:", nodes)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("node selection failed: %w", err)
		}
		node = selectedNode
	}

	// Check if node exists
	if err := kubectlRunFn("get", "nodes", node); err != nil {
		return fmt.Errorf("node %s not found", node)
	}

	// Check if kubectl node-shell plugin is installed
	if _, err := lookPathFn("kubectl-node-shell"); err != nil {
		logger.Warn("kubectl node-shell plugin not installed, installing via krew...")
		if err := ensureKubectlPlugin("kubectl-node-shell", "node-shell"); err != nil {
			return err
		}
	}

	logger.Info("Opening shell to node %s", node)

	// Execute node-shell
	return kubectlRunInteractiveFn("node-shell", "-n", "kube-system", "-x", node)
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
	output, err := commandOutputFn("kubectl", "get", "externalsecret", "--all-namespaces",
		"--no-headers", "--output=jsonpath={range .items[*]}{.metadata.namespace},{.metadata.name}{\"\\n\"}{end}")
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
		timestamp := fmt.Sprintf("%d", nowFn().Unix())
		if err := commandRunFn("kubectl", "--namespace", namespace,
			"annotate", "externalsecret", name,
			fmt.Sprintf("force-sync=%s", timestamp), "--overwrite"); err != nil {
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
		Long:    `Removes pods that are in Failed, Pending, or Succeeded states. Use --phase to specify which phases to clean. If not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cleansePods(namespace, phases, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be deleted without making changes")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Limit to specific namespace (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&phases, "phase", "", "Comma-separated list of pod phases to prune (Failed,Succeeded,Pending) (optional - will prompt if not provided)")

	// Add completion for namespace flag
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func cleansePods(namespace string, phasesStr string, dryRun bool) error {
	logger := common.NewColorLogger()

	// If namespace is not provided, prompt for selection
	if namespace == "" {
		selectedNS, err := selectNamespaceFn("Select namespace:", true)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return err
		}
		namespace = selectedNS // Empty string means "all namespaces"
	}

	// If phases are not provided, prompt for selection
	var phases []string
	if phasesStr == "" {
		phaseOptions := []string{"Failed", "Succeeded", "Completed", "Pending"}
		selectedPhases, err := chooseMultiOptionFn("Select pod phases to prune (use 'x' to toggle, Enter to confirm):", phaseOptions, 0)
		if err != nil {
			// User cancelled selection
			return nil
		}
		if len(selectedPhases) == 0 {
			// User didn't select anything (didn't press 'x' to toggle)
			logger.Warn("No phases selected. Press 'x' to toggle selection before pressing Enter.")
			return nil
		}
		logger.Debug("Selected phases from gum: %v", selectedPhases)
		phases = selectedPhases
	} else {
		phases = strings.Split(phasesStr, ",")
	}

	totalDeleted := 0

	for _, phase := range phases {
		phase = strings.TrimSpace(phase)
		if phase == "" {
			continue
		}

		// Normalize phase to title case (e.g., "succeeded" -> "Succeeded")
		if len(phase) > 0 {
			phase = strings.ToUpper(phase[:1]) + strings.ToLower(phase[1:])
		}

		// Map user-friendly "Completed" to Kubernetes phase "Succeeded"
		actualPhase := phase
		if phase == "Completed" {
			actualPhase = "Succeeded"
		}

		logger.Info("Cleaning pods in %s phase", phase)

		// Build kubectl command
		args := []string{"delete", "pods"}
		if namespace != "" {
			args = append(args, "--namespace", namespace)
		} else {
			args = append(args, "--all-namespaces")
		}
		args = append(args, "--field-selector", fmt.Sprintf("status.phase=%s", actualPhase))

		if dryRun {
			// First get the list of pods that would be deleted
			listArgs := []string{"get", "pods"}
			if namespace != "" {
				listArgs = append(listArgs, "--namespace", namespace)
			} else {
				listArgs = append(listArgs, "--all-namespaces")
			}
			listArgs = append(listArgs, "--field-selector", fmt.Sprintf("status.phase=%s", actualPhase), "-o", "name")

			output, err := commandOutputFn("kubectl", listArgs...)
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

			logger.Debug("Running: kubectl %s", strings.Join(args, " "))
			output, err := commandCombinedOutputFn("kubectl", args...)
			if err != nil {
				logger.Error("Failed to delete pods in %s phase: %v\nOutput: %s", phase, err, string(output))
				continue
			}

			outputStr := strings.TrimSpace(string(output))
			logger.Debug("Output: %s", outputStr)

			// Count deleted pods from output
			lines := strings.Split(outputStr, "\n")
			deleted := 0
			for _, line := range lines {
				if strings.Contains(line, "deleted") {
					deleted++
					logger.Info("  %s", line)
				}
			}

			if deleted > 0 {
				logger.Info("Deleted %d pods in %s phase", deleted, phase)
				totalDeleted += deleted
			} else if outputStr != "" {
				logger.Info("No pods found in %s phase to delete", phase)
			} else {
				logger.Info("No pods in %s phase", phase)
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
		namespace                    string
		format                       string
		key                          string
		secretName                   string
		unsafeRevealValues           bool
		unsafeAcknowledgePrintSecret bool
		unsafeForceNonTTY            bool
	)

	cmd := &cobra.Command{
		Use:   "view-secret [secret-name]",
		Short: "View safe secret metadata",
		Long:  `Retrieves secret data and displays safe metadata by default. If secret name is not provided, presents an interactive selector.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				secretName = args[0]
			}
			return viewSecretWithOptions(namespace, secretName, format, key, viewSecretOptions{
				unsafeRevealValues:           unsafeRevealValues,
				unsafeAcknowledgePrintSecret: unsafeAcknowledgePrintSecret,
				unsafeForceNonTTY:            unsafeForceNonTTY,
			})
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")
	cmd.Flags().StringVarP(&format, "format", "o", "table", "Output format (table|json|yaml)")
	cmd.Flags().StringVarP(&key, "key", "k", "", "Specific key to inspect (optional)")
	cmd.Flags().BoolVar(&unsafeRevealValues, "unsafe-reveal-values", false, "Unsafe: reveal decoded secret values")
	cmd.Flags().BoolVar(&unsafeAcknowledgePrintSecret, "i-understand-this-prints-secrets", false, "Required with --unsafe-reveal-values to acknowledge secret output")
	cmd.Flags().BoolVar(&unsafeForceNonTTY, "unsafe-force-non-tty", false, "Unsafe: allow secret output when stdout is not a terminal")

	// Add completion for namespace flag
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)

	return cmd
}

func listSecretNames(namespace string) ([]string, error) {
	output, err := commandOutputFn("kubectl", "get", "secrets", "-n", namespace, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets in namespace %s: %w", namespace, err)
	}

	return strings.Fields(string(output)), nil
}

type viewSecretOptions struct {
	unsafeRevealValues           bool
	unsafeAcknowledgePrintSecret bool
	unsafeForceNonTTY            bool
}

type secretKeyData struct {
	value []byte
	meta  secretMetadata
}

type secretMetadata struct {
	DecodedBytes int    `json:"decodedBytes" yaml:"decodedBytes"`
	SHA256Prefix string `json:"sha256Prefix" yaml:"sha256Prefix"`
	ReadError    string `json:"readError,omitempty" yaml:"readError,omitempty"`
	DecodeError  string `json:"decodeError,omitempty" yaml:"decodeError,omitempty"`
}

func viewSecret(namespace, secretName, format, key string) error {
	return viewSecretWithOptions(namespace, secretName, format, key, viewSecretOptions{})
}

func viewSecretWithOptions(namespace, secretName, format, key string, opts viewSecretOptions) error {
	logger := common.NewColorLogger()

	if err := validateViewSecretOptions(opts); err != nil {
		return err
	}

	// If secret name is not provided, prompt for selection
	if secretName == "" {
		secrets, err := listSecretNames(namespace)
		if err != nil {
			return err
		}

		if len(secrets) == 0 && namespace == "default" {
			logger.Info("No secrets found in namespace %s, prompting for another namespace", namespace)

			selectedNamespace, err := selectNamespaceFn("Select namespace:", false)
			if err != nil {
				if ui.IsCancellation(err) {
					return nil
				}
				return fmt.Errorf("namespace selection failed: %w", err)
			}
			if strings.TrimSpace(selectedNamespace) == "" {
				return nil
			}

			namespace = selectedNamespace
			secrets, err = listSecretNames(namespace)
			if err != nil {
				return err
			}
		}

		if len(secrets) == 0 {
			return fmt.Errorf("no secrets found in namespace %s; use --namespace to choose a namespace with secrets", namespace)
		}

		// Use Filter for better search experience with many secrets
		selectedSecret, err := filterOptionFn("Search for secret:", secrets)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("secret selection failed: %w", err)
		}
		secretName = selectedSecret
	}

	// Get list of keys using go template
	listKeysOutput, err := commandOutputFn("kubectl", "get", "secret", secretName,
		"-n", namespace,
		"--template={{range $k, $v := .data}}{{$k}}{{\"\\n\"}}{{end}}")
	if err != nil {
		return fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretName, err)
	}

	keys := strings.Split(strings.TrimSpace(string(listKeysOutput)), "\n")
	if len(keys) == 0 || (len(keys) == 1 && keys[0] == "") {
		logger.Warn("Secret %s/%s has no data keys", namespace, secretName)
		return nil
	}

	secretData := make(map[string]secretKeyData)

	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}

		// Get the base64 encoded value for this key using jsonpath for safe key handling
		// This avoids shell escaping issues with special characters in key names
		jsonpathExpr := fmt.Sprintf("{.data.%s}", k)
		encodedValue, err := commandOutputFn("kubectl", "get", "secret", secretName, "-n", namespace, "-o", "jsonpath="+jsonpathExpr)
		if err != nil {
			// Try with bracket notation for keys with special characters
			jsonpathExpr = fmt.Sprintf("{.data['%s']}", strings.ReplaceAll(k, "'", "\\'"))
			encodedValue, err = commandOutputFn("kubectl", "get", "secret", secretName, "-n", namespace, "-o", "jsonpath="+jsonpathExpr)
			if err != nil {
				secretData[k] = secretKeyData{meta: secretMetadata{ReadError: err.Error()}}
				continue
			}
		}

		decoded, err := decodeBase64Fn(string(encodedValue))
		if err != nil {
			secretData[k] = secretKeyData{meta: secretMetadata{DecodeError: err.Error()}}
		} else {
			secretData[k] = secretKeyData{
				value: decoded,
				meta:  newSecretMetadata(decoded),
			}
		}
	}

	// Filter by key if specified
	if key != "" {
		if data, ok := secretData[key]; ok {
			if opts.unsafeRevealValues {
				fmt.Println(secretDisplayValue(data))
				return nil
			}
			printSecretMetadataTable([]string{key}, secretData)
			return nil
		}
		return fmt.Errorf("key %s not found in secret", key)
	}

	// Display based on format
	switch format {
	case "json":
		return printSecretJSON(secretData, opts.unsafeRevealValues)
	case "yaml":
		return printSecretYAML(secretData, opts.unsafeRevealValues)
	default: // table
		logger.Info("Secret: %s/%s", namespace, secretName)
		fmt.Printf("\n")

		printSecretTable(secretData, opts.unsafeRevealValues)
	}

	return nil
}

func validateViewSecretOptions(opts viewSecretOptions) error {
	if opts.unsafeRevealValues != opts.unsafeAcknowledgePrintSecret {
		if opts.unsafeRevealValues {
			return fmt.Errorf("--unsafe-reveal-values requires --i-understand-this-prints-secrets")
		}
		return fmt.Errorf("--i-understand-this-prints-secrets requires --unsafe-reveal-values")
	}
	if opts.unsafeRevealValues && !opts.unsafeForceNonTTY && !isStdoutTerminalFn() {
		return fmt.Errorf("refusing to print secret values because stdout is not a terminal; add --unsafe-force-non-tty for local-only redirected output")
	}
	return nil
}

func newSecretMetadata(value []byte) secretMetadata {
	sum := sha256.Sum256(value)
	return secretMetadata{
		DecodedBytes: len(value),
		SHA256Prefix: fmt.Sprintf("%x", sum)[:12],
	}
}

func sortedSecretKeys(data map[string]secretKeyData) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func secretMetadataMap(data map[string]secretKeyData) map[string]secretMetadata {
	output := make(map[string]secretMetadata, len(data))
	for k, v := range data {
		output[k] = v.meta
	}
	return output
}

func secretValueMap(data map[string]secretKeyData) map[string]string {
	output := make(map[string]string, len(data))
	for k, v := range data {
		output[k] = secretDisplayValue(v)
	}
	return output
}

func secretDisplayValue(data secretKeyData) string {
	switch {
	case data.meta.ReadError != "":
		return "<error reading value>"
	case data.meta.DecodeError != "":
		return "<error decoding>"
	default:
		return string(data.value)
	}
}

func printSecretJSON(data map[string]secretKeyData, revealValues bool) error {
	var output []byte
	var err error
	if revealValues {
		output, err = json.MarshalIndent(secretValueMap(data), "", "  ")
	} else {
		output, err = json.MarshalIndent(secretMetadataMap(data), "", "  ")
	}
	if err != nil {
		return fmt.Errorf("failed to marshal secret output: %w", err)
	}
	fmt.Println(string(output))
	return nil
}

func printSecretYAML(data map[string]secretKeyData, revealValues bool) error {
	var output []byte
	var err error
	if revealValues {
		output, err = yaml.Marshal(secretValueMap(data))
	} else {
		output, err = yaml.Marshal(secretMetadataMap(data))
	}
	if err != nil {
		return fmt.Errorf("failed to marshal secret output: %w", err)
	}
	fmt.Print(string(output))
	return nil
}

func printSecretTable(data map[string]secretKeyData, revealValues bool) {
	if revealValues {
		printSecretValuesTable(sortedSecretKeys(data), data)
		return
	}
	printSecretMetadataTable(sortedSecretKeys(data), data)
}

func printSecretMetadataTable(keys []string, data map[string]secretKeyData) {
	for _, k := range keys {
		meta := data[k].meta
		fmt.Printf("KEY: %s\n", k)
		if meta.ReadError != "" {
			fmt.Printf("READ_ERROR: %s\n\n", meta.ReadError)
			continue
		}
		if meta.DecodeError != "" {
			fmt.Printf("DECODE_ERROR: %s\n\n", meta.DecodeError)
			continue
		}
		fmt.Printf("DECODED_BYTES: %d\n", meta.DecodedBytes)
		fmt.Printf("SHA256_PREFIX: %s\n\n", meta.SHA256Prefix)
	}
}

func printSecretValuesTable(keys []string, data map[string]secretKeyData) {
	for _, k := range keys {
		v := secretDisplayValue(data[k])
		if strings.Contains(v, "\n") {
			lines := strings.Split(v, "\n")
			fmt.Printf("KEY: %s\n", k)
			fmt.Printf("VALUE (multiline, %d lines):\n", len(lines))
			fmt.Printf("%s\n", strings.Repeat("-", 80))
			fmt.Println(v)
			fmt.Printf("%s\n\n", strings.Repeat("-", 80))
		} else {
			fmt.Printf("KEY: %s\n", k)
			fmt.Printf("VALUE: %s\n\n", v)
		}
	}
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
		Long:  `Reconcile multiple Flux resources of a specific type across all namespaces. If --type is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return syncFluxResources(resourceType, namespace, parallel, dryRun)
		},
	}

	cmd.Flags().StringVarP(&resourceType, "type", "t", "", "Resource type (gitrepo|helmrelease|kustomization|ocirepository) (optional - will prompt if not provided)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Limit to specific namespace (default: all namespaces)")
	cmd.Flags().BoolVar(&parallel, "parallel", false, "Run reconciliations in parallel (experimental)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List resources that would be synced without syncing them")

	return cmd
}

func syncFluxResources(resourceType, namespace string, parallel, dryRun bool) error {
	logger := common.NewColorLogger()

	// If resource type is not provided, prompt for selection
	if resourceType == "" {
		options := []string{
			"gitrepository - Git repositories",
			"helmrelease - Helm releases",
			"kustomization - Kustomizations",
			"ocirepository - OCI repositories",
		}
		selected, err := chooseOptionFn("Select resource type to sync:", options)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil // User cancelled - exit cleanly
			}
			return fmt.Errorf("resource type selection failed: %w", err)
		}
		// Extract the resource type from the selection
		resourceType = strings.Split(selected, " ")[0]
	}

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

	output, err := commandOutputFn("kubectl", args...)
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
		message := fmt.Sprintf("About to sync %d %s resources. Continue?", len(resources), fullType)
		confirmed, err := confirmActionFn(message, false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			logger.Info("Sync cancelled")
			return nil
		}
	}

	successCount := 0
	failCount := 0

	if parallel {
		// Parallel execution using goroutines
		var mu sync.Mutex
		var wg sync.WaitGroup

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

			wg.Add(1)
			go func(ns, name string) {
				defer wg.Done()

				logger.Info("Syncing %s %s/%s", fullType, ns, name)

				if err := commandRunFn("flux", "reconcile", fullType, name, "-n", ns); err != nil {
					mu.Lock()
					logger.Error("Failed to sync %s/%s: %v", ns, name, err)
					failCount++
					mu.Unlock()
				} else {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}(ns, name)
		}

		wg.Wait()
	} else {
		// Sequential execution
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

			if err := commandRunFn("flux", "reconcile", fullType, name, "-n", ns); err != nil {
				logger.Error("Failed to sync %s/%s: %v", ns, name, err)
				failCount++
				continue
			}
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

func forceSyncExternalSecret(namespace, secretName string, all bool, _timeout int) error {
	logger := common.NewColorLogger()

	var secrets []string

	if all {
		// Get all ExternalSecrets in namespace
		output, err := commandOutputFn("kubectl", "get", "externalsecret",
			"--namespace", namespace,
			"-o", "jsonpath={.items[*].metadata.name}")
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
		timestamp := fmt.Sprintf("%d", nowFn().Unix())
		if err := commandRunFn("kubectl", "--namespace", namespace,
			"annotate", "externalsecret", name,
			fmt.Sprintf("force-sync=%s", timestamp), "--overwrite"); err != nil {
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
				confirmed, err := confirmActionFn("This will uninstall and reinstall ARC. Continue?", false)
				if err != nil {
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
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
	if output, err := commandCombinedOutputFn("helm", "-n", "actions-runner-system", "uninstall", "home-ops-runner"); err != nil {
		// It might not exist, which is okay
		if !strings.Contains(string(output), "not found") {
			redacted := common.RedactCommandOutput(string(output))
			return fmt.Errorf("failed to uninstall home-ops-runner: %w\n%s", err, redacted)
		}
	}

	// Uninstall controller
	logger.Info("Uninstalling actions-runner-controller...")
	if output, err := commandCombinedOutputFn("helm", "-n", "actions-runner-system", "uninstall", "actions-runner-controller"); err != nil {
		// It might not exist, which is okay
		if !strings.Contains(string(output), "not found") {
			redacted := common.RedactCommandOutput(string(output))
			return fmt.Errorf("failed to uninstall actions-runner-controller: %w\n%s", err, redacted)
		}
	}

	// Wait a bit for cleanup
	logger.Info("Waiting for cleanup...")
	sleepFn(5 * time.Second)

	// Reconcile controller
	logger.Info("Reconciling actions-runner-controller HelmRelease...")
	if err := commandRunFn("flux", "-n", "actions-runner-system", "reconcile", "hr", "actions-runner-controller"); err != nil {
		return fmt.Errorf("failed to reconcile actions-runner-controller: %w", err)
	}

	// Reconcile runner
	logger.Info("Reconciling home-ops-runner HelmRelease...")
	if err := commandRunFn("flux", "-n", "actions-runner-system", "reconcile", "hr", "home-ops-runner"); err != nil {
		return fmt.Errorf("failed to reconcile home-ops-runner: %w", err)
	}

	logger.Success("ARC upgrade completed successfully")
	return nil
}

func newRenderKsCommand() *cobra.Command {
	var (
		outputFile string
		ksName     string
	)

	cmd := &cobra.Command{
		Use:   "render-ks <ks.yaml>",
		Short: "Render a Kustomization locally using flux",
		Long:  `Builds and renders a Kustomization locally without applying to cluster. Provide path to ks.yaml file. Use --name when the file contains multiple Flux Kustomizations.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderKustomization(args[0], ksName, outputFile)
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write output to file instead of stdout")
	cmd.Flags().StringVar(&ksName, "name", "", "Kustomization name within ks.yaml when the file contains multiple documents")

	return cmd
}

func renderKustomization(ksPath, ksName, outputFile string) error {
	logger := common.NewColorLogger()

	// Parse the ks.yaml file using the common helper
	ksInfo, err := parseKustomizationFileWithSelector(ksPath, ksName)
	if err != nil {
		return err
	}

	logger.Info("Rendering Kustomization from %s", ksInfo.KsFile)

	// Build the kustomization using flux build with dry-run (with spinner)
	outputStr, err := spinWithOutputFn(
		fmt.Sprintf("Rendering Kustomization %s/%s", ksInfo.Namespace, ksInfo.Name),
		"flux", "build", "kustomization", ksInfo.Name,
		"--namespace", ksInfo.Namespace,
		"--path", ksInfo.FullPath,
		"--kustomization-file", ksInfo.KsFile,
		"--dry-run",
	)
	if err != nil {
		return fmt.Errorf("failed to render kustomization: %w", err)
	}

	if outputFile != "" {
		// Write to file
		if err := os.WriteFile(outputFile, []byte(outputStr), 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		logger.Success("Rendered Kustomization written to %s", outputFile)
	} else {
		// Print to stdout
		fmt.Println(outputStr)
	}

	return nil
}

func newApplyKsCommand() *cobra.Command {
	var (
		dryRun bool
		ksName string
	)

	cmd := &cobra.Command{
		Use:   "apply-ks [ks.yaml]",
		Short: "Apply a locally rendered Kustomization",
		Long:  `Renders a Kustomization locally and applies it to the cluster. If no path is provided, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				ksPath         string
				selectedKsName string
			)

			// If no args provided, show interactive selector
			if len(args) == 0 {
				selected, selectedName, err := selectKustomizationFile()
				if err != nil {
					return err
				}
				if selected == "" {
					// User cancelled - exit gracefully
					return nil
				}
				ksPath = selected
				selectedKsName = selectedName
			} else {
				ksPath = args[0]
			}

			targetName := ksName
			if targetName == "" {
				targetName = selectedKsName
			}

			return applyKustomization(ksPath, targetName, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Perform a dry-run without applying")
	cmd.Flags().StringVar(&ksName, "name", "", "Kustomization name within ks.yaml when the file contains multiple documents")

	return cmd
}

func applyKustomization(ksPath, ksName string, dryRun bool) error {
	logger := common.NewColorLogger()

	// Parse the ks.yaml file using the common helper
	ksInfo, err := parseKustomizationFileWithSelector(ksPath, ksName)
	if err != nil {
		return err
	}

	logger.Info("Rendering Kustomization from %s", ksInfo.KsFile)

	// Build the kustomization using flux build with dry-run (with spinner)
	logger.Info("Building Kustomization %s", ksInfo.Name)

	outputStr, err := spinWithOutputFn(
		fmt.Sprintf("Rendering Kustomization %s/%s", ksInfo.Namespace, ksInfo.Name),
		"flux", "build", "kustomization", ksInfo.Name,
		"--namespace", ksInfo.Namespace,
		"--path", ksInfo.FullPath,
		"--kustomization-file", ksInfo.KsFile,
		"--dry-run",
	)
	if err != nil {
		return fmt.Errorf("failed to render kustomization: %w", err)
	}

	if dryRun {
		logger.Info("[DRY RUN] Would apply the following resources:")
		fmt.Println(outputStr)
		return nil
	}

	err = spinWithFuncFn("Applying Kustomization", func() error { return kubectlApplyManifestFn(outputStr) })
	if err != nil {
		return fmt.Errorf("failed to apply kustomization: %w", err)
	}

	logger.Success("Kustomization applied successfully")
	return nil
}

func selectKustomizationFile() (string, string, error) {
	logger := common.NewColorLogger()

	// Find git repository root
	gitRoot, err := findGitRoot()
	if err != nil {
		return "", "", err
	}

	// Search for all ks.yaml files in kubernetes/apps
	appsDir := fmt.Sprintf("%s/kubernetes/apps", gitRoot)
	if _, err := os.Stat(appsDir); err != nil {
		return "", "", fmt.Errorf("kubernetes/apps directory not found at %s", appsDir)
	}

	// Find all ks.yaml files recursively without relying on shell utilities
	ksFiles, err := findKustomizationFiles(appsDir)
	if err != nil {
		return "", "", err
	}
	if len(ksFiles) == 0 {
		return "", "", fmt.Errorf("no ks.yaml files found in %s", appsDir)
	}

	// Make paths relative to git root for cleaner display
	var relativeFiles []string
	for _, file := range ksFiles {
		if file == "" {
			continue
		}
		relPath := strings.TrimPrefix(file, gitRoot+"/")
		relativeFiles = append(relativeFiles, relPath)
	}

	logger.Info("Found %d Kustomization files", len(relativeFiles))

	// Use Filter for better search experience with many files
	selected, err := filterOptionFn("Search for Kustomization:", relativeFiles)
	if err != nil {
		// User cancelled - exit gracefully without error
		return "", "", nil
	}

	fullPath := fmt.Sprintf("%s/%s", gitRoot, selected)
	infos, err := parseKustomizationDocuments(fullPath)
	if err != nil {
		return "", "", err
	}
	if len(infos) == 1 {
		return fullPath, infos[0].Name, nil
	}

	var choices []string
	for _, info := range infos {
		choices = append(choices, fmt.Sprintf("%s (%s)", info.Name, info.Namespace))
	}

	selectedTarget, err := filterOptionFn("Select Kustomization document:", choices)
	if err != nil {
		return "", "", nil
	}

	selectedName := strings.TrimSpace(strings.SplitN(selectedTarget, " (", 2)[0])
	return fullPath, selectedName, nil
}

func newDeleteKsCommand() *cobra.Command {
	var (
		force  bool
		ksName string
	)

	cmd := &cobra.Command{
		Use:   "delete-ks <ks.yaml>",
		Short: "Delete resources from a locally rendered Kustomization",
		Long:  `Renders a Kustomization locally and deletes its resources from the cluster. Provide path to ks.yaml file. Use --name when the file contains multiple Flux Kustomizations.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := confirmActionFn("This will delete all resources in the Kustomization. Continue?", false)
				if err != nil {
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
					return fmt.Errorf("deletion cancelled")
				}
			}
			return deleteKustomization(args[0], ksName)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")
	cmd.Flags().StringVar(&ksName, "name", "", "Kustomization name within ks.yaml when the file contains multiple documents")

	return cmd
}

func deleteKustomization(ksPath, ksName string) error {
	logger := common.NewColorLogger()

	// Parse the ks.yaml file using the common helper
	ksInfo, err := parseKustomizationFileWithSelector(ksPath, ksName)
	if err != nil {
		return err
	}

	logger.Info("Rendering Kustomization from %s", ksInfo.KsFile)

	// Build the kustomization using flux build with dry-run
	output, err := fluxBuildKustomizationFn(ksInfo.Name, ksInfo.Namespace, ksInfo.FullPath, ksInfo.KsFile)
	if err != nil {
		return fmt.Errorf("failed to render kustomization: %w", err)
	}

	// Delete the rendered output
	logger.Info("Deleting resources from Kustomization")
	if err := spinWithFuncFn("Deleting Kustomization resources", func() error {
		return kubectlDeleteManifestFn(string(output))
	}); err != nil {
		return fmt.Errorf("failed to delete kustomization resources: %w", err)
	}

	logger.Success("Kustomization resources deleted successfully")
	return nil
}
