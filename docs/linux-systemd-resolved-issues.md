# Linux systemd-resolved DNS Limitations

## Overview

When using `spark-http-proxy configure-dns` on Linux systems with systemd-resolved, you may notice some DNS queries for external domains being sent to the HTTP proxy's DNS server.

## What You Might See

The `configure-dns` command creates systemd-resolved configuration to route specific domains (like `*.loc`) to the HTTP proxy's DNS server. However, due to systemd-resolved's DNS routing behavior, you might occasionally see:

- `REFUSED` responses in DNS server logs for external domains like `api.github.com`
- All DNS queries being sent to the HTTP proxy DNS server instead of just local domains

**Important**: This does not affect the functionality of the HTTP proxy. Your local domains (like `myapp.loc`) will still resolve correctly and your applications will work as expected.

## Why This Happens

This is due to limitations in how systemd-resolved handles domain-specific DNS routing. The HTTP proxy's DNS server correctly responds with `REFUSED` for domains it doesn't handle, and your system falls back to other DNS servers for external domains.

## Solutions

### Accept Current Behavior (Recommended)

The `REFUSED` responses are the correct behavior - your DNS server should only handle the domains it's configured for. External domains will be resolved by your system's fallback DNS servers automatically. This is working as intended.

## Verification

### Check DNS Server Logs

```bash
# View DNS server logs to see query patterns
docker compose logs dns

# Monitor for REFUSED responses
docker compose logs dns | grep REFUSED
```

### Test DNS Resolution

```bash
# Check systemd-resolved status
resolvectl status

# Test local domain resolution
resolvectl query myapp.loc

# Test external domain resolution
resolvectl query google.com
```

## Expected Behavior

### With systemd-resolved Configuration

- Local domains resolve to `127.0.0.1`
- External domains may show `REFUSED` responses in DNS logs
- External domains eventually resolve through fallback mechanisms
- HTTP proxy functionality remains unaffected

The HTTP proxy's core functionality (HTTP/HTTPS routing) is unaffected by these DNS routing quirks.
