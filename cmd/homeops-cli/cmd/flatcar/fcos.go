package flatcar

import (
	versionconfig "homeops-cli/internal/config"

	"github.com/spf13/cobra"
)

// The `fcos` command group lives in this package on purpose: Fedora CoreOS
// nodes share every deployer (Proxmox/vSphere/TrueNAS), the kubeadm-over-SSH
// orchestration and all node lifecycle commands with Flatcar. Only the Ignition
// template, the hypervisor fw_cfg key, the Proxmox image source and os-status
// differ, and those are selected by the OS family threaded through here.

// NewFCOSCommand builds the `fcos` command group.
func NewFCOSCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fcos",
		Short: "Manage Fedora CoreOS + kubeadm nodes",
		Long: `Provision and inspect Fedora CoreOS (FCOS) + kubeadm nodes.

A node runs FCOS when homeops.yaml sets cluster.os: fcos (or the node's own
cluster.nodes[].os / test_node.os). The kubeadm configs, containerd config,
kube-vip and networking addresses are identical to Flatcar; bootstrap,
'cluster rehearse-node' and the lifecycle commands under 'flatcar'
(kubeconfig, save-pki, reboot-node, reset-node, ...) work unchanged for FCOS
nodes.

Subcommands:
  render-ignition  Render and print the FCOS Ignition JSON for a node
  gen-kubeadm      Render kubeadm init/join config for a node (same as flatcar)
  deploy-vm        Deploy FCOS VM(s) on Proxmox, vSphere/ESXi, or TrueNAS
  os-status        Show rpm-ostree deployment + greenboot status across nodes`,
	}

	cmd.AddCommand(
		newRenderIgnitionCommandFor(versionconfig.OSFCOS),
		newGenKubeadmCommand(),
		newFCOSDeployVMCommand(),
		newFCOSOSStatusCommand(),
	)
	return cmd
}

func newFCOSDeployVMCommand() *cobra.Command {
	opts := &deployVMOptions{osFamily: versionconfig.OSFCOS}
	cmd := &cobra.Command{
		Use:   "deploy-vm",
		Short: "Deploy Fedora CoreOS k8s VM(s) on Proxmox, vSphere, or TrueNAS with Ignition",
		Example: `  # Deploy every node configured os: fcos on Proxmox, staging the current
  # stable-stream qemu image automatically
  homeops-cli fcos deploy-vm --power-on

  # Deploy one node from an image already on the Proxmox host
  homeops-cli fcos deploy-vm --nodes k8s-2 --image-path /var/lib/vz/template/cache/fcos.qcow2`,
		Long: `Deploy one or more Fedora CoreOS control-plane VMs on the chosen hypervisor.
Only nodes configured os: fcos may be deployed (default: all of them); a node
configured os: flatcar is refused so it can never boot the wrong Ignition.

--provider proxmox (default): each VM boots the FCOS qemu image and receives
its Ignition via fw_cfg under opt/com.coreos/config. With neither --image-path
nor --image-volume, the current image of --stream (default stable) is resolved
from builds.coreos.fedoraproject.org stream metadata, downloaded to the Proxmox
image cache dir, verified against the published sha256 and uncompressed-sha256,
decompressed, and imported (idempotent: a verified image is reused).

--provider vsphere (alias esxi): each VM is cloned from a pre-imported FCOS
OVA template and receives its Ignition via guestinfo.

--provider truenas: each VM boots a pre-staged FCOS image zvol and receives its
Ignition via fw_cfg (opt/com.coreos/config).

First boot layers greenboot and reboots once before building the Kubernetes
sysext, so allow for one extra reboot before kubeadm becomes available.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeployVM(cmd, *opts)
		},
	}

	registerDeployVMFlags(cmd, opts)
	return cmd
}
