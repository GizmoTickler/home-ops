package testutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// TempDir creates a temporary directory for tests and returns cleanup function
func TempDir(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "homeops-test-*")
	require.NoError(t, err)

	return dir, func() {
		_ = os.RemoveAll(dir)
	}
}

// TempFile creates a temporary file with content for tests
func TempFile(t *testing.T, dir, pattern string, content []byte) string {
	t.Helper()
	file, err := os.CreateTemp(dir, pattern)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	if content != nil {
		_, err = file.Write(content)
		require.NoError(t, err)
	}

	return file.Name()
}

// SetEnv sets environment variable and returns cleanup function
func SetEnv(t *testing.T, key, value string) func() {
	t.Helper()
	oldValue, existed := os.LookupEnv(key)

	err := os.Setenv(key, value)
	require.NoError(t, err)

	return func() {
		if existed {
			_ = os.Setenv(key, oldValue)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

// SetEnvs sets multiple environment variables and returns cleanup function
func SetEnvs(t *testing.T, envs map[string]string) func() {
	t.Helper()
	cleanups := make([]func(), 0, len(envs))

	for k, v := range envs {
		cleanups = append(cleanups, SetEnv(t, k, v))
	}

	return func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}
}

// ExecuteCommand executes a cobra command with args and returns output and error
func ExecuteCommand(root *cobra.Command, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	err := root.Execute()
	return buf.String(), err
}

// CaptureOutput captures stdout and stderr during function execution
func CaptureOutput(f func()) (string, string, error) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()

	os.Stdout = wOut
	os.Stderr = wErr

	outCh := make(chan string)
	errCh := make(chan string)

	go func() {
		out, _ := io.ReadAll(rOut)
		outCh <- string(out)
	}()

	go func() {
		err, _ := io.ReadAll(rErr)
		errCh <- string(err)
	}()

	f()

	_ = wOut.Close()
	_ = wErr.Close()

	stdout := <-outCh
	stderr := <-errCh

	os.Stdout = oldStdout
	os.Stderr = oldStderr

	return stdout, stderr, nil
}

// CheckNoSecretOutput returns an error when captured test output contains
// obvious secret-looking values. The error does not include the matched line.
func CheckNoSecretOutput(output string) error {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		switch {
		case strings.HasPrefix(upper, "VALUE:"):
			return fmt.Errorf("captured output contains possible secret label VALUE")
		case strings.Contains(upper, "-----BEGIN ") && strings.Contains(upper, " PRIVATE KEY-----"):
			return fmt.Errorf("captured output contains private key block marker")
		case strings.Contains(upper, "-----BEGIN CERTIFICATE-----"):
			return fmt.Errorf("captured output contains certificate block marker")
		}

		label, value, ok := splitOutputLabel(trimmed)
		if !ok || isRedactedOutputValue(value) {
			continue
		}

		if isSecretOutputLabel(label) {
			return fmt.Errorf("captured output contains possible secret label %q", label)
		}
	}

	return nil
}

// RequireNoSecretOutput fails the test when captured command output contains
// obvious secret-looking values.
func RequireNoSecretOutput(t testing.TB, outputs ...string) {
	t.Helper()

	for _, output := range outputs {
		require.NoError(t, CheckNoSecretOutput(output))
	}
}

func splitOutputLabel(line string) (string, string, bool) {
	colon := strings.Index(line, ":")
	equals := strings.Index(line, "=")

	var idx int
	switch {
	case colon >= 0 && equals >= 0:
		idx = min(colon, equals)
	case colon >= 0:
		idx = colon
	case equals >= 0:
		idx = equals
	default:
		return "", "", false
	}

	label := strings.TrimSpace(line[:idx])
	value := strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
	if label == "" || value == "" {
		return "", "", false
	}

	return label, value, true
}

func isSecretOutputLabel(label string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(label))

	switch normalized {
	case "token", "access_token", "refresh_token", "id_token", "client_secret", "password":
		return true
	default:
		return false
	}
}

func isRedactedOutputValue(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "" ||
		normalized == "<redacted>" ||
		normalized == "redacted" ||
		normalized == "***" ||
		normalized == "****" ||
		normalized == "xxxxx" ||
		normalized == "placeholder"
}

// CreateTestConfig creates a test configuration file
func CreateTestConfig(t *testing.T, dir string, config interface{}) string {
	t.Helper()
	configPath := filepath.Join(dir, "config.yaml")

	// You would marshal config to YAML here
	// This is a placeholder implementation
	err := os.WriteFile(configPath, []byte("test: config"), 0644)
	require.NoError(t, err)

	return configPath
}

// AssertFileExists checks if file exists
func AssertFileExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.NoError(t, err)
}

// AssertFileContains checks if file contains expected content
func AssertFileContains(t *testing.T, path string, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(content), expected)
}

// MockHTTPResponse creates a mock HTTP response for testing
type MockHTTPResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
}

// CompareYAML compares two YAML strings ignoring formatting differences
func CompareYAML(t *testing.T, expected, actual string) {
	t.Helper()
	// This would parse both YAMLs and compare as objects
	// Placeholder for now
	require.Equal(t, expected, actual)
}
