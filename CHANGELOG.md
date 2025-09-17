# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Expose DNS server TCP port 19322 alongside UDP port for Lima virtualization compatibility ([#56](https://github.com/sparkfabrik/http-proxy/issues/56))

### Fixed
- Fixed restart command to automatically start containers when not running instead of failing ([#40](https://github.com/sparkfabrik/http-proxy/issues/40))
  - The `spark-http-proxy restart` command now intelligently detects container state
  - When containers are not running: automatically starts them using existing recreate logic
  - When containers are running: restarts them as before using `docker compose restart`
  - Preserves monitoring detection for both basic and metrics-enabled stacks
- Fixed Docker build issues by removing problematic ca-certificates installation that was causing SSL certificate verification failures in CI environment
- Remove HSTS (HTTP Strict Transport Security) headers from HTTPS responses in development environments to prevent browser caching issues when certificates change or are revoked
- Apply `disable-hsts` middleware at the HTTPS entrypoint level to ensure ALL HTTPS traffic (both dinghy-layer and native Traefik routes) benefits from this development-friendly configuration

### Added
- CHANGELOG.md file to track project changes
