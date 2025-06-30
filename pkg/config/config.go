package config

import (
	"os"
	"strconv"
	"strings"
)

// DnsServerConfig holds common configuration values used across the application
type DnsServerConfig struct {
	Domains string // Comma-separated list of domains/TLDs to handle
	DNSIP   string
	DNSPort string
}

// Load loads configuration from environment variables with defaults
func Load() *DnsServerConfig {
	return &DnsServerConfig{
		Domains: GetEnvOrDefault("HTTP_PROXY_DNS_TLDS", "loc"),
		DNSIP:   GetEnvOrDefault("HTTP_PROXY_DNS_TARGET_IP", "127.0.0.1"),
		DNSPort: GetEnvOrDefault("HTTP_PROXY_DNS_PORT", "19322"),
	}
}

// SplitDomains splits the comma-separated domains/TLDs string into a slice
func (c *DnsServerConfig) SplitDomains() []string {
	domains := []string{}
	for _, domain := range strings.Split(c.Domains, ",") {
		domain = strings.TrimSpace(domain)
		if domain != "" {
			domains = append(domains, domain)
		}
	}
	return domains
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
