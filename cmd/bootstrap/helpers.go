package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"homeops-cli/internal/helmfile"
	internalKubernetes "homeops-cli/internal/kubernetes"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/onepassword"
	"homeops-cli/internal/talosctl"
	"homeops-cli/internal/templates"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// validateKubeconfig validates that the kubeconfig is working and cluster is accessible
func validateKubeconfig(config *BootstrapConfig, log *zap.SugaredLogger) error {
	if config.DryRun {
		log.Info("[DRY RUN] Would validate kubeconfig")
		return nil
	}

	log.Info("Waiting for cluster API server to be ready...")

	maxAttempts := 12 // 2 minutes total with exponential backoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Debugf("Kubeconfig validation attempt %d/%d", attempt, maxAttempts)

		clientset, err := internalKubernetes.NewClient(config.KubeConfig)
		if err != nil {
			log.Debugf("Failed to create kubernetes client: %v", err)
			time.Sleep(time.Duration(attempt*5) * time.Second)
			continue
		}

		// Test cluster connectivity with timeout
		_, err = clientset.Discovery().RESTClient().Get().AbsPath("/livez").Timeout(10 * time.Second).Do(context.Background()).Raw()

		if err == nil {
			// If cluster-info succeeds, test node accessibility
			_, err = clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
			if err == nil {
				log.Debug("Kubeconfig validation passed - cluster is accessible")
				return nil
			}
			log.Debugf("Cluster connectivity OK, but nodes not ready yet: %v", err)
		} else {
			log.Debugf("Cluster connectivity not ready yet: %v", err)
		}

		// Don't wait after the last attempt
		if attempt < maxAttempts {
			waitTime := time.Duration(attempt*5) * time.Second // 5s, 10s, 15s, etc.
			log.Debugf("Waiting %v before next attempt...", waitTime)
			time.Sleep(waitTime)
		}
	}

	return fmt.Errorf("cluster did not become ready after %d attempts over 2+ minutes", maxAttempts)
}

// applyNamespaces creates the initial namespaces required for bootstrap
func applyNamespaces(config *BootstrapConfig, log *zap.SugaredLogger) error {
	if config.DryRun {
		log.Info("[DRY RUN] Would apply initial namespaces")
		return nil
	}

	clientset, err := internalKubernetes.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Define all namespaces used in the cluster
	// This ensures all namespaces exist before any resources are applied
	namespaces := []string{
		"actions-runner-system",
		"cert-manager",
		"external-secrets",
		"flux-system",
		"kube-system", // Usually exists but we'll ensure it's there
		"longhorn-system",
		"network",
		"observability",
		"openebs-system",
		"system",
		"system-upgrade",
		"volsync-system",
	}

	for _, nsName := range namespaces {
		log.Debugf("Creating namespace: %s", nsName)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		_, err := clientset.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		if err != nil {
			if errors.IsAlreadyExists(err) {
				log.Debugf("Namespace %s already exists", nsName)
				continue
			}
			return fmt.Errorf("failed to create namespace %s: %w", nsName, err)
		}
		log.Debugf("Successfully created namespace: %s", nsName)
	}

	return nil
}

// validateClusterSecretStoreTemplate validates the clustersecretstore YAML file
func validateClusterSecretStoreTemplate(log *zap.SugaredLogger) error {
	log.Info("Validating clustersecretstore.yaml file")

	// Get the YAML file content
	yamlContent, err := templates.GetBootstrapFile("clustersecretstore.yaml")
	if err != nil {
		return fmt.Errorf("failed to get clustersecretstore YAML: %w", err)
	}

	// Validate YAML syntax
	if err := validateYAMLSyntax([]byte(yamlContent)); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Check for 1Password references
	opRefs := onepassword.ExtractReferences(yamlContent)
	if len(opRefs) == 0 {
		log.Warn("No 1Password references found in clustersecretstore YAML")
	} else {
		log.Infof("Found %d 1Password references in clustersecretstore", len(opRefs))
		for _, ref := range opRefs {
			if err := onepassword.ValidateReferenceFormat(ref); err != nil {
				return fmt.Errorf("invalid 1Password reference '%s': %w", ref, err)
			}
		}
		log.Info("All 1Password references in clustersecretstore are valid")
	}

	log.Info("ClusterSecretStore YAML validation completed successfully")
	return nil
}

// validateYAMLSyntax validates YAML syntax by attempting to parse it
func validateYAMLSyntax(content []byte) error {
	var result interface{}
	return yaml.Unmarshal(content, &result)
}

// validateResourcesYAML validates the resources YAML content (replaces validateResourcesContent for non-template files)
func validateResourcesYAML(yamlContent string, log *zap.SugaredLogger) error {
	log.Info("Validating resources.yaml content")

	// Validate YAML syntax
	if err := validateYAMLSyntax([]byte(yamlContent)); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Check that expected secrets were created (no namespaces since resources.yaml only contains secrets)
	expectedSecrets := []string{"onepassword-secret", "sops-age", "cloudflare-tunnel-id-secret"}
	for _, secret := range expectedSecrets {
		if !strings.Contains(yamlContent, fmt.Sprintf("name: %s", secret)) {
			return fmt.Errorf("expected secret '%s' not found in YAML content", secret)
		}
	}
	log.Debug("All expected secrets found in YAML content")

	// Verify that the content contains proper Kubernetes resources
	if !strings.Contains(yamlContent, "apiVersion: v1") {
		return fmt.Errorf("YAML content does not contain valid Kubernetes resources")
	}
	if !strings.Contains(yamlContent, "kind: Secret") {
		return fmt.Errorf("YAML content does not contain Secret resources")
	}

	return nil
}

// validateClusterSecretStoreYAML validates the cluster secret store YAML content (replaces validateClusterSecretStoreContent for non-template files)
func validateClusterSecretStoreYAML(yamlContent string, log *zap.SugaredLogger) error {
	log.Info("Validating clustersecretstore.yaml content")

	// Validate YAML syntax
	if err := validateYAMLSyntax([]byte(yamlContent)); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}

	// Check for ClusterSecretStore resource
	if !strings.Contains(yamlContent, "kind: ClusterSecretStore") {
		return fmt.Errorf("YAML content does not contain ClusterSecretStore resource")
	}

	// Check for expected ClusterSecretStore name
	if !strings.Contains(yamlContent, "name: onepassword") {
		return fmt.Errorf("expected ClusterSecretStore 'onepassword' not found in YAML content")
	}

	// Check for 1Password provider configuration
	if !strings.Contains(yamlContent, "onepassword:") {
		return fmt.Errorf("YAML content does not contain 1Password provider configuration")
	}

	log.Debug("ClusterSecretStore YAML validation passed")
	return nil
}

func validatePrerequisites(config *BootstrapConfig) error {
	// Check for required binaries
	requiredBins := []string{"talosctl", "kubectl", "kustomize", "op", "helmfile"}
	for _, bin := range requiredBins {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary '%s' not found in PATH", bin)
		}
	}

	// Check for required environment variables
	if config.K8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}
	if config.TalosVersion == "" {
		return fmt.Errorf("TALOS_VERSION environment variable not set")
	}

	// Check for required files (only talosconfig since templates are now embedded)
	// Skip empty talosconfig path (uses default)
	if config.TalosConfig != "" {
		if _, err := os.Stat(config.TalosConfig); os.IsNotExist(err) {
			return fmt.Errorf("required file '%s' not found", config.TalosConfig)
		}
	}

	return nil
}

func runPreflightChecks(config *BootstrapConfig, log *zap.SugaredLogger) error {
	checks := []func(*BootstrapConfig, *zap.SugaredLogger) *PreflightResult{
		checkToolAvailability,
		checkEnvironmentFiles,
		checkNetworkConnectivity,
		checkDNSResolution,
		check1PasswordAuthPreflight,
		checkMachineConfigRendering,
		checkTalosNodes,
	}

	var failures []string
	for _, check := range checks {
		result := check(config, log)
		switch result.Status {
		case "PASS":
			log.Infof("✅ ✓ %s: %s", result.Name, result.Message)
		case "WARN":
			log.Warnf("⚠️ ⚠ %s: %s", result.Name, result.Message)
		default:
			log.Errorf("❌ ✗ %s: %s", result.Name, result.Message)
			failures = append(failures, result.Name)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("preflight checks failed: %s", strings.Join(failures, ", "))
	}
	return nil
}

func checkToolAvailability(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	requiredBins := []string{"talosctl", "kubectl", "kustomize", "op", "helmfile"}
	var missing []string

	for _, bin := range requiredBins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}

	if len(missing) > 0 {
		return &PreflightResult{
			Name:    "Tool Availability",
			Status:  "FAIL",
			Message: fmt.Sprintf("Missing required tools: %s", strings.Join(missing, ", ")),
		}
	}

	return &PreflightResult{
		Name:    "Tool Availability",
		Status:  "PASS",
		Message: "All required tools are available",
	}
}

func checkEnvironmentFiles(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	// Validate versions are set (now using hardcoded defaults from templates)
	if config.K8sVersion == "" {
		return &PreflightResult{
			Name:    "Environment Files",
			Status:  "FAIL",
			Message: "KUBERNETES_VERSION not set",
		}
	}

	if config.TalosVersion == "" {
		return &PreflightResult{
			Name:    "Environment Files",
			Status:  "FAIL",
			Message: "TALOS_VERSION not set",
		}
	}

	// Check talosconfig file (only if specified)
	if config.TalosConfig != "" {
		if _, err := os.Stat(config.TalosConfig); os.IsNotExist(err) {
			return &PreflightResult{
				Name:    "Environment Files",
				Status:  "FAIL",
				Message: fmt.Sprintf("talosconfig file not found: %s", config.TalosConfig),
			}
		}
	}

	return &PreflightResult{
		Name:    "Environment Files",
		Status:  "PASS",
		Message: fmt.Sprintf("Environment files valid (K8s: %s, Talos: %s)", config.K8sVersion, config.TalosVersion),
	}
}

func checkNetworkConnectivity(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	// Test connectivity to GitHub (for CRDs)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://github.com", nil)
	resp, err := client.Do(req)
	if err != nil {
		return &PreflightResult{
			Name:    "Network Connectivity",
			Status:  "FAIL",
			Message: fmt.Sprintf("Cannot reach GitHub: %v", err),
		}
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warnf("Failed to close response body: %v", closeErr)
		}
	}()

	return &PreflightResult{
		Name:    "Network Connectivity",
		Status:  "PASS",
		Message: "Network connectivity verified",
	}
}

func checkDNSResolution(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	// Test DNS resolution
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resolver := &net.Resolver{}
	_, err := resolver.LookupHost(ctx, "github.com")
	if err != nil {
		return &PreflightResult{
			Name:    "DNS Resolution",
			Status:  "FAIL",
			Message: fmt.Sprintf("DNS resolution failed: %v", err),
		}
	}

	return &PreflightResult{
		Name:    "DNS Resolution",
		Status:  "PASS",
		Message: "DNS resolution working",
	}
}

func check1PasswordAuthPreflight(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	if err := onepassword.Ensure1PasswordAuth(); err != nil {
		return &PreflightResult{
			Name:    "1Password Authentication",
			Status:  "FAIL",
			Message: fmt.Sprintf("1Password authentication failed: %v", err),
		}
	}
	return &PreflightResult{
		Name:    "1Password Authentication",
		Status:  "PASS",
		Message: "1Password CLI authenticated",
	}
}

func checkMachineConfigRendering(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	// Test rendering of machine configurations
	// Use a sample node template for patch testing
	patchTemplate := "nodes/192.168.122.10.yaml"

	_, err := renderMachineConfigFromEmbedded("controlplane.yaml", patchTemplate, "controlplane", log)
	if err != nil {
		return &PreflightResult{
			Name:    "Machine Config Rendering",
			Status:  "FAIL",
			Message: fmt.Sprintf("Failed to render machine config: %v", err),
		}
	}

	return &PreflightResult{
		Name:    "Machine Config Rendering",
		Status:  "PASS",
		Message: "Machine configurations render successfully",
	}
}

func checkTalosNodes(config *BootstrapConfig, log *zap.SugaredLogger) *PreflightResult {
	// Check if we can get Talos nodes
	nodes, err := getTalosNodes(config.TalosConfig, log)
	if err != nil {
		return &PreflightResult{
			Name:    "Talos Nodes",
			Status:  "FAIL",
			Message: fmt.Sprintf("Cannot retrieve Talos nodes: %v", err),
		}
	}

	if len(nodes) == 0 {
		return &PreflightResult{
			Name:    "Talos Nodes",
			Status:  "WARN",
			Message: "No Talos nodes found in configuration",
		}
	}

	return &PreflightResult{
		Name:    "Talos Nodes",
		Status:  "PASS",
		Message: fmt.Sprintf("Found %d Talos nodes", len(nodes)),
	}
}

// Removed local check1PasswordAuth in favor of common.Ensure1PasswordAuth

func applyTalosConfig(config *BootstrapConfig, log *zap.SugaredLogger) error {
	// Get list of nodes from talosctl config with retry
	nodes, err := getTalosNodesWithRetry(config.TalosConfig, log, 3)
	if err != nil {
		return err
	}

	log.Infof("Found %d Talos nodes to configure", len(nodes))

	// Apply configuration to each node
	var failures []string
	for _, node := range nodes {
		nodeTemplate := fmt.Sprintf("nodes/%s.yaml", node)

		// Get machine type from embedded node template
		machineType, err := getMachineTypeFromEmbedded(nodeTemplate)
		if err != nil {
			log.Errorf("Failed to determine machine type for %s: %v", node, err)
			failures = append(failures, node)
			continue
		}

		log.Debugf("Applying Talos configuration to %s (type: %s)", node, machineType)

		// Determine base template
		var baseTemplate string
		switch machineType {
		case "controlplane":
			baseTemplate = "controlplane.yaml"
		case "worker":
			baseTemplate = "worker.yaml"
		default:
			log.Errorf("Unknown machine type for %s: %s", node, machineType)
			failures = append(failures, node)
			continue
		}

		// Render machine config using embedded templates
		renderedConfig, err := renderMachineConfigFromEmbedded(baseTemplate, nodeTemplate, machineType, log)
		if err != nil {
			log.Errorf("Failed to render config for %s: %v", node, err)
			failures = append(failures, node)
			continue
		}

		if config.DryRun {
			log.Infof("[DRY RUN] Would apply config to %s (type: %s)", node, machineType)
			continue
		}

		// Apply the config with retry
		if err := applyNodeConfigWithRetry(node, renderedConfig, log, 3); err != nil {
			// Check if node is already configured
			if strings.Contains(err.Error(), "certificate required") || strings.Contains(err.Error(), "already configured") {
				log.Warnf("Node %s is already configured, skipping", node)
				continue
			}
			log.Errorf("Failed to apply config to %s after retries: %v", node, err)
			failures = append(failures, node)
			continue
		}

		log.Infof("✅ Successfully applied configuration to %s", node)
	}

	if len(failures) > 0 {
		return fmt.Errorf("failed to configure nodes: %s", strings.Join(failures, ", "))
	}

	return nil
}

func getTalosNodes(talosConfig string, log *zap.SugaredLogger) ([]string, error) {
	runner := talosctl.NewRunner(log)
	args := []string{"config", "info", "--output", "json"}
	if talosConfig != "" {
		args = append([]string{"--talosconfig", talosConfig}, args...)
	}

	output, err := runner.Run(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get Talos nodes: %w", err)
	}

	var configInfo struct {
		Nodes []string `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(output), &configInfo); err != nil {
		return nil, fmt.Errorf("failed to parse Talos config: %w", err)
	}

	if len(configInfo.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes found in Talos configuration")
	}

	return configInfo.Nodes, nil
}

func getTalosNodesWithRetry(talosConfig string, log *zap.SugaredLogger, maxRetries int) ([]string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		nodes, err := getTalosNodes(talosConfig, log)
		if err == nil {
			return nodes, nil
		}

		lastErr = err
		log.Warnf("Attempt %d/%d to get Talos nodes failed: %v", attempt, maxRetries, err)

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	return nil, fmt.Errorf("failed to get Talos nodes after %d attempts: %w", maxRetries, lastErr)
}

func applyNodeConfigWithRetry(node string, config []byte, log *zap.SugaredLogger, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := applyNodeConfig(node, config, log)
		if err == nil {
			return nil
		}

		lastErr = err
		// Don't retry if node is already configured
		if strings.Contains(err.Error(), "certificate required") || strings.Contains(err.Error(), "already configured") {
			return err
		}

		log.Warnf("Attempt %d/%d to apply config to %s failed: %v", attempt, maxRetries, node, err)

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * 3 * time.Second)
		}
	}

	return fmt.Errorf("failed to apply config to %s after %d attempts: %w", node, maxRetries, lastErr)
}

// applyTalosPatch function removed - now using Go YAML processor in renderMachineConfig

func applyNodeConfig(node string, config []byte, log *zap.SugaredLogger) error {
	runner := talosctl.NewRunner(log)
	args := []string{"--nodes", node, "apply-config", "--insecure", "--file", "/dev/stdin"}

	_, err := runner.RunWithStdin(bytes.NewBuffer(config), args...)
	return err
}

func getMachineTypeFromEmbedded(nodeTemplate string) (string, error) {
	// Get the node template content with proper talos/ prefix
	fullTemplatePath := fmt.Sprintf("talos/%s", nodeTemplate)
	content, err := templates.GetTalosTemplate(fullTemplatePath)
	if err != nil {
		return "", fmt.Errorf("failed to get node template: %w", err)
	}

	// Parse machine type from template content
	// Check for explicit type field
	if strings.Contains(content, "type: worker") {
		return "worker", nil
	}

	// If node has VIP configuration, it's a controlplane node
	if strings.Contains(content, "vip:") {
		return "controlplane", nil
	}

	// Default to controlplane since all nodes in this cluster are controlplane
	// (can be changed to "worker" if worker nodes are added later)
	return "controlplane", nil
}

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate, machineType string, log *zap.SugaredLogger) ([]byte, error) {
	// Get base config from embedded YAML file with proper talos/ prefix
	fullBaseTemplatePath := fmt.Sprintf("talos/%s", baseTemplate)
	baseConfig, err := templates.GetTalosTemplate(fullBaseTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get base config: %w", err)
	}

	// Get patch config from embedded YAML file with proper talos/ prefix
	fullPatchTemplatePath := fmt.Sprintf("talos/%s", patchTemplate)
	patchConfig, err := templates.GetTalosTemplate(fullPatchTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get patch config: %w", err)
	}

	// Trim leading document separator if present
	patchConfigTrimmed := strings.TrimPrefix(patchConfig, "---\n")
	patchConfigTrimmed = strings.TrimPrefix(patchConfigTrimmed, "---\r\n")

	// Now split by document separators
	var patchParts []string
	if strings.Contains(patchConfigTrimmed, "\n---\n") {
		patchParts = strings.Split(patchConfigTrimmed, "\n---\n")
	} else if strings.Contains(patchConfigTrimmed, "\n---") {
		patchParts = strings.Split(patchConfigTrimmed, "\n---")
	} else {
		patchParts = []string{patchConfigTrimmed}
	}

	// The first part should be the machine config
	machineConfigPatch := strings.TrimSpace(patchParts[0])

	// Ensure the machine config patch starts with proper YAML
	if !strings.HasPrefix(machineConfigPatch, "machine:") && !strings.HasPrefix(machineConfigPatch, "version:") {
		return nil, fmt.Errorf("machine config patch does not start with valid Talos config")
	}

	// Ensure the patch has proper Talos config structure
	if !strings.Contains(machineConfigPatch, "version:") {
		machineConfigPatch = "version: v1alpha1\n" + machineConfigPatch
	}

	// Resolve 1Password references in both configs BEFORE merging
	resolvedBaseConfig, err := onepassword.ResolveReferencesInContent(baseConfig, log)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in base config: %w", err)
	}

	resolvedPatchConfig, err := onepassword.ResolveReferencesInContent(machineConfigPatch, log)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve 1Password references in patch config: %w", err)
	}

	// Use talosctl for merging resolved configs (following proven patterns)
	mergedConfig, err := mergeConfigsWithTalosctl([]byte(resolvedBaseConfig), []byte(resolvedPatchConfig), log)
	if err != nil {
		return nil, fmt.Errorf("failed to merge configs with talosctl: %w", err)
	}

	// If there are additional YAML documents in the patch (UserVolumeConfig, etc.), append them
	var resolvedConfig string
	if len(patchParts) > 1 {
		// Collect additional documents (skip the first one which is the machine config)
		additionalDocs := patchParts[1:]
		additionalParts := strings.Join(additionalDocs, "\n---\n")

		// Resolve 1Password references in additional parts too
		resolvedAdditionalParts, err := onepassword.ResolveReferencesInContent(additionalParts, log)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve 1Password references in additional config parts: %w", err)
		}
		resolvedConfig = string(mergedConfig) + "\n---\n" + resolvedAdditionalParts
	} else {
		resolvedConfig = string(mergedConfig)
	}

	// Debug: Check if the resolved config still contains 1Password references
	if strings.Contains(resolvedConfig, "op://") {
		log.Warn("Warning: Resolved config still contains 1Password references")
		// Find remaining references
		opRefs := onepassword.ExtractReferences(resolvedConfig)
		for _, ref := range opRefs {
			log.Warnf("Unresolved 1Password reference: %s", ref)
		}
	}

	return []byte(resolvedConfig), nil
}

// mergeConfigsWithTalosctl merges base and patch configs using talosctl (onedr0p approach)
func mergeConfigsWithTalosctl(baseConfig, patchConfig []byte, log *zap.SugaredLogger) ([]byte, error) {
	// Create temporary files for talosctl processing
	baseFile, err := os.CreateTemp("", "talos-base-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create base config temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(baseFile.Name()) // Ignore cleanup errors
	}()
	defer func() {
		_ = baseFile.Close() // Ignore cleanup errors
	}()

	patchFile, err := os.CreateTemp("", "talos-patch-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create patch config temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(patchFile.Name()) // Ignore cleanup errors
	}()
	defer func() {
		_ = patchFile.Close() // Ignore cleanup errors
	}()

	// Write configs to temp files
	if _, err := baseFile.Write(baseConfig); err != nil {
		return nil, fmt.Errorf("failed to write base config: %w", err)
	}
	if _, err := patchFile.Write(patchConfig); err != nil {
		return nil, fmt.Errorf("failed to write patch config: %w", err)
	}

	// Close files so talosctl can read them
	if err := baseFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close base file: %w", err)
	}
	if err := patchFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close patch file: %w", err)
	}

	// Use talosctl to merge configurations
	runner := talosctl.NewRunner(log)
	mergedConfig, err := runner.Run("machineconfig", "patch", baseFile.Name(), "--patch", "@"+patchFile.Name())
	if err != nil {
		return nil, fmt.Errorf("talosctl config merge failed: %w", err)
	}

	return []byte(mergedConfig), nil
}

// validateEtcdRunning validates that etcd is actually running after bootstrap
func validateEtcdRunning(talosConfig, controller string, log *zap.SugaredLogger) error {
	maxAttempts := 6 // 1 minute total with 10s intervals
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Debugf("Etcd validation attempt %d/%d", attempt, maxAttempts)

		runner := talosctl.NewRunner(log)
		_, err := runner.Run("--talosconfig", talosConfig, "--nodes", controller, "etcd", "status")

		if err == nil {
			log.Debug("Etcd is running and responding")
			return nil
		}

		// Check if etcd service is actually running (not just waiting)
		serviceOutput, serviceErr := runner.Run("--talosconfig", talosConfig, "--nodes", controller, "service", "etcd")
		if serviceErr == nil && strings.Contains(string(serviceOutput), "STATE    Running") {
			log.Debug("Etcd service is running")
			return nil
		}

		log.Debugf("Etcd validation attempt %d failed: %v", attempt, err)
		if attempt < maxAttempts {
			time.Sleep(10 * time.Second)
		}
	}

	return fmt.Errorf("etcd failed to start after bootstrap")
}

func bootstrapTalos(config *BootstrapConfig, log *zap.SugaredLogger) error {
	// Get a random controller node
	controller, err := getRandomController(config.TalosConfig, log)
	if err != nil {
		return fmt.Errorf("failed to get controller node for bootstrap: %w", err)
	}

	log.Debugf("Bootstrapping Talos on controller %s", controller)

	if config.DryRun {
		log.Info("[DRY RUN] Would bootstrap Talos on controller %s", controller)
		return nil
	}

	// Check if cluster is already bootstrapped first with timeout
	log.Debug("Checking if cluster is already bootstrapped...")
	runner := talosctl.NewRunner(log)
	_, err = runner.Run("--talosconfig", config.TalosConfig, "--nodes", controller, "etcd", "status")
	if err == nil {
		log.Info("Talos cluster is already bootstrapped (etcd is running)")
		return nil
	}
	log.Debugf("Cluster not bootstrapped yet: %v", err)

	// Bootstrap with retry logic similar to onedr0p's implementation
	maxAttempts := 10
	for attempts := 0; attempts < maxAttempts; attempts++ {
		log.Debugf("Bootstrap attempt %d/%d on controller %s", attempts+1, maxAttempts, controller)

		output, err := runner.Run("--talosconfig", config.TalosConfig, "--nodes", controller, "bootstrap")
		outputStr := string(output)

		// Success cases (following onedr0p's logic)
		if err == nil {
			// Validate that etcd actually started after bootstrap
			log.Debug("Bootstrap command succeeded, validating etcd started...")
			if err := validateEtcdRunning(config.TalosConfig, controller, log); err != nil {
				log.Debugf("Bootstrap succeeded but etcd validation failed: %v", err)
				if attempts < maxAttempts-1 {
					log.Debug("Waiting 10 seconds before next bootstrap attempt")
					time.Sleep(10 * time.Second)
					continue
				}
				return fmt.Errorf("bootstrap command succeeded but etcd failed to start: %w", err)
			}
			log.Infof("✅ Talos cluster bootstrapped successfully")
			return nil
		}

		// Handle "AlreadyExists" as success (like onedr0p does)
		if strings.Contains(outputStr, "AlreadyExists") ||
			strings.Contains(outputStr, "already exists") ||
			strings.Contains(outputStr, "cluster is already initialized") {
			log.Info("Bootstrap already exists - cluster is already initialized")
			return nil
		}

		log.Debugf("Bootstrap attempt %d failed: %v", attempts+1, err)
		log.Debugf("Bootstrap output: %s", outputStr)

		// Wait 5 seconds between attempts (matching onedr0p's timing)
		if attempts < maxAttempts-1 {
			log.Debug("Waiting 5 seconds before next bootstrap attempt")
			time.Sleep(5 * time.Second)
		}

	}

	return fmt.Errorf("failed to bootstrap controller after %d attempts", maxAttempts)
}

func getRandomController(talosConfig string, log *zap.SugaredLogger) (string, error) {
	runner := talosctl..NewRunner(log)
	args := []string{"config", "info", "--output", "json"}
	if talosConfig != "" {
		args = append([]string{"--talosconfig", talosConfig}, args...)
	}
	output, err := runner.Run(args...)
	if err != nil {
		return "", err
	}

	var configInfo struct {
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal([]byte(output), &configInfo); err != nil {
		return "", err
	}

	if len(configInfo.Endpoints) == 0 {
		return "", fmt.Errorf("no controllers found")
	}

	// Simple selection of first endpoint (you could randomize if needed)
	return configInfo.Endpoints[0], nil
}

func fetchKubeconfig(config *BootstrapConfig, log *zap.SugaredLogger) error {
	controller, err := getRandomController(config.TalosConfig, log)
	if err != nil {
		return fmt.Errorf("failed to get controller node for kubeconfig: %w", err)
	}

	if config.DryRun {
		log.Info("[DRY RUN] Would fetch kubeconfig from controller %s", controller)
		return nil
	}

	log.Debugf("Fetching kubeconfig from controller %s", controller)

	// Ensure directory exists for kubeconfig
	kubeconfigDir := filepath.Dir(config.KubeConfig)
	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		return fmt.Errorf("failed to create kubeconfig directory %s: %w", kubeconfigDir, err)
	}

	// Try to fetch kubeconfig with retry logic
	maxAttempts := 10
	for attempts := 0; attempts < maxAttempts; attempts++ {
		log.Debugf("Kubeconfig fetch attempt %d/%d", attempts+1, maxAttempts)

		runner := talosctl.NewRunner(log)
		args := []string{
			"--talosconfig", config.TalosConfig,
			"kubeconfig",
			"--nodes", controller,
			"--force",
			"--force-context-name", "home-ops-cluster",
			config.KubeConfig,
		}
		output, err := runner.Run(args...)
		if err == nil {
			log.Debug("Kubeconfig fetched successfully")

			// Verify the kubeconfig file was created and is readable
			if _, statErr := os.Stat(config.KubeConfig); statErr != nil {
				return fmt.Errorf("kubeconfig file was not created at %s: %w", config.KubeConfig, statErr)
			}

			// Quick validation that the kubeconfig contains expected content
			kubeconfigContent, readErr := os.ReadFile(config.KubeConfig)
			if readErr != nil {
				return fmt.Errorf("failed to read kubeconfig file: %w", readErr)
			}

			if !strings.Contains(string(kubeconfigContent), "apiVersion: v1") ||
				!strings.Contains(string(kubeconfigContent), "kind: Config") {
				return fmt.Errorf("kubeconfig file does not contain valid Kubernetes configuration")
			}

			// Save kubeconfig to 1Password for chezmoi
			if err := onepassword.SaveKubeconfig(kubeconfigContent, log); err != nil {
				log.Warnf("Failed to save kubeconfig to 1Password: %v", err)
				log.Warn("Continuing with bootstrap - kubeconfig is available locally")
			} else {
				log.Info("✅ Kubeconfig saved to 1Password for chezmoi")
			}

			log.Info("✅ Kubeconfig fetched and validated successfully")
			return nil
		}

		outputStr := string(output)
		if strings.Contains(outputStr, "connection refused") {
			log.Warnf("Kubeconfig fetch attempt %d/%d: Controller not ready", attempts+1, maxAttempts)
		} else if strings.Contains(outputStr, "timeout") {
			log.Warnf("Kubeconfig fetch attempt %d/%d: Timeout", attempts+1, maxAttempts)
		} else {
			log.Warnf("Kubeconfig fetch attempt %d/%d failed: %s", attempts+1, maxAttempts, strings.TrimSpace(outputStr))
		}

		if attempts == maxAttempts-1 {
			return fmt.Errorf("failed to fetch kubeconfig after %d attempts. Last error: %s", maxAttempts, outputStr)
		}

		// Wait before retry
		waitTime := time.Duration(attempts+1) * 5 * time.Second
		log.Debugf("Waiting %v before next kubeconfig fetch attempt...", waitTime)
		time.Sleep(waitTime)
	}

	return fmt.Errorf("unexpected error in kubeconfig fetch loop")
}

func waitForNodes(config *BootstrapConfig, log *zap.SugaredLogger) error {
	if config.DryRun {
		log.Info("[DRY RUN] Would wait for nodes to be ready")
		return nil
	}

	// First, wait for nodes to appear
	log.Info("Waiting for nodes to become available...")
	if err := waitForNodesAvailable(config, log); err != nil {
		return err
	}

	// Check if nodes are already ready (re-bootstrap scenario)
	if ready, err := checkIfNodesReady(config, log); err != nil {
		return fmt.Errorf("failed to check node readiness: %w", err)
	} else if ready {
		log.Infof("✅ Nodes are already ready (CNI likely already installed)")
		return nil
	}

	// Wait for nodes to be in Ready=False state (fresh bootstrap sequence)
	log.Info("Waiting for nodes to be in 'Ready=False' state...")
	if err := waitForNodesReadyFalse(config, log); err != nil {
		return err
	}

	return nil
}

// checkIfNodesReady checks if nodes are already in Ready=True state
func checkIfNodesReady(config *BootstrapConfig, log *zap.SugaredLogger) (bool, error) {
	clientset, err := internalKubernetes.NewClient(config.KubeConfig)
	if err != nil {
		return false, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to check node ready status: %w", err)
	}

	if len(nodes.Items) == 0 {
		return false, nil // No nodes yet
	}

	allReady := true
	for _, node := range nodes.Items {
		isReady := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				isReady = true
				break
			}
		}
		if !isReady {
			allReady = false
			break
		}
	}

	if allReady {
		log.Infof("All %d nodes are already Ready=True", len(nodes.Items))
		return true, nil
	}

	return false, nil
}

func waitForNodesAvailable(config *BootstrapConfig, log *zap.SugaredLogger) error {
	clientset, err := internalKubernetes.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	maxAttempts := 30 // 10 minutes
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			if attempt%3 == 0 { // Log every minute
				log.Debugf("Attempt %d/%d: Nodes not yet available", attempt, maxAttempts)
			}
			if attempt == maxAttempts {
				return fmt.Errorf("nodes not available after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(20 * time.Second)
			continue
		}

		if len(nodes.Items) > 0 {
			log.Infof("✅ Found %d nodes: %v", len(nodes.Items), getNodeNames(nodes))
			return nil
		}

		if attempt%3 == 0 { // Log every minute
			log.Debugf("Attempt %d/%d: No nodes found yet", attempt, maxAttempts)
		}
		time.Sleep(20 * time.Second)
	}

	return fmt.Errorf("no nodes found after %d attempts", maxAttempts)
}

func getNodeNames(nodes *corev1.NodeList) []string {
	names := make([]string, len(nodes.Items))
	for i, node := range nodes.Items {
		names[i] = node.Name
	}
	return names
}

func waitForNodesReadyFalse(config *BootstrapConfig, log *zap.SugaredLogger) error {
	clientset, err := internalKubernetes.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	maxAttempts := 60 // 20 minutes
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			if attempt%3 == 0 { // Log every minute
				log.Debugf("Attempt %d/%d: Failed to check node ready status", attempt, maxAttempts)
			}
			if attempt == maxAttempts {
				return fmt.Errorf("failed to check node ready status after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(20 * time.Second)
			continue
		}

		allReadyFalse := true
		readyFalseCount := 0
		if len(nodes.Items) > 0 {
			for _, node := range nodes.Items {
				isReadyFalse := false
				for _, condition := range node.Status.Conditions {
					if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionFalse {
						isReadyFalse = true
						break
					}
				}
				if isReadyFalse {
					readyFalseCount++
				} else {
					allReadyFalse = false
				}
			}
		} else {
			allReadyFalse = false
		}

		if allReadyFalse && readyFalseCount > 0 {
			log.Infof("✅ All %d nodes are in Ready=False state", readyFalseCount)
			return nil
		}

		if attempt%3 == 0 { // Log every minute
			log.Infof("Attempt %d/%d: %d/%d nodes Ready=False, waiting for all nodes...",
				attempt, maxAttempts, readyFalseCount, len(nodes.Items))
		}
		time.Sleep(20 * time.Second)
	}

	return fmt.Errorf("nodes did not reach Ready=False state after %d attempts", maxAttempts)
}

func applyCRDs(config *BootstrapConfig, log *zap.SugaredLogger) error {
	// Use the new helmfile-based CRD application method
	return applyCRDsFromHelmfile(config, log)
}

func applyCRDsFromHelmfile(config *BootstrapConfig, log *zap.SugaredLogger) error {
	log.Info("Applying CRDs from dedicated helmfile...")

	if config.DryRun {
		log.Info("[DRY RUN] Would apply CRDs from crds/helmfile.yaml")
		return nil
	}

	// Create temporary directory for helmfile execution
	tempDir, err := os.MkdirTemp("", "homeops-crds-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			log.Warnf("Warning: failed to remove temp directory: %v", removeErr)
		}
	}()

	// Get embedded CRDs helmfile content
	crdsHelmfileTemplate, err := templates.GetBootstrapFile("helmfile.d/00-crds.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded CRDs helmfile: %w", err)
	}

	// The CRDs helmfile doesn't need templating, write it directly
	crdsHelmfilePath := filepath.Join(tempDir, "00-crds.yaml")
	if err := os.WriteFile(crdsHelmfilePath, []byte(crdsHelmfileTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write CRDs helmfile: %w", err)
	}

	log.Info("Using dedicated CRDs helmfile to extract CRDs only")

	// Use helmfile template to generate CRDs only (the helmfile has --include-crds but we filter to CRDs)
	runner := helmfile.NewRunner(log)
	opts := helmfile.RunOptions{
		Dir: tempDir,
		Env: []string{fmt.Sprintf("ROOT_DIR=%s", config.RootDir)},
	}
	output, err := runner.RunWithOptions(opts, "--file", crdsHelmfilePath, "template")
	if err != nil {
		return fmt.Errorf("failed to template CRDs from helmfile: %w", err)
	}

	if len(output) == 0 {
		log.Warn("No manifests generated from CRDs helmfile template")
		return nil
	}

	// Extract only the CRDs from the output
	crdManifests, otherManifests, err := separateCRDsFromManifests(string(output))
	if err != nil {
		return fmt.Errorf("failed to separate CRDs from manifests: %w", err)
	}

	if len(otherManifests) > 0 {
		log.Debugf("Found %d non-CRD resources in CRDs helmfile output, ignoring them", len(otherManifests))
	}

	// Apply only the CRDs
	if len(crdManifests) > 0 {
		log.Infof("Applying %d CRDs...", len(crdManifests))
		crdYaml := strings.Join(crdManifests, "\n---\n")

		applyCmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-", "--kubeconfig", config.KubeConfig)
		applyCmd.Stdin = bytes.NewReader([]byte(crdYaml))

		if applyOutput, err := applyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w\nOutput: %s", err, string(applyOutput))
		}

		log.Info("CRDs applied, waiting for them to be established...")

		// Wait for CRDs to be established
		if err := waitForCRDsEstablished(config, log); err != nil {
			return fmt.Errorf("CRDs failed to be established: %w", err)
		}
	} else {
		log.Warn("No CRDs found in helmfile template output")
	}

	log.Info("✅ CRDs applied and established successfully")
	return nil
}

func applyResources(config *BootstrapConfig, log *zap.SugaredLogger) error {
	// Get resources from embedded YAML file (no longer a template)
	resources, err := templates.GetBootstrapFile("resources.yaml")
	if err != nil {
		return fmt.Errorf("failed to get resources: %w", err)
	}

	// Resolve 1Password references in the YAML content
	log.Info("Resolving 1Password references in bootstrap resources...")
	resolvedResources, err := onepassword.ResolveReferencesInContent(resources, log)
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
	}

	if config.DryRun {
		// Validate YAML content and 1Password references
		if err := validateResourcesYAML(resolvedResources, log); err != nil {
			return fmt.Errorf("resources validation failed: %w", err)
		}
		log.Info("[DRY RUN] Resources validation passed - would apply resources")
		return nil
	}

	// Apply resources with force-conflicts to handle cert-manager managed fields
	cmd := exec.Command("kubectl", "apply", "--server-side", "--force-conflicts", "--filename", "-")
	cmd.Stdin = bytes.NewReader([]byte(resolvedResources))

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply resources: %w\n%s", err, output)
	}

	log.Info("✅ Resources applied successfully")
	return nil
}

func applyClusterSecretStore(config *BootstrapConfig, log *zap.SugaredLogger) error {
	// Get cluster secret store from embedded YAML file (no longer a template)
	clusterSecretStore, err := templates.GetBootstrapFile("clustersecretstore.yaml")
	if err != nil {
		return fmt.Errorf("failed to get cluster secret store: %w", err)
	}

	// Resolve 1Password references in the YAML content
	log.Info("Resolving 1Password references in cluster secret store...")
	resolvedClusterSecretStore, err := onepassword.ResolveReferencesInContent(clusterSecretStore, log)
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
	}

	if config.DryRun {
		// Validate YAML content and 1Password references
		if err := validateClusterSecretStoreYAML(resolvedClusterSecretStore, log); err != nil {
			return fmt.Errorf("cluster secret store validation failed: %w", err)
		}
		log.Info("[DRY RUN] Cluster secret store validation passed - would apply cluster secret store")
		return nil
	}

	// Apply cluster secret store with force-conflicts to handle field management conflicts
	cmd := exec.Command("kubectl", "apply", "--namespace=external-secrets", "--server-side", "--force-conflicts", "--filename", "-", "--wait=true")
	cmd.Stdin = bytes.NewReader([]byte(resolvedClusterSecretStore))

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply cluster secret store: %w\n%s", err, output)
	}

	log.Info("✅ Cluster secret store applied successfully")
	return nil
}

func syncHelmReleases(config *BootstrapConfig, log *zap.SugaredLogger) error {
	if config.DryRun {
		// Validate clustersecretstore template that would be applied after helmfile
		if err := applyClusterSecretStore(config, log); err != nil {
			return err
		}
		if err := validateClusterSecretStoreTemplate(log); err != nil {
			return fmt.Errorf("clustersecretstore template validation failed: %w", err)
		}
		// Test dynamic values template rendering
		if err := testDynamicValuesTemplate(config, log); err != nil {
			return fmt.Errorf("dynamic values template test failed: %w", err)
		}
		log.Info("[DRY RUN] Template validation passed - would sync Helm releases")
		return nil
	}

	// Create temporary directory for helmfile execution
	tempDir, err := os.MkdirTemp("", "homeops-bootstrap-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			log.Warnf("Warning: failed to remove temp directory: %v", removeErr)
		}
	}()

	// Create values.yaml.gotmpl in temp directory
	valuesTemplate, err := templates.GetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}

	valuesPath := filepath.Join(tempDir, "values.yaml.gotmpl")
	if err := os.WriteFile(valuesPath, []byte(valuesTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	// Get embedded apps helmfile content
	appsHelmfileTemplate, err := templates.GetBootstrapFile("helmfile.d/01-apps.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded apps helmfile: %w", err)
	}

	// The apps helmfile doesn't need templating, write it directly
	helmfilePath := filepath.Join(tempDir, "01-apps.yaml")
	if err := os.WriteFile(helmfilePath, []byte(appsHelmfileTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write apps helmfile: %w", err)
	}

	log.Info("Created dynamic helmfile with Go template support")
	log.Infof("Setting working directory to: %s", config.RootDir)

	runner := helmfile.NewRunner(log)
	opts := helmfile.RunOptions{
		Dir: tempDir,
		Env: []string{
			fmt.Sprintf("HELMFILE_TEMPLATE_DIR=%s", tempDir),
			fmt.Sprintf("ROOT_DIR=%s", config.RootDir),
		},
	}
	if _, err := runner.RunWithOptions(opts, "--file", helmfilePath, "sync", "--hide-notes"); err != nil {
		return fmt.Errorf("failed to sync Helm releases: %w", err)
	}

	log.Info("✅ Helm releases synced successfully")

	// Wait for external-secrets webhook to be ready before applying ClusterSecretStore
	log.Info("Waiting for external-secrets webhook to be ready...")
	if err := waitForExternalSecretsWebhook(config, log); err != nil {
		return fmt.Errorf("external-secrets webhook failed to become ready: %w", err)
	}

	// Apply cluster secret store after external-secrets is deployed and ready
	if err := applyClusterSecretStore(config, log); err != nil {
		return fmt.Errorf("failed to apply cluster secret store: %w", err)
	}

	return nil
}

// separateCRDsFromManifests separates CRD manifests from other manifests
func separateCRDsFromManifests(manifestsYaml string) ([]string, []string, error) {
	var crdManifests []string
	var otherManifests []string

	// Split by YAML document separator
	documents := strings.Split(manifestsYaml, "\n---\n")

	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" || doc == "---" {
			continue
		}

		// Check if this is a CRD by looking for "kind: CustomResourceDefinition"
		if strings.Contains(doc, "kind: CustomResourceDefinition") {
			crdManifests = append(crdManifests, doc)
		} else {
			otherManifests = append(otherManifests, doc)
		}
	}

	return crdManifests, otherManifests, nil
}

// waitForCRDsEstablished waits for all CRDs to be established
func waitForCRDsEstablished(config *BootstrapConfig, log *zap.SugaredLogger) error {
	clientConfig, err := clientcmd.BuildConfigFromFlags("", config.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create client config: %w", err)
	}

	apiExtClientset, err := apiextensionsclientset.NewForConfig(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create apiextensions client: %w", err)
	}

	maxAttempts := 30 // 2 minutes with 4-second intervals
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		crds, err := apiExtClientset.ApiextensionsV1().CustomResourceDefinitions().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			log.Debugf("Attempt %d/%d: Failed to check CRD status", attempt, maxAttempts)
			if attempt == maxAttempts {
				return fmt.Errorf("failed to check CRD status after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(4 * time.Second)
			continue
		}

		allEstablished := true
		establishedCount := 0
		totalCRDs := len(crds.Items)

		for _, crd := range crds.Items {
			isEstablished := false
			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
					isEstablished = true
					break
				}
			}

			if isEstablished {
				establishedCount++
				log.Debugf("CRD %s is established", crd.Name)
			} else {
				log.Debugf("CRD %s is not established", crd.Name)
				allEstablished = false
			}
		}

		if allEstablished && totalCRDs > 0 {
			log.Infof("✅ All %d CRDs are established", totalCRDs)
			return nil
		}

		if attempt%5 == 0 { // Log every 20 seconds
			log.Infof("Waiting for CRDs to be established: %d/%d ready", establishedCount, totalCRDs)
		}
		time.Sleep(4 * time.Second)
	}

	return fmt.Errorf("CRDs did not become established after %d attempts", maxAttempts)
}

// waitForExternalSecretsWebhook waits for the external-secrets webhook to be ready
func waitForExternalSecretsWebhook(config *BootstrapConfig, log *zap.SugaredLogger) error {
	clientset, err := internalKubernetes.NewClient(config.KubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	maxAttempts := 60 // 5 minutes with 5-second intervals
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check if the webhook deployment is ready
		deployment, err := clientset.AppsV1().Deployments("external-secrets").Get(context.Background(), "external-secrets-webhook", metav1.GetOptions{})
		if err != nil {
			log.Debugf("Attempt %d/%d: Failed to check webhook deployment status", attempt, maxAttempts)
			if attempt == maxAttempts {
				return fmt.Errorf("failed to check webhook deployment status after %d attempts: %w", maxAttempts, err)
			}
			time.Sleep(5 * time.Second)
			continue
		}

		if deployment.Status.ReadyReplicas > 0 {
			// Also check if the webhook service has endpoints
			endpoints, err := clientset.CoreV1().Endpoints("external-secrets").Get(context.Background(), "external-secrets-webhook", metav1.GetOptions{})
			if err == nil {
				if len(endpoints.Subsets) > 0 && len(endpoints.Subsets[0].Addresses) > 0 {
					log.Infof("✅ External-secrets webhook is ready with %d ready replicas and endpoints available", deployment.Status.ReadyReplicas)
					return nil
				}
			}
			log.Debug("Webhook deployment ready but no endpoints available yet")
		} else {
			log.Debugf("Webhook deployment not ready yet (ready replicas: %d)", deployment.Status.ReadyReplicas)
		}

		if attempt%6 == 0 { // Log every 30 seconds
			log.Infof("Still waiting for external-secrets webhook to be ready (attempt %d/%d)", attempt, maxAttempts)
		}
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("external-secrets webhook did not become ready after %d attempts", maxAttempts)
}

// testDynamicValuesTemplate tests the dynamic values template rendering
func testDynamicValuesTemplate(config *BootstrapConfig, log *zap.SugaredLogger) error {
	log.Info("Testing dynamic values template rendering...")

	// Create metrics collector
	metricsCollector := metrics.NewPerformanceCollector()

	// Test releases to verify template works
	testReleases := []string{"cilium", "coredns", "spegel", "cert-manager", "external-secrets", "flux-operator", "flux-instance"}

	for _, release := range testReleases {
		log.Debugf("Testing values rendering for release: %s", release)

		values, err := templates.RenderHelmfileValues(release, config.RootDir, metricsCollector)
		if err != nil {
			return fmt.Errorf("failed to render values for %s: %w", release, err)
		}

		// Validate that we got some values back (not empty)
		if strings.TrimSpace(values) == "" {
			return fmt.Errorf("rendered values for %s are empty", release)
		}

		log.Debugf("Successfully rendered values for %s (%d characters)", release, len(values))
	}

	log.Infof("✅ Dynamic values template rendering test passed")
	return nil
}
