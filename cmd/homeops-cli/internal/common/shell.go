package common

import (
	"fmt"
	"strings"
)

// ShellQuote wraps value in single quotes for safe use as a single POSIX shell
// argument, escaping any embedded single quotes. Use this for EVERY path-like or
// otherwise untrusted value interpolated into a remote shell command — an
// unquoted value containing whitespace or a shell metacharacter (; & | $ ` etc.)
// is a command-injection vector.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// proxmoxOptUnsafe lists characters that would break a Proxmox API option string
// (options are comma-separated key=val pairs) or escape a shell command. Paths
// and Proxmox volume IDs legitimately contain '/', '-', '_', '.' and ':', so
// those are intentionally allowed; everything dangerous is rejected.
const proxmoxOptUnsafe = ",;&|<>(){}$`\"'\\\t\r\n *?[]!~#"

// ValidateProxmoxOptValue rejects a value that is empty or contains a character
// that could break Proxmox option parsing or inject a shell command. field is
// used only in the error message (e.g. "--image-path"). Use it for any value
// interpolated into a Proxmox option string (import-from=, fw_cfg file=, ...)
// or into a remote shell command before it reaches the host.
func ValidateProxmoxOptValue(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if i := strings.IndexAny(value, proxmoxOptUnsafe); i >= 0 {
		return fmt.Errorf("%s contains unsafe character %q (no whitespace, commas, quotes, or shell metacharacters allowed): %q",
			field, string(value[i]), value)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains unsafe control character %q (no control characters allowed): %q",
				field, r, value)
		}
	}
	return nil
}
