package talos

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/proxmox"
)

// withProxmoxManager runs fn against a connected Proxmox VM manager.
var withProxmoxManagerFn = func(fn func(*proxmox.VMManager) error) error {
	host, tokenID, secret, nodeName, err := getProxmoxCredentialsFn()
	if err != nil {
		return err
	}
	manager, err := proxmox.NewVMManager(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()
	return fn(manager)
}

func requireProxmox(provider, what string) error {
	normalized, err := normalizeVMProvider(provider)
	if err != nil {
		return err
	}
	if normalized != "proxmox" {
		return fmt.Errorf("vm %s currently supports --provider proxmox (got %q)", what, provider)
	}
	return nil
}

// newSetVMCommand updates VM hardware (memory/cores).
func newSetVMCommand() *cobra.Command {
	var name, provider string
	var memory, cores int
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update a VM's hardware (memory, cores)",
		Example: `  homeops-cli vm set --name dev-vm --memory 8192
  homeops-cli vm set --name dev-vm --cores 4 --memory 16384`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if err := requireProxmox(provider, "set"); err != nil {
				return err
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error {
				return m.SetVMResources(name, memory, cores)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().IntVar(&memory, "memory", 0, "new memory in MB (0 = unchanged)")
	cmd.Flags().IntVar(&cores, "cores", 0, "new CPU cores (0 = unchanged)")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
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

  # Set an absolute size
  homeops-cli vm resize-disk --name dev-vm --disk scsi1 --size 200G`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if err := requireProxmox(provider, "resize-disk"); err != nil {
				return err
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
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error {
				return m.ResizeVMDisk(name, disk, spec)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().StringVar(&disk, "disk", "scsi0", "disk to resize (e.g. scsi0, scsi1)")
	cmd.Flags().StringVar(&grow, "grow", "", "grow by this much (e.g. 20G)")
	cmd.Flags().StringVar(&size, "size", "", "grow to this absolute size (e.g. 200G)")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
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
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if err := requireProxmox(provider, "restart"); err != nil {
				return err
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error {
				return m.RestartVM(name)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
	return cmd
}
