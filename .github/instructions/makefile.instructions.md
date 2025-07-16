---
description: 'Makefile and build automation guidelines'
applyTo: '**/Makefile,**/makefile,**/*.mk'
---

# Makefile and Build Automation Guidelines

## Makefile Structure

- Follow the patterns established in #Makefile
- Use .PHONY for targets that don't create files
- Add help target with target descriptions
- Use consistent variable naming (UPPERCASE for constants)
- Group related targets logically
- Add comments for complex targets

## Build Targets

- Implement clean, build, test, and install targets
- Use docker-compose for multi-service builds
- Include linting and formatting targets
- Provide development and production build variants
- Include dependency installation targets
- Add release and version management targets

## Testing Integration

- Include targets that run #test/test.sh
- Add certificate testing with #test/test-certs.sh
- Provide targets for different test environments
- Include integration test targets
- Add targets for Docker setup validation
- Include performance and load testing targets

## Docker Integration

- Use #compose.yml for service orchestration
- Include targets for building Docker images
- Add targets for starting/stopping services
- Include cleanup targets for Docker resources
- Provide targets for different environments (dev, prod)
- Add targets for image scanning and security checks

## Development Workflow

- Include targets for code generation if needed
- Add targets for dependency management
- Include formatting and linting automation
- Provide targets for documentation generation
- Add targets for local development setup
- Include targets for pre-commit hooks

## Best Practices

- Make targets idempotent when possible
- Use proper error handling and exit codes
- Include verbose and quiet modes
- Use consistent target naming conventions
- Add dependency checking for required tools
- Include cross-platform compatibility considerations
- Document complex targets with comments
- Use variables for repeated values and paths
