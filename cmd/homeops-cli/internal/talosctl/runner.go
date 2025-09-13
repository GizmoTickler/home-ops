package talosctl

import (
	"bytes"
	"fmt"
	"os/exec"

	"go.uber.org/zap"
)

// Runner is a wrapper for running talosctl commands
type Runner struct {
	logger *zap.SugaredLogger
}

// NewRunner creates a new talosctl runner
func NewRunner(log *zap.SugaredLogger) *Runner {
	return &Runner{
		logger: log,
	}
}

// Run runs a talosctl command and returns its output
func (r *Runner) Run(args ...string) (string, error) {
	r.logger.Debugf("Running talosctl command: %v", args)

	cmd := exec.Command("talosctl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("talosctl command failed: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return stdout.String(), nil
}

// RunWithStdin runs a talosctl command with stdin and returns its output
func (r *Runner) RunWithStdin(stdin *bytes.Buffer, args ...string) (string, error) {
	r.logger.Debugf("Running talosctl command with stdin: %v", args)

	cmd := exec.Command("talosctl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = stdin

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("talosctl command failed: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return stdout.String(), nil
}
