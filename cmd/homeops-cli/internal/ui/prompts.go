package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// isGumAvailable checks if the gum binary is installed and available in PATH
func isGumAvailable() bool {
	_, err := exec.LookPath("gum")
	return err == nil
}

// isInteractiveDisabled checks if interactive mode is explicitly disabled via environment variable
func isInteractiveDisabled() bool {
	return os.Getenv("HOMEOPS_NO_INTERACTIVE") == "1"
}

// IsCancellation checks if an error is from user cancellation (Ctrl+C)
func IsCancellation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "cancelled by user") ||
		strings.Contains(err.Error(), "cancelled")
}

// StyleOptions defines styling options for text
type StyleOptions struct {
	Foreground string
	Background string
	Bold       bool
	Italic     bool
	Border     string
}

// Confirm presents a yes/no confirmation prompt using gum
// Returns true if the user confirms, false otherwise
// Gracefully falls back to basic fmt.Scanln if gum is unavailable
func Confirm(message string, defaultYes bool) (bool, error) {
	if !isGumAvailable() || isInteractiveDisabled() {
		return confirmBasic(message, defaultYes)
	}

	args := []string{"confirm", message}

	// Set default option
	if defaultYes {
		args = append(args, "--default")
	}

	cmd := exec.Command("gum", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		// Exit code 0 = yes, 1 = no, 130 = cancelled
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			if exitCode == 1 {
				return false, nil
			}
			if exitCode == 130 {
				return false, fmt.Errorf("cancelled by user")
			}
		}
		return false, err
	}
	return true, nil
}

// confirmBasic is a fallback confirmation prompt using basic input
func confirmBasic(message string, defaultYes bool) (bool, error) {
	prompt := message
	if defaultYes {
		prompt += " (Y/n): "
	} else {
		prompt += " (y/N): "
	}

	fmt.Print(prompt)
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil && err.Error() != "unexpected newline" {
		return false, err
	}

	response = strings.ToLower(strings.TrimSpace(response))

	// Empty response uses default
	if response == "" {
		return defaultYes, nil
	}

	return response == "y" || response == "yes", nil
}

// Choose presents a list of options for the user to select one
// Returns the selected option as a string
func Choose(prompt string, options []string) (string, error) {
	if !isGumAvailable() || isInteractiveDisabled() {
		return chooseBasic(prompt, options)
	}

	args := []string{"choose", "--header", prompt}
	args = append(args, options...)

	cmd := exec.Command("gum", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return "", fmt.Errorf("cancelled by user")
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

// chooseBasic is a fallback chooser using numbered list
func chooseBasic(prompt string, options []string) (string, error) {
	fmt.Println(prompt)
	for i, opt := range options {
		fmt.Printf("%d) %s\n", i+1, opt)
	}
	fmt.Print("Enter number: ")

	var choice int
	_, err := fmt.Scanln(&choice)
	if err != nil {
		return "", err
	}

	if choice < 1 || choice > len(options) {
		return "", fmt.Errorf("invalid choice")
	}

	return options[choice-1], nil
}

// ChooseMulti presents a list of options for the user to select multiple
// Returns the selected options as a string slice
func ChooseMulti(prompt string, options []string, limit int) ([]string, error) {
	if !isGumAvailable() || isInteractiveDisabled() {
		return chooseMultiBasic(prompt, options)
	}

	args := []string{"choose", "--header", prompt, "--no-limit"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	args = append(args, options...)

	cmd := exec.Command("gum", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return nil, fmt.Errorf("cancelled by user")
		}
		return nil, err
	}

	result := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(result) == 1 && result[0] == "" {
		return []string{}, nil
	}
	return result, nil
}

// chooseMultiBasic is a fallback multi-choice using comma-separated input
func chooseMultiBasic(prompt string, options []string) ([]string, error) {
	fmt.Println(prompt)
	for i, opt := range options {
		fmt.Printf("%d) %s\n", i+1, opt)
	}
	fmt.Print("Enter numbers (comma-separated): ")

	var input string
	_, err := fmt.Scanln(&input)
	if err != nil {
		return nil, err
	}

	choices := strings.Split(input, ",")
	result := []string{}
	for _, c := range choices {
		choice := strings.TrimSpace(c)
		var idx int
		_, err := fmt.Sscanf(choice, "%d", &idx)
		if err != nil || idx < 1 || idx > len(options) {
			continue
		}
		result = append(result, options[idx-1])
	}

	return result, nil
}

// Filter provides fuzzy filtering for a list of options
// Returns the selected option as a string
func Filter(prompt string, options []string) (string, error) {
	if !isGumAvailable() || isInteractiveDisabled() {
		return chooseBasic(prompt, options)
	}

	args := []string{"filter", "--placeholder", prompt}

	cmd := exec.Command("gum", args...)
	cmd.Stdin = strings.NewReader(strings.Join(options, "\n"))
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return "", fmt.Errorf("cancelled by user")
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

// Input prompts for a single-line text input
// Returns the entered text as a string
func Input(prompt, placeholder string) (string, error) {
	if !isGumAvailable() || isInteractiveDisabled() {
		return inputBasic(prompt)
	}

	args := []string{"input", "--prompt", prompt + " "}
	if placeholder != "" {
		args = append(args, "--placeholder", placeholder)
	}

	cmd := exec.Command("gum", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return "", fmt.Errorf("cancelled by user")
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

// inputBasic is a fallback input using basic fmt
func inputBasic(prompt string) (string, error) {
	fmt.Print(prompt + ": ")
	var input string
	_, err := fmt.Scanln(&input)
	return input, err
}

// Spin displays a spinner while executing a command
// The spinner automatically stops when the command completes
func Spin(title, command string, args ...string) error {
	if !isGumAvailable() || isInteractiveDisabled() {
		// Fallback: just run the command without spinner
		cmd := exec.Command(command, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	spinArgs := []string{"spin", "--spinner", "dot", "--title", title, "--"}
	spinArgs = append(spinArgs, command)
	spinArgs = append(spinArgs, args...)

	cmd := exec.Command("gum", spinArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// SpinWithOutput displays a spinner and captures command output
// Returns the output and any error
func SpinWithOutput(title, command string, args ...string) (string, error) {
	if !isGumAvailable() || isInteractiveDisabled() {
		// Fallback: just run the command without spinner
		cmd := exec.Command(command, args...)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	spinArgs := []string{"spin", "--spinner", "dot", "--title", title, "--"}
	spinArgs = append(spinArgs, command)
	spinArgs = append(spinArgs, args...)

	cmd := exec.Command("gum", spinArgs...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// SpinWithFunc runs a Go function with a spinner
func SpinWithFunc(title string, fn func() error) error {
	if !isGumAvailable() || isInteractiveDisabled() {
		// Fallback: just run the function without spinner
		return fn()
	}

	// Create a wrapper script that runs indefinitely until killed
	// We'll run the actual function in parallel and kill the spinner when done
	tmpScript := `#!/bin/sh
while true; do
    sleep 1
done
`

	// Write temporary script
	tmpFile, err := os.CreateTemp("", "spinner-*.sh")
	if err != nil {
		// Fallback if we can't create temp file
		return fn()
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.WriteString(tmpScript); err != nil {
		_ = tmpFile.Close()
		return fn()
	}
	_ = tmpFile.Close()

	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		return fn()
	}

	// Start spinner in background - output directly to TTY to avoid mixing with stdout
	spinCmd := exec.Command("gum", "spin", "--spinner", "dot", "--title", title, "--", tmpFile.Name())

	// Open /dev/tty for direct terminal I/O to prevent escape codes in stdout
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		spinCmd.Stdin = tty // Prevent terminal queries from going to stdout
		spinCmd.Stdout = tty
		spinCmd.Stderr = tty
		defer func() { _ = tty.Close() }()
	}

	if err := spinCmd.Start(); err != nil {
		// If spinner fails to start, just run the function
		return fn()
	}

	// Run the actual function
	fnErr := fn()

	// Terminate the spinner gracefully and wait for cleanup
	if spinCmd.Process != nil {
		// First try SIGTERM for graceful shutdown
		_ = spinCmd.Process.Signal(os.Interrupt)

		// Give it a moment to clean up terminal state
		time.Sleep(100 * time.Millisecond)

		// Force kill if still running
		_ = spinCmd.Process.Kill()
		_ = spinCmd.Wait() // Clean up zombie process

		// Use stty to fully reset terminal state - this is more reliable than ANSI codes
		// Run stty sane to restore terminal to sane state
		resetCmd := exec.Command("stty", "sane")
		if tty != nil {
			resetCmd.Stdin = tty
			resetCmd.Stdout = tty
			resetCmd.Stderr = tty
		}
		_ = resetCmd.Run()

		// Also send ANSI reset as backup
		if tty != nil {
			// Send comprehensive ANSI reset sequences directly to TTY
			// \033[0m - Reset all attributes
			// \033[?25h - Show cursor
			// \r - Carriage return to start of line
			_, _ = tty.WriteString("\033[0m\033[?25h\r")
			_ = tty.Sync()
		}

		// Small delay to ensure terminal state is fully restored
		time.Sleep(50 * time.Millisecond)
	}

	return fnErr
}

// Style applies styling to text using gum
func Style(text string, opts StyleOptions) string {
	if !isGumAvailable() || isInteractiveDisabled() {
		return text
	}

	args := []string{"style"}

	if opts.Foreground != "" {
		args = append(args, "--foreground", opts.Foreground)
	}
	if opts.Background != "" {
		args = append(args, "--background", opts.Background)
	}
	if opts.Bold {
		args = append(args, "--bold")
	}
	if opts.Italic {
		args = append(args, "--italic")
	}
	if opts.Border != "" {
		args = append(args, "--border", opts.Border)
	}

	args = append(args, text)

	cmd := exec.Command("gum", args...)
	output, err := cmd.Output()
	if err != nil {
		return text // Fallback to unstyled text
	}

	return string(output)
}

// InstallInstructions returns installation instructions for gum
func InstallInstructions() string {
	return `
Gum is not installed. To enable interactive features, install gum:

macOS:
  brew install gum

Linux (go install):
  go install github.com/charmbracelet/gum@latest

For other platforms, visit: https://github.com/charmbracelet/gum

To disable this message, set: export HOMEOPS_NO_INTERACTIVE=1
`
}

// CheckGumInstallation checks if gum is installed and returns a helpful message if not
// Returns nil if gum is available or interactive mode is disabled
func CheckGumInstallation() error {
	if isInteractiveDisabled() {
		return nil
	}

	if !isGumAvailable() {
		fmt.Println(InstallInstructions())
		return nil // Not an error, just informational
	}

	return nil
}
