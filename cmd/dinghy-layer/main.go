// Package main implements a compatibility layer for dinghy-http-proxy that
// translates Docker container environment variables (VIRTUAL_HOST, VIRTUAL_PORT)
// into Traefik dynamic configuration files. This allows containers configured
// for nginx-proxy/jwilder to work seamlessly with Traefik without modification.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// CompatibilityLayer implements the service.EventHandler interface and provides
// a compatibility layer that translates nginx-proxy environment variables to
// Traefik dynamic configuration. It monitors Docker events and generates
// appropriate Traefik routing rules for containers with VIRTUAL_HOST variables.
type CompatibilityLayer struct {
	dockerClient *client.Client
	logger       *logger.Logger
	config       *CompatibilityConfig
}

// CompatibilityConfig holds the configuration options for the compatibility layer.
// It controls the behavior of the dinghy compatibility service including dry-run
// mode, logging level, and the directory where Traefik dynamic configuration
// files should be written.
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

// ContainerInfo holds essential container information extracted from Docker
// container inspection. This struct contains the minimal set of data needed
// to generate Traefik configuration from nginx-proxy environment variables.
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

	// keep holds the config file names of the containers processed in this scan.
	// After the scan it drives reconciliation: any dinghy-layer config file not
	// in this set belongs to a container that no longer exists and is removed.
	keep := make(map[string]struct{})
	scanErrors := 0

	for _, cont := range containers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			name, err := cl.processContainer(ctx, cont.ID)
			if err != nil {
				scanErrors++
				cl.logger.Error("Failed to process container",
					"error", err,
					"container_id", utils.FormatDockerID(cont.ID),
					"container_name", cont.Names)
				// Continue processing other containers instead of failing fast
				continue
			}
			if name != "" {
				keep[name] = struct{}{}
			}
		}
	}

	// Skip reconciliation when a container could not be inspected. A transient
	// inspect failure would leave that container out of keep, and pruning then
	// would delete the config of a container that is still running. Reconcile
	// only when the scan saw every container cleanly.
	if scanErrors > 0 {
		cl.logger.Warn("Skipping orphaned-config reconciliation after scan errors",
			"errors", scanErrors)
		return nil
	}

	if err := cl.reconcileConfigs(keep); err != nil {
		cl.logger.Error("Failed to reconcile Traefik configs", "error", err)
	}

	return nil
}

// HandleEvent processes a Docker event
func (cl *CompatibilityLayer) HandleEvent(ctx context.Context, event events.Message) error {
	switch event.Action {
	case "start":
		_, err := cl.processContainer(ctx, event.Actor.ID)
		return err
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

// processContainer inspects a container and writes or refreshes its Traefik
// config file. It returns the base name of the config file it wrote, or an
// empty string when the container is skipped (not running, no VIRTUAL_HOST, or
// already managed by native Traefik labels). The returned name lets the initial
// scan know which files must survive reconciliation.
func (cl *CompatibilityLayer) processContainer(ctx context.Context, containerID string) (string, error) {
	inspect, err := utils.RetryContainerInspect(ctx, cl.dockerClient, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	// Extract container information
	containerInfo := cl.extractContainerInfo(inspect)

	// Skip if container is not running
	if !containerInfo.IsRunning {
		cl.logger.Debug("Skipping non-running container",
			"container_id", utils.FormatDockerID(containerID),
			"container_name", containerInfo.Name)
		return "", nil
	}

	// Skip if no VIRTUAL_HOST found
	if containerInfo.VirtualHost == "" {
		cl.logger.Debug("Skipping container without VIRTUAL_HOST",
			"container_id", utils.FormatDockerID(containerID),
			"container_name", containerInfo.Name)
		return "", nil
	}

	// Skip one-off containers created by "docker compose run". They inherit
	// VIRTUAL_HOST from the service definition, so routing them would make a
	// short-lived container compete with the service for the same domain.
	if utils.IsComposeOneOff(inspect.Config.Labels) {
		cl.logger.Info("Skipping one-off compose container",
			"container_id", utils.FormatDockerID(containerID),
			"container_name", containerInfo.Name,
			"virtual_host", containerInfo.VirtualHost)
		return "", nil
	}

	// Skip if traefik labels are already set; native labels take precedence and
	// Traefik's Docker provider handles those containers directly.
	if utils.HasTraefikLabel(inspect.Config.Labels) {
		cl.logger.Debug("Skipping container with existing Traefik label",
			"container_id", utils.FormatDockerID(containerID),
			"container_name", containerInfo.Name)
		return "", nil
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
	if err := cl.writeTraefikConfig(containerID, traefikConfig); err != nil {
		return "", err
	}

	return cl.configFileName(containerID), nil
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
			regexPattern := convertWildcardToRegex(host.hostname)
			if regexPattern == "" {
				cl.logger.Warn("Skipping invalid hostname (potential ReDoS attack)",
					"container_id", utils.FormatDockerID(inspect.ID),
					"hostname", host.hostname)
				continue
			}
			rule = fmt.Sprintf("HostRegexp(`%s`)", regexPattern)
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
	if inspect.NetworkSettings == nil || inspect.NetworkSettings.Networks == nil {
		return ""
	}

	// Sort network names so the chosen IP is deterministic across restarts and
	// events. Go map iteration order is randomized, which would otherwise make
	// the routed backend IP vary for containers attached to multiple networks.
	names := make([]string, 0, len(inspect.NetworkSettings.Networks))
	for name := range inspect.NetworkSettings.Networks {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if ip := inspect.NetworkSettings.Networks[name].IPAddress; ip != "" {
			return ip
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

	// Write atomically using temporary file
	tempFile := configFile + ".tmp"
	if err := os.WriteFile(tempFile, configData, ConfigFilePermissions); err != nil {
		return fmt.Errorf("failed to write temporary config file: %w", err)
	}

	// Atomically rename temporary file to final config file
	if err := os.Rename(tempFile, configFile); err != nil {
		os.Remove(tempFile) // Clean up on failure
		return fmt.Errorf("failed to rename config file: %w", err)
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

// reconcileConfigs removes the config files of containers that no longer exist.
// keep holds the base names of the config files that must survive, one per
// container processed in the current scan. Only files this service owns (those
// matching dinghyConfigFilePattern) are eligible for removal, so certificate,
// middleware, and auto-TLS files sharing the dynamic directory are left alone.
//
// This is what reconciles a recreated container's IP. When the container's die
// event was missed, its old config file survives with a stale backend endpoint;
// the next scan removes it because no current container maps to that file.
func (cl *CompatibilityLayer) reconcileConfigs(keep map[string]struct{}) error {
	entries, err := os.ReadDir(cl.config.TraefikDynamicDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read Traefik dynamic directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !isDinghyConfigFile(name) {
			continue
		}
		if _, ok := keep[name]; ok {
			continue
		}

		if cl.config.DryRun {
			cl.logger.Info("DRY RUN: Would remove orphaned Traefik config", "config_file", name)
			continue
		}

		path := filepath.Join(cl.config.TraefikDynamicDir, name)
		if err := os.Remove(path); err != nil {
			cl.logger.Error("Failed to remove orphaned Traefik config", "error", err, "config_file", name)
			continue
		}

		cl.logger.Info("Removed orphaned Traefik configuration", "config_file", name)
	}

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
	// Validate hostname length to prevent ReDoS attacks
	if len(hostname) > 253 {
		return ""
	}

	// Validate complexity to prevent ReDoS attacks
	// Limit number of wildcards to prevent exponential backtracking
	wildcardCount := strings.Count(hostname, "*")
	if wildcardCount > 5 {
		return ""
	}

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
	// Prefer the lowest exposed TCP port, then fall back to the lowest bound TCP
	// port. Sorting makes the selection deterministic; Go map iteration order is
	// randomized, which would otherwise pick a different port across restarts for
	// containers that expose more than one port.
	var exposed []int
	if inspect.Config.ExposedPorts != nil {
		for port := range inspect.Config.ExposedPorts {
			if n := tcpPortNumber(string(port)); n > 0 {
				exposed = append(exposed, n)
			}
		}
	}
	if port := lowestTCPPort(exposed); port != "" {
		return port
	}

	var bound []int
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Ports != nil {
		for port := range inspect.NetworkSettings.Ports {
			if n := tcpPortNumber(string(port)); n > 0 {
				bound = append(bound, n)
			}
		}
	}
	if port := lowestTCPPort(bound); port != "" {
		return port
	}

	return "80"
}

// lowestTCPPort returns the smallest port in the slice as a string, or "" if empty.
func lowestTCPPort(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	sort.Ints(ports)
	return strconv.Itoa(ports[0])
}

// tcpPortNumber returns the numeric port for a Docker "<n>/tcp" port string,
// or 0 if the string is not a TCP port or cannot be parsed.
func tcpPortNumber(port string) int {
	if !strings.HasSuffix(port, "/tcp") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(port, "/tcp"))
	if err != nil {
		return 0
	}
	return n
}

// configFileName returns the config file name for a container
func (cl *CompatibilityLayer) configFileName(containerID string) string {
	return fmt.Sprintf("%s.yaml", utils.FormatDockerID(containerID))
}

// dinghyConfigFilePattern matches the config file names this service generates:
// the 12-character short container ID from utils.FormatDockerID followed by
// ".yaml". It deliberately excludes the other files that share the Traefik
// dynamic directory, such as the entrypoint-generated auto-tls.yml and the
// middlewares copied in at image build time.
var dinghyConfigFilePattern = regexp.MustCompile(`^[0-9a-f]{12}\.yaml$`)

// isDinghyConfigFile reports whether name is a config file generated by this
// service, and therefore safe for reconciliation to remove.
func isDinghyConfigFile(name string) bool {
	return dinghyConfigFilePattern.MatchString(name)
}
