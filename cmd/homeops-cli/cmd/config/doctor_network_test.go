package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
)

type probeRecorder struct {
	proxmox, truenas, vsphere int
	heads                     []string
	proxmoxErr                error
	headErr                   error
}

func stubProbes(t *testing.T) *probeRecorder {
	t.Helper()
	rec := &probeRecorder{}
	origP, origT, origV, origH := probeProxmoxFn, probeTrueNASFn, probeVSphereFn, headImageFn
	probeProxmoxFn = func(host, tokenID, secret, node string) error { rec.proxmox++; return rec.proxmoxErr }
	probeTrueNASFn = func(host, apiKey string) error { rec.truenas++; return nil }
	probeVSphereFn = func(host, username, password string) error { rec.vsphere++; return nil }
	headImageFn = func(url string) error { rec.heads = append(rec.heads, url); return rec.headErr }
	t.Cleanup(func() {
		probeProxmoxFn, probeTrueNASFn, probeVSphereFn, headImageFn = origP, origT, origV, origH
	})
	return rec
}

func TestRunNetworkChecksProbesConfiguredProviders(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Secrets: map[string]string{
			config.KeyProxmoxHost:        "literal://pve.test",
			config.KeyProxmoxTokenID:     "literal://root@pam!ci",
			config.KeyProxmoxTokenSecret: "literal://tok",
			config.KeyProxmoxNode:        "literal://pve",
			config.KeyTrueNASHost:        "literal://nas.test",
			config.KeyTrueNASAPIKey:      "literal://key",
			// vSphere intentionally unconfigured.
		},
	})
	defer restore()
	rec := stubProbes(t)

	failures := 0
	fail := func(string, ...interface{}) { failures++ }
	runNetworkChecks(common.NewColorLogger(), config.Get(), fail)

	assert.Equal(t, 1, rec.proxmox)
	assert.Equal(t, 1, rec.truenas)
	assert.Equal(t, 0, rec.vsphere, "unconfigured providers are skipped, not probed")
	assert.Zero(t, failures)
	// built-in catalog images (ubuntu/rocky/debian/fedora) are HEAD-checked;
	// RHEL has no URL without an override.
	assert.Len(t, rec.heads, 4)
}

func TestRunNetworkChecksReportsFailures(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Secrets: map[string]string{
			config.KeyProxmoxHost:        "literal://pve.test",
			config.KeyProxmoxTokenID:     "literal://root@pam!ci",
			config.KeyProxmoxTokenSecret: "literal://tok",
			config.KeyProxmoxNode:        "literal://pve",
		},
	})
	defer restore()
	rec := stubProbes(t)
	rec.proxmoxErr = errors.New("connection refused")
	rec.headErr = errors.New("HTTP 404")

	failures := 0
	fail := func(string, ...interface{}) { failures++ }
	runNetworkChecks(common.NewColorLogger(), config.Get(), fail)

	require.Equal(t, 1+4, failures, "proxmox probe + 4 image HEADs fail")
}

func TestDoctorOfflineByDefault(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	defer restore()
	rec := stubProbes(t)

	// runDoctor without network must not touch the probes (other checks may
	// fail in this hermetic environment; only the probe count matters here).
	_ = runDoctor(true, false)
	assert.Zero(t, rec.proxmox+rec.truenas+rec.vsphere)
	assert.Empty(t, rec.heads)
}
