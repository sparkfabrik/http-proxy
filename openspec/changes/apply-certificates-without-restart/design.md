## Context

Traefik serves a certificate only when its file provider references it. That
reference lives in `/traefik/dynamic/auto-tls.yml`, written by
`build/traefik/entrypoint.sh` when the container starts. The certificates
themselves are bind-mounted read only; `traefik_dynamic` is a named volume, so
the host CLI cannot write into it and the file survives restarts.

That is why both certificate commands restart Traefik: the restart is a way to
re-run the scan, not a way to load the certificate.

## What was measured

All four measurements were taken against a running proxy on a developer machine.

| What was done                                     | How long until it was served |
| ------------------------------------------------- | ---------------------------- |
| `auto-tls.yml` rewritten with different content   | under 5 seconds              |
| `auto-tls.yml` rewritten with identical content   | under 3 seconds, 2 trials    |
| Certificate changed on disk, nothing else touched | 30 and 36 seconds, 2 trials  |

The third row is the confound-controlled one. Peer discovery and dinghy-layer
write into the dynamic directory continuously, so a reload caused by someone
else's write would look the same. The dynamic directory listing was hashed at
every poll and reported `dynamic-dir-changed=no` throughout both trials.

One rule explains all three rows: **the file provider's watch fires on the write
event, not on a content difference.** Reloading the provider re-reads the
certificate files from disk. With no write at all, that re-reading falls to
Traefik's own timer, which is where 30 seconds comes from.

The identical-content result is the one that decides the design. It means the
scan can be run unconditionally: for a new certificate the output differs, for a
regenerated one it does not, and both are live in about three seconds. No branch,
and no need to know which case is in hand.

The spec asks for 10 seconds rather than 3. The measured figure is a first-poll
hit, so it is an upper bound rather than a measurement, and a test that asserts
three seconds would be asserting the sampling interval.

## Decision: one generator, invoked two ways

`generate_tls_config` in the entrypoint stays the only writer of `auto-tls.yml`.
A guard makes it callable without starting Traefik:

```sh
if [ "$1" = "--tls-only" ]; then
    generate_tls_config
    exit 0
fi
```

It is placed after the function definitions and before `write_proxy_declaration`,
so the normal start path is unchanged.

The alternative considered was the CLI writing its own per-certificate file into
the dynamic directory. That was rejected: it puts two generators in the same
directory, and they drift apart the moment the entrypoint's scan and the CLI's
writer disagree about the same certificate.

## Decision: probe before invoking

The guard ships in the container image, so a CLI carrying this change will meet
proxies that predate it. What an older image does with the flag was measured
rather than assumed:

- `docker exec http-proxy traefik --tls-only` prints usage and exits 1. It does
  not bind any port, so it cannot disturb the running proxy.
- `docker exec` inherits the container's environment.
  `HTTP_PROXY_TAILSCALE_ENABLED=true` was observed inside a running container.

So an old entrypoint invoked with the flag would fall through its whole body:
rewrite the declaration, regenerate `auto-tls.yml` (which is the wanted effect),
run `remove_peer_config`, and then exit 1 with a page of usage text.

`remove_peer_config` is the problem. It deletes every `tailscale-peer-*.yaml`
when peer routing is off, and generating a certificate has no business
withdrawing peer routes. The machine used for these measurements had a live
`tailscale-peer-mac-sparkfabrik-paolomainardi.yaml` at the time.

The CLI therefore checks for the guard before using it:

```bash
docker exec http-proxy grep -q -- '--tls-only' /entrypoint.sh
```

When the guard is absent, nothing is sent to the container. The certificate still
applies on Traefik's own timer, and at the next start regardless, so the command
succeeds and says so without claiming the certificate is already live.

## Decision: fix the empty-certificate case in the same change

`generate_tls_config` returns before writing anything when it finds no
certificate files. Reproduced against a copy of the real entrypoint with an empty
certificates directory and a pre-existing config file:

```
Scanning for certificates in .../certs...
No certificate files found in .../certs
--- auto-tls.yml after a scan with zero certificates ---
STALE CONTENT referencing a deleted cert
```

Removing the last certificate therefore leaves `auto-tls.yml` pointing at files
that no longer exist, and a restart does not clear it either, because the file
lives in a named volume and the scan returns early again on the way up.

This is pre-existing, but the change makes `remove-cert` depend on the scan, so
it has to be correct for zero certificates. The early return is replaced by
writing the header with an empty certificate list.

## Risks

- **The scan runs against certificates written a moment earlier.** `mkcert`
  returns only after both files are on disk, and the scan reads them from the
  same bind mount, so there is no window where it sees the certificate without
  the key.
- **The 30-second tail is still there when the guard is absent.** That is the
  behaviour on an old image, and the message says the certificate will apply
  shortly rather than promising it is live.
- **Traefik's timer is not a documented interface.** Nothing in this change
  depends on it: it is the fallback path, and the fallback is also covered by the
  next start.
