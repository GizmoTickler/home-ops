package talos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "talos",
		Short: "Manage Talos Linux nodes and clusters",
		Long:  `Commands for managing Talos Linux nodes, including configuration, upgrades, and VM deployments`,
	}

	// Add subcommands
	cmd.AddCommand(
		newApplyNodeCommand(),
		newUpgradeNodeCommand(),
		newUpgradeK8sCommand(),
		newRebootNodeCommand(),
		newShutdownClusterCommand(),
		newResetNodeCommand(),
		newResetClusterCommand(),
		newKubeconfigCommand(),
		newDeployVMCommand(),
		newManageVMCommand(),
	)

	return cmd
}

func newApplyNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "apply-node",
		Short: "Apply Talos config to a node",
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyNodeConfig(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().StringVar(&mode, "mode", "auto", "Apply mode (auto, interactive, etc.)")
	cmd.MarkFlagRequired("ip")

	return cmd
}

func applyNodeConfig(nodeIP, mode string) error {
	logger := common.NewColorLogger()
	
	// Get machine type
	machineType, err := getMachineTypeFromNode(nodeIP)
	if err != nil {
		return fmt.Errorf("failed to get machine type: %w", err)
	}

	logger.Info("Applying configuration to node %s (type: %s)", nodeIP, machineType)

	// Render machine config
	talosDir := "./talos"
	machineConfigPath := filepath.Join(talosDir, fmt.Sprintf("%s.yaml.j2", machineType))
	nodeConfigPath := filepath.Join(talosDir, "nodes", fmt.Sprintf("%s.yaml.j2", nodeIP))

	// Check files exist
	if !common.FileExists(machineConfigPath) {
		return fmt.Errorf("machine config not found: %s", machineConfigPath)
	}
	if !common.FileExists(nodeConfigPath) {
		return fmt.Errorf("node config not found: %s", nodeConfigPath)
	}

	// Render the configuration
	renderedConfig, err := renderMachineConfig(machineConfigPath, nodeConfigPath)
	if err != nil {
		return fmt.Errorf("failed to render config: %w", err)
	}

	// Apply the configuration
	cmd := exec.Command("talosctl", "--nodes", nodeIP, "apply-config", "--mode", mode, "--file", "/dev/stdin")
	cmd.Stdin = bytes.NewReader(renderedConfig)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply config: %w\n%s", err, output)
	}

	logger.Success("Configuration applied successfully to %s", nodeIP)
	return nil
}

func getMachineTypeFromNode(nodeIP string) (string, error) {
	cmd := exec.Command("talosctl", "--nodes", nodeIP, "get", "machinetypes", "--output=jsonpath={.spec}")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get machine type: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func renderMachineConfig(baseFile, patchFile string) ([]byte, error) {
	// Render base config
	baseCmd := exec.Command("minijinja-cli", baseFile)
	baseOutput, err := baseCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to render base config: %w", err)
	}

	// Inject secrets
	baseCmd = exec.Command("op", "inject")
	baseCmd.Stdin = bytes.NewReader(baseOutput)
	baseConfig, err := baseCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inject secrets in base: %w", err)
	}

	// Render patch config
	patchCmd := exec.Command("minijinja-cli", patchFile)
	patchOutput, err := patchCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to render patch config: %w", err)
	}

	// Inject secrets
	patchCmd = exec.Command("op", "inject")
	patchCmd.Stdin = bytes.NewReader(patchOutput)
	patchConfig, err := patchCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inject secrets in patch: %w", err)
	}

	// Apply patch using talosctl
	return applyTalosPatch(baseConfig, patchConfig)
}

func applyTalosPatch(base, patch []byte) ([]byte, error) {
	// Create temp files
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

	// Write configs
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

func newUpgradeNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "upgrade-node",
		Short: "Upgrade Talos on a single node",
		RunE: func(cmd *cobra.Command, args []string) error {
			return upgradeNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")
	cmd.MarkFlagRequired("ip")

	return cmd
}

func upgradeNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()

	// Get factory image from node config
	nodeFile := filepath.Join("./talos/nodes", fmt.Sprintf("%s.yaml.j2", nodeIP))
	if !common.FileExists(nodeFile) {
		return fmt.Errorf("node config not found: %s", nodeFile)
	}

	// Render node config to get image
	cmd := exec.Command("minijinja-cli", nodeFile)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to render node config: %w", err)
	}

	// Inject secrets
	cmd = exec.Command("op", "inject")
	cmd.Stdin = bytes.NewReader(output)
	configOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to inject secrets: %w", err)
	}

	// Extract factory image using yq
	cmd = exec.Command("yq", ".machine.install.image")
	cmd.Stdin = bytes.NewReader(configOutput)
	imageOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get factory image: %w", err)
	}

	factoryImage := strings.TrimSpace(string(imageOutput))
	logger.Info("Upgrading node %s to image: %s", nodeIP, factoryImage)

	// Perform upgrade
	cmd = exec.Command("talosctl", "--nodes", nodeIP, "upgrade", 
		"--image", factoryImage, 
		"--reboot-mode", mode,
		"--timeout", "10m")
	
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	logger.Success("Node %s upgraded successfully", nodeIP)
	return nil
}

func newUpgradeK8sCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade-k8s",
		Short: "Upgrade Kubernetes across the whole cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return upgradeK8s()
		},
	}

	return cmd
}

func upgradeK8s() error {
	logger := common.NewColorLogger()

	// Get a random node
	node, err := getRandomNode()
	if err != nil {
		return err
	}

	k8sVersion := os.Getenv("KUBERNETES_VERSION")
	if k8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}

	logger.Info("Upgrading Kubernetes to version %s via node %s", k8sVersion, node)

	cmd := exec.Command("talosctl", "--nodes", node, "upgrade-k8s", "--to", k8sVersion)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Kubernetes upgrade failed: %w", err)
	}

	logger.Success("Kubernetes upgraded successfully to %s", k8sVersion)
	return nil
}

func getRandomNode() (string, error) {
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
		return "", fmt.Errorf("no endpoints found")
	}

	return configInfo.Endpoints[0], nil
}

func newRebootNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "reboot-node",
		Short: "Reboot Talos on a single node",
		RunE: func(cmd *cobra.Command, args []string) error {
			return rebootNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")
	cmd.MarkFlagRequired("ip")

	return cmd
}

func rebootNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()
	logger.Info("Rebooting node %s with mode %s", nodeIP, mode)

	cmd := exec.Command("talosctl", "--nodes", nodeIP, "reboot", "--mode", mode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reboot failed: %w\n%s", err, output)
	}

	logger.Success("Node %s reboot initiated", nodeIP)
	return nil
}

func newShutdownClusterCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "shutdown-cluster",
		Short: "Shutdown Talos across the whole cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Print("Shutdown the Talos cluster ... continue? (y/N): ")
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("shutdown cancelled")
				}
			}
			return shutdownCluster()
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force shutdown without confirmation")

	return cmd
}

func shutdownCluster() error {
	logger := common.NewColorLogger()

	// Get all nodes
	nodes, err := getAllNodes()
	if err != nil {
		return err
	}

	logger.Info("Shutting down cluster nodes: %s", strings.Join(nodes, ", "))

	cmd := exec.Command("talosctl", "shutdown", "--nodes", strings.Join(nodes, ","), "--force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shutdown failed: %w\n%s", err, output)
	}

	logger.Success("Cluster shutdown initiated")
	return nil
}

func getAllNodes() ([]string, error) {
	cmd := exec.Command("talosctl", "config", "info", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var configInfo struct {
		Nodes []string `json:"nodes"`
	}
	if err := json.Unmarshal(output, &configInfo); err != nil {
		return nil, err
	}

	return configInfo.Nodes, nil
}

func newResetNodeCommand() *cobra.Command {
	var (
		nodeIP string
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "reset-node",
		Short: "Reset Talos on a single node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Printf("Reset Talos node '%s' ... continue? (y/N): ", nodeIP)
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("reset cancelled")
				}
			}
			return resetNode(nodeIP)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")
	cmd.MarkFlagRequired("ip")

	return cmd
}

func resetNode(nodeIP string) error {
	logger := common.NewColorLogger()
	logger.Info("Resetting node %s", nodeIP)

	cmd := exec.Command("talosctl", "reset", "--nodes", nodeIP, "--graceful=false")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reset failed: %w\n%s", err, output)
	}

	logger.Success("Node %s reset initiated", nodeIP)
	return nil
}

func newResetClusterCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset-cluster",
		Short: "Reset Talos across the whole cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Print("Reset the Talos cluster ... continue? (y/N): ")
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("reset cancelled")
				}
			}
			return resetCluster()
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")

	return cmd
}

func resetCluster() error {
	logger := common.NewColorLogger()

	// Get all nodes
	nodes, err := getAllNodes()
	if err != nil {
		return err
	}

	logger.Info("Resetting cluster nodes: %s", strings.Join(nodes, ", "))

	cmd := exec.Command("talosctl", "reset", "--nodes", strings.Join(nodes, ","), "--graceful=false")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reset failed: %w\n%s", err, output)
	}

	logger.Success("Cluster reset initiated")
	return nil
}

func newKubeconfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate the kubeconfig for a Talos cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return generateKubeconfig()
		},
	}

	return cmd
}

func generateKubeconfig() error {
	logger := common.NewColorLogger()

	// Get a random node
	node, err := getRandomNode()
	if err != nil {
		return err
	}

	rootDir := "."
	logger.Info("Generating kubeconfig from node %s", node)

	cmd := exec.Command("talosctl", "kubeconfig", "--nodes", node, 
		"--force", "--force-context-name", "main", rootDir)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to generate kubeconfig: %w\n%s", err, output)
	}

	logger.Success("Kubeconfig generated successfully")
	return nil
}

func newDeployVMCommand() *cobra.Command {
	var (
		name         string
		memory       int
		vcpus        int
		diskSize     int
		openebsSize  int
		rookSize     int
		macAddress   string
		pool         string
		skipZVolCreate bool
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Talos VM on TrueNAS with auto-generated ZVol paths",
		Long:  `Deploy a new Talos VM on TrueNAS with proper ZVol naming convention`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return deployVMWithPattern(name, pool, memory, vcpus, diskSize, openebsSize, rookSize, macAddress, skipZVolCreate)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().StringVar(&pool, "pool", "flashstor", "Storage pool")
	cmd.Flags().IntVar(&memory, "memory", 4096, "Memory in MB")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "Number of vCPUs")
	cmd.Flags().IntVar(&diskSize, "disk-size", 250, "Boot disk size in GB")
	cmd.Flags().IntVar(&openebsSize, "openebs-size", 1024, "OpenEBS disk size in GB")
	cmd.Flags().IntVar(&rookSize, "rook-size", 800, "Rook disk size in GB")
	cmd.Flags().StringVar(&macAddress, "mac-address", "", "MAC address (optional)")
	cmd.Flags().BoolVar(&skipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation")
	cmd.MarkFlagRequired("name")

	return cmd
}

func deployVMWithPattern(name, pool string, memory, vcpus, diskSize, openebsSize, rookSize int, macAddress string, skipZVolCreate bool) error {
	logger := common.NewColorLogger()

	// Generate ZVol paths
	bootZVol := fmt.Sprintf("%s/VM/%s-boot", pool, name)
	openebsZVol := fmt.Sprintf("%s/VM/%s-ebs", pool, name)
	rookZVol := fmt.Sprintf("%s/VM/%s-rook", pool, name)

	logger.Info("Deploying VM %s with pattern:", name)
	logger.Info("  Boot ZVol: %s (%dGB)", bootZVol, diskSize)
	logger.Info("  OpenEBS ZVol: %s (%dGB)", openebsZVol, openebsSize)
	logger.Info("  Rook ZVol: %s (%dGB)", rookZVol, rookSize)

	// Build the command
	args := []string{
		"NAME=" + name,
		fmt.Sprintf("MEMORY=%d", memory),
		fmt.Sprintf("VCPUS=%d", vcpus),
		fmt.Sprintf("DISK_SIZE=%d", diskSize),
		fmt.Sprintf("OPENEBS_SIZE=%d", openebsSize),
		fmt.Sprintf("ROOK_SIZE=%d", rookSize),
		"BOOT_ZVOL=" + bootZVol,
		"OPENEBS_ZVOL=" + openebsZVol,
		"ROOK_ZVOL=" + rookZVol,
	}

	if macAddress != "" {
		args = append(args, "MAC_ADDRESS="+macAddress)
	}

	if skipZVolCreate {
		args = append(args, "SKIP_ZVOL_CREATE=true")
	}

	// Use the existing deploy script
	cmd := exec.Command("task", "talos:deploy-vm", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("VM deployment failed: %w", err)
	}

	logger.Success("VM %s deployed successfully", name)
	return nil
}

func newManageVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manage-vm",
		Short: "Manage VMs on TrueNAS",
		Long:  `Commands for managing VMs on TrueNAS Scale`,
	}

	cmd.AddCommand(
		newListVMsCommand(),
		newStartVMCommand(),
		newStopVMCommand(),
		newDeleteVMCommand(),
		newInfoVMCommand(),
	)

	return cmd
}

func newListVMsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all VMs on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listVMs()
		},
	}
}

func listVMs() error {
	cmd := exec.Command("task", "talos:list-vms")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newStartVMCommand() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a VM on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return startVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.MarkFlagRequired("name")

	return cmd
}

func startVM(name string) error {
	cmd := exec.Command("task", "talos:start-vm", "NAME="+name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newStopVMCommand() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a VM on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.MarkFlagRequired("name")

	return cmd
}

func stopVM(name string) error {
	cmd := exec.Command("task", "talos:stop-vm", "NAME="+name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newDeleteVMCommand() *cobra.Command {
	var (
		name  string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a VM and all associated ZVols on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Printf("Delete VM '%s' and all its ZVols ... continue? (y/N): ", name)
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					return fmt.Errorf("deletion cancelled")
				}
			}
			return deleteVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")
	cmd.MarkFlagRequired("name")

	return cmd
}

func deleteVM(name string) error {
	cmd := exec.Command("task", "talos:delete-vm", "NAME="+name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newInfoVMCommand() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Get detailed information about a VM on TrueNAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return infoVM(name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.MarkFlagRequired("name")

	return cmd
}

func infoVM(name string) error {
	cmd := exec.Command("task", "talos:info-vm", "NAME="+name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
