package common

import "testing"

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
