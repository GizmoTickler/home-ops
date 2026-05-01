package talos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/iso"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/talos"
	"homeops-cli/internal/templates"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vsphere"
	localyaml "homeops-cli/internal/yaml"

	"github.com/spf13/cobra"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"gopkg.in/yaml.v3"
)

// talosCommandTimeout caps how long we wait for talosctl invocations that don't
// have their own dedicated timeout knob (apply-config, kubeconfig).
const talosCommandTimeout = 10 * time.Minute

var (
	chooseVMFunc                      = ui.Choose
	chooseTalosNodeFn                 = ui.Choose
	chooseOptionFn                    = ui.Choose
	inputPromptFn                     = ui.Input
	confirmActionFn                   = ui.Confirm
	getTrueNASVMNamesFn               = getTrueNASVMNames
	getProxmoxVMNamesFn               = getProxmoxVMNames
	getESXiVMNamesFn                  = getESXiVMNames
	vsphereGetVMNamesFn               = vsphere.GetVMNames
	proxmoxGetTalosNodeConfigFn       = proxmox.GetTalosNodeConfig
	proxmoxDefaultVMConfig            = proxmox.DefaultVMConfig
	getProxmoxCredentialsFn           = proxmox.GetCredentials
	getTalosNodeIPsFn                 = talos.GetNodeIPs
	get1PasswordSecretFn              = common.Get1PasswordSecretSilent
	getTalosTemplateFn                = templates.GetTalosTemplate
	workingDirectoryFn                = common.GetWorkingDirectory
	getVSphereCredsFn                 = getVSphereCredentials
	getVSphereHostFn                  = getVSphereHost
	getTrueNASCredentialsFn           = getTrueNASCredentials
	ensureVMLifecycleProviderFn       = ensureVMLifecycleProviderAvailable
	getMachineTypeFromNodeFn          = getMachineTypeFromNode
	renderMachineConfigFromEmbeddedFn = renderMachineConfigFromEmbedded
	injectSecretsFn                   = common.InjectSecrets
	ensure1PasswordAuthFn             = common.Ensure1PasswordAuth
	talosctlOutputFn                  = common.Output
	talosctlCombinedOutputFn          = common.CombinedOutput
	talosApplyConfigFn                = func(nodeIP, mode, config string) ([]byte, error) {
		cmd := common.Command("talosctl", "--nodes", nodeIP, "apply-config", "--mode", mode, "--file", "/dev/stdin")
		cmd.Stdin = bytes.NewReader([]byte(config))
		out, err := cmd.CombinedOutput()
		// Redact before returning — apply-config error output may echo a snippet of
		// the machineconfig that contains secrets.
		return []byte(common.RedactCommandOutput(string(out))), err
	}
	talosctlNodeOutputFn = func(nodeIP string, args ...string) ([]byte, error) {
		commandArgs := append([]string{"--nodes", nodeIP}, args...)
		return common.Output("talosctl", commandArgs...)
	}
	generateKubeconfigFn = func(node, rootDir string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), talosCommandTimeout)
		defer cancel()
		result, err := common.RunCommand(ctx, common.CommandOptions{
			Name: "talosctl",
			Args: []string{"kubeconfig", "--nodes", node, "--force", "--force-context-name", "home-ops-cluster", rootDir},
		})
		// Combined output preserves diagnostic information without leaking raw secrets
		// (kubeconfig content goes to disk via talosctl, not stdout).
		combined := []byte(result.Stdout + result.Stderr)
		return combined, err
	}
	pushKubeconfigTo1PasswordFn        = common.PushKubeconfigTo1Password
	pullKubeconfigFrom1PasswordFn      = common.PullKubeconfigFrom1Password
	startTrueNASVMFn                   = startVM
	stopTrueNASVMFn                    = stopVM
	infoTrueNASVMFn                    = infoVM
	deleteTrueNASVMFn                  = deleteVM
	startProxmoxVMFn                   = startVMOnProxmox
	stopProxmoxVMFn                    = stopVMOnProxmox
	infoProxmoxVMFn                    = infoVMOnProxmox
	deleteProxmoxVMFn                  = deleteVMOnProxmox
	powerOnVSphereVMFn                 = powerOnVMOnVSphere
	powerOffVSphereVMFn                = powerOffVMOnVSphere
	infoVSphereVMFn                    = infoVMOnVSphere
	deleteVSphereVMFn                  = deleteVMOnVSphere
	prepareISOForTrueNASFn             = prepareISOForTrueNAS
	prepareISOForProxmoxFn             = prepareISOForProxmox
	prepareISOForVSphereFn             = prepareISOForVSphere
	prepareISOForTargetFn              = prepareISOForTarget
	spinWithFuncFn                     = ui.SpinWithFunc
	spinCommandFn                      = ui.Spin
	updateNodeTemplatesWithSchematicFn = updateNodeTemplatesWithSchematic
	uploadISOToVSphereFn               = uploadISOToVSphere
	httpGetFn                          = http.Get
	controlplaneTemplatePath           = "cmd/homeops-cli/internal/templates/talos/controlplane.yaml"
	newISODownloaderFn                 = func() isoDownloader {
		return iso.NewDownloader()
	}
	newTrueNASVMManagerFn = func(host, apiKey string, port int, useSSL bool) trueNASVMManager {
		return truenas.NewVMManager(host, apiKey, port, useSSL)
	}
	newProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (proxmoxVMManager, error) {
		return proxmox.NewVMManager(host, tokenID, secret, nodeName, insecure)
	}
	newVSphereClientFn = func(host, username, password string, insecure bool) vsphereClient {
		return vsphere.NewClient(host, username, password, insecure)
	}
	newTalosFactoryClientFn = func() talosFactoryClient {
		return talos.NewFactoryClient()
	}
	newTrueNASSSHClientFn = func(config ssh.SSHConfig) trueNASSSHClient {
		return ssh.NewSSHClient(config)
	}
	newVSphereDeployerFn = func(host, username, password string) (vsphereVMDeployer, error) {
		client := vsphere.NewClient(host, username, password, true)
		if err := client.Connect(host, username, password, true); err != nil {
			return nil, fmt.Errorf("failed to connect to vSphere: %w", err)
		}
		return &defaultVSphereDeployer{client: client}, nil
	}
	newESXiK8sVMDeployerFn = func(host, username string) (esxiK8sVMDeployer, error) {
		return vsphere.NewESXiSSHClient(host, username)
	}
)

type talosFactoryClient interface {
	LoadSchematicFromTemplate() (*talos.SchematicConfig, error)
	GenerateISOFromSchematic(*talos.SchematicConfig, string, string, string) (*talos.ISOInfo, error)
}

type isoDownloader interface {
	DownloadCustomISO(iso.DownloadConfig) error
}

type trueNASSSHClient interface {
	Connect() error
	Close() error
	VerifyFile(string) (bool, int64, error)
}

type trueNASVMManager interface {
	Connect() error
	Close() error
	DeployVM(truenas.VMConfig) error
	ListVMs() error
	StartVM(string) error
	StopVM(string, bool) error
	DeleteVM(string, bool, string) error
	GetVMInfo(string) error
	CleanupOrphanedZVols(string, string) error
}

type proxmoxVMManager interface {
	Close() error
	ListVMs() error
	StartVM(string) error
	StopVM(string, bool) error
	DeleteVM(string) error
	GetVMInfo(string) error
	UploadISOFromURL(string, string, string) error
	DeployVM(proxmox.VMConfig) error
}

type vsphereClient interface {
	Connect(string, string, string, bool) error
	Close() error
	FindVM(string) (*object.VirtualMachine, error)
	ListVMs() ([]*object.VirtualMachine, error)
	GetVMInfo(*object.VirtualMachine) (*mo.VirtualMachine, error)
	UploadISOToDatastore(string, string, string) error
	PowerOnVM(*object.VirtualMachine) error
	PowerOffVM(*object.VirtualMachine) error
	DeleteVM(*object.VirtualMachine) error
}

type vsphereVMDeployer interface {
	CreateVM(vsphere.VMConfig) error
	DeployVMsConcurrently([]vsphere.VMConfig) error
	Close() error
}

type defaultVSphereDeployer struct {
	client *vsphere.Client
}

func (d *defaultVSphereDeployer) CreateVM(config vsphere.VMConfig) error {
	_, err := d.client.CreateVM(config)
	return err
}

func (d *defaultVSphereDeployer) DeployVMsConcurrently(configs []vsphere.VMConfig) error {
	return d.client.DeployVMsConcurrently(configs)
}

func (d *defaultVSphereDeployer) Close() error {
	return d.client.Close()
}

type esxiK8sVMDeployer interface {
	CreateK8sVM(vsphere.VMConfig) error
	Close()
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "talos",
		Short: "Manage Talos Linux nodes and clusters",
		Long: `Commands for managing Talos Linux nodes, including configuration, upgrades, and VM deployments.

Use ` + "`homeops-cli talos manage-vm`" + ` for VM lifecycle operations such as list, start, stop, info, poweron, poweroff, and delete.`,
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
		newPrepareISOCommand(),
		newDeployVMCommand(),
		newManageVMCommand(),
		newVMLifecycleRootGuidanceCommand("list"),
		newVMLifecycleRootGuidanceCommand("start"),
		newVMLifecycleRootGuidanceCommand("stop"),
		newVMLifecycleRootGuidanceCommand("info"),
		newVMLifecycleRootGuidanceCommand("poweron"),
		newVMLifecycleRootGuidanceCommand("poweroff"),
		newVMLifecycleRootGuidanceCommand("delete"),
	)

	return cmd
}

// getEnvOrDefault returns the value of an environment variable or a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getTrueNASCredentials retrieves TrueNAS credentials from 1Password or environment variables
func getTrueNASCredentials() (host, apiKey string, err error) {
	logger := common.NewColorLogger()
	usedEnvFallback := false

	// Try 1Password first - batch lookup for better performance
	secrets := common.Get1PasswordSecretsBatch([]string{
		constants.OpTrueNASHost,
		constants.OpTrueNASAPI,
	})
	host = secrets[constants.OpTrueNASHost]
	apiKey = secrets[constants.OpTrueNASAPI]

	// Fall back to environment variables if 1Password fails
	if host == "" {
		host = os.Getenv(constants.EnvTrueNASHost)
		if host != "" {
			usedEnvFallback = true
		}
	}
	if apiKey == "" {
		apiKey = os.Getenv(constants.EnvTrueNASAPIKey)
		if apiKey != "" {
			usedEnvFallback = true
		}
	}

	// Check if we have both credentials
	if host == "" || apiKey == "" {
		return "", "", fmt.Errorf("TrueNAS credentials not found. Please set %s and %s environment variables or configure 1Password with '%s' and '%s'",
			constants.EnvTrueNASHost, constants.EnvTrueNASAPIKey, constants.OpTrueNASHost, constants.OpTrueNASAPI)
	}

	// Warn if using environment variables (less secure than 1Password)
	if usedEnvFallback {
		logger.Warn("Using environment variables for TrueNAS credentials. Consider using 1Password for better security.")
	}

	return host, apiKey, nil
}

// get1PasswordSecret retrieves a secret from 1Password using the shared common function
func get1PasswordSecret(reference string) string {
	return get1PasswordSecretFn(reference)
}

// getSpicePassword retrieves SPICE password from 1Password or environment variables
func getSpicePassword() string {
	// Try 1Password first
	password := get1PasswordSecret(constants.OpTrueNASSPICEPass)
	if password != "" {
		return password
	}
	// Fall back to environment variable
	return os.Getenv(constants.EnvSPICEPassword)
}

func getVSphereCredentials() (host, username, password string, err error) {
	logger := common.NewColorLogger()
	usedEnvFallback := false

	host = get1PasswordSecret(constants.OpESXiHost)
	if host == "" {
		// Backward-compatible fallback for older/more direct vault layout.
		host = get1PasswordSecretFn("op://Infrastructure/esxi/host")
	}
	username = get1PasswordSecret(constants.OpESXiUsername)
	password = get1PasswordSecret(constants.OpESXiPassword)

	if host == "" {
		host = os.Getenv(constants.EnvVSphereHost)
		if host != "" {
			usedEnvFallback = true
		}
	}
	if username == "" {
		username = os.Getenv(constants.EnvVSphereUsername)
		if username != "" {
			usedEnvFallback = true
		}
	}
	if password == "" {
		password = os.Getenv(constants.EnvVSpherePassword)
		if password != "" {
			usedEnvFallback = true
		}
	}

	if host == "" || username == "" || password == "" {
		return "", "", "", fmt.Errorf("vSphere credentials not found. Please set %s, %s, and %s environment variables or configure 1Password",
			constants.EnvVSphereHost, constants.EnvVSphereUsername, constants.EnvVSpherePassword)
	}

	if usedEnvFallback {
		logger.Warn("Using environment variables for vSphere credentials. Consider using 1Password for better security.")
	}

	return host, username, password, nil
}

func withTrueNASVMManager(logger *common.ColorLogger, fn func(trueNASVMManager) error) error {
	host, apiKey, err := getTrueNASCredentialsFn()
	if err != nil {
		return err
	}

	vmManager := newTrueNASVMManagerFn(host, apiKey, 443, true)
	if err := vmManager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	return fn(vmManager)
}

func withProxmoxVMManager(logger *common.ColorLogger, fn func(proxmoxVMManager) error) error {
	host, tokenID, secret, nodeName, err := getProxmoxCredentialsFn()
	if err != nil {
		return err
	}

	vmManager, err := newProxmoxVMManagerFn(host, tokenID, secret, nodeName, true)
	if err != nil {
		return fmt.Errorf("failed to create Proxmox VM manager: %w", err)
	}
	defer func() {
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		}
	}()

	return fn(vmManager)
}

func withVSphereClient(logger *common.ColorLogger, fn func(vsphereClient) error) error {
	host, username, password, err := getVSphereCredsFn()
	if err != nil {
		return err
	}

	client := newVSphereClientFn(host, username, password, true)
	if err := client.Connect(host, username, password, true); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			logger.Warn("Failed to close vSphere connection: %v", closeErr)
		}
	}()

	return fn(client)
}

type talosConfigInfo struct {
	Endpoints []string `json:"endpoints"`
	Nodes     []string `json:"nodes"`
}

func selectTalosNode(prompt string) (string, error) {
	nodeIPs, err := getTalosNodeIPsFn()
	if err != nil {
		return "", err
	}

	selectedNode, err := chooseTalosNodeFn(prompt, nodeIPs)
	if err != nil {
		if ui.IsCancellation(err) {
			return "", nil
		}
		return "", fmt.Errorf("node selection failed: %w", err)
	}

	return selectedNode, nil
}

func getTalosConfigInfo() (*talosConfigInfo, error) {
	output, err := talosctlOutputFn("talosctl", "config", "info", "--output", "json")
	if err != nil {
		return nil, err
	}

	var configInfo talosConfigInfo
	if err := json.Unmarshal(output, &configInfo); err != nil {
		return nil, err
	}

	return &configInfo, nil
}

func runTalosctlCombinedOutput(args ...string) ([]byte, error) {
	return talosctlCombinedOutputFn("talosctl", args...)
}

func newApplyNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "apply-node",
		Short: "Apply Talos config to a node",
		Long:  `Apply Talos configuration to a node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return applyNodeConfig(nodeIP, mode, dryRun)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&mode, "mode", "auto", "Apply mode (auto, interactive, etc.)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Render and validate, but do not apply the configuration")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func applyNodeConfig(nodeIP, mode string, dryRun bool) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Get machine type
	machineType, err := getMachineTypeFromNodeFn(nodeIP)
	if err != nil {
		return fmt.Errorf("failed to get machine type: %w", err)
	}

	logger.Info("Applying configuration to node %s (type: %s)", nodeIP, machineType)

	// Render machine config using embedded templates
	machineConfigTemplate := fmt.Sprintf("talos/%s.yaml", machineType)
	nodeConfigTemplate := fmt.Sprintf("talos/nodes/%s.yaml", nodeIP)

	// Render the configuration
	renderedConfig, err := renderMachineConfigFromEmbeddedFn(machineConfigTemplate, nodeConfigTemplate)
	if err != nil {
		return fmt.Errorf("failed to render config: %w", err)
	}

	// Resolve 1Password references in the rendered config with signin-once retry
	logger.Info("Resolving 1Password references in Talos configuration...")
	resolvedConfig, err := injectSecretsFn(string(renderedConfig))
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not authenticated") || strings.Contains(errStr, "not signed in") || strings.Contains(errStr, "please run 'op signin'") {
			logger.Info("Attempting 1Password CLI signin due to authentication error...")
			if err2 := ensure1PasswordAuthFn(); err2 != nil {
				return fmt.Errorf("1Password signin failed: %w (original: %v)", err2, err)
			}
			// Retry once after successful signin
			if retryResolved, retryErr := injectSecretsFn(string(renderedConfig)); retryErr == nil {
				resolvedConfig = retryResolved
			} else {
				return fmt.Errorf("secret resolution failed after signin: %w", retryErr)
			}
		} else {
			return fmt.Errorf("failed to resolve 1Password references: %w", err)
		}
	}

	if dryRun {
		// Basic YAML validation to ensure the rendered config is structurally valid
		var data any
		if err := yaml.Unmarshal([]byte(resolvedConfig), &data); err != nil {
			return fmt.Errorf("rendered config failed YAML validation: %w", err)
		}
		logger.Info("[DRY RUN] Would apply config to %s (type: %s)", nodeIP, machineType)
		return nil
	}

	// Apply the configuration
	output, err := talosApplyConfigFn(nodeIP, mode, resolvedConfig)
	if err != nil {
		return fmt.Errorf("failed to apply config: %w\n%s", err, output)
	}

	logger.Success("Configuration applied successfully to %s", nodeIP)
	return nil
}

// getESXiVMNames retrieves the list of VM names from ESXi/vSphere
func getESXiVMNames() ([]string, error) {
	return vsphereGetVMNamesFn()
}

func normalizeVMProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "proxmox":
		return "proxmox", nil
	case "truenas":
		return "truenas", nil
	case "vsphere", "esxi":
		return "vsphere", nil
	default:
		return "", fmt.Errorf("unsupported provider: %s. Supported providers: proxmox, truenas, vsphere", provider)
	}
}

func getVMNamesForProvider(provider string) ([]string, error) {
	normalized, err := normalizeVMProvider(provider)
	if err != nil {
		return nil, err
	}

	switch normalized {
	case "truenas":
		return getTrueNASVMNamesFn()
	case "proxmox":
		return getProxmoxVMNamesFn()
	case "vsphere":
		return getESXiVMNamesFn()
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func vmProviderDisplayName(provider string) string {
	switch provider {
	case "truenas":
		return "TrueNAS"
	case "vsphere":
		return "vSphere/ESXi"
	default:
		return "Proxmox"
	}
}

func vmProviderPrerequisites(provider string) string {
	switch provider {
	case "truenas":
		return fmt.Sprintf("%s/%s environment variables or 1Password refs %s/%s",
			constants.EnvTrueNASHost, constants.EnvTrueNASAPIKey, constants.OpTrueNASHost, constants.OpTrueNASAPI)
	case "vsphere":
		return fmt.Sprintf("%s/%s/%s environment variables or vSphere 1Password refs",
			constants.EnvVSphereHost, constants.EnvVSphereUsername, constants.EnvVSpherePassword)
	default:
		return "Proxmox host, token ID, token secret, and node configuration from environment or 1Password"
	}
}

func ensureVMLifecycleProviderAvailable(provider, action string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	var capabilityErr error
	switch normalizedProvider {
	case "truenas":
		_, _, capabilityErr = getTrueNASCredentialsFn()
	case "proxmox":
		_, _, _, _, capabilityErr = getProxmoxCredentialsFn()
	case "vsphere":
		_, _, _, capabilityErr = getVSphereCredsFn()
	}

	if capabilityErr == nil {
		return nil
	}

	return fmt.Errorf("%s VM lifecycle commands require %s: %w. Use `homeops-cli talos manage-vm %s --provider %s --name <vm-name>` after configuring prerequisites",
		vmProviderDisplayName(normalizedProvider),
		vmProviderPrerequisites(normalizedProvider),
		capabilityErr,
		action,
		normalizedProvider)
}

func chooseVMNameForProvider(name, provider, action string) (string, error) {
	if strings.TrimSpace(name) != "" {
		return name, nil
	}

	vmNames, err := getVMNamesForProvider(provider)
	if err != nil {
		return "", err
	}

	selectedVM, err := chooseVMFunc(fmt.Sprintf("Select VM to %s:", action), vmNames)
	if err != nil {
		if ui.IsCancellation(err) {
			return "", nil
		}
		return "", fmt.Errorf("VM selection failed: %w", err)
	}

	return selectedVM, nil
}

func newVMLifecycleRootGuidanceCommand(action string) *cobra.Command {
	var (
		provider string
		name     string
		force    bool
	)

	cmd := &cobra.Command{
		Use:    action,
		Hidden: true,
		Short:  fmt.Sprintf("Use manage-vm %s", action),
		RunE: func(cmd *cobra.Command, args []string) error {
			normalizedProvider, err := normalizeVMProvider(provider)
			if err != nil {
				normalizedProvider = provider
			}

			example := fmt.Sprintf("homeops-cli talos manage-vm %s --provider %s", action, normalizedProvider)
			if strings.TrimSpace(name) != "" {
				example += fmt.Sprintf(" --name %s", name)
			} else if action != "list" {
				example += " --name <vm-name>"
			}
			if action == "delete" && force {
				example += " --force"
			}

			return fmt.Errorf("VM lifecycle command `talos %s` is available under `talos manage-vm %s`; use `%s`", action, action, example)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	if action != "list" {
		cmd.Flags().StringVar(&name, "name", "", "VM name")
	}
	if action == "delete" {
		cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")
	}

	return cmd
}

// getTrueNASVMNames retrieves the list of VM names from TrueNAS
func getTrueNASVMNames() ([]string, error) {
	// Get TrueNAS connection details
	host, apiKey, err := truenas.GetCredentials()
	if err != nil {
		return nil, err
	}

	// Create TrueNAS client and connect
	client := truenas.NewWorkingClient(host, apiKey, 443, true)
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Query VMs
	vms, err := client.QueryVMs(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query VMs: %w", err)
	}

	// Extract VM names
	vmNames := make([]string, 0, len(vms))
	for _, vm := range vms {
		if vm.Name != "" {
			vmNames = append(vmNames, vm.Name)
		}
	}

	if len(vmNames) == 0 {
		return nil, fmt.Errorf("no VMs found on TrueNAS")
	}

	return vmNames, nil
}

// getProxmoxVMNames retrieves the list of VM names from Proxmox
func getProxmoxVMNames() ([]string, error) {
	// Get Proxmox connection details
	host, tokenID, secret, nodeName, err := proxmox.GetCredentials()
	if err != nil {
		return nil, err
	}

	// Create Proxmox client and connect
	client, err := proxmox.NewClient(host, tokenID, secret, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create Proxmox client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Connect(nodeName); err != nil {
		return nil, fmt.Errorf("failed to connect to Proxmox: %w", err)
	}

	// Query VMs
	vms, err := client.ListVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to query VMs: %w", err)
	}

	// Extract VM names
	vmNames := make([]string, 0, len(vms))
	for _, vm := range vms {
		if vm.Name != "" {
			vmNames = append(vmNames, vm.Name)
		}
	}

	if len(vmNames) == 0 {
		return nil, fmt.Errorf("no VMs found on Proxmox")
	}

	return vmNames, nil
}

func getMachineTypeFromNode(nodeIP string) (string, error) {
	output, err := talosctlNodeOutputFn(nodeIP, "get", "machinetypes", "--output=jsonpath={.spec}")
	if err != nil {
		return "", fmt.Errorf("failed to get machine type: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func renderMachineConfigFromEmbedded(baseTemplate, patchTemplate string) ([]byte, error) {
	return renderMachineConfigFromEmbeddedWithSchematic(baseTemplate, patchTemplate, "")
}

func renderMachineConfigFromEmbeddedWithSchematic(baseTemplate, patchTemplate, schematicID string) ([]byte, error) {
	logger := common.NewColorLogger()
	metricsCollector := metrics.NewPerformanceCollector()

	// Create unified template renderer
	renderer := templates.NewTemplateRenderer(common.GetWorkingDirectory(), logger, metricsCollector)

	// Prepare environment variables for template rendering
	env := make(map[string]string)
	env["SCHEMATIC_ID"] = schematicID

	// Add other common environment variables
	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	env["KUBERNETES_VERSION"] = versionConfig.KubernetesVersion
	env["TALOS_VERSION"] = versionConfig.TalosVersion

	// Use the unified renderer for Talos config rendering and merging
	return renderer.RenderTalosConfigWithMerge(baseTemplate, patchTemplate, env)
}

func newUpgradeNodeCommand() *cobra.Command {
	var (
		nodeIP string
		mode   string
	)

	cmd := &cobra.Command{
		Use:   "upgrade-node",
		Short: "Upgrade Talos on a single node",
		Long:  `Upgrade Talos on a node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return upgradeNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func upgradeNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node to upgrade:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Get factory image from controlplane config instead of individual node configs
	controlplaneTemplate := "talos/controlplane.yaml"
	configOutput, err := getTalosTemplateFn(controlplaneTemplate)
	if err != nil {
		return fmt.Errorf("failed to get controlplane config: %w", err)
	}

	// Extract factory image using Go YAML processor
	metrics := metrics.NewPerformanceCollector()
	processor := localyaml.NewProcessor(nil, metrics)

	// Parse YAML content into a map
	configData, err := processor.ParseString(string(configOutput))
	if err != nil {
		return fmt.Errorf("failed to parse controlplane config: %w", err)
	}

	// Extract factory image using GetValue
	factoryImageValue, err := processor.GetValue(configData, "machine.install.image")
	if err != nil {
		return fmt.Errorf("failed to get factory image: %w", err)
	}

	factoryImage, ok := factoryImageValue.(string)
	if !ok {
		return fmt.Errorf("factory image is not a string: %v", factoryImageValue)
	}

	logger.Info("Upgrading node %s to image: %s", nodeIP, factoryImage)

	// Perform upgrade with spinner
	err = spinCommandFn(fmt.Sprintf("Upgrading node %s", nodeIP),
		"talosctl", "--nodes", nodeIP, "upgrade",
		"--image", factoryImage,
		"--reboot-mode", mode,
		"--timeout", "10m")

	if err != nil {
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

	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	k8sVersion := getEnvOrDefault("KUBERNETES_VERSION", versionConfig.KubernetesVersion)
	if k8sVersion == "" {
		return fmt.Errorf("KUBERNETES_VERSION environment variable not set")
	}

	logger.Info("Upgrading Kubernetes to version %s via node %s", k8sVersion, node)

	// Perform Kubernetes upgrade with spinner
	err = spinCommandFn(fmt.Sprintf("Upgrading Kubernetes to %s", k8sVersion),
		"talosctl", "--nodes", node, "upgrade-k8s", "--to", k8sVersion)

	if err != nil {
		return fmt.Errorf("kubernetes upgrade failed: %w", err)
	}

	logger.Success("Kubernetes upgraded successfully to %s", k8sVersion)
	return nil
}

func getRandomNode() (string, error) {
	configInfo, err := getTalosConfigInfo()
	if err != nil {
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
		Long:  `Reboot a Talos node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return rebootNode(nodeIP, mode)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().StringVar(&mode, "mode", "powercycle", "Reboot mode")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func rebootNode(nodeIP, mode string) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node to reboot:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Add confirmation for reboot
	confirmed, err := confirmActionFn(fmt.Sprintf("Are you sure you want to reboot node %s?", nodeIP), false)
	if err != nil {
		return fmt.Errorf("confirmation failed: %w", err)
	}
	if !confirmed {
		logger.Info("Reboot cancelled")
		return nil
	}
	logger.Info("Rebooting node %s with mode %s", nodeIP, mode)

	output, err := runTalosctlCombinedOutput("--nodes", nodeIP, "reboot", "--mode", mode)
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
				confirmed, err := confirmActionFn("Shutdown the Talos cluster?", false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
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

	output, err := runTalosctlCombinedOutput("shutdown", "--nodes", strings.Join(nodes, ","), "--force")
	if err != nil {
		return fmt.Errorf("shutdown failed: %w\n%s", err, output)
	}

	logger.Success("Cluster shutdown initiated")
	return nil
}

func getAllNodes() ([]string, error) {
	configInfo, err := getTalosConfigInfo()
	if err != nil {
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
		Long:  `Reset a Talos node. If --ip is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return resetNode(nodeIP, force)
		},
	}

	cmd.Flags().StringVar(&nodeIP, "ip", "", "Node IP address (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Force reset without confirmation")

	// Add completion for IP flag
	_ = cmd.RegisterFlagCompletionFunc("ip", completion.ValidNodeIPs)

	return cmd
}

func resetNode(nodeIP string, force bool) error {
	logger := common.NewColorLogger()

	// If node IP is not provided, prompt for selection
	if nodeIP == "" {
		selectedNode, err := selectTalosNode("Select a Talos node to reset:")
		if err != nil {
			return err
		}
		if selectedNode == "" {
			return nil
		}
		nodeIP = selectedNode
	}

	// Add confirmation for reset
	if !force {
		confirmed, err := confirmActionFn(fmt.Sprintf("Reset Talos node '%s'? This is destructive!", nodeIP), false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			logger.Info("Reset cancelled")
			return fmt.Errorf("reset cancelled")
		}
	}

	logger.Info("Resetting node %s", nodeIP)

	output, err := runTalosctlCombinedOutput("reset", "--nodes", nodeIP, "--graceful=false")
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
				confirmed, err := confirmActionFn("Reset the Talos cluster? This is destructive!", false)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return fmt.Errorf("confirmation failed: %w", err)
				}
				if !confirmed {
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

	output, err := runTalosctlCombinedOutput("reset", "--nodes", strings.Join(nodes, ","), "--graceful=false")
	if err != nil {
		return fmt.Errorf("reset failed: %w\n%s", err, output)
	}

	logger.Success("Cluster reset initiated")
	return nil
}

func newKubeconfigCommand() *cobra.Command {
	var push, pull bool

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate, push, or pull kubeconfig for a Talos cluster",
		Long: `Manage kubeconfig for a Talos cluster.

Without flags: generates kubeconfig from a cluster node
With --push: generates and saves to 1Password
With --pull: retrieves kubeconfig from 1Password`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()

			if pull {
				return pullKubeconfigFrom1Password(logger)
			}
			if err := generateKubeconfig(); err != nil {
				return err
			}
			if push {
				return pushKubeconfigTo1Password(logger)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&push, "push", false, "Save generated kubeconfig to 1Password")
	cmd.Flags().BoolVar(&pull, "pull", false, "Pull kubeconfig from 1Password")
	cmd.MarkFlagsMutuallyExclusive("push", "pull")

	return cmd
}

func generateKubeconfig() error {
	logger := common.NewColorLogger()

	// Get a random node
	node, err := getRandomNode()
	if err != nil {
		return err
	}

	rootDir := workingDirectoryFn()
	logger.Info("Generating kubeconfig from node %s", node)

	output, err := generateKubeconfigFn(node, rootDir)
	if err != nil {
		return fmt.Errorf("failed to generate kubeconfig: %w\n%s", err, output)
	}

	logger.Success("Kubeconfig generated successfully")
	return nil
}

func pushKubeconfigTo1Password(logger *common.ColorLogger) error {
	rootDir := workingDirectoryFn()
	kubeconfigPath := filepath.Join(rootDir, "kubeconfig")

	logger.Info("Pushing kubeconfig to 1Password...")
	if err := pushKubeconfigTo1PasswordFn(kubeconfigPath, logger); err != nil {
		return err
	}

	logger.Success("Kubeconfig saved to 1Password")
	return nil
}

func pullKubeconfigFrom1Password(logger *common.ColorLogger) error {
	rootDir := workingDirectoryFn()
	kubeconfigPath := filepath.Join(rootDir, "kubeconfig")

	logger.Info("Pulling kubeconfig from 1Password...")
	if err := pullKubeconfigFrom1PasswordFn(kubeconfigPath, logger); err != nil {
		return err
	}

	logger.Success("Kubeconfig saved to %s", kubeconfigPath)
	return nil
}

func promptDeployVMOptions(name, provider *string, memory, vcpus, diskSize, openebsSize *int, generateISO, dryRun *bool, datastore, network *string, nodeCount, concurrent, startIndex *int) error {
	logger := common.NewColorLogger()

	// Step 1: Select deployment pattern
	patternOptions := []string{
		"Default - 3-node k8s cluster (16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS each)",
		"Custom - Choose your own configuration",
	}

	selectedPattern, err := chooseOptionFn("Select deployment pattern:", patternOptions)
	if err != nil {
		return err
	}

	isCustom := strings.HasPrefix(selectedPattern, "Custom")

	// Step 2: Select provider
	providerOptions := deployVMProviderOptions()

	selectedProvider, err := chooseOptionFn("Select virtualization provider:", providerOptions)
	if err != nil {
		return err
	}

	*provider = providerFromDeployOption(selectedProvider)
	logSelectedDeployProvider(logger, *provider)

	// Step 3: Get VM name
	vmName, err := inputPromptFn("Enter VM name (base name for multi-node):", "k8s")
	if err != nil {
		return err
	}
	if vmName == "" {
		vmName = "k8s" // Use default if empty
	}
	*name = vmName
	logger.Info("VM name: %s", vmName)

	// Step 4: Configure resources based on pattern
	if isCustom {
		// Custom configuration
		logger.Info("Custom configuration mode - enter resource values")
		if err := promptDeployVMResourceOptions(*provider, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex); err != nil {
			return err
		}

		if providerSupportsBatchDeploy(*provider) {
			logger.Info("Custom resources: %d VMs with %d vCPUs, %dGB RAM, %dGB boot, %dGB OpenEBS each",
				*nodeCount, *vcpus, *memory/1024, *diskSize, *openebsSize)
		} else {
			logger.Info("Custom resources: %d vCPUs, %dGB RAM, %dGB boot, %dGB OpenEBS",
				*vcpus, *memory/1024, *diskSize, *openebsSize)
		}
	} else {
		applyDefaultDeployVMOptions(*provider, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex)
		logDefaultDeployResources(logger, *provider)
	}

	// Step 5: vSphere-specific configuration (if applicable)
	if *provider == "vsphere" {
		datastoreInput, err := inputPromptFn("Enter datastore name:", "truenas-iscsi")
		if err != nil {
			return err
		}
		if datastoreInput != "" {
			*datastore = datastoreInput
		} else {
			*datastore = "truenas-iscsi"
		}

		networkInput, err := inputPromptFn("Enter network port group:", "vl999")
		if err != nil {
			return err
		}
		if networkInput != "" {
			*network = networkInput
		} else {
			*network = "vl999"
		}
	}

	// Step 6: Ask about ISO generation
	generateOptions := []string{
		"No - Use existing ISO",
		"Yes - Generate custom ISO using schematic.yaml",
	}

	selectedGenerate, err := chooseOptionFn("Generate custom Talos ISO?", generateOptions)
	if err != nil {
		return err
	}

	*generateISO = strings.HasPrefix(selectedGenerate, "Yes")
	if *generateISO {
		logger.Info("Will generate custom ISO using schematic.yaml")
	}

	// Step 7: Ask about dry-run mode
	dryRunOptions := []string{
		"Real Deployment - Actually create the VM",
		"Dry-Run - Preview what would be done without creating the VM",
	}

	selectedDryRun, err := chooseOptionFn("Select deployment mode:", dryRunOptions)
	if err != nil {
		return err
	}

	*dryRun = strings.HasPrefix(selectedDryRun, "Dry-Run")
	if *dryRun {
		logger.Info("🔍 Dry-run mode enabled - no changes will be made")
	}

	return nil
}

func providerFromDeployOption(selectedProvider string) string {
	switch {
	case strings.HasPrefix(selectedProvider, "TrueNAS"):
		return "truenas"
	case strings.HasPrefix(selectedProvider, "Proxmox"):
		return "proxmox"
	default:
		return "vsphere"
	}
}

func logSelectedDeployProvider(logger *common.ColorLogger, provider string) {
	switch provider {
	case "truenas":
		logger.Info("Selected provider: TrueNAS")
	case "proxmox":
		logger.Info("Selected provider: Proxmox VE")
	default:
		logger.Info("Selected provider: vSphere/ESXi")
	}
}

func providerSupportsBatchDeploy(provider string) bool {
	return provider == "proxmox" || provider == "vsphere"
}

func promptIntWithDefault(prompt, placeholder string, defaultValue int) (int, error) {
	input, err := inputPromptFn(prompt, placeholder)
	if err != nil {
		return 0, err
	}
	if input == "" {
		return defaultValue, nil
	}

	value := defaultValue
	_, _ = fmt.Sscanf(input, "%d", &value)
	return value, nil
}

func promptDeployVMBatchOptions(provider string, nodeCount, concurrent, startIndex *int) error {
	if !providerSupportsBatchDeploy(provider) {
		*nodeCount = 1
		*concurrent = 1
		*startIndex = 0
		return nil
	}

	value, err := promptIntWithDefault("Enter number of VMs to deploy:", "3", 3)
	if err != nil {
		return err
	}
	*nodeCount = value

	if *nodeCount > 1 {
		*startIndex, err = promptIntWithDefault("Enter starting index for VM naming:", "0", 0)
		if err != nil {
			return err
		}
		*concurrent, err = promptIntWithDefault("Enter number of concurrent deployments:", "3", 3)
		if err != nil {
			return err
		}
		return nil
	}

	*startIndex = 0
	*concurrent = 1
	return nil
}

func promptDeployVMResourceOptions(provider string, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex *int) error {
	if err := promptDeployVMBatchOptions(provider, nodeCount, concurrent, startIndex); err != nil {
		return err
	}

	var err error
	*vcpus, err = promptIntWithDefault("Enter number of vCPUs:", "16", 16)
	if err != nil {
		return err
	}

	memoryGB, err := promptIntWithDefault("Enter memory in GB:", "48", 48)
	if err != nil {
		return err
	}
	*memory = memoryGB * 1024

	*diskSize, err = promptIntWithDefault("Enter boot disk size in GB:", "250", 250)
	if err != nil {
		return err
	}
	*openebsSize, err = promptIntWithDefault("Enter OpenEBS disk size in GB:", "1024", 1024)
	if err != nil {
		return err
	}

	return nil
}

func applyDefaultDeployVMOptions(provider string, memory, vcpus, diskSize, openebsSize, nodeCount, concurrent, startIndex *int) {
	*vcpus = 16
	*memory = 49152
	*diskSize = 250
	*openebsSize = 1024

	if providerSupportsBatchDeploy(provider) {
		*nodeCount = 3
		*startIndex = 0
		*concurrent = 3
		return
	}

	*nodeCount = 1
	*startIndex = 0
	*concurrent = 1
}

func logDefaultDeployResources(logger *common.ColorLogger, provider string) {
	if providerSupportsBatchDeploy(provider) {
		logger.Info("Default resources: 3 VMs with 16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS each")
		return
	}

	logger.Info("Default resources: 16 vCPUs, 48GB RAM, 250GB boot, 1TB OpenEBS")
}

func deployVMProviderOptions() []string {
	return []string{
		"Proxmox - Deploy to Proxmox VE (default)",
		"TrueNAS - Deploy to TrueNAS Scale",
		"vSphere/ESXi - Deploy to vSphere or ESXi",
	}
}

func newDeployVMCommand() *cobra.Command {
	var (
		name           string
		memory         int
		vcpus          int
		diskSize       int
		openebsSize    int
		macAddress     string
		pool           string
		skipZVolCreate bool
		generateISO    bool
		provider       string
		dryRun         bool
		// vSphere specific flags
		datastore  string
		network    string
		concurrent int
		nodeCount  int
		startIndex int
	)

	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Talos VM on TrueNAS, vSphere/ESXi, or Proxmox",
		Long: `Deploy a new Talos VM on TrueNAS, vSphere/ESXi, or Proxmox VE.

Defaults to Proxmox VE deployment. Use --provider truenas for TrueNAS or --provider vsphere/esxi for vSphere/ESXi.

For TrueNAS: Uses proper ZVol naming convention and SPICE console.
For vSphere/ESXi: Deploys to specified datastore with enhanced VM configuration.
For Proxmox: Uses predefined node configs (k8s-0, k8s-1, k8s-2) with UEFI, NUMA, and disk passthrough.

Use --generate-iso to create a custom ISO using the schematic.yaml configuration.

If no flags are provided, presents an interactive menu with default and custom patterns.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()

			// Check if running in interactive mode (no flags set)
			if name == "" && !cmd.Flags().Changed("provider") && !cmd.Flags().Changed("dry-run") {
				// Show interactive prompts
				err := promptDeployVMOptions(&name, &provider, &memory, &vcpus, &diskSize, &openebsSize, &generateISO, &dryRun, &datastore, &network, &nodeCount, &concurrent, &startIndex)
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return err
				}
			}

			normalizedProvider, err := normalizeVMProvider(provider)
			if err != nil {
				return err
			}
			provider = normalizedProvider

			// Validate required name
			if name == "" {
				return fmt.Errorf("VM name is required (use --name flag or interactive mode)")
			}

			// Show dry-run mode indicator
			if dryRun {
				logger.Info("🔍 DRY-RUN MODE - No changes will be made")
			}

			// Deploy to appropriate provider
			switch provider {
			case "truenas":
				return deployVMWithPatternDryRun(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO, dryRun)
			case "proxmox":
				return deployVMOnProxmoxDryRun(name, memory, vcpus, diskSize, openebsSize, generateISO, concurrent, nodeCount, startIndex, dryRun)
			default:
				return deployVMOnVSphereDryRun(name, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, startIndex, dryRun)
			}
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (required for single VM, base name for multiple VMs)")
	cmd.Flags().StringVar(&pool, "pool", "flashstor/VM", "Storage pool (TrueNAS only)")
	cmd.Flags().IntVar(&memory, "memory", 64*1024, "Memory in MB (default: 64GB)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 16, "Number of vCPUs (default: 16)")
	cmd.Flags().IntVar(&diskSize, "disk-size", 250, "Boot disk size in GB (default: 250GB)")
	cmd.Flags().IntVar(&openebsSize, "openebs-size", 800, "OpenEBS disk size in GB (default: 800GB)")
	cmd.Flags().StringVar(&macAddress, "mac-address", "", "MAC address (optional)")
	cmd.Flags().BoolVar(&skipZVolCreate, "skip-zvol-create", false, "Skip ZVol creation (TrueNAS only)")
	cmd.Flags().BoolVar(&generateISO, "generate-iso", false, "Generate custom ISO using schematic.yaml")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Perform a dry run without creating the VM")

	// vSphere specific flags
	cmd.Flags().StringVar(&datastore, "datastore", "truenas-iscsi", "Datastore name (vSphere: truenas-iscsi, datastore1, etc.)")
	cmd.Flags().StringVar(&network, "network", "vl999", "Network port group name (vSphere only)")
	cmd.Flags().IntVar(&concurrent, "concurrent", 3, "Number of concurrent VM deployments (Proxmox and vSphere)")
	cmd.Flags().IntVar(&nodeCount, "node-count", 1, "Number of VMs to deploy (Proxmox and vSphere)")
	cmd.Flags().IntVar(&startIndex, "start-index", 0, "Starting index for generated VM names in batch deployments")

	return cmd
}

func getVSphereHost() (string, error) {
	host := get1PasswordSecret(constants.OpESXiHost)
	if host == "" {
		// Backward-compatible fallback for older/more direct vault layout.
		host = get1PasswordSecretFn("op://Infrastructure/esxi/host")
	}
	if host == "" {
		host = os.Getenv(constants.EnvVSphereHost)
	}
	if host == "" {
		return "", fmt.Errorf("ESXi host not found. Please set %s environment variable or configure 1Password", constants.EnvVSphereHost)
	}

	return host, nil
}

func buildVSphereVMNames(baseName string, nodeCount, startIndex int) ([]string, error) {
	return buildDeploymentVMNames(baseName, nodeCount, startIndex)
}

func buildDeploymentVMNames(baseName string, nodeCount, startIndex int) ([]string, error) {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		return nil, fmt.Errorf("VM name is required")
	}
	if nodeCount <= 0 {
		return nil, fmt.Errorf("node count must be greater than 0")
	}
	if startIndex < 0 {
		return nil, fmt.Errorf("start index must be greater than or equal to 0")
	}
	if nodeCount == 1 {
		return []string{baseName}, nil
	}
	if _, exists := vsphere.GetK8sNodeConfig(baseName); exists {
		return nil, fmt.Errorf("multi-node deployment cannot start from a numbered k8s node name (%s). Use the shared base name 'k8s' with --node-count instead", baseName)
	}
	if _, exists := proxmox.GetTalosNodeConfig(baseName); exists {
		return nil, fmt.Errorf("multi-node deployment cannot start from a numbered k8s node name (%s). Use the shared base name 'k8s' with --node-count instead", baseName)
	}

	vmNames := make([]string, 0, nodeCount)
	for i := 0; i < nodeCount; i++ {
		vmNames = append(vmNames, fmt.Sprintf("%s-%d", baseName, startIndex+i))
	}

	return vmNames, nil
}

func buildGenericVSphereVMConfig(name string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network, isoPath string) vsphere.VMConfig {
	return vsphere.VMConfig{
		Name:                 name,
		Memory:               memory,
		VCPUs:                vcpus,
		DiskSize:             diskSize,
		OpenEBSSize:          openebsSize,
		Datastore:            datastore,
		Network:              network,
		ISO:                  isoPath,
		MacAddress:           macAddress,
		PowerOn:              true,
		EnableIOMMU:          true,
		ExposeCounters:       true,
		ThinProvisioned:      true,
		EnablePrecisionClock: true,
		EnableWatchdog:       true,
	}
}

func buildGenericVSphereVMConfigs(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network, isoPath string, nodeCount, startIndex int) ([]vsphere.VMConfig, error) {
	vmNames, err := buildVSphereVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	configs := make([]vsphere.VMConfig, 0, len(vmNames))
	for idx, vmName := range vmNames {
		configMAC := ""
		if idx == 0 && nodeCount == 1 {
			configMAC = macAddress
		}
		configs = append(configs, buildGenericVSphereVMConfig(vmName, memory, vcpus, diskSize, openebsSize, configMAC, datastore, network, isoPath))
	}

	return configs, nil
}

func buildK8sVSphereVMConfigs(baseName string, memory, vcpus, diskSize, openebsSize int, network string, nodeCount, startIndex int) ([]vsphere.VMConfig, error) {
	vmNames, err := buildVSphereVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	configs := make([]vsphere.VMConfig, 0, len(vmNames))
	for _, vmName := range vmNames {
		if _, exists := vsphere.GetK8sNodeConfig(vmName); !exists {
			return nil, fmt.Errorf("no predefined configuration for k8s node: %s (valid nodes: k8s-0, k8s-1, k8s-2, k8s-3)", vmName)
		}

		config := vsphere.GetK8sVMConfig(vmName)
		config.Memory = memory
		config.VCPUs = vcpus
		config.DiskSize = diskSize
		config.OpenEBSSize = openebsSize
		config.Network = network
		config.ISO = vsphere.DefaultISOPath()
		config.PowerOn = true
		configs = append(configs, config)
	}

	return configs, nil
}

// validateVMName checks if the VM name contains invalid characters
func validateVMName(name string) error {
	if strings.Contains(name, "-") {
		return fmt.Errorf("VM name '%s' cannot contain dashes (-). Use underscores (_) or alphanumeric characters only", name)
	}
	if name == "" {
		return fmt.Errorf("VM name cannot be empty")
	}
	return nil
}

type vmDeploymentDryRunSummary struct {
	Provider string
	VMNames  []string
	Lines    []string
}

type vsphereDeploymentPlan struct {
	Mode        string
	VMNames     []string
	Configs     []vsphere.VMConfig
	NodeConfigs []vsphere.K8sNodeConfig
	Concurrent  int
	ISOPath     string
}

type trueNASISOSelection struct {
	ISOPath      string
	SchematicID  string
	TalosVersion string
	CustomISO    bool
}

func emitVMDeploymentDryRunSummary(logger *common.ColorLogger, summary vmDeploymentDryRunSummary, generateISO bool) {
	logger.Info("[DRY RUN] Would deploy VM with the following configuration:")
	logger.Info("  Provider: %s", summary.Provider)
	if len(summary.VMNames) == 1 {
		logger.Info("  VM Name: %s", summary.VMNames[0])
	} else {
		logger.Info("  VM Names: %s", strings.Join(summary.VMNames, ", "))
	}
	for _, line := range summary.Lines {
		logger.Info("  %s", line)
	}
	if generateISO {
		logger.Info("  Generate Custom ISO: Yes")
	}
	logger.Success("[DRY RUN] VM deployment preview complete - no changes made")
}

func buildTrueNASDryRunSummary(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate bool) vmDeploymentDryRunSummary {
	lines := []string{
		fmt.Sprintf("Pool: %s", pool),
		fmt.Sprintf("Memory: %d MB (%d GB)", memory, memory/1024),
		fmt.Sprintf("vCPUs: %d", vcpus),
		fmt.Sprintf("Boot Disk: %d GB", diskSize),
		fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
	}
	if macAddress != "" {
		lines = append(lines, fmt.Sprintf("MAC Address: %s", macAddress))
	}
	if skipZVolCreate {
		lines = append(lines, "Skip ZVol Creation: Yes")
	}

	return vmDeploymentDryRunSummary{
		Provider: "TrueNAS",
		VMNames:  []string{name},
		Lines:    lines,
	}
}

func appendBatchDeploymentLines(lines []string, nodeCount, startIndex, concurrent int) []string {
	if nodeCount <= 1 {
		return lines
	}

	lines = append(lines, fmt.Sprintf("Node Count: %d", nodeCount))
	lines = append(lines, fmt.Sprintf("Start Index: %d", startIndex))
	lines = append(lines, fmt.Sprintf("Concurrent Deployments: %d", concurrent))
	return lines
}

func buildVSphereDryRunSummary(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, concurrent, nodeCount, startIndex int) (vmDeploymentDryRunSummary, error) {
	vmNames, err := buildVSphereVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return vmDeploymentDryRunSummary{}, err
	}

	summary := vmDeploymentDryRunSummary{
		Provider: "vSphere/ESXi",
		VMNames:  vmNames,
	}

	if strings.HasPrefix(baseName, "k8s") {
		configs, err := buildK8sVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, network, nodeCount, startIndex)
		if err != nil {
			return vmDeploymentDryRunSummary{}, err
		}
		if len(configs) > 0 {
			nodeConfig, _ := vsphere.GetK8sNodeConfig(configs[0].Name)
			summary.Lines = append(summary.Lines,
				"Deployment Mode: SSH-based (production k8s node)",
				fmt.Sprintf("Boot Datastore: %s", nodeConfig.BootDatastore),
				"OpenEBS Datastore: truenas-iscsi",
				fmt.Sprintf("RDM (Ceph): %s", nodeConfig.RDMPath),
				fmt.Sprintf("SR-IOV PCI Device: %s", nodeConfig.PCIDevice),
			)
			if len(configs) == 1 {
				summary.Lines = append(summary.Lines,
					fmt.Sprintf("MAC Address: %s", nodeConfig.MacAddress),
					fmt.Sprintf("CPU Affinity: %s", nodeConfig.CPUAffinity),
				)
			} else {
				summary.Lines = append(summary.Lines, fmt.Sprintf("Node Presets: %s", strings.Join(vmNames, ", ")))
			}
			summary.Lines = append(summary.Lines,
				fmt.Sprintf("Memory: %d MB (%d GB) - pinned reservation", memory, memory/1024),
				fmt.Sprintf("vCPUs: %d", vcpus),
				fmt.Sprintf("Boot Disk: %d GB", diskSize),
				fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
				fmt.Sprintf("Network: %s (SR-IOV passthrough)", network),
			)
		}
	} else {
		configs, err := buildGenericVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, vsphere.DefaultISOPath(), nodeCount, startIndex)
		if err != nil {
			return vmDeploymentDryRunSummary{}, err
		}
		summary.Lines = append(summary.Lines,
			"Deployment Mode: govmomi (generic VM)",
			fmt.Sprintf("Datastore: %s", datastore),
			fmt.Sprintf("Network: %s (vmxnet3)", network),
			fmt.Sprintf("Memory: %d MB (%d GB)", memory, memory/1024),
			fmt.Sprintf("vCPUs: %d", vcpus),
			fmt.Sprintf("Boot Disk: %d GB", diskSize),
			fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
		)
		if len(configs) == 1 && configs[0].MacAddress != "" {
			summary.Lines = append(summary.Lines, fmt.Sprintf("MAC Address: %s", configs[0].MacAddress))
		}
	}

	summary.Lines = appendBatchDeploymentLines(summary.Lines, len(vmNames), startIndex, concurrent)
	return summary, nil
}

func buildProxmoxDryRunSummary(plan *proxmoxDeploymentPlan, memory, vcpus, diskSize, openebsSize int) vmDeploymentDryRunSummary {
	summary := vmDeploymentDryRunSummary{
		Provider: "Proxmox VE",
		VMNames:  plan.VMNames,
	}

	if plan.AllPredefined {
		summary.Lines = append(summary.Lines, "Deployment Mode: Predefined Talos node configuration")
		if len(plan.Presets) == 1 {
			preset := plan.Presets[0]
			summary.Lines = append(summary.Lines,
				fmt.Sprintf("VMID: %d", preset.VMID),
				fmt.Sprintf("Boot Storage: %s", preset.BootStorage),
				fmt.Sprintf("OpenEBS Storage: %s", preset.OpenEBSStorage),
				fmt.Sprintf("Ceph Disk (passthrough): /dev/disk/by-id/%s", preset.CephDiskByID),
				fmt.Sprintf("CPU Affinity: %s", preset.CPUAffinity),
				fmt.Sprintf("NUMA Node: %d", preset.NUMANode),
				fmt.Sprintf("MAC Address: %s", preset.MacAddress),
			)
		} else {
			summary.Lines = append(summary.Lines, fmt.Sprintf("Node Presets: %s", strings.Join(plan.VMNames, ", ")))
		}

		defaultConfig := proxmox.DefaultVMConfig
		summary.Lines = append(summary.Lines,
			fmt.Sprintf("Memory: %d MB (%d GB)", defaultConfig.Memory, defaultConfig.Memory/1024),
			fmt.Sprintf("vCPUs: %d", defaultConfig.Cores),
			fmt.Sprintf("CPU Type: %s", defaultConfig.CPUType),
			fmt.Sprintf("Boot Disk: %d GB", defaultConfig.BootDiskSize),
			fmt.Sprintf("OpenEBS Disk: %d GB", defaultConfig.OpenEBSSize),
			fmt.Sprintf("Network: %s (VLAN %d, MTU %d)", defaultConfig.NetworkBridge, defaultConfig.VLANID, defaultConfig.NetworkMTU),
			fmt.Sprintf("BIOS: %s (UEFI)", defaultConfig.BIOS),
			"NUMA: Enabled",
			fmt.Sprintf("SCSI Controller: %s", defaultConfig.SCSIController),
		)
	} else {
		summary.Lines = append(summary.Lines,
			"Deployment Mode: Custom configuration",
			fmt.Sprintf("Memory: %d MB (%d GB)", memory, memory/1024),
			fmt.Sprintf("vCPUs: %d", vcpus),
			fmt.Sprintf("Boot Disk: %d GB", diskSize),
			fmt.Sprintf("OpenEBS Disk: %d GB", openebsSize),
		)
	}

	summary.Lines = appendBatchDeploymentLines(summary.Lines, len(plan.VMNames), plan.StartIndex, plan.Concurrent)
	return summary
}

func buildVSphereDeploymentPlan(mode string, configs []vsphere.VMConfig, nodeConfigs []vsphere.K8sNodeConfig, concurrent int, isoPath string) *vsphereDeploymentPlan {
	vmNames := make([]string, 0, len(configs))
	for _, config := range configs {
		vmNames = append(vmNames, config.Name)
	}

	return &vsphereDeploymentPlan{
		Mode:        mode,
		VMNames:     vmNames,
		Configs:     configs,
		NodeConfigs: nodeConfigs,
		Concurrent:  normalizeDeploymentConcurrency(concurrent, len(configs)),
		ISOPath:     isoPath,
	}
}

func buildGenericVSphereDeploymentPlan(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network, isoPath string, concurrent, nodeCount, startIndex int) (*vsphereDeploymentPlan, error) {
	configs, err := buildGenericVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, isoPath, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	return buildVSphereDeploymentPlan("generic", configs, nil, concurrent, isoPath), nil
}

func buildK8sVSphereDeploymentPlan(baseName string, memory, vcpus, diskSize, openebsSize int, network string, concurrent, nodeCount, startIndex int) (*vsphereDeploymentPlan, error) {
	configs, err := buildK8sVSphereVMConfigs(baseName, memory, vcpus, diskSize, openebsSize, network, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	nodeConfigs := make([]vsphere.K8sNodeConfig, 0, len(configs))
	for _, config := range configs {
		nodeConfig, exists := vsphere.GetK8sNodeConfig(config.Name)
		if !exists {
			return nil, fmt.Errorf("no predefined configuration for k8s node: %s", config.Name)
		}
		nodeConfigs = append(nodeConfigs, nodeConfig)
	}

	return buildVSphereDeploymentPlan("k8s", configs, nodeConfigs, concurrent, vsphere.DefaultISOPath()), nil
}

func buildTrueNASVMConfig(name string, memory, vcpus, diskSize, openebsSize int, host, apiKey, isoURL, networkBridge, pool, macAddress, spicePassword, schematicID, talosVersion string, skipZVolCreate, customISO bool) truenas.VMConfig {
	return truenas.VMConfig{
		Name:           name,
		Memory:         memory,
		VCPUs:          vcpus,
		DiskSize:       diskSize,
		OpenEBSSize:    openebsSize,
		TrueNASHost:    host,
		TrueNASAPIKey:  apiKey,
		TrueNASPort:    443,
		NoSSL:          false,
		TalosISO:       isoURL,
		NetworkBridge:  networkBridge,
		StoragePool:    pool,
		MacAddress:     macAddress,
		SkipZVolCreate: skipZVolCreate,
		SpicePassword:  spicePassword,
		UseSpice:       true,
		SchematicID:    schematicID,
		TalosVersion:   talosVersion,
		CustomISO:      customISO,
	}
}

func trueNASPreparedISORequiredError() error {
	return fmt.Errorf("schema-based ISO generation is required for VM deployment. Please use the --generate-iso flag to create a custom Talos ISO, or run 'homeops-cli talos prepare-iso' first to prepare the ISO")
}

func requiredSpicePassword() (string, error) {
	password := getSpicePassword()
	if password == "" {
		return "", fmt.Errorf("SPICE password is required - use SPICE_PASSWORD env var or configure 1Password")
	}
	return password, nil
}

func resolveTrueNASDeploymentAccess(logger *common.ColorLogger) (host, apiKey, spicePassword string, err error) {
	logger.Debug("Retrieving TrueNAS credentials")
	host, apiKey, err = getTrueNASCredentials()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get TrueNAS credentials: %w", err)
	}
	logger.Debug("TrueNAS host: %s", host)

	logger.Debug("Retrieving SPICE password")
	spicePassword, err = requiredSpicePassword()
	if err != nil {
		return "", "", "", err
	}
	logger.Debug("SPICE password retrieved successfully")

	return host, apiKey, spicePassword, nil
}

func connectedTrueNASVMManager(logger *common.ColorLogger, host, apiKey string) (trueNASVMManager, error) {
	logger.Debug("Creating VM manager for TrueNAS host: %s", host)
	vmManager := newTrueNASVMManagerFn(host, apiKey, 443, true)
	if vmManager == nil {
		return nil, fmt.Errorf("failed to create VM manager")
	}

	logger.Debug("Connecting to TrueNAS API")
	if err := vmManager.Connect(); err != nil {
		return nil, fmt.Errorf("TrueNAS connection failed: %w", err)
	}
	logger.Debug("Successfully connected to TrueNAS")
	return vmManager, nil
}

func trueNASNetworkBridge() string {
	return getEnvOrDefault("NETWORK_BRIDGE", "br0")
}

func executeTrueNASVMDeployment(logger *common.ColorLogger, vmManager trueNASVMManager, config truenas.VMConfig) error {
	if err := spinWithFuncFn(fmt.Sprintf("Deploying VM %s", config.Name), func() error {
		logger.Debug("Calling vmManager.DeployVM with configuration")
		if err := vmManager.DeployVM(config); err != nil {
			return fmt.Errorf("VM deployment failed: %w", err)
		}
		return nil
	}); err != nil {
		logger.Error("VM deployment failed: %v", err)
		return err
	}

	return nil
}

func logNamedVMDeploymentStart(logger *common.ColorLogger, provider string, vmNames []string) {
	if len(vmNames) == 1 {
		logger.Info("Starting %s VM deployment: %s", provider, vmNames[0])
		return
	}

	logger.Info("Starting %s deployment for %d VMs: %s", provider, len(vmNames), strings.Join(vmNames, ", "))
}

func logNamedVMDeploymentSuccess(logger *common.ColorLogger, provider string, vmNames []string) {
	if len(vmNames) == 1 {
		logger.Success("VM %s deployed successfully on %s", vmNames[0], provider)
		return
	}

	logger.Success("Deployed %d VMs successfully on %s", len(vmNames), provider)
}

func logTrueNASDeploymentSuccess(logger *common.ColorLogger, config truenas.VMConfig) {
	logger.Success("VM %s deployed successfully!", config.Name)
	logger.Info("VM deployment completed with the following configuration:")
	logger.Info("  VM Name:      %s", config.Name)
	logger.Info("  Memory:       %d MB", config.Memory)
	logger.Info("  vCPUs:        %d", config.VCPUs)
	logger.Info("  Storage Pool: %s", config.StoragePool)
	logger.Info("  Network:      %s", config.NetworkBridge)
	if config.MacAddress != "" {
		logger.Info("  MAC Address:  %s", config.MacAddress)
	}
	logger.Info("  ISO Source:   %s", config.TalosISO)
	if config.CustomISO {
		logger.Info("  Schematic ID: %s", config.SchematicID)
		logger.Info("  Talos Ver:    %s", config.TalosVersion)
	}
	logger.Info("ZVol naming pattern:")
	logger.Info("  Boot disk:   %s/%s-boot (%dGB)", config.StoragePool, config.Name, config.DiskSize)
	if config.OpenEBSSize > 0 {
		logger.Info("  OpenEBS disk: %s/%s-openebs (%dGB)", config.StoragePool, config.Name, config.OpenEBSSize)
	}
}

func prepareGeneratedTrueNASISO(logger *common.ColorLogger) (*trueNASISOSelection, error) {
	logger.Info("STEP 1: Generating custom Talos ISO using schematic.yaml...")

	logger.Debug("Creating Talos factory client")
	factoryClient := newTalosFactoryClientFn()
	if factoryClient == nil {
		return nil, fmt.Errorf("failed to create factory client")
	}

	logger.Debug("Loading schematic from embedded template")
	schematic, err := factoryClient.LoadSchematicFromTemplate()
	if err != nil {
		return nil, fmt.Errorf("failed to load schematic template: %w", err)
	}

	versionConfig := versionconfig.GetVersions(workingDirectoryFn())
	logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=metal", versionConfig.TalosVersion)
	isoInfo, err := factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", "metal")
	if err != nil {
		return nil, fmt.Errorf("ISO generation failed: %w", err)
	}
	if isoInfo == nil {
		return nil, fmt.Errorf("ISO generation returned nil result")
	}

	if err := os.Setenv("SCHEMATIC_ID", isoInfo.SchematicID); err != nil {
		logger.Warn("Failed to set SCHEMATIC_ID environment variable: %v", err)
	} else {
		logger.Debug("Set SCHEMATIC_ID environment variable: %s", isoInfo.SchematicID)
	}

	logger.Success("Custom ISO generated successfully")
	logger.Debug("ISO Details: URL=%s, SchematicID=%s, Version=%s", isoInfo.URL, isoInfo.SchematicID, isoInfo.TalosVersion)
	logger.Info("STEP 2: Downloading custom ISO to TrueNAS (REQUIRED BEFORE VM CREATION)...")

	downloader := newISODownloaderFn()
	downloadConfig := iso.GetDefaultConfig()
	downloadConfig.TrueNASHost = get1PasswordSecret(constants.OpTrueNASHost)
	downloadConfig.TrueNASUsername = get1PasswordSecret(constants.OpTrueNASUsername)
	downloadConfig.ISOURL = isoInfo.URL
	downloadConfig.ISOFilename = fmt.Sprintf("metal-amd64-%s.iso", isoInfo.SchematicID[:8])

	if err := downloader.DownloadCustomISO(downloadConfig); err != nil {
		return nil, fmt.Errorf("CRITICAL: Failed to download custom ISO to TrueNAS - VM deployment cannot proceed: %w", err)
	}

	logger.Success("Custom ISO downloaded to TrueNAS successfully")
	selection := &trueNASISOSelection{
		ISOPath:      filepath.Join(downloadConfig.ISOStoragePath, downloadConfig.ISOFilename),
		SchematicID:  isoInfo.SchematicID,
		TalosVersion: isoInfo.TalosVersion,
		CustomISO:    true,
	}
	logger.Debug("Updated ISO path: %s", selection.ISOPath)
	logger.Info("ISO preparation completed - proceeding with VM deployment...")
	return selection, nil
}

func verifyPreparedTrueNASISO(logger *common.ColorLogger, host string) (*trueNASISOSelection, error) {
	standardISOPath := constants.TrueNASStandardISOPath
	logger.Debug("Checking for prepared ISO at: %s", standardISOPath)

	sshConfig := ssh.SSHConfig{
		Host:       host,
		Username:   get1PasswordSecret(constants.OpTrueNASUsername),
		Port:       "22",
		SSHItemRef: constants.OpTrueNASSSHPrivateKey,
	}
	sshClient := newTrueNASSSHClientFn(sshConfig)

	if err := sshClient.Connect(); err != nil {
		logger.Warn("Cannot verify prepared ISO due to SSH connection failure: %v", err)
		return nil, trueNASPreparedISORequiredError()
	}
	defer func() {
		if closeErr := sshClient.Close(); closeErr != nil {
			logger.Warn("Failed to close SSH client: %v", closeErr)
		}
	}()

	exists, size, err := sshClient.VerifyFile(standardISOPath)
	if err != nil {
		logger.Warn("Failed to verify prepared ISO: %v", err)
		return nil, trueNASPreparedISORequiredError()
	}
	if !exists {
		logger.Info("No prepared ISO found at %s", standardISOPath)
		return nil, fmt.Errorf("no prepared ISO found. Please run 'homeops-cli talos prepare-iso' first to prepare the ISO, or use the --generate-iso flag to generate a new one")
	}

	versionConfig := versionconfig.GetVersions(workingDirectoryFn())
	logger.Success("Using prepared ISO: %s (size: %d bytes)", standardISOPath, size)
	logger.Info("Prepared ISO found - proceeding with VM deployment...")

	return &trueNASISOSelection{
		ISOPath:      standardISOPath,
		TalosVersion: versionConfig.TalosVersion,
		CustomISO:    true,
	}, nil
}

func resolveTrueNASISOSelection(logger *common.ColorLogger, host string, generateISO bool) (*trueNASISOSelection, error) {
	logger.Debug("Determining ISO configuration (generateISO=%t)", generateISO)
	if generateISO {
		return prepareGeneratedTrueNASISO(logger)
	}

	return verifyPreparedTrueNASISO(logger, host)
}

func executeProxmoxDeploymentPlan(logger *common.ColorLogger, host, tokenID, secret, nodeName string, plan *proxmoxDeploymentPlan) error {
	if len(plan.Configs) == 1 || plan.Concurrent == 1 {
		vmManager, err := newProxmoxVMManagerFn(host, tokenID, secret, nodeName, true)
		if err != nil {
			return fmt.Errorf("failed to create Proxmox VM manager: %w", err)
		}
		defer func() {
			if closeErr := vmManager.Close(); closeErr != nil {
				logger.Warn("Failed to close VM manager: %v", closeErr)
			}
		}()

		for _, vmConfig := range plan.Configs {
			if err := vmManager.DeployVM(vmConfig); err != nil {
				return fmt.Errorf("failed to deploy VM %s: %w", vmConfig.Name, err)
			}
		}
		return nil
	}

	logger.Info("Deploying %d Proxmox VMs with concurrency %d", len(plan.Configs), plan.Concurrent)
	return deployProxmoxVMsConcurrently(host, tokenID, secret, nodeName, plan.Configs, plan.Concurrent)
}

func logVSphereGenericSingleVMConfig(logger *common.ColorLogger, config vsphere.VMConfig) {
	logger.Info("Deploying VM: %s", config.Name)
	logger.Info("Enhanced Configuration:")
	logger.Info("  Memory: %d MB", config.Memory)
	logger.Info("  vCPUs: %d", config.VCPUs)
	logger.Info("  Boot Disk: %d GB (thin provisioned: %v)", config.DiskSize, config.ThinProvisioned)
	logger.Info("  OpenEBS Disk: %d GB (thin provisioned: %v)", config.OpenEBSSize, config.ThinProvisioned)
	logger.Info("  Datastore: %s", config.Datastore)
	logger.Info("  Network: %s (vmxnet3)", config.Network)
	logger.Info("  ISO: %s", config.ISO)
	if config.MacAddress != "" {
		logger.Info("  MAC Address: %s", config.MacAddress)
	}
	logger.Info("  IOMMU Enabled: %v", config.EnableIOMMU)
	logger.Info("  CPU Counters Exposed: %v", config.ExposeCounters)
	logger.Info("  Precision Clock: %v", config.EnablePrecisionClock)
	logger.Info("  Watchdog Timer: %v", config.EnableWatchdog)
	logger.Info("  EFI Firmware: enabled")
	logger.Info("  UEFI Secure Boot: disabled")
	logger.Info("  NVME Controllers: 2 (separate for each disk)")
}

func logVSphereGenericParallelPlan(logger *common.ColorLogger, plan *vsphereDeploymentPlan, memory, vcpus, diskSize, openebsSize int, datastore, network string) {
	logger.Info("Deploying %d VMs in parallel (max concurrent: %d)", len(plan.Configs), plan.Concurrent)
	logger.Info("Enhanced VM Configuration (for all VMs):")
	logger.Info("  Memory: %d MB", memory)
	logger.Info("  vCPUs: %d", vcpus)
	logger.Info("  Boot Disk: %d GB (thin provisioned)", diskSize)
	logger.Info("  OpenEBS Disk: %d GB (thin provisioned)", openebsSize)
	logger.Info("  Datastore: %s", datastore)
	logger.Info("  Network: %s (vmxnet3)", network)
	logger.Info("  ISO: %s", plan.ISOPath)
	logger.Info("  IOMMU Enabled: true")
	logger.Info("  CPU Counters Exposed: true")
	logger.Info("  Precision Clock: enabled")
	logger.Info("  Watchdog Timer: enabled")
	logger.Info("  EFI Firmware: enabled")
	logger.Info("  UEFI Secure Boot: disabled")
	logger.Info("  NVME Controllers: 2 (separate for each disk)")
	logger.Info("")
	logger.Info("VMs to deploy:")
	for _, config := range plan.Configs {
		if config.MacAddress != "" {
			logger.Info("  - %s (MAC: %s)", config.Name, config.MacAddress)
		} else {
			logger.Info("  - %s", config.Name)
		}
	}
}

func executeVSphereGenericDeploymentPlan(logger *common.ColorLogger, client vsphereVMDeployer, plan *vsphereDeploymentPlan) error {
	if len(plan.Configs) == 1 {
		if err := client.CreateVM(plan.Configs[0]); err != nil {
			return fmt.Errorf("failed to create VM: %w", err)
		}
		return nil
	}

	if err := client.DeployVMsConcurrently(plan.Configs); err != nil {
		return fmt.Errorf("parallel deployment failed: %w", err)
	}
	return nil
}

func deployVMWithPatternDryRun(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate, generateISO, dryRun bool) error {
	if dryRun {
		logger := common.NewColorLogger()
		summary := buildTrueNASDryRunSummary(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate)
		emitVMDeploymentDryRunSummary(logger, summary, generateISO)
		return nil
	}
	return deployVMWithPattern(name, pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO)
}

func deployVMOnVSphereDryRun(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount, startIndex int, dryRun bool) error {
	if dryRun {
		logger := common.NewColorLogger()
		summary, err := buildVSphereDryRunSummary(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, concurrent, nodeCount, startIndex)
		if err != nil {
			return err
		}
		emitVMDeploymentDryRunSummary(logger, summary, generateISO)
		return nil
	}
	return deployVMOnVSphere(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, startIndex)
}

// deployVMOnProxmoxDryRun handles Proxmox VM deployment with dry-run support
func deployVMOnProxmoxDryRun(baseName string, memory, vcpus, diskSize, openebsSize int, generateISO bool, concurrent, nodeCount, startIndex int, dryRun bool) error {
	logger := common.NewColorLogger()
	plan, err := buildProxmoxDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, concurrent, nodeCount, startIndex)
	if err != nil {
		return err
	}

	if dryRun {
		summary := buildProxmoxDryRunSummary(plan, memory, vcpus, diskSize, openebsSize)
		emitVMDeploymentDryRunSummary(logger, summary, generateISO)
		return nil
	}

	return deployVMOnProxmox(baseName, memory, vcpus, diskSize, openebsSize, generateISO, concurrent, nodeCount, startIndex)
}

// deployVMOnProxmox deploys a VM on Proxmox VE
func deployVMOnProxmox(baseName string, memory, vcpus, diskSize, openebsSize int, generateISO bool, concurrent, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()
	plan, err := buildProxmoxDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, concurrent, nodeCount, startIndex)
	if err != nil {
		return err
	}
	logNamedVMDeploymentStart(logger, "Proxmox VE", plan.VMNames)

	// Get Proxmox credentials
	host, tokenID, secret, nodeName, err := getProxmoxCredentialsFn()
	if err != nil {
		return err
	}

	// Generate ISO if requested
	if generateISO {
		logger.Info("Generating custom Talos ISO...")
		if err := prepareISOForProxmoxFn(); err != nil {
			return fmt.Errorf("failed to prepare ISO: %w", err)
		}
	}

	if err := executeProxmoxDeploymentPlan(logger, host, tokenID, secret, nodeName, plan); err != nil {
		return err
	}

	logNamedVMDeploymentSuccess(logger, "Proxmox VE", plan.VMNames)
	return nil
}

type proxmoxDeploymentPlan struct {
	StartIndex    int
	Concurrent    int
	VMNames       []string
	Configs       []proxmox.VMConfig
	Presets       []proxmox.TalosNodeConfig
	AllPredefined bool
}

func buildProxmoxDeploymentPlan(baseName string, memory, vcpus, diskSize, openebsSize int, concurrent, nodeCount, startIndex int) (*proxmoxDeploymentPlan, error) {
	vmNames, err := buildDeploymentVMNames(baseName, nodeCount, startIndex)
	if err != nil {
		return nil, err
	}

	plan := &proxmoxDeploymentPlan{
		StartIndex: startIndex,
		Concurrent: normalizeDeploymentConcurrency(concurrent, len(vmNames)),
		VMNames:    vmNames,
	}

	configs, presets := buildProxmoxVMConfigs(vmNames, memory, vcpus, diskSize, openebsSize)
	plan.Configs = configs
	plan.Presets = presets
	plan.AllPredefined = len(presets) == len(vmNames)

	return plan, nil
}

func buildProxmoxVMConfigs(vmNames []string, memory, vcpus, diskSize, openebsSize int) ([]proxmox.VMConfig, []proxmox.TalosNodeConfig) {
	configs := make([]proxmox.VMConfig, 0, len(vmNames))
	presets := make([]proxmox.TalosNodeConfig, 0, len(vmNames))
	for _, name := range vmNames {
		nodeConfig, isPredefined := proxmoxGetTalosNodeConfigFn(name)

		var vmConfig proxmox.VMConfig
		if isPredefined {
			presets = append(presets, nodeConfig)
			vmConfig = proxmoxDefaultVMConfig
			vmConfig.Name = name
			vmConfig.BootStorage = nodeConfig.BootStorage
			vmConfig.OpenEBSStorage = nodeConfig.OpenEBSStorage
			vmConfig.CephDiskByID = nodeConfig.CephDiskByID
			vmConfig.CPUAffinity = nodeConfig.CPUAffinity
			vmConfig.NUMANode = nodeConfig.NUMANode
			vmConfig.MacAddress = nodeConfig.MacAddress
		} else {
			vmConfig = proxmoxDefaultVMConfig
			vmConfig.Name = name
			vmConfig.Memory = memory
			vmConfig.Cores = vcpus
			vmConfig.BootDiskSize = diskSize
			vmConfig.OpenEBSSize = openebsSize
		}

		configs = append(configs, vmConfig)
	}

	return configs, presets
}

func normalizeDeploymentConcurrency(concurrent, total int) int {
	if total <= 1 {
		return 1
	}
	if concurrent <= 0 {
		return 1
	}
	if concurrent > total {
		return total
	}
	return concurrent
}

func deployProxmoxVMsConcurrently(host, tokenID, secret, nodeName string, configs []proxmox.VMConfig, concurrent int) error {
	logger := common.NewColorLogger()
	concurrent = normalizeDeploymentConcurrency(concurrent, len(configs))

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, concurrent)
		mu       sync.Mutex
		failures []string
	)

	for _, cfg := range configs {
		cfg := cfg
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			logger.Info("Starting Proxmox deployment worker for %s", cfg.Name)
			vmManager, err := newProxmoxVMManagerFn(host, tokenID, secret, nodeName, true)
			if err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: failed to create Proxmox VM manager: %v", cfg.Name, err))
				mu.Unlock()
				return
			}
			defer func() {
				if closeErr := vmManager.Close(); closeErr != nil {
					logger.Warn("Failed to close Proxmox VM manager for %s: %v", cfg.Name, closeErr)
				}
			}()

			if err := vmManager.DeployVM(cfg); err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", cfg.Name, err))
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	if len(failures) > 0 {
		return fmt.Errorf("failed to deploy %d/%d Proxmox VMs: %s", len(failures), len(configs), strings.Join(failures, "; "))
	}

	return nil
}

// prepareISOForProxmox handles Proxmox-specific ISO preparation
func prepareISOForProxmox() error {
	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	isoFilename := fmt.Sprintf("talos-%s-nocloud-amd64.iso", versionConfig.TalosVersion)
	target := isoPreparationTarget{
		providerName:   "Proxmox",
		platform:       "nocloud",
		uploadStep:     "Uploading ISO to Proxmox storage...",
		uploadSpinner:  "Uploading ISO to Proxmox",
		location:       proxmox.GetISOPath("local", isoFilename),
		deployCommand:  "homeops-cli talos deploy-vm --provider proxmox --name <vm_name> [other flags]",
		summaryMessage: "Custom ISO generated and uploaded to Proxmox local storage",
		uploadISO: func(isoInfo *talos.ISOInfo) error {
			return withProxmoxVMManager(common.NewColorLogger(), func(vmManager proxmoxVMManager) error {
				if err := vmManager.UploadISOFromURL(isoInfo.URL, isoFilename, "local"); err != nil {
					return fmt.Errorf("failed to upload custom ISO to Proxmox: %w", err)
				}
				return nil
			})
		},
	}

	return prepareISOForTargetFn(target)
}

func deployVMWithPattern(name, pool string, memory, vcpus, diskSize, openebsSize int, macAddress string, skipZVolCreate, generateISO bool) error {
	logger := common.NewColorLogger()
	logger.Info("Starting VM deployment: %s", name)
	logger.Debug("VM Configuration: pool=%s, memory=%dMB, vcpus=%d, diskSize=%dGB, openebsSize=%dGB, macAddress=%s, skipZVolCreate=%t, generateISO=%t",
		pool, memory, vcpus, diskSize, openebsSize, macAddress, skipZVolCreate, generateISO)

	// Validate input parameters
	if name == "" {
		return fmt.Errorf("VM name cannot be empty")
	}
	if pool == "" {
		return fmt.Errorf("storage pool cannot be empty")
	}
	if memory <= 0 {
		return fmt.Errorf("memory must be greater than 0, got %d", memory)
	}
	if vcpus <= 0 {
		return fmt.Errorf("vCPUs must be greater than 0, got %d", vcpus)
	}
	if diskSize <= 0 {
		return fmt.Errorf("disk size must be greater than 0, got %d", diskSize)
	}
	if openebsSize < 0 {
		return fmt.Errorf("openebs size cannot be negative, got %d", openebsSize)
	}

	// Validate VM name - no dashes allowed
	logger.Debug("Validating VM name: %s", name)
	if err := validateVMName(name); err != nil {
		return fmt.Errorf("VM name validation failed: %w", err)
	}

	host, apiKey, spicePassword, err := resolveTrueNASDeploymentAccess(logger)
	if err != nil {
		return err
	}

	isoSelection, err := resolveTrueNASISOSelection(logger, host, generateISO)
	if err != nil {
		return err
	}

	vmManager, err := connectedTrueNASVMManager(logger, host, apiKey)
	if err != nil {
		return err
	}

	defer func() {
		logger.Debug("Closing VM manager connection")
		if closeErr := vmManager.Close(); closeErr != nil {
			logger.Warn("Failed to close VM manager: %v", closeErr)
		} else {
			logger.Debug("VM manager connection closed successfully")
		}
	}()

	// Build VM configuration with auto-generated ZVol paths matching the pattern from working scripts
	logger.Debug("Building VM configuration")
	networkBridge := trueNASNetworkBridge()
	logger.Debug("Network bridge: %s", networkBridge)

	config := buildTrueNASVMConfig(name, memory, vcpus, diskSize, openebsSize, host, apiKey, isoSelection.ISOPath, networkBridge, pool, macAddress, spicePassword, isoSelection.SchematicID, isoSelection.TalosVersion, skipZVolCreate, isoSelection.CustomISO)

	logger.Debug("VM configuration built successfully")
	logger.Debug("Configuration summary: Name=%s, Memory=%dMB, vCPUs=%d, ISO=%s, Bridge=%s, Pool=%s",
		name, memory, vcpus, isoSelection.ISOPath, networkBridge, pool)

	// STEP 3: Deploy the VM (ISO is now ready on TrueNAS)
	logger.Info("STEP 3: Starting VM deployment process...")

	if err := executeTrueNASVMDeployment(logger, vmManager, config); err != nil {
		return err
	}
	logTrueNASDeploymentSuccess(logger, config)

	logger.Debug("VM deployment function completed successfully")
	return nil
}

func newManageVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manage-vm",
		Short: "Manage VMs on Proxmox, TrueNAS, or vSphere",
		Long: `Commands for managing VMs on Proxmox VE, TrueNAS Scale, or vSphere/ESXi.

Examples:
  homeops-cli talos manage-vm list --provider proxmox
  homeops-cli talos manage-vm start --provider proxmox --name <vm-name>
  homeops-cli talos manage-vm stop --provider truenas --name <vm-name>
  homeops-cli talos manage-vm info --provider vsphere --name <vm-name>
  homeops-cli talos manage-vm delete --provider proxmox --name <vm-name> --force

Provider prerequisites:
  Proxmox: host, API token, token secret, and node name from environment or 1Password
  TrueNAS: host and API key from environment or 1Password
  vSphere/ESXi: host, username, and password from environment or 1Password`,
	}

	cmd.AddCommand(
		newListVMsCommand(),
		newStartVMCommand(),
		newStopVMCommand(),
		newPowerOnVMCommand(),
		newPowerOffVMCommand(),
		newDeleteVMCommand(),
		newInfoVMCommand(),
		newCleanupZVolsCommand(),
	)

	return cmd
}

func newListVMsCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all VMs on Proxmox, TrueNAS, or vSphere",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "list"); err != nil {
				return err
			}
			return listVMs(provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")

	return cmd
}

func listVMs(provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	logger := common.NewColorLogger()

	switch normalizedProvider {
	case "truenas":
		return withTrueNASVMManager(logger, func(vmManager trueNASVMManager) error {
			return vmManager.ListVMs()
		})

	case "proxmox":
		return withProxmoxVMManager(logger, func(vmManager proxmoxVMManager) error {
			return vmManager.ListVMs()
		})

	case "vsphere":
		return withVSphereClient(logger, func(client vsphereClient) error {
			vms, err := client.ListVMs()
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			fmt.Println("\nVMs on vSphere:")
			fmt.Println("================")
			for _, vm := range vms {
				fmt.Printf("- %s\n", vm.Name())
			}
			fmt.Printf("\nTotal: %d VMs\n", len(vms))

			return nil
		})
	}

	return nil
}

func newStartVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Start a VM on Proxmox, TrueNAS, or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "start"); err != nil {
				return err
			}
			return startVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

// startVMWithProvider starts a VM on the specified provider with interactive selector
func startVMWithProvider(name, provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = chooseVMNameForProvider(name, normalizedProvider, "start")
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	// Call appropriate start function based on provider
	switch normalizedProvider {
	case "truenas":
		return startTrueNASVMFn(name)
	case "proxmox":
		return startProxmoxVMFn(name)
	case "vsphere":
		return powerOnVSphereVMFn(name)
	}

	return nil
}

// stopVMWithProvider stops a VM on the specified provider with interactive selector
func stopVMWithProvider(name, provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = chooseVMNameForProvider(name, normalizedProvider, "stop")
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	switch normalizedProvider {
	case "truenas":
		return stopTrueNASVMFn(name, false)
	case "proxmox":
		return stopProxmoxVMFn(name, false)
	case "vsphere":
		return powerOffVSphereVMFn(name)
	}

	return nil
}

// infoVMWithProvider gets VM info from the specified provider with interactive selector
func infoVMWithProvider(name, provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = chooseVMNameForProvider(name, normalizedProvider, "get info")
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	switch normalizedProvider {
	case "truenas":
		return infoTrueNASVMFn(name)
	case "proxmox":
		return infoProxmoxVMFn(name)
	case "vsphere":
		return infoVSphereVMFn(name)
	}

	return nil
}

// infoVMOnVSphere gets detailed VM information from vSphere/ESXi
func infoVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Getting vSphere/ESXi VM info: %s", vmName)

	return withVSphereClient(logger, func(client vsphereClient) error {
		vm, err := client.FindVM(vmName)
		if err != nil {
			return fmt.Errorf("failed to find VM %s: %w", vmName, err)
		}

		vmInfo, err := client.GetVMInfo(vm)
		if err != nil {
			return fmt.Errorf("failed to get VM info for %s: %w", vmName, err)
		}

		logger.Info("VM Information for: %s", vmName)
		logger.Info("  Power State: %s", vmInfo.Runtime.PowerState)
		logger.Info("  Guest OS: %s", vmInfo.Config.GuestFullName)
		logger.Info("  CPUs: %d", vmInfo.Config.Hardware.NumCPU)
		logger.Info("  Memory: %d MB", vmInfo.Config.Hardware.MemoryMB)
		logger.Info("  UUID: %s", vmInfo.Config.Uuid)

		if vmInfo.Guest != nil && vmInfo.Guest.IpAddress != "" {
			logger.Info("  IP Address: %s", vmInfo.Guest.IpAddress)
		}

		return nil
	})
}

func startVM(name string) error {
	logger := common.NewColorLogger()
	return withTrueNASVMManager(logger, func(vmManager trueNASVMManager) error {
		return vmManager.StartVM(name)
	})
}

func newStopVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Stop a VM on Proxmox, TrueNAS, or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "stop"); err != nil {
				return err
			}
			return stopVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func stopVM(name string, force bool) error {
	logger := common.NewColorLogger()
	return withTrueNASVMManager(logger, func(vmManager trueNASVMManager) error {
		return vmManager.StopVM(name, force)
	})
}

func newDeleteVMCommand() *cobra.Command {
	var (
		name     string
		force    bool
		provider string
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Delete a VM on Proxmox, TrueNAS (with ZVols), or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "delete"); err != nil {
				return err
			}
			return deleteVMWithConfirmation(name, provider, force)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func deleteVMWithConfirmation(name, provider string, force bool) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = chooseVMNameForProvider(name, normalizedProvider, "delete")
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	// Add confirmation for deletion
	if !force {
		var message string
		switch normalizedProvider {
		case "vsphere":
			message = fmt.Sprintf("Delete VM '%s' on vSphere/ESXi? This is destructive!", name)
		case "proxmox":
			message = fmt.Sprintf("Delete VM '%s' on Proxmox? This is destructive!", name)
		default:
			message = fmt.Sprintf("Delete VM '%s' and all its ZVols on TrueNAS? This is destructive!", name)
		}

		confirmed, err := confirmActionFn(message, false)
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			return fmt.Errorf("deletion cancelled")
		}
	}

	switch normalizedProvider {
	case "truenas":
		return deleteTrueNASVMFn(name)
	case "proxmox":
		return deleteProxmoxVMFn(name)
	case "vsphere":
		return deleteVSphereVMFn(name)
	}

	return nil
}

func deleteVM(name string) error {
	logger := common.NewColorLogger()
	storagePool := getEnvOrDefault("STORAGE_POOL", "flashstor")
	return withTrueNASVMManager(logger, func(vmManager trueNASVMManager) error {
		return vmManager.DeleteVM(name, true, storagePool)
	})
}

func newInfoVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Get detailed information about a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Get detailed information about a VM on Proxmox, TrueNAS, or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "info"); err != nil {
				return err
			}
			return infoVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func infoVM(name string) error {
	logger := common.NewColorLogger()
	return withTrueNASVMManager(logger, func(vmManager trueNASVMManager) error {
		return vmManager.GetVMInfo(name)
	})
}

// newPrepareISOCommand creates the prepare-iso command
func newPrepareISOCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "prepare-iso",
		Short: "Generate custom Talos ISO from schematic and upload to storage provider",
		Long: `Generate a custom Talos ISO using schematic.yaml configuration and upload it to the specified provider.
This command will:
1. Generate a Talos schematic from the embedded schematic.yaml template
2. Create a custom ISO from the Talos factory
3. Upload the ISO to Proxmox storage, TrueNAS storage, or a vSphere datastore
4. Update the node configuration templates with the new schematic ID

This separates ISO preparation from VM deployment, allowing you to prepare the ISO once
and deploy multiple VMs using the same custom configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return prepareISOWithProvider(provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Storage provider: proxmox (default), truenas, or vsphere/esxi")

	return cmd
}

type isoPreparationTarget struct {
	providerName   string
	platform       string
	uploadStep     string
	uploadSpinner  string
	location       string
	deployCommand  string
	uploadISO      func(*talos.ISOInfo) error
	summaryMessage string
}

// prepareISOWithProvider handles the ISO generation and upload process for different providers
func prepareISOWithProvider(provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	switch normalizedProvider {
	case "truenas":
		return prepareISOForTrueNASFn()
	case "proxmox":
		return prepareISOForProxmoxFn()
	case "vsphere":
		return prepareISOForVSphereFn()
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

func prepareISOForTarget(target isoPreparationTarget) error {
	logger := common.NewColorLogger()
	logger.Info("Starting custom Talos ISO preparation for %s...", target.providerName)

	versionConfig := versionconfig.GetVersions(common.GetWorkingDirectory())
	logger.Debug("Using versions: Kubernetes=%s, Talos=%s", versionConfig.KubernetesVersion, versionConfig.TalosVersion)

	logger.Debug("Creating Talos factory client")
	factoryClient := newTalosFactoryClientFn()
	if factoryClient == nil {
		return fmt.Errorf("failed to create factory client")
	}

	logger.Info("STEP 1: Loading schematic configuration...")
	schematic, err := factoryClient.LoadSchematicFromTemplate()
	if err != nil {
		return fmt.Errorf("failed to load schematic template: %w", err)
	}
	logger.Success("Schematic configuration loaded successfully")

	logger.Info("STEP 2: Generating custom Talos ISO...")
	var isoInfo *talos.ISOInfo
	err = spinWithFuncFn("Generating custom Talos ISO", func() error {
		logger.Debug("Generating ISO with parameters: version=%s, arch=amd64, platform=%s", versionConfig.TalosVersion, target.platform)
		var genErr error
		isoInfo, genErr = factoryClient.GenerateISOFromSchematic(schematic, versionConfig.TalosVersion, "amd64", target.platform)
		if genErr != nil {
			return fmt.Errorf("ISO generation failed: %w", genErr)
		}
		if isoInfo == nil {
			return fmt.Errorf("ISO generation returned nil result")
		}
		return nil
	})
	if err != nil {
		return err
	}

	logger.Success("Custom ISO generated successfully")
	logger.Info("ISO Details:")
	logger.Info("  URL: %s", isoInfo.URL)
	logger.Info("  Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  Talos Version: %s", isoInfo.TalosVersion)

	logger.Info("STEP 3: %s", target.uploadStep)
	err = spinWithFuncFn(target.uploadSpinner, func() error {
		return target.uploadISO(isoInfo)
	})
	if err != nil {
		return err
	}

	logger.Success("Custom ISO uploaded to %s successfully", target.providerName)
	logger.Info("ISO Location: %s", target.location)

	logger.Info("STEP 4: Updating node configuration templates...")
	if err := updateNodeTemplatesWithSchematicFn(isoInfo.SchematicID, isoInfo.TalosVersion); err != nil {
		logger.Warn("Failed to update node templates: %v", err)
		logger.Warn("You may need to manually update the templates with schematic ID: %s", isoInfo.SchematicID)
	} else {
		logger.Success("Node configuration templates updated successfully")
	}

	logger.Success("ISO preparation completed successfully!")
	logger.Info("Summary:")
	logger.Info("  - %s", target.summaryMessage)
	logger.Info("  - Schematic ID: %s", isoInfo.SchematicID)
	logger.Info("  - Talos Version: %s", isoInfo.TalosVersion)
	logger.Info("  - ISO Path: %s", target.location)
	logger.Info("  - Node templates updated with new schematic ID")
	logger.Info("")
	logger.Info("You can now deploy VMs using: %s", target.deployCommand)
	logger.Info("(The deploy-vm command will automatically use the prepared ISO)")

	return nil
}

// prepareISOForTrueNAS handles TrueNAS-specific ISO preparation
func prepareISOForTrueNAS() error {
	target := isoPreparationTarget{
		providerName:   "TrueNAS",
		platform:       "metal",
		uploadStep:     "Uploading ISO to TrueNAS...",
		uploadSpinner:  "Uploading ISO to TrueNAS",
		location:       constants.TrueNASStandardISOPath,
		deployCommand:  "homeops-cli talos deploy-vm --provider truenas --name <vm_name> [other flags]",
		summaryMessage: "Custom ISO generated and uploaded to TrueNAS",
		uploadISO: func(isoInfo *talos.ISOInfo) error {
			downloader := newISODownloaderFn()
			downloadConfig := iso.GetDefaultConfig()
			downloadConfig.TrueNASHost = get1PasswordSecret(constants.OpTrueNASHost)
			downloadConfig.TrueNASUsername = get1PasswordSecret(constants.OpTrueNASUsername)
			downloadConfig.ISOURL = isoInfo.URL
			downloadConfig.ISOFilename = filepath.Base(constants.TrueNASStandardISOPath)

			if err := downloader.DownloadCustomISO(downloadConfig); err != nil {
				return fmt.Errorf("failed to upload custom ISO to TrueNAS: %w", err)
			}
			return nil
		},
	}

	return prepareISOForTargetFn(target)
}

// prepareISOForVSphere handles vSphere-specific ISO preparation
func prepareISOForVSphere() error {
	target := isoPreparationTarget{
		providerName:   "vSphere",
		platform:       "nocloud",
		uploadStep:     "Uploading ISO to vSphere datastore...",
		uploadSpinner:  "Uploading ISO to vSphere",
		location:       vsphere.DefaultISOPath(),
		deployCommand:  "homeops-cli talos deploy-vm --provider vsphere --name <vm_name> [other flags]",
		summaryMessage: "Custom ISO generated and uploaded to vSphere datastore1",
		uploadISO: func(isoInfo *talos.ISOInfo) error {
			return uploadISOToVSphereFn(isoInfo.URL)
		},
	}

	return prepareISOForTargetFn(target)
}

// uploadISOToVSphere downloads ISO from URL and uploads it to vSphere datastore
func uploadISOToVSphere(isoURL string) error {
	logger := common.NewColorLogger()

	// Download ISO to temporary file
	logger.Info("Downloading ISO from factory...")
	tempFile, err := downloadISOToTemp(isoURL)
	if err != nil {
		return fmt.Errorf("failed to download ISO: %w", err)
	}
	defer func() {
		if err := os.Remove(tempFile); err != nil {
			logger.Warn("Failed to remove temporary file %s: %v", tempFile, err)
		}
	}()

	logger.Success("ISO downloaded to temporary file: %s", tempFile)

	// Upload to vSphere datastore
	return withVSphereClient(logger, func(client vsphereClient) error {
		logger.Info("Uploading ISO to vSphere datastore1...")
		if err := client.UploadISOToDatastore(tempFile, vsphere.DefaultISODatastore, vsphere.DefaultISOFilename); err != nil {
			return fmt.Errorf("failed to upload ISO to datastore: %w", err)
		}

		logger.Success("ISO uploaded to vSphere datastore successfully")
		return nil
	})
}

// downloadISOToTemp downloads ISO from URL to a temporary file and returns the file path
func downloadISOToTemp(isoURL string) (string, error) {
	logger := common.NewColorLogger()

	// Create temporary file
	tempFile, err := os.CreateTemp("", "talos-*.iso")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temporary file: %w", err)
	}

	tempPath := tempFile.Name()
	logger.Debug("Created temporary file: %s", tempPath)

	// Download ISO
	logger.Debug("Downloading from URL: %s", isoURL)
	resp, err := httpGetFn(isoURL)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to download ISO: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to download ISO: HTTP %d", resp.StatusCode)
	}

	// Create file for writing
	outFile, err := os.Create(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer func() { _ = outFile.Close() }()

	// Copy with progress tracking
	size := resp.ContentLength
	if size > 0 {
		logger.Info("Downloading %d MB ISO...", size/(1024*1024))
	}

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to write ISO data: %w", err)
	}

	return tempPath, nil
}

// updateNodeTemplatesWithSchematic updates the controlplane template with the new schematic ID
func updateNodeTemplatesWithSchematic(schematicID, talosVersion string) error {
	logger := common.NewColorLogger()

	// Update controlplane.yaml template with schematic ID
	templateFile := controlplaneTemplatePath
	logger.Debug("Updating controlplane template: %s", templateFile)

	// Read the template file
	content, err := os.ReadFile(templateFile)
	if err != nil {
		return fmt.Errorf("failed to read controlplane template %s: %w", templateFile, err)
	}

	// Build the new factory image URL with the schematic ID
	newFactoryImage := fmt.Sprintf("factory.talos.dev/installer/%s:%s", schematicID, talosVersion)

	// Replace the existing factory image URL with the new schematic-based URL
	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")

	for i, line := range lines {
		if strings.Contains(line, "image: factory.talos.dev/installer/") {
			// Extract the indentation to maintain YAML formatting
			var indent strings.Builder
			for _, char := range line {
				if char == ' ' {
					indent.WriteRune(char)
				} else {
					break
				}
			}
			lines[i] = indent.String() + "image: " + newFactoryImage
			logger.Debug("Updated factory image to: %s", newFactoryImage)
			break
		}
	}

	updatedContent := strings.Join(lines, "\n")

	// Write the updated content back
	if err := os.WriteFile(templateFile, []byte(updatedContent), 0644); err != nil {
		return fmt.Errorf("failed to write controlplane template %s: %w", templateFile, err)
	}

	logger.Debug("Updated controlplane template %s with schematic ID %s", templateFile, schematicID)
	return nil
}

// newCleanupZVolsCommand creates a command to cleanup orphaned ZVols
func newCleanupZVolsCommand() *cobra.Command {
	var (
		vmName      string
		storagePool string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "cleanup-zvols",
		Short: "Clean up orphaned ZVols for a VM that was already deleted",
		Long:  `Clean up orphaned ZVols when a VM was deleted but its ZVols remain. This is useful when VM deletion didn't properly clean up the storage volumes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				confirmed, err := confirmActionFn(fmt.Sprintf("Delete orphaned ZVols for VM '%s'?", vmName), false)
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("cleanup cancelled")
				}
			}
			return cleanupOrphanedZVols(vmName, storagePool)
		},
	}

	cmd.Flags().StringVar(&vmName, "vm-name", "", "Name of the VM whose ZVols to clean up (required)")
	cmd.Flags().StringVar(&storagePool, "pool", "flashstor", "Storage pool (default: flashstor)")
	cmd.Flags().BoolVar(&force, "force", false, "Force cleanup without confirmation")
	_ = cmd.MarkFlagRequired("vm-name")

	return cmd
}

// cleanupOrphanedZVols deletes orphaned ZVols for a VM that no longer exists
func cleanupOrphanedZVols(vmName, storagePool string) error {
	logger := common.NewColorLogger()
	logger.Info("Starting cleanup of orphaned ZVols for VM: %s", vmName)
	return withTrueNASVMManager(logger, func(vmManager trueNASVMManager) error {
		if err := vmManager.CleanupOrphanedZVols(vmName, storagePool); err != nil {
			return fmt.Errorf("failed to cleanup orphaned ZVols: %w", err)
		}

		logger.Success("Successfully cleaned up orphaned ZVols for VM: %s", vmName)
		return nil
	})
}

// deployVMOnVSphere deploys one or more VMs on vSphere/ESXi
func deployVMOnVSphere(baseName string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()
	logger.Info("Starting vSphere/ESXi VM deployment with enhanced configuration")

	host, err := getVSphereHostFn()
	if err != nil {
		return err
	}

	// Check if this is a k8s node deployment - use SSH-based method for exact config match.
	// Batch deployments commonly use the shared base name "k8s", which expands to k8s-0, k8s-1, ...
	isK8sNode := strings.HasPrefix(baseName, "k8s")
	if isK8sNode {
		return deployK8sVMViaSSH(baseName, host, memory, vcpus, diskSize, openebsSize, network, generateISO, nodeCount, startIndex)
	}

	// For non-k8s VMs, use the standard govmomi approach
	return deployGenericVMOnVSphere(baseName, host, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, generateISO, concurrent, nodeCount, startIndex)
}

// deployK8sVMViaSSH deploys k8s VMs using SSH for exact configuration control
// This ensures the VMs match the existing manually-deployed production VMs exactly
func deployK8sVMViaSSH(baseName string, host string, memory, vcpus, diskSize, openebsSize int, network string, _generateISO bool, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()
	logger.Info("Deploying k8s VM(s) via SSH with production configuration")

	// Create ESXi SSH client (fetches SSH key from 1Password)
	esxiClient, err := newESXiK8sVMDeployerFn(host, "root")
	if err != nil {
		return fmt.Errorf("failed to create ESXi SSH client: %w", err)
	}
	defer esxiClient.Close() // Clean up SSH key file

	plan, err := buildK8sVSphereDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, network, nodeCount, nodeCount, startIndex)
	if err != nil {
		return err
	}

	// Deploy each VM
	for idx, config := range plan.Configs {
		nodeConfig := plan.NodeConfigs[idx]

		logger.Info("Deploying %s with production configuration:", config.Name)
		logger.Info("  Boot Datastore: %s", nodeConfig.BootDatastore)
		logger.Info("  OpenEBS Datastore: truenas-iscsi")
		logger.Info("  RDM (Ceph): %s", nodeConfig.RDMPath)
		logger.Info("  SR-IOV PCI: %s", nodeConfig.PCIDevice)
		logger.Info("  MAC Address: %s", nodeConfig.MacAddress)
		logger.Info("  CPU Affinity: %s", nodeConfig.CPUAffinity)
		logger.Info("  Memory: %d MB (%d GB) - pinned reservation", config.Memory, config.Memory/1024)
		logger.Info("  vCPUs: %d", config.VCPUs)
		logger.Info("  Boot Disk: %d GB", config.DiskSize)
		logger.Info("  OpenEBS Disk: %d GB", config.OpenEBSSize)

		// Create the VM
		if err := esxiClient.CreateK8sVM(config); err != nil {
			return fmt.Errorf("failed to create VM %s: %w", config.Name, err)
		}

		logger.Success("VM %s deployed successfully!", config.Name)
	}

	return nil
}

// deployGenericVMOnVSphere deploys non-k8s VMs using govmomi (legacy behavior)
func deployGenericVMOnVSphere(baseName string, host string, memory, vcpus, diskSize, openebsSize int, macAddress, datastore, network string, generateISO bool, concurrent, nodeCount, startIndex int) error {
	logger := common.NewColorLogger()

	_, username, password, err := getVSphereCredsFn()
	if err != nil {
		return err
	}

	client, err := newVSphereDeployerFn(host, username, password)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("Failed to close vSphere connection: %v", err)
		}
	}()

	// Handle ISO generation if requested
	var isoPath string
	if generateISO {
		logger.Info("Generating custom Talos ISO...")
		logger.Warn("For vSphere, please ensure the ISO is already uploaded to the datastore")
		logger.Warn("Run 'homeops-cli talos prepare-iso' first if needed")
		isoPath = vsphere.DefaultISOPath()
	} else {
		isoPath = vsphere.DefaultISOPath()
	}

	plan, err := buildGenericVSphereDeploymentPlan(baseName, memory, vcpus, diskSize, openebsSize, macAddress, datastore, network, isoPath, concurrent, nodeCount, startIndex)
	if err != nil {
		return err
	}

	if len(plan.Configs) == 1 {
		logVSphereGenericSingleVMConfig(logger, plan.Configs[0])
	} else {
		logVSphereGenericParallelPlan(logger, plan, memory, vcpus, diskSize, openebsSize, datastore, network)
	}

	if err := executeVSphereGenericDeploymentPlan(logger, client, plan); err != nil {
		return err
	}

	if len(plan.Configs) == 1 {
		logger.Success("VM %s deployed successfully with enhanced configuration!", plan.Configs[0].Name)
	} else {
		logger.Success("Successfully deployed %d VMs with enhanced configuration!", len(plan.Configs))
	}

	return nil
}

// deleteVMOnVSphere deletes a VM from vSphere/ESXi
func deleteVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Starting vSphere/ESXi VM deletion for: %s", vmName)
	return withVSphereClient(logger, func(client vsphereClient) error {
		vm, err := client.FindVM(vmName)
		if err != nil {
			return fmt.Errorf("failed to find VM %s: %w", vmName, err)
		}

		logger.Info("Found VM: %s", vmName)
		if err := client.DeleteVM(vm); err != nil {
			return fmt.Errorf("failed to delete VM %s: %w", vmName, err)
		}

		logger.Success("VM %s deleted successfully!", vmName)
		return nil
	})
}

// startVMOnProxmox starts a VM on Proxmox VE
func startVMOnProxmox(name string) error {
	logger := common.NewColorLogger()
	logger.Info("Starting Proxmox VM: %s", name)
	return withProxmoxVMManager(logger, func(vmManager proxmoxVMManager) error {
		return vmManager.StartVM(name)
	})
}

// stopVMOnProxmox stops a VM on Proxmox VE
func stopVMOnProxmox(name string, force bool) error {
	logger := common.NewColorLogger()
	logger.Info("Stopping Proxmox VM: %s", name)
	return withProxmoxVMManager(logger, func(vmManager proxmoxVMManager) error {
		return vmManager.StopVM(name, force)
	})
}

// deleteVMOnProxmox deletes a VM on Proxmox VE
func deleteVMOnProxmox(name string) error {
	logger := common.NewColorLogger()
	logger.Info("Deleting Proxmox VM: %s", name)
	return withProxmoxVMManager(logger, func(vmManager proxmoxVMManager) error {
		return vmManager.DeleteVM(name)
	})
}

// infoVMOnProxmox gets detailed VM information from Proxmox VE
func infoVMOnProxmox(name string) error {
	logger := common.NewColorLogger()
	logger.Info("Getting Proxmox VM info: %s", name)
	return withProxmoxVMManager(logger, func(vmManager proxmoxVMManager) error {
		return vmManager.GetVMInfo(name)
	})
}

func newPowerOnVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "poweron",
		Short: "Power on a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Power on a VM on Proxmox, TrueNAS, or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "poweron"); err != nil {
				return err
			}
			return powerOnVM(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

func newPowerOffVMCommand() *cobra.Command {
	var (
		name     string
		provider string
	)

	cmd := &cobra.Command{
		Use:   "poweroff",
		Short: "Power off a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Power off a VM on Proxmox, TrueNAS, or vSphere/ESXi. If --name is not specified, presents an interactive selector.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureVMLifecycleProviderFn(provider, "poweroff"); err != nil {
				return err
			}
			return powerOffVM(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "proxmox", "Virtualization provider: proxmox (default), vsphere/esxi, or truenas")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", completion.ValidVMNames)

	return cmd
}

// powerOnVM powers on a VM on the specified provider with interactive selector
func powerOnVM(name, provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = chooseVMNameForProvider(name, normalizedProvider, "power on")
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	switch normalizedProvider {
	case "truenas":
		return startTrueNASVMFn(name)
	case "proxmox":
		return startProxmoxVMFn(name)
	case "vsphere":
		return powerOnVSphereVMFn(name)
	}

	return nil
}

// powerOffVM powers off a VM on the specified provider with interactive selector
func powerOffVM(name, provider string) error {
	normalizedProvider, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = chooseVMNameForProvider(name, normalizedProvider, "power off")
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	switch normalizedProvider {
	case "truenas":
		return stopTrueNASVMFn(name, true)
	case "proxmox":
		return stopProxmoxVMFn(name, true)
	case "vsphere":
		return powerOffVSphereVMFn(name)
	}

	return nil
}

func powerOnVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Powering on vSphere/ESXi VM: %s", vmName)
	return withVSphereClient(logger, func(client vsphereClient) error {
		vm, err := client.FindVM(vmName)
		if err != nil {
			return fmt.Errorf("failed to find VM %s: %w", vmName, err)
		}

		if err := client.PowerOnVM(vm); err != nil {
			return fmt.Errorf("failed to power on VM %s: %w", vmName, err)
		}

		logger.Success("VM %s powered on successfully!", vmName)
		return nil
	})
}

// powerOffVMOnVSphere powers off a VM on vSphere/ESXi
func powerOffVMOnVSphere(vmName string) error {
	logger := common.NewColorLogger()
	logger.Info("Powering off vSphere/ESXi VM: %s", vmName)
	return withVSphereClient(logger, func(client vsphereClient) error {
		vm, err := client.FindVM(vmName)
		if err != nil {
			return fmt.Errorf("failed to find VM %s: %w", vmName, err)
		}

		if err := client.PowerOffVM(vm); err != nil {
			return fmt.Errorf("failed to power off VM %s: %w", vmName, err)
		}

		logger.Success("VM %s powered off successfully!", vmName)
		return nil
	})
}
