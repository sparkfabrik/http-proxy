package utils

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// RetryConfig configures retry behavior for operations
type RetryConfig struct {
	// MaxAttempts is the maximum number of attempts (including the first one)
	MaxAttempts int
	// InitialDelay is the delay before the first retry
	InitialDelay time.Duration
	// MaxDelay is the maximum delay between retries
	MaxDelay time.Duration
	// BackoffMultiplier is the factor by which the delay increases after each retry
	BackoffMultiplier float64
}

// DefaultRetryConfig returns a sensible default retry configuration for Docker operations
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       3,
		InitialDelay:      100 * time.Millisecond,
		MaxDelay:          2 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// RetryableFunc is a function that can be retried. It should return an error if the operation
// should be retried, or nil if successful. The context can be used to cancel the operation.
type RetryableFunc func(ctx context.Context) error

// Retry executes a function with retry logic and exponential backoff
// It respects context cancellation and returns the last error encountered
func Retry(ctx context.Context, config RetryConfig, fn RetryableFunc) error {
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 1
	}

	var lastErr error
	delay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Check if context is cancelled before attempting
		if err := CheckContext(ctx); err != nil {
			return err
		}

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil // Success
		}

		// Don't sleep after the last attempt
		if attempt == config.MaxAttempts {
			break
		}

		// Calculate next delay with exponential backoff
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}

		// Update delay for next iteration
		delay = time.Duration(float64(delay) * config.BackoffMultiplier)
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", config.MaxAttempts, lastErr)
}

// RetryContainerInspect wraps ContainerInspect with retry logic
func RetryContainerInspect(ctx context.Context, dockerClient *client.Client, containerID string) (types.ContainerJSON, error) {
	var result types.ContainerJSON

	err := Retry(ctx, DefaultRetryConfig(), func(ctx context.Context) error {
		var err error
		result, err = dockerClient.ContainerInspect(ctx, containerID)
		return err
	})

	return result, err
}

// RetryContainerList wraps ContainerList with retry logic
func RetryContainerList(ctx context.Context, dockerClient *client.Client, options container.ListOptions) ([]types.Container, error) {
	var result []types.Container

	err := Retry(ctx, DefaultRetryConfig(), func(ctx context.Context) error {
		var err error
		result, err = dockerClient.ContainerList(ctx, options)
		return err
	})

	return result, err
}

// RetryNetworkConnect wraps NetworkConnect with retry logic
func RetryNetworkConnect(ctx context.Context, dockerClient *client.Client, networkID, containerName string, config *network.EndpointSettings) error {
	return Retry(ctx, DefaultRetryConfig(), func(ctx context.Context) error {
		return dockerClient.NetworkConnect(ctx, networkID, containerName, config)
	})
}

// RetryNetworkInspect wraps NetworkInspect with retry logic
func RetryNetworkInspect(ctx context.Context, dockerClient *client.Client, networkID string, options network.InspectOptions) (network.Inspect, error) {
	var result network.Inspect

	err := Retry(ctx, DefaultRetryConfig(), func(ctx context.Context) error {
		var err error
		result, err = dockerClient.NetworkInspect(ctx, networkID, options)
		return err
	})

	return result, err
}

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

// HasManageableContainersInNetwork checks if a network has any manageable containers,
// optionally excluding a specific container
func HasManageableContainersInNetwork(ctx context.Context, dockerClient *client.Client, networkID, excludeContainerName string) (bool, error) {
	// Inspect the network to get the container map
	networkResource, err := dockerClient.NetworkInspect(ctx, networkID,
		network.InspectOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to inspect network: %w", err)
	}

	for containerID, endpoint := range networkResource.Containers {
		// Skip if this is the excluded container
		if excludeContainerName != "" {
			containerName := strings.TrimPrefix(endpoint.Name, "/")
			if containerName == excludeContainerName {
				continue
			}
		}

		// Inspect the container to get its details
		inspect, err := RetryContainerInspect(ctx, dockerClient, containerID)
		if err != nil {
			continue // Skip containers we can't inspect
		}

		// Only consider running containers
		if !inspect.State.Running {
			continue
		}

		if ShouldManageContainer(inspect.Config.Env, inspect.Config.Labels) {
			return true, nil
		}
	}

	return false, nil
}

// SliceToSet converts a slice of strings to a map[string]struct{} for O(1) lookups
// This is useful for creating sets from slices where you only need to check existence
func SliceToSet(slice []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, item := range slice {
		set[item] = struct{}{}
	}
	return set
}
