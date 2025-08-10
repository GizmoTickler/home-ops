package errors

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// MockLogger implements RecoveryLogger for testing
type MockLogger struct {
	logs []LogEntry
}

type LogEntry struct {
	Level   string
	Message string
	Fields  []interface{}
}

func (m *MockLogger) Debug(msg string, fields ...interface{}) {
	m.logs = append(m.logs, LogEntry{Level: "DEBUG", Message: msg, Fields: fields})
}

func (m *MockLogger) Info(msg string, fields ...interface{}) {
	m.logs = append(m.logs, LogEntry{Level: "INFO", Message: msg, Fields: fields})
}

func (m *MockLogger) Warn(msg string, fields ...interface{}) {
	m.logs = append(m.logs, LogEntry{Level: "WARN", Message: msg, Fields: fields})
}

func (m *MockLogger) Error(msg string, fields ...interface{}) {
	m.logs = append(m.logs, LogEntry{Level: "ERROR", Message: msg, Fields: fields})
}

func (m *MockLogger) GetLogs() []LogEntry {
	return m.logs
}

func (m *MockLogger) Reset() {
	m.logs = make([]LogEntry, 0)
}

// TestHomeOpsError tests the basic error functionality
func TestHomeOpsError(t *testing.T) {
	err := NewValidationError("TEST_CODE", "Test message", nil)
	
	if err.Type != ErrTypeValidation {
		t.Errorf("Expected error type %v, got %v", ErrTypeValidation, err.Type)
	}
	
	if err.Code != "TEST_CODE" {
		t.Errorf("Expected error code TEST_CODE, got %s", err.Code)
	}
	
	if err.Message != "Test message" {
		t.Errorf("Expected message 'Test message', got %s", err.Message)
	}
}

// TestErrorContext tests error context functionality
func TestErrorContext(t *testing.T) {
	err := NewValidationError("TEST_CODE", "Test message", nil).
		WithContext("test_operation", "test_component").
		WithRequestID("req-123").
		WithStackTrace()
	
	if err.Context == nil {
		t.Fatal("Expected error context to be set")
	}
	
	if err.Context.Operation != "test_operation" {
		t.Errorf("Expected operation 'test_operation', got %s", err.Context.Operation)
	}
	
	if err.Context.Component != "test_component" {
		t.Errorf("Expected component 'test_component', got %s", err.Context.Component)
	}
	
	if err.Context.RequestID != "req-123" {
		t.Errorf("Expected request ID 'req-123', got %s", err.Context.RequestID)
	}
	
	if len(err.Context.StackTrace) == 0 {
		t.Error("Expected stack trace to be captured")
	}
}

// TestUserFriendlyMessages tests user-friendly error messages
func TestUserFriendlyMessages(t *testing.T) {
	tests := []struct {
		errorType ErrorType
		code      string
		expectedActions int
	}{
		{ErrTypeValidation, "REQUIRED_FIELD", 1},
		{ErrTypeKubernetes, "CONNECTION_FAILED", 2},
		{ErrTypeSecurity, "ENCRYPTION_FAILED", 2},
		{ErrTypeFileSystem, "FILE_NOT_FOUND", 2},
	}
	
	for _, test := range tests {
		t.Run(fmt.Sprintf("%s_%s", test.errorType, test.code), func(t *testing.T) {
			var err *HomeOpsError
			switch test.errorType {
			case ErrTypeValidation:
				err = NewValidationError(test.code, "Test message", nil)
			case ErrTypeKubernetes:
				err = NewKubernetesError(test.code, "Test message", nil)
			case ErrTypeSecurity:
				err = NewSecurityError(test.code, "Test message", nil)
			case ErrTypeFileSystem:
				err = NewFileSystemError(test.code, "Test message", nil)
			}
			
			msg := err.GetUserFriendlyMessage()
			if msg == nil {
				t.Fatal("Expected user-friendly message to be generated")
			}
			
			if len(msg.SuggestedActions) < test.expectedActions {
				t.Errorf("Expected at least %d suggested actions, got %d", 
					test.expectedActions, len(msg.SuggestedActions))
			}
			
			if msg.UserMessage == "" {
				t.Error("Expected user message to be set")
			}
		})
	}
}

// TestValidationFramework tests the validation framework
func TestValidationFramework(t *testing.T) {
	type TestStruct struct {
		Name     string `validate:"required"`
		Email    string `validate:"email"`
		MinField string `validate:"min=5"`
		MaxField string `validate:"max=10"`
	}
	
	context := &ValidationContext{
		Component: "test",
		Operation: "validation",
	}
	
	validator := NewValidator(context)
	
	// Test valid struct
	validStruct := TestStruct{
		Name:     "John Doe",
		Email:    "john@example.com",
		MinField: "12345",
		MaxField: "short",
	}
	
	result := validator.ValidateStruct(validStruct)
	if !result.IsValid {
		t.Errorf("Expected valid struct to pass validation, got errors: %v", result.Errors)
	}
	
	// Test invalid struct
	invalidStruct := TestStruct{
		Name:     "", // Required field empty
		Email:    "invalid-email", // Invalid email
		MinField: "123", // Too short
		MaxField: "this is too long", // Too long
	}
	
	result = validator.ValidateStruct(invalidStruct)
	if result.IsValid {
		t.Error("Expected invalid struct to fail validation")
	}
	
	if len(result.Errors) == 0 {
		t.Error("Expected validation errors to be reported")
	}
}

// TestRecoveryManager tests the recovery and retry functionality
func TestRecoveryManager(t *testing.T) {
	logger := &MockLogger{}
	rm := NewRecoveryManager(logger)
	
	// Test successful operation (no retry needed)
	ctx := context.Background()
	attempts := 0
	
	result := rm.ExecuteWithRecovery(ctx, "test_operation", func() error {
		attempts++
		return nil // Success on first try
	})
	
	if !result.Success {
		t.Error("Expected successful operation")
	}
	
	if result.Attempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", result.Attempts)
	}
	
	if result.Recovered {
		t.Error("Expected no recovery needed")
	}
	
	// Test operation that succeeds after retries
	attempts = 0
	result = rm.ExecuteWithRecovery(ctx, "test_retry", func() error {
		attempts++
		if attempts < 3 {
			return NewNetworkError("CONNECTION_FAILED", "Network error", nil)
		}
		return nil // Success on third try
	})
	
	if !result.Success {
		t.Error("Expected operation to succeed after retries")
	}
	
	if result.Attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", result.Attempts)
	}
	
	if !result.Recovered {
		t.Error("Expected recovery to be marked as true")
	}
	
	// Test operation that fails permanently
	attempts = 0
	result = rm.ExecuteWithRecovery(ctx, "test_failure", func() error {
		attempts++
		return NewValidationError("INVALID_INPUT", "Validation error", nil)
	})
	
	if result.Success {
		t.Error("Expected operation to fail")
	}
	
	if result.Attempts != 1 {
		t.Errorf("Expected 1 attempt for non-retryable error, got %d", result.Attempts)
	}
}

// TestExponentialBackoffStrategy tests the exponential backoff strategy
func TestExponentialBackoffStrategy(t *testing.T) {
	strategy := &ExponentialBackoffStrategy{
		MaxAttempts:   3,
		BaseDelay:     100 * time.Millisecond,
		MaxDelay:      1 * time.Second,
		Multiplier:    2.0,
		JitterEnabled: false, // Disable jitter for predictable testing
	}
	
	// Test retry decision
	networkErr := NewNetworkError("CONNECTION_FAILED", "Network error", nil)
	validationErr := NewValidationError("INVALID_INPUT", "Validation error", nil)
	
	if !strategy.ShouldRetry(networkErr, 1) {
		t.Error("Expected network error to be retryable")
	}
	
	if strategy.ShouldRetry(validationErr, 1) {
		t.Error("Expected validation error to not be retryable")
	}
	
	if strategy.ShouldRetry(networkErr, 4) {
		t.Error("Expected retry to be rejected after max attempts")
	}
	
	// Test backoff duration
	duration1 := strategy.GetBackoffDuration(1)
	duration2 := strategy.GetBackoffDuration(2)
	duration3 := strategy.GetBackoffDuration(3)
	
	if duration1 != 100*time.Millisecond {
		t.Errorf("Expected first backoff to be 100ms, got %v", duration1)
	}
	
	if duration2 != 200*time.Millisecond {
		t.Errorf("Expected second backoff to be 200ms, got %v", duration2)
	}
	
	if duration3 != 400*time.Millisecond {
		t.Errorf("Expected third backoff to be 400ms, got %v", duration3)
	}
}

// TestErrorMetrics tests the error metrics collection
func TestErrorMetrics(t *testing.T) {
	metrics := NewErrorMetrics()
	
	// Record some errors
	err1 := NewValidationError("REQUIRED_FIELD", "Field required", nil)
	err2 := NewNetworkError("CONNECTION_FAILED", "Network failed", nil)
	err3 := NewValidationError("INVALID_FORMAT", "Invalid format", nil)
	
	metrics.RecordError(err1)
	metrics.RecordError(err2)
	metrics.RecordError(err3)
	
	// Record recovery attempts
	metrics.RecordRecoveryAttempt(ErrTypeNetwork)
	metrics.RecordRecoverySuccess(ErrTypeNetwork)
	
	// Get report
	report := metrics.GetReport()
	
	if report.TotalErrors != 3 {
		t.Errorf("Expected 3 total errors, got %d", report.TotalErrors)
	}
	
	if report.ErrorsByType[ErrTypeValidation] != 2 {
		t.Errorf("Expected 2 validation errors, got %d", report.ErrorsByType[ErrTypeValidation])
	}
	
	if report.ErrorsByType[ErrTypeNetwork] != 1 {
		t.Errorf("Expected 1 network error, got %d", report.ErrorsByType[ErrTypeNetwork])
	}
	
	if report.RecoveryRates[ErrTypeNetwork] != 100.0 {
		t.Errorf("Expected 100%% recovery rate for network errors, got %f", 
			report.RecoveryRates[ErrTypeNetwork])
	}
	
	if len(report.MostCommonErrors) == 0 {
		t.Error("Expected most common errors to be populated")
	}
}

// TestHealthStatus tests the health status calculation
func TestHealthStatus(t *testing.T) {
	metrics := NewErrorMetrics()
	
	// Test healthy system
	health := metrics.GetHealthStatus()
	if health.Status != "excellent" {
		t.Errorf("Expected excellent health status for new metrics, got %s", health.Status)
	}
	
	if health.HealthScore != 100.0 {
		t.Errorf("Expected health score of 100, got %f", health.HealthScore)
	}
	
	// Add some critical errors
	securityErr := NewSecurityError("ENCRYPTION_FAILED", "Encryption failed", nil)
	metrics.RecordError(securityErr)
	
	health = metrics.GetHealthStatus()
	if health.CriticalErrors != 1 {
		t.Errorf("Expected 1 critical error, got %d", health.CriticalErrors)
	}
	
	if health.HealthScore >= 100.0 {
		t.Errorf("Expected health score to decrease after critical error, got %f", health.HealthScore)
	}
	
	if len(health.Recommendations) == 0 {
		t.Error("Expected recommendations to be provided")
	}
}

// TestAlertManager tests the alert management functionality
func TestAlertManager(t *testing.T) {
	metrics := NewErrorMetrics()
	alertManager := NewAlertManager(metrics)
	
	// Set low thresholds for testing
	alertManager.SetThreshold("error_rate", 0.1)
	alertManager.SetThreshold("critical_errors", 0.5)
	
	// Add some errors to trigger alerts
	securityErr := NewSecurityError("ENCRYPTION_FAILED", "Encryption failed", nil)
	metrics.RecordError(securityErr)
	
	// Check for alerts
	alerts := alertManager.CheckAlerts()
	if len(alerts) == 0 {
		t.Error("Expected alerts to be triggered")
	}
	
	// Check for critical error alert
	foundCriticalAlert := false
	for _, alert := range alerts {
		if alert.ID == "critical_errors" {
			foundCriticalAlert = true
			if alert.Severity != "critical" {
				t.Errorf("Expected critical severity, got %s", alert.Severity)
			}
			break
		}
	}
	
	if !foundCriticalAlert {
		t.Error("Expected critical error alert to be triggered")
	}
	
	// Test active alerts
	activeAlerts := alertManager.GetActiveAlerts()
	if len(activeAlerts) == 0 {
		t.Error("Expected active alerts to be returned")
	}
}

// TestGracefulDegradation tests the graceful degradation functionality
func TestGracefulDegradation(t *testing.T) {
	logger := &MockLogger{}
	gdm := NewGracefulDegradationManager(logger)
	
	// Register a fallback
	fallbackCalled := false
	gdm.RegisterFallback("test_operation", func() error {
		fallbackCalled = true
		return nil
	})
	
	// Test successful primary operation
	err := gdm.ExecuteWithFallback("test_operation", func() error {
		return nil
	})
	
	if err != nil {
		t.Errorf("Expected no error for successful operation, got %v", err)
	}
	
	if fallbackCalled {
		t.Error("Expected fallback not to be called for successful operation")
	}
	
	// Test failed primary operation with successful fallback
	fallbackCalled = false
	err = gdm.ExecuteWithFallback("test_operation", func() error {
		return NewNetworkError("CONNECTION_FAILED", "Network failed", nil)
	})
	
	if err != nil {
		t.Errorf("Expected no error when fallback succeeds, got %v", err)
	}
	
	if !fallbackCalled {
		t.Error("Expected fallback to be called for failed operation")
	}
	
	// Test operation without fallback
	err = gdm.ExecuteWithFallback("unknown_operation", func() error {
		return NewNetworkError("CONNECTION_FAILED", "Network failed", nil)
	})
	
	if err == nil {
		t.Error("Expected error when no fallback is available")
	}
}

// TestConfigValidator tests the configuration validation
func TestConfigValidator(t *testing.T) {
	validator := NewConfigValidator()
	
	// Test environment validation
	requiredVars := []string{"HOME", "USER"} // These should exist on most systems
	result := validator.ValidateEnvironment(requiredVars)
	
	if !result.IsValid {
		t.Errorf("Expected environment validation to pass, got errors: %v", result.Errors)
	}
	
	// Test with missing environment variable
	missingVars := []string{"NONEXISTENT_VAR_12345"}
	result = validator.ValidateEnvironment(missingVars)
	
	if result.IsValid {
		t.Error("Expected environment validation to fail for missing variable")
	}
	
	if len(result.Errors) == 0 {
		t.Error("Expected validation errors for missing environment variable")
	}
	
	// Test CLI tools validation
	commonTools := []string{"ls", "cat"} // These should exist on Unix systems
	result = validator.ValidateCLITools(commonTools)
	
	if !result.IsValid {
		t.Errorf("Expected CLI tools validation to pass, got errors: %v", result.Errors)
	}
	
	// Test with missing CLI tool
	missingTools := []string{"nonexistent_tool_12345"}
	result = validator.ValidateCLITools(missingTools)
	
	if result.IsValid {
		t.Error("Expected CLI tools validation to fail for missing tool")
	}
	
	if len(result.Errors) == 0 {
		t.Error("Expected validation errors for missing CLI tool")
	}
}

// BenchmarkErrorCreation benchmarks error creation performance
func BenchmarkErrorCreation(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := NewValidationError("TEST_CODE", "Test message", nil).
			WithContext("test_operation", "test_component").
			WithRequestID("req-123")
		_ = err
	}
}

// BenchmarkRecoveryExecution benchmarks recovery execution performance
func BenchmarkRecoveryExecution(b *testing.B) {
	logger := &MockLogger{}
	rm := NewRecoveryManager(logger)
	ctx := context.Background()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := rm.ExecuteWithRecovery(ctx, "benchmark_operation", func() error {
			return nil // Always succeed for benchmark
		})
		_ = result
	}
}

// BenchmarkMetricsCollection benchmarks metrics collection performance
func BenchmarkMetricsCollection(b *testing.B) {
	metrics := NewErrorMetrics()
	err := NewValidationError("TEST_CODE", "Test message", nil)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		metrics.RecordError(err)
	}
}