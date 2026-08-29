## Why

A developer with more than one machine runs one proxy per machine, and each proxy can only see its own Docker socket. A container exposed as `app.loc` on one machine does not exist as far as another machine is concerned: the second machine's DNS answers `127.0.0.1` for that name, its own proxy has no route for it, and the request ends in a 404 from the wrong machine.

The workarounds all cost something. Publishing a host port and browsing to `192.168.1.x:8080` throws away the hostname, the certificate and the path routing, and breaks any application that builds absolute URLs. Renaming the container to `app.machine-a.loc` makes the machine part of every project's configuration, so the same repository needs a different value depending on where it is checked out. Editing `/etc/hosts` on each machine turns a routing problem into a list that has to be maintained by hand and goes stale the moment a container moves.

What a developer actually wants is for the hostname to keep meaning the same thing everywhere. The machines are already on a Tailscale tailnet that gives every one of them a stable address and knows which of them are online, so the information needed to answer "who serves this hostname" is available without anybody writing it down.

## What Changes

**A machine can forward a hostname it does not serve to the machine that does.** Requests for hostnames served locally are unaffected and never leave the machine. Only a hostname that no local container claims is eligible to be forwarded.

**Peer machines are discovered, never configured.** The proxy asks the local Tailscale daemon which machines belong to the same user and are currently online, and probes those. A machine that is offline, belongs to somebody else, or is not running the proxy contributes nothing and costs nothing beyond a short probe. There is no peer list to maintain, and adding a machine to the tailnet is enough to make its hostnames reachable.

**A peer's routes are read from the proxy it is already running.** Nothing new is exposed on the peer and no agent has to be installed there beyond the proxy itself.

**Only a machine running this proxy is used.** The proxy declares its own identity, and a machine is adopted only when that declaration is present. Answering on the expected port is not enough: an unrelated reverse proxy with an open dashboard is not a source of routes, and is reported as such rather than being silently adopted.

**Hostnames are unqualified and identical on every machine.** The name a project uses today keeps working, in place, on every machine. No project configuration changes.

**Forwarding preserves the request as sent**, including the hostname, so the peer's own proxy performs the final match. A container mounted under a path of a hostname is reached through that path exactly as it is locally, without the forwarding machine having to understand the arrangement.

**Encryption terminates on the machine the browser is talking to**, using the certificates already installed there. Certificate authorities are not shared between machines and no machine has to trust another machine's authority.

**A local container always wins.** When a hostname is served both locally and by a peer, the local container answers and the collision is reported, so a forgotten container elsewhere can never silently shadow the one being worked on.

**A forwarded hostname is never forwarded onward.** Only hostnames a machine serves from its own containers are offered to peers, so two machines cannot bounce a request between them.

**The whole behaviour is opt-in and off by default.** It changes what a proxy answers for names it does not own, which is not something an upgrade should start doing on its own.

## Capabilities

### New Capabilities

- `tailscale-peer-routing`: a hostname served by a container on one machine is reachable, under the same name, from every other machine belonging to the same user on the same Tailscale tailnet. Tailscale is a hard dependency: it is what supplies the addresses and the account of who owns each machine.

### Modified Capabilities

None. `virtual-path-routing` is untouched: a path-mounted container is forwarded by the rule it already has, and its behaviour on the machine that runs it does not change.

## Impact

- A new service alongside the existing three: it discovers peers, reads their routes, and writes the forwarding configuration the proxy watches. It owns its own configuration files and removes them when a peer goes away, and it records the outcome of each cycle for the command line to report.
- Peer discovery reads the local Tailscale daemon's account of the network. On platforms exposing a daemon socket the service reads it directly, which is a new mount; on platforms that do not, the host writes the same document to a state directory the service reads. Either way the same ownership check runs, and there is no mode in which it does not.
- `pkg/config`: the settings that turn the behaviour on, the peer source, the refresh interval, and the source's location.
- `pkg/config/traefik.go`: unchanged. A forwarding route is an ordinary router and service and needs no new fields.
- `cmd/dinghy-layer`: unchanged. Its file-ownership check already ignores files it did not create, so the new files survive its reconciliation and its own files are never touched by the new service.
- `build/Dockerfile` and both compose files: the new binary, its service definition behind a profile, and the socket mount.
- The Traefik entrypoint additionally declares the proxy's identity, unconditionally, since a machine can be a source of routes for others without forwarding any itself.
- `bin/spark-http-proxy`: starts the new service when the behaviour is enabled, and gains `tailscale-peers`, which reports the peers found on the last cycle, the hostnames each contributes, the reason any of them contributed nothing, and any collisions. `status` and `show-config` gain a summary and the new settings.
- `test/test.sh`: a second proxy inside the test stack standing in for a second machine, proving a remote-only hostname is answered by the remote backend while a locally-served hostname stays local.
- `README.md`, `AGENTS.md`: the architecture is described in terms of three sidecar services and becomes four. The exposure this introduces is documented where users will read it, not only in the commit.
- `CHANGELOG.md`: an entry, as the contributor guide requires.
- The agent skill for this proxy lives in a separate repository and is a separate pull request once this is released.
- Existing users: no action. With the behaviour disabled, which is the default, every machine routes exactly as it does today.
