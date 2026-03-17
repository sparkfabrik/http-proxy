# AGENTS.md

Guidance for agentic coding agents working in this repository.

## Repository Overview

Spark HTTP Proxy is a local development reverse proxy built on Traefik. It consists of:

- **`bin/spark-http-proxy`** — Bash CLI wrapper (the user-facing tool)
- **`cmd/`** — Go binaries: `dns-server`, `dinghy-layer`, `join-networks`
- **`pkg/`** — Shared Go packages (`config`, `logger`, `service`, `utils`)
- **`build/`** — Dockerfiles for each service (traefik, prometheus, grafana, services)
- **`bin/compose.yml`** — Production compose (GHCR pre-built images)
- **`compose.yml`** — Development compose (builds from source)
- **`test/`** — Integration tests only (no unit tests exist)

## Build Commands

```bash
make build                  # Build all three Go binaries
make build-go-dns           # Build cmd/dns-server only
make build-go-dinghy-layer  # Build cmd/dinghy-layer only
make build-go-join-networks # Build cmd/join-networks only
make clean                  # Remove build artifacts from cmd/*/
go build ./...              # Quick compilation check (no output binaries)
go mod tidy                 # Clean up go.mod / go.sum
```

After building binaries for manual testing, **remove them** before committing:

```bash
rm -f cmd/dns-server/dns-server cmd/dinghy-layer/dinghy-layer cmd/join-networks/join-networks
```

## Test Commands

There are **no unit tests**. All tests are Docker-based integration tests.

```bash
make test                       # Full rebuild + integration tests
./test/test.sh --no-rebuild     # Run tests against an already-running stack (faster)
./test/test.sh --help           # Show test options
docker compose config           # Validate compose file syntax
```

Tests require:
- Docker daemon running
- Ports 80, 443, 19322 available
- `dig` and `curl` installed (for DNS and HTTP assertions)

There is no way to run a single test in isolation — `test/test.sh` is a monolithic shell script. To iterate on a specific area, use `--no-rebuild` and comment out unrelated test sections temporarily.

## Development Environment

```bash
make dev-up             # Start full dev stack (builds from source, basic stack)
make dev-up-metrics     # Start dev stack with Prometheus + Grafana
make dev-down           # Stop dev stack and remove volumes
make dev-cli-traefik    # Open a shell in the Traefik container
```

The dev stack uses `compose.yml` (root) with `build:` contexts. The production stack
uses `bin/compose.yml` with pre-built GHCR images.

## Lint and Format

```bash
gofmt -l ./cmd ./pkg        # List files needing formatting
gofmt -w ./cmd ./pkg        # Format in place
go vet ./...                # Check for suspicious constructs
goimports -w ./cmd ./pkg    # Fix imports (install: go install golang.org/x/tools/cmd/goimports@latest)
```

No linter config file exists. Use `golangci-lint` if available, otherwise `go vet` is the baseline.
CI runs `make test` and a Trivy security scan — no separate lint step in CI.

## Go Code Style

Source: `.github/instructions/go.instructions.md` (Effective Go + Google Go Style).

- Format with `gofmt` / `goimports` always — no exceptions
- Use `camelCase` for unexported, `PascalCase` for exported names
- Package names: lowercase, single word, no underscores (e.g. `config`, `logger`)
- Interface names use `-er` suffix when possible (`Reader`, `Writer`)
- Keep the happy path left-aligned; return early to reduce nesting
- Error handling: check immediately, wrap with `fmt.Errorf("context: %w", err)`, never
  log and return — choose one
- Error messages: lowercase, no trailing punctuation
- Place `main` packages in `cmd/`, shared code in `pkg/`
- Keep interfaces small (1–3 methods); define them near the consumer, not the implementor
- Accept interfaces, return concrete types
- Use `defer` for cleanup; always know how a goroutine will exit
- After any implementation, run `make test` to verify nothing is broken

## Bash Script Style (`bin/spark-http-proxy`)

The CLI is a single Bash script. Follow the conventions already established in it:

- `set -e` at the top — every new function must be safe to run under errexit
- Logging via the four helpers: `log_info`, `log_success`, `log_error`, `log_warning`
- Local variables declared with `local` at the top of every function
- Docker Compose via `dc_cmd` and `dc_metrics` helpers — never call `docker compose` directly
- Use `docker compose` (plugin form), never the legacy `docker-compose`
- New commands must be added in **all four places**:
  1. Function definition (before `show_version`)
  2. `case` dispatch block (before the `*` catch-all)
  3. `show_usage` help text
  4. `generate_completion` commands string
- Commands that do not need Docker (e.g. pure git or config ops) must be added to the
  prerequisite skip list near line 326

## Docker / Dockerfile Style

Source: `.github/instructions/docker.instructions.md`

- Use multi-stage builds (see `build/Dockerfile` as the reference)
- Use specific base image versions — never `latest` in Dockerfiles
- Use minimal base images (`alpine`, `distroless`)
- Run containers as non-root users where possible
- Use `COPY` over `ADD` unless `ADD` features are needed
- Set explicit `WORKDIR`
- Use `docker compose` (not `docker-compose`) in all scripts and Make targets

## Makefile Style

Source: `.github/instructions/makefile.instructions.md`

- All targets in `.PHONY` if they don't produce files
- Every target has a `## Description` comment for the `help` target
- `UPPERCASE` for variable names
- Group related targets logically (build, dev, test, clean)

## Changelog

- Keep `CHANGELOG.md` updated for every user-visible change
- Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)
- Add entries under `[Unreleased]` in the appropriate section (`Added`, `Fixed`, `Changed`)
- Link entries to the relevant PR: `([#N](https://github.com/sparkfabrik/http-proxy/pull/N))`

## General Guidelines

Source: `.github/instructions/general-coding.instructions.md`

- Never commit secrets or API keys; use environment variables for configuration
- Update `README.md` when adding new features or environment variables
- Document new env vars in both `README.md` and `examples/applications.yml`
- Keep functions small and focused on a single responsibility
- CI runs on every push; `main` branch pushes trigger image builds to GHCR
- Images are published to `ghcr.io/sparkfabrik/http-proxy-{traefik,services,prometheus,grafana}`
- Multi-arch builds target `linux/amd64` and `linux/arm64`
