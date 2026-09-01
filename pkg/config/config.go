package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds common configuration values used across the application
type Config struct {
	Domains            []string // List of domains/TLDs to handle
	DNSIP              string
	DNSPort            string
	DNSForwardEnabled  bool
	DNSUpstreamServers []string
}

// Load loads configuration from environment variables with defaults
func Load() *Config {
	return &Config{
		Domains:            GetEnvOrDefaultStringSlice("HTTP_PROXY_DNS_TLDS", []string{"loc"}),
		DNSIP:              GetEnvOrDefault("HTTP_PROXY_DNS_TARGET_IP", "127.0.0.1"),
		DNSPort:            GetEnvOrDefault("HTTP_PROXY_DNS_PORT", "19322"),
		DNSForwardEnabled:  strings.ToLower(GetEnvOrDefault("HTTP_PROXY_DNS_FORWARD_ENABLED", "false")) == "true",
		DNSUpstreamServers: GetEnvOrDefaultStringSlice("HTTP_PROXY_DNS_UPSTREAM_SERVERS", []string{"8.8.8.8:53", "1.1.1.1:53"}),
	}
}

// Where the tailnet status document comes from.
const (
	// TailscaleSourceSocket reads the document from the daemon's unix socket.
	TailscaleSourceSocket = "socket"
	// TailscaleSourceFile reads a document the host writes.
	TailscaleSourceFile = "file"
)

// Defaults for tailnet peer routing. The paths are inside the container.
const (
	DefaultTailscaleRefreshInterval = 60 * time.Second
	DefaultTailscaleStatusMaxAge    = 10 * time.Minute
	DefaultTailscaleSocket          = "/var/run/tailscale/tailscaled.sock"
	DefaultTailscaleStatusFile      = "/state/tailscale-status.json"
	DefaultTailscaleStateFile       = "/state/tailscale-peers.json"
	DefaultTailscaleLocalAPIURL     = "http://http-proxy:8080"
	DefaultTraefikDynamicDir        = "/traefik/dynamic"
	DefaultHostsStateFile           = "/state/hosts.tsv"
	DefaultHostsJSONFile            = "/state/hosts.json"
)

// TailscaleConfig holds the configuration of the tailnet peer routing service.
type TailscaleConfig struct {
	Enabled           bool
	Source            string
	RefreshInterval   time.Duration
	StatusMaxAge      time.Duration
	TailscaleSocket   string
	StatusFile        string
	StateFile         string
	LocalAPIURL       string
	TraefikDynamicDir string
}

// LoadTailscale loads the tailnet peer routing configuration.
func LoadTailscale() *TailscaleConfig {
	return &TailscaleConfig{
		Enabled:           strings.ToLower(GetEnvOrDefault("HTTP_PROXY_TAILSCALE_ENABLED", "false")) == "true",
		Source:            strings.ToLower(GetEnvOrDefault("HTTP_PROXY_TAILSCALE_SOURCE", TailscaleSourceSocket)),
		RefreshInterval:   getEnvOrDefaultDuration("HTTP_PROXY_TAILSCALE_REFRESH_INTERVAL", DefaultTailscaleRefreshInterval),
		StatusMaxAge:      getEnvOrDefaultDuration("HTTP_PROXY_TAILSCALE_STATUS_MAX_AGE", DefaultTailscaleStatusMaxAge),
		TailscaleSocket:   GetEnvOrDefault("HTTP_PROXY_TAILSCALE_SOCKET", DefaultTailscaleSocket),
		StatusFile:        GetEnvOrDefault("HTTP_PROXY_TAILSCALE_STATUS_FILE", DefaultTailscaleStatusFile),
		StateFile:         GetEnvOrDefault("HTTP_PROXY_TAILSCALE_STATE_FILE", DefaultTailscaleStateFile),
		LocalAPIURL:       GetEnvOrDefault("HTTP_PROXY_TAILSCALE_LOCAL_API_URL", DefaultTailscaleLocalAPIURL),
		TraefikDynamicDir: GetEnvOrDefault("TRAEFIK_DYNAMIC_DIR", DefaultTraefikDynamicDir),
	}
}

// Validate checks the tailnet peer routing configuration.
func (c *TailscaleConfig) Validate() error {
	if c.Source != TailscaleSourceSocket && c.Source != TailscaleSourceFile {
		return fmt.Errorf("unknown peer source %q, expected %q or %q", c.Source, TailscaleSourceSocket, TailscaleSourceFile)
	}
	return nil
}

// GetEnvOrDefault returns the environment variable value or a default if not set
func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetEnvOrDefaultStringSlice returns an environment variable as a comma-separated slice or a default
func GetEnvOrDefaultStringSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		result := []string{}
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				result = append(result, item)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return defaultValue
}

// getEnvOrDefaultDuration returns an environment variable parsed as a duration,
// or the default when it is unset, unparseable or not positive.
func getEnvOrDefaultDuration(key string, defaultValue time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}
