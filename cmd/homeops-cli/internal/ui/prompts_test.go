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
	t.Setenv("PATH", t.TempDir())
	t.Setenv(constants.EnvHomeOpsNoInteract, "1")

	assert.False(t, isGumAvailable())
	assert.True(t, isInteractiveDisabled())
	assert.True(t, IsCancellation(errors.New("cancelled by user")))
	assert.False(t, IsCancellation(nil))
	assert.Equal(t, "plain", Style("plain", StyleOptions{Bold: true}))
	assert.Contains(t, InstallInstructions(), "brew install gum")
	require.NoError(t, CheckGumInstallation())
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
