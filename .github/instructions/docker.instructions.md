---
description: 'Docker and containerization best practices for this HTTP proxy project'
applyTo: '**/Dockerfile,**/compose.yml,**/compose.*.yml,**/*.dockerfile'
---

# Docker and Container Guidelines

## Dockerfile Best Practices

- Follow multi-stage builds as shown in #build/Dockerfile
- Use specific base image versions, not `latest`
- Minimize layers and combine RUN commands when logical
- Use .dockerignore to exclude unnecessary files
- Set appropriate USER for security
- Use COPY instead of ADD unless you need ADD's features
- Set explicit WORKDIR
- Use LABEL for metadata and maintainer information

## Security Practices

- Run containers as non-root users when possible
- Use minimal base images (alpine, distroless)
- Use read-only filesystems when appropriate

## Development Workflow

- Use #Makefile targets for consistent builds
- Run integration tests with `make test`
