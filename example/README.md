# HTTP Proxy Stack Example

This directory contains example configurations for using the http-proxy stack with pre-built images from GitHub Container Registry.

## Available Images

### Stable Release Images (recommended)

- **`ghcr.io/sparkfabrik/http-proxy-traefik:latest`** - Traefik HTTP proxy
- **`ghcr.io/sparkfabrik/http-proxy-services:latest`** - Background services (dinghy-layer, join-networks, dns-server)

### Development Images (for testing)

Development images are built from feature branches and include:

- **`ghcr.io/sparkfabrik/http-proxy-traefik:<branch-name>`** - Latest from branch
- **`ghcr.io/sparkfabrik/http-proxy-traefik:<branch-name>-<sha>`** - Specific commit
- **`ghcr.io/sparkfabrik/http-proxy-services:<branch-name>`** - Latest from branch
- **`ghcr.io/sparkfabrik/http-proxy-services:<branch-name>-<sha>`** - Specific commit

To use development images, update the `compose.yml` tags accordingly:

```yaml
# Example: Use images from 'feature/new-routing' branch
services:
  dinghy_layer:
    image: ghcr.io/sparkfabrik/http-proxy-services:feature-new-routing
  # ... other services
  traefik:
    image: ghcr.io/sparkfabrik/http-proxy-traefik:feature-new-routing
```

## Files

- **`compose.yml`** - The main HTTP proxy stack using stable published images
- **`compose.examples.yml`** - Example applications demonstrating different routing configurations
- **`html/index.html`** - Sample HTML file for the nginx example

## Quick Start

### 1. Basic HTTP Proxy

```bash
# Using Docker Compose directly
docker compose up -d

# Using the convenience script
./bin/spark-http-proxy start

# Check status
docker compose ps
```

### 2. HTTP Proxy with Monitoring

```bash
# Using Docker Compose with profiles
docker compose --profile metrics up -d

# Using the convenience script (recommended)
./bin/spark-http-proxy start-with-metrics

# Check all services including monitoring
docker compose ps
```

### 3. Access Services

**Basic Stack:**

- **Traefik Dashboard**: <http://localhost:8080>

**With Monitoring:**

- **Traefik Dashboard**: <http://localhost:8080>
- **Grafana Dashboard**: <http://localhost:30001> (admin/admin)
- **Prometheus**: <http://localhost:9090>

### 4. Convenience Commands

```bash
# Open dashboards in browser
./bin/spark-http-proxy dashboard    # Traefik
./bin/spark-http-proxy grafana      # Grafana (if running)
./bin/spark-http-proxy prometheus   # Prometheus (if running)

# Stop only monitoring services (keep proxy running)
./bin/spark-http-proxy stop-metrics
```

### 5. Start Example Applications (Optional)

```bash
# Start example applications
docker compose -f compose.examples.yml up -d

# Check all services
docker compose -f compose.examples.yml ps
```

**Example Apps Access:**

- <http://whoami-traefik.docker>
- <http://whoami-virtual.docker>
- <http://whoami-custom.docker>
- <http://whoami-multi1.docker> and <http://whoami-multi2.docker>
- <http://nginx.docker> and <http://www.nginx.docker>
- <http://whoami-https.docker> and <https://whoami-https.docker> (HTTPS example)

### DNS Forwarding Configuration

By default, the DNS server operates in purely authoritative mode for the managed TLD (e.g., `.docker`) and returns REFUSED for external domains. This ensures security by not forwarding external DNS queries.

To enable forwarding to upstream DNS servers for external domains:

```bash
# Set environment variable
HTTP_PROXY_DNS_FORWARD_ENABLED=true

# In docker-compose.yml
services:
  dns:
    environment:
      - HTTP_PROXY_DNS_FORWARD_ENABLED=true
```

**Behavior:**

- `HTTP_PROXY_DNS_FORWARD_ENABLED=false` (default): Return REFUSED for external domains
- `HTTP_PROXY_DNS_FORWARD_ENABLED=true`: Forward external domains to upstream DNS

**Example:**

```bash
# With forwarding disabled (default)
❯ dig @127.0.0.1 -p 19322 google.com
# Returns: status: REFUSED

# With forwarding enabled
❯ dig @127.0.0.1 -p 19322 google.com
# Returns: google.com IP addresses
```

## DNS Configuration

To resolve `.docker` domains, configure your system DNS to use the proxy's DNS server:

```bash
# Add to /etc/resolver/docker (macOS)
nameserver 127.0.0.1
port 19322

# Or add to /etc/resolv.conf (Linux)
nameserver 127.0.0.1:19322
```

## Configuration Methods

The example applications demonstrate different ways to configure routing:

### 1. Traefik Labels (Recommended)

```yaml
services:
  myapp:
    image: myapp:latest
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.myapp.rule=Host(`myapp.docker`)"
      - "traefik.http.routers.myapp.entrypoints=http"
```

### 2. VIRTUAL_HOST Environment Variable

```yaml
services:
  myapp:
    image: myapp:latest
    environment:
      - VIRTUAL_HOST=myapp.docker
      - VIRTUAL_PORT=8080 # Optional, defaults to 80
```

### 3. Multi-domain VIRTUAL_HOST

```yaml
services:
  myapp:
    image: myapp:latest
    environment:
      - VIRTUAL_HOST=myapp.docker,api.myapp.docker,www.myapp.docker
```

## Environment Variables

The HTTP proxy stack supports several environment variables for configuration:

### LOG_LEVEL

Controls the logging verbosity for all proxy services. Supported levels:

- **`debug`** - Detailed debugging information (most verbose)
- **`info`** - General operational messages (default)
- **`warn`** - Warning messages only
- **`error`** - Error messages only (least verbose)

#### Usage Examples

**Set globally for all services:**

```bash
# Set log level for the entire stack
export LOG_LEVEL=debug
docker compose up -d
```

**Set per service in compose file:**

```yaml
services:
  dinghy_layer:
    image: ghcr.io/sparkfabrik/http-proxy-services:latest
    environment:
      - LOG_LEVEL=debug
    # ... other configuration

  dns:
    image: ghcr.io/sparkfabrik/http-proxy-services:latest
    environment:
      - LOG_LEVEL=warn
    # ... other configuration
```

**With docker run:**

```bash
docker run -e LOG_LEVEL=debug ghcr.io/sparkfabrik/http-proxy-services:latest
```

### Other Environment Variables

- **`LOG_FORMAT`** - Set to `json` for structured JSON logging (default: text)
- **`HTTP_PROXY_DNS_UPSTREAM_SERVERS`** - Comma-separated list of upstream DNS servers for forwarding (default: 8.8.8.8:53,1.1.1.1:53)
- **`HTTP_PROXY_DNS_FORWARD_ENABLED`** - Enable/disable DNS forwarding for external domains (default: false)
- **`DRY_RUN`** - Set to `true` to enable dry-run mode for dinghy-layer service

## Adding Your Own Services

To add your own services to be proxied:

1. **Using a separate compose file** (recommended):

   ```yaml
   services:
     myapp:
       image: myapp:latest
       environment:
         - VIRTUAL_HOST=myapp.docker

   networks:
     default:
       name: http-proxy_default
       external: true
   ```

2. **Or add to the main compose.yml** and restart the stack.

## Cleanup

```bash
# Stop example applications
docker compose -f compose.examples.yml down

# Stop the proxy stack
docker compose down

# Remove volumes (optional)
docker compose down -v
```

## Troubleshooting

### Service not accessible

1. Check if the container is running: `docker compose ps`
2. Verify DNS resolution: `dig myapp.docker @127.0.0.1 -p 19322`
3. Check Traefik dashboard: <http://localhost:8080>
4. View logs: `docker compose logs service-name`

### DNS not working

1. Verify DNS server is running: `docker compose ps dns`
2. Test DNS server: `dig test.docker @127.0.0.1 -p 19322`
3. Check system DNS configuration

### Debugging with LOG_LEVEL

For detailed troubleshooting, increase the logging verbosity:

```bash
# Enable debug logging for all services
LOG_LEVEL=debug docker compose up -d

# View detailed logs
docker compose logs -f

# Or for specific services
docker compose logs -f dns
docker compose logs -f dinghy_layer
```

For more troubleshooting information, see the main project README.
docker compose logs -f

````bash
## Cleanup

```bash
# Stop and remove containers
docker compose down

# Remove volumes as well
docker compose down -v
````

## Notes

- The proxy automatically discovers new containers with `VIRTUAL_HOST` or Traefik labels
- No restart required when adding new services
- DNS server provides automatic resolution for `.docker` domains
- Traefik dashboard shows all configured routes and services
- All infrastructure services are excluded from Traefik discovery (`traefik.enable=false`)
