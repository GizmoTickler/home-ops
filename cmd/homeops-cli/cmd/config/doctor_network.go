package config

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/images"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/truenas"
	"homeops-cli/internal/vsphere"
)

// Network probes are real connections to the hypervisor APIs and image
// mirrors; each is behind a seam so doctor tests stay offline.
var (
	probeProxmoxFn = func(host, tokenID, secret, node string) error {
		manager, err := proxmox.NewVMManager(host, tokenID, secret, node, common.EnvBool(constants.EnvProxmoxInsecure, false))
		if err != nil {
			return err
		}
		return manager.Close()
	}
	probeTrueNASFn = func(host, apiKey string) error {
		manager := truenas.NewVMManager(host, apiKey, 443, true)
		if err := manager.Connect(); err != nil {
			return err
		}
		return manager.Close()
	}
	probeVSphereFn = func(host, username, password string) error {
		manager, err := vsphere.NewVMManager(host, username, password, common.EnvBool(constants.EnvVSphereInsecure, false))
		if err != nil {
			return err
		}
		return manager.Close()
	}
	headImageFn = func(url string) error {
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Head(url)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
)

// resolveDoctorKeys resolves a set of semantic secret keys; ok is false when
// any of them is missing (the provider is then reported as not configured).
func resolveDoctorKeys(cfg *config.Config, keys ...string) (map[string]string, bool) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		v, err := resolveRefFn(cfg.SecretRef(key))
		if err != nil || v == "" {
			return nil, false
		}
		values[key] = v
	}
	return values, true
}

// runNetworkChecks probes each configured hypervisor API and HEAD-checks the
// resolvable image catalog URLs. Providers without credentials are reported
// and skipped (not every setup uses all three).
func runNetworkChecks(logger *common.ColorLogger, cfg *config.Config, fail func(string, ...interface{})) {
	// Hypervisor API reachability.
	if v, ok := resolveDoctorKeys(cfg, config.KeyProxmoxHost, config.KeyProxmoxTokenID, config.KeyProxmoxTokenSecret, config.KeyProxmoxNode); !ok {
		logger.Info("network: proxmox credentials not configured — probe skipped")
	} else if err := probeProxmoxFn(v[config.KeyProxmoxHost], v[config.KeyProxmoxTokenID], v[config.KeyProxmoxTokenSecret], v[config.KeyProxmoxNode]); err != nil {
		fail("network: proxmox API unreachable: %v", err)
	} else {
		logger.Success("network: proxmox API reachable")
	}

	if v, ok := resolveDoctorKeys(cfg, config.KeyTrueNASHost, config.KeyTrueNASAPIKey); !ok {
		logger.Info("network: truenas credentials not configured — probe skipped")
	} else if err := probeTrueNASFn(v[config.KeyTrueNASHost], v[config.KeyTrueNASAPIKey]); err != nil {
		fail("network: truenas API unreachable: %v", err)
	} else {
		logger.Success("network: truenas API reachable")
	}

	if v, ok := resolveDoctorKeys(cfg, config.KeyVSphereHost, config.KeyVSphereUsername, config.KeyVSpherePassword); !ok {
		logger.Info("network: vsphere credentials not configured — probe skipped")
	} else if err := probeVSphereFn(v[config.KeyVSphereHost], v[config.KeyVSphereUsername], v[config.KeyVSpherePassword]); err != nil {
		fail("network: vsphere API unreachable: %v", err)
	} else {
		logger.Success("network: vsphere API reachable")
	}

	// Image catalog URLs ('vm create' inputs). Local hypervisor paths and
	// unset entries (e.g. RHEL without an override) are skipped.
	for _, osKey := range images.Known() {
		img, err := images.Resolve(osKey)
		if err != nil || !strings.HasPrefix(img.URL, "http") {
			continue
		}
		if err := headImageFn(img.URL); err != nil {
			fail("network: image %s (%s): %v", osKey, img.URL, err)
		} else {
			logger.Success("network: image %s OK", osKey)
		}
	}
}
