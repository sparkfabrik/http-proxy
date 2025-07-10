---
description: "General coding guidelines and project-specific best practices"
applyTo: "**/*.{js,ts,py,php,go,java,c,cpp,cs,rb,rs}"
---

# General Coding Guidelines

## Docker

- Use `docker compose` command (not `docker-compose`)
- Always use specific image tags, avoid `latest` in production
- Use multi-stage builds to reduce image size
- Include .dockerignore files

## Code Quality Standards

- Write clean, readable, and maintainable code
- Follow the DRY (Don't Repeat Yourself) principle
- Use meaningful and descriptive names
- Keep functions small and focused on single responsibility
- Write self-documenting code

## Documentation Standards

- Update README.md for new features or significant changes
- Document new environment variables in README.md
- Add new env variables to example/compose.yml
- Include inline comments for complex business logic
- Use JSDoc/TSDoc for function documentation

## Security Guidelines

- Never commit secrets or API keys
- Use environment variables for configuration
- Validate all user inputs
- Follow principle of least privilege
