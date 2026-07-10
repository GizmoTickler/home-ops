package provider

import (
	"errors"
	"fmt"
	"testing"
)

func TestUnsupportedErrorMessage(t *testing.T) {
	err := Unsupported("truenas", "no template concept")
	want := "not supported on truenas: no template concept"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestUnsupportedErrorType(t *testing.T) {
	err := Unsupported("vsphere", "qcow2 import needs conversion")
	var u *UnsupportedError
	if !errors.As(err, &u) {
		t.Fatalf("errors.As failed to unwrap *UnsupportedError")
	}
	if u.Provider != "vsphere" {
		t.Errorf("Provider = %q, want %q", u.Provider, "vsphere")
	}
	if u.Reason != "qcow2 import needs conversion" {
		t.Errorf("Reason = %q, want %q", u.Reason, "qcow2 import needs conversion")
	}
}

func TestIsUnsupported(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"direct unsupported", Unsupported("proxmox", "nope"), true},
		{"wrapped unsupported", fmt.Errorf("dispatch: %w", Unsupported("proxmox", "nope")), true},
		{"wrapped plain", fmt.Errorf("dispatch: %w", errors.New("boom")), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUnsupported(tt.err); got != tt.want {
				t.Errorf("IsUnsupported(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCapabilitiesAbsentMeansSupported(t *testing.T) {
	caps := Capabilities{
		FeatureTemplateImport: "TrueNAS has no VM template concept",
	}

	if reason, unsupported := caps[FeatureTemplateImport]; !unsupported || reason == "" {
		t.Errorf("expected FeatureTemplateImport to be unsupported with a reason, got %q/%v", reason, unsupported)
	}

	// A feature absent from the map is supported by contract.
	if _, unsupported := caps[FeatureClone]; unsupported {
		t.Errorf("FeatureClone should be supported (absent from map)")
	}
}

func TestFeatureConstantValues(t *testing.T) {
	// Features map 1:1 onto vm subcommands/flags; lock the wire strings.
	want := map[Feature]string{
		FeatureCreate:         "create",
		FeatureSet:            "set",
		FeatureResizeDisk:     "resize-disk",
		FeatureRestart:        "restart",
		FeatureSnapshot:       "snapshot",
		FeatureClone:          "clone",
		FeatureIP:             "ip",
		FeatureConsole:        "console",
		FeatureTemplateImport: "template import",
	}
	for feature, str := range want {
		if string(feature) != str {
			t.Errorf("feature %v = %q, want %q", feature, string(feature), str)
		}
	}
}
