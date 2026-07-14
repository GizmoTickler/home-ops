package common

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "/mnt/tank/iso", "'/mnt/tank/iso'"},
		{"spaces", "/tmp/vm dir", "'/tmp/vm dir'"},
		{"single quote", "node's vm", `'node'"'"'s vm'`},
		{"metachars stay literal", "/tmp/a;rm -rf /", "'/tmp/a;rm -rf /'"},
		{"empty", "", "''"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShellQuote(c.in); got != c.want {
				t.Fatalf("ShellQuote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestShellQuoteRoundTripsThroughPOSIXShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting invariant requires /bin/sh")
	}

	cases := []string{
		"",
		"plain",
		"with spaces",
		"tabs\tand\nnewlines",
		"node's vm",
		`double " quote`,
		`dollar $HOME and backtick ` + "`uname`",
		"semicolon; ampersand& pipe| redirect> less<",
		"glob * ? [abc] and bang !",
		"unicode snowman ☃ and emoji 🏠",
		string([]byte{0x01, 'c', 't', 'r', 'l', 0x7f}),
	}

	for i, value := range cases {
		t.Run(fmt.Sprintf("case-%02d", i), func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", "printf %s "+ShellQuote(value))
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			got, err := cmd.Output()
			if err != nil {
				t.Fatalf("shell round-trip failed for %q: %v stderr=%q", value, err, stderr.String())
			}
			if string(got) != value {
				t.Fatalf("ShellQuote round-trip mismatch:\n got: %q\nwant: %q", string(got), value)
			}
		})
	}
}

func TestValidateProxmoxOptValue(t *testing.T) {
	valid := []string{
		"/var/lib/vz/snippets",
		"/mnt/flashstor/images/flatcar-4593.2.1.raw",
		"local-zfs:vm-200-disk-0", // Proxmox volume IDs contain ':'
		"flatcar_prod.img",
	}
	for _, v := range valid {
		if err := ValidateProxmoxOptValue("--x", v); err != nil {
			t.Errorf("expected %q to be valid, got %v", v, err)
		}
	}

	invalid := []struct {
		name, in string
	}{
		{"empty", ""},
		{"comma breaks proxmox opts", "/tmp/a,b"},
		{"space", "/tmp/a b"},
		{"semicolon injection", "/tmp/a;rm -rf /"},
		{"command substitution", "/tmp/$(reboot)"},
		{"backtick", "/tmp/`id`"},
		{"pipe", "/tmp/a|b"},
		{"single quote", "/tmp/a'b"},
		{"double quote", `/tmp/a"b`},
		{"newline", "/tmp/a\nb"},
		{"ampersand", "/tmp/a&b"},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateProxmoxOptValue("--x", c.in); err == nil {
				t.Fatalf("expected %q to be rejected, got nil error", c.in)
			}
		})
	}
}
