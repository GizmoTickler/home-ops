package vm

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/vmlifecycle"
)

// runLifecycleOp normalizes the provider then runs op against a freshly
// constructed lifecycle for it (closed afterwards).
func runLifecycleOp(provider string, op func(vmprov.VMLifecycle) error) error {
	normalized, err := vmlifecycle.NormalizeVMProvider(provider)
	if err != nil {
		return err
	}
	return vmlifecycle.WithVMLifecycle(normalized, op)
}

// providerFlagUsage is the shared --provider help text for vm subcommands.
const providerFlagUsage = "Virtualization provider: proxmox, vsphere/esxi, or truenas (default: hypervisors.default in homeops.yaml)"

// newSetVMCommand updates VM hardware (memory/cores).
func newSetVMCommand() *cobra.Command {
	var name, provider string
	var memory, cores int
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update a VM's hardware (memory, cores)",
		Example: `  homeops-cli vm set --name dev-vm --memory 8192
  homeops-cli vm set --provider truenas --name dev-vm --cores 4 --memory 16384`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveVMNameForAction(name, provider, "configure")
			if err != nil {
				return err
			}
			if name == "" {
				return nil // picker cancelled
			}
			if memory, err = promptIntIfInteractive(memory, "New memory in MB (blank = unchanged):", "8192"); err != nil {
				return err
			}
			if cores, err = promptIntIfInteractive(cores, "New CPU cores (blank = unchanged):", "4"); err != nil {
				return err
			}
			if memory == 0 && cores == 0 {
				return fmt.Errorf("nothing to change: pass --memory and/or --cores")
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error {
				return lc.SetVMResources(name, memory, cores)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (prompts if omitted)")
	cmd.Flags().IntVar(&memory, "memory", 0, "new memory in MB (0 = unchanged)")
	cmd.Flags().IntVar(&cores, "cores", 0, "new CPU cores/vCPUs (0 = unchanged)")
	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), providerFlagUsage)
	return cmd
}

// newResizeDiskCommand grows a VM disk.
func newResizeDiskCommand() *cobra.Command {
	var name, disk, grow, size, provider string
	cmd := &cobra.Command{
		Use:   "resize-disk",
		Short: "Grow a VM disk (disks can never shrink)",
		Example: `  # Grow the boot disk by 20G
  homeops-cli vm resize-disk --name dev-vm --grow 20G

  # Set an absolute size on a specific disk
  homeops-cli vm resize-disk --name dev-vm --disk scsi1 --size 200G
  homeops-cli vm resize-disk --provider truenas --name dev-vm --disk openebs --grow 100G`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveVMNameForAction(name, provider, "resize a disk on")
			if err != nil {
				return err
			}
			if name == "" {
				return nil // picker cancelled
			}
			// On an interactive run with neither size flag, prompt for a grow amount.
			if grow == "" && size == "" {
				if grow, err = promptStringIfInteractive(grow, "Grow disk by (e.g. 20G):", "20G"); err != nil {
					return err
				}
			}
			spec := ""
			switch {
			case grow != "" && size != "":
				return fmt.Errorf("pass --grow or --size, not both")
			case grow != "":
				spec = "+" + strings.TrimPrefix(grow, "+")
			case size != "":
				spec = size
			default:
				return fmt.Errorf("pass --grow <N>G or --size <N>G")
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error {
				return lc.ResizeVMDisk(name, disk, spec)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (prompts if omitted)")
	cmd.Flags().StringVar(&disk, "disk", "", "disk to resize (default: boot disk; proxmox: scsi0/scsi1..., truenas: boot/openebs/zvol path, vsphere: scsiN or device label)")
	cmd.Flags().StringVar(&grow, "grow", "", "grow by this much (e.g. 20G)")
	cmd.Flags().StringVar(&size, "size", "", "grow to this absolute size (e.g. 200G)")
	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), providerFlagUsage)
	return cmd
}

// newRestartVMCommand reboots a VM.
func newRestartVMCommand() *cobra.Command {
	var name, provider string
	cmd := &cobra.Command{
		Use:     "restart",
		Short:   "Restart (reboot) a VM",
		Example: `  homeops-cli vm restart --name dev-vm`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveVMNameForAction(name, provider, "restart")
			if err != nil {
				return err
			}
			if name == "" {
				return nil // picker cancelled
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error {
				return lc.RestartVM(name)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (prompts if omitted)")
	cmd.Flags().StringVar(&provider, "provider", vmlifecycle.DefaultProviderName(), providerFlagUsage)
	return cmd
}
