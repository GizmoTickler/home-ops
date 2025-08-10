package testing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeops-cli/internal/config"
	"homeops-cli/internal/errors"
	"homeops-cli/internal/metrics"
)

// TestFramework provides utilities for testing HomeOps components
type TestFramework struct {
	tempDir    string
	metrics    *metrics.PerformanceCollector
	cleanupFns []func() error
}

// TestConfig represents test configuration
type TestConfig struct {
	TempDir     string
	LogLevel    string
	Timeout     time.Duration
	CleanupMode string // "always", "on_success", "never"
}

// MockData represents test data for various components
type MockData struct {
	YAMLContent    string
	Secrets        map[string]string
	Configs        map[string]interface{}
	ExpectedErrors []string
}

// TestResult represents the result of a test operation
type TestResult struct {
	Success      bool
	Duration     time.Duration
	Error        error
	Metrics      map[string]interface{}
	Artifacts    []string
	CleanupError error
}

// NewTestFramework creates a new test framework instance
func NewTestFramework(t *testing.T, config TestConfig) (*TestFramework, error) {
	tempDir := config.TempDir
	if tempDir == "" {
		var err error
		tempDir, err = os.MkdirTemp("", "homeops-test-*")
		if err != nil {
			return nil, errors.NewFileSystemError("TEST_TEMP_DIR_ERROR",
				"failed to create temporary directory for testing", err)
		}
	}

	framework := &TestFramework{
		tempDir:    tempDir,
		metrics:    metrics.NewPerformanceCollector(),
		cleanupFns: make([]func() error, 0),
	}

	// Register cleanup with testing framework
	t.Cleanup(func() {
		if err := framework.Cleanup(); err != nil {
			t.Errorf("Test cleanup failed: %v", err)
		}
	})

	return framework, nil
}

// CreateTempFile creates a temporary file with content
func (tf *TestFramework) CreateTempFile(name, content string) (string, error) {
	filePath := filepath.Join(tf.tempDir, name)
	dir := filepath.Dir(filePath)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", errors.NewFileSystemError("TEST_FILE_CREATE_ERROR",
			fmt.Sprintf("failed to create directory for test file: %s", dir), err)
	}

	file, err := os.Create(filePath)
	if err != nil {
		return "", errors.NewFileSystemError("TEST_FILE_CREATE_ERROR",
			fmt.Sprintf("failed to create test file: %s", filePath), err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", closeErr)
		}
	}()

	if _, err := file.WriteString(content); err != nil {
		return "", errors.NewFileSystemError("TEST_FILE_WRITE_ERROR",
			fmt.Sprintf("failed to write content to test file: %s", filePath), err)
	}

	return filePath, nil
}

// CreateTestConfig creates a test configuration file
func (tf *TestFramework) CreateTestConfig(configData map[string]interface{}) (string, error) {
	// Create a basic config manager to write the config
	cfg := &config.Config{
		TalosVersion:      "v1.10.6",
		KubernetesVersion: "v1.33.3",
		OnePasswordVault:  "homeops",
		LogLevel:          "info",
		CacheDir:          tf.tempDir,
		SecretCacheTTL:    300, // 5 minutes in seconds
	}

	// Override with test data
	for key, value := range configData {
		switch key {
		case "talos_version":
			if v, ok := value.(string); ok {
				cfg.TalosVersion = v
			}
		case "kubernetes_version":
			if v, ok := value.(string); ok {
				cfg.KubernetesVersion = v
			}
		case "onepassword_vault":
			if v, ok := value.(string); ok {
				cfg.OnePasswordVault = v
			}
		case "log_level":
			if v, ok := value.(string); ok {
				cfg.LogLevel = v
			}
		case "secret_cache_ttl":
			if v, ok := value.(int); ok {
				cfg.SecretCacheTTL = v
			}
		}
	}

	// Write config to file (simplified YAML writing)
	configContent := fmt.Sprintf(`talos_version: %s
kubernetes_version: %s
onepassword_vault: %s
log_level: %s
cache_dir: %s
secret_cache_ttl: %d
`,
		cfg.TalosVersion, cfg.KubernetesVersion, cfg.OnePasswordVault,
		cfg.LogLevel, cfg.CacheDir, cfg.SecretCacheTTL)

	return tf.CreateTempFile("config.yaml", configContent)
}

// CreateMockYAML creates a mock YAML file for testing
func (tf *TestFramework) CreateMockYAML(filename, content string) (string, error) {
	if content == "" {
		content = `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
  namespace: default
data:
  key1: value1
  key2: value2
`
	}
	return tf.CreateTempFile(filename, content)
}

// RunWithMetrics executes a function and tracks its performance
func (tf *TestFramework) RunWithMetrics(name string, fn func() error) TestResult {
	start := time.Now()
	err := tf.metrics.TrackOperation(name, fn)
	duration := time.Since(start)

	report, _ := tf.metrics.GetOperationReport(name)
	metricsData := map[string]interface{}{
		"average_duration": report.AverageDuration,
		"total_calls":      report.TotalCalls,
		"error_rate":       report.ErrorRate,
	}

	return TestResult{
		Success:  err == nil,
		Duration: duration,
		Error:    err,
		Metrics:  metricsData,
	}
}

// AssertNoError fails the test if error is not nil
func (tf *TestFramework) AssertNoError(t *testing.T, err error, message string) {
	if err != nil {
		t.Fatalf("%s: %v", message, err)
	}
}

// AssertError fails the test if error is nil
func (tf *TestFramework) AssertError(t *testing.T, err error, message string) {
	if err == nil {
		t.Fatalf("%s: expected error but got nil", message)
	}
}

// AssertErrorType fails the test if error is not of expected type
func (tf *TestFramework) AssertErrorType(t *testing.T, err error, expectedType errors.ErrorType, message string) {
	if err == nil {
		t.Fatalf("%s: expected error of type %s but got nil", message, expectedType)
	}

	if !errors.IsType(err, expectedType) {
		actualType, _ := errors.GetType(err)
		t.Fatalf("%s: expected error type %s but got %s", message, expectedType, actualType)
	}
}

// AssertFileExists fails the test if file doesn't exist
func (tf *TestFramework) AssertFileExists(t *testing.T, filePath, message string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("%s: file %s does not exist", message, filePath)
	}
}

// AssertFileContains fails the test if file doesn't contain expected content
func (tf *TestFramework) AssertFileContains(t *testing.T, filePath, expectedContent, message string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("%s: failed to read file %s: %v", message, filePath, err)
	}

	if !strings.Contains(string(content), expectedContent) {
		t.Fatalf("%s: file %s does not contain expected content '%s'", message, filePath, expectedContent)
	}
}

// GetTempDir returns the temporary directory path
func (tf *TestFramework) GetTempDir() string {
	return tf.tempDir
}

// GetMetrics returns the performance collector
func (tf *TestFramework) GetMetrics() *metrics.PerformanceCollector {
	return tf.metrics
}

// AddCleanupFunction adds a function to be called during cleanup
func (tf *TestFramework) AddCleanupFunction(fn func() error) {
	tf.cleanupFns = append(tf.cleanupFns, fn)
}

// Cleanup removes temporary files and performs cleanup
func (tf *TestFramework) Cleanup() error {
	var errors []string

	// Run custom cleanup functions
	for _, fn := range tf.cleanupFns {
		if err := fn(); err != nil {
			errors = append(errors, err.Error())
		}
	}

	// Remove temporary directory
	if err := os.RemoveAll(tf.tempDir); err != nil {
		errors = append(errors, fmt.Sprintf("failed to remove temp dir: %v", err))
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// BenchmarkOperation runs a benchmark test for an operation
func (tf *TestFramework) BenchmarkOperation(b *testing.B, name string, fn func() error) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := tf.metrics.TrackOperation(name, fn); err != nil {
			b.Fatalf("Benchmark operation failed: %v", err)
		}
	}
	b.StopTimer()

	// Report metrics
	report, exists := tf.metrics.GetOperationReport(name)
	if exists {
		b.Logf("Average duration: %v, Error rate: %.2f%%",
			report.AverageDuration, report.ErrorRate*100)
	}
}
