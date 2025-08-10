package common

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/fatih/color"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

// ColorLogger provides colored console output
type ColorLogger struct {
	Level LogLevel
}

// NewColorLogger creates a new colored logger
func NewColorLogger() *ColorLogger {
	level := InfoLevel
	if os.Getenv("DEBUG") == "1" || os.Getenv("LOG_LEVEL") == "debug" {
		level = DebugLevel
	}
	return &ColorLogger{Level: level}
}

// Debug logs debug messages
func (l *ColorLogger) Debug(msg string, args ...interface{}) {
	if l.Level <= DebugLevel {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		color.Blue("%s DEBUG %s", timestamp, fmt.Sprintf(msg, args...))
	}
}

// Info logs info messages
func (l *ColorLogger) Info(msg string, args ...interface{}) {
	if l.Level <= InfoLevel {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		color.Cyan("%s INFO %s", timestamp, fmt.Sprintf(msg, args...))
	}
}

// Warn logs warning messages
func (l *ColorLogger) Warn(msg string, args ...interface{}) {
	if l.Level <= WarnLevel {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		color.Yellow("%s WARN %s", timestamp, fmt.Sprintf(msg, args...))
	}
}

// Error logs error messages
func (l *ColorLogger) Error(msg string, args ...interface{}) {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	color.Red("%s ERROR %s", timestamp, fmt.Sprintf(msg, args...))
}

// Success logs success messages (always shown)
func (l *ColorLogger) Success(msg string, args ...interface{}) {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	color.Green("%s SUCCESS %s", timestamp, fmt.Sprintf(msg, args...))
}

// CheckEnv verifies that required environment variables are set
func CheckEnv(vars ...string) error {
	var missing []string
	for _, v := range vars {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %v", missing)
	}
	return nil
}

// CheckCLI verifies that required CLI tools are available
func CheckCLI(tools ...string) error {
	var missing []string
	for _, tool := range tools {
		if _, err := os.Stat(tool); err != nil {
			if _, err := exec.LookPath(tool); err != nil {
				missing = append(missing, tool)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required CLI tools: %v", missing)
	}
	return nil
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// DirExists checks if a directory exists
func DirExists(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return info.IsDir()
}
