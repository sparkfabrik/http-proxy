package config

import (
	"os"
	"strconv"
)

// Config holds common configuration values used across the application
type Config struct {
	DomainTLD         string
	DNSIP             string
	DNSPort           string
	DNSForwardEnabled bool
}

// Load loads configuration from environment variables with defaults
func Load() *Config {
	return &Config{
		DomainTLD:         GetEnvOrDefault("DOMAIN_TLD", "loc"),
		DNSIP:             GetEnvOrDefault("DNS_IP", "127.0.0.1"),
		DNSPort:           GetEnvOrDefault("DNS_PORT", "19322"),
		DNSForwardEnabled: strings.ToLower(GetEnvOrDefault("DNS_FORWARD_ENABLED", "false")) == "true",
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
