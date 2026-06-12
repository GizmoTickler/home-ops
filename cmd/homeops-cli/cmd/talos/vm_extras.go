package talos

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/proxmox"
)

// runInteractiveSSHFn execs ssh wired to the terminal. Swappable for tests.
var runInteractiveSSHFn = func(args ...string) error {
	return common.RunInteractive(os.Stdin, os.Stdout, os.Stderr, "ssh", args...)
}

// vmIPAddressesFn discovers a VM's IPs via the guest agent. Swappable for tests.
var vmIPAddressesFn = func(name string) ([]string, error) {
	var ips []string
	err := withProxmoxManagerFn(func(m *proxmox.VMManager) error {
		var err error
		ips, err = m.VMIPAddresses(name)
		return err
	})
	return ips, err
}

// newSnapshotCommand groups snapshot operations.
func newSnapshotCommand() *cobra.Command {
	var name, snap, provider string
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage VM snapshots (create, list, rollback, delete)",
		Example: `  homeops-cli vm snapshot create --name dev-vm --snap pre-upgrade
  homeops-cli vm snapshot list --name dev-vm
  homeops-cli vm snapshot rollback --name dev-vm --snap pre-upgrade
  homeops-cli vm snapshot delete --name dev-vm --snap pre-upgrade`,
	}

	requireArgs := func(what string, needSnap bool) error {
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if needSnap && snap == "" {
			return fmt.Errorf("--snap is required")
		}
		return requireProxmox(provider, "snapshot "+what)
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs("create", true); err != nil {
				return err
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error { return m.SnapshotVM(name, snap) })
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs("list", false); err != nil {
				return err
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error { return m.ListVMSnapshots(name) })
		},
	}
	rollback := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back to a snapshot (DESTRUCTIVE: state after the snapshot is lost)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs("rollback", true); err != nil {
				return err
			}
			ok, err := confirmActionFn(fmt.Sprintf("Roll back VM %s to snapshot %q? Disk changes after the snapshot will be LOST.", name, snap), false)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("rollback cancelled by user")
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error { return m.RollbackVM(name, snap) })
		},
	}
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs("delete", true); err != nil {
				return err
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error { return m.DeleteVMSnapshot(name, snap) })
		},
	}

	cmd.PersistentFlags().StringVar(&name, "name", "", "VM name (required)")
	cmd.PersistentFlags().StringVar(&snap, "snap", "", "snapshot name")
	cmd.PersistentFlags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
	cmd.AddCommand(create, list, rollback, del)
	return cmd
}

// newCloneVMCommand clones a VM.
func newCloneVMCommand() *cobra.Command {
	var name, to, provider string
	var vmid int
	var linked bool
	cmd := &cobra.Command{
		Use:     "clone",
		Short:   "Clone a VM (full clone by default)",
		Example: `  homeops-cli vm clone --name template-vm --to dev-vm2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || to == "" {
				return fmt.Errorf("--name and --to are required")
			}
			if err := requireProxmox(provider, "clone"); err != nil {
				return err
			}
			return withProxmoxManagerFn(func(m *proxmox.VMManager) error {
				return m.CloneVM(name, to, vmid, !linked)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source VM name (required)")
	cmd.Flags().StringVar(&to, "to", "", "new VM name (required)")
	cmd.Flags().IntVar(&vmid, "vmid", 0, "VMID for the clone (0 = auto)")
	cmd.Flags().BoolVar(&linked, "linked", false, "linked clone instead of full")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
	return cmd
}

// resolveVMIP finds a VM's IP: guest agent first, cluster.nodes fallback.
func resolveVMIP(name string) (string, error) {
	if ips, err := vmIPAddressesFn(name); err == nil && len(ips) > 0 {
		return ips[0], nil
	} else if node, ok := versionconfig.Get().NodeByName(name); ok && node.IP != "" {
		return node.IP, nil
	} else if err != nil {
		return "", err
	}
	return "", fmt.Errorf("could not discover an IP for VM %s", name)
}

// newVMIPCommand prints a VM's discovered IP addresses.
func newVMIPCommand() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:     "ip <name>",
		Short:   "Show a VM's IP addresses (QEMU guest agent)",
		Args:    cobra.ExactArgs(1),
		Example: `  homeops-cli vm ip dev-vm`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProxmox(provider, "ip"); err != nil {
				return err
			}
			ips, err := vmIPAddressesFn(args[0])
			if err != nil {
				if node, ok := versionconfig.Get().NodeByName(args[0]); ok && node.IP != "" {
					fmt.Println(node.IP)
					return nil
				}
				return err
			}
			for _, ip := range ips {
				fmt.Println(ip)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
	return cmd
}

// newVMSSHCommand opens an interactive SSH session to a VM.
func newVMSSHCommand() *cobra.Command {
	var user, provider string
	cmd := &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into a VM (IP discovered via guest agent or cluster config)",
		Args:  cobra.ExactArgs(1),
		Example: `  homeops-cli vm ssh dev-vm --user ubuntu
  homeops-cli vm ssh k8s-0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireProxmox(provider, "ssh"); err != nil {
				return err
			}
			ip, err := resolveVMIP(args[0])
			if err != nil {
				return err
			}
			sshUser := user
			if sshUser == "" {
				sshUser = resolveSecretKey(versionconfig.KeyNodeSSHUser)
			}
			target := ip
			if sshUser != "" {
				target = fmt.Sprintf("%s@%s", sshUser, ip)
			}
			common.NewColorLogger().Info("Connecting to %s (%s)...", args[0], target)
			return runInteractiveSSHFn(target)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "SSH user (default: secrets.node_ssh_user)")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
	return cmd
}
