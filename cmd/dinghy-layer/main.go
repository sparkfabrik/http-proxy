package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/service"
	"github.com/sparkfabrik/http-proxy/pkg/utils"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultTraefikDynamicDir is the default directory for Traefik dynamic configuration files
	DefaultTraefikDynamicDir = "/traefik/dynamic"

	// ConfigFilePermissions defines the permissions for config files
	ConfigFilePermissions = 0644

	// ConfigDirPermissions defines the permissions for config directories
	ConfigDirPermissions = 0755
)

// CompatibilityLayer implements the service.EventHandler interface
type CompatibilityLayer struct {
	dockerClient *client.Client
	logger       *logger.Logger
	config       *CompatibilityConfig
}

// CompatibilityConfig holds the configuration for the compatibility layer
type CompatibilityConfig struct {
	DryRun            bool
	LogLevel          string
	TraefikDynamicDir string
}

// Validate checks if the configuration is valid
func (c *CompatibilityConfig) Validate() error {
	if c.TraefikDynamicDir == "" {
		return fmt.Errorf("traefik dynamic directory cannot be empty")
	}

	return utils.ValidateLogLevel(c.LogLevel)
}

// NewCompatibilityLayer creates a new CompatibilityLayer instance
func NewCompatibilityLayer(cfg *CompatibilityConfig) *CompatibilityLayer {
	return &CompatibilityLayer{
		config: cfg,
	}
}

// GetName returns the service name
func (cl *CompatibilityLayer) GetName() string {
	return "dinghy-compatibility"
}

// SetDependencies sets the Docker client and logger from the service framework
func (cl *CompatibilityLayer) SetDependencies(dockerClient *client.Client, logger *logger.Logger) {
	cl.dockerClient = dockerClient
	cl.logger = logger
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
		VirtualHost: utils.GetDockerEnvVar(inspect.Config.Env, "VIRTUAL_HOST"),
		VirtualPort: utils.GetDockerEnvVar(inspect.Config.Env, "VIRTUAL_PORT"),
		IsRunning:   inspect.State.Running,
	}
}

// HandleInitialScan performs initial processing of existing containers
func (cl *CompatibilityLayer) HandleInitialScan(ctx context.Context) error {
	containers, err := utils.RetryContainerList(ctx, cl.dockerClient, container.ListOptions{})
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
					"container_id", utils.FormatDockerID(cont.ID),
					"container_name", cont.Names)
				// Continue processing other containers instead of failing fast
			}
		}
	}

	return nil
}

// HandleEvent processes a Docker event
func (cl *CompatibilityLayer) HandleEvent(ctx context.Context, event events.Message) error {
	switch event.Action {
	case "start":
		return cl.processContainer(ctx, event.Actor.ID)
	case "die":
		return cl.removeTraefikConfig(event.Actor.ID)
	default:
		// Unhandled events are not an error, just log and continue
		cl.logger.Debug("Unhandled container action", "action", event.Action, "container_id", utils.FormatDockerID(event.Actor.ID))
		return nil
	}
}

func main() {
	ctx := context.Background()

	// Initialize configuration
	cfg := &CompatibilityConfig{
		DryRun:            config.GetEnvOrDefault("DRY_RUN", "false") == "true",
		LogLevel:          config.GetEnvOrDefault("LOG_LEVEL", "info"),
		TraefikDynamicDir: config.GetEnvOrDefault("TRAEFIK_DYNAMIC_DIR", DefaultTraefikDynamicDir),
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}

	// Create handler
	handler := NewCompatibilityLayer(cfg)

	// Run service with shared framework
	if err := service.RunWithSignalHandling(ctx, handler.GetName(), cfg.LogLevel, handler); err != nil {
		fmt.Fprintf(os.Stderr, "Service failed: %v\n", err)
		os.Exit(1)
	}
}

func (cl *CompatibilityLayer) processContainer(ctx context.Context, containerID string) error {
	inspect, err := utils.RetryContainerInspect(ctx, cl.dockerClient, containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	// Extract container information
	containerInfo := cl.extractContainerInfo(inspect)

	// Skip if container is not running
	if !containerInfo.IsRunning {
		cl.logger.Debug("Skipping non-running container",
			"container_id", utils.FormatDockerID(containerID),
			"container_name", containerInfo.Name)
		return nil
	}

	// Skip if no VIRTUAL_HOST found
	if containerInfo.VirtualHost == "" {
		cl.logger.Debug("Skipping container without VIRTUAL_HOST",
			"container_id", utils.FormatDockerID(containerID),
			"container_name", containerInfo.Name)
		return nil
	}

	// Skip if traefik labels are already set.
	// Check for traefik labels (any label starting with "traefik.")
	labels := inspect.Config.Labels
	for label := range labels {
		if strings.HasPrefix(label, "traefik.") {
			cl.logger.Debug("Skipping container with existing Traefik label",
				"container_id", utils.FormatDockerID(containerID),
				"container_name", containerInfo.Name,
				"label", label)
			return nil
		}
	}

	cl.logger.Info("Found container with VIRTUAL_HOST",
		"container_id", utils.FormatDockerID(containerID),
		"container_name", containerInfo.Name,
		"virtual_host", containerInfo.VirtualHost,
		"virtual_port", containerInfo.VirtualPort)

	// Generate Traefik configuration
	traefikConfig := cl.generateTraefikConfig(inspect, containerInfo)

	cl.logger.Info("Generated Traefik configuration",
		"container_id", utils.FormatDockerID(containerID),
		"routers", len(traefikConfig.HTTP.Routers),
		"services", len(traefikConfig.HTTP.Services))

	// Write Traefik configuration to file
	return cl.writeTraefikConfig(containerID, traefikConfig)
}

func (cl *CompatibilityLayer) generateTraefikConfig(inspect types.ContainerJSON, containerInfo ContainerInfo) *config.TraefikConfig {
	traefikConfig := config.NewTraefikConfig()

	// Generate service name from container name
	serviceName := generateServiceName(inspect.Name)

	// Parse VIRTUAL_HOST (can contain multiple hosts separated by commas)
	hosts := parseVirtualHosts(containerInfo.VirtualHost)

	// Get container IP address
	containerIP := getContainerIP(inspect)
	if containerIP == "" {
		cl.logger.Error("Could not determine container IP", "container_id", utils.FormatDockerID(inspect.ID))
		return traefikConfig
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

		// Create HTTP router
		httpRouter := &config.Router{
			Rule:        rule,
			Service:     serviceName,
			EntryPoints: []string{"http"},
		}
		traefikConfig.HTTP.Routers[routerName] = httpRouter

		// Create HTTPS router (always created now)
		httpsRouterName := fmt.Sprintf("%s-tls-%d", serviceName, i)
		httpsRouter := &config.Router{
			Rule:        rule,
			Service:     serviceName,
			EntryPoints: []string{"https"},
			TLS:         &config.RouterTLSConfig{},
		}
		traefikConfig.HTTP.Routers[httpsRouterName] = httpsRouter
	}

	// Set up service
	port := getEffectivePort(hosts, containerInfo.VirtualPort, inspect)
	serverURL := fmt.Sprintf("http://%s:%s", containerIP, port)

	loadBalancer := &config.LoadBalancer{
		Servers: []config.Server{
			{URL: serverURL},
		},
	}

	traefikConfig.HTTP.Services[serviceName] = &config.Service{
		LoadBalancer: loadBalancer,
	}

	return traefikConfig
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

func (cl *CompatibilityLayer) writeTraefikConfig(containerID string, cfg *config.TraefikConfig) error {
	if cl.config.DryRun {
		cl.logger.Info("DRY RUN: Would write Traefik config",
			"container_id", utils.FormatDockerID(containerID),
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
	configData, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal Traefik config: %w", err)
	}

	// Write config file
	if err := os.WriteFile(configFile, configData, ConfigFilePermissions); err != nil {
		return fmt.Errorf("failed to write Traefik config file: %w", err)
	}

	cl.logger.Info("Wrote Traefik configuration",
		"container_id", utils.FormatDockerID(containerID),
		"config_file", configFile)

	return nil
}

func (cl *CompatibilityLayer) removeTraefikConfig(containerID string) error {
	if cl.config.DryRun {
		cl.logger.Info("DRY RUN: Would remove Traefik config",
			"container_id", utils.FormatDockerID(containerID),
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
		"container_id", utils.FormatDockerID(containerID),
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

// configFileName returns the config file name for a container
func (cl *CompatibilityLayer) configFileName(containerID string) string {
	return fmt.Sprintf("%s.yaml", utils.FormatDockerID(containerID))
}
