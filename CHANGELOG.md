# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `spark-http-proxy hosts` lists what the proxy serves, which machine serves it, and the directory local containers run from
- HTTPS to a forwarded hostname needs a certificate on the machine reaching it, now documented ([#118](https://github.com/sparkfabrik/http-proxy/issues/118))
- `tailscale-refresh-peers` runs a discovery cycle now instead of waiting for the next one ([#132](https://github.com/sparkfabrik/http-proxy/issues/132))
- Tailnet peer routing: reach a hostname served by another machine on the same Tailscale account
- `tailscale-peers` reports the last discovery cycle, and `--json` the same machine-readably
- `start-with-tailscale` and `stop-tailscale` start and stop peer routing on their own
- `tailscale-status` writes the tailnet status document peer discovery reads on macOS
- `VIRTUAL_PATH` mounts a container under a path of its `VIRTUAL_HOST` ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `self-test` checks a mounted path over HTTP and HTTPS ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- OpenSpec change tracking under `openspec/` ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `list-certs` lists installed certificates and `remove-cert` removes them ([#107](https://github.com/sparkfabrik/http-proxy/issues/107))
- Unit tests for the parsing and config helpers ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- CI `go-checks` job running `gofmt`, `go vet` and `go test -race` ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- DNS server listens on TCP 19322 as well as UDP, for Lima ([#56](https://github.com/sparkfabrik/http-proxy/issues/56))
- `upgrade` pulls new images and recreates only the changed containers ([#96](https://github.com/sparkfabrik/http-proxy/pull/96))
- `self-update` updates the script and compose files from git ([#96](https://github.com/sparkfabrik/http-proxy/pull/96))
- This CHANGELOG

### Changed

- `tailscale-peers --refresh` replaces `tailscale-refresh-peers`, which is removed
- The peer table has two groups, `PROXY` and `EXCLUDED`, with the reason in a `STATUS` column
- The peer summary is one line: machines, how many run this proxy and forward what, how many are excluded
- `self-update` matches upstream exactly, including after a force push
- `tailscale-peers` groups the machine table, running-this-proxy first
- `self-update` applies the update it pulled instead of printing the command to do it
- `version` reports each container's image revision instead of `unknown`
- `tailscale-status` says how many machines the document holds and how many are online
- `generate-mkcert` and `remove-cert` apply certificates without restarting the proxy ([#145](https://github.com/sparkfabrik/http-proxy/issues/145))
- A destructive command run without a terminal refuses instead of proceeding ([#147](https://github.com/sparkfabrik/http-proxy/issues/147))
- `status` names the machines forwarding and what each serves ([#142](https://github.com/sparkfabrik/http-proxy/issues/142))
- Every base image is pinned to a digest beside its tag ([#139](https://github.com/sparkfabrik/http-proxy/issues/139))
- The service image is built on `alpine:3.24` rather than `alpine:latest` ([#123](https://github.com/sparkfabrik/http-proxy/issues/123))
- Peer discovery records its last completed cycle in `tailscale-peers-completed-at` ([#134](https://github.com/sparkfabrik/http-proxy/issues/134))
- Errors and warnings go to stderr rather than stdout
- Warn when a container's routing variables are ignored ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Warn when two containers claim the same host and path, naming both ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `generate-mkcert` and `remove-cert` reject an argument containing a path ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- `self-test` verifies end-to-end routing rather than only DNS liveness ([#104](https://github.com/sparkfabrik/http-proxy/issues/104))

### Fixed

- `hosts` reports only what Traefik serves, including containers whose rule names several hostnames
- Commands no longer abort on a state directory Docker created as root; they say how to take it back
- The certificate scan reports a write it could not make ([#151](https://github.com/sparkfabrik/http-proxy/issues/151))
- The README starts the proxy with the CLI rather than compose, which left directories owned by root ([#150](https://github.com/sparkfabrik/http-proxy/issues/150))
- Removing the last certificate no longer leaves the TLS configuration pointing at deleted files ([#145](https://github.com/sparkfabrik/http-proxy/issues/145))
- Prompting commands say why they stopped when there is no terminal ([#147](https://github.com/sparkfabrik/http-proxy/issues/147))
- Commands the CLI suggests can be pasted ([#118](https://github.com/sparkfabrik/http-proxy/issues/118))
- Peer routing no longer reports itself broken for up to a minute after a restart ([#141](https://github.com/sparkfabrik/http-proxy/issues/141))
- The macOS status agent reports whether it actually loaded ([#130](https://github.com/sparkfabrik/http-proxy/issues/130))
- The tailnet status source is detected from the host rather than defaulted ([#128](https://github.com/sparkfabrik/http-proxy/issues/128))
- A staleness tolerance written as `05m` no longer aborts the command ([#128](https://github.com/sparkfabrik/http-proxy/issues/128))
- macOS keeps the status document current with a launchd agent ([#128](https://github.com/sparkfabrik/http-proxy/issues/128))
- An optional-stacks record the CLI cannot read is left alone rather than deleted
- Clearing one optional stack no longer corrupts the record
- Compose profiles are exported even when no optional stack is on
- Optional stacks survive `start`, `restart` and `upgrade` ([#124](https://github.com/sparkfabrik/http-proxy/issues/124))
- A stack already running when nothing is recorded is recorded on the next command ([#124](https://github.com/sparkfabrik/http-proxy/issues/124))
- Commands distinguish an optional stack switched off from its services not running ([#124](https://github.com/sparkfabrik/http-proxy/issues/124))
- `self-test` no longer aborts on the first failed check ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Fix the CORS example in `examples/applications.yml`, which could never have worked ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Correct `AGENTS.md` on unit tests and the verification step ([#113](https://github.com/sparkfabrik/http-proxy/issues/113))
- Ignore one-off containers created by `docker compose run` ([#111](https://github.com/sparkfabrik/http-proxy/issues/111))
- Reconcile generated config against running containers on the initial scan ([#109](https://github.com/sparkfabrik/http-proxy/issues/109))
- Backend IP and port selection is deterministic for multi-network containers ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- DNS A-record TTL lowered from 3600s to 60s so a changed target propagates ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Guard a nil-pointer panic in `join-networks` when a container reports no networks ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Signal-driven shutdown is deterministic in the event-driven services ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Reconnect to the Docker event stream when the daemon closes it ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- Reject a non-IPv4 `HTTP_PROXY_DNS_TARGET_IP` at startup ([#101](https://github.com/sparkfabrik/http-proxy/issues/101))
- `restart` starts containers when they are not running instead of failing ([#40](https://github.com/sparkfabrik/http-proxy/issues/40))
- Remove the ca-certificates install that broke CI builds
- Remove HSTS headers from HTTPS responses, since they force HTTPS on local domains
- Apply the `disable-hsts` middleware at the HTTPS entrypoint so all HTTPS traffic gets it
