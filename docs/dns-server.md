# DNS Server Configuration and Usage

The HTTP proxy includes a **built-in DNS server** that automatically resolves configured domains to localhost, eliminating the need to manually edit `/etc/hosts` or configure system DNS.

## Features

- **TLD Support**: Handle any domain with specific top-level domains (e.g., `*.loc`, `*.dev`)
- **Specific Domain Support**: Handle only explicitly configured domains
- **Mixed Configuration**: Support both TLDs and specific domains simultaneously
- **Configurable Target**: Resolve domains to any IP address (default: `127.0.0.1`)
- **Standard DNS Protocol**: Works with all DNS clients and system resolvers

## Configuration

### Environment Variables

| Variable                   | Default     | Description                                                |
| -------------------------- | ----------- | ---------------------------------------------------------- |
| `HTTP_PROXY_DNS_TLDS`      | `loc`       | Comma-separated list of TLDs or specific domains to handle |
| `HTTP_PROXY_DNS_TARGET_IP` | `127.0.0.1` | IP address to resolve all configured domains to            |
| `HTTP_PROXY_DNS_PORT`      | `19322`     | UDP port for the DNS server to listen on                   |

### Docker Compose Configuration

```yaml
services:
  dns:
    image: ghcr.io/sparkfabrik/http-proxy-services:latest
    environment:
      - HTTP_PROXY_HTTP_PROXY_DNS_TLDS=${DNS_TLDS:-loc}
      - HTTP_PROXY_DNS_TARGET_IP=${DNS_TARGET_IP:-127.0.0.1}
      - HTTP_PROXY_DNS_PORT=${DNS_PORT:-19322}
    ports:
      - "19322:19322/udp"
```

## Usage Patterns

### 1. TLD Support (Recommended)

Handle any subdomain of specific top-level domains:

```bash
# Configuration
HTTP_PROXY_HTTP_PROXY_DNS_TLDS=loc

# Resolves:
✅ myapp.loc → 127.0.0.1
✅ api.loc → 127.0.0.1
✅ anything.loc → 127.0.0.1
✅ sub.domain.loc → 127.0.0.1

# Does not resolve:
❌ myapp.dev → Not handled
❌ example.com → Not handled
```

### 2. Multiple TLDs

Support multiple development environments:

```bash
# Configuration
HTTP_PROXY_HTTP_PROXY_DNS_TLDS=loc,dev,docker

# Resolves:
✅ myapp.loc → 127.0.0.1
✅ api.dev → 127.0.0.1
✅ service.docker → 127.0.0.1
```

### 3. Specific Domains

Handle only explicitly configured domains:

```bash
# Configuration
HTTP_PROXY_HTTP_PROXY_DNS_TLDS=spark.loc,api.dev

# Resolves:
✅ spark.loc → 127.0.0.1
✅ api.dev → 127.0.0.1

# Does not resolve:
❌ other.loc → Not handled
❌ spark.dev → Not handled
❌ api.loc → Not handled
```

### 4. Mixed Configuration

Combine TLDs and specific domains:

```bash
# Configuration
HTTP_PROXY_HTTP_PROXY_DNS_TLDS=loc,myproject.dev

# Resolves:
✅ anything.loc → 127.0.0.1      # TLD match
✅ myproject.dev → 127.0.0.1     # Specific domain match

# Does not resolve:
❌ other.dev → Not handled       # Not the specific domain
❌ anything.com → Not handled    # Different TLD
```

## System Integration

### macOS - Domain-Specific Resolution (Recommended)

Configure your system to use the DNS server only for specific domains:

```bash
# Create resolver directory
sudo mkdir -p /etc/resolver

# Configure .loc domains
echo "nameserver 127.0.0.1" | sudo tee /etc/resolver/loc
echo "port 19322" | sudo tee -a /etc/resolver/loc

# Configure .dev domains (if using multiple TLDs)
echo "nameserver 127.0.0.1" | sudo tee /etc/resolver/dev
echo "port 19322" | sudo tee -a /etc/resolver/dev
```

### Linux - systemd-resolved (Recommended)

Configure systemd-resolved to use the DNS server for specific domains:

```bash
# Configure systemd-resolved to use http-proxy DNS for .loc domains
sudo mkdir -p /etc/systemd/resolved.conf.d
sudo tee /etc/systemd/resolved.conf.d/http-proxy.conf > /dev/null <<EOF
[Resolve]
DNS=172.17.0.1:19322
Domains=~loc
EOF

# For multiple TLDs, add them to the Domains line:
# Domains=~loc ~dev ~docker

# Restart systemd-resolved to apply changes
sudo systemctl restart systemd-resolved

# Verify configuration
systemd-resolve --status
```

## Testing and Verification

### Command Line Testing

```bash
# Test with dig
dig @127.0.0.1 -p 19322 myapp.loc

# Test with nslookup
nslookup myapp.loc 127.0.0.1 19322

# Test with host
host myapp.loc 127.0.0.1 -p 19322
```

### Application Testing

```bash
# Test HTTP access with custom DNS
curl --dns-servers 127.0.0.1:19322 http://myapp.loc

# Test with custom hosts file (alternative)
echo "127.0.0.1 myapp.loc" >> /etc/hosts
curl http://myapp.loc
```

### Browser Testing

1. Configure system DNS as described above
2. Open browser and navigate to `http://myapp.loc`
3. Should resolve without editing hosts file

## Troubleshooting

### DNS Server Not Responding

```bash
# Check if DNS server is running
docker compose ps dns

# Check DNS server logs
docker compose logs dns

# Test DNS server accessibility
nc -u -v 127.0.0.1 19322
```

### Domain Not Resolving

```bash
# Verify DNS server configuration
docker compose exec dns env | grep DNS

# Test specific domain directly
dig @127.0.0.1 -p 19322 your-domain.loc

# Check system DNS configuration
scutil --dns | grep 127.0.0.1                    # macOS
systemd-resolve --status | grep "172.17.0.1"    # Linux

# Test systemd-resolved configuration (Linux)
resolvectl query your-domain.loc                 # Should use configured DNS
resolvectl status                                 # Show DNS servers per interface
```

### Performance Considerations

- The DNS server is designed for development use
- It handles standard DNS queries efficiently
- For high-traffic scenarios, consider using system hosts file instead
- Response time is typically < 1ms for configured domains

## Security Notes

- The DNS server only resolves configured domains
- Unknown domains are rejected (NXDOMAIN response)
- All configured domains resolve to the same target IP
- No DNS forwarding or recursive resolution is performed
- Suitable for development environments only

## Integration Examples

### Development Stack

```yaml
# docker-compose.yml
services:
  dns:
    environment:
      - HTTP_PROXY_HTTP_PROXY_DNS_TLDS=myproject.loc
      - HTTP_PROXY_DNS_TARGET_IP=127.0.0.1

  web:
    environment:
      - VIRTUAL_HOST=app.myproject.loc

  api:
    environment:
      - VIRTUAL_HOST=api.myproject.loc
```

### Multi-Environment Setup

```yaml
# docker-compose.yml
services:
  dns:
    environment:
      - HTTP_PROXY_HTTP_PROXY_DNS_TLDS=dev,staging,loc
      - HTTP_PROXY_DNS_TARGET_IP=127.0.0.1

  # Development
  app-dev:
    environment:
      - VIRTUAL_HOST=myapp.dev

  # Staging
  app-staging:
    environment:
      - VIRTUAL_HOST=myapp.staging
```

## Advanced Configuration

### Custom Target IP

Point domains to a different IP address:

```yaml
services:
  dns:
    environment:
      - HTTP_PROXY_HTTP_PROXY_DNS_TLDS=loc
      - HTTP_PROXY_DNS_TARGET_IP=192.168.1.100 # Point to another machine
```

### Custom Port

Run DNS server on a different port:

```yaml
services:
  dns:
    environment:
      - HTTP_PROXY_DNS_PORT=5353
    ports:
      - "5353:5353/udp"
```

### Health Checks

Monitor DNS server health:

```yaml
services:
  dns:
    healthcheck:
      test: ["CMD", "dig", "@127.0.0.1", "-p", "19322", "health.loc", "+short"]
      interval: 30s
      timeout: 10s
      retries: 3
```
