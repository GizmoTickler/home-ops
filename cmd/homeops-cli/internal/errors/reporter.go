package errors

import (
	"context"
	"fmt"
	"sync"
	"time"

	"homeops-cli/internal/metrics"
)

// ErrorHandler defines the interface for handling specific error types
type ErrorHandler interface {
	Handle(ctx context.Context, err *HomeOpsError) error
	CanHandle(errorType ErrorType) bool
	GetPriority() int // Higher priority handlers are called first
}

// ErrorReporter provides centralized error reporting and handling
type ErrorReporter struct {
	logger   Logger // Interface for logging
	metrics  *metrics.PerformanceCollector
	handlers []ErrorHandler
	mu       sync.RWMutex
}

// Logger interface for dependency injection
type Logger interface {
	Error(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Debug(msg string, fields ...interface{})
}

// NewErrorReporter creates a new error reporter
func NewErrorReporter(logger Logger, metrics *metrics.PerformanceCollector) *ErrorReporter {
	return &ErrorReporter{
		logger:   logger,
		metrics:  metrics,
		handlers: make([]ErrorHandler, 0),
	}
}

// RegisterHandler adds an error handler
func (er *ErrorReporter) RegisterHandler(handler ErrorHandler) {
	er.mu.Lock()
	defer er.mu.Unlock()

	// Insert handler in priority order (highest first)
	inserted := false
	for i, h := range er.handlers {
		if handler.GetPriority() > h.GetPriority() {
			er.handlers = append(er.handlers[:i], append([]ErrorHandler{handler}, er.handlers[i:]...)...)
			inserted = true
			break
		}
	}
	if !inserted {
		er.handlers = append(er.handlers, handler)
	}
}

// ReportError processes and reports an error through registered handlers
func (er *ErrorReporter) ReportError(ctx context.Context, err *HomeOpsError) error {
	if err == nil {
		return nil
	}

	// Add context if missing
	if err.Context == nil {
		_ = err.WithContext("unknown", "error_reporter")
	}

	// Log the error
	er.logError(err)

	// Update metrics
	er.updateMetrics(err)

	// Try handlers in priority order
	er.mu.RLock()
	handlers := make([]ErrorHandler, len(er.handlers))
	copy(handlers, er.handlers)
	er.mu.RUnlock()

	for _, handler := range handlers {
		if handler.CanHandle(err.Type) {
			if handleErr := handler.Handle(ctx, err); handleErr != nil {
				er.logger.Error("Error handler failed", "handler_type", fmt.Sprintf("%T", handler), "error", handleErr)
				continue
			}
			// Handler succeeded, stop processing
			return nil
		}
	}

	// No handler could process the error
	return err
}

// logError logs the error with appropriate level and context
func (er *ErrorReporter) logError(err *HomeOpsError) {
	fields := []interface{}{
		"error_type", string(err.Type),
		"error_code", err.Code,
		"message", err.Message,
	}

	if err.Context != nil {
		fields = append(fields,
			"operation", err.Context.Operation,
			"component", err.Context.Component,
			"timestamp", err.Context.Timestamp,
		)
		if err.Context.RequestID != "" {
			fields = append(fields, "request_id", err.Context.RequestID)
		}
	}

	if len(err.Details) > 0 {
		fields = append(fields, "details", err.Details)
	}

	if err.Cause != nil {
		fields = append(fields, "cause", err.Cause.Error())
	}

	// Log at appropriate level based on error type
	switch err.Type {
	case ErrTypeSecurity:
		er.logger.Error("Security error occurred", fields...)
	case ErrTypeValidation:
		er.logger.Warn("Validation error occurred", fields...)
	case ErrTypeNotFound:
		er.logger.Info("Resource not found", fields...)
	default:
		er.logger.Error("Error occurred", fields...)
	}
}

// updateMetrics updates error metrics
func (er *ErrorReporter) updateMetrics(err *HomeOpsError) {
	if er.metrics == nil {
		return
	}

	// Track error by type
	metricName := fmt.Sprintf("error_%s", string(err.Type))
	_ = er.metrics.TrackOperation(metricName, func() error {
		return err // This will increment error count
	})

	// Track error by code
	if err.Code != "" {
		codeMetricName := fmt.Sprintf("error_code_%s", err.Code)
		_ = er.metrics.TrackOperation(codeMetricName, func() error {
			return err
		})
	}
}

// GetErrorMetrics returns error-related metrics
func (er *ErrorReporter) GetErrorMetrics() map[string]interface{} {
	if er.metrics == nil {
		return nil
	}

	report := er.metrics.GetReport()
	errorMetrics := make(map[string]interface{})

	for name, metrics := range report {
		if len(name) > 6 && name[:6] == "error_" {
			// Calculate total errors from error rate and total calls
			totalErrors := int64(float64(metrics.TotalCalls) * metrics.ErrorRate)
			errorMetrics[name] = map[string]interface{}{
				"total_errors":    totalErrors,
				"total_calls":     metrics.TotalCalls,
				"error_rate":      metrics.ErrorRate,
				"last_occurrence": metrics.LastExecution,
			}
		}
	}

	return errorMetrics
}

// RetryHandler implements automatic retry logic for transient errors
type RetryHandler struct {
	MaxAttempts    int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	Multiplier     float64
	RetryableTypes map[ErrorType]bool
}

// NewRetryHandler creates a new retry handler
func NewRetryHandler() *RetryHandler {
	return &RetryHandler{
		MaxAttempts: 3,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Multiplier:  2.0,
		RetryableTypes: map[ErrorType]bool{
			ErrTypeNetwork:    true,
			ErrTypeKubernetes: true,
			ErrTypeTalos:      true,
		},
	}
}

// CanHandle checks if this handler can process the error type
func (rh *RetryHandler) CanHandle(errorType ErrorType) bool {
	return rh.RetryableTypes[errorType]
}

// GetPriority returns the handler priority
func (rh *RetryHandler) GetPriority() int {
	return 100 // High priority for retry logic
}

// Handle implements the ErrorHandler interface
func (rh *RetryHandler) Handle(ctx context.Context, err *HomeOpsError) error {
	// This is a placeholder - actual retry logic would need the original operation
	// In practice, this would be implemented at the operation level
	return fmt.Errorf("retry handler requires operation context")
}

// WithRetry wraps an operation with retry logic
func (rh *RetryHandler) WithRetry(ctx context.Context, operation func() error) error {
	var lastErr error

	for attempt := 1; attempt <= rh.MaxAttempts; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if homeOpsErr, ok := err.(*HomeOpsError); ok {
			if !rh.CanHandle(homeOpsErr.Type) {
				return err // Not retryable
			}
		} else {
			return err // Not a HomeOpsError, don't retry
		}

		// Don't sleep on the last attempt
		if attempt < rh.MaxAttempts {
			delay := rh.calculateDelay(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				// Continue to next attempt
			}
		}
	}

	return lastErr
}

// calculateDelay calculates the delay for the given attempt using exponential backoff
func (rh *RetryHandler) calculateDelay(attempt int) time.Duration {
	delay := float64(rh.BaseDelay) * pow(rh.Multiplier, float64(attempt-1))
	if delay > float64(rh.MaxDelay) {
		delay = float64(rh.MaxDelay)
	}
	return time.Duration(delay)
}

// pow is a simple power function for float64
func pow(base, exp float64) float64 {
	result := 1.0
	for i := 0; i < int(exp); i++ {
		result *= base
	}
	return result
}
