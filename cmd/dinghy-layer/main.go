package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/traefik/traefik/v3/pkg/config/dynamic"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultTraefikDynamicDir is the default directory for Traefik dynamic configuration files
	DefaultTraefikDynamicDir = "/traefik/dynamic"

	// ConfigFilePermissions defines the permissions for config files
	ConfigFilePermissions = 0644

	// ConfigDirPermissions defines the permissions for config directories
	ConfigDirPermissions = 0755

	// DefaultDockerTimeout is the default timeout for Docker operations
	DefaultDockerTimeout = 30 * time.Second
)

// CompatibilityLayer encapsulates the compatibility layer functionality
type CompatibilityLayer struct {
	client *client.Client
	logger *logger.Logger
	config *CompatibilityConfig
}

// NewCompatibilityLayer creates a new CompatibilityLayer instance
func NewCompatibilityLayer(ctx context.Context, cfg *CompatibilityConfig) (*CompatibilityLayer, error) {
	// Initialize logger
	log := logger.NewWithLevel("dinghy-compatibility", logger.LogLevel(cfg.LogLevel))

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

	return &CompatibilityLayer{
		client: dockerClient,
		logger: log,
		config: cfg,
	}, nil
}

// Close cleanly shuts down the compatibility layer
func (cl *CompatibilityLayer) Close() error {
	return cl.client.Close()
}

// CompatibilityConfig holds the configuration for the compatibility layer
type CompatibilityConfig struct {
	DryRun            bool
	LogLevel          string
	CheckInterval     time.Duration
	TraefikDynamicDir string
}

// Validate checks if the configuration is valid
func (c *CompatibilityConfig) Validate() error {
	if c.CheckInterval <= 0 {
		return fmt.Errorf("check interval must be positive, got %v", c.CheckInterval)
	}

	if c.TraefikDynamicDir == "" {
		return fmt.Errorf("traefik dynamic directory cannot be empty")
	}

	validLogLevels := map[string]bool{
		"debug": true, "info": true, "warn": true, "error": true,
	}
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("invalid log level %q, must be one of: debug, info, warn, error", c.LogLevel)
	}

	return nil
}

// TraefikLabels represents the labels to be applied to containers
type TraefikLabels struct {
	Enable      string
	Rule        string
	Port        string
	RouterName  string
	ServiceName string
}

// ContainerInfo holds essential container information for processing
type ContainerInfo struct {
	ID          string
	Name        string
	VirtualHost string
	VirtualPort string
	IsRunning   bool
}

// extractContainerInfo extracts relevant information from a container inspection
func (cl *CompatibilityLayer) extractContainerInfo(inspect types.ContainerJSON) ContainerInfo {
	return ContainerInfo{
		ID:          inspect.ID,
		Name:        strings.TrimPrefix(inspect.Name, "/"),
		VirtualHost: getEnvVar(inspect.Config.Env, "VIRTUAL_HOST"),
		VirtualPort: getEnvVar(inspect.Config.Env, "VIRTUAL_PORT"),
		IsRunning:   inspect.State.Running,
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize configuration
	cfg := &CompatibilityConfig{
		DryRun:            config.GetEnvOrDefault("DRY_RUN", "false") == "true",
		LogLevel:          config.GetEnvOrDefault("LOG_LEVEL", "info"),
		CheckInterval:     parseDuration(config.GetEnvOrDefault("CHECK_INTERVAL", "30s")),
		TraefikDynamicDir: config.GetEnvOrDefault("TRAEFIK_DYNAMIC_DIR", DefaultTraefikDynamicDir),
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize compatibility layer
	cl, err := NewCompatibilityLayer(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize compatibility layer: %v\n", err)
		os.Exit(1)
	}
	defer cl.Close()

	cl.logger.Info("Starting Dinghy Compatibility Layer",
		"dry_run", cfg.DryRun,
		"log_level", cfg.LogLevel,
		"check_interval", cfg.CheckInterval,
		"traefik_dynamic_dir", cfg.TraefikDynamicDir)
	if cfg.DryRun {
		cl.logger.Warn("Running in dry-run mode - no actual changes will be made")
	}

	cl.logger.Info("Connected to Docker daemon")

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the compatibility layer
	errChan := make(chan error, 1)
	go func() {
		errChan <- cl.Run(ctx)
	}()

	// Wait for shutdown signal or error
	select {
	case <-sigChan:
		cl.logger.Info("Received shutdown signal")
		cancel()
	case err := <-errChan:
		if err != nil {
			cl.logger.Error("Compatibility layer error", "error", err)
			os.Exit(1)
		}
	}

	cl.logger.Info("Shutting down gracefully")
}

// Run starts the compatibility layer event loop
func (cl *CompatibilityLayer) Run(ctx context.Context) error {
	// Initial scan of existing containers
	cl.logger.Info("Performing initial scan of existing containers")
	if err := cl.scanExistingContainers(ctx); err != nil {
		cl.logger.Error("Initial scan failed", "error", err)
		return err
	}

	// Listen for Docker events
	eventsChan, errChan := cl.client.Events(ctx, events.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("type", "container"),
			filters.Arg("event", "start"),
			filters.Arg("event", "die"),
		),
	})

	ticker := time.NewTicker(cl.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-eventsChan:
			cl.processContainerSafely(ctx, event.Actor.ID, string(event.Action))
		case err := <-errChan:
			if err != nil {
				cl.logger.Error("Docker events error", "error", err)
				// Reconnect and continue
				time.Sleep(5 * time.Second)
				eventsChan, errChan = cl.client.Events(ctx, events.ListOptions{
					Filters: filters.NewArgs(
						filters.Arg("type", "container"),
						filters.Arg("event", "start"),
						filters.Arg("event", "die"),
					),
				})
			}
		case <-ticker.C:
			// Periodic scan to catch any missed containers
			cl.logger.Debug("Performing periodic container scan")
			if err := cl.scanExistingContainers(ctx); err != nil {
				cl.logger.Error("Periodic scan failed", "error", err)
			}
		}
	}
}

func (cl *CompatibilityLayer) scanExistingContainers(ctx context.Context) error {
	containers, err := cl.client.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	cl.logger.Info("Scanning existing containers", "count", len(containers))

	for _, cont := range containers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := cl.processContainer(ctx, cont.ID); err != nil {
				cl.logger.Error("Failed to process container",
					"error", err,
					"container_id", cl.shortContainerID(cont.ID),
					"container_name", cont.Names)
				// Continue processing other containers instead of failing fast
			}
		}
	}

	return nil
}

func (cl *CompatibilityLayer) processContainer(ctx context.Context, containerID string) error {
	inspect, err := cl.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	// Extract container information
	containerInfo := cl.extractContainerInfo(inspect)

	// Skip if container is not running
	if !containerInfo.IsRunning {
		cl.logger.Debug("Skipping non-running container",
			"container_id", cl.shortContainerID(containerID),
			"container_name", containerInfo.Name)
		return nil
	}

	// Skip if no VIRTUAL_HOST found
	if containerInfo.VirtualHost == "" {
		cl.logger.Debug("Skipping container without VIRTUAL_HOST",
			"container_id", cl.shortContainerID(containerID),
			"container_name", containerInfo.Name)
		return nil
	}

	cl.logger.Info("Found container with VIRTUAL_HOST",
		"container_id", cl.shortContainerID(containerID),
		"container_name", containerInfo.Name,
		"virtual_host", containerInfo.VirtualHost,
		"virtual_port", containerInfo.VirtualPort)

	// Generate Traefik configuration
	traefikConfig := cl.generateTraefikConfig(inspect, containerInfo.VirtualHost, containerInfo.VirtualPort)

	cl.logger.Info("Generated Traefik configuration",
		"container_id", cl.shortContainerID(containerID),
		"routers", len(traefikConfig.HTTP.Routers),
		"services", len(traefikConfig.HTTP.Services))

	// Write Traefik configuration to file
	return cl.writeTraefikConfig(containerID, traefikConfig)
}

func getEnvVar(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

func (cl *CompatibilityLayer) generateTraefikConfig(inspect types.ContainerJSON, virtualHost, virtualPort string) *dynamic.Configuration {
	config := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers:  make(map[string]*dynamic.Router),
			Services: make(map[string]*dynamic.Service),
		},
	}

	// Generate service name from container name
	serviceName := generateServiceName(inspect.Name)

	// Parse VIRTUAL_HOST (can contain multiple hosts separated by commas)
	hosts := parseVirtualHosts(virtualHost)

	// Get container IP address
	containerIP := getContainerIP(inspect)
	if containerIP == "" {
		cl.logger.Error("Could not determine container IP", "container_id", cl.shortContainerID(inspect.ID))
		return config
	}

	for i, host := range hosts {
		routerName := fmt.Sprintf("%s-%d", serviceName, i)

		// Set up router rule
		var rule string
		if isWildcardHost(host.hostname) {
			// Handle wildcard hosts
			rule = fmt.Sprintf("HostRegexp(`%s`)", convertWildcardToRegex(host.hostname))
		} else {
			// Regular host
			rule = fmt.Sprintf("Host(`%s`)", host.hostname)
		}

		config.HTTP.Routers[routerName] = &dynamic.Router{
			Rule:        rule,
			Service:     serviceName,
			EntryPoints: []string{"web"},
		}
	}

	// Set up service
	port := getEffectivePort(hosts, virtualPort, inspect)
	serverURL := fmt.Sprintf("http://%s:%s", containerIP, port)

	loadBalancer := &dynamic.ServersLoadBalancer{
		Servers: []dynamic.Server{
			{URL: serverURL},
		},
	}
	loadBalancer.SetDefaults()

	config.HTTP.Services[serviceName] = &dynamic.Service{
		LoadBalancer: loadBalancer,
	}

	return config
}

func getContainerIP(inspect types.ContainerJSON) string {
	// Try to get IP from the first network
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Networks != nil {
		for _, network := range inspect.NetworkSettings.Networks {
			if network.IPAddress != "" {
				return network.IPAddress
			}
		}
	}
	return ""
}

func getEffectivePort(hosts []virtualHost, virtualPort string, inspect types.ContainerJSON) string {
	// Check if any host specifies a port
	for _, host := range hosts {
		if host.port != "" {
			return host.port
		}
	}

	// Use VIRTUAL_PORT if specified
	if virtualPort != "" {
		return virtualPort
	}

	// Fall back to default port detection
	return getDefaultPort(inspect)
}

func (cl *CompatibilityLayer) writeTraefikConfig(containerID string, config *dynamic.Configuration) error {
	if cl.config.DryRun {
		cl.logger.Info("DRY RUN: Would write Traefik config",
			"container_id", cl.shortContainerID(containerID),
			"config_file", cl.configFileName(containerID))
		return nil
	}

	// Ensure the dynamic config directory exists
	if err := os.MkdirAll(cl.config.TraefikDynamicDir, ConfigDirPermissions); err != nil {
		return fmt.Errorf("failed to create Traefik dynamic directory: %w", err)
	}

	// Generate config file path
	configFile := filepath.Join(cl.config.TraefikDynamicDir, cl.configFileName(containerID))

	// Marshal config to YAML
	configData, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal Traefik config: %w", err)
	}

	// Write config file
	if err := os.WriteFile(configFile, configData, ConfigFilePermissions); err != nil {
		return fmt.Errorf("failed to write Traefik config file: %w", err)
	}

	cl.logger.Info("Wrote Traefik configuration",
		"container_id", cl.shortContainerID(containerID),
		"config_file", configFile)

	return nil
}

func (cl *CompatibilityLayer) removeTraefikConfig(containerID string) error {
	if cl.config.DryRun {
		cl.logger.Info("DRY RUN: Would remove Traefik config",
			"container_id", cl.shortContainerID(containerID),
			"config_file", cl.configFileName(containerID))
		return nil
	}

	configFile := filepath.Join(cl.config.TraefikDynamicDir, cl.configFileName(containerID))

	// Check if file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		cl.logger.Debug("Traefik config file does not exist", "config_file", configFile)
		return nil
	}

	// Remove config file
	if err := os.Remove(configFile); err != nil {
		return fmt.Errorf("failed to remove Traefik config file: %w", err)
	}

	cl.logger.Info("Removed Traefik configuration",
		"container_id", cl.shortContainerID(containerID),
		"config_file", configFile)

	return nil
}

type virtualHost struct {
	hostname string
	port     string
}

func parseVirtualHosts(virtualHostEnv string) []virtualHost {
	var hosts []virtualHost

	// Split by comma for multiple hosts
	hostEntries := strings.Split(virtualHostEnv, ",")

	for _, entry := range hostEntries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// Check if port is specified (host:port format)
		parts := strings.Split(entry, ":")
		if len(parts) == 2 && isPort(parts[1]) {
			hosts = append(hosts, virtualHost{
				hostname: parts[0],
				port:     parts[1],
			})
		} else {
			hosts = append(hosts, virtualHost{
				hostname: entry,
				port:     "",
			})
		}
	}

	return hosts
}

func isPort(s string) bool {
	port, err := strconv.Atoi(s)
	return err == nil && port > 0 && port <= 65535
}

func isWildcardHost(hostname string) bool {
	return strings.Contains(hostname, "*") || strings.HasPrefix(hostname, "~")
}

func convertWildcardToRegex(hostname string) string {
	if strings.HasPrefix(hostname, "~") {
		// Already a regex, return as-is (remove the ~ prefix)
		return strings.TrimPrefix(hostname, "~")
	}

	// Convert wildcard to regex
	regex := strings.ReplaceAll(hostname, ".", "\\.")
	regex = strings.ReplaceAll(regex, "*", ".*")
	return fmt.Sprintf("^%s$", regex)
}

func generateServiceName(containerName string) string {
	// Remove leading slash and sanitize name for Traefik
	name := strings.TrimPrefix(containerName, "/")
	// Replace invalid characters with hyphens
	reg := regexp.MustCompile(`[^a-zA-Z0-9-]`)
	name = reg.ReplaceAllString(name, "-")
	// Remove consecutive hyphens
	reg = regexp.MustCompile(`-+`)
	name = reg.ReplaceAllString(name, "-")
	// Trim hyphens from start and end
	name = strings.Trim(name, "-")

	if name == "" {
		name = "service"
	}

	return name
}

func getDefaultPort(inspect types.ContainerJSON) string {
	// Get the first exposed port or return "80" as default
	if inspect.Config.ExposedPorts != nil {
		for port := range inspect.Config.ExposedPorts {
			if strings.HasSuffix(string(port), "/tcp") {
				return strings.TrimSuffix(string(port), "/tcp")
			}
		}
	}

	// Check port bindings
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Ports != nil {
		for port := range inspect.NetworkSettings.Ports {
			if strings.HasSuffix(string(port), "/tcp") {
				return strings.TrimSuffix(string(port), "/tcp")
			}
		}
	}

	return "80"
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return DefaultDockerTimeout
	}
	return d
}

// shortContainerID returns a shortened version of a container ID for logging
func (cl *CompatibilityLayer) shortContainerID(containerID string) string {
	if len(containerID) >= 12 {
		return containerID[:12]
	}
	return containerID
}

// configFileName returns the config file name for a container
func (cl *CompatibilityLayer) configFileName(containerID string) string {
	return fmt.Sprintf("%s.yaml", cl.shortContainerID(containerID))
}

// processContainerSafely wraps container processing with proper error handling and logging
func (cl *CompatibilityLayer) processContainerSafely(ctx context.Context, containerID string, action string) {
	// Respect context cancellation
	select {
	case <-ctx.Done():
		cl.logger.Debug("Context cancelled, skipping container processing",
			"container_id", cl.shortContainerID(containerID))
		return
	default:
	}

	var err error
	switch action {
	case "start":
		err = cl.processContainer(ctx, containerID)
	case "die":
		err = cl.removeTraefikConfig(containerID)
	default:
		cl.logger.Debug("Unhandled container action", "action", action, "container_id", cl.shortContainerID(containerID))
		return
	}

	if err != nil {
		cl.logger.Error("Failed to process container",
			"error", err,
			"action", action,
			"container_id", cl.shortContainerID(containerID))
	}
}
