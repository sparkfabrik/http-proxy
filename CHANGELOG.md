# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Remove HSTS (HTTP Strict Transport Security) headers from HTTPS responses in development environments to prevent browser caching issues when certificates change or are revoked
- Apply `disable-hsts` middleware at the HTTPS entrypoint level to ensure ALL HTTPS traffic (both dinghy-layer and native Traefik routes) benefits from this development-friendly configuration

### Added
- CHANGELOG.md file to track project changes