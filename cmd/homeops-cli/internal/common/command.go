package common

import (
	"io"
	"os/exec"
)

var (
	commandFactory = exec.Command
	lookPathFunc   = exec.LookPath
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
