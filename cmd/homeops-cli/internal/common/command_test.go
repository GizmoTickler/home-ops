package common

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCommandCapturesStdoutAndStderrSeparately(t *testing.T) {
	result, err := RunCommand(context.Background(), CommandOptions{
		Name: "sh",
		Args: []string{"-c", "printf 'out'; printf 'err' >&2"},
	})

	require.NoError(t, err)
	assert.Equal(t, "out", result.Stdout)
	assert.Equal(t, "err", result.Stderr)
	assert.Equal(t, 0, result.ExitCode)
	assert.False(t, result.TimedOut)
}

func TestRunCommandReturnsExitErrorsWithCapturedOutput(t *testing.T) {
	result, err := RunCommand(context.Background(), CommandOptions{
		Name: "sh",
		Args: []string{"-c", "printf 'partial-out'; printf 'partial-err' >&2; exit 7"},
	})

	require.Error(t, err)
	assert.Equal(t, "partial-out", result.Stdout)
	assert.Equal(t, "partial-err", result.Stderr)
	assert.Equal(t, 7, result.ExitCode)
	assert.False(t, result.TimedOut)
}

func TestRunCommandCancelsAfterTimeout(t *testing.T) {
	result, err := RunCommand(context.Background(), CommandOptions{
		Name:    "sh",
		Args:    []string{"-c", "sleep 2"},
		Timeout: 20 * time.Millisecond,
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.True(t, result.TimedOut)
	assert.NotEqual(t, 0, result.ExitCode)
}

func TestRunCommandRedactsSensitiveOutput(t *testing.T) {
	result, err := RunCommand(context.Background(), CommandOptions{
		Name: "sh",
		Args: []string{"-c", "printf 'password=SENTINEL_PASSWORD_VALUE\\n'; printf 'token: SENTINEL_TOKEN_VALUE\\n' >&2"},
	})

	require.NoError(t, err)
	assert.Equal(t, "password=<redacted>\n", result.Stdout)
	assert.Equal(t, "token: <redacted>\n", result.Stderr)
	assert.NotContains(t, result.Stdout, "SENTINEL_PASSWORD_VALUE")
	assert.NotContains(t, result.Stderr, "SENTINEL_TOKEN_VALUE")
}

func TestRunCommandUsesCustomRedactor(t *testing.T) {
	result, err := RunCommand(context.Background(), CommandOptions{
		Name:     "sh",
		Args:     []string{"-c", "printf 'visible-output'"},
		Redactor: func(string) string { return "custom-redacted" },
	})

	require.NoError(t, err)
	assert.Equal(t, "custom-redacted", result.Stdout)
}
