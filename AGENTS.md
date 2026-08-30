# AGENTS.md

Guidance for agentic coding agents working in this repository.

> `CLAUDE.md` is a symlink to this file. Both Claude Code and other agentic
> harnesses read the same content — edit `AGENTS.md` only.

> **Using this proxy in another project?** A dedicated `spark-http-proxy` agent
> skill covers exposing containers, certificates, DNS, and troubleshooting:
> [sparkfabrik/sf-agents-harness → skills/system/spark-http-proxy](https://github.com/sparkfabrik/sf-agents-harness/tree/main/skills/system/spark-http-proxy).
> This `AGENTS.md` is for working **on** the proxy's own source; the skill is for
> **using** the proxy from a consumer project.

## Repository Overview

Spark HTTP Proxy is a local development reverse proxy built on Traefik. It consists of:

- **`bin/spark-http-proxy`** — Bash CLI wrapper (the user-facing tool)
- **`cmd/`** — Go binaries: `dns-server`, `dinghy-layer`, `join-networks`, `tailscale-peers`
- **`pkg/`** — Shared Go packages (`config`, `logger`, `service`, `utils`)
- **`build/`** — Dockerfiles for each service (traefik, prometheus, grafana, services)
- **`bin/compose.yml`** — Production compose (GHCR pre-built images)
- **`compose.yml`** — Development compose (builds from source)
- **`test/`** — Docker-based integration tests (`test.sh`); unit tests live
  beside the code in `cmd/` and `pkg/`

## Architecture: the big picture

The novel part is not Traefik itself but the four sidecar Go binaries that feed
and surround it. Understanding their interaction requires reading across `cmd/`,
`pkg/`, `compose.yml`, and `build/traefik/`.

### The five runtime services (see `compose.yml`)

1. **`traefik`** (container name `http-proxy`) — the actual proxy. Runs with
   `exposedByDefault: false` (`build/traefik/traefik.yml`), so it ignores every
   container unless explicitly opted in via `traefik.*` labels or a generated
   dynamic-config file. Ports 80/443 public, 30000→8080 dashboard.
2. **`dinghy_layer`** (`cmd/dinghy-layer`) — the compatibility translator.
   Watches Docker events, reads `VIRTUAL_HOST`/`VIRTUAL_PORT`/`VIRTUAL_PATH`
   env vars on containers, and **writes Traefik dynamic YAML config files** into
   the shared `traefik_dynamic` volume. This is how nginx-proxy/jwilder-style
   containers work without native Traefik labels. `VIRTUAL_PATH` mounts a
   container under a path of its `VIRTUAL_HOST`, so two containers can share one
   domain; those routers carry an explicit `priority` while host-only routers
   deliberately do not, keeping their existing rule-length ordering untouched.
3. **`join_networks`** (`cmd/join-networks`) — the connectivity glue. Traefik
   can only route to containers on networks it has joined. This watches Docker
   events and **connects the `http-proxy` container to any Docker network that
   holds a manageable container**. Without it, routes resolve but traffic can't
   reach the backend. See `docs/network-joining-flow.md`.
4. **`tailscale_peers`** (`cmd/tailscale-peers`) — the cross-machine layer, and
   the only optional one: it runs behind the `tailscale` compose profile and does
   nothing unless `HTTP_PROXY_TAILSCALE_ENABLED=true`. It reads the tailnet
   status document from a source (`pkg/tailscale`: the daemon's unix socket, or a
   file the host writes on macOS), probes each machine of the same account for
   its routing table over the Traefik API on port 30000 (`pkg/traefikapi`), and
   **writes `tailscale-peer-*.yaml` into the same `traefik_dynamic` volume**,
   forwarding a hostname no local container serves to the machine that serves it.
   The same-user check lives in `pkg/tailscale` alone and reads no configuration,
   which is what keeps it from depending on the platform. See the
   **Tailnet peer routing** section below.
5. **`dns`** (`cmd/dns-server`) — built on `github.com/miekg/dns`. Resolves
   configured TLDs/domains (default `*.loc`) to `127.0.0.1` so no `/etc/hosts`
   editing is needed. Optionally forwards non-matching queries upstream.
   Listens on UDP+TCP 19322.

### The dynamic-config data flow (the key mechanism)

```
container with VIRTUAL_HOST  ─┐
                              ├─► dinghy_layer ──writes──► traefik_dynamic volume ──watched by──► traefik ──routes──► container
container with traefik.* labels ─────────────────────────(read directly by Docker provider)──────►
                              join_networks ──connects http-proxy to the container's network──►
```

Two independent paths produce Traefik routes: native `traefik.*` labels (read
directly by Traefik's Docker provider) and `VIRTUAL_HOST` env vars (translated
by `dinghy_layer` into files). Both require `join_networks` to have bridged the
network first.

That is the local mechanism. The cross-machine one, `tailscale_peers`, ends in
the same volume and the same file provider, and is diagrammed in the README
under "How the routes get there", alongside the request path that reads what
both of them write.

### Shared Go packages (`pkg/`)

- **`pkg/config`** — env-var loading (`config.go`, all `HTTP_PROXY_DNS_*` vars
  with defaults) **and** the Traefik dynamic-config YAML structs (`traefik.go`:
  `TraefikConfig`/`Router`/`Service`/`TLSConfig`). `dinghy_layer` marshals these
  structs to produce the files Traefik watches.
- **`pkg/service`** — `docker_event_service.go`: the shared Docker-event-watching
  loop (`EventHandler` interface, `RunWithSignalHandling`). Both `dinghy_layer`
  and `join_networks` are `EventHandler` implementations on top of this. Performs
  an initial full scan, then streams events with signal-based graceful shutdown.
- **`pkg/logger`**, **`pkg/utils`** — leveled logging (`LOG_LEVEL`) and helpers.

All four binaries build from the **same `build/Dockerfile`** (multi-stage) and
are selected at runtime by their `command:` in compose.

### Configuration surface

Runtime behaviour is driven by env vars (mostly `HTTP_PROXY_DNS_*` and
`LOG_LEVEL`), defaulted in `pkg/config/config.go` and wired through
`compose.yml`. Per-container routing is driven by
`VIRTUAL_HOST`/`VIRTUAL_PORT`/`VIRTUAL_PATH` or `traefik.*` labels. When adding
an env var, update `pkg/config/config.go`, `compose.yml`, `README.md`, and
`examples/applications.yml` together.

Note that **any** `traefik.` label on a container makes `dinghy_layer` skip it
entirely, `VIRTUAL_HOST` included. That is intentional (native labels win) but
easy to trip over by adding a middleware label alone, so the layer now warns
when it happens.

## Tailnet peer routing

Off by default. The narrative is in the README; these are the constraints that
are easy to break, and the diagram is in the architecture section above.

**The ownership filter is the trust boundary.** `Status.Peers` in
`pkg/tailscale/status.go` accepts a machine only when its `UserID` matches
`Self.UserID`. That package reads no environment at all, deliberately: no setting
can reach the filter. Three tests guard it, and a change that makes ownership
configurable is a change to the capability, not an implementation detail.

**A source produces a document; everything downstream is shared.** Adding a
platform means adding a `tailscale.Source`, never a second filter. A source that
cannot produce a document contributes nothing that cycle: an unreachable daemon
and an empty tailnet are different states and are reported differently.

**A machine is adopted only if it declares itself.** `build/traefik/entrypoint.sh`
writes `spark-http-proxy-declaration.yaml`, a middleware named
`traefikapi.ProxyDeclarationName` that no router references, so it changes no
routing on the machine that publishes it. `Client.Declares` is asked before the
routing table, so a machine that is not this proxy costs one request. The check
fails closed, which means **both machines need this version before anything is
forwarded**. The declaration is written unconditionally, including when peer
routing is disabled: it says what the proxy is, not what it does. Its name must
stay outside the `tailscale-peer-*.yaml` glob, or disabling forwarding on a
machine would also make it undiscoverable by everyone else.

**A peer rule is copied verbatim, so it has to be constrained.** `Routes` in
`pkg/traefikapi` refuses a rule unless every top-level alternative names a host
it is not negating: `Host(`peer.loc`) || PathPrefix(`/`)` would otherwise become
a local router matching every request, and hostname extraction sees only
`peer.loc`, so local precedence would never recognise the shadowing as a
collision. A rule that does not parse confidently is refused for the same reason.
Refusals are logged and reported rather than dropped silently.

**File ownership in the dynamic directory is scoped by prefix.**
`tailscale-peers` removes only `tailscale-peer-*.yaml` and `dinghy-layer` removes
only `^[0-9a-f]{12}\.yaml$`, so neither can delete the other's files or the
entrypoint's `auto-tls.yml`. That prefix is also the loop guard read back from
peers (`traefikapi.PeerRouterPrefix`) and the glob the Traefik entrypoint clears
when the behaviour is disabled. **All three move together.**

**Disabling has to actually disable.** With the profile off the service never
starts, so nothing would clear the files it left behind. The entrypoint removes
them at startup unless `HTTP_PROXY_TAILSCALE_ENABLED=true`, which is why the
Traefik service receives that variable. Startup is too late for `stop-tailscale`,
so the service also withdraws its own routes as it shuts down: stopping peer
routing must stop forwarding without a proxy restart.

**The three timing values are independent on purpose.** The refresh interval
(60 seconds) paces the cycle, `HTTP_PROXY_TAILSCALE_STATUS_MAX_AGE` (10 minutes)
bounds how stale a host-written ownership document may be, and the probe backoff
ceiling (15 minutes) bounds how long a failing machine goes unprobed. The
staleness tolerance was once derived from the interval, which meant slowing the
poll silently loosened a trust input. Keep them separate, and have tests drive
the interval explicitly rather than inheriting the default.

**The compose socket mount lives in `compose.tailscale-socket.yml`.** A platform
without a daemon socket has no path to mount, and a bind mount of a missing path
gets a directory created for it. `bin/spark-http-proxy` passes that file only
when the source is `socket`, and builds its compose invocation as the
`COMPOSE_FILES` array; `COMPOSE_FILE` stays a single path because Compose reads
it as a `:`-separated list.

**The state directory is a trust input**, not a scratch area: the document in it
decides where traffic goes. It is created `0700` and the status document `0600`.

**The command line reports, it does not discover.** The service renders its own
report next to the state file (`Report.Render`), and `spark-http-proxy
tailscale-peers` prints it. Keep it that way: a formatter in shell would be a
second implementation of what a peer contributed, and would need a JSON parser
this project does not depend on.

**A test that names a host path is testing the host.** Point every fixture at
something the test created under its own directory, never at `/var/run`, `~` or
an installed binary: an assertion naming `/var/run/tailscale/tailscaled.sock`
passed on a developer machine running Tailscale and proved nothing, and one
resolving the `tailscale` client found `/usr/bin/tailscale` and never exercised
the refusal it was written for. Both would have passed against code that ignored
their input entirely. Where the fixture has to be a real object, such as a unix
socket, create it: `python3` can bind one, and it outlives the process.

**The integration test uses the file source with a synthetic status document**,
so the ownership filter runs during the test instead of being bypassed by it. A
second Traefik inside the stack stands in for a second machine, with its own
static config listening on port 30000, since inside the stack there is no host
port mapping. Build with the profile active: `docker compose build` skips
profile-gated services, so a plain build leaves `tailscale_peers` on a stale
image and the suite silently tests old code.

## Build Commands

```bash
make build                  # Build the Go binaries
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

Two suites. Unit tests sit beside the code as `_test.go` files; the integration
suite is a single shell script that runs the real stack in Docker.

```bash
go test ./...                   # Unit tests — fast, no Docker
make test                       # Full rebuild + integration tests (NOT unit tests)
./test/test.sh --no-rebuild     # Run integration tests against a running stack (faster)
./test/test.sh --help           # Show test options
docker compose config           # Validate compose file syntax
```

`make test` runs the integration suite only, so run `go test ./...` as well
before pushing. CI runs both.

Tests require:

- Docker daemon running
- Ports 80, 443, 19322 available
- `dig` and `curl` installed (for DNS and HTTP assertions)

There is no way to run a single test in isolation — `test/test.sh` is a monolithic shell script. To iterate on a specific area, use `--no-rebuild` and comment out unrelated test sections temporarily.

## Continuous Integration

`.github/workflows/ci.yml` runs five jobs on every push. A pull request is not
ready while any of them is red.

- **`go-checks`** — `gofmt`, `go vet`, and `go test ./... -race`. Run the same
  three locally before pushing; they are the cheapest gate and catch the most.
- **`test`** — builds the service and Traefik images and runs `test/test.sh`
  against a real stack. This is the only end-to-end coverage the CLI has.
- **`security-scan`** and **Trivy** — dependency and image scanning.
- **`dev-deploy`** and **`deploy`** — build and publish the images.

There is no staging environment: merging to `main` publishes images that every
developer machine pulls on its next `upgrade`.

## Development Environment

```bash
make dev-up             # Start full dev stack (builds from source, basic stack)
make dev-up-metrics     # Start dev stack with Prometheus + Grafana
make dev-down           # Stop dev stack and remove volumes
make dev-cli-traefik    # Open a shell in the Traefik container
```

The dev stack uses `compose.yml` (root) with `build:` contexts. The production stack
uses `bin/compose.yml` with pre-built GHCR images.

## Command safety

Sorted by what a command does to state that cannot be recreated.

**Read freely.** `status`, `show-config`, `tailscale-peers`, `list-certs`,
`logs`, `self-test`.

**Run deliberately.** `start*`, `restart`, `stop-metrics`, `stop-tailscale`,
`upgrade`, `self-update`, `configure-dns`, `generate-mkcert`,
`tailscale-refresh-peers`. Recoverable, but they restart containers, rewrite
system DNS, or install a certificate authority.

**Ask first.** `clean` and `destroy` (both remove volumes, so both take
monitoring data; `destroy` also removes images), `remove-cert`,
`docker compose down -v`, `git push --force`, and any write to
`~/.local/spark/http-proxy/state` — that directory is a trust input rather than
a cache, since its contents decide whose traffic is forwarded where.

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

## Dependency safety

**Check the registry, never memory.** Before adding or upgrading a dependency,
look up what actually exists and take the newest stable release the runtime
supports. For Go that is `https://proxy.golang.org/<module>/@latest`. A version
recalled rather than checked is how a build ends up pinned to something that was
never released.

**Pin what is pulled at build time.** Base images carry readable version tags,
not `latest` and not digests: `renovate.json` extends `config:recommended`,
which bumps tags and leaves digests alone, so a digest would go stale unmaintained.

The Go dependency set is deliberately small; adding to it is worth arguing in the
pull request.

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

## OpenSpec

`openspec/` holds specs under `specs/` and in-flight changes under `changes/`,
on the `spec-driven` schema in `openspec/config.yaml`. Archived changes sit in
`changes/archive/` under their completion date.

**A change needs one** when it spans several files and settles an architectural
question, so the reasoning can be reviewed before the code exists. A bug fix or a
rename does not.

```bash
openspec new change <name>        # scaffold proposal, design, tasks, specs
openspec status --change <name>   # which artifacts are complete
openspec validate <name>          # deltas parse and every requirement has a scenario
openspec archive <name>           # merge deltas into specs/ and file the change
```

Specs describe observable behaviour. Go identifiers, Traefik rule syntax and the
reasoning behind a decision belong in `design.md`, not in a spec.

## Git workflow

**Never push to `main`.** Branch, open a pull request, and let CI run.

Branches are named `<type>/<issue>-<slug>`: `fix/134-cycle-barrier`,
`feat/132-refresh-peers`, `docs/122-agents-sections`.

Pull requests are **squash-merged**, so the squash subject is the history. It
carries the conventional-commit subject, the issue, and the pull request:

```
fix(cli): assert the status agent loaded instead of trusting launchctl #130 (#131)

Closes: sparkfabrik/http-proxy#130
Assisted-by: claude-code/claude-opus-5
```

Footers are fully qualified: a bare `#N` does not resolve outside this
repository. `Closes:` when the commit resolves the issue, `Refs:` otherwise.

## General Guidelines

Source: `.github/instructions/general-coding.instructions.md`

- Never commit secrets or API keys; use environment variables for configuration
- Update `README.md` when adding new features or environment variables
- Document new env vars in both `README.md` and `examples/applications.yml`
- Keep functions small and focused on a single responsibility
- CI runs on every push; `main` branch pushes trigger image builds to GHCR
- Images are published to `ghcr.io/sparkfabrik/http-proxy-{traefik,services,prometheus,grafana}`
- Multi-arch builds target `linux/amd64` and `linux/arm64`
