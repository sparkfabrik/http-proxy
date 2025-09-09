# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Fixed restart command to automatically start containers when not running instead of failing ([#40](https://github.com/sparkfabrik/http-proxy/issues/40))
  - The `spark-http-proxy restart` command now intelligently detects container state
  - When containers are not running: automatically starts them using existing recreate logic
  - When containers are running: restarts them as before using `docker compose restart`
  - Preserves monitoring detection for both basic and metrics-enabled stacks
- Fixed Docker build issues by removing problematic ca-certificates installation that was causing SSL certificate verification failures in CI environment