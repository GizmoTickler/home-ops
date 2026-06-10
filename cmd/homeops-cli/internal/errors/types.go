// Package errors provides typed, code-tagged errors for operations where the
// category matters to callers (security checks, validation, template
// rendering, filesystem access). Plain fmt.Errorf wrapping remains the norm
// elsewhere; reach for these constructors when a caller branches on IsType.
package errors

import (
	"fmt"
)

// ErrorType represents the category of error
type ErrorType string

const (
	ErrTypeTemplate   ErrorType = "template"
	ErrTypeValidation ErrorType = "validation"
	ErrTypeSecurity   ErrorType = "security"
	ErrTypeFileSystem ErrorType = "filesystem"
	ErrTypeNotFound   ErrorType = "notfound"
)

// HomeOpsError represents a structured error with a category and stable code
type HomeOpsError struct {
	Type    ErrorType `json:"type"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Cause   error     `json:"-"`
}

// Error implements the error interface
func (e *HomeOpsError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause for error wrapping
func (e *HomeOpsError) Unwrap() error {
	return e.Cause
}

// NewTemplateError creates a new template-related error
func NewTemplateError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{Type: ErrTypeTemplate, Code: code, Message: message, Cause: cause}
}

// NewValidationError creates a new validation error
func NewValidationError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{Type: ErrTypeValidation, Code: code, Message: message, Cause: cause}
}

// NewSecurityError creates a new security-related error
func NewSecurityError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{Type: ErrTypeSecurity, Code: code, Message: message, Cause: cause}
}

// NewFileSystemError creates a new filesystem error
func NewFileSystemError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{Type: ErrTypeFileSystem, Code: code, Message: message, Cause: cause}
}

// NewNotFoundError creates a new not found error
func NewNotFoundError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{Type: ErrTypeNotFound, Code: code, Message: message, Cause: cause}
}

// IsType checks if an error is of a specific type
func IsType(err error, errorType ErrorType) bool {
	if homeOpsErr, ok := err.(*HomeOpsError); ok {
		return homeOpsErr.Type == errorType
	}
	return false
}
