package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
	"homeops-cli/internal/talos"
)

type BootstrapConfig struct {
	RootDir        string
	KubeConfig     string
	TalosConfig    string
	K8sVersion     string
	TalosVersion   string
	DryRun         bool
	SkipCRDs       bool
	SkipResources  bool
	SkipHelmfile   bool
}

func NewCommand() *cobra.Command {
	var config BootstrapConfig

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap Talos nodes and Cluster applications",
		Long: `Bootstrap a complete Talos cluster including:
- Applying Talos configuration to all nodes
- Bootstrapping the cluster
- Installing CRDs and resources
- Syncing Helm releases`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(&config)
		},
	}

	// Add flags
	cmd.Flags().StringVar(&config.RootDir, "root-dir", ".", "Root directory of the project")
	cmd.Flags().StringVar(&config.KubeConfig, "kubeconfig", "./kubeconfig", "Path to kubeconfig file")
	cmd.Flags().StringVar(&config.TalosConfig, "talosconfig", "./talosconfig", "Path to talosconfig file")
	cmd.Flags().StringVar(&config.K8sVersion, "k8s-version", os.Getenv("KUBERNETES_VERSION"), "Kubernetes version")
	cmd.Flags().StringVar(&config.TalosVersion, "talos-version", os.Getenv("TALOS_VERSION"), "Talos version")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Perform a dry run without making changes")
	cmd.Flags().BoolVar(&config.SkipCRDs, "skip-crds", false, "Skip CRD installation")
	cmd.Flags().BoolVar(&config.SkipResources, "skip-resources", false, "Skip resource creation")
	cmd.Flags().BoolVar(&config.SkipHelmfile, "skip-helmfile", false, "Skip Helmfile sync")

	return cmd
}

func runBootstrap(config *BootstrapConfig) error {
	// Initialize logger with colors
	logger := common.NewColorLogger()

	logger.Info("Starting cluster bootstrap process")

	// Validate prerequisites
	if err := validatePrerequisites(config); err != nil {
		return fmt.Errorf("prerequisite validation failed: %w", err)
	}

	// Check 1Password authentication
	if err := check1PasswordAuth(); err != nil {
		return fmt.Errorf("1Password authentication failed: %w", err)
	}

	// Apply Talos configuration
	logger.Debug("Applying Talos configuration to nodes")
	if err := applyTalosConfig(config, logger); err != nil {
		return fmt.Errorf("failed to apply Talos config: %w", err)
	}

	// Bootstrap Talos
	logger.Debug("Bootstrapping Talos cluster")
	if err := bootstrapTalos(config, logger); err != nil {
		return fmt.Errorf("failed to bootstrap Talos: %w", err)
	}

	// Fetch kubeconfig
	logger.Debug("Fetching kubeconfig")
	if err := fetchKubeconfig(config, logger); err != nil {
		return fmt.Errorf("failed to fetch kubeconfig: %w", err)
	}

	// Wait for nodes to be ready
	logger.Debug("Waiting for nodes to be available")
	if err := waitForNodes(config, logger); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}

	// Apply CRDs
	if !config.SkipCRDs {
		logger.Debug("Applying CRDs")
		if err := applyCRDs(config, logger); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}

	// Apply resources
	if !config.SkipResources {
		logger.Debug("Applying resources")
		if err := applyResources(config, logger); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}

	// Sync Helm releases
	if !config.SkipHelmfile {
		logger.Debug("Syncing Helm releases")
		if err := syncHelmReleases(config, logger); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}

	logger.Success("Congrats! The cluster is bootstrapped and Flux is syncing the Git repository")
	return nil
}

func validatePrerequisites(config *BootstrapConfig) error {
	// Check for required binaries
	requiredBins := []string{"talosctl", "kubectl", "kustomize", "minijinja-cli", "op", "yq", "helmfile"}
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

	// Check for required files
	requiredFiles := []string{
		config.TalosConfig,
		filepath.Join(config.RootDir, "talos", "controlplane.yaml.j2"),
	}
	for _, file := range requiredFiles {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			return fmt.Errorf("required file '%s' not found", file)
		}
	}

	return nil
}

func check1PasswordAuth() error {
	cmd := exec.Command("op", "whoami", "--format=json")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to authenticate with 1Password CLI: %w", err)
	}

	// Verify we got valid JSON response
	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("invalid 1Password response: %w", err)
	}

	return nil
}

func applyTalosConfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get list of nodes from talosctl config
	nodes, err := getTalosNodes()
	if err != nil {
		return err
	}

	controlplanePath := filepath.Join(config.RootDir, "talos", "controlplane.yaml.j2")
	workerPath := filepath.Join(config.RootDir, "talos", "worker.yaml.j2")

	// Check files exist
	if _, err := os.Stat(controlplanePath); os.IsNotExist(err) {
		return fmt.Errorf("controlplane configuration not found: %s", controlplanePath)
	}

	// Worker file is optional
	hasWorker := false
	if _, err := os.Stat(workerPath); err == nil {
		hasWorker = true
	}

	// Apply configuration to each node
	for _, node := range nodes {
		nodeFile := filepath.Join(config.RootDir, "talos", "nodes", fmt.Sprintf("%s.yaml.j2", node))
		
		if _, err := os.Stat(nodeFile); os.IsNotExist(err) {
			return fmt.Errorf("node configuration not found: %s", nodeFile)
		}

		// Get machine type from node file
		machineType, err := getMachineType(nodeFile)
		if err != nil {
			return err
		}

		logger.Debug(fmt.Sprintf("Applying Talos configuration to %s (type: %s)", node, machineType))

		// Render the configuration
		var basePath string
		if machineType == "controlplane" {
			basePath = controlplanePath
		} else if machineType == "worker" && hasWorker {
			basePath = workerPath
		} else {
			return fmt.Errorf("unknown machine type: %s", machineType)
		}

		// Render machine config
		renderedConfig, err := renderMachineConfig(basePath, nodeFile)
		if err != nil {
			return fmt.Errorf("failed to render config for %s: %w", node, err)
		}

		if config.DryRun {
			logger.Info(fmt.Sprintf("[DRY RUN] Would apply config to %s", node))
			continue
		}

		// Apply the config
		if err := applyNodeConfig(node, renderedConfig); err != nil {
			// Check if node is already configured
			if strings.Contains(err.Error(), "certificate required") {
				logger.Warn(fmt.Sprintf("Node %s is already configured, skipping", node))
				continue
			}
			return fmt.Errorf("failed to apply config to %s: %w", node, err)
		}

		logger.Info(fmt.Sprintf("Successfully applied configuration to %s", node))
	}

	return nil
}

func getTalosNodes() ([]string, error) {
	cmd := exec.Command("talosctl", "config", "info", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get Talos nodes: %w", err)
	}

	var configInfo struct {
		Nodes []string `json:"nodes"`
	}
	if err := json.Unmarshal(output, &configInfo); err != nil {
		return nil, fmt.Errorf("failed to parse Talos config: %w", err)
	}

	if len(configInfo.Nodes) == 0 {
		return nil, fmt.Errorf("no Talos nodes found in configuration")
	}

	return configInfo.Nodes, nil
}

func getMachineType(nodeFile string) (string, error) {
	cmd := exec.Command("yq", ".machine.type", nodeFile)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get machine type: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

func renderMachineConfig(baseFile, patchFile string) ([]byte, error) {
	// Use the Go implementation instead of shell script
	baseConfig, err := renderTemplate(baseFile)
	if err != nil {
		return nil, fmt.Errorf("failed to render base config: %w", err)
	}

	patchConfig, err := renderTemplate(patchFile)
	if err != nil {
		return nil, fmt.Errorf("failed to render patch config: %w", err)
	}

	// Apply patch using talosctl
	return applyTalosPatch(baseConfig, patchConfig)
}

func renderTemplate(file string) ([]byte, error) {
	// First render with minijinja
	cmd := exec.Command("minijinja-cli", file)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("minijinja render failed: %w", err)
	}

	// Then inject secrets with op
	cmd = exec.Command("op", "inject")
	cmd.Stdin = bytes.NewReader(output)
	result, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("op inject failed: %w", err)
	}

	return result, nil
}

func applyTalosPatch(base, patch []byte) ([]byte, error) {
	// Write temporary files
	baseFile, err := os.CreateTemp("", "talos-base-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(baseFile.Name())

	patchFile, err := os.CreateTemp("", "talos-patch-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(patchFile.Name())

	if _, err := baseFile.Write(base); err != nil {
		return nil, err
	}
	if _, err := patchFile.Write(patch); err != nil {
		return nil, err
	}

	baseFile.Close()
	patchFile.Close()

	// Apply patch
	cmd := exec.Command("talosctl", "machineconfig", "patch", baseFile.Name(), "--patch", "@"+patchFile.Name())
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to patch config: %w", err)
	}

	return output, nil
}

func applyNodeConfig(node string, config []byte) error {
	cmd := exec.Command("talosctl", "--nodes", node, "apply-config", "--insecure", "--file", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(config)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, output)
	}

	return nil
}

func bootstrapTalos(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get a random controller node
	controller, err := getRandomController()
	if err != nil {
		return err
	}

	logger.Debug(fmt.Sprintf("Bootstrapping Talos on controller %s", controller))

	if config.DryRun {
		logger.Info("[DRY RUN] Would bootstrap Talos")
		return nil
	}

	// Try to bootstrap, checking if already bootstrapped
	for attempts := 0; attempts < 30; attempts++ {
		cmd := exec.Command("talosctl", "--nodes", controller, "bootstrap")
		output, err := cmd.CombinedOutput()
		
		if err == nil {
			logger.Info("Talos cluster bootstrapped successfully")
			return nil
		}

		outputStr := string(output)
		if strings.Contains(outputStr, "AlreadyExists") {
			logger.Info("Talos cluster is already bootstrapped")
			return nil
		}

		logger.Info(fmt.Sprintf("Bootstrap in progress, waiting 10 seconds... (attempt %d/30)", attempts+1))
		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("failed to bootstrap Talos after 30 attempts")
}

func getRandomController() (string, error) {
	cmd := exec.Command("talosctl", "config", "info", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	var configInfo struct {
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal(output, &configInfo); err != nil {
		return "", err
	}

	if len(configInfo.Endpoints) == 0 {
		return "", fmt.Errorf("no controllers found")
	}

	// Simple selection of first endpoint (you could randomize if needed)
	return configInfo.Endpoints[0], nil
}

func fetchKubeconfig(config *BootstrapConfig, logger *common.ColorLogger) error {
	controller, err := getRandomController()
	if err != nil {
		return err
	}

	if config.DryRun {
		logger.Info("[DRY RUN] Would fetch kubeconfig")
		return nil
	}

	cmd := exec.Command("talosctl", "kubeconfig", "--nodes", controller, 
		"--force", "--force-context-name", "main", filepath.Base(config.KubeConfig))
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to fetch kubeconfig: %w", err)
	}

	logger.Info("Kubeconfig fetched successfully")
	return nil
}

func waitForNodes(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would wait for nodes")
		return nil
	}

	// First check if all nodes are already ready
	cmd := exec.Command("kubectl", "wait", "nodes", "--for=condition=Ready=True", 
		"--all", "--timeout=10s")
	if err := cmd.Run(); err == nil {
		logger.Info("All nodes are ready")
		return nil
	}

	// Wait for nodes to be available (Ready=False)
	logger.Info("Waiting for nodes to become available...")
	for attempts := 0; attempts < 30; attempts++ {
		cmd := exec.Command("kubectl", "wait", "nodes", "--for=condition=Ready=False",
			"--all", "--timeout=10s")
		if err := cmd.Run(); err == nil {
			logger.Info("Nodes are available")
			return nil
		}

		logger.Info(fmt.Sprintf("Nodes not available yet, retrying in 10 seconds... (attempt %d/30)", attempts+1))
		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("timeout waiting for nodes")
}

func applyCRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	crds := []struct {
		name string
		url  string
	}{
		{
			name: "external-dns",
			url:  "https://raw.githubusercontent.com/kubernetes-sigs/external-dns/refs/tags/v0.18.0/config/crd/standard/dnsendpoints.externaldns.k8s.io.yaml",
		},
		{
			name: "gateway-api",
			url:  "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/experimental-install.yaml",
		},
		{
			name: "prometheus-operator",
			url:  "https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.84.1/stripped-down-crds.yaml",
		},
	}

	for _, crd := range crds {
		logger.Debug(fmt.Sprintf("Applying CRD: %s", crd.name))

		if config.DryRun {
			logger.Info(fmt.Sprintf("[DRY RUN] Would apply CRD: %s", crd.name))
			continue
		}

		// Download CRD
		resp, err := http.Get(crd.url)
		if err != nil {
			return fmt.Errorf("failed to download CRD %s: %w", crd.name, err)
		}
		defer resp.Body.Close()

		crdContent, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read CRD %s: %w", crd.name, err)
		}

		// Apply CRD
		cmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-")
		cmd.Stdin = bytes.NewReader(crdContent)
		
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to apply CRD %s: %w\n%s", crd.name, err, output)
		}

		logger.Info(fmt.Sprintf("Applied CRD: %s", crd.name))
	}

	return nil
}

func applyResources(config *BootstrapConfig, logger *common.ColorLogger) error {
	resourcesFile := filepath.Join(config.RootDir, "bootstrap", "resources.yaml.j2")
	
	if _, err := os.Stat(resourcesFile); os.IsNotExist(err) {
		logger.Warn("Resources file not found, skipping")
		return nil
	}

	if config.DryRun {
		logger.Info("[DRY RUN] Would apply resources")
		return nil
	}

	// Render resources
	resources, err := renderTemplate(resourcesFile)
	if err != nil {
		return fmt.Errorf("failed to render resources: %w", err)
	}

	// Apply resources
	cmd := exec.Command("kubectl", "apply", "--server-side", "--filename", "-")
	cmd.Stdin = bytes.NewReader(resources)
	
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply resources: %w\n%s", err, output)
	}

	logger.Info("Resources applied successfully")
	return nil
}

func syncHelmReleases(config *BootstrapConfig, logger *common.ColorLogger) error {
	helmfileFile := filepath.Join(config.RootDir, "bootstrap", "helmfile.yaml")
	
	if _, err := os.Stat(helmfileFile); os.IsNotExist(err) {
		logger.Warn("Helmfile not found, skipping")
		return nil
	}

	if config.DryRun {
		logger.Info("[DRY RUN] Would sync Helm releases")
		return nil
	}

	cmd := exec.Command("helmfile", "--file", helmfileFile, "sync", "--hide-notes")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to sync Helm releases: %w", err)
	}

	logger.Info("Helm releases synced successfully")
	return nil
}
