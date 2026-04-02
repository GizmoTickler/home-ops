package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"homeops-cli/internal/constants"
	"homeops-cli/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGumBackedPromptHelpers(t *testing.T) {
	scriptDir := t.TempDir()
	gumPath := filepath.Join(scriptDir, "gum")
	require.NoError(t, os.WriteFile(gumPath, []byte(`#!/bin/sh
set -eu
cmd="$1"
shift || true
case "$cmd" in
  confirm)
    exit "${GUM_CONFIRM_EXIT:-0}"
    ;;
  choose)
    has_no_limit=0
    has_limit=0
    for arg in "$@"; do
      if [ "$arg" = "--no-limit" ]; then
        has_no_limit=1
      fi
      if [ "$arg" = "--limit" ]; then
        has_limit=1
      fi
    done
    if [ "$has_no_limit" = "1" ] && [ "$has_limit" = "1" ]; then
      echo 'conflicting limit flags' >&2
      exit 99
    fi
    if [ "$has_limit" = "1" ]; then
      printf 'one\n'
      exit 0
    fi
    if [ "$has_no_limit" = "1" ]; then
      printf 'one\ntwo\n'
      exit 0
    fi
    printf 'picked\n'
    ;;
  filter)
    printf 'filtered\n'
    ;;
  input)
    printf 'typed\n'
    ;;
  style)
    if [ "${GUM_FAIL_STYLE:-0}" = "1" ]; then
      exit 1
    fi
    printf 'styled\n'
    ;;
  spin)
    while [ "$1" != "--" ]; do
      shift
    done
    shift
    exec "$@"
    ;;
  *)
    echo "unexpected command" >&2
    exit 1
    ;;
esac
`), 0o755))

	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(constants.EnvHomeOpsNoInteract, "")

	ok, err := Confirm("continue", false)
	require.NoError(t, err)
	assert.True(t, ok)

	t.Setenv("GUM_CONFIRM_EXIT", "1")
	ok, err = Confirm("continue", false)
	require.NoError(t, err)
	assert.False(t, ok)

	t.Setenv("GUM_CONFIRM_EXIT", "130")
	ok, err = Confirm("continue", false)
	require.Error(t, err)
	assert.False(t, ok)
	assert.True(t, IsCancellation(err))

	t.Setenv("GUM_CONFIRM_EXIT", "0")
	choice, err := Choose("pick", []string{"one", "two"})
	require.NoError(t, err)
	assert.Equal(t, "picked", choice)

	choices, err := ChooseMulti("pick", []string{"one", "two"}, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, choices)

	choices, err = ChooseMulti("pick", []string{"one", "two"}, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"one"}, choices)

	filtered, err := Filter("filter", []string{"one", "two"})
	require.NoError(t, err)
	assert.Equal(t, "filtered", filtered)

	value, err := Input("prompt", "placeholder")
	require.NoError(t, err)
	assert.Equal(t, "typed", value)

	assert.Equal(t, "styled\n", Style("plain", StyleOptions{Bold: true, Foreground: "blue"}))

	t.Setenv("GUM_FAIL_STYLE", "1")
	assert.Equal(t, "plain", Style("plain", StyleOptions{Italic: true}))
}

func TestGumSpinnerHelpers(t *testing.T) {
	scriptDir := t.TempDir()
	gumPath := filepath.Join(scriptDir, "gum")
	require.NoError(t, os.WriteFile(gumPath, []byte(`#!/bin/sh
set -eu
if [ "$1" != "spin" ]; then
  exit 1
fi
trap 'if [ -n "${GUM_SPIN_LOG:-}" ]; then echo stopped >> "$GUM_SPIN_LOG"; fi; exit 0' INT TERM
if [ -n "${GUM_SPIN_LOG:-}" ]; then echo started >> "$GUM_SPIN_LOG"; fi
if [ -n "${GUM_SPIN_NOISE:-}" ]; then echo "$GUM_SPIN_NOISE" >&2; fi
while true; do sleep 1; done
`), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(constants.EnvHomeOpsNoInteract, "")

	require.NoError(t, Spin("spin", "sh", "-c", "exit 0"))

	t.Setenv("GUM_SPIN_NOISE", "spinner-noise")
	output, err := SpinWithOutput("spin", "sh", "-c", "printf spinner-output")
	require.NoError(t, err)
	assert.Equal(t, "spinner-output", output)

	called := false
	require.NoError(t, SpinWithFunc("spin", func() error {
		called = true
		return nil
	}))
	assert.True(t, called)
}

func TestSpinWithFuncCleansUpAfterPanic(t *testing.T) {
	scriptDir := t.TempDir()
	gumPath := filepath.Join(scriptDir, "gum")
	require.NoError(t, os.WriteFile(gumPath, []byte(`#!/bin/sh
set -eu
if [ "$1" != "spin" ]; then
  exit 1
fi
trap 'exit 0' INT TERM
while true; do sleep 1; done
`), 0o755))

	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(constants.EnvHomeOpsNoInteract, "")

	start := time.Now()
	require.Panics(t, func() {
		_ = SpinWithFunc("spin", func() error {
			panic("boom")
		})
	})
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestStopGumSpinnerStopsBackgroundProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap 'exit 0' INT TERM; while true; do sleep 1; done")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid

	stopGumSpinner(cmd, nil, "")

	err := syscall.Kill(pid, 0)
	require.Error(t, err)
}

func TestCheckGumInstallationMessage(t *testing.T) {
	t.Setenv(constants.EnvHomeOpsNoInteract, "")
	t.Setenv("PATH", t.TempDir())

	stdout, _, err := testutil.CaptureOutput(func() {
		require.NoError(t, CheckGumInstallation())
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "Gum is not installed")
	assert.Contains(t, stdout, "brew install gum")
}

func TestRunWithSpinnerGumPathRestoresQuiet(t *testing.T) {
	scriptDir := t.TempDir()
	gumPath := filepath.Join(scriptDir, "gum")
	require.NoError(t, os.WriteFile(gumPath, []byte(`#!/bin/sh
set -eu
if [ "$1" != "spin" ]; then
  exit 1
fi
trap 'exit 0' INT TERM
while true; do sleep 1; done
`), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(constants.EnvHomeOpsNoInteract, "")

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
	scriptDir := t.TempDir()
	gumPath := filepath.Join(scriptDir, "gum")
	require.NoError(t, os.WriteFile(gumPath, []byte(`#!/bin/sh
set -eu
if [ "$1" != "spin" ]; then
  exit 1
fi
printf 'spinner-noise\n' >&2
trap 'exit 0' INT TERM
while true; do sleep 1; done
`), 0o755))
	t.Setenv("PATH", scriptDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(constants.EnvHomeOpsNoInteract, "")

	stdout, stderr, err := testutil.CaptureOutput(func() {
		output, spinErr := SpinWithOutput("spin", "sh", "-c", "printf clean-output")
		require.NoError(t, spinErr)
		fmt.Print(output)
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "clean-output")
	assert.NotContains(t, stdout, "spinner-noise")
	assert.True(t, stderr == "" || strings.Contains(stderr, "spinner-noise"))
}
