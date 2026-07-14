package common

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func FuzzParseSizeSpec(f *testing.F) {
	for _, seed := range []string{
		"20G",
		"+10Gi",
		"1T",
		"512M",
		"100",
		" +30G ",
		"",
		"+",
		"20X",
		"-5G",
		"0G",
		strconv.FormatInt(math.MaxInt64, 10) + "T",
		"9223372036854775807G",
		"１２G",
		"1\x00G",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		bytes, relative, err := ParseSizeSpec(spec)
		if err != nil {
			return
		}
		if bytes <= 0 {
			t.Fatalf("ParseSizeSpec(%q) succeeded with non-positive byte count %d (relative=%v)", spec, bytes, relative)
		}
		if !relative && bytes < 0 {
			t.Fatalf("ParseSizeSpec(%q) succeeded with negative byte count %d", spec, bytes)
		}

		if wantBytes, wantRelative, ok := saneSizeSpecExpected(spec); ok {
			if bytes != wantBytes || relative != wantRelative {
				t.Fatalf("ParseSizeSpec(%q) = (%d, %v), want (%d, %v)", spec, bytes, relative, wantBytes, wantRelative)
			}
		}
	})
}

func saneSizeSpecExpected(spec string) (int64, bool, bool) {
	s := strings.TrimSpace(spec)
	relative := strings.HasPrefix(s, "+")
	if relative {
		s = strings.TrimPrefix(s, "+")
	}
	if s == "" {
		return 0, false, false
	}

	unit := int64(1)
	switch s[len(s)-1] {
	case 'M', 'm':
		unit = 1 << 20
		s = s[:len(s)-1]
	case 'G', 'g':
		unit = 1 << 30
		s = s[:len(s)-1]
	case 'T', 't':
		unit = 1 << 40
		s = s[:len(s)-1]
	}
	if len(s) == 0 || len(s) > 6 {
		return 0, false, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false, false
	}
	return n * unit, relative, true
}

func FuzzShellQuote(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain",
		"/tmp/vm dir",
		"node's vm",
		"'; reboot #",
		"$(id)",
		"`id`",
		"line\nbreak",
		"\x00binary",
		"emoji-☃",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		quoted := ShellQuote(value)
		if !isSingleSafeShellToken(quoted) {
			t.Fatalf("ShellQuote(%q) produced unsafe token %q", value, quoted)
		}

		err := ValidateProxmoxOptValue("--fuzz", value)
		if value == "" || containsProxmoxUnsafeForFuzz(value) {
			if err == nil {
				t.Fatalf("ValidateProxmoxOptValue accepted unsafe value %q", value)
			}
		}
	})
}

func isSingleSafeShellToken(s string) bool {
	if !strings.HasPrefix(s, "'") {
		return false
	}
	for i := 1; i < len(s); {
		next := strings.IndexByte(s[i:], '\'')
		if next < 0 {
			return false
		}
		i += next
		if i == len(s)-1 {
			return true
		}
		if !strings.HasPrefix(s[i:], `'"'"'`) {
			return false
		}
		i += len(`'"'"'`)
	}
	return false
}

func containsProxmoxUnsafeForFuzz(s string) bool {
	if strings.ContainsAny(s, proxmoxOptUnsafe) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func FuzzRedactCommandOutput(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain output",
		"password=SENTINEL_PASSWORD_VALUE\n",
		"api_key: SENTINEL_API_KEY_VALUE\n",
		"-----BEGIN PRIVATE KEY-----\nSENTINEL_FAKE_PRIVATE_KEY\n-----END PRIVATE KEY-----\n",
		"kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
		"certificate key: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
		"client-key-data: LS0tS0VZLURBVEEtU0hPVUxELU5PVC1MRUFL\n",
		strings.Repeat("token=x\n", 128),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, output string) {
		redacted := RedactCommandOutput(output)
		if len(redacted) > len(output)*4+1024 {
			t.Fatalf("redaction expanded output from %d to %d bytes", len(output), len(redacted))
		}
		if containsFakePrivateKeyBlock(output) && strings.Contains(redacted, "SENTINEL_FAKE_PRIVATE_KEY") {
			t.Fatalf("private key block was not redacted")
		}
		if kubeadmTokenPattern.MatchString(redacted) {
			t.Fatalf("kubeadm token-shaped value was not redacted")
		}
		if caCertHashPattern.MatchString(redacted) {
			t.Fatalf("kubeadm CA hash-shaped value was not redacted")
		}
	})
}

func containsFakePrivateKeyBlock(s string) bool {
	return strings.Contains(s, "-----BEGIN PRIVATE KEY-----") &&
		strings.Contains(s, "SENTINEL_FAKE_PRIVATE_KEY") &&
		strings.Contains(s, "-----END PRIVATE KEY-----")
}
