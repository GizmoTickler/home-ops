package common

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindGitRoot walks up the directory tree to find the git repository root
func FindGitRoot(startDir string) (string, error) {
	currentDir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	for {
		// Check if .git directory exists
		gitDir := filepath.Join(currentDir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return currentDir, nil
		}

		// Move up one directory
		parentDir := filepath.Dir(currentDir)

		// If we've reached the root directory, stop
		if parentDir == currentDir {
			return "", fmt.Errorf("git repository root not found")
		}

		currentDir = parentDir
	}
}

// GetWorkingDirectory returns the git repository root if possible, otherwise the current directory
func GetWorkingDirectory() string {
	gitRoot, err := FindGitRoot(".")
	if err != nil {
		// Fallback to current directory if git root not found
		cwd, err := os.Getwd()
		if err != nil {
			return "." // Ultimate fallback
		}
		return cwd
	}
	return gitRoot
}
