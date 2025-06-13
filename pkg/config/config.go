package config

import (
	"os"
	"strconv"
)

// Config holds common configuration values used across the application
type Config struct {
	DomainTLD string
	DNSIP     string
	DNSPort   string
}

// Load loads configuration from environment variables with defaults
func Load() *Config {
	return &Config{
		DomainTLD: getEnvOrDefault("DOMAIN_TLD", "loc"),
		DNSIP:     getEnvOrDefault("DNS_IP", "127.0.0.1"),
		DNSPort:   getEnvOrDefault("DNS_PORT", "19322"),
	}
}

// getEnvOrDefault returns the environment variable value or a default if not set
func getEnvOrDefault(key, defaultValue string) string {
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
