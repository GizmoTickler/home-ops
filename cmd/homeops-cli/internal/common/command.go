package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"time"
)

var (
	commandFactory = exec.Command
	lookPathFunc   = exec.LookPath
)

// Redactor rewrites command output before it is returned to callers.
type Redactor func(string) string

// CommandOptions configures timeout-aware command execution.
type CommandOptions struct {
	Name     string
	Args     []string
	Timeout  time.Duration
	Redactor Redactor
	// Stdin, when set, is wired to the process's standard input (e.g. to
	// stream file content through ssh without touching argv).
	Stdin io.Reader
}

// CommandResult contains redacted command output streams and process metadata.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

var (
	privateKeyBlockPattern = regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	secretLabelPattern     = regexp.MustCompile(`(?i)\b((?:access|refresh|id)[_-]?token|client[_-]?secret|api[_-]?key|private[_ -]?key|password|passwd|token|secret)(\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s\r\n]+)`)
	// kubeadm join material printed in init/join output (and any error that echoes it).
	kubeadmFlagSecretPattern = regexp.MustCompile(`(--(?:token|certificate-key|discovery-token-ca-cert-hash))([ =])(\S+)`)
	kubeadmTokenPattern      = regexp.MustCompile(`\b[a-z0-9]{6}\.[a-z0-9]{16}\b`)           // bootstrap token
	caCertHashPattern        = regexp.MustCompile(`sha256:[0-9a-f]{64}`)                     // discovery CA hash
	certificateKeyPattern    = regexp.MustCompile(`(?i)(certificate key:\s*)([0-9a-f]{64})`) // upload-certs key line
)

// Command creates a command using the shared command factory.
func Command(name string, args ...string) *exec.Cmd {
	return commandFactory(name, args...)
}

// LookPath resolves an executable using the shared lookup function.
func LookPath(file string) (string, error) {
	return lookPathFunc(file)
}

// Output runs a command and returns stdout.
func Output(name string, args ...string) ([]byte, error) {
	return Command(name, args...).Output()
}

// CombinedOutput runs a command and returns combined stdout/stderr.
func CombinedOutput(name string, args ...string) ([]byte, error) {
	return Command(name, args...).CombinedOutput()
}

// RunCommand runs an external command with optional timeout and redacted output capture.
func RunCommand(ctx context.Context, opts CommandOptions) (CommandResult, error) {
	if opts.Name == "" {
		return CommandResult{ExitCode: -1}, fmt.Errorf("command name is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, opts.Name, opts.Args...)
	// After the context kills the process, force-close its I/O pipes so Wait
	// can't be held hostage by orphaned grandchildren that inherited them
	// (e.g. a shell's `sleep` child surviving the shell's SIGKILL).
	cmd.WaitDelay = 3 * time.Second
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{
		Stdout:   redactCommandOutput(stdout.String(), opts.Redactor),
		Stderr:   redactCommandOutput(stderr.String(), opts.Redactor),
		ExitCode: 0,
		TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
	}

	if err != nil {
		result.ExitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		if runCtx.Err() != nil {
			return result, runCtx.Err()
		}
		return result, err
	}

	return result, nil
}

// RunInteractive runs a command wired to the provided stdio streams.
func RunInteractive(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := Command(name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// SetCommandFactoryForTesting temporarily overrides command creation.
func SetCommandFactoryForTesting(factory func(string, ...string) *exec.Cmd) func() {
	old := commandFactory
	commandFactory = factory
	return func() {
		commandFactory = old
	}
}

// SetLookPathFuncForTesting temporarily overrides executable lookup.
func SetLookPathFuncForTesting(fn func(string) (string, error)) func() {
	old := lookPathFunc
	lookPathFunc = fn
	return func() {
		lookPathFunc = old
	}
}

func redactCommandOutput(output string, redactor Redactor) string {
	if redactor != nil {
		return redactor(output)
	}
	return RedactCommandOutput(output)
}

// RedactCommandOutput conservatively masks obvious secret-labeled values.
func RedactCommandOutput(output string) string {
	output = privateKeyBlockPattern.ReplaceAllString(output, "<redacted private key>")
	output = secretLabelPattern.ReplaceAllString(output, "${1}${2}<redacted>")
	// kubeadm join material (token / cert-key / CA hash) — space- or =-separated
	// flags and the standalone forms kubeadm prints in init/join output.
	output = kubeadmFlagSecretPattern.ReplaceAllString(output, "${1}${2}<redacted>")
	output = caCertHashPattern.ReplaceAllString(output, "sha256:<redacted>")
	output = certificateKeyPattern.ReplaceAllString(output, "${1}<redacted>")
	output = kubeadmTokenPattern.ReplaceAllString(output, "<redacted-token>")
	return output
}
