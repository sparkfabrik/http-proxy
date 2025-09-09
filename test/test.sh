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

# Test configuration constants
readonly DNS_PORT="19322"
readonly TARGET_IP="127.0.0.1"
readonly DNS_TIMEOUT="2"
readonly DNS_RETRIES="1"
readonly TEST_DOMAINS_LOC="test.loc,example.loc"
readonly TEST_DOMAINS_DEV="test.loc,example.dev"
readonly TEST_DOMAINS_SPARK="spark.loc,api.spark.loc,spark.dev,api.spark.dev"
readonly REJECT_DOMAINS="example.com,test.org"
readonly REJECT_DOMAINS_SPARK="other.loc,example.com"

# Sleep durations
readonly SLEEP_STACK_START=5
readonly SLEEP_DNS_RESTART=3
readonly SLEEP_DNS_CONFIG=3
readonly SLEEP_CONFIG_RESTORE=2
readonly SLEEP_PROXY_CONFIG=5
readonly SLEEP_CONTAINER_CHECK=2

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

# Helper function to log and sleep
wait_with_message() {
    local duration="$1"
    local message="$2"
    log "Waiting ${duration}s ${message}..."
    sleep "$duration"
}

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

        wait_with_message "$SLEEP_CONTAINER_CHECK" "for container to initialize"
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

        wait_with_message "$SLEEP_CONFIG_RESTORE" "for HTTP service to be ready"
        attempt=$((attempt + 1))
    done

    error "HTTP access to ${hostname} failed after ${max_attempts} attempts"
    return 1
}

# Test HSTS headers are NOT present in HTTPS responses
test_hsts_headers() {
    local hostname=$1
    local max_attempts=10
    local attempt=1

    log "Testing HSTS headers are NOT present for ${hostname}..."

    while [ $attempt -le $max_attempts ]; do
        # Try HTTPS connection and check for HSTS header
        local headers=$(curl -k -s -I -H "Host: ${hostname}" https://localhost:443 2>/dev/null || echo "")
        
        if [ -n "$headers" ]; then
            # Check if Strict-Transport-Security header is present
            if echo "$headers" | grep -i "strict-transport-security" >/dev/null 2>&1; then
                error "HSTS header found in HTTPS response for ${hostname}"
                error "Headers received: $(echo "$headers" | grep -i strict-transport-security)"
                return 1
            else
                success "HSTS header correctly absent for ${hostname}"
                return 0
            fi
        fi

        wait_with_message "$SLEEP_CONFIG_RESTORE" "for HTTPS service to be ready"
        attempt=$((attempt + 1))
    done

    error "HTTPS access to ${hostname} failed after ${max_attempts} attempts"
    return 1
}

# Test DNS functionality
test_dns() {
    local hostname="$1"
    local should_resolve="${2:-should_resolve}"

    command -v dig >/dev/null 2>&1 || return 0

    local result=$(dig -4 @${TARGET_IP} -p ${DNS_PORT} "$hostname" +short +time=${DNS_TIMEOUT} +tries=${DNS_RETRIES} 2>/dev/null)
    local exit_code=$?

    if [ "$should_resolve" = "should_not_resolve" ]; then
        [ $exit_code -ne 0 ] || [ -z "$result" ]
    else
        [ $exit_code -eq 0 ] && [ "$result" = "$TARGET_IP" ]
    fi
}

# Check if DNS server is running and accessible
# Check if DNS server is accessible
check_dns_server() {
    command -v dig >/dev/null 2>&1 || return 0

    local attempt=1
    while [ $attempt -le 10 ]; do
        if dig @${TARGET_IP} -p ${DNS_PORT} "test.spark.loc" +short +time=1 +tries=1 >/dev/null 2>&1; then
            return 0
        fi
        wait_with_message "$SLEEP_CONTAINER_CHECK" "for DNS server to be ready"
        attempt=$((attempt + 1))
    done

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
    log "Testing configured domain resolution..."
    for hostname in "$TRAEFIK_HOSTNAME" "$VIRTUAL_HOST_HOSTNAME" "$VIRTUAL_HOST_PORT_HOSTNAME" "$MULTI_VIRTUAL_HOST_HOSTNAME1" "$MULTI_VIRTUAL_HOST_HOSTNAME2"; do
        dns_tests_total=$((dns_tests_total + 1))
        if test_dns "$hostname" "should_resolve"; then
            dns_tests_passed=$((dns_tests_passed + 1))
        fi
    done

    # Test 2: TLD support - any subdomain of configured TLD should resolve
    log "Testing TLD support (any .spark.loc domain should resolve)..."

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
    log "Testing rejection of non-configured domains..."

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
    log "Testing edge cases..."

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
    log "Testing forwarding of external domain (google.com)..."
    upstream_tests_total=$((upstream_tests_total + 1))

    local external_result
    local external_exit_code
    external_result=$(dig @127.0.0.1 -p 19322 "google.com" +short +time=5 +tries=2 2>/dev/null)
    external_exit_code=$?

    if [ $external_exit_code -eq 0 ] && [ -n "$external_result" ]; then
        # Get the first IP address from the result (handle multiple IPs)
        local first_ip=$(echo "$external_result" | head -n1 | tr -d '\n')
        if [[ "$first_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            success "External domain google.com correctly forwarded to upstream servers (resolved to: ${first_ip})"
            upstream_tests_passed=$((upstream_tests_passed + 1))
        else
            # Check if forwarding is enabled - if disabled, this is expected behavior
            log "Checking if DNS forwarding is enabled..."
            local forwarding_enabled=$(docker compose exec -T dns env | grep HTTP_PROXY_DNS_FORWARD_ENABLED || echo "")

            if [[ "$forwarding_enabled" == *"false"* ]] || [ -z "$forwarding_enabled" ]; then
                success "External domain google.com not resolved - DNS forwarding is disabled (this is expected behavior)"
                upstream_tests_passed=$((upstream_tests_passed + 1))
            else
                error "External domain google.com failed to resolve via upstream servers - invalid IP format (exit: ${external_exit_code}, first IP: ${first_ip})"
            fi
        fi
    else
        local forwarding_enabled=$(docker compose exec -T dns env | grep HTTP_PROXY_DNS_FORWARD_ENABLED || echo "")

        if [[ "$forwarding_enabled" == *"false"* ]] || [ -z "$forwarding_enabled" ]; then
            success "External domain google.com not resolved - DNS forwarding is disabled (this is expected behavior)"
            upstream_tests_passed=$((upstream_tests_passed + 1))
        else
            error "External domain google.com failed to resolve via upstream servers (exit: ${external_exit_code}, result: ${external_result})"
        fi
    fi

    # Test 2: Query for another well-known external domain
    log "Testing forwarding of another external domain (cloudflare.com)..."
    upstream_tests_total=$((upstream_tests_total + 1))

    local cf_result
    local cf_exit_code
    cf_result=$(dig @127.0.0.1 -p 19322 "cloudflare.com" +short +time=5 +tries=2 2>/dev/null)
    cf_exit_code=$?

    if [ $cf_exit_code -eq 0 ] && [ -n "$cf_result" ]; then
        # Get the first IP address from the result (handle multiple IPs)
        local first_cf_ip=$(echo "$cf_result" | head -n1 | tr -d '\n')
        if [[ "$first_cf_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            success "External domain cloudflare.com correctly forwarded to upstream servers (resolved to: ${first_cf_ip})"
            upstream_tests_passed=$((upstream_tests_passed + 1))
        else
            # Check if forwarding is enabled - if disabled, this is expected behavior
            local forwarding_enabled=$(docker compose exec -T dns env | grep HTTP_PROXY_DNS_FORWARD_ENABLED || echo "")

            if [[ "$forwarding_enabled" == *"false"* ]] || [ -z "$forwarding_enabled" ]; then
                success "External domain cloudflare.com not resolved - DNS forwarding is disabled (this is expected behavior)"
                upstream_tests_passed=$((upstream_tests_passed + 1))
            else
                error "External domain cloudflare.com failed to resolve via upstream servers - invalid IP format (exit: ${cf_exit_code}, first IP: ${first_cf_ip})"
            fi
        fi
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
    log "Verifying configured domains still resolve to target IP..."
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
    log "Testing DNS forwarding enabled"
    export HTTP_PROXY_DNS_FORWARD_ENABLED="true"
    export HTTP_PROXY_DNS_UPSTREAM_SERVERS="8.8.8.8:53,1.1.1.1:53"
    docker compose up -d dns --quiet-pull 2>/dev/null || true
    wait_with_message "$SLEEP_DNS_RESTART" "for DNS service to restart with forwarding enabled"

    if check_dns_server; then
        # Test external domain resolution
        local external_result
        external_result=$(dig @127.0.0.1 -p 19322 "google.com" +short +time=5 +tries=2 2>/dev/null)

        # Check if we got at least one valid IPv4 address (handle multiple IPs)
        if [ -n "$external_result" ]; then
            # Get the first IP address from the result
            local first_ip=$(echo "$external_result" | head -n1 | tr -d '\n')
            if [[ "$first_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
                success "DNS forwarding enabled: external domains resolve correctly (got ${first_ip})"
                config_tests_passed=$((config_tests_passed + 1))
            else
                warning "DNS forwarding enabled but external domain resolution failed - invalid IP format"
            fi
        else
            warning "DNS forwarding enabled but external domain resolution failed - no result"
        fi
    else
        warning "DNS server not accessible for forwarding enabled test"
    fi

    # Test configuration 2: Forwarding disabled
    log "Testing DNS forwarding disabled"
    export HTTP_PROXY_DNS_FORWARD_ENABLED="false"
    docker compose up -d dns --quiet-pull 2>/dev/null || true
    wait_with_message "$SLEEP_DNS_RESTART" "for DNS service to restart with forwarding disabled"

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
    wait_with_message "$SLEEP_CONFIG_RESTORE" "for DNS service to restore original configuration"

    log "DNS forwarding configuration tests: ${config_tests_passed}/${config_tests_total} passed"

    if [ "$config_tests_passed" -eq "$config_tests_total" ]; then
        success "DNS forwarding configuration tests passed"
        return 0
    else
        warning "Some DNS forwarding configuration tests failed"
        return 1
    fi
}

# Test DNS server with different configurations using docker compose
test_dns_configurations() {
    log "Testing DNS server with different configurations..."
    log "================================================="

    local original_dir=$(pwd)
    cd "$(dirname "$0")/.."

    local passed=0

    # Test configurations
    log "Testing DNS config: Single TLD (loc)"
    test_with_dns_config "loc" "$TEST_DOMAINS_LOC" "$REJECT_DOMAINS" && passed=$((passed + 1))

    log "Testing DNS config: Multiple TLDs (loc,dev)"
    test_with_dns_config "loc,dev" "$TEST_DOMAINS_DEV" "$REJECT_DOMAINS" && passed=$((passed + 1))

    log "Testing DNS config: Specific domains (spark.loc,spark.dev)"
    test_with_dns_config "spark.loc,spark.dev" "$TEST_DOMAINS_SPARK" "$REJECT_DOMAINS_SPARK" && passed=$((passed + 1))

    cd "$original_dir"

    # Restore original configuration
    unset HTTP_PROXY_DNS_TLDS
    docker compose up -d dns >/dev/null 2>&1 || true
    wait_with_message "$SLEEP_CONFIG_RESTORE" "for DNS service to restore original configuration"

    if [ "$passed" -eq 3 ]; then
        success "DNS configuration tests passed"
        return 0
    else
        error "DNS configuration tests failed"
        return 1
    fi
}

# Helper function to test with a specific DNS configuration
# Test DNS with specific configuration
test_with_dns_config() {
    local config="$1"
    local should_resolve="$2"
    local should_not_resolve="$3"

    log "Testing configuration: $config"

    # Apply configuration
    docker compose stop dns >/dev/null 2>&1 || true
    docker compose rm -f dns >/dev/null 2>&1 || true
    export HTTP_PROXY_DNS_TLDS="$config"
    docker compose up -d dns --force-recreate >/dev/null 2>&1 || true
    wait_with_message "$SLEEP_DNS_CONFIG" "for DNS service to apply new configuration"

    check_dns_server || return 1

    # Test domains
    local passed=0 total=0

    IFS=',' read -ra domains <<< "$should_resolve"
    for domain in "${domains[@]}"; do
        total=$((total + 1))
        if test_dns "$domain" "should_resolve"; then
            passed=$((passed + 1))
            success "✓ $domain"
        else
            error "✗ $domain (should resolve)"
        fi
    done

    IFS=',' read -ra domains <<< "$should_not_resolve"
    for domain in "${domains[@]}"; do
        total=$((total + 1))
        if test_dns "$domain" "should_not_resolve"; then
            passed=$((passed + 1))
            success "✓ $domain (correctly rejected)"
        else
            error "✗ $domain (should be rejected)"
        fi
    done

    [ "$passed" -eq "$total" ]
}

# Cleanup test containers
cleanup() {
    docker rm -f "$TRAEFIK_CONTAINER" "$VIRTUAL_HOST_CONTAINER" "$VIRTUAL_HOST_PORT_CONTAINER" "$MULTI_VIRTUAL_HOST_CONTAINER" 2>/dev/null || true
}

# Full stack cleanup and rebuild
full_cleanup_and_rebuild() {
    log "Setting up HTTP proxy stack..."
    cd "$(dirname "$0")/.."
    docker compose down --volumes --remove-orphans 2>/dev/null || true
    cleanup
    docker image prune -f >/dev/null 2>&1 || true
    log "Building Docker images..."
    docker compose build --pull
    success "Build completed"
}

# Main test function
main() {
    log "Starting HTTP Proxy Integration Tests"
    log "======================================"

    # Setup
    if [ "$1" = "--no-rebuild" ]; then
        cleanup
    else
        full_cleanup_and_rebuild
    fi

    # Start stack and create test containers
    cd "$(dirname "$0")/.."
    log "Starting HTTP proxy stack..."
    docker compose up -d
    wait_with_message "$SLEEP_STACK_START" "for proxy services to initialize"
    success "Stack started"

    # Create test containers
    log "Creating test containers..."
    docker run -d --name "$TRAEFIK_CONTAINER" \
        --label "traefik.enable=true" \
        --label "traefik.http.routers.${TRAEFIK_CONTAINER}.rule=Host(\`app1.${TEST_DOMAIN}\`)" \
        --label "traefik.http.services.${TRAEFIK_CONTAINER}.loadbalancer.server.port=80" \
        --network http-proxy_default nginx:alpine

    docker run -d --name "$VIRTUAL_HOST_CONTAINER" \
        --env "VIRTUAL_HOST=app2.${TEST_DOMAIN}" nginx:alpine

    docker run -d --name "$VIRTUAL_HOST_PORT_CONTAINER" \
        --env "VIRTUAL_HOST=app3.${TEST_DOMAIN}" --env "VIRTUAL_PORT=80" nginx:alpine

    docker run -d --name "$MULTI_VIRTUAL_HOST_CONTAINER" \
        --env "VIRTUAL_HOST=app4.${TEST_DOMAIN},app5.${TEST_DOMAIN}" \
        --env "VIRTUAL_PORT=80" nginx:alpine

    # Wait for containers
    wait_for_container "$TRAEFIK_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_PORT_CONTAINER"
    wait_for_container "$MULTI_VIRTUAL_HOST_CONTAINER"
    wait_with_message "$SLEEP_PROXY_CONFIG" "for proxy configuration to propagate"

    # Run tests
    local passed=0 total=0

    # HTTP Tests
    log "Testing HTTP access..."
    total=$((total + 1))
    local http_passed=0
    test_http_access "app1.${TEST_DOMAIN}" && http_passed=$((http_passed + 1))
    test_http_access "app2.${TEST_DOMAIN}" && http_passed=$((http_passed + 1))
    test_http_access "app3.${TEST_DOMAIN}" && http_passed=$((http_passed + 1))
    test_http_access "app4.${TEST_DOMAIN}" && http_passed=$((http_passed + 1))
    test_http_access "app5.${TEST_DOMAIN}" && http_passed=$((http_passed + 1))
    [ "$http_passed" -eq 5 ] && passed=$((passed + 1))

    # HSTS Tests
    log "Testing HSTS headers are NOT present..."
    total=$((total + 1))
    local hsts_passed=0
    test_hsts_headers "app1.${TEST_DOMAIN}" && hsts_passed=$((hsts_passed + 1))
    test_hsts_headers "app2.${TEST_DOMAIN}" && hsts_passed=$((hsts_passed + 1))
    test_hsts_headers "app3.${TEST_DOMAIN}" && hsts_passed=$((hsts_passed + 1))
    test_hsts_headers "app4.${TEST_DOMAIN}" && hsts_passed=$((hsts_passed + 1))
    test_hsts_headers "app5.${TEST_DOMAIN}" && hsts_passed=$((hsts_passed + 1))
    [ "$hsts_passed" -eq 5 ] && passed=$((passed + 1))

    # DNS Tests (if dig available)
    if command -v dig >/dev/null 2>&1; then
        log "Testing DNS functionality..."
        total=$((total + 1))
        test_all_dns && passed=$((passed + 1))

        log "Testing upstream DNS..."
        total=$((total + 1))
        test_upstream_dns && passed=$((passed + 1))

        log "Testing DNS forwarding configurations..."
        total=$((total + 1))
        test_dns_forwarding_configurations && passed=$((passed + 1))

        log "Testing DNS server configurations..."
        total=$((total + 1))
        test_dns_configurations && passed=$((passed + 1))
    fi

    # Results
    log "Test Results:"
    log "============="
    log "HTTP Tests: ${http_passed}/5 passed"
    log "HSTS Tests: ${hsts_passed}/5 passed"
    log "Test Suites: ${passed}/${total} passed"

    cleanup

    if [ "$passed" -eq "$total" ]; then
        success "All tests passed! HTTP proxy is working correctly."
        return 0
    else
        error "Some tests failed. Check the logs above for details."
        return 1
    fi
}

# Handle script interruption
trap cleanup EXIT

# Help message
if [ "$1" = "--help" ] || [ "$1" = "-h" ]; then
    echo "HTTP Proxy Integration Test Script"
    echo "Usage: $0 [--no-rebuild|--help]"
    echo ""
    echo "Options:"
    echo "  --no-rebuild    Skip full cleanup and rebuild"
    echo "  --help, -h      Show this help message"
    exit 0
fi

# Run tests and capture exit code
main "$@"
exit_code=$?

# Exit with the same code as main function
exit $exit_code
