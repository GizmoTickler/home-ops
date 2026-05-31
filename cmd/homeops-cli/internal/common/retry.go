package common

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// RetryConfig configures Retry. The zero value is usable (1 attempt, no delay);
// Retry applies the documented fallbacks for unset fields.
type RetryConfig struct {
	// Attempts is the total number of tries. Values < 1 are treated as 1.
	Attempts int
	// BaseDelay is the backoff before the 2nd attempt; it doubles each retry.
	BaseDelay time.Duration
	// MaxDelay caps the per-retry backoff (0 = uncapped).
	MaxDelay time.Duration
	// Retryable decides whether an error is worth retrying. nil => IsRetryableError.
	Retryable func(error) bool
	// Sleep performs the backoff wait (injectable for tests). nil => time.Sleep.
	Sleep func(time.Duration)
	// Logger, if set, logs each retry at Debug level.
	Logger *ColorLogger
}

// DefaultAPIRetry returns a sensible config for transient, idempotent network/API
// calls: 4 tries with 500ms → 1s → 2s backoff (capped at 4s).
func DefaultAPIRetry() RetryConfig {
	return RetryConfig{Attempts: 4, BaseDelay: 500 * time.Millisecond, MaxDelay: 4 * time.Second}
}

// Retry runs fn until it succeeds, returns a non-retryable error, or exhausts the
// configured attempts, applying exponential backoff between tries. It returns the
// last error fn produced. Only use it for IDEMPOTENT operations — never blind-retry
// a mutating call that may have partially applied.
func Retry(cfg RetryConfig, fn func() error) error {
	attempts := cfg.Attempts
	if attempts < 1 {
		attempts = 1
	}
	retryable := cfg.Retryable
	if retryable == nil {
		retryable = IsRetryableError
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	delay := cfg.BaseDelay
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt == attempts || !retryable(err) {
			return err
		}
		if cfg.Logger != nil {
			cfg.Logger.Debug("retry %d/%d after transient error: %v (waiting %s)", attempt, attempts-1, err, delay)
		}
		if delay > 0 {
			sleep(delay)
			delay *= 2
			if cfg.MaxDelay > 0 && delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
		}
	}
	return err
}

// RetryValue is Retry for a function that returns a value plus an error, so call
// sites don't need a captured-variable closure. The value from the last attempt
// (zero value on persistent failure) is returned alongside the error.
func RetryValue[T any](cfg RetryConfig, fn func() (T, error)) (T, error) {
	var result T
	err := Retry(cfg, func() error {
		v, e := fn()
		if e != nil {
			return e
		}
		result = v
		return nil
	})
	return result, err
}

// IsRetryableError reports whether err looks like a transient failure worth
// retrying — connection refused/reset, timeouts, EOF, 5xx, TLS handshake hiccups.
// Context cancellation/deadline is never retryable (the caller asked to stop).
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, frag := range []string{
		"connection refused",
		"connection reset",
		"connection closed",
		"broken pipe",
		"no route to host",
		"network is unreachable",
		"i/o timeout",
		"timeout",
		"temporarily unavailable",
		"try again",
		"unexpected eof",
		"tls handshake",
		"handshake timeout",
		"server misbehaving",
		"too many requests",
		" 502", " 503", " 504",
		"status 502", "status 503", "status 504",
	} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}
