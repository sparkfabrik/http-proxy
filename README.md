# Spark HTTP Proxy

[![GitHub Container Registry](https://img.shields.io/badge/ghcr.io-sparkfabrik%2Fhttp--proxy-blue)](https://ghcr.io/sparkfabrik/http-proxy)
[![CI Pipeline](https://github.com/sparkfabrik/http-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sparkfabrik/http-proxy/actions/workflows/ci.yml)

**Automatic HTTP routing for Docker containers** - A modern Traefik-based proxy that automatically discovers your containers and creates routing rules based on environment variables.

Perfect for local development environments, this proxy eliminates manual configuration by detecting containers with `VIRTUAL_HOST` environment variables and instantly making them accessible via custom domains.

> **Note**: This is a refactored and enhanced version of the [codekitchen/dinghy-http-proxy](https://github.com/codekitchen/dinghy-http-proxy) project. Spark HTTP Proxy is an HTTP Proxy and DNS server originally designed for [Dinghy](https://github.com/codekitchen/dinghy) but enhanced for broader use cases and improved maintainability.

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

## Testing

This project includes comprehensive integration tests that validate all HTTP proxy and DNS functionality using real Docker containers.

### Running Tests Locally

The easiest way to run all tests:

```bash
make test
```

Or directly run the test script:

```bash
# Run comprehensive integration tests
./test/test.sh

# Keep environment running for manual inspection
KEEP_RUNNING=true ./test/test.sh

# Run with debug output
DEBUG=true ./test/test.sh
```

### What Gets Tested

The integration test suite validates:

**HTTP Proxy Functionality:**

- ✅ Traefik label-based routing (`traefik.http.routers.*`)
- ✅ VIRTUAL_HOST environment variable routing
- ✅ VIRTUAL_PORT custom port handling
- ✅ Multiple domain configurations
- ✅ Container lifecycle events (start/stop/restart)

**DNS Server Functionality:**

- ✅ DNS resolution for `.spark.loc` domains
- ✅ Custom TLD support (configurable via `DOMAIN_TLD`)
- ✅ Real-time DNS updates when containers change

**End-to-End Scenarios:**

- ✅ Full stack startup and teardown
- ✅ Multiple container configurations
- ✅ HTTP accessibility via curl
- ✅ DNS resolution via dig
- ✅ Automatic cleanup and resource management

### Test Architecture

The test script (`test/test.sh`):

1. **Environment Setup**: Starts the full HTTP proxy stack with `docker-compose`
2. **Container Deployment**: Launches test containers with different routing configurations:
   - Traefik labels with `app1.spark.loc`
   - VIRTUAL_HOST with `app2.spark.loc`
   - VIRTUAL_HOST + VIRTUAL_PORT with `app3.spark.loc:8080`
3. **HTTP Testing**: Uses `curl` to verify each container is accessible via its domain
4. **DNS Testing**: Uses `dig` to verify DNS server resolves domains correctly
5. **Cleanup**: Automatically removes all test resources

### Continuous Integration

Tests run automatically in GitHub Actions on every push and pull request:

- **Dependencies**: Installs `curl` and `dnsutils` for testing
- **Build Validation**: Ensures Docker image builds successfully
- **Integration Tests**: Runs the full test suite via `make test`
- **Multi-Architecture**: Tests on `linux/amd64` and `linux/arm64`

See the [CI workflow](.github/workflows/ci.yml) for complete configuration.

### Manual Testing

For quick manual verification:

```bash
# Start the stack
docker-compose up -d

# Test a simple container
docker run -d --name test-app \
  -e VIRTUAL_HOST=test.spark.loc \
  nginx:alpine

# Verify HTTP access
curl -H "Host: test.spark.loc" http://localhost

# Verify DNS resolution
dig @127.0.0.1 -p 19322 test.spark.loc

# Cleanup
docker-compose down
docker rm -f test-app
```

For detailed testing documentation and troubleshooting, see [TEST_README.md](TEST_README.md).

## Troubleshooting

### Container not accessible?

1. **Check environment variables**: `docker inspect <container>` and look for `VIRTUAL_HOST`
2. **Check logs**: `docker logs http-proxy`
3. **Verify DNS**: Can you `ping myapp.local`?
4. **Test with curl**: `curl -H "Host: myapp.local" http://localhost`
5. **Run integration tests**: `make test` to verify the entire stack

### Common Issues

- **Port conflicts**: Make sure port 80 isn't used by another service
- **Docker socket**: Ensure `/var/run/docker.sock` is mounted
- **Network access**: Container must be on the same Docker network or default bridge
- **DNS issues**: Check if port 19322/udp is available for the DNS server

### Debug Mode

Enable verbose logging:

```bash
docker run -d \
  -e LOG_LEVEL=debug \
  -p 80:80 \
  -p 19322:19322/udp \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/sparkfabrik/http-proxy:latest
```

### Testing Individual Components

Test HTTP proxy only:

```bash
# Start just the HTTP proxy
docker-compose up http-proxy

# Test with a simple container
docker run -d --name test \
  -e VIRTUAL_HOST=test.spark.loc \
  nginx:alpine

curl -H "Host: test.spark.loc" http://localhost
```

Test DNS server only:

```bash
# Start the full stack
docker-compose up -d

# Test DNS resolution
dig @127.0.0.1 -p 19322 any-domain.spark.loc

# Should return 127.0.0.1 for any .spark.loc domain
```

## License

MIT License - see [LICENSE](LICENSE) file for details.

---

**Need help?** [Open an issue](https://github.com/sparkfabrik/http-proxy/issues) or check the [discussions](https://github.com/sparkfabrik/http-proxy/discussions).
