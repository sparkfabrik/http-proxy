package logger

import (
	"log/slog"
	"os"
	"strings"
)

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

	logger := slog.New(handler)

	return &Logger{
		Logger:    logger,
		component: component,
	}
}

// isJSONFormat determines if we should use JSON logging format
// based on environment variables
func isJSONFormat() bool {
	format := strings.ToLower(os.Getenv("LOG_FORMAT"))
	return format == "json"
}

// withComponent adds the component field to all log entries
func (l *Logger) withComponent() *slog.Logger {
	return l.Logger.With("component", l.component)
}

// Info logs an info message with optional key-value pairs
func (l *Logger) Info(msg string, args ...interface{}) {
	l.withComponent().Info(msg, args...)
}

// Error logs an error message with optional key-value pairs
func (l *Logger) Error(msg string, args ...interface{}) {
	l.withComponent().Error(msg, args...)
}

// Debug logs a debug message with optional key-value pairs
func (l *Logger) Debug(msg string, args ...interface{}) {
	l.withComponent().Debug(msg, args...)
}

// Warn logs a warning message with optional key-value pairs
func (l *Logger) Warn(msg string, args ...interface{}) {
	l.withComponent().Warn(msg, args...)
}

// With returns a new logger with the given key-value pairs added to all log entries
func (l *Logger) With(args ...interface{}) *Logger {
	return &Logger{
		Logger:    l.withComponent().With(args...),
		component: l.component,
	}
}
