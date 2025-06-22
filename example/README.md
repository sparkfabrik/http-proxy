# HTTP Proxy Stack Example

This directory contains example configurations for using the http-proxy stack with pre-built images from GitHub Container Registry.

## Files

- **`compose.yml`** - The main HTTP proxy stack using published images
- **`compose.examples.yml`** - Example applications demonstrating different routing configurations
- **`html/index.html`** - Sample HTML file for the nginx example

## Quick Start

### 1. Start the HTTP Proxy Stack

```bash
# Start the core proxy services
docker compose up -d

# Check status
docker compose ps

# View logs
docker compose logs -f
```

### 2. Start Example Applications (Optional)

```bash
# Start example applications
docker compose -f compose.examples.yml up -d

# Check all services
docker compose -f compose.examples.yml ps
```

### 3. Access Services

- **Traefik Dashboard**: <http://localhost:8080>
- **Example Apps** (if started):
  - <http://whoami-traefik.docker>
  - <http://whoami-virtual.docker>
  - <http://whoami-custom.docker>
  - <http://whoami-multi1.docker> and <http://whoami-multi2.docker>
  - <http://nginx.docker> and <http://www.nginx.docker>

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
      - "traefik.http.routers.myapp.entrypoints=web"
```

### 2. VIRTUAL_HOST Environment Variable
```yaml
services:
  myapp:
    image: myapp:latest
    environment:
      - VIRTUAL_HOST=myapp.docker
      - VIRTUAL_PORT=8080  # Optional, defaults to 80
```

### 3. Multi-domain VIRTUAL_HOST
```yaml
services:
  myapp:
    image: myapp:latest
    environment:
      - VIRTUAL_HOST=myapp.docker,api.myapp.docker,www.myapp.docker
```

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

For more troubleshooting information, see the main project README.
docker compose logs -f
```

## Cleanup

```bash
# Stop and remove containers
docker compose down

# Remove volumes as well
docker compose down -v
```

## Notes

- The proxy automatically discovers new containers with `VIRTUAL_HOST` or Traefik labels
- No restart required when adding new services
- DNS server provides automatic resolution for `.docker` domains
- Traefik dashboard shows all configured routes and services
- All infrastructure services are excluded from Traefik discovery (`traefik.enable=false`)
