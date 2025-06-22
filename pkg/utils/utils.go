package utils

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FormatDockerID returns a shortened version of a Docker ID for logging
// This can be used for container IDs, network IDs, or any Docker resource ID
func FormatDockerID(id string) string {
	if len(id) >= 12 {
		return id[:12]
	}
	return id
}

// GetDockerEnvVar extracts an environment variable value from a Docker container's env slice
// This is commonly used when inspecting containers to get specific environment variables
func GetDockerEnvVar(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

// ValidLogLevels contains the set of valid log levels
var ValidLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// ValidateLogLevel checks if the provided log level is valid
func ValidateLogLevel(level string) error {
	if !ValidLogLevels[level] {
		return fmt.Errorf("invalid log level %q, must be one of: debug, info, warn, error", level)
	}
	return nil
}

// CheckContext returns an error if the context is cancelled
// This is useful for long-running operations that should respect cancellation
func CheckContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// RetryOperation executes an operation with configurable retry attempts
func RetryOperation(operation func() error, description string, maxRetries int, retryDelay time.Duration, logger Logger) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if attempt < maxRetries {
				logger.Warn("Operation attempt failed, retrying",
					"attempt", attempt,
					"max_attempts", maxRetries,
					"operation", description,
					"error", err,
					"retry_delay", retryDelay)

				time.Sleep(retryDelay)
				continue
			}
		} else {
			if attempt > 1 {
				logger.Info("Operation succeeded after retry",
					"operation", description,
					"attempt", attempt)
			}
			return nil
		}
	}

	return fmt.Errorf("operation %s failed after %d attempts: %w", description, maxRetries, lastErr)
}

// Logger interface for the retry operation
// This allows the utility to work with any logger implementation
type Logger interface {
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}
