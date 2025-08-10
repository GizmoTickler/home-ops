package internal

import (
	"testing"
	"time"

	"homeops-cli/internal/config"
	"homeops-cli/internal/errors"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/security"
	testframework "homeops-cli/internal/testing"
	"homeops-cli/internal/yaml"
)

// TestFoundationComponents tests the basic functionality of Phase 1 components
func TestFoundationComponents(t *testing.T) {
	testConfig := testframework.TestConfig{
		LogLevel:    "debug",
		Timeout:     time.Minute,
		CleanupMode: "always",
	}

	framework, err := testframework.NewTestFramework(t, testConfig)
	if err != nil {
		t.Fatalf("Failed to create test framework: %v", err)
	}

	t.Run("ConfigManager", func(t *testing.T) {
		testConfigManager(t, framework)
	})

	t.Run("ErrorHandling", func(t *testing.T) {
		testErrorHandling(t, framework)
	})

	t.Run("YAMLProcessor", func(t *testing.T) {
		testYAMLProcessor(t, framework)
	})

	t.Run("MetricsCollector", func(t *testing.T) {
		testMetricsCollector(t, framework)
	})

	t.Run("SecretCache", func(t *testing.T) {
		testSecretCache(t, framework)
	})
}

func testConfigManager(t *testing.T, framework *testframework.TestFramework) {
	// Create test config file
	configData := map[string]interface{}{
		"talos_version":      "v1.6.0",
		"kubernetes_version": "v1.29.0",
		"log_level":          "debug",
		"secret_cache_ttl":   600,
	}

	configPath, err := framework.CreateTestConfig(configData)
	framework.AssertNoError(t, err, "Failed to create test config")

	// Test config loading
	result := framework.RunWithMetrics("config_load", func() error {
		cfg, err := config.LoadConfigFromPath(configPath)
		if err != nil {
			return err
		}

		// Validate loaded config
		if cfg.TalosVersion != "v1.6.0" {
			t.Errorf("Expected TalosVersion v1.6.0, got %s", cfg.TalosVersion)
		}
		if cfg.KubernetesVersion != "v1.29.0" {
			t.Errorf("Expected KubernetesVersion v1.29.0, got %s", cfg.KubernetesVersion)
		}
		if cfg.LogLevel != "debug" {
			t.Errorf("Expected LogLevel debug, got %s", cfg.LogLevel)
		}
		if cfg.SecretCacheTTL != 600 {
			t.Errorf("Expected SecretCacheTTL 600, got %d", cfg.SecretCacheTTL)
		}

		return cfg.Validate()
	})

	framework.AssertNoError(t, result.Error, "Config loading failed")
	t.Logf("Config loading took: %v", result.Duration)
}

func testErrorHandling(t *testing.T, framework *testframework.TestFramework) {
	// Test different error types
	tests := []struct {
		name        string
		errorType   errors.ErrorType
		constructor func() error
	}{
		{
			name:      "ValidationError",
			errorType: errors.ErrTypeValidation,
			constructor: func() error {
				return errors.NewValidationError("TEST_VALIDATION", "test validation error", nil)
			},
		},
		{
			name:      "SecurityError",
			errorType: errors.ErrTypeSecurity,
			constructor: func() error {
				return errors.NewSecurityError("TEST_SECURITY", "test security error", nil)
			},
		},
		{
			name:      "NotFoundError",
			errorType: errors.ErrTypeNotFound,
			constructor: func() error {
				return errors.NewNotFoundError("TEST_NOT_FOUND", "test not found error", nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.constructor()
			framework.AssertError(t, err, "Expected error to be created")
			framework.AssertErrorType(t, err, test.errorType, "Error type mismatch")
		})
	}
}

func testYAMLProcessor(t *testing.T, framework *testframework.TestFramework) {
	yamlContent := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
  namespace: default
data:
  key1: value1
  key2: value2
  nested:
    subkey: subvalue
`

	yamlFile, err := framework.CreateMockYAML("test.yaml", yamlContent)
	framework.AssertNoError(t, err, "Failed to create test YAML file")

	metricsCollector := metrics.NewPerformanceCollector()
	processor := yaml.NewProcessor(nil, metricsCollector)

	// Test parsing
	result := framework.RunWithMetrics("yaml_parse", func() error {
		data, err := processor.ParseFile(yamlFile)
		if err != nil {
			return err
		}

		// Test getting values
		name, err := processor.GetValue(data, "metadata.name")
		if err != nil {
			return err
		}
		if name != "test-config" {
			t.Errorf("Expected name 'test-config', got %v", name)
		}

		// Test setting values
		err = processor.SetValue(data, "metadata.labels.environment", "test")
		if err != nil {
			return err
		}

		// Test writing back
		outputFile := framework.GetTempDir() + "/output.yaml"
		return processor.WriteFile(outputFile, data)
	})

	framework.AssertNoError(t, result.Error, "YAML processing failed")
	t.Logf("YAML processing took: %v", result.Duration)
}

func testMetricsCollector(t *testing.T, framework *testframework.TestFramework) {
	collector := metrics.NewPerformanceCollector()

	// Test operation tracking
	err := collector.TrackOperation("test_operation", func() error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	framework.AssertNoError(t, err, "Operation tracking failed")

	// Test metrics retrieval
	report, exists := collector.GetOperationReport("test_operation")
	if !exists {
		t.Fatal("Expected operation report to exist")
	}

	if report.TotalCalls != 1 {
		t.Errorf("Expected 1 call, got %d", report.TotalCalls)
	}

	if report.AverageDuration < time.Millisecond {
		t.Errorf("Expected duration > 1ms, got %v", report.AverageDuration)
	}

	t.Logf("Metrics: calls=%d, avg_duration=%v, error_rate=%.2f%%", 
		report.TotalCalls, report.AverageDuration, report.ErrorRate*100)
}

func testSecretCache(t *testing.T, framework *testframework.TestFramework) {
	cache, err := security.NewSecretCache("test-password", time.Minute)
	framework.AssertNoError(t, err, "Failed to create secret cache")

	testSecret := []byte("super-secret-data")
	testKey := "test-secret"

	// Test storing secret
	result := framework.RunWithMetrics("secret_store", func() error {
		return cache.Store(testKey, testSecret)
	})
	framework.AssertNoError(t, result.Error, "Secret storage failed")

	// Test retrieving secret
	result = framework.RunWithMetrics("secret_retrieve", func() error {
		retrieved, err := cache.Retrieve(testKey)
		if err != nil {
			return err
		}

		if string(retrieved) != string(testSecret) {
			t.Errorf("Retrieved secret doesn't match original")
		}

		return nil
	})
	framework.AssertNoError(t, result.Error, "Secret retrieval failed")

	// Test cache metadata
	metadata := cache.GetMetadata()
	if len(metadata) != 1 {
		t.Errorf("Expected 1 cached secret, got %d", len(metadata))
	}

	if metadata[0].Key != testKey {
		t.Errorf("Expected key %s, got %s", testKey, metadata[0].Key)
	}

	t.Logf("Secret cache: size=%d, access_count=%d", 
		cache.Size(), metadata[0].AccessCount)
}