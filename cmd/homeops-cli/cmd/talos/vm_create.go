package talos

import (
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/images"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
)

// stageImageFn downloads a cloud image onto the hypervisor host over SSH
// (idempotent: skips when already present). Swappable for tests.
var stageImageFn = func(sshUser, host, url, destPath string) error {
	client := ssh.NewSSHClient(ssh.SSHConfig{Host: host, Username: sshUser, Port: "22"})
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect to %s@%s: %w", sshUser, host, err)
	}
	defer func() { _ = client.Close() }()
	cmd := fmt.Sprintf("mkdir -p %s && [ -s %s ] || wget -q -O %s %s",
		common.ShellQuote(path.Dir(destPath)), common.ShellQuote(destPath),
		common.ShellQuote(destPath), common.ShellQuote(url))
	if _, err := client.ExecuteCommand(cmd); err != nil {
		return fmt.Errorf("stage image on %s: %w", host, err)
	}
	return nil
}

// deployCloudInitVMFn creates the VM via the Proxmox manager. Swappable for tests.
var deployCloudInitVMFn = func(cfg proxmox.VMConfig) error {
	host, tokenID, secret, nodeName, err := getProxmoxCredentialsFn()
	if err != nil {
		return err
	}
	manager, err := newProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()
	return manager.DeployVM(cfg)
}

// newCreateVMCommand deploys a general-purpose VM from a cloud image
// (Ubuntu/Rocky/Debian/Fedora/RHEL or any qcow2) with cloud-init.
func newCreateVMCommand() *cobra.Command {
	var (
		name, osKey, image, storage, bridge, ipCfg, gateway, nameserver, user, sshKey, sshUser string
		memory, cores, diskGB, vlan, mtu                                                       int
		start                                                                                  bool
		provider                                                                               string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a general-purpose VM from a cloud image (Ubuntu, Rocky, RHEL, ...)",
		Long: `Deploy a VM from a cloud image with cloud-init first-boot configuration
(user, SSH key, networking). Images for ubuntu/rocky/debian/fedora resolve to
the distros' latest stable cloud images; override or add OSes via the images:
map in homeops.yaml (RHEL requires this — its KVM guest image is
subscription-gated). Any qcow2 URL or hypervisor-local path works via --image.

Currently implemented for Proxmox. The image is staged onto the Proxmox host
over SSH (root by default), imported as the boot disk, and configured via a
cloud-init drive.`,
		Example: `  # Latest Ubuntu LTS with DHCP and your default SSH key
  homeops-cli vm create --name dev-vm --os ubuntu

  # Rocky 10 with static IP and custom sizing
  homeops-cli vm create --name rocky0 --os rocky --memory 8192 --cores 4 \
    --disk-gb 80 --ip 192.168.120.50/22 --gateway 192.168.123.254

  # RHEL 10.1 (set images.rhel in homeops.yaml first)
  homeops-cli vm create --name rhel0 --os rhel

  # Any qcow2 already on the hypervisor
  homeops-cli vm create --name custom0 --image /var/lib/vz/template/cache/custom.qcow2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			normalized, err := normalizeVMProvider(provider)
			if err != nil {
				return err
			}
			if normalized != "proxmox" {
				return fmt.Errorf("vm create currently supports --provider proxmox (got %q); TrueNAS/vSphere support is planned", provider)
			}

			cfg := versionconfig.Get()
			imageRef := image
			ciUser := user
			if imageRef == "" {
				img, err := images.Resolve(osKey)
				if err != nil {
					return err
				}
				imageRef = img.URL
				if ciUser == "" {
					ciUser = img.User
				}
			}
			if ciUser == "" {
				ciUser = "admin"
			}

			// Stage remote URLs onto the hypervisor for import-from.
			imagePath := imageRef
			if strings.HasPrefix(imageRef, "http://") || strings.HasPrefix(imageRef, "https://") {
				pveHost := resolveSecretKey(versionconfig.KeyProxmoxHost)
				if pveHost == "" {
					return fmt.Errorf("cannot stage image: secrets.%s did not resolve", versionconfig.KeyProxmoxHost)
				}
				imagePath = "/var/lib/vz/template/cache/" + path.Base(imageRef)
				logger.Info("Staging %s on %s:%s ...", path.Base(imageRef), pveHost, imagePath)
				if err := stageImageFn(sshUser, pveHost, imageRef, imagePath); err != nil {
					return err
				}
			}

			authorizedKey := sshKey
			if authorizedKey == "" {
				authorizedKey = strings.TrimSpace(resolveSecretKey(versionconfig.KeyNodeSSHAuthorizedKey))
			}
			if authorizedKey == "" {
				logger.Warn("No SSH key (--ssh-key or secrets.node_ssh_authorized_key) — you may not be able to log in")
			}

			vmDefaults := cfg.Hypervisors.Proxmox.VM
			if storage == "" {
				storage = vmDefaults.BootStorage
			}
			if storage == "" {
				storage = "local-lvm"
			}
			if bridge == "" {
				bridge = vmDefaults.NetworkBridge
			}
			if bridge == "" {
				bridge = "vmbr0"
			}

			ip := "ip=dhcp"
			if ipCfg != "" && ipCfg != "dhcp" {
				ip = "ip=" + ipCfg
				if gateway != "" {
					ip += ",gw=" + gateway
				}
			}

			vmConfig := proxmox.VMConfig{
				Name:          name,
				Memory:        memory,
				Cores:         cores,
				Sockets:       1,
				BootDiskSize:  diskGB,
				BootStorage:   storage,
				ImageDiskPath: imagePath,
				NetworkBridge: bridge,
				NetworkMTU:    mtu,
				VLANID:        vlan,
				Discard:       true,
				IOThread:      true,
				PowerOn:       start,
				CloudInit: &proxmox.CloudInitConfig{
					User:       ciUser,
					SSHKeys:    authorizedKey,
					IPConfig:   ip,
					Nameserver: nameserver,
				},
			}
			logger.Info("Creating VM %s (%s, %dMB/%d cores, %dGB on %s)...", name, path.Base(imagePath), memory, cores, diskGB, storage)
			if err := deployCloudInitVMFn(vmConfig); err != nil {
				return err
			}
			logger.Success("VM %s created — log in as %s@<vm-ip> once cloud-init finishes", name, ciUser)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().StringVar(&osKey, "os", "ubuntu", fmt.Sprintf("OS to deploy: %s", strings.Join(images.Known(), ", ")))
	cmd.Flags().StringVar(&image, "image", "", "explicit qcow2 image URL or hypervisor-local path (overrides --os)")
	cmd.Flags().IntVar(&memory, "memory", 4096, "memory in MB")
	cmd.Flags().IntVar(&cores, "cores", 2, "CPU cores")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 40, "boot disk size in GB")
	cmd.Flags().StringVar(&storage, "storage", "", "storage pool (default: hypervisors.proxmox.vm.boot_storage)")
	cmd.Flags().StringVar(&bridge, "bridge", "", "network bridge (default: hypervisors.proxmox.vm.network_bridge)")
	cmd.Flags().IntVar(&vlan, "vlan", 0, "VLAN tag (0 = none)")
	cmd.Flags().IntVar(&mtu, "mtu", 0, "network MTU (0 = default)")
	cmd.Flags().StringVar(&ipCfg, "ip", "dhcp", "IP config: dhcp or CIDR (e.g. 192.168.120.50/22)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "default gateway (with --ip CIDR)")
	cmd.Flags().StringVar(&nameserver, "nameserver", "", "DNS server(s) for cloud-init")
	cmd.Flags().StringVar(&user, "user", "", "cloud-init login user (default: per-OS convention)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: secrets.node_ssh_authorized_key)")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH user on the hypervisor for image staging")
	cmd.Flags().BoolVar(&start, "start", true, "power on after creation")
	cmd.Flags().StringVar(&provider, "provider", defaultProviderName(), "hypervisor (currently: proxmox)")
	return cmd
}
