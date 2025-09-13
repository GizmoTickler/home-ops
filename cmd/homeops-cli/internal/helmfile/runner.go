package helmfile

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"go.uber.org/zap"
)

// Runner is a wrapper for running helmfile commands
type Runner struct {
	logger *zap.SugaredLogger
}

// RunOptions holds options for running a helmfile command
type RunOptions struct {
	Dir string
	Env []string
}

// NewRunner creates a new helmfile runner
func NewRunner(log *zap.SugaredLogger) *Runner {
	return &Runner{
		logger: log,
	}
}

// Run runs a helmfile command and returns its output
func (r *Runner) Run(args ...string) (string, error) {
	return r.RunWithOptions(RunOptions{}, args...)
}

// RunWithOptions runs a helmfile command with options and returns its output
func (r *Runner) RunWithOptions(opts RunOptions, args ...string) (string, error) {
	r.logger.Debugf("Running helmfile command: %v", args)

	cmd := exec.Command("helmfile", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = opts.Dir
	cmd.Env = append(os.Environ(), opts.Env...)

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("helmfile command failed: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	return stdout.String(), nil
}
