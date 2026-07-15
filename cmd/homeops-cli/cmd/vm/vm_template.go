package vm

import (
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/images"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"
	"homeops-cli/internal/vsphere"
)

// importProxmoxTemplateFn deploys + converts a cloud-image template on
// Proxmox. Swappable for tests.
var importProxmoxTemplateFn = func(cfg proxmox.VMConfig) error {
	return vmlifecycle.WithProxmoxVMManager(common.NewColorLogger(), func(m vmlifecycle.ProxmoxVMManager) error {
		return m.ImportTemplate(cfg)
	})
}

// markVSphereTemplateFn converts an existing vSphere VM into a template.
// Swappable for tests.
var markVSphereTemplateFn = func(name string) error {
	host, username, password, err := vmlifecycle.GetVSphereCredsFn()
	if err != nil {
		return err
	}
	manager, err := vsphere.NewVMManager(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false))
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()
	return manager.MarkVMAsTemplate(name)
}

// newVMTemplateCommand groups template operations.
func newVMTemplateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage reusable VM templates",
	}
	cmd.AddCommand(newVMTemplateImportCommand())
	return cmd
}

// newVMTemplateImportCommand imports a cloud image as a reusable template.
func newVMTemplateImportCommand() *cobra.Command {
	var (
		name, osKey, image, storage, bridge, user, sshKey, sshUser, fromVM string
		memory, cores, diskGB                                              int
		provider                                                           string
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a cloud image as a reusable VM template",
		Long: `Build a template that 'vm clone' (and on vSphere, 'vm create') can copy.

Per provider:
  proxmox  stages the cloud image, creates a VM with a cloud-init drive, and
           flips the Proxmox template flag; clones inherit the baked-in
           default user/SSH key. --from-vm instead converts an existing VM.
  vsphere  --from-vm converts an existing (powered-off) VM into a template.
           Direct qcow2 import is not possible (vSphere needs VMDK/OVA);
           deploy a cloud image once with govc/ovftool, then convert it.
  truenas  not supported: TrueNAS has no VM template concept. Use
           'vm create --provider truenas' (images are staged directly) or
           'vm clone' (ZFS clones).`,
		Example: `  # Ubuntu LTS template on Proxmox
  homeops-cli vm template import --name ubuntu-tpl --os ubuntu

  # Convert an existing VM into a template
  homeops-cli vm template import --name golden-vm --from-vm golden-vm --provider vsphere`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			normalized, err := vmlifecycle.NormalizeVMProvider(provider)
			if err != nil {
				return err
			}

			// Conversion mode: flip an existing VM's template flag.
			if fromVM != "" {
				switch normalized {
				case "proxmox":
					return vmlifecycle.WithProxmoxVMManager(logger, func(m vmlifecycle.ProxmoxVMManager) error {
						return m.ConvertVMToTemplate(fromVM)
					})
				case "vsphere":
					return markVSphereTemplateFn(fromVM)
				default:
					return vmprov.Unsupported("truenas", "TrueNAS has no VM template concept; use 'vm create --provider truenas' or 'vm clone'")
				}
			}

			// Image-import mode.
			switch normalized {
			case "truenas":
				return vmprov.Unsupported("truenas", "TrueNAS has no VM template concept; 'vm create --provider truenas' stages cloud images directly, or use 'vm clone'")
			case "vsphere":
				return vmprov.Unsupported("vsphere", "importing a qcow2 cloud image requires VMDK/OVA conversion; deploy a template once with govc/ovftool, or convert an existing VM with --from-vm")
			}

			if name == "" {
				if !ui.IsInteractive() {
					return fmt.Errorf("--name is required")
				}
				entered, perr := ui.Input("Template name:", "ubuntu-tpl")
				if perr != nil {
					return perr
				}
				if name = strings.TrimSpace(entered); name == "" {
					return nil // cancelled
				}
			}
			imageRef, ciUser, err := resolveCloudImage(osKey, image, user)
			if err != nil {
				return err
			}
			authorizedKey := resolveAuthorizedKey(logger, sshKey)
			cfg := versionconfig.Get()

			imagePath := imageRef
			if strings.HasPrefix(imageRef, "http://") || strings.HasPrefix(imageRef, "https://") {
				pveHost := vmlifecycle.ResolveSecretKey(versionconfig.KeyProxmoxHost)
				if pveHost == "" {
					return fmt.Errorf("cannot stage image: secrets.%s did not resolve", versionconfig.KeyProxmoxHost)
				}
				imagePath = path.Join(cfg.Hypervisors.Proxmox.ImageCacheDir, path.Base(imageRef))
				stagingUser := sshUser
				if stagingUser == "" {
					stagingUser = cfg.Hypervisors.Proxmox.SSHUser
				}
				logger.Info("Staging %s on %s:%s ...", path.Base(imageRef), pveHost, imagePath)
				if err := stageImageFn(stagingUser, pveHost, imageRef, imagePath); err != nil {
					return err
				}
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

			logger.Info("Importing template %s (%s, %dGB on %s)...", name, path.Base(imagePath), diskGB, storage)
			return importProxmoxTemplateFn(proxmox.VMConfig{
				Name:          name,
				Memory:        memory,
				Cores:         cores,
				Sockets:       1,
				BootDiskSize:  diskGB,
				BootStorage:   storage,
				ImageDiskPath: imagePath,
				NetworkBridge: bridge,
				Discard:       true,
				IOThread:      true,
				CloudInit: &proxmox.CloudInitConfig{
					User:     ciUser,
					SSHKeys:  authorizedKey,
					IPConfig: "ip=dhcp",
				},
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "template name (required for image import)")
	cmd.Flags().StringVar(&osKey, "os", "ubuntu", fmt.Sprintf("OS to import: %s", strings.Join(images.Known(), ", ")))
	cmd.Flags().StringVar(&image, "image", "", "explicit qcow2 image URL or hypervisor-local path (overrides --os)")
	cmd.Flags().IntVar(&memory, "memory", 2048, "template memory in MB (clones can resize)")
	cmd.Flags().IntVar(&cores, "cores", 2, "template CPU cores (clones can resize)")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 10, "template boot disk size in GB (clones can grow it)")
	cmd.Flags().StringVar(&storage, "storage", "", "storage pool (default: hypervisors.proxmox.vm.boot_storage)")
	cmd.Flags().StringVar(&bridge, "bridge", "", "network bridge (default: hypervisors.proxmox.vm.network_bridge)")
	cmd.Flags().StringVar(&user, "user", "", "default cloud-init user baked into the template")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key baked into the template (default: secrets.node_ssh_authorized_key)")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user on the hypervisor for image staging (default: root)")
	cmd.Flags().StringVar(&fromVM, "from-vm", "", "convert this existing VM into a template instead of importing an image")
	addProviderFlag(cmd, &provider)
	return cmd
}
