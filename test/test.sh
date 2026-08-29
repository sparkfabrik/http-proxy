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
ORPHAN_CONTAINER="test-orphan-reconcile-app"
ONEOFF_CONTAINER="test-oneoff-app"
PATH_ROOT_CONTAINER="test-path-root-app"
PATH_MOUNTED_CONTAINER="test-path-mounted-app"
WILDCARD_CONTAINER="test-wildcard-app"
WILDCARD_MOUNTED_CONTAINER="test-wildcard-mounted-app"

# Hostname configurations for DNS testing
TRAEFIK_HOSTNAME="app1.${TEST_DOMAIN}"
VIRTUAL_HOST_HOSTNAME="app2.${TEST_DOMAIN}"
VIRTUAL_HOST_PORT_HOSTNAME="app3.${TEST_DOMAIN}"
MULTI_VIRTUAL_HOST_HOSTNAME1="app4.${TEST_DOMAIN}"
MULTI_VIRTUAL_HOST_HOSTNAME2="app5.${TEST_DOMAIN}"
ORPHAN_HOSTNAME="app6.${TEST_DOMAIN}"
ONEOFF_HOSTNAME="app7.${TEST_DOMAIN}"
PATH_HOSTNAME="app8.${TEST_DOMAIN}"
WILDCARD_HOSTNAME="app9.wild.${TEST_DOMAIN}"

# Tailnet peer routing: a second proxy inside the stack standing in for a
# second machine, with its own backends behind it.
PEER_PROXY_CONTAINER="test-peer-proxy"
PEER_ROOT_CONTAINER="test-peer-root-app"
PEER_API_CONTAINER="test-peer-api-app"
PEER_SHARED_CONTAINER="test-peer-shared-app"
LOCAL_SHARED_CONTAINER="test-local-shared-app"
PEER_HOSTNAME="peer1.${TEST_DOMAIN}"
PEER_SHARED_HOSTNAME="peer2.${TEST_DOMAIN}"
PEER_ROOT_BODY="body-from-peer-root-container"
PEER_API_BODY="body-from-peer-mounted-container"
PEER_SHARED_BODY="body-from-peer-shared-container"
LOCAL_SHARED_BODY="body-from-local-shared-container"
PEER_STATE_DIR="test/state"
PEER_DYNAMIC_DIR="test/peer-dynamic"
PEER_CONFIG_FILE="test/peer-traefik.yml"
PEER_MERGED_COMPOSE="test/compose.merged.yml"
PEER_CLI_HOME="test/clihome"
TRAEFIK_PROXY_NAME="http-proxy"

# Bodies that identify which container answered. A mounted path cannot be
# checked by status code: when its route is missing the request falls through to
# the container serving the hostname, which answers 200 from the wrong place.
PATH_ROOT_BODY="body-from-root-container"
PATH_MOUNTED_BODY="body-from-mounted-container"
WILDCARD_BODY="body-from-wildcard-container"
WILDCARD_MOUNTED_BODY="body-from-wildcard-mounted-container"

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

# Test that a recreated container's orphaned config is pruned on the next
# initial scan (issue #109). Stopping dinghy_layer makes it miss the die event,
# so the old container's config file survives as an orphan with a stale backend
# until reconciliation removes it.
test_orphan_config_reconciliation() {
    docker run -d --name "$ORPHAN_CONTAINER" \
        --env "VIRTUAL_HOST=${ORPHAN_HOSTNAME}" nginx:alpine
    wait_for_container "$ORPHAN_CONTAINER" || return 1
    test_http_access "$ORPHAN_HOSTNAME" || return 1

    local old_id
    old_id=$(docker inspect --format '{{.Id}}' "$ORPHAN_CONTAINER" | cut -c1-12)
    if ! docker exec http-proxy test -f "/traefik/dynamic/${old_id}.yaml"; then
        error "Expected config file ${old_id}.yaml was not generated"
        return 1
    fi

    local had_auto_tls=0
    docker exec http-proxy test -f /traefik/dynamic/auto-tls.yml && had_auto_tls=1

    log "Stopping dinghy_layer to simulate a missed die event..."
    docker compose stop dinghy_layer

    docker rm -f "$ORPHAN_CONTAINER"
    docker run -d --name "$ORPHAN_CONTAINER" \
        --env "VIRTUAL_HOST=${ORPHAN_HOSTNAME}" nginx:alpine
    wait_for_container "$ORPHAN_CONTAINER" || return 1

    local new_id
    new_id=$(docker inspect --format '{{.Id}}' "$ORPHAN_CONTAINER" | cut -c1-12)

    log "Restarting dinghy_layer to trigger the initial scan..."
    docker compose start dinghy_layer

    # Wait for the initial scan to reconcile the orphaned config
    local attempt=1
    while [ $attempt -le 10 ]; do
        if ! docker exec http-proxy test -f "/traefik/dynamic/${old_id}.yaml"; then
            break
        fi
        wait_with_message "$SLEEP_CONTAINER_CHECK" "for the initial scan to reconcile configs"
        attempt=$((attempt + 1))
    done

    if docker exec http-proxy test -f "/traefik/dynamic/${old_id}.yaml"; then
        error "Orphaned config ${old_id}.yaml was not removed"
        return 1
    fi
    success "Orphaned config ${old_id}.yaml was removed"

    if ! docker exec http-proxy test -f "/traefik/dynamic/${new_id}.yaml"; then
        error "Config ${new_id}.yaml for the recreated container is missing"
        return 1
    fi
    if [ "$had_auto_tls" -eq 1 ] && ! docker exec http-proxy test -f /traefik/dynamic/auto-tls.yml; then
        error "auto-tls.yml was removed by reconciliation"
        return 1
    fi
    test_http_access "$ORPHAN_HOSTNAME" || return 1
    success "Recreated container config and shared files survived reconciliation"

    docker rm -f "$ORPHAN_CONTAINER" >/dev/null 2>&1 || true
    return 0
}

# Test that one-off containers created by "docker compose run" are ignored
# (issue #111). Compose marks them with com.docker.compose.oneoff=True and they
# inherit VIRTUAL_HOST from the service, so routing them would let a short-lived
# container claim the service domain.
test_oneoff_container_ignored() {
    docker run -d --name "$ONEOFF_CONTAINER" \
        --label "com.docker.compose.oneoff=True" \
        --env "VIRTUAL_HOST=${ONEOFF_HOSTNAME}" nginx:alpine
    wait_for_container "$ONEOFF_CONTAINER" || return 1
    wait_with_message "$SLEEP_PROXY_CONFIG" "for proxy configuration to propagate"

    local container_id
    container_id=$(docker inspect --format '{{.Id}}' "$ONEOFF_CONTAINER" | cut -c1-12)
    if docker exec http-proxy test -f "/traefik/dynamic/${container_id}.yaml"; then
        error "Config file ${container_id}.yaml was generated for a one-off container"
        docker rm -f "$ONEOFF_CONTAINER" >/dev/null 2>&1 || true
        return 1
    fi
    success "No config file was generated for the one-off container"

    local status
    status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
        -H "Host: ${ONEOFF_HOSTNAME}" "http://localhost:${HTTP_PORT}" 2>/dev/null || echo "000")
    if [ "$status" != "404" ]; then
        error "Expected 404 for ${ONEOFF_HOSTNAME}, got ${status}"
        docker rm -f "$ONEOFF_CONTAINER" >/dev/null 2>&1 || true
        return 1
    fi
    success "One-off container domain ${ONEOFF_HOSTNAME} is not routed"

    docker rm -f "$ONEOFF_CONTAINER" >/dev/null 2>&1 || true
    return 0
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

# Serve a fixed body from an nginx container, at the root and under a path, so
# a response identifies which container produced it. nginx:alpine is used
# throughout because wait_for_container probes readiness by running curl inside
# the container, which an image without a shell can never satisfy.
run_body_container() {
    local name=$1 body=$2
    shift 2

    docker run -d --name "$name" \
        -e "BODY=$body" \
        "$@" \
        nginx:alpine sh -c 'mkdir -p /usr/share/nginx/html/api /usr/share/nginx/html/api-docs && \
            printf "%s" "$BODY" > /usr/share/nginx/html/index.html && \
            printf "%s" "$BODY" > /usr/share/nginx/html/api/index.html && \
            printf "%s" "$BODY" > /usr/share/nginx/html/api-docs/index.html && \
            exec nginx -g "daemon off;"'
}

# Fetch a URL through the proxy and compare the body against what is expected.
#
# Redirects are followed: nginx answers /api with a 301 to /api/ when serving a
# directory, and that redirect page looks the same from either container, so
# without -L the assertion could not tell them apart. --resolve rather than a
# Host header, so the redirect target resolves back through the proxy, and both
# ports are mapped because the backend does not know the request arrived over
# TLS and sends its redirect to the http scheme.
test_body() {
    local hostname=$1 path=$2 expected=$3 label=$4
    local max_attempts=10
    local attempt=1
    local body=""

    log "Testing ${label}..."

    while [ $attempt -le $max_attempts ]; do
        body="$(curl -s -L \
            --resolve "${hostname}:${HTTP_PORT}:127.0.0.1" \
            --resolve "${hostname}:443:127.0.0.1" \
            "http://${hostname}${path}" 2>/dev/null || true)"
        if [ "$body" = "$expected" ]; then
            success "${label}"
            return 0
        fi
        wait_with_message "$SLEEP_CONFIG_RESTORE" "for routing to settle"
        attempt=$((attempt + 1))
    done

    error "${label} (got '${body:-<empty>}', expected '${expected}')"
    return 1
}

# The same over HTTPS, so the two schemes cannot diverge unnoticed.
test_body_https() {
    local hostname=$1 path=$2 expected=$3 label=$4
    local max_attempts=10
    local attempt=1
    local body=""

    log "Testing ${label}..."

    while [ $attempt -le $max_attempts ]; do
        body="$(curl -s -k -L \
            --resolve "${hostname}:443:127.0.0.1" \
            --resolve "${hostname}:${HTTP_PORT}:127.0.0.1" \
            "https://${hostname}${path}" 2>/dev/null || true)"
        if [ "$body" = "$expected" ]; then
            success "${label}"
            return 0
        fi
        wait_with_message "$SLEEP_CONFIG_RESTORE" "for routing to settle"
        attempt=$((attempt + 1))
    done

    error "${label} (got '${body:-<empty>}', expected '${expected}')"
    return 1
}

# VIRTUAL_PATH: a container mounted under a path of a hostname another container
# serves. Every assertion compares bodies, because status codes cannot tell a
# working path route from a request falling through to the host.
test_virtual_path_routing() {
    local passed=0 total=0

    # The root of the shared hostname belongs to the container without a path.
    total=$((total + 1))
    test_body "$PATH_HOSTNAME" "/" "$PATH_ROOT_BODY" \
        "root of ${PATH_HOSTNAME} is served by the host container" && passed=$((passed + 1))

    # The mounted path, and anything beneath it, belongs to the mounted one.
    total=$((total + 1))
    test_body "$PATH_HOSTNAME" "/api" "$PATH_MOUNTED_BODY" \
        "/api is served by the mounted container" && passed=$((passed + 1))

    total=$((total + 1))
    test_body "$PATH_HOSTNAME" "/api/" "$PATH_MOUNTED_BODY" \
        "/api/ is served by the mounted container" && passed=$((passed + 1))

    # The regression this feature exists to avoid: PathPrefix alone is a raw
    # string prefix in Traefik, so /api would also capture /api-docs. Both
    # containers serve /api-docs, so only the body distinguishes them.
    total=$((total + 1))
    test_body "$PATH_HOSTNAME" "/api-docs/" "$PATH_ROOT_BODY" \
        "/api-docs is not captured by the /api mount" && passed=$((passed + 1))

    # HTTPS must route identically.
    total=$((total + 1))
    test_body_https "$PATH_HOSTNAME" "/api/" "$PATH_MOUNTED_BODY" \
        "/api/ over HTTPS is served by the mounted container" && passed=$((passed + 1))

    total=$((total + 1))
    test_body_https "$PATH_HOSTNAME" "/" "$PATH_ROOT_BODY" \
        "root over HTTPS is served by the host container" && passed=$((passed + 1))

    log "VIRTUAL_PATH routing: ${passed}/${total} checks passed"
    [ "$passed" -eq "$total" ]
}

# A mounted path must outrank a wildcard that also matches its hostname. Without
# an explicit priority this depends on which generated rule happens to be the
# longer string, which is not something to leave to chance.
test_virtual_path_beats_wildcard() {
    local passed=0 total=0

    total=$((total + 1))
    test_body "$WILDCARD_HOSTNAME" "/" "$WILDCARD_BODY" \
        "root of ${WILDCARD_HOSTNAME} is served by the wildcard container" && passed=$((passed + 1))

    total=$((total + 1))
    test_body "$WILDCARD_HOSTNAME" "/api" "$WILDCARD_MOUNTED_BODY" \
        "/api outranks the wildcard covering the same hostname" && passed=$((passed + 1))

    log "VIRTUAL_PATH against a wildcard: ${passed}/${total} checks passed"
    [ "$passed" -eq "$total" ]
}

# Stopping the mounted container removes its routes, so its paths fall through
# to whatever serves the hostname. A deployed ingress behaves the same way, so
# this is pinned as intended behaviour rather than left to be discovered.
test_virtual_path_fallthrough() {
    log "Stopping the mounted container to check the documented fall-through..."
    docker rm -f "$PATH_MOUNTED_CONTAINER" >/dev/null 2>&1 || true
    wait_with_message "$SLEEP_PROXY_CONFIG" "for the mounted route to be withdrawn"

    local passed=0 total=0

    total=$((total + 1))
    test_body "$PATH_HOSTNAME" "/api" "$PATH_ROOT_BODY" \
        "/api falls through to the host container once the mounted one stops" && passed=$((passed + 1))

    total=$((total + 1))
    test_body "$PATH_HOSTNAME" "/" "$PATH_ROOT_BODY" \
        "the host container is unaffected" && passed=$((passed + 1))

    log "VIRTUAL_PATH fall-through: ${passed}/${total} checks passed"
    [ "$passed" -eq "$total" ]
}

# Cleanup test containers
# Tailnet peer routing: a second Traefik stands in for a second machine, found
# through a synthetic status document read by the file source.

peer_compose() {
    COMPOSE_PROFILES=tailscale docker compose -f compose.yml -f test/compose.peers.yml "$@"
}

cleanup_peer_stack() {
    peer_compose rm -sf tailscale_peers >/dev/null 2>&1 || true
    docker rm -f "$PEER_PROXY_CONTAINER" "$PEER_ROOT_CONTAINER" "$PEER_API_CONTAINER" \
        "$PEER_SHARED_CONTAINER" "$LOCAL_SHARED_CONTAINER" >/dev/null 2>&1 || true
    rm -rf "$PEER_STATE_DIR" "$PEER_DYNAMIC_DIR" "$PEER_CONFIG_FILE" \
        "$PEER_MERGED_COMPOSE" "$PEER_CLI_HOME" 2>/dev/null || true
}

# Writes the static configuration of the stand-in proxy, which serves route
# queries on 30000 itself.
write_peer_static_config() {
    mkdir -p "$PEER_DYNAMIC_DIR"

    cat > "${PEER_CONFIG_FILE}" <<EOF
api:
  dashboard: true
  insecure: true

entryPoints:
  http:
    address: ":80"
  traefik:
    address: ":30000"

providers:
  file:
    directory: "/traefik/dynamic"
    watch: true

serversTransport:
  insecureSkipVerify: true
EOF
}

# Writes the routing table the stand-in publishes, http entrypoint only.
write_peer_routing_table() {
    mkdir -p "$PEER_DYNAMIC_DIR"

    # The stand-in declares itself, as a real machine does.
    cat > "${PEER_DYNAMIC_DIR}/spark-http-proxy-declaration.yaml" <<'EOF'
http:
  middlewares:
    spark-http-proxy:
      headers:
        customResponseHeaders:
          X-Spark-Http-Proxy: "1"
EOF

    cat > "${PEER_DYNAMIC_DIR}/peer-machine.yaml" <<EOF
http:
  routers:
    remote-0:
      rule: "Host(\`${PEER_HOSTNAME}\`)"
      service: remote
      entryPoints: [http]
    remote-api-0:
      rule: "Host(\`${PEER_HOSTNAME}\`) && (PathPrefix(\`/api/\`) || Path(\`/api\`))"
      priority: 10004
      service: remote-api
      entryPoints: [http]
    shared-0:
      rule: "Host(\`${PEER_SHARED_HOSTNAME}\`)"
      service: shared
      entryPoints: [http]
  services:
    remote:
      loadBalancer:
        servers:
          - url: "http://${PEER_ROOT_CONTAINER}:80"
    remote-api:
      loadBalancer:
        servers:
          - url: "http://${PEER_API_CONTAINER}:80"
    shared:
      loadBalancer:
        servers:
          - url: "http://${PEER_SHARED_CONTAINER}:80"
EOF
}

# Writes the tailnet status document, with the stand-in's account as argument.
write_tailnet_status() {
    local peer_user_id="$1" peer_address="$2"

    mkdir -p "$PEER_STATE_DIR"
    chmod 700 "$PEER_STATE_DIR"

    cat > "${PEER_STATE_DIR}/tailscale-status.json" <<EOF
{
  "Self": {"HostName": "machine-a", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.1"]},
  "Peer": {
    "nodekey:machine-b": {
      "HostName": "machine-b",
      "Online": true,
      "UserID": ${peer_user_id},
      "TailscaleIPs": ["${peer_address}"]
    }
  }
}
EOF
}

# Runs the CLI against the test stack, with its own home and a merged compose
# file carrying the test's peer routing settings.
peer_cli() {
    HOME="$(pwd)/${PEER_CLI_HOME}" \
    COMPOSE_FILE="$(pwd)/${PEER_MERGED_COMPOSE}" \
    COMPOSE_PROJECT_NAME="$(basename "$(pwd)")" \
        bin/spark-http-proxy "$@"
}

# Gives the CLI a home whose state directory is the one the service writes to.
prepare_peer_cli_home() {
    rm -rf "$PEER_CLI_HOME"
    mkdir -p "${PEER_CLI_HOME}/.local/spark/http-proxy"
    ln -s "$(pwd)/${PEER_STATE_DIR}" "${PEER_CLI_HOME}/.local/spark/http-proxy/state"
    printf 'true\n' >"${PEER_CLI_HOME}/.local/spark/http-proxy/tailscale-enabled"
    # Both profiles active, so the rendered file keeps every service and the
    # CLI's own resolution decides which of them a command brings up.
    COMPOSE_PROFILES=tailscale,metrics docker compose -f compose.yml -f test/compose.peers.yml \
        config >"$PEER_MERGED_COMPOSE" 2>/dev/null
}

# Polls until a hostname stops answering with a body.
test_body_absent() {
    local hostname=$1 path=$2 unexpected=$3 label=$4
    local max_attempts=10
    local attempt=1
    local body=""

    log "Testing ${label}..."

    while [ $attempt -le $max_attempts ]; do
        body="$(curl -s -L \
            --resolve "${hostname}:${HTTP_PORT}:127.0.0.1" \
            "http://${hostname}${path}" 2>/dev/null || true)"
        if [ "$body" != "$unexpected" ]; then
            success "${label}"
            return 0
        fi
        wait_with_message "$SLEEP_CONFIG_RESTORE" "for the route to be withdrawn"
        attempt=$((attempt + 1))
    done

    error "${label} (still answering with '${unexpected}')"
    return 1
}

# The launchd status agent, exercised on any platform: the CLI is sourced with
# a fake HOME and a stub launchctl, so the assertions are about the code rather
# than about the host.
test_status_agent() {
    local passed=0 total=0
    local home stub_dir plist defs
    home="$(mktemp -d)"
    stub_dir="${home}/stubs"

    # The definitions are written inside bin/ so the CLI resolves its own
    # directory, and with it the compose file, as it does when run for real.
    defs="bin/.status-agent-defs"
    sed '/^case "$1" in/,$d' bin/spark-http-proxy >"${defs}"
    mkdir -p "${stub_dir}"
    plist="${home}/Library/LaunchAgents/com.sparkfabrik.http-proxy.tailscale-status.plist"

    # A client the installer can resolve, in a directory the agent PATH names.
    printf '#!/bin/sh\nexit 0\n' >"${stub_dir}/tailscale"
    chmod +x "${stub_dir}/tailscale"

    # launchctl that reports success for every verb, and a print that answers
    # whether the label was recorded as loaded by the stub itself.
    cat >"${stub_dir}/launchctl" <<'STUB'
#!/bin/sh
case "${1}" in
print) [ -f "${LAUNCHCTL_STUB_LOADED}" ] ;;
bootstrap) [ "${LAUNCHCTL_STUB_BOOTSTRAP_LOADS}" = "yes" ] && : >"${LAUNCHCTL_STUB_LOADED}"; exit 0 ;;
bootout) [ "${LAUNCHCTL_STUB_BOOTOUT_UNLOADS:-yes}" = "yes" ] && rm -f "${LAUNCHCTL_STUB_LOADED}"; exit 0 ;;
esac
STUB
    chmod +x "${stub_dir}/launchctl"

    # The second argument is the PATH the installing shell carries.
    run_agent_install() {
        HOME="${home}" \
            PATH="${2:-${stub_dir}:/usr/bin:/bin}" \
            HTTP_PROXY_TAILSCALE_AGENT_PATH="${stub_dir}:/usr/bin:/bin" \
            LAUNCHCTL_STUB_LOADED="${home}/loaded" \
            LAUNCHCTL_STUB_BOOTSTRAP_LOADS="${1}" \
            DEFS="${defs}" \
            bash -c '. "${DEFS}"; install_status_agent' 2>&1
    }

    total=$((total + 1))
    if run_agent_install no | grep -q "Could not load"; then
        success "an install that loads nothing reports failure"
        passed=$((passed + 1))
    else
        error "an install that loaded nothing reported success"
    fi

    total=$((total + 1))
    if run_agent_install yes | grep -q "Status refresh installed"; then
        success "an install that loads the label reports success"
        passed=$((passed + 1))
    else
        error "an install that loaded the label did not report success"
    fi

    local first second
    run_agent_install yes "${stub_dir}:/usr/bin:/bin" >/dev/null 2>&1
    first="$(cat "${plist}")"
    run_agent_install yes "${stub_dir}:/usr/bin:/bin:/sbin:${home}" >/dev/null 2>&1
    second="$(cat "${plist}")"

    total=$((total + 1))
    if [ -n "${first}" ] && [ "${first}" = "${second}" ]; then
        success "the plist is the same whichever shell installed it"
        passed=$((passed + 1))
    else
        error "the plist changed with the installing shell"
    fi

    total=$((total + 1))
    if grep -q "<string>${stub_dir}:/usr/bin:/bin</string>" "${plist}"; then
        success "the plist carries the agent PATH"
        passed=$((passed + 1))
    else
        error "the plist does not carry the agent PATH"
    fi

    # Removal reports what it achieved rather than what it attempted.
    run_removal() {
        HOME="${home}" \
            PATH="${stub_dir}:/usr/bin:/bin" \
            LAUNCHCTL_STUB_LOADED="${1}" \
            LAUNCHCTL_STUB_BOOTOUT_UNLOADS="${2:-yes}" \
            DEFS="${defs}" \
            bash -c '. "${DEFS}"; remove_status_agent' 2>&1
    }

    run_agent_install yes >/dev/null 2>&1
    total=$((total + 1))
    if run_removal "${home}/loaded" | grep -q "Status refresh removed" && [ ! -e "${plist}" ]; then
        success "removal that unloads the label reports removal"
        passed=$((passed + 1))
    else
        error "removal did not report removal after unloading the label"
    fi

    # A label still loaded after bootout must not be reported as removed.
    run_agent_install yes >/dev/null 2>&1
    total=$((total + 1))
    if run_removal "${home}/loaded" no | grep -q "still loaded"; then
        success "removal that leaves the label loaded says so"
        passed=$((passed + 1))
    else
        error "removal reported success with the label still loaded"
    fi

    rm -rf "${home}" "${defs}"
    log "Status agent tests: ${passed}/${total} passed"
    [ "${passed}" -eq "${total}" ]
}

test_tailscale_peer_routing() {
    local passed=0 total=0
    local traefik_image peer_address

    log "Testing tailnet peer routing..."

    traefik_image="$(docker inspect -f '{{.Config.Image}}' http-proxy 2>/dev/null || true)"
    if [ -z "$traefik_image" ]; then
        error "Cannot determine the Traefik image, is the stack running?"
        return 1
    fi

    # The second machine's backends, reachable from its proxy by container name.
    run_body_container "$PEER_ROOT_CONTAINER" "$PEER_ROOT_BODY" --network http-proxy_default
    run_body_container "$PEER_API_CONTAINER" "$PEER_API_BODY" --network http-proxy_default
    run_body_container "$PEER_SHARED_CONTAINER" "$PEER_SHARED_BODY" --network http-proxy_default

    # The same hostname served locally, to prove the local container wins.
    run_body_container "$LOCAL_SHARED_CONTAINER" "$LOCAL_SHARED_BODY" \
        --env "VIRTUAL_HOST=${PEER_SHARED_HOSTNAME}"

    write_peer_static_config
    write_peer_routing_table

    docker run -d --name "$PEER_PROXY_CONTAINER" \
        --network http-proxy_default \
        -v "$(pwd)/${PEER_CONFIG_FILE}:/etc/traefik/traefik.yml:ro" \
        -v "$(pwd)/${PEER_DYNAMIC_DIR}:/traefik/dynamic" \
        "$traefik_image" >/dev/null

    wait_with_message "$SLEEP_PROXY_CONFIG" "for the second machine's proxy to start"

    peer_address="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$PEER_PROXY_CONTAINER")"
    if [ -z "$peer_address" ]; then
        error "Cannot determine the second machine's address"
        return 1
    fi

    # A machine belonging to another account must contribute nothing, however
    # reachable it is.
    write_tailnet_status 2 "$peer_address"
    peer_compose up -d tailscale_peers >/dev/null 2>&1
    wait_with_message "$SLEEP_PROXY_CONFIG" "for a discovery cycle to run"

    total=$((total + 1))
    if test_body_absent "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "a machine belonging to another account contributes nothing"; then
        passed=$((passed + 1))
    fi

    # The same machine, now on this account.
    write_tailnet_status 1 "$peer_address"
    wait_with_message "$SLEEP_PROXY_CONFIG" "for the peer's routes to be picked up"

    total=$((total + 1))
    if test_body "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "a hostname served only by the second machine is answered by its container"; then
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if test_body_https "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "the same hostname over HTTPS, terminated locally"; then
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if test_body "$PEER_HOSTNAME" "/api" "$PEER_API_BODY" \
        "a path mounted on the second machine is reached through its path"; then
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if test_body "$PEER_SHARED_HOSTNAME" "" "$LOCAL_SHARED_BODY" \
        "a hostname served both locally and remotely is answered locally"; then
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if [ -r "${PEER_STATE_DIR}/tailscale-peers.txt" ] && grep -q "machine-b" "${PEER_STATE_DIR}/tailscale-peers.txt" 2>/dev/null; then
        success "the discovery cycle is recorded where the command line reads it"
        passed=$((passed + 1))
    else
        find "${PEER_STATE_DIR}" -maxdepth 1 -exec ls -ld {} + 2>&1 | sed 's/^/    /'
        error "the peer report at ${PEER_STATE_DIR}/tailscale-peers.txt is missing or unreadable"
    fi

    write_tailnet_status 1 "$peer_address"

    # The recorded state, not the caller's environment, is what a later command
    # and a stack recreation read.
    prepare_peer_cli_home

    total=$((total + 1))
    if peer_cli tailscale-peers 2>&1 | grep -q "machine-b"; then
        success "the command reports peers with no variable in the environment"
        passed=$((passed + 1))
    else
        error "the command did not report peers from the recorded state"
    fi

    # The profile a lifecycle command would bring up, without bringing it up.
    total=$((total + 1))
    if peer_cli config --services 2>/dev/null | grep -q "tailscale_peers"; then
        success "a recreation addresses peer routing from the recorded state"
        passed=$((passed + 1))
    else
        error "a recreation would not address peer routing"
    fi

    printf 'metrics=true\n' >>"${PEER_CLI_HOME}/.local/spark/http-proxy/optional-stacks"

    total=$((total + 1))
    if peer_cli config --services 2>/dev/null | grep -q "prometheus"; then
        success "a recreation keeps monitoring, which a plain start used to destroy"
        passed=$((passed + 1))
    else
        error "a recreation would destroy monitoring, as it did before the record"
    fi

    local peers_before
    peers_before="$(docker inspect -f '{{.Id}}' "$(basename "$(pwd)")-tailscale_peers-1" 2>/dev/null || echo none)"

    peer_cli up -d --force-recreate >/dev/null 2>&1 || warning "the CLI recreate reported an error"
    wait_with_message "$SLEEP_PROXY_CONFIG" "for the stack to come back"

    total=$((total + 1))
    if [ "$(docker inspect -f '{{.Id}}' "$(basename "$(pwd)")-tailscale_peers-1" 2>/dev/null || echo none)" != "$peers_before" ]; then
        success "recreating the stack recreates peer routing, so an upgrade reaches it"
        passed=$((passed + 1))
    else
        error "peer routing was left on its old container, as an upgrade used to leave it"
    fi

    peer_cli stop-metrics >/dev/null 2>&1 || true

    total=$((total + 1))
    if peer_cli config --services 2>/dev/null | grep -q "prometheus"; then
        error "stopping monitoring left it in the recorded state"
    else
        success "stopping monitoring clears it from the recorded state"
        passed=$((passed + 1))
    fi

    # Stopping peer routing alone withdraws the routes without a proxy restart.
    peer_compose stop tailscale_peers || warning "could not stop the peer routing service"
    log "Peer configuration after stopping the service: $(docker exec "$TRAEFIK_PROXY_NAME" sh -c 'ls -1 /traefik/dynamic/ | grep tailscale-peer || echo none' 2>/dev/null)"

    total=$((total + 1))
    if test_body_absent "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "stopping peer routing alone withdraws the forwarded hostname"; then
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if test_body "$PEER_SHARED_HOSTNAME" "" "$LOCAL_SHARED_BODY" \
        "the proxy keeps serving its own containers with peer routing stopped"; then
        passed=$((passed + 1))
    fi

    peer_compose start tailscale_peers >/dev/null 2>&1 || true
    wait_with_message "$SLEEP_PROXY_CONFIG" "for peer routing to come back"

    total=$((total + 1))
    if test_body "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "starting peer routing again restores the forwarded hostname"; then
        passed=$((passed + 1))
    fi

    write_tailnet_status 1 "$peer_address"

    # The source follows the socket the host has, not a setting.
    local absent_socket="${PEER_CLI_HOME}/no-such-socket"

    total=$((total + 1))
    if HTTP_PROXY_TAILSCALE_SOCKET="${absent_socket}" peer_cli show-config 2>/dev/null | grep -q "Peer routing source: file"; then
        success "the source is the file when the host has no daemon socket"
        passed=$((passed + 1))
    else
        error "the source did not fall back to the file with no socket present"
    fi

    # A real unix socket, so the assertion tests its own fixture rather than
    # whatever the host happens to have. Bound through a chdir because AF_UNIX
    # paths are limited to about 100 characters.
    local fake_socket="${PEER_CLI_HOME}/tailscaled.sock"
    rm -f "${fake_socket}"
    python3 -c "
import os, socket, sys
path = sys.argv[1]
os.chdir(os.path.dirname(path))
s = socket.socket(socket.AF_UNIX)
s.bind(os.path.basename(path))
" "${fake_socket}"

    total=$((total + 1))
    if HTTP_PROXY_TAILSCALE_SOCKET="${fake_socket}" peer_cli show-config 2>/dev/null | grep -q "Peer routing source: socket"; then
        success "the source is the socket when the host has one"
        passed=$((passed + 1))
    else
        error "the source did not resolve to the socket with one present"
    fi

    # A regular file at the socket path is not a socket: a bind mount of a
    # missing path leaves a directory or a file there.
    local not_a_socket="${PEER_CLI_HOME}/not-a-socket"
    : >"${not_a_socket}"

    total=$((total + 1))
    if HTTP_PROXY_TAILSCALE_SOCKET="${not_a_socket}" peer_cli show-config 2>/dev/null | grep -q "Peer routing source: file"; then
        success "a regular file at the socket path is not treated as a socket"
        passed=$((passed + 1))
    else
        error "a regular file at the socket path was treated as a socket"
    fi

    total=$((total + 1))
    if HTTP_PROXY_TAILSCALE_SOCKET="${absent_socket}" HTTP_PROXY_TAILSCALE_SOURCE=socket \
        peer_cli show-config 2>/dev/null | grep -q "Peer routing source: socket"; then
        success "an explicit source overrides what the host has"
        passed=$((passed + 1))
    else
        error "the explicit source was not honoured"
    fi

    # A cleared entry must leave the rest of the record readable, and must
    # leave the line boundary a later record appends after.
    local record="${PEER_CLI_HOME}/.local/spark/http-proxy/optional-stacks"
    printf 'tailscale=true\nmetrics=true\n' >"${record}"
    peer_cli stop-metrics >/dev/null 2>&1 || true

    total=$((total + 1))
    if ! grep -qx "tailscale=true" "${record}" 2>/dev/null; then
        error "clearing one entry lost the other: $(tr '\n' ' ' <"${record}" 2>/dev/null)"
    elif [ -n "$(tail -c1 "${record}")" ]; then
        error "clearing one entry left the record without its final newline, so the next record concatenates"
    else
        success "clearing one entry leaves the record intact"
        passed=$((passed + 1))
    fi

    # An unreadable record is left alone rather than deleted.
    printf 'tailscale=true\nmetrics=true\n' >"${record}"
    chmod 000 "${record}"
    peer_cli stop-metrics >/dev/null 2>&1 || true
    chmod 644 "${record}" 2>/dev/null || true

    total=$((total + 1))
    if [ -f "${record}" ] && grep -qx "tailscale=true" "${record}" && grep -qx "metrics=true" "${record}"; then
        success "an unreadable record is left alone rather than deleted"
        passed=$((passed + 1))
    else
        error "an unreadable record was destroyed: $(tr '\n' ' ' <"${record}" 2>&1)"
    fi

    printf 'tailscale=true\n' >"${record}"
    printf 'metrics=true\n' >>"${record}"

    total=$((total + 1))
    if grep -qx "tailscale=true" "${record}" && grep -qx "metrics=true" "${record}"; then
        success "a record written after a clear is readable"
        passed=$((passed + 1))
    else
        error "the record was corrupted: $(tr '\n' ' ' <"${record}")"
    fi

    # clean addresses every recorded stack, so nothing is left behind for a
    # later start to find in an unknown state.
    peer_cli clean >/dev/null 2>&1 || warning "the CLI clean reported an error"

    total=$((total + 1))
    if docker ps --filter "label=com.docker.compose.project=$(basename "$(pwd)")" \
        --format '{{.Names}}' | grep -q "tailscale_peers"; then
        error "clean left the peer routing container running"
    else
        success "clean removes the peer routing container"
        passed=$((passed + 1))
    fi

    peer_compose up -d >/dev/null 2>&1
    wait_with_message "$SLEEP_STACK_START" "for the stack to come back after clean"
    write_tailnet_status 1 "$peer_address"
    wait_with_message "$SLEEP_PROXY_CONFIG" "for peer routing to settle"

    total=$((total + 1))
    if test_body "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "the stack forwards again after clean and a fresh start"; then
        passed=$((passed + 1))
    fi

    # A machine that goes away takes its hostnames with it.
    docker rm -f "$PEER_PROXY_CONTAINER" >/dev/null 2>&1 || true

    total=$((total + 1))
    if test_body_absent "$PEER_HOSTNAME" "" "$PEER_ROOT_BODY" \
        "stopping the second machine withdraws its hostnames"; then
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if test_body "$PEER_SHARED_HOSTNAME" "" "$LOCAL_SHARED_BODY" \
        "the local container keeps answering after the peer goes away"; then
        passed=$((passed + 1))
    fi

    cleanup_peer_stack

    log "Tailnet peer routing: ${passed}/${total} passed"
    [ "$passed" -eq "$total" ]
}

cleanup() {
    cleanup_peer_stack
    docker rm -f "$TRAEFIK_CONTAINER" "$VIRTUAL_HOST_CONTAINER" "$VIRTUAL_HOST_PORT_CONTAINER" "$MULTI_VIRTUAL_HOST_CONTAINER" "$ORPHAN_CONTAINER" "$ONEOFF_CONTAINER" "$PATH_ROOT_CONTAINER" "$PATH_MOUNTED_CONTAINER" "$WILDCARD_CONTAINER" "$WILDCARD_MOUNTED_CONTAINER" 2>/dev/null || true
}

# Takes the stack down at the end of a run, volumes included.
teardown_stack() {
    log "Removing the test stack and its volumes..."
    COMPOSE_PROFILES=tailscale docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}

# Full stack cleanup and rebuild
full_cleanup_and_rebuild() {
    log "Setting up HTTP proxy stack..."
    cd "$(dirname "$0")/.."
    COMPOSE_PROFILES=tailscale docker compose down --volumes --remove-orphans 2>/dev/null || true
    cleanup
    docker image prune -f >/dev/null 2>&1 || true
    log "Building Docker images..."
    # With the profile: compose build skips services whose profile is inactive.
    COMPOSE_PROFILES=tailscale docker compose build --pull
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

    # Two containers sharing one hostname: the second is mounted under /api.
    run_body_container "$PATH_ROOT_CONTAINER" "$PATH_ROOT_BODY" \
        --env "VIRTUAL_HOST=${PATH_HOSTNAME}"
    run_body_container "$PATH_MOUNTED_CONTAINER" "$PATH_MOUNTED_BODY" \
        --env "VIRTUAL_HOST=${PATH_HOSTNAME}" --env "VIRTUAL_PATH=/api"

    # A wildcard covering the same hostname, to prove the mount outranks it.
    run_body_container "$WILDCARD_CONTAINER" "$WILDCARD_BODY" \
        --env "VIRTUAL_HOST=*.wild.${TEST_DOMAIN}"
    run_body_container "$WILDCARD_MOUNTED_CONTAINER" "$WILDCARD_MOUNTED_BODY" \
        --env "VIRTUAL_HOST=${WILDCARD_HOSTNAME}" --env "VIRTUAL_PATH=/api"

    # Wait for containers
    wait_for_container "$TRAEFIK_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_CONTAINER"
    wait_for_container "$VIRTUAL_HOST_PORT_CONTAINER"
    wait_for_container "$MULTI_VIRTUAL_HOST_CONTAINER"
    wait_for_container "$PATH_ROOT_CONTAINER"
    wait_for_container "$PATH_MOUNTED_CONTAINER"
    wait_for_container "$WILDCARD_CONTAINER"
    wait_for_container "$WILDCARD_MOUNTED_CONTAINER"
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

    # Orphaned config reconciliation test (issue #109)
    log "Testing orphaned config reconciliation..."
    total=$((total + 1))
    local orphan_passed=0
    test_orphan_config_reconciliation && orphan_passed=1
    [ "$orphan_passed" -eq 1 ] && passed=$((passed + 1))

    # One-off compose container test (issue #111)
    log "Testing one-off compose containers are ignored..."
    total=$((total + 1))
    local oneoff_passed=0
    test_oneoff_container_ignored && oneoff_passed=1
    [ "$oneoff_passed" -eq 1 ] && passed=$((passed + 1))

    # VIRTUAL_PATH routing (issue #113)
    log "Testing VIRTUAL_PATH routing..."
    total=$((total + 1))
    local vpath_passed=0
    test_virtual_path_routing && vpath_passed=1
    [ "$vpath_passed" -eq 1 ] && passed=$((passed + 1))

    log "Testing VIRTUAL_PATH against a wildcard host..."
    total=$((total + 1))
    local vpath_wildcard_passed=0
    test_virtual_path_beats_wildcard && vpath_wildcard_passed=1
    [ "$vpath_wildcard_passed" -eq 1 ] && passed=$((passed + 1))

    # Runs last of the VIRTUAL_PATH suites: it removes the mounted container,
    # so anything after it would see the hostname without its path.
    log "Testing VIRTUAL_PATH fall-through when the mounted container stops..."
    total=$((total + 1))
    local vpath_fallthrough_passed=0
    test_virtual_path_fallthrough && vpath_fallthrough_passed=1
    [ "$vpath_fallthrough_passed" -eq 1 ] && passed=$((passed + 1))

    log "Testing the launchd status agent..."
    total=$((total + 1))
    local agent_passed=0
    test_status_agent && agent_passed=1
    [ "$agent_passed" -eq 1 ] && passed=$((passed + 1))

    # Tailnet peer routing: brings up a second proxy of its own, and removes it.
    log "Testing tailnet peer routing..."
    total=$((total + 1))
    local peers_passed=0
    test_tailscale_peer_routing && peers_passed=1
    [ "$peers_passed" -eq 1 ] && passed=$((passed + 1))

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
    teardown_stack

    log "Test Results:"
    log "============="
    log "HTTP Tests: ${http_passed}/5 passed"
    log "HSTS Tests: ${hsts_passed}/5 passed"
    log "Orphan Reconciliation Tests: ${orphan_passed}/1 passed"
    log "One-off Container Tests: ${oneoff_passed}/1 passed"
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

# Sourcing this file defines its functions and runs nothing, so one of them can
# be exercised on its own without the suite touching the machine.
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
    return 0
fi

# Run tests and capture exit code
main "$@"
exit_code=$?

# Exit with the same code as main function
exit $exit_code
