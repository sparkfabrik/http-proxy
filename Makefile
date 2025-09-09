DOCKER_IMAGE_NAME ?= sparkfabrik/http-proxy:latest

.PHONY: help docker-build docker-run docker-logs build test test-dns compose-up

help: ## Show help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

dev-up: dev-down ## Run the development environment (basic stack)
	@echo "Starting development environment (basic stack)..."
	@docker compose --profile metrics down -v
	@docker compose up -d --build --remove-orphans

dev-cli-traefik: dev-up ## Access the Traefik container CLI
	@echo "Accessing Traefik container CLI..."
	@docker compose exec http-proxy ash

dev-up-metrics: ## Run the development environment with monitoring stack
	@echo "Starting development environment with monitoring..."
	@docker compose --profile metrics down -v
	@docker compose --profile metrics up -d --build --remove-orphans

dev-down: ## Stop the development environment
	@echo "Stopping development environment..."
	@docker compose --profile metrics down -v

dev-logs-join-networks: ## Show logs for the joined networks
	@echo "Showing logs for the joined networks..."
	@docker-compose logs -f join_networks

test: ## Run integration tests
	@echo "Running integration tests..."
	@chmod +x test/test.sh
	@./test/test.sh

compose-up: ## Run Traefik with Docker
	@docker rm -vf http-proxy || true
	@docker-compose up -d --remove-orphans
	@cd build/traefik/test && \
		docker-compose up -d
