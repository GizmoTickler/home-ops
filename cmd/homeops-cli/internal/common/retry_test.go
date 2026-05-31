package common

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	var slept []time.Duration
	err := Retry(RetryConfig{
		Attempts:  4,
		BaseDelay: time.Second,
		Sleep:     func(d time.Duration) { slept = append(slept, d) },
	}, func() error {
		calls++
		if calls < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
	// Two backoffs before the 3rd (successful) attempt, doubling.
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("unexpected backoff schedule: %v", slept)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	err := Retry(RetryConfig{
		Attempts:  3,
		BaseDelay: time.Millisecond,
		Sleep:     func(time.Duration) {},
	}, func() error {
		calls++
		return errors.New("i/o timeout")
	})
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryStopsOnNonRetryableError(t *testing.T) {
	calls := 0
	sentinel := errors.New("validation failed: bad name")
	err := Retry(RetryConfig{
		Attempts:  5,
		BaseDelay: time.Millisecond,
		Sleep:     func(time.Duration) { t.Fatal("must not sleep on non-retryable error") },
	}, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", calls)
	}
}

func TestRetryRespectsMaxDelay(t *testing.T) {
	var slept []time.Duration
	_ = Retry(RetryConfig{
		Attempts:  5,
		BaseDelay: time.Second,
		MaxDelay:  3 * time.Second,
		Sleep:     func(d time.Duration) { slept = append(slept, d) },
	}, func() error { return errors.New("timeout") })
	// 1s, 2s, then capped at 3s, 3s (4 backoffs before the 5th attempt).
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second}
	if fmt.Sprint(slept) != fmt.Sprint(want) {
		t.Fatalf("backoff = %v, want %v", slept, want)
	}
}

func TestRetryValuePropagatesResult(t *testing.T) {
	calls := 0
	got, err := RetryValue(RetryConfig{Attempts: 3, Sleep: func(time.Duration) {}}, func() (int, error) {
		calls++
		if calls < 2 {
			return 0, errors.New("connection reset")
		}
		return 42, nil
	})
	if err != nil || got != 42 {
		t.Fatalf("expected 42, nil; got %d, %v", got, err)
	}
}

func TestIsRetryableError(t *testing.T) {
	retryable := []error{
		errors.New("dial tcp: connection refused"),
		errors.New("read: connection reset by peer"),
		errors.New("net/http: TLS handshake timeout"),
		errors.New("i/o timeout"),
		fmt.Errorf("api returned status 503"),
		errors.New("EOF: unexpected EOF"),
	}
	for _, e := range retryable {
		if !IsRetryableError(e) {
			t.Errorf("expected retryable: %v", e)
		}
	}

	nonRetryable := []error{
		nil,
		errors.New("invalid configuration"),
		errors.New("VM with name 'x' already exists"),
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", context.Canceled),
	}
	for _, e := range nonRetryable {
		if IsRetryableError(e) {
			t.Errorf("expected non-retryable: %v", e)
		}
	}
}

// netTimeoutErr implements net.Error with Timeout()==true to exercise the net.Error path.
type netTimeoutErr struct{}

func (netTimeoutErr) Error() string   { return "operation slow" }
func (netTimeoutErr) Timeout() bool   { return true }
func (netTimeoutErr) Temporary() bool { return true }

func TestIsRetryableErrorNetTimeout(t *testing.T) {
	if !IsRetryableError(netTimeoutErr{}) {
		t.Fatal("expected net.Error Timeout() to be retryable")
	}
}
