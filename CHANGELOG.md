# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Support `VIRTUAL_PATH` to mount a container under a path of its `VIRTUAL_HOST`, so a browser-served frontend and its API can share one origin locally with no CORS, no preflight and one certificate. Matching is by path segment, so `/api` never captures `/api-docs`; nothing is stripped, so the backend receives the prefix it was mounted at; the mounted routers carry an explicit priority while host-only routers keep the ordering they have always had ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `self-test` also checks a mounted path, over HTTP and HTTPS, comparing the response body rather than the status code: a missing path route falls through to the container serving the hostname, which answers `200` and would otherwise hide the failure ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- OpenSpec change tracking under `openspec/`, starting with the specification behind `VIRTUAL_PATH` ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Add `list-certs` command to list installed certificates and `remove-cert` command to remove certificate pairs for one or more domains and restart Traefik ([#107](https://github.com/sparkfabrik/http-proxy/issues/107))
- Unit tests for the pure parsing/config helpers in `dinghy-layer`, `dns-server`, `config`, and `utils` ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- CI `go-checks` job running `gofmt`, `go vet`, and `go test -race` on every non-`main` branch ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Expose DNS server TCP port 19322 alongside UDP port for Lima virtualization compatibility ([#56](https://github.com/sparkfabrik/http-proxy/issues/56))
- Add `upgrade` command to pull latest Docker images and recreate only changed containers, preserving volumes (grafana/prometheus data) ([#96](https://github.com/sparkfabrik/http-proxy/pull/96))
- Add `self-update` command to update the script and compose files from the git repository, with guards against non-git installs and dirty working trees ([#96](https://github.com/sparkfabrik/http-proxy/pull/96))

### Changed

- Warn instead of staying silent when a container's routing variables are ignored: when it carries any `traefik.` label, which makes the layer skip it entirely, and when `VIRTUAL_PATH` is set without `VIRTUAL_HOST`. Both were debug-level, so invisible at the default log level ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Warn when two containers claim the same host and path, naming both, since which of them answers is otherwise arbitrary. Detected across container events, not only at startup ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `generate-mkcert` and `remove-cert` reject an argument containing a path, pointing at the hostname instead. A certificate covers a hostname, and a container mounted under a path is served by its host's certificate ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `self-test` now verifies end-to-end routing instead of only DNS liveness: it starts a throwaway container with `VIRTUAL_HOST`, asserts DNS resolves the test domain to the configured target IP, and that the proxy serves it over both HTTP and HTTPS (with retries while routes propagate), then cleans up. Exits non-zero with a per-check report on failure ([#104](https://github.com/sparkfabrik/http-proxy/issues/104))

### Fixed

- `self-test` no longer aborts on the first failed check. Its probes returned non-zero from inside a command substitution, which under `set -e` ended the script before it could report which check failed or remove the containers it had started ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Fix the CORS example in `examples/applications.yml`, which could never have worked: its `traefik.` middleware label made the layer skip the container, so its `VIRTUAL_HOST` was ignored and no route existed. It now declares its routers as labels too ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Correct `AGENTS.md`, which stated that the repository has no unit tests and that `make test` is the verification step. Unit tests exist beside the code, and `make test` runs only the integration suite ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Ignore one-off containers created by `docker compose run` (labelled `com.docker.compose.oneoff=True`) in `dinghy-layer` and `join-networks`; they inherit `VIRTUAL_HOST` from the service definition and could claim the service domain with a backend port nothing listens on, returning `502` ([#111](https://github.com/sparkfabrik/http-proxy/issues/111))
- Reconcile the generated `dinghy-layer` config files against running containers during the initial scan, removing orphaned files whose container no longer exists; a recreated container previously kept a stale backend IP that returned `502` until a full teardown ([#109](https://github.com/sparkfabrik/http-proxy/issues/109))
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
