package utils

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
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

// ShouldManageContainer checks if a container should be managed based on dinghy env vars or traefik labels
// Returns true if the container has VIRTUAL_HOST environment variable or traefik labels
func ShouldManageContainer(env []string, labels map[string]string) bool {
	// Check for dinghy VIRTUAL_HOST environment variable
	if GetDockerEnvVar(env, "VIRTUAL_HOST") != "" {
		return true
	}

	// Check for traefik labels (any label starting with "traefik.")
	for label := range labels {
		if strings.HasPrefix(label, "traefik.") {
			return true
		}
	}

	return false
}

// GetRunningContainersInNetwork returns all running containers connected to the specified network,
// optionally excluding a container by name
func GetRunningContainersInNetwork(ctx context.Context, dockerClient *client.Client, networkID, excludeContainerName string) ([]types.Container, error) {
	// Get all containers
	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var networkContainers []types.Container
	for _, cont := range containers {
		// Skip non-running containers
		if cont.State != "running" {
			continue
		}

		// Skip if this is the excluded container
		if excludeContainerName != "" && len(cont.Names) > 0 {
			containerName := strings.TrimPrefix(cont.Names[0], "/")
			if containerName == excludeContainerName {
				continue
			}
		}

		// Check if this container is connected to the network
		inspect, err := dockerClient.ContainerInspect(ctx, cont.ID)
		if err != nil {
			continue // Skip containers we can't inspect
		}

		isConnected := false
		for _, networkData := range inspect.NetworkSettings.Networks {
			if networkData.NetworkID == networkID {
				isConnected = true
				break
			}
		}

		if isConnected {
			networkContainers = append(networkContainers, cont)
		}
	}

	return networkContainers, nil
}

// HasManageableContainersInNetwork checks if a network has any manageable containers,
// optionally excluding a specific container
func HasManageableContainersInNetwork(ctx context.Context, dockerClient *client.Client, networkID, excludeContainerName string) (bool, error) {
	containers, err := GetRunningContainersInNetwork(ctx, dockerClient, networkID, excludeContainerName)
	if err != nil {
		return false, err
	}

	for _, cont := range containers {
		// Inspect the container to get env vars and labels
		inspect, err := dockerClient.ContainerInspect(ctx, cont.ID)
		if err != nil {
			continue // Skip containers we can't inspect
		}

		if ShouldManageContainer(inspect.Config.Env, inspect.Config.Labels) {
			return true, nil
		}
	}

	return false, nil
}
