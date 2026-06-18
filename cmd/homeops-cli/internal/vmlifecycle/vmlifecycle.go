// Package vmlifecycle provides the provider-generic VM lifecycle foundation
// (credentials, manager interfaces, lifecycle construction/dispatch, and
// provider name resolution) shared by the top-level `vm` command and the
// legacy Talos deploy-vm/prepare-iso commands. It is hypervisor-aware but
// guest-OS agnostic: it operates on Proxmox, TrueNAS, and vSphere VMs
// regardless of what they run.
package vmlifecycle

import (
	"fmt"
	"os"
	"strings"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vsphere"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
)

// Seam variables for dependency injection in tests.
var (
	ChooseVMFunc            = ui.Choose
	GetTrueNASVMNamesFn     = getTrueNASVMNames
	GetProxmoxVMNamesFn     = getProxmoxVMNames
	GetESXiVMNamesFn        = GetESXiVMNames
	VSphereGetVMNamesFn     = vsphere.GetVMNames
	GetProxmoxCredentialsFn = proxmox.GetCredentials
	ResolveSecretKeyFn      = func(key string) string {
		return versionconfig.Get().ResolveSecretSilent(key)
	}
	GetVSphereCredsFn           = GetVSphereCredentials
	GetVSphereHostFn            = GetVSphereHost
	GetTrueNASCredentialsFn     = GetTrueNASCredentials
	EnsureVMLifecycleProviderFn = ensureVMLifecycleProviderAvailable
	NewVMLifecycleFn            = newVMLifecycle
	NewTrueNASVMManagerFn       = func(host, apiKey string, port int, useSSL bool) TrueNASVMManager {
		return truenas.NewVMManager(host, apiKey, port, useSSL)
	}
	NewProxmoxVMManagerFn = func(host, tokenID, secret, nodeName string, insecure bool) (ProxmoxVMManager, error) {
		return proxmox.NewVMManager(host, tokenID, secret, nodeName, insecure)
	}
	NewVSphereClientFn = func(host, username, password string, insecure bool) VSphereClient {
		return vsphere.NewClient(host, username, password, insecure)
	}
	NewVSphereVMManagerFn = func(host, username, password string, insecure bool) (vmprov.VMLifecycle, error) {
		return vsphere.NewVMManager(host, username, password, insecure)
	}
)

type TrueNASVMManager interface {
	Connect() error
	Close() error
	DeployVM(truenas.VMConfig) error
	ListVMs() error
	VMSummaries() ([]vmprov.VMSummary, error)
	StartVM(string) error
	StopVM(string, bool) error
	RestartVM(string) error
	DeleteVM(string, bool, string) error
	GetVMInfo(string) error
	SetVMResources(string, int, int) error
	ResizeVMDisk(string, string, string) error
	SnapshotVM(string, string) error
	ListVMSnapshots(string) error
	RollbackVM(string, string) error
	DeleteVMSnapshot(string, string) error
	Clone(string, string, vmprov.CloneOptions) error
	VMIPAddresses(string) ([]string, error)
	ConsoleURL(string) (string, error)
	Capabilities() vmprov.Capabilities
	CleanupOrphanedZVols(string, string) error
}

type ProxmoxVMManager interface {
	vmprov.VMLifecycle
	UploadISOFromURL(string, string, string) error
	DeployVM(proxmox.VMConfig) error
	ImportTemplate(proxmox.VMConfig) error
	ConvertVMToTemplate(string) error
	ConsoleURLs(string) (string, string, error)
}

type VSphereClient interface {
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

// GetEnvOrDefault returns the value of an environment variable or a default value
func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetTrueNASCredentials retrieves TrueNAS credentials through the shared
// provider client (configured secret references + env fallback).
func GetTrueNASCredentials() (host, apiKey string, err error) {
	return truenas.GetCredentials()
}

// ResolveSecretKey resolves a semantic secret key through the homeops config
// ("" on miss).
func ResolveSecretKey(key string) string {
	return ResolveSecretKeyFn(key)
}

// TruenasDefaultPool returns the configured TrueNAS VM storage pool
// (hypervisors.truenas.vm.boot_storage in homeops.yaml), or fallback.
func TruenasDefaultPool(fallback string) string {
	if p := versionconfig.Get().Hypervisors.TrueNAS.VM.BootStorage; p != "" {
		return p
	}
	return fallback
}

// GetSpicePassword retrieves the SPICE password through the configured secret
// reference, with environment-variable fallback.
func GetSpicePassword() string {
	password := ResolveSecretKey(versionconfig.KeyTrueNASSpicePassword)
	if password != "" {
		return password
	}
	return os.Getenv(constants.EnvSPICEPassword)
}

func GetVSphereCredentials() (host, username, password string, err error) {
	host = ResolveSecretKey(versionconfig.KeyVSphereHost)
	username = ResolveSecretKey(versionconfig.KeyVSphereUsername)
	password = ResolveSecretKey(versionconfig.KeyVSpherePassword)

	if host == "" {
		host = os.Getenv(constants.EnvVSphereHost)
	}
	if username == "" {
		username = os.Getenv(constants.EnvVSphereUsername)
	}
	if password == "" {
		password = os.Getenv(constants.EnvVSpherePassword)
	}

	if host == "" || username == "" || password == "" {
		return "", "", "", fmt.Errorf("vSphere credentials not found: configure secrets.%s/%s/%s in your homeops config or set %s/%s/%s ('homeops-cli config doctor' shows what resolves)",
			versionconfig.KeyVSphereHost, versionconfig.KeyVSphereUsername, versionconfig.KeyVSpherePassword,
			constants.EnvVSphereHost, constants.EnvVSphereUsername, constants.EnvVSpherePassword)
	}

	return host, username, password, nil
}

func WithTrueNASVMManager(logger *common.ColorLogger, fn func(TrueNASVMManager) error) error {
	host, apiKey, err := GetTrueNASCredentialsFn()
	if err != nil {
		return err
	}

	vmManager := NewTrueNASVMManagerFn(host, apiKey, 443, true)
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

func WithProxmoxVMManager(logger *common.ColorLogger, fn func(ProxmoxVMManager) error) error {
	host, tokenID, secret, nodeName, err := GetProxmoxCredentialsFn()
	if err != nil {
		return err
	}

	vmManager, err := NewProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
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

func WithVSphereClient(logger *common.ColorLogger, fn func(VSphereClient) error) error {
	host, username, password, err := GetVSphereCredsFn()
	if err != nil {
		return err
	}

	client := NewVSphereClientFn(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false))
	if err := client.Connect(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false)); err != nil {
		return fmt.Errorf("failed to connect to vSphere: %w", err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			logger.Warn("Failed to close vSphere connection: %v", closeErr)
		}
	}()

	return fn(client)
}

// truenasLifecycleAdapter narrows the TrueNAS manager to the shared
// provider.VMLifecycle contract. TrueNAS deletion takes storage options;
// they are fixed at construction because the CLI runs one lifecycle
// operation per invocation.
type truenasLifecycleAdapter struct {
	TrueNASVMManager
	deleteZVols bool
	storagePool string
}

func (a truenasLifecycleAdapter) DeleteVM(name string) error {
	return a.TrueNASVMManager.DeleteVM(name, a.deleteZVols, a.storagePool)
}

var _ vmprov.VMLifecycle = truenasLifecycleAdapter{}

// newVMLifecycle builds the lifecycle implementation for a normalized
// provider name. All VM lifecycle dispatch (list/start/stop/info/delete/
// poweron/poweroff) funnels through here instead of per-action switches.
func newVMLifecycle(normalizedProvider string) (vmprov.VMLifecycle, error) {
	switch normalizedProvider {
	case "truenas":
		host, apiKey, err := GetTrueNASCredentialsFn()
		if err != nil {
			return nil, err
		}
		vmManager := NewTrueNASVMManagerFn(host, apiKey, 443, true)
		if err := vmManager.Connect(); err != nil {
			return nil, fmt.Errorf("failed to connect to TrueNAS: %w", err)
		}
		return truenasLifecycleAdapter{
			TrueNASVMManager: vmManager,
			deleteZVols:      true,
			storagePool:      GetEnvOrDefault("STORAGE_POOL", TruenasDefaultPool("flashstor")),
		}, nil
	case "proxmox":
		host, tokenID, secret, nodeName, err := GetProxmoxCredentialsFn()
		if err != nil {
			return nil, err
		}
		vmManager, err := NewProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
		if err != nil {
			return nil, fmt.Errorf("failed to create Proxmox VM manager: %w", err)
		}
		return vmManager, nil
	case "vsphere":
		host, username, password, err := GetVSphereCredsFn()
		if err != nil {
			return nil, err
		}
		return NewVSphereVMManagerFn(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false))
	}
	return nil, fmt.Errorf("unsupported provider: %s", normalizedProvider)
}

// WithVMLifecycle runs fn against a freshly constructed provider lifecycle
// and always closes it afterwards.
func WithVMLifecycle(normalizedProvider string, fn func(vmprov.VMLifecycle) error) error {
	lifecycle, err := NewVMLifecycleFn(normalizedProvider)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lifecycle.Close(); closeErr != nil {
			common.NewColorLogger().Warn("Failed to close VM manager: %v", closeErr)
		}
	}()
	return fn(lifecycle)
}

// RunVMLifecycleAction normalizes the provider, resolves the VM name
// (prompting interactively when not given), then runs op against the
// provider's lifecycle implementation.
func RunVMLifecycleAction(name, providerName, action string, op func(vmprov.VMLifecycle, string) error) error {
	normalizedProvider, err := NormalizeVMProvider(providerName)
	if err != nil {
		return err
	}

	name, err = ChooseVMNameForProvider(name, normalizedProvider, action)
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}

	return WithVMLifecycle(normalizedProvider, func(lifecycle vmprov.VMLifecycle) error {
		return op(lifecycle, name)
	})
}

// GetESXiVMNames retrieves the list of VM names from ESXi/vSphere
func GetESXiVMNames() ([]string, error) {
	return VSphereGetVMNamesFn()
}

func NormalizeVMProvider(provider string) (string, error) {
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

func GetVMNamesForProvider(provider string) ([]string, error) {
	normalized, err := NormalizeVMProvider(provider)
	if err != nil {
		return nil, err
	}

	switch normalized {
	case "truenas":
		return GetTrueNASVMNamesFn()
	case "proxmox":
		return GetProxmoxVMNamesFn()
	case "vsphere":
		return GetESXiVMNamesFn()
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
		return fmt.Sprintf("secrets.%s/%s in your homeops config, or %s/%s environment variables",
			versionconfig.KeyTrueNASHost, versionconfig.KeyTrueNASAPIKey,
			constants.EnvTrueNASHost, constants.EnvTrueNASAPIKey)
	case "vsphere":
		return fmt.Sprintf("secrets.%s/%s/%s in your homeops config, or %s/%s/%s environment variables",
			versionconfig.KeyVSphereHost, versionconfig.KeyVSphereUsername, versionconfig.KeyVSpherePassword,
			constants.EnvVSphereHost, constants.EnvVSphereUsername, constants.EnvVSpherePassword)
	default:
		return fmt.Sprintf("secrets.%s/%s/%s in your homeops config, or the PROXMOX_* environment variables",
			versionconfig.KeyProxmoxHost, versionconfig.KeyProxmoxTokenID, versionconfig.KeyProxmoxTokenSecret)
	}
}

func ensureVMLifecycleProviderAvailable(provider, action string) error {
	normalizedProvider, err := NormalizeVMProvider(provider)
	if err != nil {
		return err
	}

	var capabilityErr error
	switch normalizedProvider {
	case "truenas":
		_, _, capabilityErr = GetTrueNASCredentialsFn()
	case "proxmox":
		_, _, _, _, capabilityErr = GetProxmoxCredentialsFn()
	case "vsphere":
		_, _, _, capabilityErr = GetVSphereCredsFn()
	}

	if capabilityErr == nil {
		return nil
	}

	hint := ""
	if versionconfig.Get().Source == "" {
		hint = " No homeops.yaml found — run 'homeops-cli config init' to scaffold one."
	}
	return fmt.Errorf("%s VM lifecycle commands require %s: %w.%s Use `homeops-cli vm %s --provider %s --name <vm-name>` after configuring prerequisites",
		vmProviderDisplayName(normalizedProvider),
		vmProviderPrerequisites(normalizedProvider),
		capabilityErr,
		hint,
		action,
		normalizedProvider)
}

func ChooseVMNameForProvider(name, provider, action string) (string, error) {
	if strings.TrimSpace(name) != "" {
		return name, nil
	}

	vmNames, err := GetVMNamesForProvider(provider)
	if err != nil {
		return "", err
	}

	selectedVM, err := ChooseVMFunc(fmt.Sprintf("Select VM to %s:", action), vmNames)
	if err != nil {
		if ui.IsCancellation(err) {
			return "", nil
		}
		return "", fmt.Errorf("VM selection failed: %w", err)
	}

	return selectedVM, nil
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
	client, err := proxmox.NewClient(host, tokenID, secret, common.EnvBool(constants.EnvProxmoxInsecure, false))
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

func GetVSphereHost() (string, error) {
	host := ResolveSecretKey(versionconfig.KeyVSphereHost)
	if host == "" {
		host = os.Getenv(constants.EnvVSphereHost)
	}
	if host == "" {
		return "", fmt.Errorf("ESXi host not found. Please set %s environment variable or configure 1Password", constants.EnvVSphereHost)
	}

	return host, nil
}

// DefaultProviderName returns hypervisors.default from homeops.yaml.
func DefaultProviderName() string {
	if p := versionconfig.Get().Hypervisors.Default; p != "" {
		return p
	}
	return "proxmox"
}
