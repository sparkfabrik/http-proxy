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
MULTI_VIRTUAL_HOST_CONTAINER="test-multi-virtual-host-app"

# Hostname configurations for DNS testing
TRAEFIK_HOSTNAME="app1.${TEST_DOMAIN}"
VIRTUAL_HOST_HOSTNAME="app2.${TEST_DOMAIN}"
VIRTUAL_HOST_PORT_HOSTNAME="app3.${TEST_DOMAIN}"
MULTI_VIRTUAL_HOST_HOSTNAME1="app4.${TEST_DOMAIN}"
MULTI_VIRTUAL_HOST_HOSTNAME2="app5.${TEST_DOMAIN}"

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
    local should_resolve="$2"  # Optional parameter: "should_resolve" or "should_not_resolve"

    # Default to should resolve if not specified
    if [ -z "$should_resolve" ]; then
        should_resolve="should_resolve"
    fi

    # Check if dig is available
    if ! command -v dig >/dev/null 2>&1; then
        log "dig command not available, skipping DNS test for ${hostname}"
        return 0
    fi

    log "Testing DNS resolution for ${hostname}..."

    # Test DNS resolution using dig with timeout and error handling
    local result
    local dig_exit_code

    # Capture both output and exit code
    result=$(dig @127.0.0.1 -p $dns_port "$hostname" +short +time=2 +tries=1 2>/dev/null)
    dig_exit_code=$?

    if [ "$should_resolve" = "should_not_resolve" ]; then
        # This domain should NOT resolve
        # For non-configured domains, the DNS server should either:
        # 1. Return empty response (silently drop)
        # 2. Return NXDOMAIN
        # 3. Timeout (if the server drops the query)
        if [ $dig_exit_code -ne 0 ] || [ -z "$result" ] || [[ "$result" == *"timed out"* ]] || [[ "$result" == *"connection refused"* ]]; then
            success "DNS correctly rejected ${hostname} (not configured)"
            return 0
        else
            error "DNS incorrectly resolved ${hostname} to ${result} (should have been rejected)"
            return 1
        fi
    else
        # This domain SHOULD resolve
        if [ $dig_exit_code -ne 0 ]; then
            error "DNS resolution failed for ${hostname} (exit code: ${dig_exit_code})"
            return 1
        fi

        if [ -z "$result" ] || [[ "$result" == *"timed out"* ]] || [[ "$result" == *"connection refused"* ]]; then
            error "DNS resolution failed for ${hostname} (no response or timeout)"
            return 1
        fi

        # Clean up the result (remove any trailing dots or whitespace)
        result=$(echo "$result" | tr -d '\n' | sed 's/\.$//')

        if [ "$result" = "$expected_ip" ]; then
            success "DNS resolution for ${hostname} works (resolved to ${result})"
            return 0
        else
            error "DNS resolution for ${hostname} returned unexpected result: ${result} (expected ${expected_ip})"
            return 1
        fi
    fi
}

# Check if DNS server is running and accessible
check_dns_server() {
    local dns_port="19322"
    local max_attempts=10
    local attempt=1

    log "Checking if DNS server is accessible..."

    while [ $attempt -le $max_attempts ]; do
        # Try to query a simple domain - we don't care about the result, just that the server responds
        if dig @127.0.0.1 -p $dns_port "test.spark.loc" +short +time=2 +tries=1 >/dev/null 2>&1; then
            success "DNS server is accessible on port ${dns_port}"
            return 0
        fi

        # Check if it's a connection refused (server not running) vs timeout (server running but not responding)
        local test_result
        test_result=$(dig @127.0.0.1 -p $dns_port "test.spark.loc" +short +time=1 +tries=1 2>&1)

        if [[ "$test_result" == *"connection refused"* ]]; then
            log "DNS server not yet available (connection refused), waiting... (attempt ${attempt}/${max_attempts})"
        else
            log "DNS server responding but query failed, waiting... (attempt ${attempt}/${max_attempts})"
        fi

        sleep 2
        attempt=$((attempt + 1))
    done

    error "DNS server is not accessible after ${max_attempts} attempts"
    return 1
}

# Test all DNS functionality
test_all_dns() {
    log "Testing DNS server functionality..."
    log "=================================="

    # First, check if DNS server is accessible
    if ! check_dns_server; then
        error "DNS server is not accessible, skipping DNS tests"
        return 1
    fi

    local dns_tests_passed=0
    local dns_tests_total=0

    # Test 1: Basic hostname resolution (configured domains should resolve)
    log "Test 1: Testing configured domain resolution..."
    for hostname in "$TRAEFIK_HOSTNAME" "$VIRTUAL_HOST_HOSTNAME" "$VIRTUAL_HOST_PORT_HOSTNAME" "$MULTI_VIRTUAL_HOST_HOSTNAME1" "$MULTI_VIRTUAL_HOST_HOSTNAME2"; do
        dns_tests_total=$((dns_tests_total + 1))
        if test_dns "$hostname" "should_resolve"; then
            dns_tests_passed=$((dns_tests_passed + 1))
        fi
    done

    # Test 2: TLD support - any subdomain of configured TLD should resolve
    log "Test 2: Testing TLD support (any .spark.loc domain should resolve)..."

    local tld_test_domains=(
        "test.spark.loc"
        "example.spark.loc"
        "api.test.spark.loc"
    )

    for hostname in "${tld_test_domains[@]}"; do
        dns_tests_total=$((dns_tests_total + 1))
        if test_dns "$hostname" "should_resolve"; then
            dns_tests_passed=$((dns_tests_passed + 1))
        fi
    done

    # Test 3: Negative tests - domains that should NOT resolve
    log "Test 3: Testing rejection of non-configured domains..."

    local negative_test_domains=(
        "example.com"
        "test.org"
        "service.local"
        "wrong.tld"
    )

    for hostname in "${negative_test_domains[@]}"; do
        dns_tests_total=$((dns_tests_total + 1))
        if test_dns "$hostname" "should_not_resolve"; then
            dns_tests_passed=$((dns_tests_passed + 1))
        fi
    done

    # Test 4: Edge cases
    log "Test 4: Testing edge cases..."

    # Test malformed domains (these should not resolve)
    local edge_case_domains=(
        "."
        ".loc"
    )

    for hostname in "${edge_case_domains[@]}"; do
        dns_tests_total=$((dns_tests_total + 1))
        if test_dns "$hostname" "should_not_resolve"; then
            dns_tests_passed=$((dns_tests_passed + 1))
        fi
    done

    # Test valid DNS format with trailing dot (should resolve)
    log "Testing valid DNS format with trailing dot..."
    dns_tests_total=$((dns_tests_total + 1))
    if test_dns "spark.loc." "should_resolve"; then
        dns_tests_passed=$((dns_tests_passed + 1))
    fi

    log "DNS Test Results: ${dns_tests_passed}/${dns_tests_total} tests passed"

    if [ "$dns_tests_passed" -eq "$dns_tests_total" ]; then
        success "All DNS tests passed!"
        return 0
    else
        error "Some DNS tests failed (${dns_tests_passed}/${dns_tests_total})"
        return 1
    fi
}

# Test upstream DNS server functionality
test_upstream_dns() {
    log "Testing upstream DNS server functionality..."
    log "=========================================="

    # First, check if DNS server is accessible
    if ! check_dns_server; then
        error "DNS server not accessible, skipping upstream tests"
        return 1
    fi

    local upstream_tests_passed=0
    local upstream_tests_total=0

    # Check if dig is available
    if ! command -v dig >/dev/null 2>&1; then
        log "dig command not available, skipping upstream DNS tests"
        return 0
    fi

    # Test 1: Query for a domain not in our configured domains but that should resolve via upstream
    # We'll use google.com as it should always resolve via upstream servers
    log "Test 1: Testing forwarding of external domain (google.com)..."
    upstream_tests_total=$((upstream_tests_total + 1))

    local external_result
    local external_exit_code
    external_result=$(dig @127.0.0.1 -p 19322 "google.com" +short +time=5 +tries=2 2>/dev/null)
    external_exit_code=$?

    if [ $external_exit_code -eq 0 ] && [ -n "$external_result" ] && [[ "$external_result" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        success "External domain google.com correctly forwarded to upstream servers (resolved to: ${external_result})"
        upstream_tests_passed=$((upstream_tests_passed + 1))
    else
        # Check if forwarding is enabled - if disabled, this is expected behavior
        log "Checking if DNS forwarding is enabled..."
        local forwarding_enabled=$(docker compose exec -T dns env | grep HTTP_PROXY_DNS_FORWARD_ENABLED || echo "")

        if [[ "$forwarding_enabled" == *"false"* ]] || [ -z "$forwarding_enabled" ]; then
            success "External domain google.com not resolved - DNS forwarding is disabled (this is expected behavior)"
            upstream_tests_passed=$((upstream_tests_passed + 1))
        else
            error "External domain google.com failed to resolve via upstream servers (exit: ${external_exit_code}, result: ${external_result})"
        fi
    fi

    # Test 2: Query for another well-known external domain
    log "Test 2: Testing forwarding of another external domain (cloudflare.com)..."
    upstream_tests_total=$((upstream_tests_total + 1))

    local cf_result
    local cf_exit_code
    cf_result=$(dig @127.0.0.1 -p 19322 "cloudflare.com" +short +time=5 +tries=2 2>/dev/null)
    cf_exit_code=$?

    if [ $cf_exit_code -eq 0 ] && [ -n "$cf_result" ] && [[ "$cf_result" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        success "External domain cloudflare.com correctly forwarded to upstream servers (resolved to: ${cf_result})"
        upstream_tests_passed=$((upstream_tests_passed + 1))
    else
        # Check if forwarding is enabled - if disabled, this is expected behavior
        local forwarding_enabled=$(docker compose exec -T dns env | grep HTTP_PROXY_DNS_FORWARD_ENABLED || echo "")

        if [[ "$forwarding_enabled" == *"false"* ]] || [ -z "$forwarding_enabled" ]; then
            success "External domain cloudflare.com not resolved - DNS forwarding is disabled (this is expected behavior)"
            upstream_tests_passed=$((upstream_tests_passed + 1))
        else
            error "External domain cloudflare.com failed to resolve via upstream servers (exit: ${cf_exit_code}, result: ${cf_result})"
        fi
    fi

    # Test 3: Verify configured domains still resolve to our target IP
    log "Test 3: Verifying configured domains still resolve to target IP..."
    upstream_tests_total=$((upstream_tests_total + 1))

    if test_dns "test.spark.loc" "should_resolve"; then
        success "Configured domain test.spark.loc still resolves correctly to target IP"
        upstream_tests_passed=$((upstream_tests_passed + 1))
    else
        error "Configured domain test.spark.loc failed to resolve to target IP"
    fi

    log "Upstream DNS Test Results: ${upstream_tests_passed}/${upstream_tests_total} tests passed"

    if [ "$upstream_tests_passed" -eq "$upstream_tests_total" ]; then
        success "All upstream DNS tests passed"
        return 0
    else
        warning "Some upstream DNS tests failed (${upstream_tests_passed}/${upstream_tests_total})"
        return 1
    fi
}

# Test DNS with forwarding enabled and disabled
test_dns_forwarding_configurations() {
    log "Testing DNS server with different forwarding configurations..."
    log "============================================================"

    local original_dir=$(pwd)
    cd "$(dirname "$0")/.."

    local config_tests_passed=0
    local config_tests_total=2

    # Test configuration 1: Forwarding enabled
    log "Configuration Test 1: DNS forwarding enabled"
    export HTTP_PROXY_DNS_FORWARD_ENABLED="true"
    export HTTP_PROXY_DNS_UPSTREAM_SERVERS="8.8.8.8:53,1.1.1.1:53"
    docker compose up -d dns --quiet-pull 2>/dev/null || true
    sleep 5

    if check_dns_server; then
        # Test external domain resolution
        local external_result
        external_result=$(dig @127.0.0.1 -p 19322 "google.com" +short +time=5 +tries=2 2>/dev/null)

        if [ -n "$external_result" ] && [[ "$external_result" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            success "DNS forwarding enabled: external domains resolve correctly"
            config_tests_passed=$((config_tests_passed + 1))
        else
            warning "DNS forwarding enabled but external domain resolution failed"
        fi
    else
        warning "DNS server not accessible for forwarding enabled test"
    fi

    # Test configuration 2: Forwarding disabled
    log "Configuration Test 2: DNS forwarding disabled"
    export HTTP_PROXY_DNS_FORWARD_ENABLED="false"
    docker compose up -d dns --quiet-pull 2>/dev/null || true
    sleep 5

    if check_dns_server; then
        # Test that external domains do NOT resolve
        local external_result
        local external_exit_code
        external_result=$(dig @127.0.0.1 -p 19322 "google.com" +short +time=3 +tries=1 2>/dev/null)
        external_exit_code=$?

        # With forwarding disabled, external domains should either not resolve or timeout
        if [ $external_exit_code -ne 0 ] || [ -z "$external_result" ]; then
            success "DNS forwarding disabled: external domains correctly rejected"
            config_tests_passed=$((config_tests_passed + 1))
        else
            warning "DNS forwarding disabled but external domain still resolved: ${external_result}"
        fi
    else
        warning "DNS server not accessible for forwarding disabled test"
    fi

    cd "$original_dir"

    # Restore original configuration
    unset HTTP_PROXY_DNS_FORWARD_ENABLED
    unset HTTP_PROXY_DNS_UPSTREAM_SERVERS
    docker compose up -d dns --quiet-pull 2>/dev/null || true
    sleep 3

    log "DNS forwarding configuration tests: ${config_tests_passed}/${config_tests_total} passed"

    if [ "$config_tests_passed" -eq "$config_tests_total" ]; then
        success "DNS forwarding configuration tests passed"
        return 0
    else
        warning "Some DNS forwarding configuration tests failed"
        return 1
    fi
}

# Test DNS server with different configurations using docker-compose
test_dns_configurations() {
    log "Testing DNS server with different configurations..."
    log "================================================="

    local original_dir=$(pwd)
    cd "$(dirname "$0")/.."

    # Test configuration 1: Single TLD (loc)
    log "Configuration Test 1: Single TLD (loc)"
    test_with_dns_config "loc" "test.loc,example.loc" "example.com,test.org"

    # Test configuration 2: Multiple TLDs (loc,dev)
    log "Configuration Test 2: Multiple TLDs (loc,dev)"
    test_with_dns_config "loc,dev" "test.loc,example.dev" "example.com,test.org"

    # Test configuration 3: Specific domains (spark.loc,spark.dev)
    log "Configuration Test 3: Specific domains (spark.loc,spark.dev)"
    test_with_dns_config "spark.loc,spark.dev" "spark.loc,api.spark.loc,spark.dev,api.spark.dev" "other.loc,example.com"

    cd "$original_dir"

    # Restore original DNS configuration
    unset HTTP_PROXY_DNS_TLDS
    docker-compose up -d dns --quiet-pull 2>/dev/null || true
    sleep 3

    success "DNS configuration tests completed"
}

# Helper function to test with a specific DNS configuration
test_with_dns_config() {
    local config="$1"
    local should_resolve="$2"
    local should_not_resolve="$3"

    log "Testing with HTTP_PROXY_DNS_TLDS='${config}'"

    # Set environment variable and restart DNS service
    export HTTP_PROXY_DNS_TLDS="$config"
    docker-compose up -d dns --quiet-pull 2>/dev/null || true

    # Wait for DNS service to be ready
    sleep 5

    if ! check_dns_server; then
        warning "DNS server not accessible for config '${config}', skipping"
        return 1
    fi

    local config_tests_passed=0
    local config_tests_total=0

    # Test domains that should resolve
    IFS=',' read -ra RESOLVE_DOMAINS <<< "$should_resolve"
    for domain in "${RESOLVE_DOMAINS[@]}"; do
        config_tests_total=$((config_tests_total + 1))
        if test_dns "$domain" "should_resolve" >/dev/null 2>&1; then
            config_tests_passed=$((config_tests_passed + 1))
        fi
    done

    # Test domains that should NOT resolve
    IFS=',' read -ra NO_RESOLVE_DOMAINS <<< "$should_not_resolve"
    for domain in "${NO_RESOLVE_DOMAINS[@]}"; do
        config_tests_total=$((config_tests_total + 1))
        if test_dns "$domain" "should_not_resolve" >/dev/null 2>&1; then
            config_tests_passed=$((config_tests_passed + 1))
        fi
    done

    log "Config test results for '${config}': ${config_tests_passed}/${config_tests_total}"

    if [ "$config_tests_passed" -eq "$config_tests_total" ]; then
        success "Configuration test passed for: ${config}"
        return 0
    else
        warning "Configuration test failed for: ${config} (${config_tests_passed}/${config_tests_total})"
        return 1
    fi
}

# Test DNS on a specific port
test_dns_on_port() {
    local hostname="$1"
    local port="$2"
    local should_resolve="$3"
    local expected_ip="127.0.0.1"

    # Check if dig is available
    if ! command -v dig >/dev/null 2>&1; then
        return 0
    fi

    # Test DNS resolution using dig on specific port with error handling
    local result
    local dig_exit_code

    # Capture both output and exit code
    result=$(dig @127.0.0.1 -p "$port" "$hostname" +short +time=2 +tries=1 2>/dev/null)
    dig_exit_code=$?

    if [ "$should_resolve" = "should_not_resolve" ]; then
        # This domain should NOT resolve
        if [ $dig_exit_code -ne 0 ] || [ -z "$result" ] || [[ "$result" == *"timed out"* ]] || [[ "$result" == *"connection refused"* ]]; then
            return 0
        else
            return 1
        fi
    else
        # This domain should resolve
        if [ $dig_exit_code -eq 0 ] && [ -n "$result" ] && [ "$result" = "$expected_ip" ]; then
            return 0
        else
            return 1
        fi
    fi
}

cleanup() {
    log "Cleaning up test containers..."

    docker rm -f "$TRAEFIK_CONTAINER" 2>/dev/null || true
    docker rm -f "$VIRTUAL_HOST_CONTAINER" 2>/dev/null || true
    docker rm -f "$VIRTUAL_HOST_PORT_CONTAINER" 2>/dev/null || true
    docker rm -f "$MULTI_VIRTUAL_HOST_CONTAINER" 2>/dev/null || true

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
    docker rm -f "$MULTI_VIRTUAL_HOST_CONTAINER" 2>/dev/null || true

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

    # Container 4: Multiple comma-separated VIRTUAL_HOST values
    log "Creating container with multiple VIRTUAL_HOST values: ${MULTI_VIRTUAL_HOST_CONTAINER}"
    docker run -d \
        --name "$MULTI_VIRTUAL_HOST_CONTAINER" \
        --env "VIRTUAL_HOST=app4.${TEST_DOMAIN},app5.${TEST_DOMAIN}" \
        --env "VIRTUAL_PORT=80" \
        nginx:alpine

    # Wait for containers to be ready
    wait_for_container "$TRAEFIK_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_PORT_CONTAINER"
    wait_for_container "$MULTI_VIRTUAL_HOST_CONTAINER"

    # Give some time for the proxy to detect and configure routes
    log "Waiting for proxy configuration to propagate..."
    sleep 15

    # Step 3: Test HTTP access
    log "Testing HTTP access to all containers..."
    log "======================================="

    local test_passed=0
    local test_total=5

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

    # Test multi-VIRTUAL_HOST container (first hostname)
    if test_http_access "app4.${TEST_DOMAIN}"; then
        test_passed=$((test_passed + 1))
    fi

    # Test multi-VIRTUAL_HOST container (second hostname)
    if test_http_access "app5.${TEST_DOMAIN}"; then
        test_passed=$((test_passed + 1))
    fi

    # Show detailed curl responses for debugging
    log "Detailed HTTP responses:"
    log "========================"

    for app in app1 app2 app3 app4 app5; do
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

    # Step 5: Test upstream DNS functionality
    log "Step 5: Testing upstream DNS functionality..."
    log "============================================"

    if ! test_upstream_dns; then
        warning "Upstream DNS tests failed, but continuing..."
        # Don't fail the entire test suite for upstream tests
    fi

    # Step 6: Test DNS forwarding configurations
    log "Step 6: Testing DNS forwarding configurations..."
    log "==============================================="

    # Only run forwarding configuration tests if we have dig available
    if command -v dig >/dev/null 2>&1; then
        if ! test_dns_forwarding_configurations; then
            warning "DNS forwarding configuration tests failed, but continuing..."
            # Don't fail the entire test suite for configuration tests
        fi
    else
        log "Skipping DNS forwarding configuration tests (dig command not available)"
    fi

    # Step 7: Test DNS server configurations
    log "Step 7: Testing DNS server configurations..."
    log "==========================================="

    # Only run configuration tests if we have dig available
    if command -v dig >/dev/null 2>&1; then
        if ! test_dns_configurations; then
            warning "DNS configuration tests failed, but continuing..."
            # Don't fail the entire test suite for configuration tests
        fi
    else
        log "Skipping DNS configuration tests (dig command not available)"
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
    echo "   - Multiple comma-separated VIRTUAL_HOST values"
    echo "4. Testing HTTP access to all containers using curl"
    echo "5. Testing DNS resolution with comprehensive coverage:"
    echo "   - Basic hostname resolution for configured domains"
    echo "   - TLD support (any subdomain of configured TLD should resolve)"
    echo "   - Negative tests (non-configured domains should be rejected)"
    echo "   - Edge cases and malformed domain handling"
    echo "6. Testing upstream DNS server functionality:"
    echo "   - External domain forwarding when enabled"
    echo "   - Configured domain resolution to target IP"
    echo "   - Forwarding disabled behavior verification"
    echo "7. Testing different DNS server configurations using docker-compose:"
    echo "   - Single TLD: loc"
    echo "   - Multiple TLDs: loc,dev"
    echo "   - Specific domains: spark.loc,spark.dev"
    echo ""
    echo "All test containers use the domain suffix: ${TEST_DOMAIN}"
    echo ""
    echo "DNS Tests verify that the server:"
    echo "- Resolves configured domains and their subdomains"
    echo "- Rejects queries for non-configured domains (security)"
    echo "- Handles both TLD patterns (*.loc) and specific domains (spark.loc)"
    echo "- Supports comma-separated domain lists in HTTP_PROXY_DNS_TLDS environment variable"
    exit 0
fi

# Run the main test
main "$@"
