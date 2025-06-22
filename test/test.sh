#!/bin/bash

# HTTP Proxy Integration Test Script
# Tests the refactored dinghy-layer and join-networks services

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging function
log() {
    echo -e "${BLUE}[$(date '+%H:%M:%S')]${NC} $1"
}

success() {
    echo -e "${GREEN}✓${NC} $1"
}

error() {
    echo -e "${RED}✗${NC} $1"
}

warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

# Test configuration
TEST_DOMAIN="spark.loc"
HTTP_PORT="80"

# Container configurations
TRAEFIK_CONTAINER="test-traefik-app"
VIRTUAL_HOST_CONTAINER="test-virtual-host-app"
VIRTUAL_HOST_PORT_CONTAINER="test-virtual-host-port-app"

# Hostname configurations for DNS testing
TRAEFIK_HOSTNAME="app1.${TEST_DOMAIN}"
VIRTUAL_HOST_HOSTNAME="app2.${TEST_DOMAIN}"
VIRTUAL_HOST_PORT_HOSTNAME="app3.${TEST_DOMAIN}"

# Wait function
wait_for_container() {
    local container_name=$1
    local max_attempts=30
    local attempt=1

    log "Waiting for container ${container_name} to be ready..."

    while [ $attempt -le $max_attempts ]; do
        if docker ps --format "table {{.Names}}" | grep -q "^${container_name}$"; then
            if docker exec "$container_name" curl -f -s http://localhost >/dev/null 2>&1; then
                success "Container ${container_name} is ready"
                return 0
            fi
        fi

        sleep 2
        attempt=$((attempt + 1))
    done

    error "Container ${container_name} failed to become ready"
    return 1
}

# HTTP test function
test_http_access() {
    local hostname=$1
    local max_attempts=10
    local attempt=1

    log "Testing HTTP access to ${hostname}..."

    while [ $attempt -le $max_attempts ]; do
        if curl -f -s -H "Host: ${hostname}" http://localhost:${HTTP_PORT} >/dev/null 2>&1; then
            success "HTTP access to ${hostname} works"
            return 0
        fi

        sleep 3
        attempt=$((attempt + 1))
    done

    error "HTTP access to ${hostname} failed after ${max_attempts} attempts"
    return 1
}

# Test DNS functionality
test_dns() {
    local hostname="$1"
    local expected_ip="127.0.0.1"
    local dns_port="19322"

    # Check if dig is available
    if ! command -v dig >/dev/null 2>&1; then
        log "dig command not available, skipping DNS test for ${hostname}"
        return 0
    fi

    log "Testing DNS resolution for ${hostname}..."

    # Test DNS resolution using dig
    local result
    result=$(dig @localhost -p $dns_port "$hostname" +short 2>/dev/null)

    if [ -z "$result" ]; then
        error "DNS resolution failed for ${hostname}"
        return 1
    fi

    if [ "$result" = "$expected_ip" ]; then
        success "DNS resolution for ${hostname} works (resolved to ${result})"
        return 0
    else
        error "DNS resolution for ${hostname} returned unexpected result: ${result} (expected ${expected_ip})"
        return 1
    fi
}

# Test all DNS functionality
test_all_dns() {
    log "Testing DNS server functionality..."
    log "=================================="

    local dns_tests_passed=0
    local dns_tests_total=0

    # Test each hostname
    for hostname in "$TRAEFIK_HOSTNAME" "$VIRTUAL_HOST_HOSTNAME" "$VIRTUAL_HOST_PORT_HOSTNAME"; do
        dns_tests_total=$((dns_tests_total + 1))
        if test_dns "$hostname"; then
            dns_tests_passed=$((dns_tests_passed + 1))
        fi
    done

    # Test some additional DNS queries
    log "Testing additional DNS queries..."

    # Test a generic .spark.loc domain
    dns_tests_total=$((dns_tests_total + 1))
    if test_dns "test.spark.loc"; then
        dns_tests_passed=$((dns_tests_passed + 1))
    fi

    # Test another .spark.loc domain
    dns_tests_total=$((dns_tests_total + 1))
    if test_dns "example.spark.loc"; then
        dns_tests_passed=$((dns_tests_passed + 1))
    fi

    log "DNS Test Results: ${dns_tests_passed}/${dns_tests_total} tests passed"

    if [ "$dns_tests_passed" -eq "$dns_tests_total" ]; then
        success "All DNS tests passed!"
        return 0
    else
        error "Some DNS tests failed"
        return 1
    fi
}

# Cleanup function
cleanup() {
    log "Cleaning up test containers..."

    docker rm -f "$TRAEFIK_CONTAINER" 2>/dev/null || true
    docker rm -f "$VIRTUAL_HOST_CONTAINER" 2>/dev/null || true
    docker rm -f "$VIRTUAL_HOST_PORT_CONTAINER" 2>/dev/null || true

    success "Cleanup completed"
}

# Full stack cleanup and rebuild
full_cleanup_and_rebuild() {
    log "Full cleanup and rebuild of HTTP proxy stack..."
    log "==============================================="

    # Stop and remove all containers from the stack
    log "Stopping and removing all stack containers..."
    cd "$(dirname "$0")/.."
    docker compose down --volumes --remove-orphans 2>/dev/null || true

    # Remove any dangling containers that might interfere
    docker rm -f "$TRAEFIK_CONTAINER" 2>/dev/null || true
    docker rm -f "$VIRTUAL_HOST_CONTAINER" 2>/dev/null || true
    docker rm -f "$VIRTUAL_HOST_PORT_CONTAINER" 2>/dev/null || true

    # Remove any dangling images from previous builds (optional, but ensures clean state)
    log "Cleaning up dangling images..."
    docker image prune -f >/dev/null 2>&1 || true

    # Rebuild all images from scratch
    log "Building all images from scratch..."
    docker compose build --pull

    success "Full cleanup and rebuild completed"
}

# Main test function
main() {
    log "Starting HTTP Proxy Integration Tests"
    log "======================================"

    # Check if we should skip rebuild
    if [ "$1" = "--no-rebuild" ]; then
        log "Skipping full rebuild (--no-rebuild flag detected)"
        # Just cleanup test containers
        cleanup
    else
        # Full cleanup and rebuild to ensure clean state
        full_cleanup_and_rebuild
    fi

    # Step 1: Start the HTTP proxy stack
    log "Starting HTTP proxy stack..."
    cd "$(dirname "$0")/.."
    docker compose up -d

    # Wait for services to be ready
    log "Waiting for proxy services to start..."
    sleep 10

    # Step 2: Create test containers
    log "Creating test containers..."

    # Container 1: Traefik labels
    log "Creating container with Traefik labels: ${TRAEFIK_CONTAINER}"
    docker run -d \
        --name "$TRAEFIK_CONTAINER" \
        --label "traefik.enable=true" \
        --label "traefik.http.routers.${TRAEFIK_CONTAINER}.rule=Host(\`app1.${TEST_DOMAIN}\`)" \
        --label "traefik.http.services.${TRAEFIK_CONTAINER}.loadbalancer.server.port=80" \
        --network http-proxy_default \
        nginx:alpine

    # Container 2: VIRTUAL_HOST only
    log "Creating container with VIRTUAL_HOST: ${VIRTUAL_HOST_CONTAINER}"
    docker run -d \
        --name "$VIRTUAL_HOST_CONTAINER" \
        --env "VIRTUAL_HOST=app2.${TEST_DOMAIN}" \
        nginx:alpine

    # Container 3: VIRTUAL_HOST and VIRTUAL_PORT
    log "Creating container with VIRTUAL_HOST and VIRTUAL_PORT: ${VIRTUAL_HOST_PORT_CONTAINER}"
    docker run -d \
        --name "$VIRTUAL_HOST_PORT_CONTAINER" \
        --env "VIRTUAL_HOST=app3.${TEST_DOMAIN}" \
        --env "VIRTUAL_PORT=80" \
        nginx:alpine

    # Wait for containers to be ready
    wait_for_container "$TRAEFIK_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_PORT_CONTAINER"

    # Give some time for the proxy to detect and configure routes
    log "Waiting for proxy configuration to propagate..."
    sleep 15

    # Step 3: Test HTTP access
    log "Testing HTTP access to all containers..."
    log "======================================="

    local test_passed=0
    local test_total=3

    # Test Traefik labeled container
    if test_http_access "app1.${TEST_DOMAIN}"; then
        test_passed=$((test_passed + 1))
    fi

    # Test VIRTUAL_HOST container
    if test_http_access "app2.${TEST_DOMAIN}"; then
        test_passed=$((test_passed + 1))
    fi

    # Test VIRTUAL_HOST + VIRTUAL_PORT container
    if test_http_access "app3.${TEST_DOMAIN}"; then
        test_passed=$((test_passed + 1))
    fi

    # Show detailed curl responses for debugging
    log "Detailed HTTP responses:"
    log "========================"

    for app in app1 app2 app3; do
        log "Testing ${app}.${TEST_DOMAIN}:"
        if curl -f -s -H "Host: ${app}.${TEST_DOMAIN}" http://localhost:${HTTP_PORT} | head -5; then
            success "Response received from ${app}.${TEST_DOMAIN}"
        else
            error "No response from ${app}.${TEST_DOMAIN}"
        fi
        echo
    done

    # Show container logs for debugging
    log "Container logs for debugging:"
    log "============================="

    echo "Dinghy Layer logs:"
    docker compose logs --tail=10 dinghy_layer 2>/dev/null || true
    echo

    echo "Join Networks logs:"
    docker compose logs --tail=10 join_networks 2>/dev/null || true
    echo

    echo "DNS Server logs:"
    docker compose logs --tail=10 dns 2>/dev/null || true
    echo

    # Step 4: Test DNS functionality
    if ! test_all_dns; then
        error "DNS tests failed"
        return 1
    fi

    # Final results
    log "Test Results:"
    log "============="
    log "Passed: ${test_passed}/${test_total} HTTP tests"

    if [ $test_passed -eq $test_total ]; then
        success "All tests passed! HTTP proxy is working correctly."
        return 0
    else
        error "Some tests failed. Check the logs above for details."
        return 1
    fi
}

# Handle script interruption
trap cleanup EXIT

# Check if help is requested
if [ "$1" = "--help" ] || [ "$1" = "-h" ]; then
    echo "HTTP Proxy Integration Test Script"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --no-rebuild    Skip full cleanup and rebuild (faster for development)"
    echo "  --help, -h      Show this help message"
    echo ""
    echo "This script tests the HTTP proxy functionality by:"
    echo "1. Full cleanup and rebuild of all Docker images (unless --no-rebuild)"
    echo "2. Starting the HTTP proxy stack with docker-compose"
    echo "3. Creating test containers with different configurations:"
    echo "   - Traefik labels"
    echo "   - VIRTUAL_HOST environment variable"
    echo "   - VIRTUAL_HOST + VIRTUAL_PORT environment variables"
    echo "4. Testing HTTP access to all containers using curl"
    echo "5. Testing DNS resolution for all domains"
    echo ""
    echo "All test containers use the domain suffix: ${TEST_DOMAIN}"
    exit 0
fi

# Run the main test
main "$@"
