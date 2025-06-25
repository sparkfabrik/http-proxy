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

## CORS Support

The proxy supports Cross-Origin Resource Sharing (CORS) through environment variables:

```yaml
services:
  myapi:
    image: myapi:latest
    environment:
      - VIRTUAL_HOST=api.local
      - CORS_ENABLED=true  # Enables CORS for all origins
```

When `CORS_ENABLED=true` is set, the following CORS headers are automatically added:

- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Methods: GET, OPTIONS, PUT, POST, DELETE, PATCH`
- `Access-Control-Allow-Headers: *`
- `Access-Control-Allow-Credentials: true`
- `Access-Control-Max-Age: 86400`

### Using Traefik Labels for CORS

For more granular control, you can also enable CORS using Traefik labels instead of the `CORS_ENABLED` environment variable:

```yaml
services:
  myapi:
    image: myapi:latest
    environment:
      - VIRTUAL_HOST=api.local  # Creates HTTP and HTTPS routes
    labels:
      # Define CORS middleware
      - "traefik.http.middlewares.api-cors.headers.accesscontrolalloworiginlist=*"
      - "traefik.http.middlewares.api-cors.headers.accesscontrolallowmethods=GET,OPTIONS,PUT,POST,DELETE,PATCH"
      - "traefik.http.middlewares.api-cors.headers.accesscontrolallowheaders=*"
      - "traefik.http.middlewares.api-cors.headers.accesscontrolallowcredentials=true"
      - "traefik.http.middlewares.api-cors.headers.accesscontrolmaxage=86400"

      # Apply CORS middleware to both HTTP and HTTPS routes
      - "traefik.http.routers.api-http.middlewares=api-cors"
      - "traefik.http.routers.api-https.middlewares=api-cors"
      - "traefik.http.routers.api-http.rule=Host(`api.local`)"
      - "traefik.http.routers.api-https.rule=Host(`api.local`)"
      - "traefik.http.routers.api-http.entrypoints=http"
      - "traefik.http.routers.api-https.entrypoints=https"
      - "traefik.http.routers.api-https.tls=true"
      - "traefik.http.services.api.loadbalancer.server.port=80"
```

This approach gives you full control over CORS configuration and allows for domain-specific settings.

## HTTPS Support

The proxy automatically exposes both HTTP and HTTPS for all applications configured with `VIRTUAL_HOST`. Both protocols are available without any additional configuration.

### Automatic HTTP and HTTPS Routes

When you set `VIRTUAL_HOST=myapp.local`, you automatically get:

- **HTTP**: `http://myapp.local` (port 80)
- **HTTPS**: `https://myapp.local` (port 443)

```yaml
services:
  myapp:
    image: nginx:alpine
    environment:
      - VIRTUAL_HOST=myapp.local  # Creates both HTTP and HTTPS routes automatically
```

### Self-Signed Certificates

Traefik automatically generates self-signed certificates for HTTPS routes. For trusted certificates in development, you can use mkcert to generate wildcard certificates.

### Trusted Local Certificates with mkcert

For browser-trusted certificates without warnings, generate wildcard certificates using [mkcert](https://github.com/FiloSottile/mkcert) (install with `brew install mkcert` on macOS):

```bash
# Install the local CA
mkcert -install

# Create the certificates directory
mkdir -p ~/.config/spark/http-proxy/certs

# Generate wildcard certificate for .loc domains
mkcert -cert-file ~/.config/spark/http-proxy/certs/wildcard.loc.pem \
       -key-file ~/.config/spark/http-proxy/certs/wildcard.loc-key.pem \
       "*.loc"

# For complex multi-level domains, you can generate additional certificates:
# mkcert -cert-file ~/.config/spark/http-proxy/certs/sparkfabrik.loc.pem \
#        -key-file ~/.config/spark/http-proxy/certs/sparkfabrik.loc-key.pem \
#        "*.sparkfabrik.loc"
```

#### Start the proxy

The certificates will be automatically detected and loaded when you start the proxy:

```bash
docker compose up -d
```

The Traefik container's entrypoint script scans `~/.config/spark/http-proxy/certs/` for certificate files and automatically generates the TLS configuration in `/traefik/dynamic/auto-tls.yml`. You don't need to manually edit any configuration files!

Now your `.loc` domains will use trusted certificates! 🎉

✅ `https://myapp.loc` - Trusted
✅ `https://api.loc` - Trusted
✅ `https://project.loc` - Trusted

**Note**: The `*.loc` certificate covers single-level subdomains. For multi-level domains like `app.project.sparkfabrik.loc`, generate additional certificates as shown in the commented example above.

#### How Certificate Matching Works

Traefik automatically matches certificates to incoming HTTPS requests using **SNI (Server Name Indication)**:

1. **Certificate Detection**: The entrypoint script scans `/traefik/certs` and extracts domain information from each certificate's Subject Alternative Names (SAN)
2. **Automatic Matching**: When a browser requests `https://myapp.loc`, Traefik:
   - Receives the domain name via SNI
   - Looks through available certificates for one that matches `myapp.loc`
   - Finds the `*.loc` wildcard certificate and uses it
   - Serves the HTTPS response with the trusted certificate

3. **Wildcard Coverage**:
   - `*.loc` covers: `myapp.loc`, `api.loc`, `database.loc`
   - `*.loc` does NOT cover: `sub.myapp.loc`, `api.project.loc`
   - For multi-level domains, generate specific certificates like `*.project.loc`

4. **Fallback**: If no matching certificate is found, Traefik generates a self-signed certificate for that domain

You can see which domains each certificate covers in the container logs when it starts up.

### Using Traefik Labels Instead of VIRTUAL_HOST

If you prefer to use Traefik labels instead of `VIRTUAL_HOST`, you can achieve the same HTTP and HTTPS routes manually:

```yaml
services:
  myapp:
    image: nginx:alpine
    labels:
      # HTTP router
      - "traefik.http.routers.myapp.rule=Host(`myapp.local`)"
      - "traefik.http.routers.myapp.entrypoints=http"
      - "traefik.http.routers.myapp.service=myapp"

      # HTTPS router
      - "traefik.http.routers.myapp-tls.rule=Host(`myapp.local`)"
      - "traefik.http.routers.myapp-tls.entrypoints=https"
      - "traefik.http.routers.myapp-tls.tls=true"
      - "traefik.http.routers.myapp-tls.service=myapp"

      # Service configuration
      - "traefik.http.services.myapp.loadbalancer.server.port=80"
```

This manual approach gives you the same result as `VIRTUAL_HOST=myapp.local` but with more control over the configuration.

## Advanced Configuration with Traefik Labels

While `VIRTUAL_HOST` environment variables provide simple automatic routing, you can also use **Traefik labels** for more advanced configuration. Both methods work together seamlessly.

### Basic Traefik Labels Example

```yaml
services:
  myapp:
    image: nginx:alpine
    labels:
      # Define the routing rule - which domain/path routes to this service
      - "traefik.http.routers.myapp.rule=Host(`myapp.docker`)"

      # Specify which entrypoint to use (http = port 80)
      - "traefik.http.routers.myapp.entrypoints=http"

      # Set the target port for load balancing
      - "traefik.http.services.myapp.loadbalancer.server.port=80"
```

> **Note**: `traefik.enable=true` is **not required** since auto-discovery is always enabled in this proxy.

### Traefik Labels Breakdown

| Label | Purpose | Example |
|-------|---------|---------|
| **Router Rule** | Defines which requests route to this service | `traefik.http.routers.myapp.rule=Host(\`myapp.docker\`)` |
| **Entrypoints** | Which proxy port to listen on | `traefik.http.routers.myapp.entrypoints=http` |
| **Service Port** | Target port on the container | `traefik.http.services.myapp.loadbalancer.server.port=8080` |

### Understanding Traefik Core Concepts

To effectively use Traefik labels, it helps to understand the key concepts:

#### **Entrypoints** - The "Front Door"
An **entrypoint** is where Traefik listens for incoming traffic. Think of it as the "front door" of your proxy.

```yaml
# In our Traefik configuration:
entrypoints:
  http:              # ← This is just a custom name! You can call it anything
    address: ":80"    # Listen on port 80 for HTTP traffic
  websecure:        # ← Another custom name
    address: ":443"   # Listen on port 443 for HTTPS traffic (if configured)
  api:              # ← You could even call it "api" or "http" or "frontend"
    address: ":8080"  # Listen on port 8080
```

**Important**: `http` is just a **custom name** that we chose. You could name your entrypoints anything:
- `http`, `https`, `frontend`, `api`, `public` - whatever makes sense to you!

When you specify `traefik.http.routers.myapp.entrypoints=http`, you're telling Traefik:
> *"Route requests that come through the entrypoint named 'http' (which happens to be port 80) to my application"*

The entrypoint name must match between:
1. **Traefik configuration** (where you define `web: address: ":80"`)
2. **Container labels** (where you reference `entrypoints=web`)

#### **Load Balancer** - The "Traffic Director"
The **load balancer** determines how traffic gets distributed to your actual application containers.

```yaml
# This label creates a load balancer configuration:
- "traefik.http.services.myapp.loadbalancer.server.port=8080"
```

This tells Traefik:
> *"When routing to this service, send traffic to port 8080 on the container"*

#### **The Complete Flow**

Here's how a request flows through Traefik:

```
1. [Browser] → http://myapp.docker
                    ↓
2. [Entrypoint :80] ← "web" entrypoint receives the request
                    ↓
3. [Router] ← Checks rule: Host(`myapp.docker`) ✓ Match!
                    ↓
4. [Service] ← Routes to the configured service
                    ↓
5. [Load Balancer] ← Forwards to container port 8080
                    ↓
6. [Container] ← Your app receives the request
```

#### **Advanced Load Balancer Features**

While we typically use simple port mapping, Traefik's load balancer supports much more:

```yaml
services:
  # Multiple container instances (automatic load balancing)
  web-app:
    image: nginx:alpine
    deploy:
      replicas: 3  # 3 instances of the same app
    labels:
      - "traefik.http.routers.webapp.rule=Host(`webapp.docker`)"
      - "traefik.http.routers.webapp.entrypoints=web"
      # Traefik automatically balances between all 3 instances!

  # Health check configuration
  api-service:
    image: myapi:latest
    labels:
      - "traefik.http.routers.api.rule=Host(`api.docker`)"
      - "traefik.http.routers.api.entrypoints=web"
      - "traefik.http.services.api.loadbalancer.server.port=3000"
      # Configure health checks
      - "traefik.http.services.api.loadbalancer.healthcheck.path=/health"
      - "traefik.http.services.api.loadbalancer.healthcheck.interval=30s"
```

#### **Why This Architecture Matters**

This separation of concerns provides powerful flexibility:

- **Entrypoints**: Control *where* Traefik listens (ports, protocols)
- **Routers**: Control *which* requests go *where* (domains, paths, headers)
- **Services**: Control *how* traffic reaches your apps (ports, health checks, load balancing)

Example of advanced routing:

```yaml
services:
  # Same app, different routing based on subdomain
  app-v1:
    image: myapp:v1
    labels:
      - "traefik.http.routers.app-v1.rule=Host(`v1.myapp.docker`)"
      - "traefik.http.routers.app-v1.entrypoints=web"
      - "traefik.http.services.app-v1.loadbalancer.server.port=8080"

  app-v2:
    image: myapp:v2
    labels:
      - "traefik.http.routers.app-v2.rule=Host(`v2.myapp.docker`)"
      - "traefik.http.routers.app-v2.entrypoints=web"
      - "traefik.http.services.app-v2.loadbalancer.server.port=8080"

  # Route 90% traffic to v1, 10% to v2 (canary deployment)
  app-main:
    image: myapp:v1
    labels:
      - "traefik.http.routers.app-main.rule=Host(`myapp.docker`)"
      - "traefik.http.routers.app-main.entrypoints=web"
      - "traefik.http.services.app-main.loadbalancer.server.port=8080"
      # Weight-based routing (advanced feature)
      - "traefik.http.services.app-main.loadbalancer.server.weight=90"
```

## Dinghy Layer Compatibility

This HTTP proxy provides compatibility with the original [dinghy-http-proxy](https://github.com/codekitchen/dinghy-http-proxy) environment variables:

### Supported Environment Variables

| Variable | Support | Description |
|----------|---------|-------------|
| `VIRTUAL_HOST` | ✅ **Full** | Automatic HTTP and HTTPS routing |
| `VIRTUAL_PORT` | ✅ **Full** | Backend port configuration |
| `CORS_ENABLED` | ✅ **Full** | Enable CORS for all origins |

### Unsupported Variables

| Variable | Status | Alternative |
|----------|--------|-------------|
| `CORS_DOMAINS` | ❌ **Not supported** | Use Traefik labels for fine-grained CORS control |

### Migration Notes

- **HTTPS**: Unlike the original dinghy-http-proxy, HTTPS is automatically enabled for all `VIRTUAL_HOST` entries
- **CORS**: Only global CORS enablement is supported. For domain-specific CORS, use Traefik labels
- **Multiple domains**: Comma-separated domains in `VIRTUAL_HOST` work the same way
