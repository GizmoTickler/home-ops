package errors

import (
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
