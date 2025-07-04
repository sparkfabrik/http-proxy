package config

import (
	"os"
	"strconv"
	"strings"
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
		DNSForwardEnabled:  strings.ToLower(GetEnvOrDefault("DNS_FORWARD_ENABLED", "false")) == "true",
		DNSUpstreamServers: GetEnvOrDefaultStringSlice("DNS_UPSTREAM_SERVERS", []string{"8.8.8.8:53", "1.1.1.1:53"}),
	}
}

// GetEnvOrDefault returns the environment variable value or a default if not set
func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetEnvOrDefaultInt returns an environment variable as an integer or a default
func GetEnvOrDefaultInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
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
