// Package vm implements the top-level `vm` command group for managing VM
// lifecycle on Proxmox, TrueNAS, or vSphere. It is hypervisor-aware but
// guest-OS agnostic and builds on the shared internal/vmlifecycle foundation.
package vm

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"homeops-cli/internal/common"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// confirmActionFn is the confirmation seam for destructive vm operations.
var confirmActionFn = ui.Confirm

func NewVMLifecycleRootGuidanceCommand(action string) *cobra.Command {
	var (
		provider string
		name     string
		force    bool
	)

	cmd := &cobra.Command{
		Use:    action,
		Hidden: true,
		Short:  fmt.Sprintf("Use 'homeops-cli vm %s'", action),
		RunE: func(cmd *cobra.Command, args []string) error {
			normalizedProvider, err := vmlifecycle.NormalizeVMProvider(provider)
			if err != nil {
				normalizedProvider = provider
			}

			example := fmt.Sprintf("homeops-cli vm %s --provider %s", action, normalizedProvider)
			if strings.TrimSpace(name) != "" {
				example += fmt.Sprintf(" --name %s", name)
			} else if action != "list" {
				example += " --name <vm-name>"
			}
			if action == "delete" && force {
				example += " --force"
			}

			return fmt.Errorf("VM lifecycle commands moved to the top-level `vm` group; use `%s`", example)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	if action != "list" {
		cmd.Flags().StringVar(&name, "name", "", "VM name")
	}
	if action == "delete" {
		cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")
	}

	return cmd
}

// vmVerbGroups organizes the lifecycle verbs in help output.
var vmVerbGroups = map[string]string{
	"create": "provision", "template": "provision", "clone": "provision",
	"set": "day2", "resize-disk": "day2", "snapshot": "day2", "cleanup-zvols": "day2",
	"list": "power", "start": "power", "stop": "power", "poweron": "power",
	"poweroff": "power", "restart": "power", "delete": "power", "info": "power",
	"ip": "access", "ssh": "access", "console": "access",
}

func addVMVerbGroups(cmd *cobra.Command, subcommands []*cobra.Command) {
	cmd.AddGroup(
		&cobra.Group{ID: "provision", Title: "Provisioning:"},
		&cobra.Group{ID: "day2", Title: "Day-2 operations:"},
		&cobra.Group{ID: "power", Title: "Power & lifecycle:"},
		&cobra.Group{ID: "access", Title: "Access:"},
	)
	for _, sub := range subcommands {
		if g, ok := vmVerbGroups[sub.Name()]; ok {
			sub.GroupID = g
		}
	}
	cmd.AddCommand(subcommands...)
}

// NewVMCommand exposes the VM platform as the top-level `vm` command group,
// organized provider-first: `vm proxmox|truenas|vsphere <verb>`. The same
// verbs also exist directly under `vm` as hidden shorthands that act on
// hypervisors.default (kept for scripts and muscle memory).
func NewVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Manage VM lifecycle on Proxmox, TrueNAS, or vSphere",
		Long: `VM platform, organized per provider: pick the hypervisor, then the verb.
Works on any VM regardless of OS (Flatcar, Talos, or other).

The verbs also work directly under vm (e.g. 'vm list'), acting on
hypervisors.default from homeops.yaml.`,
		Example: `  homeops-cli vm proxmox list
  homeops-cli vm proxmox create --name dev-vm --os ubuntu
  homeops-cli vm truenas snapshot create --name web0 --snap pre-upgrade
  homeops-cli vm vsphere info --name vc0
  homeops-cli vm list                  # shorthand: hypervisors.default`,
	}
	// Provider-first groups are the visible structure.
	for _, p := range []string{"proxmox", "truenas", "vsphere"} {
		cmd.AddCommand(newProviderScopedVMGroup(p))
	}
	// Flat verbs stay as hidden shorthands for the default provider. cleanup-zvols
	// is a TrueNAS-only operation (it has no --provider flag and always talks to
	// the NAS); exposing it as a flat default-provider shorthand would silently
	// hit TrueNAS even when hypervisors.default is proxmox/vsphere, so keep it
	// reachable only under `vm truenas cleanup-zvols`.
	for _, sub := range vmLifecycleSubcommands() {
		if sub.Name() == "cleanup-zvols" {
			continue
		}
		sub.Hidden = true
		cmd.AddCommand(sub)
	}
	return cmd
}

// vmLifecycleSubcommands builds one fresh set of the lifecycle commands,
// each with live VM-name completion wired onto its --name/positional.
func vmLifecycleSubcommands() []*cobra.Command {
	cmds := lifecycleSubcommandSet()
	for _, c := range cmds {
		registerVMNameCompletion(c)
	}
	return cmds
}

func lifecycleSubcommandSet() []*cobra.Command {
	return []*cobra.Command{
		newCreateVMCommand(),
		newVMTemplateCommand(),
		newCloneVMCommand(),
		newSnapshotCommand(),
		newVMIPCommand(),
		newVMSSHCommand(),
		newVMConsoleCommand(),
		newSetVMCommand(),
		newResizeDiskCommand(),
		newRestartVMCommand(),
		newListVMsCommand(),
		newStartVMCommand(),
		newStopVMCommand(),
		newPowerOnVMCommand(),
		newPowerOffVMCommand(),
		newDeleteVMCommand(),
		newInfoVMCommand(),
		newCleanupZVolsCommand(),
	}
}

// newProviderScopedVMGroup builds one provider's verb set with --provider
// pinned to that hypervisor (and the flag hidden), e.g. `vm truenas list`.
func newProviderScopedVMGroup(provider string) *cobra.Command {
	group := &cobra.Command{
		Use:   provider,
		Short: fmt.Sprintf("VM operations on %s", provider),
		Example: fmt.Sprintf(`  homeops-cli vm %[1]s list
  homeops-cli vm %[1]s create --name dev0 --os ubuntu
  homeops-cli vm %[1]s snapshot create --name dev0 --snap pre-upgrade`, provider),
	}
	var subcommands []*cobra.Command
	for _, sub := range vmLifecycleSubcommands() {
		// TrueNAS-only verbs don't belong under the other providers.
		if sub.Name() == "cleanup-zvols" && provider != "truenas" {
			continue
		}
		// --provider may be a local flag (most verbs) or a persistent one
		// (the snapshot group); pin whichever exists.
		f := sub.Flags().Lookup("provider")
		if f == nil {
			f = sub.PersistentFlags().Lookup("provider")
		}
		if f != nil {
			_ = f.Value.Set(provider)
			f.DefValue = provider
			f.Hidden = true
		}
		subcommands = append(subcommands, sub)
	}
	addVMVerbGroups(group, subcommands)
	return group
}

func NewManageVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:        "manage-vm",
		Short:      "Manage VMs (deprecated: use top-level 'homeops-cli vm')",
		Deprecated: "use 'homeops-cli vm' instead",
		Hidden:     true,
		Long: `Commands for managing VMs on Proxmox VE, TrueNAS Scale, or vSphere/ESXi.

Examples:
  homeops-cli vm list --provider proxmox
  homeops-cli vm start --provider proxmox --name <vm-name>
  homeops-cli vm stop --provider truenas --name <vm-name>
  homeops-cli vm info --provider vsphere --name <vm-name>
  homeops-cli vm delete --provider proxmox --name <vm-name> --force

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
	var provider, output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all VMs on Proxmox, TrueNAS, or vSphere",
		Example: `  homeops-cli vm proxmox list
  homeops-cli vm truenas list --output json | jq '.vms[].name'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "list"); err != nil {
				return err
			}
			return listVMs(provider, output)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table, json, or yaml")

	return cmd
}

// vmInventory is the machine-readable shape of `vm list --output json|yaml`.
type vmInventory struct {
	Provider string             `json:"provider" yaml:"provider"`
	VMs      []vmprov.VMSummary `json:"vms" yaml:"vms"`
}

func listVMs(provider, output string) error {
	normalizedProvider, err := vmlifecycle.NormalizeVMProvider(provider)
	if err != nil {
		return err
	}

	return vmlifecycle.WithVMLifecycle(normalizedProvider, func(lifecycle vmprov.VMLifecycle) error {
		summaries, err := lifecycle.VMSummaries()
		if err != nil {
			return err
		}
		return renderVMInventory(vmInventory{Provider: normalizedProvider, VMs: summaries}, output)
	})
}

func renderVMInventory(inventory vmInventory, output string) error {
	switch output {
	case "", "table":
		if len(inventory.VMs) == 0 {
			fmt.Println("No virtual machines found.")
			return nil
		}
		rows := make([][]string, 0, len(inventory.VMs))
		for _, s := range inventory.VMs {
			details := make([]string, 0, len(s.Details))
			for _, k := range slices.Sorted(maps.Keys(s.Details)) {
				details = append(details, k+"="+s.Details[k])
			}
			rows = append(rows, []string{
				s.Name, s.ID, s.Status,
				fmt.Sprintf("%d", s.MemoryMB), fmt.Sprintf("%d", s.CPUs), strings.Join(details, " "),
			})
		}
		ui.PrintTable([]string{"NAME", "ID", "STATUS", "MEMORY(MB)", "CPUS", "DETAILS"}, rows)
		return nil
	case "json":
		raw, err := json.MarshalIndent(inventory, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	case "yaml":
		raw, err := yaml.Marshal(inventory)
		if err != nil {
			return err
		}
		fmt.Print(string(raw))
		return nil
	default:
		return fmt.Errorf("unsupported output format %q (table, json, yaml)", output)
	}
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
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "start"); err != nil {
				return err
			}
			return startVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", vmNameCompletion)

	return cmd
}

// startVMWithProvider starts a VM on the specified provider with interactive selector
func startVMWithProvider(name, provider string) error {
	return vmlifecycle.RunVMLifecycleAction(name, provider, "start", func(lifecycle vmprov.VMLifecycle, vmName string) error {
		return lifecycle.StartVM(vmName)
	})
}

// stopVMWithProvider stops a VM on the specified provider with interactive selector
func stopVMWithProvider(name, provider string) error {
	return vmlifecycle.RunVMLifecycleAction(name, provider, "stop", func(lifecycle vmprov.VMLifecycle, vmName string) error {
		return lifecycle.StopVM(vmName, false)
	})
}

// infoVMWithProvider gets VM info from the specified provider with interactive selector
func infoVMWithProvider(name, provider string) error {
	return vmlifecycle.RunVMLifecycleAction(name, provider, "get info", func(lifecycle vmprov.VMLifecycle, vmName string) error {
		return lifecycle.GetVMInfo(vmName)
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
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "stop"); err != nil {
				return err
			}
			return stopVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", vmNameCompletion)

	return cmd
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
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "delete"); err != nil {
				return err
			}
			return deleteVMWithConfirmation(name, provider, force)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "Force deletion without confirmation")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", vmNameCompletion)

	return cmd
}

func deleteVMWithConfirmation(name, provider string, force bool) error {
	normalizedProvider, err := vmlifecycle.NormalizeVMProvider(provider)
	if err != nil {
		return err
	}

	name, err = vmlifecycle.ChooseVMNameForProvider(name, normalizedProvider, "delete")
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

	return vmlifecycle.WithVMLifecycle(normalizedProvider, func(lifecycle vmprov.VMLifecycle) error {
		return lifecycle.DeleteVM(name)
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
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "info"); err != nil {
				return err
			}
			return infoVMWithProvider(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", vmNameCompletion)

	return cmd
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
	return vmlifecycle.WithTrueNASVMManager(logger, func(vmManager vmlifecycle.TrueNASVMManager) error {
		if err := vmManager.CleanupOrphanedZVols(vmName, storagePool); err != nil {
			return fmt.Errorf("failed to cleanup orphaned ZVols: %w", err)
		}

		logger.Success("Successfully cleaned up orphaned ZVols for VM: %s", vmName)
		return nil
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
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "poweron"); err != nil {
				return err
			}
			return powerOnVM(name, provider)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", vmNameCompletion)

	return cmd
}

func newPowerOffVMCommand() *cobra.Command {
	var (
		name     string
		provider string
		force    bool
	)

	cmd := &cobra.Command{
		Use:   "poweroff",
		Short: "Power off a VM on Proxmox, TrueNAS, or vSphere/ESXi",
		Long:  `Hard power off (force-stop) a VM on Proxmox, TrueNAS, or vSphere/ESXi — the guest is NOT shut down cleanly. Prompts for confirmation unless --force. If --name is not specified, presents an interactive selector. Use 'vm stop' for a graceful shutdown.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := vmlifecycle.EnsureVMLifecycleProviderFn(provider, "poweroff"); err != nil {
				return err
			}
			return powerOffVM(name, provider, force)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)")
	cmd.Flags().StringVar(&name, "name", "", "VM name (optional - will prompt if not provided)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")

	// Add completion for name flag
	_ = cmd.RegisterFlagCompletionFunc("name", vmNameCompletion)

	return cmd
}

// powerOnVM powers on a VM on the specified provider with interactive selector
func powerOnVM(name, provider string) error {
	return vmlifecycle.RunVMLifecycleAction(name, provider, "power on", func(lifecycle vmprov.VMLifecycle, vmName string) error {
		return lifecycle.StartVM(vmName)
	})
}

// powerOffVM force-stops a VM on the specified provider with interactive
// selector. The force-stop is destructive (no clean guest shutdown), so it is
// gated behind a confirmation unless force is set (the global --yes also
// satisfies confirmActionFn).
func powerOffVM(name, provider string, force bool) error {
	return vmlifecycle.RunVMLifecycleAction(name, provider, "power off", func(lifecycle vmprov.VMLifecycle, vmName string) error {
		if !force {
			ok, err := confirmActionFn(fmt.Sprintf("Force power off VM %q? The guest is not shut down cleanly and may lose unsaved data.", vmName), false)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("power off cancelled by user")
			}
		}
		return lifecycle.StopVM(vmName, true)
	})
}
