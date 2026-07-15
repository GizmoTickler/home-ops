package vm

import (
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"homeops-cli/internal/cloudinit"
	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/images"
	vmprov "homeops-cli/internal/provider"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"
	"homeops-cli/internal/vsphere"
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
	host, tokenID, secret, nodeName, err := vmlifecycle.GetProxmoxCredentialsFn()
	if err != nil {
		return err
	}
	manager, err := vmlifecycle.NewProxmoxVMManagerFn(host, tokenID, secret, nodeName, common.EnvBool(constants.EnvProxmoxInsecure, false))
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()
	return manager.DeployVM(cfg)
}

// createTrueNASCloudVMFn deploys a cloud-image VM on TrueNAS (NoCloud seed
// ISO over SSH). Swappable for tests.
var createTrueNASCloudVMFn = func(cfg truenas.CloudImageVMConfig) error {
	host, apiKey, err := vmlifecycle.GetTrueNASCredentialsFn()
	if err != nil {
		return err
	}
	sshClient := ssh.NewSSHClient(ssh.SSHConfig{Host: host, Username: trueNASSSHUser(), Port: "22"})
	if err := sshClient.Connect(); err != nil {
		return fmt.Errorf("connect to NAS over SSH (image staging): %w", err)
	}
	defer func() { _ = sshClient.Close() }()

	manager := truenas.NewVMManager(host, apiKey, 443, true)
	if err := manager.Connect(); err != nil {
		return fmt.Errorf("failed to connect to TrueNAS: %w", err)
	}
	defer func() { _ = manager.Close() }()
	return manager.CreateCloudImageVM(cfg, sshClient)
}

// createVSphereCloudVMFn deploys a template clone with guestinfo cloud-init
// on vSphere. Swappable for tests.
var createVSphereCloudVMFn = func(cfg vsphere.CloudInitVMConfig) error {
	host, username, password, err := vmlifecycle.GetVSphereCredsFn()
	if err != nil {
		return err
	}
	manager, err := vsphere.NewVMManager(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false))
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()
	return manager.CreateCloudInitVM(cfg)
}

// trueNASSSHUser resolves the SSH login used for staging files on the NAS:
// secrets.truenas_username, falling back to TrueNAS SCALE's standard admin
// account.
func trueNASSSHUser() string {
	if user := vmlifecycle.ResolveSecretKey(versionconfig.KeyTrueNASUsername); user != "" {
		return user
	}
	return versionconfig.Get().Hypervisors.TrueNAS.SSHUser
}

// resolveCloudImage resolves --os/--image to a concrete image reference and
// the cloud-init login user.
func resolveCloudImage(osKey, image, user string) (imageRef, ciUser string, err error) {
	imageRef = image
	ciUser = user
	if imageRef == "" {
		img, err := images.Resolve(osKey)
		if err != nil {
			return "", "", err
		}
		imageRef = img.URL
		if ciUser == "" {
			ciUser = img.User
		}
	}
	if ciUser == "" {
		ciUser = "admin"
	}
	return imageRef, ciUser, nil
}

// resolveAuthorizedKey returns --ssh-key or the configured node key, warning
// when neither resolves.
func resolveAuthorizedKey(logger *common.ColorLogger, sshKey string) string {
	authorizedKey := sshKey
	if authorizedKey == "" {
		authorizedKey = strings.TrimSpace(vmlifecycle.ResolveSecretKey(versionconfig.KeyNodeSSHAuthorizedKey))
	}
	if authorizedKey == "" {
		logger.Warn("No SSH key (--ssh-key or secrets.node_ssh_authorized_key) — you may not be able to log in")
	}
	return authorizedKey
}

// splitNameservers turns a comma-separated --nameserver value into a slice.
func splitNameservers(nameserver string) []string {
	if strings.TrimSpace(nameserver) == "" {
		return nil
	}
	var out []string
	for _, ns := range strings.Split(nameserver, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			out = append(out, ns)
		}
	}
	return out
}

// staticIPCIDR normalizes the --ip flag: "" for DHCP, the CIDR otherwise.
func staticIPCIDR(ipCfg string) string {
	if ipCfg == "" || ipCfg == "dhcp" {
		return ""
	}
	return ipCfg
}

// createSpec carries the resolved, provider-independent inputs of vm create.
type createSpec struct {
	name, ciUser, sshKey, imageRef      string
	memory, cores, diskGB               int
	storage, bridge                     string
	vlan, mtu                           int
	ipCfg, gateway, nameserver, sshUser string
	template                            string
	start                               bool
}

func createProxmoxVM(logger *common.ColorLogger, spec createSpec) error {
	cfg := versionconfig.Get()

	// Stage remote URLs onto the hypervisor for import-from.
	imagePath := spec.imageRef
	if strings.HasPrefix(spec.imageRef, "http://") || strings.HasPrefix(spec.imageRef, "https://") {
		pveHost := vmlifecycle.ResolveSecretKey(versionconfig.KeyProxmoxHost)
		if pveHost == "" {
			return fmt.Errorf("cannot stage image: secrets.%s did not resolve", versionconfig.KeyProxmoxHost)
		}
		imagePath = path.Join(cfg.Hypervisors.Proxmox.ImageCacheDir, path.Base(spec.imageRef))
		sshUser := spec.sshUser
		if sshUser == "" {
			sshUser = cfg.Hypervisors.Proxmox.SSHUser
		}
		logger.Info("Staging %s on %s:%s ...", path.Base(spec.imageRef), pveHost, imagePath)
		if err := stageImageFn(sshUser, pveHost, spec.imageRef, imagePath); err != nil {
			return err
		}
	}

	vmDefaults := cfg.Hypervisors.Proxmox.VM
	storage := spec.storage
	if storage == "" {
		storage = vmDefaults.BootStorage
	}
	if storage == "" {
		storage = "local-lvm"
	}
	bridge := spec.bridge
	if bridge == "" {
		bridge = vmDefaults.NetworkBridge
	}
	if bridge == "" {
		bridge = "vmbr0"
	}

	ip := "ip=dhcp"
	if cidr := staticIPCIDR(spec.ipCfg); cidr != "" {
		ip = "ip=" + cidr
		if spec.gateway != "" {
			ip += ",gw=" + spec.gateway
		}
	}

	vmConfig := proxmox.VMConfig{
		Name:          spec.name,
		Memory:        spec.memory,
		Cores:         spec.cores,
		Sockets:       1,
		BootDiskSize:  spec.diskGB,
		BootStorage:   storage,
		ImageDiskPath: imagePath,
		NetworkBridge: bridge,
		NetworkMTU:    spec.mtu,
		VLANID:        spec.vlan,
		Discard:       true,
		IOThread:      true,
		PowerOn:       spec.start,
		CloudInit: &proxmox.CloudInitConfig{
			User:       spec.ciUser,
			SSHKeys:    spec.sshKey,
			IPConfig:   ip,
			Nameserver: spec.nameserver,
		},
	}
	logger.Info("Creating VM %s (%s, %dMB/%d cores, %dGB on %s)...", spec.name, path.Base(imagePath), spec.memory, spec.cores, spec.diskGB, storage)
	if err := deployCloudInitVMFn(vmConfig); err != nil {
		return err
	}
	logger.Success("VM %s created — log in as %s@<vm-ip> once cloud-init finishes", spec.name, spec.ciUser)
	return nil
}

func createTrueNASVM(logger *common.ColorLogger, spec createSpec) error {
	if spec.vlan != 0 {
		return vmprov.Unsupported("truenas", "VLAN tagging lives on the TrueNAS bridge; create a tagged bridge and pass it with --bridge")
	}
	if spec.mtu != 0 {
		return vmprov.Unsupported("truenas", "MTU is a property of the TrueNAS bridge interface, not the VM NIC")
	}
	if err := vmlifecycle.ValidateVMName(spec.name); err != nil {
		return err
	}
	cfg := versionconfig.Get()

	pool := spec.storage
	if pool == "" {
		pool = cfg.Hypervisors.TrueNAS.VM.BootStorage
	}
	bridge := spec.bridge
	if bridge == "" {
		bridge = cfg.Hypervisors.TrueNAS.VM.NetworkBridge
	}
	if bridge == "" {
		bridge = vmlifecycle.TrueNASNetworkBridge()
	}

	userdata, err := cloudinit.Userdata(spec.ciUser, spec.sshKey, spec.name)
	if err != nil {
		return err
	}
	networkConfig, err := cloudinit.NetworkConfigV2(staticIPCIDR(spec.ipCfg), spec.gateway, splitNameservers(spec.nameserver))
	if err != nil {
		return err
	}
	seedISO, err := cloudinit.BuildNoCloudSeedISO(userdata, cloudinit.Metadata(spec.name, spec.name), networkConfig)
	if err != nil {
		return err
	}

	logger.Info("Creating VM %s on TrueNAS (%dMB/%d vCPUs, %dGB on %s)...", spec.name, spec.memory, spec.cores, spec.diskGB, pool)
	if err := createTrueNASCloudVMFn(truenas.CloudImageVMConfig{
		Name:          spec.name,
		MemoryMB:      spec.memory,
		VCPUs:         spec.cores,
		DiskGB:        spec.diskGB,
		Pool:          pool,
		ImageRef:      spec.imageRef,
		ImageDir:      cfg.Hypervisors.TrueNAS.ImageDir,
		SeedISO:       seedISO,
		NetworkBridge: bridge,
		PowerOn:       spec.start,
	}); err != nil {
		return err
	}
	logger.Success("VM %s created — log in as %s@<vm-ip> once cloud-init finishes", spec.name, spec.ciUser)
	return nil
}

func createVSphereVM(logger *common.ColorLogger, spec createSpec) error {
	switch {
	case spec.storage != "":
		return vmprov.Unsupported("vsphere", "the clone inherits the template's datastore; omit --storage")
	case spec.bridge != "":
		return vmprov.Unsupported("vsphere", "the clone inherits the template's network; omit --bridge")
	case spec.vlan != 0:
		return vmprov.Unsupported("vsphere", "VLANs come from the template's port group; omit --vlan")
	case spec.mtu != 0:
		return vmprov.Unsupported("vsphere", "MTU comes from the template's network; omit --mtu")
	}
	template := spec.template
	if template == "" {
		template = versionconfig.Get().Hypervisors.VSphere.Template
	}
	if template == "" {
		return fmt.Errorf("no template: pass --template or set hypervisors.vsphere.template in homeops.yaml ('vm template import --help' explains how to make one)")
	}

	userdata, err := cloudinit.Userdata(spec.ciUser, spec.sshKey, spec.name)
	if err != nil {
		return err
	}
	metadata, err := cloudinit.VSphereMetadata(spec.name, staticIPCIDR(spec.ipCfg), spec.gateway, splitNameservers(spec.nameserver))
	if err != nil {
		return err
	}

	logger.Info("Creating VM %s on vSphere from template %s (%dMB/%d CPUs)...", spec.name, template, spec.memory, spec.cores)
	if err := createVSphereCloudVMFn(vsphere.CloudInitVMConfig{
		TemplateName: template,
		Name:         spec.name,
		MemoryMB:     spec.memory,
		Cores:        spec.cores,
		DiskGB:       spec.diskGB,
		Userdata:     userdata,
		Metadata:     metadata,
		PowerOn:      spec.start,
	}); err != nil {
		return err
	}
	logger.Success("VM %s created — log in as %s@<vm-ip> once cloud-init finishes", spec.name, spec.ciUser)
	return nil
}

// newCreateVMCommand deploys a general-purpose VM from a cloud image
// (Ubuntu/Rocky/Debian/Fedora/RHEL or any qcow2) with cloud-init.
func newCreateVMCommand() *cobra.Command {
	var (
		name, osKey, image, storage, bridge, ipCfg, gateway, nameserver, user, sshKey, sshUser, template string
		memory, cores, diskGB, vlan, mtu                                                                 int
		start                                                                                            bool
		provider                                                                                         string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a general-purpose VM from a cloud image (Ubuntu, Rocky, RHEL, ...)",
		Long: `Deploy a VM from a cloud image with cloud-init first-boot configuration
(user, SSH key, networking). Images for ubuntu/rocky/debian/fedora resolve to
the distros' latest stable cloud images; override or add OSes via the images:
map in homeops.yaml (RHEL requires this — its KVM guest image is
subscription-gated). Any qcow2 URL or hypervisor-local path works via --image.

Per provider:
  proxmox  the image is staged onto the PVE host over SSH, imported as the
           boot disk, and configured via a native cloud-init drive
  truenas  the image is staged onto the NAS over SSH, written to a new boot
           zvol, and seeded with a NoCloud ISO (built locally, uploaded)
  vsphere  a template (--template / hypervisors.vsphere.template) is cloned
           and cloud-init is delivered through guestinfo`,
		Example: `  # Latest Ubuntu LTS with DHCP and your default SSH key
  homeops-cli vm create --name dev-vm --os ubuntu

  # Rocky 10 with static IP and custom sizing
  homeops-cli vm create --name rocky0 --os rocky --memory 8192 --cores 4 \
    --disk-gb 80 --ip 192.168.120.50/22 --gateway 192.168.123.254

  # On TrueNAS (note: TrueNAS VM names cannot contain dashes)
  homeops-cli vm create --provider truenas --name dev0 --os ubuntu

  # On vSphere from an existing template
  homeops-cli vm create --provider vsphere --name dev-vm --template ubuntu-tpl

  # Any qcow2 already on the hypervisor
  homeops-cli vm create --name custom0 --image /var/lib/vz/template/cache/custom.qcow2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := common.NewColorLogger()
			if name == "" {
				// Off a terminal (CI, the interactive menu can't reach here) keep
				// failing fast; on a TTY (e.g. the menu) prompt for the essentials
				// so create is usable without typing flags.
				if !ui.IsInteractive() {
					return fmt.Errorf("--name is required")
				}
				entered, err := ui.Input("VM name:", "dev-vm")
				if err != nil {
					return err
				}
				if name = strings.TrimSpace(entered); name == "" {
					return nil // cancelled / empty
				}
				chosenOS, err := ui.Choose("OS to deploy:", images.Known())
				if err != nil {
					if ui.IsCancellation(err) {
						return nil
					}
					return err
				}
				osKey = chosenOS
			}
			normalized, err := vmlifecycle.NormalizeVMProvider(provider)
			if err != nil {
				return err
			}

			spec := createSpec{
				name: name, memory: memory, cores: cores, diskGB: diskGB,
				storage: storage, bridge: bridge, vlan: vlan, mtu: mtu,
				ipCfg: ipCfg, gateway: gateway, nameserver: nameserver,
				sshUser: sshUser, template: template, start: start,
			}
			// vSphere clones a template, so no qcow2 resolution is needed —
			// only the login user convention.
			if normalized == "vsphere" {
				spec.ciUser = user
				if spec.ciUser == "" {
					spec.ciUser = images.DefaultUser(osKey)
				}
				if spec.ciUser == "" {
					spec.ciUser = "admin"
				}
			} else {
				spec.imageRef, spec.ciUser, err = resolveCloudImage(osKey, image, user)
				if err != nil {
					return err
				}
			}
			spec.sshKey = resolveAuthorizedKey(logger, sshKey)

			switch normalized {
			case "proxmox":
				return createProxmoxVM(logger, spec)
			case "truenas":
				return createTrueNASVM(logger, spec)
			case "vsphere":
				return createVSphereVM(logger, spec)
			}
			return fmt.Errorf("unsupported provider: %s", provider)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "VM name (required)")
	cmd.Flags().StringVar(&osKey, "os", "ubuntu", fmt.Sprintf("OS to deploy: %s", strings.Join(images.Known(), ", ")))
	cmd.Flags().StringVar(&image, "image", "", "explicit qcow2 image URL or hypervisor-local path (overrides --os)")
	cmd.Flags().IntVar(&memory, "memory", 4096, "memory in MB")
	cmd.Flags().IntVar(&cores, "cores", 2, "CPU cores")
	cmd.Flags().IntVar(&diskGB, "disk-gb", 40, "boot disk size in GB")
	cmd.Flags().StringVar(&storage, "storage", "", "storage pool (default: the provider's vm.boot_storage; vsphere: from the template)")
	cmd.Flags().StringVar(&bridge, "bridge", "", "network bridge (default: the provider's vm.network_bridge; vsphere: from the template)")
	cmd.Flags().IntVar(&vlan, "vlan", 0, "VLAN tag (proxmox only; 0 = none)")
	cmd.Flags().IntVar(&mtu, "mtu", 0, "network MTU (proxmox only; 0 = default)")
	cmd.Flags().StringVar(&ipCfg, "ip", "dhcp", "IP config: dhcp or CIDR (e.g. 192.168.120.50/22)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "default gateway (with --ip CIDR)")
	cmd.Flags().StringVar(&nameserver, "nameserver", "", "DNS server(s) for cloud-init (comma-separated)")
	cmd.Flags().StringVar(&user, "user", "", "cloud-init login user (default: per-OS convention)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: secrets.node_ssh_authorized_key)")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH user on the hypervisor for image staging (default: root on proxmox; secrets.truenas_username on truenas)")
	cmd.Flags().BoolVar(&start, "start", true, "power on after creation")
	cmd.Flags().StringVar(&template, "template", "", "vSphere template to clone (default: hypervisors.vsphere.template)")
	addProviderFlag(cmd, &provider)
	return cmd
}
