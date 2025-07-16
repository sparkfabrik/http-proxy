#!/bin/sh

# Traefik entrypoint script that auto-generates TLS configuration from user certificates

CERTS_DIR="/traefik/certs"
DYNAMIC_DIR="/traefik/dynamic"
TLS_CONFIG_FILE="${DYNAMIC_DIR}/auto-tls.yml"

generate_tls_config() {
    echo "Scanning for certificates in ${CERTS_DIR}..."

    # Check if certificates directory exists and has files
    if [ ! -d "${CERTS_DIR}" ]; then
        echo "No certificates directory found at ${CERTS_DIR}"
        return
    fi

    # Look for certificate files (both .pem and .crt extensions)
    cert_files=$(find "${CERTS_DIR}" -name "*.pem" -o -name "*.crt" | grep -v "\-key")

    if [ -z "$cert_files" ]; then
        echo "No certificate files found in ${CERTS_DIR}"
        return
    fi

    echo "Found certificates, generating TLS configuration..."

    # Start TLS configuration
    cat > "${TLS_CONFIG_FILE}" << 'EOF'
# Auto-generated TLS configuration from user certificates
tls:
  certificates:
EOF

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
                cat >> "${TLS_CONFIG_FILE}" << EOF
    - certFile: ${cert_file}
      keyFile: ${key_file}
EOF
            else
                echo "  - Adding certificate: $(basename "$cert_file") (auto-detect domains)"
                cat >> "${TLS_CONFIG_FILE}" << EOF
    - certFile: ${cert_file}
      keyFile: ${key_file}
EOF
            fi
        else
            echo "  - Warning: No key file found for certificate $(basename "$cert_file")"
        fi
    done

    echo "TLS configuration written to ${TLS_CONFIG_FILE}"
}

# Generate TLS configuration from user certificates
generate_tls_config

# Start Traefik with the original arguments
echo "Starting Traefik..."
exec traefik "$@"
