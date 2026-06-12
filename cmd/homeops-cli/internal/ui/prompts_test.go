package ui

import (
	"errors"
	"testing"

	"homeops-cli/internal/constants"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubLogger struct {
	quiet bool
	infos []string
}

func (l *stubLogger) SetQuiet(quiet bool) {
	l.quiet = quiet
}

func (l *stubLogger) Info(msg string, args ...interface{}) {
	l.infos = append(l.infos, msg)
}

func TestUIHelpers(t *testing.T) {
	t.Setenv(constants.EnvHomeOpsNoInteract, "1")

	assert.True(t, isInteractiveDisabled())
	assert.False(t, isInteractive())
	assert.True(t, IsCancellation(errors.New("cancelled by user")))
	assert.False(t, IsCancellation(nil))
	assert.Equal(t, "plain", Style("plain", StyleOptions{Bold: true}))
}

func TestRunWithSpinner(t *testing.T) {
	t.Setenv(constants.EnvHomeOpsNoInteract, "1")

	logger := &stubLogger{}
	called := false
	require.NoError(t, RunWithSpinner("working", false, logger, func() error {
		called = true
		return nil
	}))
	assert.True(t, called)
	assert.False(t, logger.quiet)

	logger = &stubLogger{}
	called = false
	require.NoError(t, RunWithSpinner("verbose", true, logger, func() error {
		called = true
		return nil
	}))
	assert.True(t, called)
	assert.Len(t, logger.infos, 1)
}

func TestSetAssumeYesAutoConfirms(t *testing.T) {
	SetAssumeYes(true)
	t.Cleanup(func() { SetAssumeYes(false) })

	ok, err := Confirm("dangerous?", false)
	if err != nil || !ok {
		t.Fatalf("assume-yes must confirm without input: ok=%v err=%v", ok, err)
	}
}

func TestStyleOffTerminalIsPlain(t *testing.T) {
	// Tests run without a TTY: Style must return the text unstyled so piped
	// output stays clean.
	got := Style("hello", StyleOptions{Foreground: "99", Bold: true})
	if got != "hello" {
		t.Fatalf("expected plain text off-terminal, got %q", got)
	}
}

func TestBannerOffTerminalIsEmpty(t *testing.T) {
	if Banner("tagline") != "" {
		t.Fatal("banner must vanish off-terminal")
	}
	PrintBanner("tagline") // must not panic or print escape codes
}

func TestBoxPrintersOffTerminal(t *testing.T) {
	// All box printers are TTY-gated no-ops here; they must not panic.
	PrintSuccessBox("done", "line")
	PrintInfoBox("plan", "line")
}
