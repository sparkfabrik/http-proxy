# Spark HTTP Proxy

[![GitHub Container Registry](https://img.shields.io/badge/ghcr.io-sparkfabrik%2Fhttp--proxy-blue)](https://ghcr.io/sparkfabrik/http-proxy)
[![CI Pipeline](https://github.com/sparkfabrik/http-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sparkfabrik/http-proxy/actions/workflows/ci.yml)

**Automatic HTTP routing for Docker containers** - A modern Traefik-based proxy that automatically discovers your containers and creates routing rules based on environment variables.

Perfect for local development environments, this proxy eliminates manual configuration by detecting containers with `VIRTUAL_HOST` environment variables and instantly making them accessible via custom domains.

> **Note**: This is a refactored and enhanced version of the [codekitchen/dinghy-http-proxy](https://github.com/codekitchen/dinghy-http-proxy) project. Spark HTTP Proxy is an HTTP Proxy and DNS server originally designed for [Dinghy](https://github.com/codekitchen/dinghy) but enhanced for broader use cases and improved maintainability. The proxy is based on jwilder's excellent [nginx-proxy](https://github.com/jwilder/nginx-proxy) project, with modifications to make it more suitable for local development work.

## Quick Start

### 1. Start the Proxy

```bash
docker run -d --name http-proxy \
  -p 80:80 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/sparkfabrik/http-proxy:latest
```

### 2. Run Your Application

```bash
docker run -d \
  -e VIRTUAL_HOST=myapp.local \
  nginx:alpine
```

### 3. Access Your App

Visit `http://myapp.local` in your browser - it just works! 🎉

## Container Configuration

Add these environment variables to any container you want to be automatically routed:

```yaml
# docker-compose.yml
services:
  myapp:
    image: nginx:alpine
    environment:
      - VIRTUAL_HOST=myapp.local           # Required: your custom domain
      - VIRTUAL_PORT=8080                  # Optional: defaults to exposed port or 80
    expose:
      - "8080"
```

### Supported Patterns

- **Single domain**: `VIRTUAL_HOST=myapp.local`
- **Multiple domains**: `VIRTUAL_HOST=app.local,api.local`
- **Wildcards**: `VIRTUAL_HOST=*.myapp.local`
- **Regex patterns**: `VIRTUAL_HOST=~^api\\..*\\.local$`

## Complete Setup with Docker Compose

```yaml
version: '3.8'

services:
  # HTTP Proxy
  http-proxy:
    image: ghcr.io/sparkfabrik/http-proxy:latest
    ports:
      - "80:80"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped

  # Your applications
  web:
    image: nginx:alpine
    environment:
      - VIRTUAL_HOST=web.local

  api:
    image: node:alpine
    environment:
      - VIRTUAL_HOST=api.local
      - VIRTUAL_PORT=3000
    expose:
      - "3000"
```

Start everything: `docker-compose up -d`

Access your apps:

- `http://web.local` → nginx container
- `http://api.local` → node container

## How It Works

The proxy uses a **dinghy compatibility layer** that:

1. **Monitors Docker Events** - Detects when containers start/stop
2. **Scans Environment Variables** - Looks for `VIRTUAL_HOST` in container config
3. **Generates Traefik Config** - Creates routing rules automatically
4. **Updates Routes Instantly** - Your apps become accessible immediately

### Reliability Features

- **Real-time Events**: Instant response when containers change
- **Periodic Scanning**: Safety net that catches missed containers (every 30s)
- **Auto-recovery**: Reconnects to Docker if connection is lost
- **Graceful Cleanup**: Removes routes when containers stop

## Configuration

Set these environment variables on the proxy container:

```bash
# Optional configuration
CHECK_INTERVAL=30s      # How often to scan for missed containers
LOG_LEVEL=info          # debug, info, warn, error
DRY_RUN=false          # Test mode - doesn't make actual changes
```

## DNS Setup (Optional)

For `.local` domains to work automatically, add this DNS server:

```bash
# Add DNS server to the proxy
docker run -d --name http-proxy \
  -p 80:80 \
  -p 53:53/udp \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/sparkfabrik/http-proxy:latest
```

Then configure your system to use `127.0.0.1` as DNS server for `.local` domains.

## Migration from nginx-proxy

This proxy is a **drop-in replacement** for nginx-proxy. Simply:

1. Stop your nginx-proxy container
2. Start this proxy with the same port mappings
3. Your existing containers with `VIRTUAL_HOST` will work immediately

No configuration changes needed! 🚀

## Troubleshooting

### Container not accessible?

1. **Check environment variables**: `docker inspect <container>` and look for `VIRTUAL_HOST`
2. **Check logs**: `docker logs http-proxy`
3. **Verify DNS**: Can you `ping myapp.local`?
4. **Test with curl**: `curl -H "Host: myapp.local" http://localhost`

### Common Issues

- **Port conflicts**: Make sure port 80 isn't used by another service
- **Docker socket**: Ensure `/var/run/docker.sock` is mounted
- **Network access**: Container must be on the same Docker network or default bridge

### Debug Mode

Enable verbose logging:

```bash
docker run -d \
  -e LOG_LEVEL=debug \
  -p 80:80 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/sparkfabrik/http-proxy:latest
```

## License

MIT License - see [LICENSE](LICENSE) file for details.

---

**Need help?** [Open an issue](https://github.com/sparkfabrik/http-proxy/issues) or check the [discussions](https://github.com/sparkfabrik/http-proxy/discussions).
