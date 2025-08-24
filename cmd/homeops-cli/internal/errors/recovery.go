package errors

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// RecoveryStrategy defines how to handle different types of errors
type RecoveryStrategy interface {
	ShouldRetry(err error, attempt int) bool
	GetBackoffDuration(attempt int) time.Duration
	GetMaxAttempts() int
	GetDescription() string
}

// RecoveryResult holds the result of a recovery operation
type RecoveryResult struct {
	Success   bool          `json:"success"`
	Attempts  int           `json:"attempts"`
	TotalTime time.Duration `json:"total_time"`
	LastError error         `json:"last_error,omitempty"`
	Recovered bool          `json:"recovered"`
	Strategy  string        `json:"strategy"`
}

// RecoveryManager handles error recovery and retry logic
type RecoveryManager struct {
	strategies      map[ErrorType]RecoveryStrategy
	defaultStrategy RecoveryStrategy
	logger          RecoveryLogger // Interface for logging
}

// RecoveryLogger interface for recovery logging
type RecoveryLogger interface {
	Debug(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	Error(msg string, fields ...interface{})
}

// NewRecoveryManager creates a new recovery manager
func NewRecoveryManager(logger RecoveryLogger) *RecoveryManager {
	rm := &RecoveryManager{
		strategies: make(map[ErrorType]RecoveryStrategy),
		logger:     logger,
	}

	// Set up default strategies
	rm.defaultStrategy = &ExponentialBackoffStrategy{
		MaxAttempts:   3,
		BaseDelay:     time.Second,
		MaxDelay:      30 * time.Second,
		Multiplier:    2.0,
		JitterEnabled: true,
	}

	// Configure specific strategies for different error types
	rm.strategies[ErrTypeNetwork] = &ExponentialBackoffStrategy{
		MaxAttempts:   5,
		BaseDelay:     500 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		Multiplier:    2.0,
		JitterEnabled: true,
	}

	rm.strategies[ErrTypeKubernetes] = &ExponentialBackoffStrategy{
		MaxAttempts:   3,
		BaseDelay:     2 * time.Second,
		MaxDelay:      30 * time.Second,
		Multiplier:    1.5,
		JitterEnabled: true,
	}

	rm.strategies[ErrTypeTalos] = &ExponentialBackoffStrategy{
		MaxAttempts:   3,
		BaseDelay:     3 * time.Second,
		MaxDelay:      45 * time.Second,
		Multiplier:    2.0,
		JitterEnabled: true,
	}

	// Security and validation errors should not be retried
	rm.strategies[ErrTypeSecurity] = &NoRetryStrategy{}
	rm.strategies[ErrTypeValidation] = &NoRetryStrategy{}

	return rm
}

// SetStrategy sets a custom recovery strategy for an error type
func (rm *RecoveryManager) SetStrategy(errorType ErrorType, strategy RecoveryStrategy) {
	rm.strategies[errorType] = strategy
}

// ExecuteWithRecovery executes a function with automatic retry and recovery
func (rm *RecoveryManager) ExecuteWithRecovery(
	ctx context.Context,
	operation string,
	fn func() error,
) *RecoveryResult {
	start := time.Now()
	result := &RecoveryResult{
		Strategy: "default",
	}

	var lastErr error
	attempt := 0

	for {
		attempt++

		// Check context cancellation
		select {
		case <-ctx.Done():
			result.LastError = ctx.Err()
			result.TotalTime = time.Since(start)
			result.Attempts = attempt - 1
			return result
		default:
		}

		rm.logger.Debug("Executing operation", "operation", operation, "attempt", attempt)

		err := fn()
		if err == nil {
			result.Success = true
			result.Attempts = attempt
			result.TotalTime = time.Since(start)
			result.Recovered = attempt > 1

			if result.Recovered {
				rm.logger.Info("Operation recovered after retries",
					"operation", operation,
					"attempts", attempt,
					"duration", result.TotalTime)
			}

			return result
		}

		lastErr = err
		strategy := rm.getStrategy(err)
		result.Strategy = strategy.GetDescription()

		// Check if we should retry
		if !strategy.ShouldRetry(err, attempt) || attempt >= strategy.GetMaxAttempts() {
			result.LastError = lastErr
			result.Attempts = attempt
			result.TotalTime = time.Since(start)

			rm.logger.Error("Operation failed after all retry attempts",
				"operation", operation,
				"attempts", attempt,
				"error", err.Error())

			return result
		}

		// Calculate backoff duration
		backoffDuration := strategy.GetBackoffDuration(attempt)

		rm.logger.Warn("Operation failed, retrying",
			"operation", operation,
			"attempt", attempt,
			"error", err.Error(),
			"backoff", backoffDuration)

		// Wait for backoff duration
		select {
		case <-ctx.Done():
			result.LastError = ctx.Err()
			result.TotalTime = time.Since(start)
			result.Attempts = attempt
			return result
		case <-time.After(backoffDuration):
			// Continue to next attempt
		}
	}
}

// getStrategy returns the appropriate recovery strategy for an error
func (rm *RecoveryManager) getStrategy(err error) RecoveryStrategy {
	if homeOpsErr, ok := err.(*HomeOpsError); ok {
		if strategy, exists := rm.strategies[homeOpsErr.Type]; exists {
			return strategy
		}
	}
	return rm.defaultStrategy
}

// ExponentialBackoffStrategy implements exponential backoff with jitter
type ExponentialBackoffStrategy struct {
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	Multiplier    float64
	JitterEnabled bool
}

func (s *ExponentialBackoffStrategy) ShouldRetry(err error, attempt int) bool {
	// Don't retry certain types of errors
	if homeOpsErr, ok := err.(*HomeOpsError); ok {
		switch homeOpsErr.Type {
		case ErrTypeSecurity, ErrTypeValidation:
			return false
		case ErrTypeFileSystem:
			// Only retry file system errors that might be transient
			return homeOpsErr.Code == "TEMP_UNAVAILABLE" || homeOpsErr.Code == "LOCK_TIMEOUT"
		}
	}
	return attempt < s.MaxAttempts
}

func (s *ExponentialBackoffStrategy) GetBackoffDuration(attempt int) time.Duration {
	delay := float64(s.BaseDelay) * math.Pow(s.Multiplier, float64(attempt-1))

	if s.JitterEnabled {
		// Add jitter (±25%)
		jitter := delay * 0.25 * (rand.Float64()*2 - 1)
		delay += jitter
	}

	if delay > float64(s.MaxDelay) {
		delay = float64(s.MaxDelay)
	}

	if delay < 0 {
		delay = float64(s.BaseDelay)
	}

	return time.Duration(delay)
}

func (s *ExponentialBackoffStrategy) GetMaxAttempts() int {
	return s.MaxAttempts
}

func (s *ExponentialBackoffStrategy) GetDescription() string {
	return fmt.Sprintf("exponential_backoff(max_attempts=%d, base_delay=%v, max_delay=%v)",
		s.MaxAttempts, s.BaseDelay, s.MaxDelay)
}

// LinearBackoffStrategy implements linear backoff
type LinearBackoffStrategy struct {
	MaxAttempts int
	Delay       time.Duration
}

func (s *LinearBackoffStrategy) ShouldRetry(err error, attempt int) bool {
	if homeOpsErr, ok := err.(*HomeOpsError); ok {
		switch homeOpsErr.Type {
		case ErrTypeSecurity, ErrTypeValidation:
			return false
		}
	}
	return attempt < s.MaxAttempts
}

func (s *LinearBackoffStrategy) GetBackoffDuration(attempt int) time.Duration {
	return s.Delay
}

func (s *LinearBackoffStrategy) GetMaxAttempts() int {
	return s.MaxAttempts
}

func (s *LinearBackoffStrategy) GetDescription() string {
	return fmt.Sprintf("linear_backoff(max_attempts=%d, delay=%v)", s.MaxAttempts, s.Delay)
}

// NoRetryStrategy never retries
type NoRetryStrategy struct{}

func (s *NoRetryStrategy) ShouldRetry(err error, attempt int) bool {
	return false
}

func (s *NoRetryStrategy) GetBackoffDuration(attempt int) time.Duration {
	return 0
}

func (s *NoRetryStrategy) GetMaxAttempts() int {
	return 1
}

func (s *NoRetryStrategy) GetDescription() string {
	return "no_retry"
}

// CircuitBreakerStrategy implements circuit breaker pattern
type CircuitBreakerStrategy struct {
	MaxAttempts      int
	FailureThreshold int
	RecoveryTimeout  time.Duration
	BaseDelay        time.Duration

	// Internal state
	failureCount    int
	lastFailureTime time.Time
	state           CircuitState
}

type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

func (s *CircuitBreakerStrategy) ShouldRetry(err error, attempt int) bool {
	now := time.Now()

	// Update circuit state
	switch s.state {
	case CircuitClosed:
		if attempt == 1 {
			s.failureCount++
			s.lastFailureTime = now
			if s.failureCount >= s.FailureThreshold {
				s.state = CircuitOpen
				return false
			}
		}
		return attempt < s.MaxAttempts

	case CircuitOpen:
		if now.Sub(s.lastFailureTime) > s.RecoveryTimeout {
			s.state = CircuitHalfOpen
			return attempt == 1 // Only allow one attempt in half-open
		}
		return false

	case CircuitHalfOpen:
		// If we're here, the previous attempt failed
		s.state = CircuitOpen
		s.lastFailureTime = now
		return false
	}

	return false
}

func (s *CircuitBreakerStrategy) GetBackoffDuration(attempt int) time.Duration {
	if s.state == CircuitOpen {
		return s.RecoveryTimeout
	}
	return s.BaseDelay
}

func (s *CircuitBreakerStrategy) GetMaxAttempts() int {
	return s.MaxAttempts
}

func (s *CircuitBreakerStrategy) GetDescription() string {
	return fmt.Sprintf("circuit_breaker(state=%v, failures=%d)", s.state, s.failureCount)
}

// Reset resets the circuit breaker on successful operation
func (s *CircuitBreakerStrategy) Reset() {
	s.failureCount = 0
	s.state = CircuitClosed
}

// GracefulDegradationManager handles fallback mechanisms
type GracefulDegradationManager struct {
	fallbacks map[string]func() error
	logger    RecoveryLogger
}

// NewGracefulDegradationManager creates a new graceful degradation manager
func NewGracefulDegradationManager(logger RecoveryLogger) *GracefulDegradationManager {
	return &GracefulDegradationManager{
		fallbacks: make(map[string]func() error),
		logger:    logger,
	}
}

// RegisterFallback registers a fallback function for an operation
func (gdm *GracefulDegradationManager) RegisterFallback(operation string, fallback func() error) {
	gdm.fallbacks[operation] = fallback
}

// ExecuteWithFallback executes an operation with fallback if it fails
func (gdm *GracefulDegradationManager) ExecuteWithFallback(
	operation string,
	primary func() error,
) error {
	err := primary()
	if err == nil {
		return nil
	}

	// Check if we have a fallback for this operation
	if fallback, exists := gdm.fallbacks[operation]; exists {
		gdm.logger.Warn("Primary operation failed, attempting fallback",
			"operation", operation,
			"error", err.Error())

		fallbackErr := fallback()
		if fallbackErr == nil {
			gdm.logger.Info("Fallback operation succeeded", "operation", operation)
			return nil
		}

		gdm.logger.Error("Fallback operation also failed",
			"operation", operation,
			"primary_error", err.Error(),
			"fallback_error", fallbackErr.Error())

		// Return the original error, but include fallback error in details
		if homeOpsErr, ok := err.(*HomeOpsError); ok {
			return homeOpsErr.WithDetail("fallback_error", fallbackErr.Error())
		}
	}

	return err
}
