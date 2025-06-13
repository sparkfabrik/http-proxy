package logger

import (
	"log"
	"os"
)

// Logger provides structured logging for the application
type Logger struct {
	*log.Logger
}

// New creates a new logger instance
func New(prefix string) *Logger {
	return &Logger{
		Logger: log.New(os.Stdout, prefix+" ", log.LstdFlags),
	}
}

// Info logs an info message
func (l *Logger) Info(v ...interface{}) {
	l.Println("[INFO]", v)
}

// Error logs an error message
func (l *Logger) Error(v ...interface{}) {
	l.Println("[ERROR]", v)
}

// Debug logs a debug message
func (l *Logger) Debug(v ...interface{}) {
	l.Println("[DEBUG]", v)
}

// Warn logs a warning message
func (l *Logger) Warn(v ...interface{}) {
	l.Println("[WARN]", v)
}
