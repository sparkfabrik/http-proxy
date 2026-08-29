## Context

Three sidecars surround Traefik today. `dinghy-layer` translates `VIRTUAL_HOST` into dynamic configuration files, `join-networks` bridges the proxy container onto the networks of manageable containers, and `dns-server` answers the local TLD. All three are concerned with one machine.

Five properties of what exists shape the decisions below.

**Every machine's proxy is already reachable from every other machine.** Both compose files publish `80:80`, `443:443` and `30000:8080` on all interfaces, so a machine on a tailnet is already listening on its tailnet address. Nothing has to be opened.

**The DNS server maps every domain it handles to one address.** `createARecord` in `cmd/dns-server/main.go` uses a single `HTTP_PROXY_DNS_TARGET_IP` for every answer. This rules out solving the problem in DNS: an unqualified hostname carries no information about which machine owns it, so DNS could not answer differently per hostname even if it were given a map.

**Traefik reports its full routing table over the API it already exposes.** `api.insecure: true` in `build/traefik/traefik.yml` publishes a read-only API on port 30000. `GET /api/http/routers` returns every router with `name`, `provider`, `rule`, `priority`, `entryPoints` and `status`. A peer therefore already publishes the list of hostnames it serves, and needs no new endpoint and no new port.

**Priority is always populated in that output.** Traefik fills an unset priority with the rule's length before reporting it. `dinghy-layer` sets `0` for host-only routers and `pathPriority` for path-mounted ones, and the API reports `49` and `10000+` respectively. Copying the reported priority therefore reproduces the peer's own ordering exactly, for both kinds of router, without having to re-derive it.

**File ownership in the dynamic directory is already scoped.** `dinghy-layer`'s reconcile only removes files matching `^[0-9a-f]{12}\.yaml$` (`isDinghyConfigFile`), explicitly to avoid touching the entrypoint's `auto-tls.yml` and the built-in middlewares. Files under any other name survive it.

The local Tailscale daemon publishes a status document listing every machine on the tailnet: a `Self` object and a `Peer` map, each entry carrying `HostName`, `OS`, `Online`, `UserID` and `TailscaleIPs`. How that document is obtained is platform-specific, and this is the one place where platforms genuinely differ.

On Linux the daemon exposes a local API on a unix socket at `/var/run/tailscale/tailscaled.sock`, mode `0666`, and `GET /localapi/v0/status` returns the document. A container can mount the socket and read it directly, with no privilege.

On macOS there is no such socket. The daemon runs as a network system extension and its local API is bound to loopback behind a token, which a container cannot reach: loopback inside a container is the container. The command-line client shipped inside the application bundle can produce the identical document with `status --json`, but only from the host.

## Goals / Non-Goals

**Goals:**

- The hostname a project uses today reaches its container from any of the user's machines, unchanged.
- The set of machines is derived at runtime, never written down.
- A request that can be served locally never leaves the machine.
- The behaviour is inert until switched on.

**Non-Goals:**

- Sharing certificate authorities between machines. TLS terminates on the machine the browser talks to.
- Load balancing or failover across machines. One hostname has one owner; a collision is an error to report, not a pool to balance.
- Reaching machines outside the user's own tailnet, or serving other people's machines. That is a different trust decision, it is not made here, and no setting exposes it.
- Changing anything about how a machine routes its own containers.

## Decisions

### The forwarding hop, not a shared namespace

The alternative was to qualify hostnames per machine, `app.machine-a.loc`, and point per-machine DNS at per-machine addresses. It needs no new code, but it puts the machine name into every project's configuration and breaks the requirement that the hostname stay the same.

Forwarding keeps DNS exactly as it is. Every machine still answers `127.0.0.1` for every name it handles, the browser still connects to the local proxy, and the local proxy decides whether the request is served here or forwarded. The name never has to encode a location because the location is resolved one layer up, where the information actually lives.

### Reading a peer's routes from its Traefik API

A peer's routing table is already published on port 30000. Reading it means a peer runs no new code: bringing the existing proxy up is enough to be discoverable.

Filtering, applied to each router in the response:

- The machine must declare itself as this proxy, which is checked before its routers are read at all.
- `status` must be `enabled`.
- `provider` must not be `internal`, which drops the dashboard and the API's own routers.
- `entryPoints` must contain `http`. Each route is emitted twice by `dinghy-layer`, once per entrypoint, and taking only the plain one gives each rule once. The forwarding machine emits its own pair.
- `name` must not begin with `peer-`. This is the loop guard, and it is the reason the naming is fixed rather than incidental: a machine offers only what it serves itself, so a forwarded route is never forwarded onward.
- The rule must contain at least one `Host(...)` or `HostRegexp(...)` term. A rule matching only a path belongs to that machine's own dashboard arrangement, not to a routable hostname.

### Identifying this proxy, not any proxy

Reading a peer's routes from port 30000 identifies a Traefik, not this proxy. On a tailnet carrying an unrelated reverse proxy with its dashboard exposed, that machine's routes would be adopted and traffic forwarded to it. The port is not an identity.

So the proxy declares itself, and a machine is adopted only when the declaration is present on the cycle its routes are used.

**The declaration is a middleware**, written by the Traefik entrypoint into the dynamic directory under a documented, stable name. A middleware is the right shape because it is inert: Traefik applies one only where a router references it, so a marker no router references changes no behaviour. A marker router would have to match something, and anything it matched would be a hostname it had taken.

**It is unconditional.** The entrypoint writes it whether or not peer routing is enabled on that machine, because it means "this is a Spark HTTP Proxy", not "this machine forwards to peers". A machine with the behaviour switched off is still a perfectly good source of routes for a machine that has it on, and that asymmetry is the common case: one desktop serving projects, one laptop reading them.

**The check fails closed.** No declaration means no routes, so a machine running a version older than this change is not adopted. That is the correct direction: the alternative is trusting a port, which is what this exists to stop. It does mean both machines need this version before anything is forwarded, which belongs in the release notes rather than being discovered.

**Its file name deliberately does not match the peer glob.** The entrypoint clears `tailscale-peer-*.yaml` when peer routing is disabled, and the marker must survive that: a machine that has switched forwarding off must remain discoverable by others. The two names cannot be allowed to converge.

The declaration is read first, before the routing table, so an unrelated proxy never has its routes read at all. A machine that is not this proxy therefore costs one small request to the middlewares endpoint and nothing more, and one that never answers is rejected on that same first request.

The report grows a status for this: a machine that answers but does not declare itself is distinct from one that never answered, and from one excluded by ownership. Three different facts, three different things to do about them.

### The generated configuration

One service per peer, because every route on a peer has the same backend:

```yaml
http:
  services:
    peer-<slug>:
      loadBalancer:
        servers:
          - url: http://<peer-tailscale-ipv4>:80
```

One pair of routers per accepted rule, mirroring what `dinghy-layer` emits for a container:

```yaml
http:
  routers:
    peer-<slug>-<i>:
      rule: <the peer's rule, verbatim>
      service: peer-<slug>
      priority: <the peer's reported priority>
      entryPoints: [http]
    peer-<slug>-tls-<i>:
      rule: <the peer's rule, verbatim>
      service: peer-<slug>
      priority: <the peer's reported priority>
      entryPoints: [https]
      tls: {}
```

The rule is copied rather than rebuilt from the extracted hostnames. A path-mounted container's rule carries its path matcher, and copying it means `virtual-path-routing` keeps working across machines with no code that knows about paths.

`passHostHeader` defaults to true in Traefik, so the peer receives the original `Host` and performs the final match itself. The hop is plain HTTP: it travels inside WireGuard, and using HTTPS would mean the forwarding machine validating a certificate issued by another machine's authority, which is precisely the sharing this design avoids. `serversTransport.insecureSkipVerify` is already true in the static configuration, so an HTTPS hop would not even be validated, making the plain hop the more honest of the two.

TLS terminates locally with whatever certificate the local machine already has for the name, which for a machine following the usual setup is the wildcard `generate-mkcert` already produced for the local TLD. No machine trusts another machine's authority.

### Extracting hostnames anyway

The rule is copied verbatim, but the hostnames inside it are still extracted, for two decisions that need them: suppressing a peer route when the hostname is served locally, and detecting two peers claiming one hostname. Extraction reads the `Host(...)` and `HostRegexp(...)` terms out of the rule string.

A regex hostname is compared as its literal pattern text. Two machines using the same wildcard is a genuine collision and is caught; a wildcard on one machine overlapping a plain name on another is not detected, and is left to Traefik's ordinary priority ordering rather than being guessed at.

### Precedence

Local first: a hostname served by any local router, taken from the local Traefik's own API through the compose network at `http://http-proxy:8080`, suppresses every peer route claiming it. The collision is logged at a level users see.

Between peers: first by sorted Tailscale hostname, so the outcome does not depend on map iteration order or on which peer answered first. The second claimant is dropped and logged.

Filtering rather than de-prioritising is what keeps priorities free to be copied verbatim. Since a peer route never coexists with a competing local route, the copied priority only ever orders a peer's own routes against each other, which is exactly the ordering it came from.

### Discovery and refresh

Peers come from the Tailscale local API: entries in `Peer` where `Online` is true and `UserID` equals `Self.UserID`, excluding `Self`. The `UserID` test is what limits this to the user's own machines and excludes nodes shared in from another tailnet.

That test is not configurable and must not become configurable. It is the trust boundary of the whole capability: without it, any node reachable on the tailnet could publish a hostname and have requests forwarded to it. The address-list fallback below is subject to the same restriction, so it cannot be used to reach around the check.

Most tailnet devices are not proxies. A phone, a television or a router simply fails the probe, so failures are expected rather than exceptional: a failing peer is retried with exponential backoff up to a ceiling, and the probe timeout is short.

The loop runs on an interval, default ten seconds, rather than watching Docker events. The trigger is a change on another machine, which no local event stream can observe.

Each poll writes one file per peer, `peer-<slug>.yaml`, atomically through a temporary file and a rename, the same way `dinghy-layer` writes. Reconciliation then removes `peer-*.yaml` files with no corresponding peer in the current cycle, scoped by its own filename pattern in the same way `dinghy-layer` scopes its own. The two patterns cannot overlap: a twelve-character hexadecimal name cannot begin with `peer-`.

`<slug>` is the peer's Tailscale hostname reduced to characters safe in a filename and in a Traefik router name.

### Reporting what was found: `tailscale-peers`

Discovery is invisible: it happens inside a container, on an interval, against machines that may or may not answer. When a hostname does not resolve to the machine a user expects, the question is always "which peers did you find, and what did each one give you". A command answers it.

```
spark-http-proxy tailscale-peers
spark-http-proxy tailscale-peers --json
```

The command does not perform discovery. Re-implementing the daemon query, the probe and the filtering in shell would duplicate logic that is already written once in Go and would drift from it. Instead the service writes the outcome of its last cycle to a state file and the command formats it, so what a user sees is exactly what the proxy acted on, including the timestamp of the cycle it came from.

The state file lives in a directory of its own, bind-mounted from the host the way `certs` already is, and deliberately not in the directory Traefik watches: Traefik's file provider parses `.json` as dynamic configuration and would report the status file as a broken one. A host bind mount also means the command reads a file rather than shelling into a container, so it still answers when the stack is stopped, reporting the last known state and saying that it is stale.

Every peer that discovery considered appears, including the ones that gave nothing, each with the reason:

| Status | Meaning |
| --- | --- |
| `ok` | probed, and its hostnames are being forwarded |
| `no proxy` | reachable, but nothing answered the route query |
| `unreachable` | did not answer within the probe timeout, currently backing off |
| `skipped` | found on the tailnet but excluded, with the reason given (offline, different user) |

Collisions are reported in their own section rather than being folded into the table, because a collision is a property of a hostname rather than of a peer: a hostname suppressed locally, or claimed by two peers, is named alongside the machine that won it.

When peer routing is disabled the command says so and explains how to enable it, rather than printing an empty table that reads like a failure.

`status` gains one summary line when the behaviour is enabled, naming how many peers contributed and how many hostnames, and pointing at `tailscale-peers` for the detail. `show-config` gains the settings, as it already does for the DNS ones.

### Where discovery runs, and the two sources

The service runs in a container, like the other three. What differs per platform is only how it obtains the status document, so that is the seam: a source produces a status document, and everything downstream — the ownership filter, the online filter, the probe, the generation — is one implementation shared by every platform. The filter is never reimplemented per source, which is what keeps the same-user guarantee from depending on how the document arrived.

**Socket.** The default. The container mounts the daemon's unix socket read-only and fetches the document over it. Linux, and any platform that grows an equivalent socket.

**Host-produced file.** For macOS. The command-line client inside the application bundle writes `status --json` to a file in the state directory, which the container already has mounted, and the source reads the document from there. The document is the same one, so the ownership filter runs over the same fields with the same result: this is not a weaker mode, it is the same discovery with a different transport. The file carries the time it was written, and a document older than a few refresh intervals is treated as no document rather than as an empty tailnet. The CLI writes it on `start`, and keeping it current is a small scheduled job on the host that runs one command.

There is no third source. An address list with no document behind it was considered and rejected; the section below records why, so it is not reintroduced.

Locating the client on macOS: the bundle's executable is not on `PATH`, so resolve `tailscale` from `PATH` first and fall back to the known path inside the application bundle.

Outbound reachability from the container to a tailnet address relies on the host forwarding bridge traffic onto its Tailscale interface. This was the open question for the desktop Docker runtime, since a virtual machine sits between the container and the host's network stack, and it is settled: a container reaches a peer's tailnet address directly, at the same latency as the host does. No host networking is needed, and the same compose shape works on both platforms.

Producing the document on macOS needs no privilege either — the bundled client answers as the logged-in user.

### Why there is no unverified mode

An earlier draft had a configured address list standing in whenever the daemon could not be queried. That is wrong, and it is worth recording why, because the idea is an easy one to have twice.

The ownership test compares a peer's user against the local machine's user, and both values come from the status document. With no document there is nothing to compare, so a fallback that probes anyway is not "the same rule, degraded" — it is the rule not running, reached by the daemon being unavailable. A missing socket would have become permission to forward to any address someone had configured.

Making it an explicit mode rather than a fallback fixed the silent part, but left a documented setting whose only purpose was to skip the check. It has now been removed entirely, on two findings:

- **macOS was its justification and does not need it.** The bundled client produces the full document unprivileged, so that platform verifies ownership like any other.
- **The integration test does not need it either.** The test writes a synthetic status document and uses the file source, which is better than a mode of its own: it exercises the same path macOS uses in production, and the ownership filter actually runs during the test rather than being bypassed by it.

So there is one rule, running on every cycle, with no supported way to turn it off. A source that is selected but unavailable contributes nothing and says so; an empty tailnet and an unreachable daemon are different states and are reported differently.

This does not make forgery impossible. The file source reads a document from disk, so an operator who hand-writes that file with invented ownership gets the same effect — the same authority they have to edit any other configuration. What has gone is the documented setting, the failure path that reached it, and the ambiguity about whether a given cycle was verified.

It does make the state directory a **trust input** rather than a scratch area. It is written by the host and read by the service, its contents decide who traffic is forwarded to, and it should be owned by the user running the proxy and not writable by anyone else. That is a property to state in the README and to get right in whatever creates the directory.

## Risks / Trade-offs

**It widens what other machines can reach.** Enabling this means local development containers, which typically have no authentication, are reachable from other devices on the tailnet. The ports are already published on all interfaces, so the exposure to a local network exists today and is not introduced here, but it becomes load-bearing rather than incidental. Documented where users read it, and the default is off.

**The route registry is the insecure API.** Port 30000 is a read-only Traefik API with no authentication. Using it as the registry means any device that can reach the port can enumerate the machine's project hostnames. This is an information disclosure, accepted knowingly in exchange for peers needing no new surface, and the README gains a note on restricting the port.

**Discovery is polling.** A hostname appearing on another machine takes up to one interval to become reachable. Ten seconds is short enough not to be noticed in practice and long enough not to matter for load.

**A wildcard on one machine can shadow a plain name on another.** Collision detection compares hostnames literally and does not evaluate patterns against names. Traefik's priority ordering decides, and the case is rare enough that guessing at overlap would cost more than it saves.

## Migration Plan

The behaviour is off unless enabled, so an upgrade changes nothing. Enabling it on one machine is harmless and does nothing useful; enabling it on two makes their hostnames mutually reachable. Disabling it and restarting removes the generated files, and the machine routes exactly as before.

## Open Questions

Whether a machine should be able to opt out of publishing, so a hostname stays local while still being able to reach peers. There is no evidence yet that anybody wants it, and the API it would need to filter is not this service's to change, so it is left out.
