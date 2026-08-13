## Why

A container can only ask for a whole hostname. There is no way to be routed on a path of a hostname another container already serves, so a browser-served frontend and its API cannot share one origin locally.

That matters because sharing an origin is what removes CORS, preflight requests and a second credential. A deployed environment gets this from an Ingress sending a path prefix to a different Service. Locally the same application code has to reach for an environment-dependent API address, or each framework's development server grows a hand-written proxy rule that duplicates the deployed routing and drifts from it.

## What Changes

**`VIRTUAL_PATH` mounts a container under a path of its `VIRTUAL_HOST`.** Both containers name the same hostname; the one being mounted under a path adds `VIRTUAL_PATH=/api`. This is the variable [nginx-proxy uses for the same purpose](https://github.com/nginx-proxy/nginx-proxy/blob/main/docs/README.md), and this layer exists to be compatible with that style of container.

**The path matcher is segment-aware.** Traefik's `PathPrefix` is a raw string prefix, so `PathPrefix(/api)` also matches `/api-docs`. An Ingress `pathType: Prefix` splits on separators and does not. The generated rule pairs `PathPrefix` with `Path` so a path never captures a sibling that merely starts with the same characters.

**Only the new routes carry an explicit priority.** Traefik orders routers by rule length when priority is unset, across providers, in one table per entrypoint. Setting a priority on the hostname routers this layer already emits would re-rank them against routers built from users' own labels, so they are left exactly as they are and only the routes a path produces are ranked explicitly.

**Nothing is stripped.** The backend receives the request path unchanged, matching both an Ingress and nginx-proxy's own default.

**`VIRTUAL_HOST` parsing is untouched**, so no existing configuration changes behaviour and each container keeps its own `VIRTUAL_PORT`.

**Declarations that cannot work are reported at a level users see**, including two that are silent today: a container carrying a `traefik.` label, which is skipped whole, and a path with no hostname to sit on.

## Capabilities

### New Capabilities

- `virtual-path-routing`: a container is routed on a path of a hostname another container serves, through the same environment-variable interface used to expose a whole hostname.

### Modified Capabilities

None. This is the first change tracked here, so there are no existing specs to modify. The behaviour of containers that do not set `VIRTUAL_PATH` is deliberately unchanged, which is why priority is set on the new routes only.

## Impact

- `cmd/dinghy-layer`: reads the new variable, validates it, builds the path rule, sets priority on the routers it produces, and keeps enough state across events to spot two containers claiming one path.
- `pkg/config`: the router struct gains the priority field it currently lacks, serialising with a non-zero floor so it cannot be dropped.
- `pkg/utils`: unchanged. A container using a path still sets `VIRTUAL_HOST`, so the check deciding whether a container is managed keeps working, and network joining is unaffected.
- `bin/spark-http-proxy`: certificate commands reject an argument containing a path, and the self-diagnosis command checks a mounted path by response content, since a missing path route falls through to the hostname's container and still answers `200`.
- `cmd/dinghy-layer/main_test.go`: cases for every accepted and rejected form, the rule and the emitted router asserted whole, and the precedence ordering pinned.
- `test/test.sh`: containers sharing a hostname, each route proved by distinct content, plus the wildcard, sibling-prefix and container-stops cases. Its container names, creation, waits, counters, printed results and cleanup are all maintained by hand and need updating together.
- `README.md`: the variable in the supported patterns, in the configuration example, and in the compatibility notes, where it is attributed to nginx-proxy rather than to the dinghy proxy that table describes, and where `VIRTUAL_DEST` is recorded as unsupported.
- `examples/`: a runnable two-container example, and a fix to the existing example that is dead today because a middleware label suppresses its `VIRTUAL_HOST`.
- `AGENTS.md`: the new variable, plus corrections to its claims that the repository has no unit tests and that the make target runs them.
- `CHANGELOG.md`: an entry, as the contributor guide requires.
- The agent skill for this proxy, which `AGENTS.md` points at for consumers. It lives in a separate repository, so it is a separate pull request, raised once this change is released.
- Existing users: no action. The variable is new, and configurations that do not set it are routed exactly as before.
