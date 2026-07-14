package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
)

// cmdProviderTimeout caps how long a cmd:// helper is allowed to run.
const cmdProviderTimeout = 30 * time.Second

// resolveEnv resolves env://VAR_NAME from the process environment.
func resolveEnv(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("env:// reference is missing a variable name")
	}
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment variable %s is not set (referenced as env://%s)", name, name)
	}
	return strings.TrimSpace(value), nil
}

// resolveFile resolves file://PATH to the file's contents. A leading ~ is
// expanded to the user's home directory. Trailing whitespace is trimmed so
// `echo secret > file` round-trips.
func resolveFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("file:// reference is missing a path")
	}
	expanded, err := ExpandHome(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(expanded) // #nosec G304 -- file:// secret provider intentionally reads an operator-configured local secret file
	if err != nil {
		return "", fmt.Errorf("failed to read secret file %s: %w", expanded, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("secret file %s is empty", expanded)
	}
	return value, nil
}

// resolveCmd resolves cmd://COMMAND by running it through the shell and
// returning trimmed stdout. This is the escape hatch for external secret
// tools (pass, gopass, vault, sops…). Stdout is never logged or embedded in
// error messages — it is the secret payload.
func resolveCmd(command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("cmd:// reference is missing a command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdProviderTimeout)
	defer cancel()
	result, err := common.RunCommand(ctx, common.CommandOptions{
		Name:     "sh",
		Args:     []string{"-c", command},
		Redactor: identityRedactor,
	})
	if err != nil {
		return "", fmt.Errorf("secret command failed (cmd://%s): %w", command, err)
	}
	value := strings.TrimSpace(result.Stdout)
	if value == "" {
		return "", fmt.Errorf("secret command produced no output (cmd://%s)", command)
	}
	return value, nil
}

// ExpandHome expands a leading ~ or ~/ in path to the current user's home
// directory.
func ExpandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ~ in %s: %w", path, err)
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
	}
	return path, nil
}
