# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Unit tests for the pure parsing/config helpers in `dinghy-layer`, `dns-server`, `config`, and `utils` ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- CI `go-checks` job running `gofmt`, `go vet`, and `go test -race` on every non-`main` branch ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Expose DNS server TCP port 19322 alongside UDP port for Lima virtualization compatibility ([#56](https://github.com/sparkfabrik/http-proxy/issues/56))
- Add `upgrade` command to pull latest Docker images and recreate only changed containers, preserving volumes (grafana/prometheus data) ([#96](https://github.com/sparkfabrik/http-proxy/pull/96))
- Add `self-update` command to update the script and compose files from the git repository, with guards against non-git installs and dirty working trees ([#96](https://github.com/sparkfabrik/http-proxy/pull/96))

### Fixed

- Make backend IP and port selection deterministic for `VIRTUAL_HOST` containers attached to multiple networks or exposing multiple ports; previously Go map iteration could route to a different network IP or port across restarts ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Lower generated DNS A-record TTL from 3600s to 60s so a changed `HTTP_PROXY_DNS_TARGET_IP` propagates quickly instead of being cached by the OS stub resolver ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Guard against a nil-pointer panic in `join-networks` when a container reports no network settings ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Make signal-driven shutdown deterministic in the event-driven services by giving a single owner control of signal handling, and abort the event-stream reconnect backoff promptly on shutdown ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Reconnect to the Docker event stream when the daemon closes it (for example on daemon restart) instead of busy-looping on the closed channel ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Reject a non-IPv4 `HTTP_PROXY_DNS_TARGET_IP` at startup; the DNS server answers only A records, so an IPv6 target would otherwise be silently truncated ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
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
