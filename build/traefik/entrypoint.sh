#!/bin/sh

# Traefik entrypoint script that auto-generates TLS configuration from user certificates

CERTS_DIR="/traefik/certs"
DYNAMIC_DIR="/traefik/dynamic"
TLS_CONFIG_FILE="${DYNAMIC_DIR}/auto-tls.yml"
# Built here and renamed into place, so a write that fails partway leaves the
# previous configuration serving rather than a truncated file serving nothing.
# The .tmp suffix is what dinghy-layer and tailscale-peers already use in this
# directory, and the provider ignores it.
TLS_CONFIG_TMP="${TLS_CONFIG_FILE}.tmp"

# Outside the tailscale-peer-*.yaml glob cleared below.
DECLARATION_FILE="spark-http-proxy-declaration.yaml"

# Leaves no certificate referenced. Removing the last certificate lands here, and
# without this the previous file survives, pointing at files that are gone.
write_empty_tls_config() {
    if ! cat > "${TLS_CONFIG_TMP}" << 'EOF'
# Auto-generated TLS configuration from user certificates
tls:
  certificates: []
EOF
    then
        echo "Failed to write ${TLS_CONFIG_FILE}" >&2
        rm -f "${TLS_CONFIG_TMP}"
        return 1
    fi

    if ! mv "${TLS_CONFIG_TMP}" "${TLS_CONFIG_FILE}"; then
        echo "Failed to replace ${TLS_CONFIG_FILE}" >&2
        rm -f "${TLS_CONFIG_TMP}"
        return 1
    fi

    echo "No certificates to reference, wrote an empty list to ${TLS_CONFIG_FILE}"
}

generate_tls_config() {
    echo "Scanning for certificates in ${CERTS_DIR}..."

    # Check if certificates directory exists and has files
    if [ ! -d "${CERTS_DIR}" ]; then
        echo "No certificates directory found at ${CERTS_DIR}"
        write_empty_tls_config
        return
    fi

    # Look for certificate files (both .pem and .crt extensions)
    cert_files=$(find "${CERTS_DIR}" -name "*.pem" -o -name "*.crt" | grep -v "\-key")

    if [ -z "$cert_files" ]; then
        echo "No certificate files found in ${CERTS_DIR}"
        write_empty_tls_config
        return
    fi

    echo "Found certificates, generating TLS configuration..."

    # Start TLS configuration. Every write is checked: the file is what makes
    # Traefik serve these certificates, and the CLI reads this function's status
    # to decide whether to tell the user they were applied.
    if ! cat > "${TLS_CONFIG_TMP}" << 'EOF'
# Auto-generated TLS configuration from user certificates
tls:
  certificates:
EOF
    then
        echo "Failed to write ${TLS_CONFIG_FILE}" >&2
        rm -f "${TLS_CONFIG_TMP}"
        return 1
    fi

    # Process each certificate file
    for cert_file in $cert_files; do
        # Get the basename without extension
        cert_base=$(basename "$cert_file" .pem)
        cert_base=$(basename "$cert_base" .crt)

        # Look for corresponding key file
        key_file=""
        for ext in pem crt key; do
            possible_key="${CERTS_DIR}/${cert_base}-key.${ext}"
            if [ -f "$possible_key" ]; then
                key_file="$possible_key"
                break
            fi

            possible_key="${CERTS_DIR}/${cert_base}.key"
            if [ -f "$possible_key" ]; then
                key_file="$possible_key"
                break
            fi
        done

        if [ -n "$key_file" ]; then
            # Extract domains from certificate
            domains=$(openssl x509 -in "$cert_file" -noout -text 2>/dev/null | \
                     grep -A1 "Subject Alternative Name" | \
                     grep "DNS:" | \
                     sed 's/.*DNS://g' | \
                     sed 's/,.*DNS:/ /g' | \
                     sed 's/,.*//g' | \
                     tr -d ' ')

            if [ -n "$domains" ]; then
                echo "  - Adding certificate: $(basename "$cert_file") for domains: $domains"
            else
                echo "  - Adding certificate: $(basename "$cert_file") (auto-detect domains)"
            fi

            # Both branches wrote the same two lines, so they share one write
            # and one check rather than carrying a copy of each.
            if ! cat >> "${TLS_CONFIG_TMP}" << EOF
    - certFile: ${cert_file}
      keyFile: ${key_file}
EOF
            then
                echo "Failed to add $(basename "$cert_file") to ${TLS_CONFIG_FILE}" >&2
                rm -f "${TLS_CONFIG_TMP}"
                return 1
            fi
        else
            echo "  - Warning: No key file found for certificate $(basename "$cert_file")"
        fi
    done

    if ! mv "${TLS_CONFIG_TMP}" "${TLS_CONFIG_FILE}"; then
        echo "Failed to replace ${TLS_CONFIG_FILE}" >&2
        rm -f "${TLS_CONFIG_TMP}"
        return 1
    fi

    echo "TLS configuration written to ${TLS_CONFIG_FILE}"
}

# Writes the middleware that identifies this proxy to other machines.
write_proxy_declaration() {
    cat > "${DYNAMIC_DIR}/${DECLARATION_FILE}" << 'EOF'
# Generated at startup, do not edit.
http:
  middlewares:
    spark-http-proxy:
      headers:
        customResponseHeaders:
          X-Spark-Http-Proxy: "1"
EOF
    echo "Wrote proxy declaration to ${DYNAMIC_DIR}/${DECLARATION_FILE}"
}

# Removes the peer routes a previous run left behind when peer routing is off.
remove_peer_config() {
    if [ "${HTTP_PROXY_TAILSCALE_ENABLED}" = "true" ]; then
        return
    fi

    # The glob does not match the declaration file.
    for peer_config in "${DYNAMIC_DIR}"/tailscale-peer-*.yaml; do
        [ -e "${peer_config}" ] || continue
        echo "Peer routing is disabled, removing $(basename "${peer_config}")"
        rm -f "${peer_config}"
    done
}

# Re-run the certificate scan on its own, for a proxy that is already up. The
# CLI uses this to apply a certificate without restarting and dropping every
# connection on the machine.
if [ "${1:-}" = "--tls-only" ]; then
    # The exit status is what the CLI reads to decide whether to tell the user
    # the certificates were applied, so it must be the scan's, not a constant.
    generate_tls_config
    exit $?
fi

# Declare this proxy to other machines
write_proxy_declaration

# Generate TLS configuration from user certificates
generate_tls_config

# Remove peer routes left behind when peer routing is disabled
remove_peer_config

# Start Traefik with the original arguments
echo "Starting Traefik..."
exec traefik "$@"
