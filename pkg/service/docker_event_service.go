package service

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/utils"
)

const (
	// DefaultDockerTimeout is the default timeout for Docker operations
	DefaultDockerTimeout = 30 * time.Second
)

// EventHandler defines the interface for processing Docker events
type EventHandler interface {
	// HandleInitialScan performs initial processing of existing containers
	HandleInitialScan(ctx context.Context) error

	// HandleEvent processes a Docker event
	HandleEvent(ctx context.Context, event events.Message) error

	// GetName returns the service name for logging
	GetName() string

	// SetDependencies injects Docker client and logger
	SetDependencies(client *client.Client, logger *logger.Logger)
}

// eventSubscriber subscribes to the Docker event stream. It matches the
// signature of (*client.Client).Events and exists as a seam so the reconnect
// behavior of the event loop can be tested without a Docker daemon.
type eventSubscriber func(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)

// Service represents a Docker-event-driven service
type Service struct {
	client         *client.Client
	logger         *logger.Logger
	handler        EventHandler
	serviceName    string
	subscribe      eventSubscriber
	reconnectDelay time.Duration
}

// NewService creates a new Docker event-driven service
func NewService(ctx context.Context, serviceName string, logLevel string, handler EventHandler) (*Service, error) {
	// Initialize logger
	log := logger.NewWithLevel(serviceName, logger.LogLevel(logLevel))

	// Initialize Docker client
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Test Docker connection with timeout
	pingCtx, cancel := context.WithTimeout(ctx, DefaultDockerTimeout)
	defer cancel()

	if _, err := dockerClient.Ping(pingCtx); err != nil {
		dockerClient.Close()
		return nil, fmt.Errorf("failed to connect to Docker daemon: %w", err)
	}

	log.Debug("Successfully connected to Docker daemon")

	// Inject dependencies into handler
	handler.SetDependencies(dockerClient, log)

	return &Service{
		client:         dockerClient,
		logger:         log,
		handler:        handler,
		serviceName:    serviceName,
		subscribe:      dockerClient.Events,
		reconnectDelay: 5 * time.Second,
	}, nil
}

// GetDockerClient returns the Docker client for use by handlers
func (s *Service) GetDockerClient() *client.Client {
	return s.client
}

// GetLogger returns the logger for use by handlers
func (s *Service) GetLogger() *logger.Logger {
	return s.logger
}

// Close cleanly shuts down the service
func (s *Service) Close() error {
	return s.client.Close()
}

// Run executes the event loop until the context is cancelled or the loop fails.
// Signal handling and lifecycle are owned by RunWithSignalHandling.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("Starting service", "name", s.serviceName)
	return s.runEventLoop(ctx)
}

// containerEventOptions returns the Docker event-stream filters for the
// container start/die events the services react to.
func containerEventOptions() events.ListOptions {
	return events.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("type", "container"),
			filters.Arg("event", "start"),
			filters.Arg("event", "die"),
		),
	}
}

// runEventLoop handles the initial scan and Docker event processing
func (s *Service) runEventLoop(ctx context.Context) error {
	// Initial scan of existing containers
	s.logger.Debug("Performing initial scan")
	if err := s.handler.HandleInitialScan(ctx); err != nil {
		s.logger.Error("Initial scan failed", "error", err)
		return err
	}

	// Listen for Docker events
	eventsChan, errChan := s.subscribe(ctx, containerEventOptions())

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-eventsChan:
			if !ok {
				// The Docker daemon closed the stream (e.g. restart). Reconnect
				// after a backoff instead of spinning on the closed channel.
				if !s.backoffBeforeReconnect(ctx) {
					return nil
				}
				eventsChan, errChan = s.subscribe(ctx, containerEventOptions())
				continue
			}
			s.processEventSafely(ctx, event)
		case err, ok := <-errChan:
			if !ok {
				if !s.backoffBeforeReconnect(ctx) {
					return nil
				}
				eventsChan, errChan = s.subscribe(ctx, containerEventOptions())
				continue
			}
			if err != nil {
				s.logger.Error("Docker events error", "error", err)
				if !s.backoffBeforeReconnect(ctx) {
					return nil
				}
				eventsChan, errChan = s.subscribe(ctx, containerEventOptions())
			}
		}
	}
}

// backoffBeforeReconnect waits before reconnecting to the Docker event stream.
// It returns false if the context is cancelled during the wait, signalling the
// caller to stop instead of reconnecting.
func (s *Service) backoffBeforeReconnect(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(s.reconnectDelay):
		return true
	}
}

// processEventSafely wraps event processing with proper error handling and logging
func (s *Service) processEventSafely(ctx context.Context, event events.Message) {
	// Respect context cancellation
	select {
	case <-ctx.Done():
		s.logger.Debug("Context cancelled, skipping event processing")
		return
	default:
	}

	if err := s.handler.HandleEvent(ctx, event); err != nil {
		s.logger.Error("Failed to process event",
			"error", err,
			"action", event.Action,
			"container_id", utils.FormatDockerID(event.Actor.ID))
	}
}

// RunWithSignalHandling is a convenience function that sets up a complete service lifecycle
func RunWithSignalHandling(ctx context.Context, serviceName string, logLevel string, handler EventHandler) error {
	service, err := NewService(ctx, serviceName, logLevel, handler)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize %s: %v\n", serviceName, err)
		os.Exit(1)
	}
	defer service.Close()

	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// Start the service
	errChan := make(chan error, 1)
	go func() {
		errChan <- service.Run(serviceCtx)
	}()

	// Wait for shutdown signal or error
	select {
	case err := <-errChan:
		if err != nil {
			service.GetLogger().Error("Service failed", "error", err)
			os.Exit(1)
		}
		service.GetLogger().Info("Service completed successfully")
	case sig := <-sigChan:
		service.GetLogger().Info("Received shutdown signal", "signal", sig)
		cancel()

		// Wait for graceful shutdown with timeout
		select {
		case err := <-errChan:
			if err != nil {
				service.GetLogger().Error("Error during shutdown", "error", err)
			}
		case <-time.After(10 * time.Second):
			service.GetLogger().Warn("Shutdown timeout, forcing exit")
		}
	}

	service.GetLogger().Info("Shutting down gracefully")
	return nil
}
