// Package cloudinit builds cloud-init payloads (user-data, meta-data,
// network-config) and NoCloud seed ISOs for providers without a native
// cloud-init drive (TrueNAS) or with guestinfo delivery (vSphere).
package cloudinit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
	"gopkg.in/yaml.v3"
)

// Userdata renders a #cloud-config user-data document creating a sudo user
// with the given SSH key. sshKey may be empty (console-only access).
func Userdata(user, sshKey, hostname string) (string, error) {
	userEntry := map[string]interface{}{
		"name":  user,
		"sudo":  "ALL=(ALL) NOPASSWD:ALL",
		"shell": "/bin/bash",
	}
	if sshKey != "" {
		userEntry["ssh_authorized_keys"] = []string{sshKey}
	}
	doc := map[string]interface{}{
		"hostname":   hostname,
		"users":      []interface{}{userEntry},
		"ssh_pwauth": false,
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render cloud-config: %w", err)
	}
	return "#cloud-config\n" + string(raw), nil
}

// Metadata renders NoCloud meta-data.
func Metadata(instanceID, hostname string) string {
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", instanceID, hostname)
}

// NetworkConfigV2 renders a netplan-style v2 network-config. An empty ipCIDR
// means DHCP on the first matching interface; otherwise the static address,
// optional default route, and nameservers are configured.
func NetworkConfigV2(ipCIDR, gateway string, nameservers []string) (string, error) {
	iface := map[string]interface{}{
		// Cloud images name NICs unpredictably (ens18, eth0, enp1s0); match
		// any en*/eth* interface.
		"match": map[string]interface{}{"name": "e*"},
	}
	if ipCIDR == "" {
		iface["dhcp4"] = true
	} else {
		iface["dhcp4"] = false
		iface["addresses"] = []string{ipCIDR}
		if gateway != "" {
			iface["routes"] = []interface{}{map[string]interface{}{"to": "default", "via": gateway}}
		}
	}
	if len(nameservers) > 0 {
		iface["nameservers"] = map[string]interface{}{"addresses": nameservers}
	}
	doc := map[string]interface{}{
		"version":   2,
		"ethernets": map[string]interface{}{"primary": iface},
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render network-config: %w", err)
	}
	return string(raw), nil
}

// VSphereMetadata renders cloud-init metadata for the VMware (guestinfo)
// datasource: instance identity plus a base64-embedded netplan network
// section.
func VSphereMetadata(name, ipCIDR, gateway string, nameservers []string) (string, error) {
	network, err := NetworkConfigV2(ipCIDR, gateway, nameservers)
	if err != nil {
		return "", err
	}
	meta := map[string]string{
		"instance-id":      name,
		"local-hostname":   name,
		"network":          base64.StdEncoding.EncodeToString([]byte(network)),
		"network.encoding": "base64",
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("render vsphere metadata: %w", err)
	}
	return string(raw), nil
}

// seedISOSize is the fixed image size for NoCloud seeds; the three text
// files are tiny, and ISO9660 metadata fits comfortably in 2MiB.
const seedISOSize = 2 << 20

// BuildNoCloudSeedISO assembles a NoCloud seed ISO (volume label "cidata")
// holding user-data, meta-data, and optionally network-config.
func BuildNoCloudSeedISO(userdata, metadata, networkConfig string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "homeops-seed")
	if err != nil {
		return nil, fmt.Errorf("create seed workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// The workspace must NOT contain the image file: Finalize packs every
	// file under the workspace into the ISO, and including the ISO's own
	// backing file makes it copy itself into itself indefinitely.
	workDir := filepath.Join(tmpDir, "work")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("create seed workspace: %w", err)
	}
	imgPath := filepath.Join(tmpDir, "seed.iso")
	storage, err := file.CreateFromPath(imgPath, seedISOSize)
	if err != nil {
		return nil, fmt.Errorf("create seed image: %w", err)
	}
	// 2048 is the canonical ISO9660 blocksize; cloud-init mounts the seed
	// with the kernel iso9660 driver, which expects it.
	fs, err := iso9660.Create(storage, seedISOSize, 0, 2048, workDir)
	if err != nil {
		return nil, fmt.Errorf("create seed filesystem: %w", err)
	}

	files := map[string]string{
		"/user-data": userdata,
		"/meta-data": metadata,
	}
	if networkConfig != "" {
		files["/network-config"] = networkConfig
	}
	for name, content := range files {
		f, err := fs.OpenFile(name, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return nil, fmt.Errorf("create %s in seed: %w", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			return nil, fmt.Errorf("write %s in seed: %w", name, err)
		}
	}

	// Rock Ridge keeps the lowercase hyphenated NoCloud filenames intact.
	if err := fs.Finalize(iso9660.FinalizeOptions{RockRidge: true, VolumeIdentifier: "cidata"}); err != nil {
		return nil, fmt.Errorf("finalize seed ISO: %w", err)
	}
	if err := storage.Close(); err != nil {
		return nil, fmt.Errorf("close seed image: %w", err)
	}
	return os.ReadFile(imgPath)
}
