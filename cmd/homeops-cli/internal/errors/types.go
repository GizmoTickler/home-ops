package errors

import (
	"fmt"
	"runtime"
	"time"
)

// ErrorType represents the category of error
type ErrorType string

const (
	ErrTypeTemplate   ErrorType = "template"
	ErrTypeKubernetes ErrorType = "kubernetes"
	ErrTypeTalos      ErrorType = "talos"
	ErrTypeValidation ErrorType = "validation"
	ErrTypeNetwork    ErrorType = "network"
	ErrTypeConfig     ErrorType = "config"
	ErrTypeSecurity   ErrorType = "security"
	ErrTypeFileSystem ErrorType = "filesystem"
	ErrTypeNotFound   ErrorType = "notfound"
)

// ErrorContext provides additional context for error tracking and debugging
type ErrorContext struct {
	Operation   string                 `json:"operation"`
	Component   string                 `json:"component"`
	RequestID   string                 `json:"request_id,omitempty"`
	UserContext map[string]interface{} `json:"user_context,omitempty"`
	Timestamp   time.Time              `json:"timestamp"`
	StackTrace  []string               `json:"stack_trace,omitempty"`
}

// ErrorMessage provides user-friendly error information
type ErrorMessage struct {
	UserMessage        string   `json:"user_message"`
	TechnicalDetails   string   `json:"technical_details"`
	SuggestedActions   []string `json:"suggested_actions,omitempty"`
	DocumentationLinks []string `json:"documentation_links,omitempty"`
}

// HomeOpsError represents a structured error with context
type HomeOpsError struct {
	Type    ErrorType              `json:"type"`
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	Cause   error                  `json:"-"`
	Context *ErrorContext          `json:"context,omitempty"`
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

// WithDetail adds a detail to the error
func (e *HomeOpsError) WithDetail(key string, value interface{}) *HomeOpsError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithContext adds context information to the error
func (e *HomeOpsError) WithContext(operation, component string) *HomeOpsError {
	if e.Context == nil {
		e.Context = &ErrorContext{
			Timestamp: time.Now(),
		}
	}
	e.Context.Operation = operation
	e.Context.Component = component
	return e
}

// WithRequestID adds a request ID to the error context
func (e *HomeOpsError) WithRequestID(requestID string) *HomeOpsError {
	if e.Context == nil {
		e.Context = &ErrorContext{
			Timestamp: time.Now(),
		}
	}
	e.Context.RequestID = requestID
	return e
}

// WithStackTrace captures the current stack trace
func (e *HomeOpsError) WithStackTrace() *HomeOpsError {
	if e.Context == nil {
		e.Context = &ErrorContext{
			Timestamp: time.Now(),
		}
	}

	// Capture stack trace
	var stackTrace []string
	for i := 1; i < 10; i++ { // Limit to 10 frames
		_, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		stackTrace = append(stackTrace, fmt.Sprintf("%s:%d", file, line))
	}
	e.Context.StackTrace = stackTrace
	return e
}

// GetUserFriendlyMessage returns a user-friendly error message with suggestions
func (e *HomeOpsError) GetUserFriendlyMessage() *ErrorMessage {
	msg := &ErrorMessage{
		TechnicalDetails: e.Error(),
	}

	// Generate user-friendly messages based on error type and code
	switch e.Type {
	case ErrTypeTemplate:
		msg.UserMessage = "Template processing failed"
		msg.SuggestedActions = []string{
			"Check template syntax and variables",
			"Verify 1Password secrets are accessible",
			"Ensure template file exists and is readable",
		}
	case ErrTypeKubernetes:
		msg.UserMessage = "Kubernetes operation failed"
		msg.SuggestedActions = []string{
			"Check cluster connectivity",
			"Verify KUBECONFIG is set correctly",
			"Ensure required permissions are granted",
		}
	case ErrTypeTalos:
		msg.UserMessage = "Talos operation failed"
		msg.SuggestedActions = []string{
			"Check Talos node connectivity",
			"Verify TALOSCONFIG is set correctly",
			"Ensure talosctl is installed and accessible",
		}
	case ErrTypeValidation:
		msg.UserMessage = "Configuration validation failed"
		msg.SuggestedActions = []string{
			"Review configuration parameters",
			"Check required fields are provided",
			"Validate configuration format",
		}
	case ErrTypeNetwork:
		msg.UserMessage = "Network operation failed"
		msg.SuggestedActions = []string{
			"Check network connectivity",
			"Verify firewall settings",
			"Ensure target services are running",
		}
	case ErrTypeConfig:
		msg.UserMessage = "Configuration error"
		msg.SuggestedActions = []string{
			"Check configuration file syntax",
			"Verify environment variables are set",
			"Ensure configuration file exists",
		}
	case ErrTypeSecurity:
		msg.UserMessage = "Security operation failed"
		msg.SuggestedActions = []string{
			"Check authentication credentials",
			"Verify access permissions",
			"Ensure security tokens are valid",
		}
	case ErrTypeFileSystem:
		msg.UserMessage = "File system operation failed"
		msg.SuggestedActions = []string{
			"Check file/directory permissions",
			"Verify file path exists",
			"Ensure sufficient disk space",
		}
	case ErrTypeNotFound:
		msg.UserMessage = "Resource not found"
		msg.SuggestedActions = []string{
			"Verify resource name and path",
			"Check if resource exists",
			"Ensure proper access permissions",
		}
	default:
		msg.UserMessage = "An error occurred"
		msg.SuggestedActions = []string{
			"Check the error details for more information",
			"Retry the operation",
			"Contact support if the issue persists",
		}
	}

	// Add documentation links based on error type
	switch e.Type {
	case ErrTypeTemplate:
		msg.DocumentationLinks = []string{
			"https://github.com/flosch/pongo2",
			"https://1password.com/developers/connect/",
		}
	case ErrTypeKubernetes:
		msg.DocumentationLinks = []string{
			"https://kubernetes.io/docs/reference/kubectl/",
		}
	case ErrTypeTalos:
		msg.DocumentationLinks = []string{
			"https://www.talos.dev/docs/",
		}
	}

	return msg
}

// NewTemplateError creates a new template-related error
func NewTemplateError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeTemplate,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewKubernetesError creates a new Kubernetes-related error
func NewKubernetesError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeKubernetes,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewTalosError creates a new Talos-related error
func NewTalosError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeTalos,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewValidationError creates a new validation error
func NewValidationError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeValidation,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewNetworkError creates a new network-related error
func NewNetworkError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeNetwork,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewConfigError creates a new configuration error
func NewConfigError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeConfig,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewSecurityError creates a new security-related error
func NewSecurityError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeSecurity,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewFileSystemError creates a new filesystem error
func NewFileSystemError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeFileSystem,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// NewNotFoundError creates a new not found error
func NewNotFoundError(code, message string, cause error) *HomeOpsError {
	return &HomeOpsError{
		Type:    ErrTypeNotFound,
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// IsType checks if an error is of a specific type
func IsType(err error, errorType ErrorType) bool {
	if homeOpsErr, ok := err.(*HomeOpsError); ok {
		return homeOpsErr.Type == errorType
	}
	return false
}

// GetType returns the error type if it's a HomeOpsError
func GetType(err error) (ErrorType, bool) {
	if homeOpsErr, ok := err.(*HomeOpsError); ok {
		return homeOpsErr.Type, true
	}
	return "", false
}

// Wrap wraps a standard error into a HomeOpsError with context.
// This is a convenience function for gradually migrating from fmt.Errorf.
func Wrap(err error, errType ErrorType, code, message string) *HomeOpsError {
	if err == nil {
		return nil
	}
	return &HomeOpsError{
		Type:    errType,
		Code:    code,
		Message: message,
		Cause:   err,
	}
}

// WrapTemplate wraps an error as a template error
func WrapTemplate(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeTemplate, "TEMPLATE_ERROR", message)
}

// WrapKubernetes wraps an error as a Kubernetes error
func WrapKubernetes(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeKubernetes, "K8S_ERROR", message)
}

// WrapTalos wraps an error as a Talos error
func WrapTalos(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeTalos, "TALOS_ERROR", message)
}

// WrapValidation wraps an error as a validation error
func WrapValidation(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeValidation, "VALIDATION_ERROR", message)
}

// WrapNetwork wraps an error as a network error
func WrapNetwork(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeNetwork, "NETWORK_ERROR", message)
}

// WrapConfig wraps an error as a configuration error
func WrapConfig(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeConfig, "CONFIG_ERROR", message)
}

// WrapFileSystem wraps an error as a filesystem error
func WrapFileSystem(err error, message string) *HomeOpsError {
	return Wrap(err, ErrTypeFileSystem, "FS_ERROR", message)
}
