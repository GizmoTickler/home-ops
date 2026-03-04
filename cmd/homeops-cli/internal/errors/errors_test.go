package errors

import (
	"fmt"
	"testing"
)

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
		errorType       ErrorType
		code            string
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
				return // This return is needed to satisfy staticcheck
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

// BenchmarkErrorCreation benchmarks error creation performance
func BenchmarkErrorCreation(b *testing.B) {
	b.ResetTimer()
	for b.Loop() {
		err := NewValidationError("TEST_CODE", "Test message", nil).
			WithContext("test_operation", "test_component").
			WithRequestID("req-123")
		_ = err
	}
}
