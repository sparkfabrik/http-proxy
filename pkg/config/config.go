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

// Where the tailnet status document comes from. The document, and therefore
// the filter applied to it, is the same in both modes; only the transport
// differs. Every cycle that forwards to a machine has run the ownership filter
// over a document, and there is no mode in which it does not.
const (
	// TailscaleSourceSocket reads the document from the daemon's unix socket.
	TailscaleSourceSocket = "socket"
	// TailscaleSourceFile reads a document the host writes, for a platform whose
	// daemon exposes no socket a container can reach.
	TailscaleSourceFile = "file"
)

// Defaults for tailnet peer routing. The socket path is where the Tailscale daemon
// listens on Linux; the state file, the status file and the dynamic directory
// are container paths, all bind-mounted.
const (
	DefaultTailscaleRefreshInterval = 10 * time.Second
	DefaultTailscaleSocket          = "/var/run/tailscale/tailscaled.sock"
	DefaultTailscaleStatusFile      = "/state/tailscale-status.json"
	DefaultTailscaleStateFile       = "/state/tailscale-peers.json"
	DefaultTailscaleLocalAPIURL     = "http://http-proxy:8080"
	DefaultTraefikDynamicDir        = "/traefik/dynamic"
)

// PeerConfig holds the configuration of the peer discovery service.
//
// There is deliberately no setting for which machines may be probed: a machine
// belonging to another user is excluded by the discovery filter, which reads no
// configuration at all.
type TailscaleConfig struct {
	Enabled           bool
	Source            string
	RefreshInterval   time.Duration
	TailscaleSocket   string
	StatusFile        string
	StateFile         string
	LocalAPIURL       string
	TraefikDynamicDir string
}

// LoadTailscale loads the tailnet peer routing configuration from environment
// variables.
func LoadTailscale() *TailscaleConfig {
	return &TailscaleConfig{
		Enabled:           strings.ToLower(GetEnvOrDefault("HTTP_PROXY_TAILSCALE_ENABLED", "false")) == "true",
		Source:            strings.ToLower(GetEnvOrDefault("HTTP_PROXY_TAILSCALE_SOURCE", TailscaleSourceSocket)),
		RefreshInterval:   getEnvOrDefaultDuration("HTTP_PROXY_TAILSCALE_REFRESH_INTERVAL", DefaultTailscaleRefreshInterval),
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

// getEnvOrDefaultDuration returns an environment variable parsed as a duration.
// An unparseable or non-positive value falls back to the default rather than
// stopping the service, so a typo slows discovery down to its usual pace
// instead of leaving the proxy without peer routes.
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
