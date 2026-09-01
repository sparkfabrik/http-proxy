// Package main implements a compatibility layer for dinghy-http-proxy that
// translates Docker container environment variables (VIRTUAL_HOST, VIRTUAL_PORT)
// into Traefik dynamic configuration files. This allows containers configured
// for nginx-proxy/jwilder to work seamlessly with Traefik without modification.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

	// claims records which container holds each host-and-path pair, so a second
	// container claiming one already taken can be reported. Detecting this only
	// during the initial scan would miss the ordinary case, where the proxy is
	// already running and a compose stack starts afterwards, so both containers
	// arrive as separate events.
	claims map[routeClaim]claimHolder

	// hosts records what each container serves, keyed by container id.
	hosts map[string][]hostRow
}

// hostRow is one line of the hosts state file.
type hostRow struct {
	hostname  string
	container string
	directory string
	routing   string
}

// hostEntry is hostRow as `hosts --json` publishes it.
type hostEntry struct {
	Hostname  string `json:"hostname"`
	Container string `json:"container"`
	Directory string `json:"directory,omitempty"`
	Routing   string `json:"routing"`
}

// routeClaim identifies a route by what a request has to match to reach it.
// The empty path is the host's own route, so two containers claiming a bare
// host collide on the same key as two claiming the same path.
type routeClaim struct {
	host string
	path string
}

// claimHolder is the container holding a claim. The ID is the identity, since
// a container is recreated with the same name; the name is what a reader needs.
type claimHolder struct {
	id   string
	name string
}

// CompatibilityConfig holds the configuration options for the compatibility layer.
// It controls the behavior of the dinghy compatibility service including dry-run
// mode, logging level, and the directory where Traefik dynamic configuration
// files should be written.
type CompatibilityConfig struct {
	DryRun            bool
	LogLevel          string
	TraefikDynamicDir string
	// HostsStateFile is what `spark-http-proxy hosts` reads. Empty disables it.
	HostsStateFile string
	// HostsJSONFile is the same rows as JSON, for `hosts --json`.
	HostsJSONFile string
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
		claims: make(map[routeClaim]claimHolder),
		hosts:  make(map[string][]hostRow),
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
	VirtualPath string
	IsRunning   bool
	// Directory is where the project runs from, for getting back to its source.
	Directory string
}

// extractContainerInfo extracts relevant information from a container inspection
func (cl *CompatibilityLayer) extractContainerInfo(inspect types.ContainerJSON) ContainerInfo {
	return ContainerInfo{
		ID:          inspect.ID,
		Name:        strings.TrimPrefix(inspect.Name, "/"),
		VirtualHost: utils.GetDockerEnvVar(inspect.Config.Env, "VIRTUAL_HOST"),
		VirtualPort: utils.GetDockerEnvVar(inspect.Config.Env, "VIRTUAL_PORT"),
		VirtualPath: utils.GetDockerEnvVar(inspect.Config.Env, "VIRTUAL_PATH"),
		IsRunning:   inspect.State.Running,
		Directory:   containerDirectory(inspect),
	}
}

// hostMatcherPattern matches a whole Host(...) matcher, which may carry several
// hostnames. quotedHostPattern then takes each one: Traefik quotes them with
// backticks or double quotes.
var (
	hostMatcherPattern = regexp.MustCompile(`Host\(([^)]*)\)`)
	quotedHostPattern  = regexp.MustCompile("[`\"]([^`\"]+)[`\"]")
)

// traefikExposed reports whether Traefik serves this container. The Docker
// provider runs with exposedByDefault false, so a container is routed only when
// it opts in. Traefik parses the label as a Go boolean, so True, TRUE, t and 1
// all enable it, and anything unparseable does not.
func traefikExposed(labels map[string]string) bool {
	enabled, err := strconv.ParseBool(labels["traefik.enable"])
	return err == nil && enabled
}

// labelHostnames are the hostnames a container's own Traefik rules claim, which
// is where routing comes from when native labels are present.
func labelHostnames(labels map[string]string) []string {
	seen := make(map[string]bool)
	hosts := make([]string, 0, 1)

	for key, rule := range labels {
		if !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
			continue
		}
		for _, matcher := range hostMatcherPattern.FindAllStringSubmatch(rule, -1) {
			for _, match := range quotedHostPattern.FindAllStringSubmatch(matcher[1], -1) {
				if !seen[match[1]] {
					seen[match[1]] = true
					hosts = append(hosts, match[1])
				}
			}
		}
	}

	slices.Sort(hosts)
	return hosts
}

// virtualHostNames are the hostnames VIRTUAL_HOST declares.
func virtualHostNames(virtualHost string) []string {
	hosts := make([]string, 0, 1)
	for _, host := range parseVirtualHosts(virtualHost) {
		hosts = append(hosts, host.hostname)
	}
	return hosts
}

// sanitizeField strips the control characters a container could put in a label,
// a name or a bind-mount path. They would otherwise break the tab-separated
// record or reach the reader's terminal as escape sequences.
func sanitizeField(value string) string {
	return strings.Map(func(r rune) rune {
		if r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r < 0x20 && r != '\t') {
			return -1
		}
		if r == '\t' {
			return ' '
		}
		return r
	}, value)
}

// recordHosts replaces what a container contributes, one row per hostname.
func (cl *CompatibilityLayer) recordHosts(containerID string, info ContainerInfo, routing string, hostnames []string) {
	rows := make([]hostRow, 0, len(hostnames))
	for _, hostname := range hostnames {
		if hostname == "" {
			continue
		}
		rows = append(rows, hostRow{
			hostname:  sanitizeField(hostname),
			container: sanitizeField(info.Name),
			directory: sanitizeField(info.Directory),
			routing:   routing,
		})
	}
	if len(rows) == 0 {
		delete(cl.hosts, containerID)
		return
	}
	cl.hosts[containerID] = rows
}

// writeHostsFile rewrites the state file from memory, in a stable order.
func (cl *CompatibilityLayer) writeHostsFile() error {
	rows := make([]hostRow, 0, len(cl.hosts))
	for _, containerRows := range cl.hosts {
		rows = append(rows, containerRows...)
	}
	slices.SortFunc(rows, func(a, b hostRow) int {
		if c := strings.Compare(a.hostname, b.hostname); c != 0 {
			return c
		}
		return strings.Compare(a.container, b.container)
	})

	if cl.config.HostsStateFile != "" {
		var b strings.Builder
		for _, row := range rows {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", row.hostname, row.container, row.directory, row.routing)
		}
		if err := writeFileAtomically(cl.config.HostsStateFile, []byte(b.String())); err != nil {
			return err
		}
	}

	if cl.config.HostsJSONFile == "" {
		return nil
	}

	// Published as JSON too, because `hosts --json` must be parseable and the
	// CLI has no JSON writer.
	entries := make([]hostEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, hostEntry{
			Hostname:  row.hostname,
			Container: row.container,
			Directory: row.directory,
			Routing:   row.routing,
		})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal the hosts entries: %w", err)
	}
	return writeFileAtomically(cl.config.HostsJSONFile, append(data, '\n'))
}

// writeFileAtomically writes through a temporary file and a rename.
func writeFileAtomically(path string, data []byte) error {
	// Created rather than named: the state directory is writable from the host,
	// and a predictable name could already be a symlink to somewhere else.
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create a temporary file beside %s: %w", path, err)
	}
	temp := file.Name()

	if err := file.Chmod(ConfigFilePermissions); err != nil {
		file.Close()
		os.Remove(temp)
		return fmt.Errorf("failed to set the permissions of %s: %w", temp, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(temp)
		return fmt.Errorf("failed to write %s: %w", temp, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return fmt.Errorf("failed to close %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("failed to rename %s: %w", temp, err)
	}
	return nil
}

// containerDirectory: the compose working dir, else the first bind mount.
func containerDirectory(inspect types.ContainerJSON) string {
	if dir := inspect.Config.Labels["com.docker.compose.project.working_dir"]; dir != "" {
		return dir
	}
	for _, m := range inspect.Mounts {
		if m.Type == "bind" && m.Source != "" {
			return m.Source
		}
	}
	return ""
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
	// Written even after inspect failures: the rows are then incomplete, but the
	// alternative is leaving the last process's file naming containers that are
	// gone. Reconciliation still waits for a clean scan, since pruning a live
	// container's config is the worse mistake.
	cl.persistHosts()

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

// persistHosts reports a write failure without failing the event.
func (cl *CompatibilityLayer) persistHosts() {
	if err := cl.writeHostsFile(); err != nil {
		cl.logger.Error("Failed to write the hosts state file", "error", err)
	}
}

// HandleEvent processes a Docker event
func (cl *CompatibilityLayer) HandleEvent(ctx context.Context, event events.Message) error {
	switch event.Action {
	case "start":
		_, err := cl.processContainer(ctx, event.Actor.ID)
		cl.persistHosts()
		return err
	case "die":
		delete(cl.hosts, event.Actor.ID)
		cl.persistHosts()
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
		HostsStateFile:    config.GetEnvOrDefault("HOSTS_STATE_FILE", config.DefaultHostsStateFile),
		HostsJSONFile:     config.GetEnvOrDefault("HOSTS_JSON_FILE", config.DefaultHostsJSONFile),
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

	// Skip if no VIRTUAL_HOST found. A container asking for a path without a
	// host is a mistake rather than a container that opted out, so it is worth
	// saying out loud: a path needs a hostname to sit on, and without
	// VIRTUAL_HOST nothing exposes the container at all.
	if containerInfo.VirtualHost == "" {
		if containerInfo.VirtualPath != "" {
			cl.logger.Warn("Ignoring VIRTUAL_PATH on a container without VIRTUAL_HOST",
				"container_id", utils.FormatDockerID(containerID),
				"container_name", containerInfo.Name,
				"virtual_path", containerInfo.VirtualPath)
			return "", nil
		}
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
	// Traefik's Docker provider handles those containers directly. Any
	// traefik. label counts, including a middleware that was never meant to
	// take over routing, so a container that also declares VIRTUAL_HOST loses
	// it. That is easy to hit when reaching for a middleware alongside a
	// mounted path, and silent at debug level, so say it plainly instead.
	if utils.HasTraefikLabel(inspect.Config.Labels) {
		// Recorded though Traefik routes it, so hosts shows the whole picture.
		// Always recorded, with nothing when the container is not exposed, so
		// turning traefik.enable off clears the rows it had.
		var served []string
		if traefikExposed(inspect.Config.Labels) {
			served = labelHostnames(inspect.Config.Labels)
		}
		cl.recordHosts(containerID, containerInfo, "traefik-labels", served)
		if containerInfo.VirtualHost != "" {
			cl.logger.Warn("Ignoring VIRTUAL_HOST and VIRTUAL_PATH on a container carrying a traefik. label",
				"container_id", utils.FormatDockerID(containerID),
				"container_name", containerInfo.Name,
				"virtual_host", containerInfo.VirtualHost,
				"virtual_path", containerInfo.VirtualPath,
				"hint", "declare the routers as labels too, or remove the label")
			return "", nil
		}
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

	cl.recordHosts(containerID, containerInfo, "virtual-host", virtualHostNames(containerInfo.VirtualHost))

	return cl.configFileName(containerID), nil
}

func (cl *CompatibilityLayer) generateTraefikConfig(inspect types.ContainerJSON, containerInfo ContainerInfo) *config.TraefikConfig {
	traefikConfig := config.NewTraefikConfig()

	// Generate service name from container name
	serviceName := generateServiceName(inspect.Name)

	// Parse VIRTUAL_HOST (can contain multiple hosts separated by commas)
	hosts := parseVirtualHosts(containerInfo.VirtualHost)

	// VIRTUAL_PATH belongs to the container, not to an individual host entry,
	// so a container naming several hosts is mounted at the same path on each.
	// An unusable value is reported and dropped: the container still gets its
	// host routes rather than disappearing from the proxy entirely.
	virtualPath, err := normalizeVirtualPath(containerInfo.VirtualPath)
	if err != nil {
		cl.logger.Warn("Ignoring VIRTUAL_PATH",
			"container_id", utils.FormatDockerID(inspect.ID),
			"container_name", containerInfo.Name,
			"virtual_path", containerInfo.VirtualPath,
			"reason", err)
	}

	// Get container IP address
	containerIP := getContainerIP(inspect)
	if containerIP == "" {
		cl.logger.Error("Could not determine container IP", "container_id", utils.FormatDockerID(inspect.ID))
		return traefikConfig
	}

	// Recorded only once the container can actually be routed to, so one with
	// no address does not hold a route it cannot serve.
	cl.recordClaims(containerInfo, hosts, virtualPath)

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

		// A mounted path narrows the rule and takes an explicit priority. Host
		// routers keep taking Traefik's rule-length ordering, so containers
		// that do not use VIRTUAL_PATH rank exactly as they always have.
		priority := 0
		if virtualPath != "" {
			rule = fmt.Sprintf("%s && %s", rule, pathMatcher(virtualPath))
			priority = pathPriority(virtualPath)
		}

		// Create HTTP router
		httpRouter := &config.Router{
			Rule:        rule,
			Service:     serviceName,
			Priority:    priority,
			EntryPoints: []string{"http"},
		}
		traefikConfig.HTTP.Routers[routerName] = httpRouter

		// Create HTTPS router (always created now)
		httpsRouterName := fmt.Sprintf("%s-tls-%d", serviceName, i)
		httpsRouter := &config.Router{
			Rule:        rule,
			Service:     serviceName,
			Priority:    priority,
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
	// Free the routes first, so a replacement container taking the same host
	// and path is not reported as colliding with the one it replaced.
	cl.releaseClaims(containerID)

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

// pathPriorityBase lifts every mounted-path router above the rule-length
// ordering Traefik applies when no priority is set. Rule length is bounded by
// how long a hostname and a path can plausibly be, so a base well past that
// keeps a mounted path ahead of a bare host and of a wildcard that happens to
// produce a long regex.
const pathPriorityBase = 10000

// normalizeVirtualPath validates VIRTUAL_PATH and returns the form used to
// build a rule: a leading separator, no trailing one, and empty when the
// container is not mounted under a path at all.
//
// The empty return with a non-nil error is deliberate. A container whose path
// cannot be used still deserves its host routes, so callers report the error
// and carry on with the empty value.
func normalizeVirtualPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}

	// A list is the mistake VIRTUAL_HOST's own syntax invites. Commas are legal
	// in a URL path, so accepting one would build a rule that never matches.
	if strings.Contains(path, ",") {
		return "", fmt.Errorf("must be a single path, not a list")
	}

	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("must start with /")
	}

	// The rule is assembled by string formatting, so a backtick would close the
	// matcher and let the rest of the value become routing syntax. The other
	// characters here cannot appear in a path Traefik matches against.
	if strings.ContainsAny(path, "`$\"'\\ \t\n\r?#") {
		return "", fmt.Errorf("contains characters that are not allowed in a path")
	}

	// Trailing separators are stripped so /api and /api/ are one declaration.
	// Repeated separators would produce a path no request can match.
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		// VIRTUAL_PATH=/ addresses the whole host, which VIRTUAL_HOST already
		// does. Treated as absent rather than as a second route for the host.
		return "", nil
	}
	if strings.Contains(trimmed, "//") {
		return "", fmt.Errorf("contains an empty path segment")
	}

	return trimmed, nil
}

// pathMatcher builds the matcher for a mounted path.
//
// PathPrefix alone is a raw string prefix in Traefik, so PathPrefix(`/api`)
// also matches /api-docs. An Ingress path prefix splits on separators and does
// not, and the point of mounting a path locally is that one relative call
// behaves the same in both places. Pairing the prefix with an exact match
// restores segment-aware behaviour.
func pathMatcher(path string) string {
	return fmt.Sprintf("(PathPrefix(`%s/`) || Path(`%s`))", path, path)
}

// pathPriority ranks a mounted path above the host it sits on, and a longer
// path above a shorter one on the same host, so /api/internal wins over /api.
func pathPriority(path string) int {
	return pathPriorityBase + len(path)
}

// recordClaims registers the routes a container is about to be given and
// reports any that another container already holds. Which of the two answers
// such a request is decided by Traefik's own ordering of identical rules, so
// the only useful thing to do is name both and let a human resolve it.
func (cl *CompatibilityLayer) recordClaims(info ContainerInfo, hosts []virtualHost, path string) {
	cl.releaseClaims(info.ID)

	for _, host := range hosts {
		claim := routeClaim{host: host.hostname, path: path}
		if holder, taken := cl.claims[claim]; taken && holder.id != info.ID {
			cl.logger.Warn("Two containers claim the same route",
				"host", claim.host,
				"path", claimPathForLog(claim.path),
				"containers", fmt.Sprintf("%s, %s", holder.name, info.Name),
				"hint", "which one answers is not defined; give one of them a different host or path")
			continue
		}
		cl.claims[claim] = claimHolder{id: info.ID, name: info.Name}
	}
}

// releaseClaims drops everything a container holds, so a route freed by one
// container stopping can be taken by another without a false collision.
func (cl *CompatibilityLayer) releaseClaims(containerID string) {
	for claim, holder := range cl.claims {
		if holder.id == containerID {
			delete(cl.claims, claim)
		}
	}
}

// claimPathForLog renders the host's own route, which has no path, as something
// readable rather than as an empty value.
func claimPathForLog(path string) string {
	if path == "" {
		return "/"
	}
	return path
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
