#!/usr/bin/env bash

# first argument is the domain name.
DOMAIN_NAME="$1"
if [ -z "$DOMAIN_NAME" ]; then
  echo "Usage: $0 <domain-name>"
  exit 1
fi
# Ensure the directory exists
CERT_DIR="$HOME/.config/spark/http-proxy/certs"
if [ ! -d "$CERT_DIR" ]; then
  echo "Creating directory for certificates: $CERT_DIR"
  mkdir -p "$CERT_DIR"
fi

mkcert -cert-file "$CERT_DIR/$DOMAIN_NAME.pem" \
       -key-file "$CERT_DIR/$DOMAIN_NAME-key.pem" \
       $DOMAIN_NAME
