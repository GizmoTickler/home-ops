package vm

import (
	"fmt"
	"strconv"
	"strings"

	"homeops-cli/internal/ui"
	"homeops-cli/internal/vmlifecycle"
)

// resolveVMNameForAction returns the VM name to operate on. When --name was
// supplied it is returned unchanged. Otherwise, on an interactive terminal it
// opens the provider's live VM picker — the same UX the start/stop/ssh verbs
// already use — returning ("", nil) when the user cancels. In non-interactive
// mode (CI, pipes, HOMEOPS_NO_INTERACTIVE=1) it returns the familiar
// "--name is required" error so scripts fail fast instead of blocking on a
// picker that cannot be answered. This is what lets the day-2 verbs be driven
// from the interactive menu, where no flags can be passed.
func resolveVMNameForAction(name, provider, action string) (string, error) {
	if strings.TrimSpace(name) != "" {
		return name, nil
	}
	if !ui.IsInteractive() {
		return "", fmt.Errorf("--name is required")
	}
	return vmlifecycle.ChooseVMNameForProvider("", provider, action)
}

// promptStringIfInteractive returns value when it is already set. When empty
// and on an interactive terminal it prompts for the value; otherwise it returns
// the empty string and leaves the caller to enforce any required-flag error
// (preserving non-interactive/CI behavior).
func promptStringIfInteractive(value, prompt, placeholder string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return value, nil
	}
	if !ui.IsInteractive() {
		return "", nil
	}
	out, err := ui.Input(prompt, placeholder)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// promptIntIfInteractive prompts for an integer when current is 0 and the
// session is interactive. A blank answer keeps 0 ("unchanged"). Non-interactive
// sessions return current unchanged so flags remain the only input path.
func promptIntIfInteractive(current int, prompt, placeholder string) (int, error) {
	if current != 0 || !ui.IsInteractive() {
		return current, nil
	}
	raw, err := ui.Input(prompt, placeholder)
	if err != nil {
		return 0, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", raw, err)
	}
	return n, nil
}
