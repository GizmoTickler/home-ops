package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/testutil"
)

// Tests run without a TTY, so every prompt routes to its basic fallback and
// SpinWithFunc runs the work directly — exactly the CI behavior we promise.

func TestIsCancellationRecognisesHuhAbort(t *testing.T) {
	assert.True(t, IsCancellation(huh.ErrUserAborted))
	assert.True(t, IsCancellation(fmt.Errorf("wrap: %w", huh.ErrUserAborted)))
	assert.True(t, IsCancellation(errors.New("cancelled by user")))
	assert.False(t, IsCancellation(nil))
	assert.False(t, IsCancellation(errors.New("boom")))
}

func TestSpinWithFuncNonTTYRunsDirectly(t *testing.T) {
	ran := false
	require.NoError(t, SpinWithFunc("working", func() error { ran = true; return nil }))
	assert.True(t, ran)

	err := SpinWithFunc("working", func() error { return errors.New("boom") })
	require.Error(t, err)
	assert.Equal(t, "boom", err.Error())
}

func TestRunWithSpinnerRestoresQuiet(t *testing.T) {
	logger := &stubLogger{}
	err := RunWithSpinner("working", false, logger, func() error {
		assert.True(t, logger.quiet)
		return nil
	})
	require.NoError(t, err)
	assert.False(t, logger.quiet)

	err = RunWithSpinner("working", false, logger, func() error {
		return errors.New("boom")
	})
	require.Error(t, err)
	assert.False(t, logger.quiet)
}

func TestSpinWithOutputDoesNotPolluteReturnedOutput(t *testing.T) {
	stdout, _, err := testutil.CaptureOutput(func() {
		output, spinErr := SpinWithOutput("spin", "sh", "-c", "printf clean-output")
		require.NoError(t, spinErr)
		fmt.Print(output)
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "clean-output")
}

func TestStyleOffTerminalIsIdentity(t *testing.T) {
	out := Style("plain", StyleOptions{Foreground: "212", Bold: true, Border: "rounded"})
	assert.Equal(t, "plain", out)
}

func TestSpinnerModelView(t *testing.T) {
	m := newSpinnerModel("doing things")
	view := m.View()
	assert.Contains(t, view, "doing things")
	assert.True(t, strings.Contains(view, "(0s)") || strings.Contains(view, "(1s)"))

	// done message quits
	_, cmd := m.Update(spinnerDoneMsg{})
	require.NotNil(t, cmd)
}

func TestSuccessBoxOffTerminal(t *testing.T) {
	// Tests run without a TTY: the flourish must vanish so CI logs stay plain.
	assert.Empty(t, SuccessBox("done", "line"))
}
