package talos

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	vmprov "homeops-cli/internal/provider"
)

// runInteractiveSSHFn execs ssh wired to the terminal. Swappable for tests.
var runInteractiveSSHFn = func(args ...string) error {
	return common.RunInteractive(os.Stdin, os.Stdout, os.Stderr, "ssh", args...)
}

// vmIPAddressesFn discovers a VM's IPs via its provider (guest agent /
// VMware Tools). Swappable for tests.
var vmIPAddressesFn = func(provider, name string) ([]string, error) {
	var ips []string
	err := runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error {
		var err error
		ips, err = lc.VMIPAddresses(name)
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
		Long: `Manage VM snapshots on any provider. Proxmox and vSphere snapshot the VM
natively; TrueNAS snapshots every zvol backing the VM under one ZFS snapshot
name (crash-consistent while the VM runs).`,
		Example: `  homeops-cli vm snapshot create --name dev-vm --snap pre-upgrade
  homeops-cli vm snapshot list --name dev-vm
  homeops-cli vm snapshot rollback --name dev-vm --snap pre-upgrade
  homeops-cli vm snapshot delete --provider truenas --name dev-vm --snap pre-upgrade`,
	}

	requireArgs := func(needSnap bool) error {
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if needSnap && snap == "" {
			return fmt.Errorf("--snap is required")
		}
		return nil
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs(true); err != nil {
				return err
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error { return lc.SnapshotVM(name, snap) })
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs(false); err != nil {
				return err
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error { return lc.ListVMSnapshots(name) })
		},
	}
	rollback := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back to a snapshot (DESTRUCTIVE: state after the snapshot is lost)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs(true); err != nil {
				return err
			}
			ok, err := confirmActionFn(fmt.Sprintf("Roll back VM %s to snapshot %q? Disk changes after the snapshot will be LOST.", name, snap), false)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("rollback cancelled by user")
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error { return lc.RollbackVM(name, snap) })
		},
	}
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete a snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireArgs(true); err != nil {
				return err
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error { return lc.DeleteVMSnapshot(name, snap) })
		},
	}

	cmd.PersistentFlags().StringVar(&name, "name", "", "VM name (required)")
	cmd.PersistentFlags().StringVar(&snap, "snap", "", "snapshot name")
	cmd.PersistentFlags().StringVar(&provider, "provider", defaultProviderName(), providerFlagUsage)
	cmd.AddCommand(create, list, rollback, del)
	return cmd
}

// newCloneVMCommand clones a VM.
func newCloneVMCommand() *cobra.Command {
	var name, to, provider string
	var vmid int
	var linked bool
	cmd := &cobra.Command{
		Use:   "clone",
		Short: "Clone a VM (full clone by default)",
		Long: `Clone a VM to a new name. Proxmox makes a full copy unless --linked;
TrueNAS clones are always ZFS snapshot clones (space-efficient); vSphere
makes full clones.`,
		Example: `  homeops-cli vm clone --name template-vm --to dev-vm2
  homeops-cli vm clone --provider truenas --name web0 --to web1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || to == "" {
				return fmt.Errorf("--name and --to are required")
			}
			return runLifecycleOp(provider, func(lc vmprov.VMLifecycle) error {
				return lc.Clone(name, to, vmprov.CloneOptions{VMID: vmid, Linked: linked})
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "source VM name (required)")
	cmd.Flags().StringVar(&to, "to", "", "new VM name (required)")
	cmd.Flags().IntVar(&vmid, "vmid", 0, "VMID for the clone (proxmox only; 0 = auto)")
	cmd.Flags().BoolVar(&linked, "linked", false, "linked clone instead of full (proxmox; truenas clones are always ZFS-linked)")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), providerFlagUsage)
	return cmd
}

// vmNameFromArgsOrPrompt returns the positional VM name, or prompts with a
// live VM picker when none was given (interactive menu / bare invocation).
// Returns "" without error when the user cancels the picker.
func vmNameFromArgsOrPrompt(args []string, provider, action string) (string, error) {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	normalized, err := normalizeVMProvider(provider)
	if err != nil {
		return "", err
	}
	return chooseVMNameForProvider(name, normalized, action)
}

// resolveVMIP finds a VM's IP: provider discovery first, cluster.nodes
// fallback (covers providers that cannot report guest IPs, e.g. TrueNAS).
func resolveVMIP(provider, name string) (string, error) {
	ips, err := vmIPAddressesFn(provider, name)
	if err == nil && len(ips) > 0 {
		return ips[0], nil
	}
	if node, ok := versionconfig.Get().NodeByName(name); ok && node.IP != "" {
		return node.IP, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("could not discover an IP for VM %s", name)
}

// newVMIPCommand prints a VM's discovered IP addresses.
func newVMIPCommand() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "ip [name]",
		Short: "Show a VM's IP addresses (guest agent / VMware Tools / cluster config)",
		Args:  cobra.MaximumNArgs(1),
		Example: `  homeops-cli vm ip dev-vm
  homeops-cli vm ip --provider vsphere vc-vm`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := vmNameFromArgsOrPrompt(args, provider, "show IPs for")
			if err != nil || name == "" {
				return err
			}
			ips, err := vmIPAddressesFn(provider, name)
			if err != nil {
				if node, ok := versionconfig.Get().NodeByName(name); ok && node.IP != "" {
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
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), providerFlagUsage)
	return cmd
}

// newVMSSHCommand opens an interactive SSH session to a VM.
func newVMSSHCommand() *cobra.Command {
	var user, provider string
	cmd := &cobra.Command{
		Use:   "ssh [name]",
		Short: "SSH into a VM (IP discovered via the provider or cluster config)",
		Args:  cobra.MaximumNArgs(1),
		Example: `  homeops-cli vm ssh dev-vm --user ubuntu
  homeops-cli vm ssh k8s-0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := vmNameFromArgsOrPrompt(args, provider, "SSH into")
			if err != nil || name == "" {
				return err
			}
			ip, err := resolveVMIP(provider, name)
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
			common.NewColorLogger().Info("Connecting to %s (%s)...", name, target)
			return runInteractiveSSHFn(target)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "SSH user (default: secrets.node_ssh_user)")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), providerFlagUsage)
	return cmd
}

// newVMConsoleCommand prints a VM's console URL.
func newVMConsoleCommand() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "console [name]",
		Short: "Show a VM's console URL (noVNC/xterm.js, SPICE, or WebMKS)",
		Args:  cobra.MaximumNArgs(1),
		Long: `Print the console endpoint for a VM:
  proxmox  noVNC and xterm.js web console URLs on the PVE node
  truenas  the display device's web console (or native SPICE) URL
  vsphere  a short-lived WebMKS websocket ticket URL`,
		Example: `  homeops-cli vm console dev-vm
  homeops-cli vm console --provider truenas web0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := vmNameFromArgsOrPrompt(args, provider, "open a console for")
			if err != nil || name == "" {
				return err
			}
			normalized, err := normalizeVMProvider(provider)
			if err != nil {
				return err
			}
			// Proxmox has two console flavours worth printing; the uniform
			// path prints the single provider URL.
			if normalized == "proxmox" {
				return withProxmoxVMManager(common.NewColorLogger(), func(m proxmoxVMManager) error {
					novnc, xtermjs, err := m.ConsoleURLs(name)
					if err != nil {
						return err
					}
					fmt.Printf("noVNC:    %s\nxterm.js: %s\n", novnc, xtermjs)
					return nil
				})
			}
			return withVMLifecycle(normalized, func(lc vmprov.VMLifecycle) error {
				url, err := lc.ConsoleURL(name)
				if err != nil {
					return err
				}
				fmt.Println(url)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), providerFlagUsage)
	return cmd
}
