package logger

import (
	"log/slog"
	"os"
	"strings"

	"github.com/sparkfabrik/http-proxy/pkg/config"
)

// getLogLevelFromEnv reads the LOG_LEVEL environment variable and returns the corresponding LogLevel.
// If the environment variable is not set or contains an invalid value, it returns LevelInfo as default.
func getLogLevelFromEnv() LogLevel {
	level := strings.ToLower(config.GetEnvOrDefault("LOG_LEVEL", "info"))
	switch level {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	case "info":
		return LevelInfo
	default:
		return LevelInfo
	}
}

// Logger provides structured logging for the application
type Logger struct {
	*slog.Logger
	component string
}

// LogLevel represents the logging level
type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// New creates a new structured logger instance
func New(component string) *Logger {
	return NewWithLevel(component, LevelInfo)
}

// NewWithEnv creates a new logger instance using the LOG_LEVEL environment variable.
// If LOG_LEVEL is not set or contains an invalid value, it defaults to info level.
func NewWithEnv(component string) *Logger {
	level := getLogLevelFromEnv()
	return NewWithLevel(component, level)
}

// NewWithLevel creates a new logger with specified log level
func NewWithLevel(component string, level LogLevel) *Logger {
	var slogLevel slog.Level
	switch level {
	case LevelDebug:
		slogLevel = slog.LevelDebug
	case LevelWarn:
		slogLevel = slog.LevelWarn
	case LevelError:
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	// Create handler with JSON output for structured logging
	opts := &slog.HandlerOptions{
		Level: slogLevel,
	}

	var handler slog.Handler
	if isJSONFormat() {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	// Create logger with component field as the first attribute
	logger := slog.New(handler).With("component", component)

	return &Logger{
		Logger:    logger,
		component: component,
	}
}

// isJSONFormat determines if we should use JSON logging format
// based on environment variables
func isJSONFormat() bool {
	format := strings.ToLower(config.GetEnvOrDefault("LOG_FORMAT", "text"))
	return format == "json"
}

// Info logs an info message with optional key-value pairs
func (l *Logger) Info(msg string, args ...interface{}) {
	l.Logger.Info(msg, args...)
}

// Error logs an error message with optional key-value pairs
func (l *Logger) Error(msg string, args ...interface{}) {
	l.Logger.Error(msg, args...)
}

// Debug logs a debug message with optional key-value pairs
func (l *Logger) Debug(msg string, args ...interface{}) {
	l.Logger.Debug(msg, args...)
}

// Warn logs a warning message with optional key-value pairs
func (l *Logger) Warn(msg string, args ...interface{}) {
	l.Logger.Warn(msg, args...)
}

// With returns a new logger with the given key-value pairs added to all log entries
func (l *Logger) With(args ...interface{}) *Logger {
	return &Logger{
		Logger:    l.Logger.With(args...),
		component: l.component,
	}
}
