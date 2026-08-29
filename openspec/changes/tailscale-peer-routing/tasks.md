## 1. Discover the machines

- [x] 1.1 The set of peers is read at runtime from the local Tailscale daemon, with no peer list configured by hand
- [x] 1.2 A peer that is offline contributes nothing
- [x] 1.3 A peer belonging to a different user contributes nothing
- [x] 1.4 The same-user restriction is not configurable, and no combination of settings widens it
- [x] 1.5 The machine itself is never treated as one of its own peers
- [x] 1.6 The status document is obtained through a source, and everything downstream is one implementation shared by every source, so the ownership filter is never reimplemented per platform
- [x] 1.7 A source that cannot produce a document contributes nothing, is reported as a failure rather than as an empty tailnet, and is retried
- [x] 1.8 macOS obtains the same document from the command-line client inside the application bundle, written to the state directory by the host, and the ownership filter runs over it unchanged
- [x] 1.9 The client is resolved from `PATH` first and from the known bundle path second
- [x] 1.10 A host-written document older than a few refresh intervals is treated as no document, not as an empty tailnet
- [x] 1.11 There is no source, setting or failure path that forwards to an address without the ownership check having run on that cycle
- [x] 1.12 The state directory is treated as a trust input: owned by the user running the proxy, not writable by others, and documented as such

## 2. Read what a peer serves

- [x] 2.1 A peer's routes are read from the proxy it already runs, with nothing new installed or exposed on it
- [x] 2.2 A device that is not running the proxy fails its probe quickly and is retried with a growing delay
- [x] 2.3 A peer's internal dashboard and API routes are not treated as routable hostnames
- [x] 2.4 A route that matches no hostname is not treated as a routable hostname
- [x] 2.5 A disabled route on a peer is not used
- [x] 2.6 Each hostname a peer serves is taken once, not once per scheme
- [x] 2.7 A route a peer holds by forwarding is not taken from it, so a request cannot be passed back and forth

## 2b. Identify this proxy, not any proxy

- [x] 2b.1 The proxy declares its own identity, written by the Traefik entrypoint under a documented, stable name
- [x] 2b.2 The declaration is inert: it changes no routing on the machine that makes it
- [x] 2b.3 It is written whether or not peer routing is enabled on that machine
- [x] 2b.4 Its name does not match the glob the entrypoint clears, so switching forwarding off leaves the machine discoverable
- [x] 2b.5 A machine answering with a routing table but not declaring itself contributes nothing
- [x] 2b.6 That machine is reported distinctly from one that did not answer and from one excluded by ownership
- [x] 2b.7 The declaration is checked before the routing table is read, so a machine that is not this proxy costs one request
- [x] 2b.8 The release notes state that both machines need this version before anything is forwarded

## 3. Build the forwarding routes

- [x] 3.1 A hostname served only by a peer is answered by that peer's container, with the response unchanged
- [x] 3.2 The peer receives the hostname as the browser sent it, and performs the final match itself
- [x] 3.3 A container mounted under a path of a hostname on a peer is reached through that path
- [x] 3.4 A wildcard hostname on a peer keeps its meaning
- [x] 3.5 Both schemes reach a remotely served hostname
- [x] 3.6 Encryption terminates locally, using the certificates already installed on the machine receiving the request, with no authority shared between machines
- [x] 3.7 The ordering a peer gives its own routes is preserved, so a path and the hostname it sits on rank as they do on the peer

## 4. Decide who owns a hostname

- [x] 4.1 A hostname served by a local container is answered locally and is never forwarded
- [x] 4.2 A hostname served both locally and remotely is reported as a collision
- [x] 4.3 Two peers serving one hostname resolve to the same peer on every poll and on every machine
- [x] 4.4 A collision between two peers is reported
- [x] 4.5 The choice does not depend on iteration order or on which peer answered first

## 5. Keep the configuration current

- [x] 5.1 Routes are refreshed on an interval, since the change that matters happens on another machine
- [x] 5.2 A peer going offline, or ceasing to serve a hostname, withdraws that hostname within one refresh
- [x] 5.3 Configuration written for a peer that is no longer reachable is removed, not left pointing at a dead address
- [x] 5.4 Configuration files are written whole, never observed half-written by the proxy
- [x] 5.5 The new service removes only its own files, and the existing per-container reconciliation removes only its own, with neither able to match the other's names
- [x] 5.6 Generated certificate configuration and built-in middlewares are untouched by both

## 6. Keep it inert until asked for

- [x] 6.1 The behaviour is off unless explicitly enabled
- [x] 6.2 With it off, no peer is discovered, no probe is sent, and routing is byte-identical to the previous release
- [x] 6.3 Disabling it and restarting leaves no forwarded hostnames behind
- [x] 6.4 The peer source, the refresh interval and the source's location are configurable, with defaults that work unconfigured on Linux and on macOS once the host job is in place

## 6b. Turn it on the way monitoring is turned on

- [x] 6b.1 A lifecycle command starts the proxy with peer routing enabled, mirroring the monitoring stack's own start command
- [x] 6b.2 A command stops peer routing alone, leaving the rest of the proxy serving
- [x] 6b.3 Stopping peer routing leaves no forwarded hostname reachable, without requiring a proxy restart
- [x] 6b.4 Both commands appear in the usage text, in the detailed help, and in shell completion, beside their monitoring counterparts
- [x] 6b.5 `status` points at the start command when peer routing is off, as it already does for monitoring
- [x] 6b.6 The underlying switch stays available for non-interactive use, with the commands as the documented way in

## 7. Make it inspectable

- [x] 7.1 The service records the outcome of each cycle, with a timestamp, somewhere the command line can read it without entering a container
- [x] 7.2 That record is kept out of the directory the proxy watches, so it is never mistaken for routing configuration
- [x] 7.3 `tailscale-peers` lists each peer considered, its address, and the hostnames it contributes
- [x] 7.4 A peer that contributed nothing is listed with its reason, distinguishing unreachable, no proxy, and excluded
- [x] 7.5 Collisions are shown, naming the hostname and the machine that serves it
- [x] 7.6 `tailscale-peers --json` emits the same information machine-readably
- [x] 7.7 `tailscale-peers` performs no discovery of its own, so it can never disagree with what the proxy is doing
- [x] 7.8 `tailscale-peers` on a stopped proxy reports the last known state and says it is not current
- [x] 7.9 `tailscale-peers` with the behaviour disabled says so and says how to enable it
- [x] 7.10 `status` gains a one-line summary when enabled, and `show-config` gains the new settings
- [x] 7.11 `tailscale-peers` appears in the usage text, the detailed help, and shell completion

## 8. Prove it with the real stack

- [x] 8.1 A second proxy inside the integration stack stands in for a second machine
- [x] 8.2 The test uses the file source with a synthetic status document, so the ownership check runs during the test rather than being bypassed by it
- [x] 8.3 A hostname served only by the second proxy is answered by its backend, proved by content rather than by status code
- [x] 8.4 A hostname served by both is answered by the local backend, proved by content
- [x] 8.5 A path mounted on the second proxy is reached through its path
- [x] 8.6 Stopping the second proxy withdraws its hostnames

## 9. Say what it costs

- [x] 9.1 The README states that enabling this makes local development containers reachable from other devices on the tailnet, and that those containers usually have no authentication
- [x] 9.2 The README states that the proxy's ports are published on all interfaces, so the exposure is not limited to the tailnet, and how to narrow it
- [x] 9.3 The README states that the route registry is an unauthenticated read-only API, and what it discloses
- [x] 9.4 The architecture description covers four sidecar services rather than three, in both the README and the contributor guide
- [x] 9.5 The README states that ownership is verified on every cycle with no way to turn it off, and that the state directory is a trust input because the document in it decides who traffic goes to
- [x] 9.6 The macOS setup is documented: what writes the status document, where it goes, and what keeps it current
- [x] 9.7 The README carries a usage section: turning it on, what to expect on each platform, reading the report, turning it off, and the fact that both machines need this version
- [x] 9.8 That section is written for someone who has not read the issue or the spec, and shows real commands and real output rather than describing them
- [x] 9.9 A changelog entry, as the contributor guide requires
