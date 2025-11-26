package common

import (
	"context"
	"os/exec"
)

// CommandWithContext creates an exec.Cmd with context support for cancellation.
// This allows commands to be gracefully terminated when the context is cancelled.
func CommandWithContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd
}

// RunCommandWithContext executes a command with context support.
// Returns the combined stdout/stderr output and any error.
func RunCommandWithContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := CommandWithContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// RunCommandWithContextOutput executes a command with context support.
// Returns only stdout output and any error.
func RunCommandWithContextOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := CommandWithContext(ctx, name, args...)
	return cmd.Output()
}

