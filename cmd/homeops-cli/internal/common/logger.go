package common

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/fatih/color"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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
	Quiet bool // When true, suppress all output
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
	if l.Quiet {
		return
	}
	if l.Level <= DebugLevel {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		color.Blue("%s DEBUG %s", timestamp, fmt.Sprintf(msg, args...))
	}
}

// Info logs info messages
func (l *ColorLogger) Info(msg string, args ...interface{}) {
	if l.Quiet {
		return
	}
	if l.Level <= InfoLevel {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		color.Cyan("%s INFO %s", timestamp, fmt.Sprintf(msg, args...))
	}
}

// Warn logs warning messages
func (l *ColorLogger) Warn(msg string, args ...interface{}) {
	if l.Quiet {
		return
	}
	if l.Level <= WarnLevel {
		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		color.Yellow("%s WARN %s", timestamp, fmt.Sprintf(msg, args...))
	}
}

// Error logs error messages
func (l *ColorLogger) Error(msg string, args ...interface{}) {
	if l.Quiet {
		return
	}
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	color.Red("%s ERROR %s", timestamp, fmt.Sprintf(msg, args...))
}

// Success logs success messages (always shown)
func (l *ColorLogger) Success(msg string, args ...interface{}) {
	if l.Quiet {
		return
	}
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

// StructuredLogger provides structured logging with Zap
type StructuredLogger struct {
	logger *zap.Logger
	sugar  *zap.SugaredLogger
}

// NewStructuredLogger creates a new structured logger
func NewStructuredLogger(level string) (*StructuredLogger, error) {
	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(parseLogLevel(level))
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.LevelKey = "level"
	config.EncoderConfig.MessageKey = "message"
	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.StacktraceKey = "stacktrace"

	logger, err := config.Build()
	if err != nil {
		return nil, err
	}

	return &StructuredLogger{
		logger: logger,
		sugar:  logger.Sugar(),
	}, nil
}

// parseLogLevel converts string level to zap level
func parseLogLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// WithFields creates a new logger with additional fields
func (l *StructuredLogger) WithFields(fields map[string]interface{}) *StructuredLogger {
	var zapFields []zap.Field
	for k, v := range fields {
		zapFields = append(zapFields, zap.Any(k, v))
	}
	return &StructuredLogger{
		logger: l.logger.With(zapFields...),
		sugar:  l.logger.With(zapFields...).Sugar(),
	}
}

// Debug logs a debug message with structured fields
func (l *StructuredLogger) Debug(msg string, fields ...zap.Field) {
	l.logger.Debug(msg, fields...)
}

// Info logs an info message with structured fields
func (l *StructuredLogger) Info(msg string, fields ...zap.Field) {
	l.logger.Info(msg, fields...)
}

// Warn logs a warning message with structured fields
func (l *StructuredLogger) Warn(msg string, fields ...zap.Field) {
	l.logger.Warn(msg, fields...)
}

// Error logs an error message with structured fields
func (l *StructuredLogger) Error(msg string, fields ...zap.Field) {
	l.logger.Error(msg, fields...)
}

// Debugf logs a debug message with printf-style formatting
func (l *StructuredLogger) Debugf(template string, args ...interface{}) {
	l.sugar.Debugf(template, args...)
}

// Infof logs an info message with printf-style formatting
func (l *StructuredLogger) Infof(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

// Warnf logs a warning message with printf-style formatting
func (l *StructuredLogger) Warnf(template string, args ...interface{}) {
	l.sugar.Warnf(template, args...)
}

// Errorf logs an error message with printf-style formatting
func (l *StructuredLogger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}

// Sync flushes any buffered log entries
func (l *StructuredLogger) Sync() error {
	return l.logger.Sync()
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
