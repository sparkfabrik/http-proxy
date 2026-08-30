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
bootout)
  [ "${LAUNCHCTL_STUB_BOOTOUT_UNLOADS:-yes}" = "yes" ] && rm -f "${LAUNCHCTL_STUB_LOADED}"
  [ "${LAUNCHCTL_STUB_BOOTOUT_FAILS:-no}" = "yes" ] && exit 1
  exit 0
  ;;
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

    # A machine with no agent yet: bootout fails because there is nothing to
    # boot out, which must not stop the first install.
    rm -f "${home}/loaded"
    total=$((total + 1))
    if HOME="${home}" PATH="${stub_dir}:/usr/bin:/bin" \
        HTTP_PROXY_TAILSCALE_AGENT_PATH="${stub_dir}:/usr/bin:/bin" \
        LAUNCHCTL_STUB_LOADED="${home}/loaded" \
        LAUNCHCTL_STUB_BOOTSTRAP_LOADS=yes \
        LAUNCHCTL_STUB_BOOTOUT_FAILS=yes \
        DEFS="${defs}" \
        bash -c '. "${DEFS}"; install_status_agent' 2>&1 | grep -q "Status refresh installed"; then
        success "the first install proceeds when bootout has nothing to unload"
        passed=$((passed + 1))
    else
        error "a failed bootout stopped the first install"
    fi

    # An agent left loaded by a failed bootout is still the old one, so a
    # bootstrap that does nothing must not read as a successful install.
    run_agent_install yes >/dev/null 2>&1
    total=$((total + 1))
    if HOME="${home}" PATH="${stub_dir}:/usr/bin:/bin" \
        HTTP_PROXY_TAILSCALE_AGENT_PATH="${stub_dir}:/usr/bin:/bin" \
        LAUNCHCTL_STUB_LOADED="${home}/loaded" \
        LAUNCHCTL_STUB_BOOTSTRAP_LOADS=no \
        LAUNCHCTL_STUB_BOOTOUT_UNLOADS=no \
        DEFS="${defs}" \
        bash -c '. "${DEFS}"; install_status_agent' 2>&1 | grep -q "Could not"; then
        success "an install over an agent that will not unload reports failure"
        passed=$((passed + 1))
    else
        error "an install over a stale loaded agent reported success"
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
    if run_removal "${home}/loaded" no | grep -q "still loaded" && [ -e "${plist}" ]; then
        success "removal that leaves the label loaded says so and keeps the plist"
        passed=$((passed + 1))
    else
        error "removal lost the plist or reported success with the label still loaded"
    fi

    rm -rf "${home}" "${defs}"
    log "Status agent tests: ${passed}/${total} passed"
    [ "${passed}" -eq "${total}" ]
}

# tailscale-refresh-peers, exercised with a stub docker so no cycle can run:
# what matters is that the command reports the truth when one does not.
# What `status` reports about peer routing, across the four states a user can
# be in. The summary file is the contract, so the fixtures are that file.
# Commands printed for a reader to copy must survive being pasted. Angle
# brackets are shell redirections, so a placeholder in a suggested command is a
# syntax error rather than a hint.
# Every interactive prompt, run with no terminal. read returns non-zero at end
# of file and this script runs under errexit, so an unguarded prompt ends the
# command before the message it prepared for exactly this case.
test_prompts_without_a_terminal() {
    local passed=0 total=0 home certs out rc

    home="$(mktemp -d)"
    certs="${home}/.local/spark/http-proxy/certs"
    mkdir -p "${certs}"

    run_cli() {
        HOME="${home}" timeout 30 bin/spark-http-proxy "$@" </dev/null 2>&1
    }

    # A missing argument has a message waiting for it.
    rc=0; out="$(run_cli generate-mkcert)" || rc=$?
    total=$((total + 1))
    if echo "${out}" | grep -qi "domain name required" && [ "${rc}" -ne 0 ]; then
        success "generate-mkcert without a domain says so and fails"
        passed=$((passed + 1))
    else
        error "generate-mkcert exited ${rc} saying: $(echo "${out}" | tr '\n' ' ')"
    fi

    rc=0; out="$(run_cli remove-cert)" || rc=$?
    total=$((total + 1))
    if echo "${out}" | grep -qi "domain name required" && [ "${rc}" -ne 0 ]; then
        success "remove-cert without a domain says so and fails"
        passed=$((passed + 1))
    else
        error "remove-cert exited ${rc} saying: $(echo "${out}" | tr '\n' ' ')"
    fi

    # A destructive action that cannot be confirmed must not happen, and must
    # say why rather than stopping silently.
    touch "${certs}/app.spark.loc.pem" "${certs}/app.spark.loc-key.pem"
    rc=0; out="$(run_cli remove-cert app.spark.loc)" || rc=$?
    total=$((total + 1))
    if [ -f "${certs}/app.spark.loc.pem" ]; then
        success "remove-cert leaves the certificate when it cannot confirm"
        passed=$((passed + 1))
    else
        error "remove-cert deleted a certificate without confirmation"
    fi

    total=$((total + 1))
    if echo "${out}" | grep -qi "terminal" && [ "${rc}" -ne 0 ]; then
        success "remove-cert says why it stopped without a terminal"
        passed=$((passed + 1))
    else
        error "remove-cert stopped without explaining: exit ${rc}, $(echo "${out}" | tr '\n' ' ')"
    fi

    # destroy, against a project holding nothing, so a wrong answer here costs
    # nothing while still exercising the real command.
    local scratch
    scratch="$(mktemp -d)"
    printf 'services:\n  nothing:\n    image: alpine\n    command: ["true"]\n' >"${scratch}/compose.yml"
    rc=0
    out="$(HOME="${home}" COMPOSE_FILE="${scratch}/compose.yml" \
        COMPOSE_PROJECT_NAME="http-proxy-test-147" \
        timeout 60 bin/spark-http-proxy destroy </dev/null 2>&1)" || rc=$?

    total=$((total + 1))
    if echo "${out}" | grep -qi "terminal" && [ "${rc}" -ne 0 ]; then
        success "destroy says why it stopped without a terminal"
        passed=$((passed + 1))
    else
        error "destroy stopped without explaining: exit ${rc}, $(echo "${out}" | tr '\n' ' ')"
    fi

    total=$((total + 1))
    if ! echo "${out}" | grep -qi "resources destroyed"; then
        success "destroy does not proceed without confirmation"
        passed=$((passed + 1))
    else
        error "destroy proceeded without confirmation"
    fi

    rm -rf "${home}" "${scratch}"
    log "Prompt tests: ${passed}/${total} passed"
    [ "${passed}" -eq "${total}" ]
}

# Applying a certificate must not restart the running proxy: a restart drops
# every connection on the machine, so generating one certificate would interrupt
# every other site the developer has open.
#
# The restart survives in one place only. An image predating the entrypoint's
# --tls-only guard cannot run the scan on its own, and the timer that re-reads
# certificates only re-reads files auto-tls.yml already references, so a NEW
# certificate on such an image would never go live. There the restart is still
# the only way to apply it.
test_certificates_apply_without_a_restart() {
    local passed=0 total=0 body host started_before started_after out rc served
    local stub home log caroot traefik_image scratch mkcert_stub certs

    # A per-run hostname. The live checks write into the machine's real
    # certificate directory and delete what they wrote, so a fixed name would
    # overwrite and then destroy a developer's certificate if they happened to
    # hold one.
    #
    # Only the exact two files this run creates are ever removed. Sweeping the
    # prefix would collect orphans from an interrupted run, but a glob delete in
    # a directory the test does not own can take a file it did not write, and an
    # unused stray certificate is a smaller problem than a deleted real one.
    host="cert-restart-test-$$-${RANDOM}.spark.loc"

    # CI runners carry no mkcert, and without one the CLI stops at certificate
    # generation and never reaches the code under test. What is under test is
    # what the CLI does with the proxy once a certificate exists, not how the
    # certificate is made, so openssl stands in where mkcert is missing.
    mkcert_stub=""
    if ! command -v mkcert >/dev/null 2>&1; then
        mkcert_stub="$(mktemp -d)"
        cat >"${mkcert_stub}/mkcert" <<'MKCERT'
#!/usr/bin/env bash
cert=""; key=""; domain=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -install) exit 0 ;;
        -CAROOT) echo "${TMPDIR:-/tmp}"; exit 0 ;;
        -cert-file) cert="$2"; shift 2 ;;
        -key-file) key="$2"; shift 2 ;;
        *) domain="$1"; shift ;;
    esac
done
# stderr is deliberately not discarded: a stub that hides why it failed makes
# the CLI look broken and says nothing about the machine it ran on.
openssl req -x509 -newkey rsa:2048 -nodes -keyout "${key}" -out "${cert}" \
    -days 1 -subj "/CN=${domain}" -addext "subjectAltName=DNS:${domain}" 2>&1 >/dev/null
MKCERT
        chmod +x "${mkcert_stub}/mkcert"
    fi

    # The restart belongs to the shared helper, which reaches it only when the
    # guard is absent. A restart in either command's own body is unconditional.
    for body in generate_mkcert remove_cert; do
        total=$((total + 1))
        if awk "/^${body}\(\) \{/,/^\}/" bin/spark-http-proxy | grep -q "dc_cmd restart"; then
            error "${body} restarts the proxy directly rather than through the helper"
        else
            success "${body} does not restart the proxy directly"
            passed=$((passed + 1))
        fi
    done

    # A stubbed docker stands in for both images, so the fallback is exercised
    # without building one. STUB_GUARD decides whether the probe finds the guard.
    stub="$(mktemp -d)"
    log="${stub}/calls"
    cat >"${stub}/docker" <<'STUB'
#!/usr/bin/env bash
# The guard probe carries --tls-only too, so it is matched before the scan.
case "$*" in
    *grep*--tls-only*) exit "${STUB_GUARD_MISSING:-0}" ;;
esac
for arg in "$@"; do
    case "${arg}" in
        restart) echo "restart" >>"${STUB_LOG}"; exit 0 ;;
        --tls-only)
            echo "scan" >>"${STUB_LOG}"
            [ -z "${STUB_SCAN_FAILS:-}" ] && exit 0
            echo "permission denied" >&2
            exit 126
            ;;
    esac
done
case "$*" in
    *ps*) [ -n "${STUB_NOT_RUNNING:-}" ] || echo "http-proxy"; exit 0 ;;
esac
exit 0
STUB
    chmod +x "${stub}/docker"

    caroot="$(mkcert -CAROOT 2>/dev/null || true)"
    home="$(mktemp -d)"

    stub_generate() {
        : >"${log}"
        env PATH="${stub}:${mkcert_stub:+${mkcert_stub}:}${PATH}" HOME="${home}" \
            CAROOT="${caroot}" STUB_LOG="${log}" "$@" \
            timeout 60 bin/spark-http-proxy generate-mkcert "${host}" </dev/null 2>&1
    }

    # An image carrying the guard: the scan runs and nothing is restarted.
    out="$(stub_generate STUB_GUARD_MISSING=0)"
    total=$((total + 1))
    if grep -q "scan" "${log}" && ! grep -q "restart" "${log}"; then
        success "a proxy carrying the guard has the scan run, not a restart"
        passed=$((passed + 1))
    else
        error "expected a scan and no restart, the calls were: $(tr '\n' ' ' <"${log}")"
    fi

    total=$((total + 1))
    if ! echo "${out}" | grep -qi "restarting"; then
        success "the output does not announce a restart that did not happen"
        passed=$((passed + 1))
    else
        error "the output announced a restart: $(echo "${out}" | tr '\n' ' ')"
    fi

    # A scan that fails must say what the proxy said. Without it the warning
    # reports that something did not work and nothing about what to look at.
    rc=0; out="$(stub_generate STUB_SCAN_FAILS=1)" || rc=$?
    total=$((total + 1))
    if echo "${out}" | grep -q "did not apply" && echo "${out}" | grep -q "permission denied"; then
        success "a failed scan reports the proxy's own reason"
        passed=$((passed + 1))
    else
        error "a failed scan gave no reason: $(echo "${out}" | grep -E "⚠️|ℹ" | tr '\n' ' ')"
    fi

    total=$((total + 1))
    if [ "${rc}" -ne 0 ]; then
        success "a failed scan makes the command exit non-zero"
        passed=$((passed + 1))
    else
        error "the certificate was not applied and the command reported success"
    fi

    # An image predating the guard: the restart is still the only way to apply a
    # new certificate, so it must still happen.
    out="$(stub_generate STUB_GUARD_MISSING=1)"
    total=$((total + 1))
    if grep -q "restart" "${log}"; then
        success "a proxy without the guard is still restarted, so the certificate applies"
        passed=$((passed + 1))
    else
        error "a certificate was generated against an older proxy and never applied: $(tr '\n' ' ' <"${log}")"
    fi

    # The proxy is not running: nothing is applied, nothing fails, nothing starts.
    rc=0; out="$(stub_generate STUB_NOT_RUNNING=1)" || rc=$?
    total=$((total + 1))
    if [ "${rc}" -eq 0 ] && ! grep -qE "restart|scan" "${log}"; then
        success "generate-mkcert succeeds with the proxy stopped, touching nothing"
        passed=$((passed + 1))
    else
        error "exit ${rc}, calls: $(tr '\n' ' ' <"${log}")"
    fi

    # The CLI's own error marker, not the word "error": mkcert prints a Firefox
    # trust-store warning containing it, and matching that would pass or fail
    # this assertion for a reason having nothing to do with the proxy.
    total=$((total + 1))
    if ! echo "${out}" | grep -q "❌"; then
        success "a stopped proxy is not reported as a failure"
        passed=$((passed + 1))
    else
        error "generating with the proxy stopped read as a failure: $(echo "${out}" | grep "❌" | tr '\n' ' ')"
    fi

    rm -rf "${stub}" "${home}"

    # The scan with no certificates at all, in a throwaway container so the
    # machine's own certificates are untouched. Removing the last certificate is
    # exactly this case, and an early return here leaves every reference dangling.
    traefik_image="$(docker inspect -f '{{.Config.Image}}' http-proxy 2>/dev/null || true)"
    if [ -n "${traefik_image}" ]; then
        scratch="$(mktemp -d)"
        mkdir -p "${scratch}/certs" "${scratch}/dynamic"
        printf 'stale content referencing a deleted certificate\n' >"${scratch}/dynamic/auto-tls.yml"
        rc=0
        docker run --rm \
            -v "$(pwd)/build/traefik/entrypoint.sh:/ep.sh:ro" \
            -v "${scratch}/certs:/traefik/certs" \
            -v "${scratch}/dynamic:/traefik/dynamic" \
            --entrypoint sh "${traefik_image}" /ep.sh --tls-only >/dev/null 2>&1 || rc=$?

        # The empty list itself, not the absence of the old content. Truncating
        # the file, deleting it, or failing halfway would all remove the stale
        # text while leaving Traefik without the configuration it reads.
        total=$((total + 1))
        if [ "${rc}" -eq 0 ] &&
            grep -q '^[[:space:]]*certificates:[[:space:]]*\[\][[:space:]]*$' \
                "${scratch}/dynamic/auto-tls.yml" 2>/dev/null; then
            success "a scan finding no certificates writes an empty certificate list"
            passed=$((passed + 1))
        else
            error "the empty scan exited ${rc} and left: $(cat "${scratch}/dynamic/auto-tls.yml" 2>&1 | tr '\n' ' ')"
        fi
        rm -rf "${scratch}"
    fi

    if [ -z "${mkcert_stub}" ] && ! command -v mkcert >/dev/null 2>&1; then
        log "Certificate application tests: ${passed}/${total} passed (no certificate tool, live checks skipped)"
        [ "${passed}" -eq "${total}" ]
        return
    fi

    # The same thing again against the real stack, which is the only proof that
    # the scan reaches Traefik rather than merely being invoked.
    started_before="$(docker inspect -f '{{.State.StartedAt}}' http-proxy 2>/dev/null || true)"
    if [ -z "${started_before}" ]; then
        error "Cannot read the proxy's start time, is the stack running?"
        return 1
    fi

    certs="${CERT_DIR:-${HOME}/.local/spark/http-proxy/certs}"
    rc=0
    out="$(env PATH="${mkcert_stub:+${mkcert_stub}:}${PATH}" \
        timeout 60 bin/spark-http-proxy generate-mkcert "${host}" </dev/null 2>&1)" || rc=$?

    total=$((total + 1))
    if [ "${rc}" -eq 0 ]; then
        success "generate-mkcert succeeds against the running stack"
        passed=$((passed + 1))
    else
        # The directory is named because a stack brought up before it existed has
        # docker create it, owned by root, and then nothing the user runs can
        # write a certificate into it.
        error "generate-mkcert exited ${rc}: $(echo "${out}" | tr '\n' ' ')"
        error "  certificate directory: $(ls -ld "${certs}" 2>&1), running as $(id -un)"
    fi

    started_after="$(docker inspect -f '{{.State.StartedAt}}' http-proxy 2>/dev/null || true)"
    total=$((total + 1))
    if [ "${started_after}" = "${started_before}" ]; then
        success "the proxy was not restarted to apply the certificate"
        passed=$((passed + 1))
    else
        error "the proxy restarted: ${started_before} became ${started_after}"
    fi

    # Ten seconds, not the three that were measured: both measurements were
    # first-poll hits, so three is the sampling interval rather than a figure.
    served=""
    for _ in $(seq 1 10); do
        sleep 1
        # The SAN, not the subject. A real mkcert certificate's subject is
        # "O=mkcert development certificate, OU=<user>@<host>" and carries no
        # hostname at all, so matching the subject would only ever pass against
        # the openssl stand-in used where mkcert is absent.
        served="$(echo | timeout 5 openssl s_client -connect 127.0.0.1:443 -servername "${host}" 2>/dev/null |
            openssl x509 -noout -ext subjectAltName 2>/dev/null || true)"
        case "${served}" in
            *"${host}"*) break ;;
        esac
    done

    total=$((total + 1))
    case "${served}" in
        *"${host}"*)
            success "the certificate is served within 10 seconds, with no restart"
            passed=$((passed + 1))
            ;;
        *)
            error "the certificate is not served after 10 seconds: ${served:-nothing presented}"
            ;;
    esac

    # Removal, driven through the scan rather than through remove-cert, because
    # confirming a removal needs a terminal and CI has none. What remove-cert
    # does with the proxy is covered by the stubbed assertions above.
    rm -f "${certs}/${host}.pem" "${certs}/${host}-key.pem"
    docker exec http-proxy /entrypoint.sh --tls-only >/dev/null 2>&1 || true

    served=""
    for _ in $(seq 1 10); do
        sleep 1
        served="$(echo | timeout 5 openssl s_client -connect 127.0.0.1:443 -servername "${host}" 2>/dev/null |
            openssl x509 -noout -ext subjectAltName 2>/dev/null || true)"
        case "${served}" in
            *"${host}"*) ;;
            *) break ;;
        esac
    done

    total=$((total + 1))
    case "${served}" in
        *"${host}"*)
            error "the removed certificate is still served after 10 seconds"
            ;;
        *)
            success "a removed certificate stops being served, with no restart"
            passed=$((passed + 1))
            ;;
    esac

    started_after="$(docker inspect -f '{{.State.StartedAt}}' http-proxy 2>/dev/null || true)"
    total=$((total + 1))
    if [ "${started_after}" = "${started_before}" ]; then
        success "the proxy was not restarted at any point in this test"
        passed=$((passed + 1))
    else
        error "the proxy restarted during the test: ${started_before} became ${started_after}"
    fi

    [ -n "${mkcert_stub}" ] && rm -rf "${mkcert_stub}"

    log "Certificate application tests: ${passed}/${total} passed"
    [ "${passed}" -eq "${total}" ]
}

test_suggested_commands_are_pasteable() {
    local passed=0 total=0 offenders

    total=$((total + 1))
    offenders="$(grep -nE '(echo|log_[a-z]+|printf).*(generate-mkcert|remove-cert|start-with-tailscale|tailscale-refresh-peers)[^"]*<[a-z_-]+>' bin/spark-http-proxy || true)"
    if [ -z "${offenders}" ]; then
        success "suggested commands carry no shell metacharacters"
        passed=$((passed + 1))
    else
        error "a suggested command would break when pasted: ${offenders}"
    fi

    log "Pasteable command tests: ${passed}/${total} passed"
    [ "${passed}" -eq "${total}" ]
}

test_status_summary() {
    local passed=0 total=0
    local home state defs
    home="$(mktemp -d)"
    state="${home}/.local/spark/http-proxy/state"
    mkdir -p "${state}"

    defs="bin/.status-summary-defs"
    sed '/^case "$1" in/,$d' bin/spark-http-proxy >"${defs}"

    run_status() {
        HOME="${home}" DEFS="${defs}" bash -c '
            . "${DEFS}"
            TAILSCALE_ENABLED=true
            show_tailscale_summary' 2>/dev/null
    }

    # Forwarding: the machines and what they serve.
    printf 'ok\n9 4\npaolo-cto-arch-p620\tnest.spark.loc,react.spark.loc\n' >"${state}/tailscale-peers-summary"
    : >"${state}/tailscale-peers.txt"
    local out
    out="$(run_status)"

    total=$((total + 1))
    if echo "${out}" | grep -q "paolo-cto-arch-p620" && echo "${out}" | grep -q "nest.spark.loc"; then
        success "status names the forwarding machine and its hostnames"
        passed=$((passed + 1))
    else
        error "status did not name the forwarding machine: $(echo "${out}" | tr '\n' ' ')"
    fi

    # The advice sits beside the hostnames it applies to, and has to survive
    # being copied: a placeholder pastes as arguments or as a redirection.
    total=$((total + 1))
    if echo "${out}" | grep -q "generate-mkcert 'nest.spark.loc'"; then
        success "status shows a certificate command for a hostname it just listed"
        passed=$((passed + 1))
    else
        error "status gave no usable certificate command: $(echo "${out}" | tr '\n' ' ')"
    fi

    # Nothing forwarded, which is the usual state with one proxy on a tailnet.
    # Nine machines were in the report and only four were probed; the other
    # five were asleep and were never asked.
    printf 'ok\n9 4\n' >"${state}/tailscale-peers-summary"
    out="$(run_status)"

    total=$((total + 1))
    if echo "${out}" | grep -q "9"; then
        success "status says how many machines were seen when nothing is forwarded"
        passed=$((passed + 1))
    else
        error "status gave no evidence discovery ran: $(echo "${out}" | tr '\n' ' ')"
    fi

    # A machine that was never probed cannot be said to be running anything.
    total=$((total + 1))
    if echo "${out}" | grep -q "4 probed"; then
        success "status distinguishes the machines it probed from those it did not"
        passed=$((passed + 1))
    else
        error "status claimed something about every machine seen: $(echo "${out}" | tr '\n' ' ')"
    fi

    total=$((total + 1))
    if echo "${out}" | grep -q "✅"; then
        success "nothing forwarded is reported as working"
        passed=$((passed + 1))
    else
        error "nothing forwarded was not reported as working: $(echo "${out}" | tr '\n' ' ')"
    fi

    # On a tailnet with one proxy this is the permanent state, so certificate
    # advice here would print on every status forever.
    total=$((total + 1))
    if echo "${out}" | grep -q "generate-mkcert"; then
        error "status advised on certificates with nothing forwarded: $(echo "${out}" | tr '\n' ' ')"
    else
        success "nothing forwarded means no certificate advice"
        passed=$((passed + 1))
    fi

    # A cycle that could not run is not a working state.
    printf 'aborted\n9 4\npaolo-cto-arch-p620\tnest.spark.loc,react.spark.loc\n' >"${state}/tailscale-peers-summary"
    out="$(run_status)"

    total=$((total + 1))
    if echo "${out}" | grep -q "✅"; then
        error "an aborted cycle was reported with a success tick: $(echo "${out}" | tr '\n' ' ')"
    else
        success "an aborted cycle is not reported as working"
        passed=$((passed + 1))
    fi

    # And it goes to stdout, since status produces one report.
    # The message itself, not merely the lines under it, which always go to
    # stdout and would make an emptiness check pass either way.
    total=$((total + 1))
    if HOME="${home}" DEFS="${defs}" bash -c '. "${DEFS}"; TAILSCALE_ENABLED=true; show_tailscale_summary' 2>/dev/null |
        grep -q "could not read the local routing table"; then
        success "the aborted message itself goes to stdout"
        passed=$((passed + 1))
    else
        error "the aborted message went to stderr, so it splits from the rest of status"
    fi

    # The longest name on a real tailnet is longer than any width guessed in
    # advance, and the columns have to line up when the feature is working.
    printf 'ok\n2 2\nMac-Sparkfabrik-PaoloMainardi\tmacos.spark.loc\nshort\tapp.spark.loc\n' >"${state}/tailscale-peers-summary"
    out="$(run_status)"

    total=$((total + 1))
    local col_long col_short
    # Only the table rows: the example command names a hostname too.
    col_long="$(echo "${out}" | grep -v generate-mkcert | grep "macos.spark.loc" | grep -bo "macos.spark.loc" | cut -d: -f1)"
    col_short="$(echo "${out}" | grep -v generate-mkcert | grep "app.spark.loc" | grep -bo "app.spark.loc" | cut -d: -f1)"
    if [ -n "${col_long}" ] && [ "${col_long}" = "${col_short}" ]; then
        success "the hostname column lines up whatever the machine names are"
        passed=$((passed + 1))
    else
        error "the columns did not line up: hostnames start at ${col_long} and ${col_short}"
    fi

    # A summary in a shape this CLI does not know is not a summary. `read` is
    # happy to leave a variable empty, so an unrecognised line would otherwise
    # print counts silently shifted out of their columns.
    printf 'ok\n9 1 2\npaolo-cto-arch-p620\tnest.spark.loc\n' >"${state}/tailscale-peers-summary"
    out="$(run_status)"

    total=$((total + 1))
    if echo "${out}" | grep -q "no usable discovery record"; then
        success "a summary in an unrecognised shape is treated as no cycle"
        passed=$((passed + 1))
    else
        error "an unrecognised summary was rendered anyway: $(echo "${out}" | tr '\n' ' ')"
    fi

    # The token is part of the shape. Anything but the two known values would
    # otherwise fall through to the success path with a tick on it.
    printf 'garbage\n9 4\npaolo-cto-arch-p620\tnest.spark.loc\n' >"${state}/tailscale-peers-summary"
    out="$(run_status)"

    total=$((total + 1))
    if echo "${out}" | grep -q "no usable discovery record"; then
        success "an unknown state token is treated as no cycle"
        passed=$((passed + 1))
    else
        error "an unknown state token was rendered anyway: $(echo "${out}" | tr '\n' ' ')"
    fi

    # An empty or truncated record must not take the whole command down: read
    # returns non-zero at end of file, and the CLI runs under errexit.
    : >"${state}/tailscale-peers-summary"
    total=$((total + 1))
    if run_status | grep -q "no usable discovery record"; then
        success "an empty record is reported rather than ending the command"
        passed=$((passed + 1))
    else
        error "an empty record did not produce the warning: $(run_status | tr '\n' ' ')"
    fi

    printf 'ok\n' >"${state}/tailscale-peers-summary"
    total=$((total + 1))
    if run_status | grep -q "no usable discovery record"; then
        success "a record missing its counts is reported rather than ending the command"
        passed=$((passed + 1))
    else
        error "a truncated record did not produce the warning: $(run_status | tr '\n' ' ')"
    fi

    # A count with a leading zero is not a shape this service writes, and bash
    # reads it as octal, so 08 is both wrong and an arithmetic error.
    printf 'ok\n9 08\n' >"${state}/tailscale-peers-summary"
    total=$((total + 1))
    if run_status 2>&1 | grep -q "no usable discovery record"; then
        success "a count with a leading zero is treated as an unrecognised record"
        passed=$((passed + 1))
    else
        error "a leading-zero count was rendered: $(run_status 2>&1 | tr '\n' ' ')"
    fi

    # A row is a name, a tab and its hostnames. Anything else is not a row this
    # service writes, and counting it invents a machine or a hostname.
    printf 'ok\n9 4\ndesktop\n' >"${state}/tailscale-peers-summary"
    total=$((total + 1))
    if run_status | grep -q "no usable discovery record"; then
        success "a machine row without hostnames is an unrecognised record"
        passed=$((passed + 1))
    else
        error "a row with no hostnames was counted: $(run_status | tr '\n' ' ')"
    fi

    printf 'ok\n9 4\ndesktop\tapp.loc\tsomething\n' >"${state}/tailscale-peers-summary"
    total=$((total + 1))
    if run_status | grep -q "no usable discovery record"; then
        success "a machine row with an extra column is an unrecognised record"
        passed=$((passed + 1))
    else
        error "a row with an extra column was rendered: $(run_status | tr '\n' ' ')"
    fi

    rm -rf "${home}" "${defs}"
    log "Status summary tests: ${passed}/${total} passed"
    [ "${passed}" -eq "${total}" ]
}

test_refresh_peers() {
    local passed=0 total=0
    local home stub_dir state
    home="$(mktemp -d)"
    stub_dir="${home}/stubs"
    state="${home}/.local/spark/http-proxy/state"
    mkdir -p "${stub_dir}" "${state}" "${home}/.local/spark/http-proxy"

    local defs="bin/.refresh-peers-defs"
    sed '/^case "$1" in/,$d' bin/spark-http-proxy >"${defs}"

    # A docker whose compose ps lists the services as running, and whose kill
    # does nothing, so the state file never advances.
    cat >"${stub_dir}/docker" <<'STUB'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "ps" ]; then echo "http-proxy"; echo "tailscale_peers"; exit 0; fi
done
exit 0
STUB
    chmod +x "${stub_dir}/docker"

    # A client that cannot produce a document, so the document path is decided
    # by the stub rather than by whether the host happens to run Tailscale.
    printf '#!/bin/sh\nexit 1\n' >"${stub_dir}/tailscale"
    chmod +x "${stub_dir}/tailscale"

    # Written the way the service writes it, indented, so the assertions are
    # about the format it actually produces.
    cat >"${state}/tailscale-peers.json" <<'STATE'
{
  "updatedAt": "2026-01-01T00:00:00Z",
  "source": "socket",
  "peers": []
}
STATE
    printf 'Tailnet peers, from the cycle at 2026-01-01T00:00:00Z\nSTALE-REPORT-MARKER\n' >"${state}/tailscale-peers.txt"
    printf '2026-01-01T00:00:00Z' >"${state}/tailscale-peers-completed-at"

    # The source is forced so the document path is exercised deliberately
    # rather than incidentally by whichever machine runs the suite.
    run_refresh() {
        HOME="${home}" \
            PATH="${stub_dir}:/usr/bin:/bin" \
            HTTP_PROXY_TAILSCALE_REFRESH_TIMEOUT="${1:-3}" \
            HTTP_PROXY_TAILSCALE_SOURCE="${3:-socket}" \
            PROBE_STATE="${PROBE_STATE:-}" \
            TAILSCALE_ENABLED_OVERRIDE="${2:-true}" \
            DEFS="${defs}" \
            bash -c '
                . "${DEFS}"
                TAILSCALE_ENABLED="${TAILSCALE_ENABLED_OVERRIDE}"
                tailscale_refresh_peers' 2>&1
        echo "exit=$?"
    }

    local out
    out="$(run_refresh 3 true)"

    total=$((total + 1))
    if echo "${out}" | grep -qi "did not complete\|timed out"; then
        success "a cycle that never completes is reported as a timeout"
        passed=$((passed + 1))
    else
        error "a cycle that never completed was not reported as a timeout"
    fi

    total=$((total + 1))
    if echo "${out}" | grep -q "STALE-REPORT-MARKER"; then
        error "the previous cycle's report was printed as though it were fresh"
    else
        success "the previous cycle's report is not printed after a timeout"
        passed=$((passed + 1))
    fi

    total=$((total + 1))
    if echo "${out}" | grep -q "exit=0"; then
        error "a timed-out refresh exited zero"
    else
        success "a timed-out refresh exits non-zero"
        passed=$((passed + 1))
    fi

    # Peer routing switched off is a choice, not a failure.
    out="$(run_refresh 3 false)"
    total=$((total + 1))
    if echo "${out}" | grep -qi "disabled" && echo "${out}" | grep -q "start-with-tailscale" &&
        ! echo "${out}" | grep -q "exit=0"; then
        success "peer routing disabled is reported, with how to enable it, and does not claim success"
        passed=$((passed + 1))
    else
        error "peer routing disabled was not reported as a failure with how to enable it"
    fi

    # A service that is not running is a fault: the cycle the user asked for
    # did not happen.
    cat >"${stub_dir}/docker" <<'STUB'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "ps" ]; then echo "http-proxy"; exit 0; fi
done
exit 0
STUB
    chmod +x "${stub_dir}/docker"

    out="$(run_refresh 3 true)"
    total=$((total + 1))
    if echo "${out}" | grep -q "not running" && ! echo "${out}" | grep -q "exit=0"; then
        success "a service that is not running is reported as a failure"
        passed=$((passed + 1))
    else
        error "a service that is not running claimed success"
    fi

    # Back to a running service for the cases below.
    cat >"${stub_dir}/docker" <<'STUB'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "ps" ]; then echo "http-proxy"; echo "tailscale_peers"; exit 0; fi
done
exit 0
STUB
    chmod +x "${stub_dir}/docker"

    # With no Tailscale client the document cannot be written, and a cycle run
    # against the stale one would answer the wrong question.
    out="$(run_refresh 3 true file)"
    total=$((total + 1))
    if echo "${out}" | grep -q "Not forcing a cycle" && ! echo "${out}" | grep -q "exit=0"; then
        success "a document that cannot be refreshed stops the cycle"
        passed=$((passed + 1))
    else
        error "a cycle was forced against a document that could not be refreshed"
    fi

    reset_state() {
        cat >"${state}/tailscale-peers.json" <<'STATE'
{
  "updatedAt": "2026-01-01T00:00:00Z",
  "source": "socket",
  "peers": []
}
STATE
        printf 'Tailnet peers, from the cycle at 2026-01-01T00:00:00Z\nSTALE-REPORT-MARKER\n' >"${state}/tailscale-peers.txt"
        printf '2026-01-01T00:00:00Z' >"${state}/tailscale-peers-completed-at"
    }

    # The barrier is written last, so a cycle still being written shows up as
    # the state file and the report moving while the barrier has not.
    cat >"${stub_dir}/docker" <<'STUB'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "ps" ]; then echo "http-proxy"; echo "tailscale_peers"; exit 0; fi
  if [ "$a" = "kill" ]; then
    printf '{\n  "updatedAt": "2026-01-01T00:00:09.123456789Z",\n  "source": "socket",\n  "peers": []\n}\n' >"${PROBE_STATE}/tailscale-peers.json"
    exit 0
  fi
done
exit 0
STUB
    chmod +x "${stub_dir}/docker"

    reset_state
    out="$(PROBE_STATE="${state}" run_refresh 5 true)"

    total=$((total + 1))
    if echo "${out}" | grep -q "STALE-REPORT-MARKER"; then
        error "a cycle still being written was reported as complete"
    else
        success "a cycle is not reported complete until the barrier moves"
        passed=$((passed + 1))
    fi

    # And the wait must still end when the report does catch up, or the fix
    # would turn every refresh into a timeout.
    cat >"${stub_dir}/docker" <<'STUB'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "ps" ]; then echo "http-proxy"; echo "tailscale_peers"; exit 0; fi
  if [ "$a" = "kill" ]; then
    printf '{\n  "updatedAt": "2026-01-01T00:00:09.123456789Z",\n  "source": "socket",\n  "peers": []\n}\n' >"${PROBE_STATE}/tailscale-peers.json"
    printf 'Tailnet peers, from the cycle at 2026-01-01T00:00:09Z\nFRESH-REPORT-MARKER\n' >"${PROBE_STATE}/tailscale-peers.txt"
    printf '2026-01-01T00:00:09.123456789Z' >"${PROBE_STATE}/tailscale-peers-completed-at"
    exit 0
  fi
done
exit 0
STUB
    chmod +x "${stub_dir}/docker"

    reset_state
    out="$(PROBE_STATE="${state}" run_refresh 5 true)"

    total=$((total + 1))
    if echo "${out}" | grep -q "FRESH-REPORT-MARKER" && echo "${out}" | grep -q "exit=0"; then
        success "a completed cycle prints its report and succeeds"
        passed=$((passed + 1))
    else
        error "a completed cycle was not reported: $(echo "${out}" | tr '\n' ' ' | head -c 120)"
    fi

    rm -rf "${home}" "${defs}"
    log "Refresh peers tests: ${passed}/${total} passed"
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

    # The certificate directory is a bind mount. Left to compose, docker creates
    # it owned by root and nothing the user runs can write a certificate into it,
    # which is the state a machine is in only when the stack came up before the
    # CLI ever did. The CLI creates it on load, so create it here too.
    mkdir -p "${LOCAL_HOME:-${HOME}}/.local/spark/http-proxy/certs"

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

    log "Testing prompts without a terminal..."
    total=$((total + 1))
    local prompts_passed=0
    test_prompts_without_a_terminal && prompts_passed=1
    [ "$prompts_passed" -eq 1 ] && passed=$((passed + 1))

    log "Testing that certificates apply without a restart..."
    total=$((total + 1))
    local cert_apply_passed=0
    test_certificates_apply_without_a_restart && cert_apply_passed=1
    [ "$cert_apply_passed" -eq 1 ] && passed=$((passed + 1))

    log "Testing that suggested commands can be pasted..."
    total=$((total + 1))
    local pasteable_passed=0
    test_suggested_commands_are_pasteable && pasteable_passed=1
    [ "$pasteable_passed" -eq 1 ] && passed=$((passed + 1))

    log "Testing what status reports about peer routing..."
    total=$((total + 1))
    local status_summary_passed=0
    test_status_summary && status_summary_passed=1
    [ "$status_summary_passed" -eq 1 ] && passed=$((passed + 1))

    log "Testing tailscale-refresh-peers..."
    total=$((total + 1))
    local refresh_passed=0
    test_refresh_peers && refresh_passed=1
    [ "$refresh_passed" -eq 1 ] && passed=$((passed + 1))

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
