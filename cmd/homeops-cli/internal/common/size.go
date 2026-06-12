package common

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseSizeSpec parses "20G"/"+20G"/"1T" style disk size specs into bytes and
// whether the spec is relative (leading '+'). Bare numbers are bytes; valid
// unit suffixes are M, G, and T (case-insensitive).
func ParseSizeSpec(spec string) (bytes int64, relative bool, err error) {
	s := strings.TrimSpace(spec)
	if strings.HasPrefix(s, "+") {
		relative = true
		s = strings.TrimPrefix(s, "+")
	}
	if s == "" {
		return 0, false, fmt.Errorf("empty size spec")
	}
	unit := int64(1)
	switch suffix := s[len(s)-1]; suffix {
	case 'M', 'm':
		unit = 1 << 20
		s = s[:len(s)-1]
	case 'G', 'g':
		unit = 1 << 30
		s = s[:len(s)-1]
	case 'T', 't':
		unit = 1 << 40
		s = s[:len(s)-1]
	default:
		if suffix < '0' || suffix > '9' {
			return 0, false, fmt.Errorf("unsupported size unit %q (use M, G, or T)", string(suffix))
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false, fmt.Errorf("invalid size spec %q (use e.g. 20G or +20G)", spec)
	}
	return n * unit, relative, nil
}
