package truenas

import (
	"fmt"
	"path"
	"strings"

	"homeops-cli/internal/common"
)

// SSHRunner is the slice of the SSH client used to stage images and seed
// ISOs on the NAS (the TrueNAS API has no file-transfer surface).
type SSHRunner interface {
	ExecuteCommand(command string) (string, error)
	UploadBytes(content []byte, remotePath string) error
}

// CloudImageVMConfig describes a general-purpose VM created from a cloud
// image with NoCloud cloud-init seeding.
type CloudImageVMConfig struct {
	Name          string
	MemoryMB      int
	VCPUs         int
	DiskGB        int
	Pool          string // zvol parent dataset (e.g. "tank/VM")
	ImageRef      string // http(s) URL or NAS-local qcow2 path
	ImageDir      string // staging dir on the NAS for images and seed ISOs
	SeedISO       []byte // NoCloud seed ISO content (built by the caller)
	NetworkBridge string
	MacAddress    string // "" = generate
	PowerOn       bool
}

// CreateCloudImageVM deploys a cloud image as a TrueNAS VM:
// stage the qcow2 on the NAS (over SSH), create the boot zvol, qemu-img
// convert the image onto it, upload the NoCloud seed ISO, create the VM with
// disk+CDROM+NIC devices, and optionally start it.
func (vm *VMManager) CreateCloudImageVM(cfg CloudImageVMConfig, sshExec SSHRunner) error {
	if cfg.Pool == "" {
		return fmt.Errorf("no storage pool: pass --storage or set hypervisors.truenas.vm.boot_storage in homeops.yaml")
	}
	if cfg.ImageDir == "" {
		return fmt.Errorf("no image staging dir: set hypervisors.truenas.image_dir in homeops.yaml")
	}

	// Duplicate check before any mutation.
	if _, err := vm.getVMByName(cfg.Name); err == nil {
		return fmt.Errorf("VM with name '%s' already exists", cfg.Name)
	}

	// Stage the image on the NAS (idempotent for URLs; local paths as-is).
	imagePath := cfg.ImageRef
	if strings.HasPrefix(cfg.ImageRef, "http://") || strings.HasPrefix(cfg.ImageRef, "https://") {
		imagePath = path.Join(cfg.ImageDir, path.Base(cfg.ImageRef))
		vm.logger.Info("Staging %s on the NAS at %s ...", path.Base(cfg.ImageRef), imagePath)
		stage := fmt.Sprintf("sudo mkdir -p %s && sudo [ -s %s ] || sudo wget -q -O %s %s",
			common.ShellQuote(cfg.ImageDir), common.ShellQuote(imagePath),
			common.ShellQuote(imagePath), common.ShellQuote(cfg.ImageRef))
		if _, err := sshExec.ExecuteCommand(stage); err != nil {
			return fmt.Errorf("stage cloud image on the NAS: %w", err)
		}
	}

	// Boot zvol, then write the image onto it.
	bootZVol := fmt.Sprintf("%s/%s-boot", cfg.Pool, cfg.Name)
	if err := vm.createSingleZVol(bootZVol, cfg.DiskGB, "boot"); err != nil {
		return err
	}
	vm.logger.Info("Writing %s onto /dev/zvol/%s ...", path.Base(imagePath), bootZVol)
	convert := fmt.Sprintf("sudo qemu-img convert -O raw %s %s",
		common.ShellQuote(imagePath), common.ShellQuote("/dev/zvol/"+bootZVol))
	if _, err := sshExec.ExecuteCommand(convert); err != nil {
		return fmt.Errorf("write cloud image to zvol: %w", err)
	}

	// NoCloud seed ISO next to the staged images.
	seedPath := path.Join(cfg.ImageDir, cfg.Name+"-seed.iso")
	if err := sshExec.UploadBytes(cfg.SeedISO, seedPath); err != nil {
		return fmt.Errorf("upload NoCloud seed ISO: %w", err)
	}

	// Create the VM and its devices.
	vmConfig := map[string]interface{}{
		"name":             cfg.Name,
		"description":      fmt.Sprintf("Cloud image VM - %s", cfg.Name),
		"vcpus":            cfg.VCPUs,
		"cores":            1,
		"threads":          1,
		"memory":           cfg.MemoryMB,
		"bootloader":       "UEFI",
		"autostart":        false,
		"time":             "LOCAL",
		"shutdown_timeout": 90,
		"cpu_mode":         "HOST-PASSTHROUGH",
	}
	createdVM, err := vm.client.CreateVM(vmConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}
	vm.logger.Info("VM created with ID: %d", createdVM.ID)

	mac := cfg.MacAddress
	if mac == "" {
		mac = vm.generateRandomMAC()
	}
	devices := []struct {
		order int
		attrs map[string]interface{}
	}{
		{1001, vm.buildDiskDeviceAttributes(bootZVol)},
		{1002, map[string]interface{}{
			"dtype":                  "NIC",
			"type":                   "VIRTIO",
			"mac":                    mac,
			"nic_attach":             cfg.NetworkBridge,
			"trust_guest_rx_filters": false,
		}},
		{1006, map[string]interface{}{
			"dtype": "CDROM",
			"path":  seedPath,
		}},
	}
	for _, device := range devices {
		if err := vm.createVMDevice(createdVM.ID, device.order, device.attrs); err != nil {
			return fmt.Errorf("failed to create %v device: %w", device.attrs["dtype"], err)
		}
	}

	if cfg.PowerOn {
		if err := vm.client.StartVM(createdVM.ID); err != nil {
			return fmt.Errorf("VM created but failed to start: %w", err)
		}
	}
	vm.logger.Success("VM %s created from %s", cfg.Name, path.Base(imagePath))
	return nil
}
