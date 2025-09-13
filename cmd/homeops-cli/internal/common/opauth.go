package common

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// Ensure1PasswordAuth verifies 1Password CLI authentication and performs an interactive
// signin if necessary. It returns an error if authentication cannot be confirmed.
func Ensure1PasswordAuth() error {
	// Check if already authenticated
	cmd := exec.Command("op", "whoami", "--format=json")
	output, err := cmd.Output()
	if err == nil {
		// Validate JSON response
		var result map[string]interface{}
		if jsonErr := json.Unmarshal(output, &result); jsonErr == nil {
			return nil // Already authenticated
		}
	}

	// Not authenticated, attempt interactive signin
	signin := exec.Command("op", "signin")
	signin.Stdin = os.Stdin
	signin.Stdout = os.Stdout
	signin.Stderr = os.Stderr
	if err := signin.Run(); err != nil {
		return fmt.Errorf("failed to sign in to 1Password: %w", err)
	}

	// Verify authentication after signin
	verify := exec.Command("op", "whoami", "--format=json")
	verifyOutput, err := verify.Output()
	if err != nil {
		return fmt.Errorf("authentication verification failed: %w", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(verifyOutput, &result); err != nil {
		return fmt.Errorf("invalid 1Password response after signin: %w", err)
	}

	return nil
}
