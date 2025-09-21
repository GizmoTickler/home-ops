package testutil

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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
