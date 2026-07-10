package images

import (
	"testing"

	"homeops-cli/internal/config"
)

func TestKnownIsSortedAndComplete(t *testing.T) {
	got := Known()
	want := []string{"debian", "fedora", "rhel", "rocky", "ubuntu"}
	if len(got) != len(want) {
		t.Fatalf("Known() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Known() = %v, want %v", got, want)
		}
	}
}

func TestResolveBuiltin(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)

	img, err := Resolve("ubuntu")
	if err != nil {
		t.Fatalf("Resolve(ubuntu) error: %v", err)
	}
	if img.OS != "ubuntu" {
		t.Errorf("OS = %q, want ubuntu", img.OS)
	}
	if img.User != "ubuntu" {
		t.Errorf("User = %q, want ubuntu", img.User)
	}
	if img.URL == "" {
		t.Errorf("expected a non-empty builtin URL")
	}
}

func TestResolveNormalizesKey(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)

	img, err := Resolve("  Ubuntu  ")
	if err != nil {
		t.Fatalf("Resolve with padded/mixed case error: %v", err)
	}
	if img.OS != "ubuntu" {
		t.Errorf("OS = %q, want ubuntu", img.OS)
	}
}

func TestResolveUnknownOS(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)

	if _, err := Resolve("plan9"); err == nil {
		t.Fatalf("expected error for unknown OS")
	}
}

func TestResolveRHELRequiresOverride(t *testing.T) {
	restore := config.SetForTesting(&config.Config{})
	t.Cleanup(restore)

	// RHEL is subscription-gated and has no builtin URL.
	if _, err := Resolve("rhel"); err == nil {
		t.Fatalf("expected error resolving rhel without an override")
	}
}

func TestResolveConfigOverride(t *testing.T) {
	restore := config.SetForTesting(&config.Config{
		Images: map[string]string{
			"rhel":   "/mnt/images/rhel-10.qcow2",
			"ubuntu": "https://mirror.local/ubuntu.img",
		},
	})
	t.Cleanup(restore)

	rhel, err := Resolve("rhel")
	if err != nil {
		t.Fatalf("Resolve(rhel) with override error: %v", err)
	}
	if rhel.URL != "/mnt/images/rhel-10.qcow2" {
		t.Errorf("rhel URL = %q, want override path", rhel.URL)
	}

	ubuntu, err := Resolve("ubuntu")
	if err != nil {
		t.Fatalf("Resolve(ubuntu) with override error: %v", err)
	}
	if ubuntu.URL != "https://mirror.local/ubuntu.img" {
		t.Errorf("ubuntu URL = %q, want override URL", ubuntu.URL)
	}
}

func TestDefaultUser(t *testing.T) {
	if got := DefaultUser("rhel"); got != "cloud-user" {
		t.Errorf("DefaultUser(rhel) = %q, want cloud-user", got)
	}
	if got := DefaultUser("ROCKY"); got != "rocky" {
		t.Errorf("DefaultUser(ROCKY) = %q, want rocky", got)
	}
	if got := DefaultUser("plan9"); got != "" {
		t.Errorf("DefaultUser(unknown) = %q, want empty", got)
	}
}
