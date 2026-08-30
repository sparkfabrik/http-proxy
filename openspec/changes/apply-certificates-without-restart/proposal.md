## Why

`generate-mkcert` and `remove-cert` both restart Traefik to apply their change. A
restart drops every in-flight connection on the machine, so generating one
certificate interrupts every other site the developer is working on.

The restart is also not the thing that makes the certificate work. Traefik only
serves a certificate that `auto-tls.yml` references, and that file is written by
the container entrypoint's scan. The restart applies the change as a side effect
of re-running that scan on the way up.

Measurements on a running proxy establish what actually applies a change:

- **Rewriting `auto-tls.yml` applies it in under 5 seconds.** Adding a router in
  place was live within 5 seconds, and reverting removed it just as fast.
- **A rewrite with byte-identical content is enough.** A regenerated certificate
  was served within 3 seconds of the file being written back over itself, in two
  trials in opposite directions. The provider watch fires on the write event, not
  on a content difference.
- **Without any write it still applies, but slowly.** A regenerated certificate
  left alone took 30 and 36 seconds in two trials, with the dynamic directory
  hashed at every poll to rule out a write from peer discovery or dinghy-layer.

One rule holds all three: the watch fires on the write, reloading the provider
re-reads the certificate files from disk, and with no write that re-reading falls
to Traefik's own timer.

So re-running the scan applies both a new and a regenerated certificate in about
three seconds, without a restart and without needing to know which case it is.

## What Changes

- **The entrypoint's certificate scan becomes callable on its own.** A guard
  makes `/entrypoint.sh --tls-only` run the scan and exit, instead of the scan
  being reachable only by starting the container.
- **`generate-mkcert` runs the scan instead of restarting Traefik.** No
  connection is dropped.
- **`remove-cert` runs the scan instead of restarting Traefik.** Removal needs
  the scan more than generation does, because deleting a certificate file leaves
  `auto-tls.yml` pointing at a file that no longer exists until something
  rewrites it.
- **The scan handles the empty case.** Today it returns early when no
  certificates are found and leaves the previous `auto-tls.yml` in place, so
  removing the last certificate leaves every reference dangling. It will write an
  empty certificate list instead.
- **Neither command fails when the proxy is not running.** The scan is skipped
  quietly, because the entrypoint runs it at the next start and the certificate
  is applied then.
- **Both messages stop announcing a restart** that no longer happens, and say
  what is actually done.
- **An older container image keeps the restart.** A CLI carrying this change and
  an image predating it cannot run the scan, and a newly generated certificate is
  referenced by nothing, so nothing would ever apply it. The CLI detects this and
  restarts as it does today, rather than invoking a flag the old entrypoint would
  pass through to Traefik.

## Capabilities

### New Capabilities

- `certificate-application`: when a certificate the proxy serves is generated or
  removed, how and how quickly that change reaches Traefik, and what happens when
  the proxy is not running.

### Modified Capabilities

None. No existing spec describes certificate handling.

## Impact

- `build/traefik/entrypoint.sh`: the `--tls-only` guard, and the empty-certificate
  case in `generate_tls_config`.
- `bin/spark-http-proxy`: the `dc_cmd restart traefik` calls in `generate_mkcert`
  (line 669) and `remove_cert` (line 808). `dc_cmd restart` at line 1708 is the
  `restart` command itself and is unchanged.
- `README.md`: the note at line 553 telling users to run `docker compose restart`
  after generating a certificate manually.
- `test/test.sh`: coverage for the scan, the empty case, and the not-running path.
- Requires a rebuilt Traefik image, so the CLI must not assume the guard exists.
