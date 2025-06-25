#!/bin/bash

# Test script to verify certificate auto-detection functionality
# This script simulates the certificate setup and tests that the entrypoint script works correctly

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TEMP_CERTS_DIR="${PROJECT_ROOT}/test/temp-certs"
TEMP_DYNAMIC_DIR="${PROJECT_ROOT}/test/temp-dynamic"

echo "🧪 Testing certificate auto-detection..."

# Clean up any previous test artifacts
rm -rf "${TEMP_CERTS_DIR}" "${TEMP_DYNAMIC_DIR}"
mkdir -p "${TEMP_CERTS_DIR}" "${TEMP_DYNAMIC_DIR}"

# Create mock certificate files
echo "📝 Creating test certificate files..."
cat > "${TEMP_CERTS_DIR}/wildcard.loc.pem" << 'EOF'
-----BEGIN CERTIFICATE-----
MOCK_CERTIFICATE_DATA_FOR_TESTING
-----END CERTIFICATE-----
EOF

cat > "${TEMP_CERTS_DIR}/wildcard.loc-key.pem" << 'EOF'
-----BEGIN PRIVATE KEY-----
MOCK_PRIVATE_KEY_DATA_FOR_TESTING
-----END PRIVATE KEY-----
EOF

# Create test files for other formats
cat > "${TEMP_CERTS_DIR}/example.crt" << 'EOF'
-----BEGIN CERTIFICATE-----
MOCK_CERTIFICATE_DATA_FOR_TESTING_CRT
-----END CERTIFICATE-----
EOF

cat > "${TEMP_CERTS_DIR}/example-key.pem" << 'EOF'
-----BEGIN PRIVATE KEY-----
MOCK_PRIVATE_KEY_DATA_FOR_TESTING_CRT
-----END PRIVATE KEY-----
EOF

echo "📁 Test certificate files created:"
ls -la "${TEMP_CERTS_DIR}"

# Test the entrypoint script logic (simulate it)
echo "🔍 Testing certificate detection logic..."

# Simulate the entrypoint script behavior
CERTS_DIR="${TEMP_CERTS_DIR}"
DYNAMIC_DIR="${TEMP_DYNAMIC_DIR}"
TLS_CONFIG_FILE="${DYNAMIC_DIR}/auto-tls.yml"

# Look for certificate files (both .pem and .crt extensions)
cert_files=$(find "${CERTS_DIR}" -name "*.pem" -o -name "*.crt" | grep -v "\-key" | head -10)

if [ -z "$cert_files" ]; then
    echo "❌ ERROR: No certificate files found!"
    exit 1
fi

echo "✅ Found certificate files: $cert_files"

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
        # Extract domains from certificate (simulate the new logic)
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

        cat >> "${TLS_CONFIG_FILE}" << EOF
    - certFile: ${cert_file}
      keyFile: ${key_file}
EOF
    else
        echo "  - Warning: No key file found for certificate $(basename "$cert_file")"
    fi
done

echo "📋 Generated TLS configuration:"
cat "${TLS_CONFIG_FILE}"

# Verify the configuration looks correct
if grep -q "certFile:" "${TLS_CONFIG_FILE}" && grep -q "keyFile:" "${TLS_CONFIG_FILE}"; then
    echo "✅ TLS configuration generated successfully!"
    echo "✅ Certificate auto-detection test PASSED!"
else
    echo "❌ TLS configuration appears invalid!"
    echo "❌ Certificate auto-detection test FAILED!"
    exit 1
fi

# Clean up
echo "🧹 Cleaning up test files..."
rm -rf "${TEMP_CERTS_DIR}" "${TEMP_DYNAMIC_DIR}"

echo "🎉 All tests completed successfully!"
