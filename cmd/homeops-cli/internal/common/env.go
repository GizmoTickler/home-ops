package common

import (
	"os"
	"strconv"
	"strings"
)

// EnvBool reads a boolean environment variable, returning def when the variable
// is unset or unparseable. Accepts the forms strconv.ParseBool understands
// (1/t/true/0/f/false, any case).
func EnvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
