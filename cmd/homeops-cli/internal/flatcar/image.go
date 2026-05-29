package flatcar

import (
	"fmt"
	"strings"

	"homeops-cli/internal/constants"
)

// ImageConfig describes which Flatcar image to resolve.
type ImageConfig struct {
	Channel string // stable | beta | alpha (default: stable)
	Version string // release version, or "current" for the channel's latest
	Arch    string // amd64-usr (default)
	// Format selects which artifact to resolve. Common choices:
	//   "qemu_uefi.img"          -> flatcar_production_qemu_uefi_image.img
	//   "proxmox"/"qcow2"        -> flatcar_production_qemu_image.img (qcow2)
	//   "iso"                    -> flatcar_production_iso_image.iso
	// Default is the qemu UEFI image, suitable for import-from on Proxmox.
	Format string
}

// channelBaseURL returns the release base URL for a channel.
func channelBaseURL(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "", "stable":
		return constants.FlatcarReleaseBaseURL
	case "beta":
		return "https://beta.release.flatcar-linux.net/amd64-usr"
	case "alpha":
		return "https://alpha.release.flatcar-linux.net/amd64-usr"
	default:
		// Treat anything else as a custom stable-style mirror channel name; assume
		// the stable host layout with the given channel as the path is not valid,
		// so fall back to stable.
		return constants.FlatcarReleaseBaseURL
	}
}

// artifactFilename maps a Format to the Flatcar artifact filename.
func artifactFilename(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "qemu_uefi", "qemu_uefi.img":
		return "flatcar_production_qemu_uefi_image.img"
	case "proxmox", "qcow2", "qemu":
		return "flatcar_production_qemu_image.img"
	case "iso":
		return "flatcar_production_iso_image.iso"
	default:
		return format
	}
}

// ResolveImageURL builds the download URL for a Flatcar release artifact.
// Layout: <channelBase>/<version>/<artifact>, where <version> may be "current".
func ResolveImageURL(cfg ImageConfig) (string, error) {
	base := channelBaseURL(cfg.Channel)

	version := strings.TrimSpace(cfg.Version)
	if version == "" {
		version = "current"
	}

	artifact := artifactFilename(cfg.Format)
	if artifact == "" {
		return "", fmt.Errorf("could not resolve Flatcar artifact filename for format %q", cfg.Format)
	}

	return fmt.Sprintf("%s/%s/%s", base, version, artifact), nil
}

// DefaultImageConfig returns the default image selection (stable channel, current
// version, qemu UEFI image) for the given version. If version is empty, "current"
// is used by ResolveImageURL.
func DefaultImageConfig(version string) ImageConfig {
	return ImageConfig{
		Channel: constants.DefaultFlatcarChannel,
		Version: version,
		Arch:    "amd64-usr",
		Format:  "qemu_uefi.img",
	}
}

// SysextURL returns the Kubernetes systemd-sysext bundle URL for a k8s version,
// matching what the Butane template references.
func SysextURL(kubernetesVersion string) string {
	return fmt.Sprintf("%s/kubernetes/kubernetes-%s-x86-64.raw",
		constants.FlatcarSysextBaseURL, kubernetesVersion)
}
