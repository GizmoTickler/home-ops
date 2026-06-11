// Package images resolves cloud-image sources for general-purpose VM
// deployment (vm create). Each OS maps to a current GenericCloud/cloud image
// URL; entries are overridable via the homeops config so users can pin
// versions or point at subscription-gated images (RHEL) and local mirrors.
package images

import (
	"fmt"
	"sort"
	"strings"

	"homeops-cli/internal/config"
)

// Image describes a deployable cloud image.
type Image struct {
	OS   string // canonical key (ubuntu, rocky, rhel, debian, fedora)
	URL  string // qcow2 image URL or local path on the hypervisor
	User string // default cloud-init login user for the OS
	Note string // shown in help/errors
}

// builtins are the "latest stable" cloud images. URLs use the distros'
// stable "latest" channels so they track point releases without code changes.
var builtins = map[string]Image{
	"ubuntu": {
		OS:   "ubuntu",
		URL:  "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		User: "ubuntu",
		Note: "Ubuntu 24.04 LTS (noble) current cloud image",
	},
	"rocky": {
		OS:   "rocky",
		URL:  "https://dl.rockylinux.org/pub/rocky/10/images/x86_64/Rocky-10-GenericCloud-Base.latest.x86_64.qcow2",
		User: "rocky",
		Note: "Rocky Linux 10 GenericCloud latest",
	},
	"debian": {
		OS:   "debian",
		URL:  "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2",
		User: "debian",
		Note: "Debian 13 (trixie) genericcloud latest",
	},
	"fedora": {
		OS:   "fedora",
		URL:  "https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2",
		User: "fedora",
		Note: "Fedora Cloud Base 42",
	},
	"rhel": {
		OS:   "rhel",
		URL:  "", // subscription-gated: must come from config images.rhel or --image
		User: "cloud-user",
		Note: "RHEL 10.x KVM Guest Image — download from access.redhat.com and set images.rhel in homeops.yaml (URL or path on the hypervisor)",
	},
}

// Resolve returns the image for an OS key, applying any override from the
// homeops config (images: map of os -> url/path).
func Resolve(osKey string) (Image, error) {
	key := strings.ToLower(strings.TrimSpace(osKey))
	img, ok := builtins[key]
	if !ok {
		return Image{}, fmt.Errorf("unknown OS %q (known: %s; or pass --image <url|path>)", osKey, strings.Join(Known(), ", "))
	}
	if override := config.Get().Images[key]; override != "" {
		img.URL = override
	}
	if img.URL == "" {
		return Image{}, fmt.Errorf("%s has no image source: %s", key, img.Note)
	}
	return img, nil
}

// Known returns the catalog keys, sorted.
func Known() []string {
	keys := make([]string, 0, len(builtins))
	for k := range builtins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DefaultUser returns the conventional cloud-init user for an OS key ("" if
// unknown).
func DefaultUser(osKey string) string {
	return builtins[strings.ToLower(osKey)].User
}
