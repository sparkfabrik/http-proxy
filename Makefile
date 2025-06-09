DOCKER_IMAGE_NAME ?= sparkfabrik/http-proxy:latest

help: ## Show help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

docker-build: ## Build the Docker image
	docker build -t $(DOCKER_IMAGE_NAME) .

docker-run: docker-build ## Run the Docker container
	docker rm -vf http-proxy || true
	docker run -d -v /var/run/docker.sock:/tmp/docker.sock:ro \
        --name=http-proxy \
        -p 80:80 \
        -p 19322:19322/udp \
        -e CONTAINER_NAME=http-proxy \
        -e DNS_IP=127.0.0.1 \
        -e DOMAIN_TLD=loc \
		$(DOCKER_IMAGE_NAME)

build: ## Build the go app.
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o join-networks

test-dns: ## Test DNS resolution (run in another terminal while dnsmasq is running)
	@echo "Clear dns cache and restart mDNSResponder"
	@sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder
	@echo "Testing DNS resolution on port 19322:"
	@echo "Testing hostmachine.loc:"
	dig @127.0.0.1 -p 19322 hostmachine.loc
	@echo "Testing any .loc domain:"
	dig @127.0.0.1 -p 19322 test.loc
	@echo "Testing specific domain with IP with dnscacheutil:"
	dscacheutil -q host -a name test.loc
