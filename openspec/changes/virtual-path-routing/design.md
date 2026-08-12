## Context

`dinghy_layer` watches Docker events and translates `VIRTUAL_HOST` and `VIRTUAL_PORT` into Traefik dynamic configuration files. Per container it emits one router per hostname entry, a TLS twin of each, and one service.

Four properties of the existing code shape the decisions below.

**A container has one backend port.** `getEffectivePort` runs once per container and returns the first entry naming a port, applied to the single service the container gets. `VIRTUAL_HOST=a.loc:3000,b.loc:4000` already routes both hostnames to 3000. Pre-existing and not changed here.

**A hostname entry can be a plain name, a wildcard, or a `~`-prefixed regex** handed to Traefik as-is.

**A container carrying any `traefik.` label is skipped entirely.** `HasTraefikLabel` matches on the prefix, so a middleware-only label suppresses `VIRTUAL_HOST` too. `examples/applications.yml` Example 7 is broken today for exactly this reason.

**No router this layer emits sets a priority.** Traefik therefore orders them all by rule length, together with every router its Docker provider builds from user labels, in one table per entrypoint.

## Goals / Non-Goals

**Goals:**

- A container is reachable on a path of a hostname another container serves.
- The path behaves as a path prefix does in a deployed environment, so one relative call works in both places.
- Which container answers a request is unambiguous among the routes this layer generates.
- A declaration that cannot work is reported where a user will see it.

**Non-Goals:**

- Rewriting or stripping the path before the backend sees it.
- Per-entry backend ports. The one-port-per-container behaviour is documented, not changed.
- Changing how any container without `VIRTUAL_PATH` is routed.
- Replacing native routing labels, which can express all of this and remain available.

## Decisions

**A separate variable, not a path inside the hostname.**

`VIRTUAL_PATH` is what `nginx-proxy` uses for this. Beyond precedent it avoids three problems: the hostname parser is untouched, so a `~` regex cannot have a slash reinterpreted as a path; each container keeps its own `VIRTUAL_PORT`, so nothing implies per-entry ports the single service cannot deliver; and it is a variable the current code does not read, so no existing configuration can change behaviour.

The alternative, `VIRTUAL_HOST=host/path`, was considered first and rejected on those three counts.

Note for the compatibility documentation: `VIRTUAL_PATH` comes from `nginx-proxy`, not from `codekitchen/dinghy-http-proxy`, which never had it. The existing table describes the latter, so the new variable needs its own note rather than a row implying dinghy supported it.

**The matcher pairs a prefix with an exact match.**

Traefik's `PathPrefix` is a raw string prefix: `PathPrefix(/api)` also matches `/api-docs`. A deployed `pathType: Prefix` splits on separators and does not. Since the point of the feature is that one relative call behaves the same in both places, the local matcher has to agree:

```
Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))
```

A trailing separator is normalised away before the rule is built, so `/api` and `/api/` are one declaration.

**Priority is set on path routers only, and on nothing else.**

This is the decision that keeps the change additive. Setting a priority on the existing hostname routers would re-rank them against every router on the machine, including ones built from user labels by a different provider, since Traefik sorts a single table per entrypoint. That is a behaviour change for people who never asked for this feature, and it would replace today's accidental-but-stable ordering between exact and wildcard hosts with an undefined tie.

So hostname routers keep inheriting rule-length ordering exactly as they do now, and only the routers a `VIRTUAL_PATH` produces carry an explicit value. That value sits far above any rule-length default, which is bounded by how long a rule string can plausibly be, and grows with the path's length so a longer path outranks a shorter one on the same hostname. Both the HTTP and HTTPS routers of one path get the same value.

Two consequences worth stating. The field must serialise: a zero value with `omitempty` would be dropped and silently restore inherited ordering, so the chosen floor is non-zero and the test asserts the emitted configuration rather than the struct. And a user who sets a higher priority on their own label wins, deliberately, which is why the requirement is scoped to the routes this layer generates.

**Nothing is stripped.**

The backend receives the path as sent. This matches a deployed Ingress and `nginx-proxy`'s own default, where the companion `VIRTUAL_DEST` is empty. `VIRTUAL_DEST` is out of scope; stripping can be added later behind it, un-stripping could not.

**A path applies to every hostname the container declares.**

`VIRTUAL_PATH` belongs to the container, as `VIRTUAL_PORT` does. A container naming two hostnames and one path is mounted at that path on both. The alternative, aligning a list of paths positionally with a list of hostnames, is a syntax nobody can read back correctly, which is also why a separator inside `VIRTUAL_PATH` is rejected rather than treated as a list.

**Duplicates are detected from a running index, not only at startup.**

Two containers claiming the same path on the same hostname is a mistake with an arbitrary outcome. Detecting it only during the initial scan would miss the normal case, because developers start the proxy first and their stack afterwards, so both containers arrive as live events. The layer therefore keeps what it has already seen, keyed by hostname and path, and reports a collision whichever way the second container arrives. The same index catches two containers claiming a hostname's root.

**Reports are visible by default.**

The existing skip paths log at debug, which is off at the default level, so a user would see nothing. Every report this change introduces is emitted at a level a user running the proxy normally will see, otherwise the requirement to report is satisfied by something nobody reads.

**Certificates stay per hostname.**

A path adds no hostname, so it needs no certificate and is served by the one already covering its host. The certificate commands reject an argument containing a path: today the filename helper maps a separator to an underscore, so such a request half-succeeds instead of being refused.

## Risks / Trade-offs

- **A stopped container changes what a path returns rather than breaking it.** When the container serving a path stops, its routes go with it and requests under that path fall through to whatever serves the hostname. For a development server that means a page and a `200`, not a 404, which is confusing to debug. A deployed Ingress behaves the same way. Documented, pinned by a test, and the reason the self-diagnosis compares response content rather than status.

- **`VIRTUAL_PATH` with a `traefik.` label does nothing, and that is pre-existing.** The container is skipped whole. A path is exactly when someone reaches for a middleware label, so this change makes an existing trap easier to fall into. Reporting it is the mitigation; changing the skip rule is a larger decision and out of scope. The broken shipped example is fixed so it stops teaching the trap.

- **Router names can still collide.** Names derive from a sanitised container name, and the sanitisation is lossy, so two differently-named containers can produce one name and the file provider drops one. Pre-existing, unchanged, and made easier to reach now that sharing a hostname is normal.

- **Path validation is a security boundary.** The rule is built by string formatting, so a path containing a backtick could close the matcher and append arbitrary routing syntax. Validation rejects anything that is not a single plain path, and that is the reason, not tidiness.

- **The integration suite's readiness check needs a container with a shell.** It probes readiness by executing a command inside the container, so an image without one can never become ready. The obvious image for distinguishing two backends by response content does not have a shell, so the test containers must either be images that do, or serve distinguishable content from an image that does.
