package flux

import (
	"bytes"
	"fmt"
	"os/exec"

	"go.uber.org/zap"
)

// Runner is a wrapper for running flux commands
type Runner struct {
	logger *zap.SugaredLogger
}

// NewRunner creates a new flux runner
func NewRunner(log *zap.SugaredLogger) *Runner {
	return &Runner{
		logger: log,
	}
}

// Run runs a flux command and returns its output
func (r *Runner) Run(args ...string) (string, error) {
	r.logger.Debugf("Running flux command: %v", args)

	cmd := exec.Command("flux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("flux command failed: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return stdout.String(), nil
}
