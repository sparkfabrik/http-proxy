#!/usr/bin/env bash
# Implements `spark-http-proxy certs`, reading the certificates in CERT_DIR.

CERTS_USAGE="Usage: $(basename "${0}") certs <list|describe|delete|generate> [domain...]

  list                 Installed certificates
  describe <domain>    Where a certificate's files are
  generate <domain>    Generate a certificate, wildcards supported
  delete <domain>...   Remove one or more certificates"

certs_usage() {
  echo "${CERTS_USAGE}"
}

# Reject a certificate argument that names a path rather than a hostname.
# A certificate covers a hostname, and a container mounted under a path with
# VIRTUAL_PATH is served by the certificate of the host it sits on. Without this
# check the path would be folded into the filename and the request would half
# succeed, leaving a certificate nothing can use.
reject_path_in_domain() {
  local domain="$1"

  if [[ "${domain}" == */* ]]; then
    log_error "'${domain}' contains a path. A certificate covers a hostname."
    log_info "Use the hostname on its own: ${domain%%/*}"
    log_info "A container mounted with VIRTUAL_PATH is served by its host's certificate."
    return 1
  fi

  return 0
}

# Rejects a domain whose stored filename would collide with another domain's.
# The mapping is lossy in two ways: a name ending in -key produces the filename
# used for the private key of the name without it, so generating "api-key" would
# overwrite the key of "api" and deleting it would remove it; and a literal
# _wildcard_ is the encoding of *, so it aliases an existing wildcard.
reject_colliding_domain() {
  local domain="$1" safe
  safe="$(cert_safe_filename "${domain}")"

  if [[ "${safe}" == *-key ]]; then
    log_error "'${domain}' cannot be stored: its filename is the private key of '${domain%-key}'"
    log_info "A certificate for it would overwrite that key. Rename the host."
    return 1
  fi

  if [[ "${domain}" == *_wildcard_* ]]; then
    log_error "'${domain}' cannot be stored: _wildcard_ is how * is encoded in filenames"
    log_info "For a wildcard use the * itself, quoted: '*.${domain#*_wildcard_.}'"
    return 1
  fi

  return 0
}

# Derive the safe filename used to store a domain's certificate files
cert_safe_filename() {
  local domain="$1"
  local safe_filename="${domain}"
  # Replace * with _wildcard_ for filename safety
  safe_filename="${safe_filename//\*/_wildcard_}"
  # Replace other problematic characters
  safe_filename="${safe_filename//\//_}"
  echo "${safe_filename}"
}

# Emits "domain<tab>file" per certificate, key files excluded, ordered by the
# domain as displayed. Sorting on the filename would misplace every wildcard,
# which is stored as _wildcard_ but shown as *.
certs_files() {
  local cert_file
  for cert_file in "${CERT_DIR}"/*.pem; do
    [[ -e "${cert_file}" ]] || return 0
    [[ "${cert_file}" == *-key.pem ]] && continue
    printf '%s\t%s\n' "$(certs_domain_of "${cert_file}")" "${cert_file}"
  done | LC_ALL=C sort
}

# The domain a certificate file is stored under, undoing the filename mapping.
certs_domain_of() {
  local safe_filename
  safe_filename="$(basename "$1" .pem)"
  printf '%s' "${safe_filename//_wildcard_/\*}"
}








certs_list() {
  local -a domains=() missing=()
  local domain cert_file total=0 noun

  while IFS=$'\t' read -r domain cert_file; do
    [[ -z "${cert_file}" ]] && continue
    total=$((total + 1))
    domains+=("${domain}")
    [[ "${#domain}" -gt "${wide}" ]] && wide="${#domain}"
    [[ -f "${cert_file%.pem}-key.pem" ]] || missing+=("${domain}")
  done < <(certs_files)

  if [[ "${total}" -eq 0 ]]; then
    log_warning "No certificates found"
    log_info "Generate one with: $(basename "${0}") certs generate '*.spark.loc'"
    return 0
  fi

  printf '%s\n' "DOMAIN" "${domains[@]}"

  echo ""
  noun="certificates"
  [[ "${total}" -eq 1 ]] && noun="certificate"
  log_info "${total} ${noun}"

  for domain in "${missing[@]}"; do
    log_warning "${domain} has no private key, so it cannot be served"
  done

  return 0
}

certs_describe() {
  local wanted="$1" cert_file key_file safe_filename

  if [[ -z "${wanted}" ]]; then
    log_error "Which domain? Usage: $(basename "${0}") certs describe <domain>"
    return 1
  fi

  safe_filename="$(cert_safe_filename "${wanted}")"
  cert_file="${CERT_DIR}/${safe_filename}.pem"
  key_file="${CERT_DIR}/${safe_filename}-key.pem"

  if [[ ! -f "${cert_file}" ]]; then
    log_error "No certificate for ${wanted}"
    log_info "See what is installed with: $(basename "${0}") certs list"
    return 1
  fi

  echo "${wanted}"
  echo "  certificate    $(abbreviate_home "${cert_file}")"
  if [[ -f "${key_file}" ]]; then
    echo "  private key    $(abbreviate_home "${key_file}")"
  else
    echo "  private key    missing, so this certificate cannot be served"
  fi

  return 0
}

certs_generate() {
  local domain="$1"

  if [ -z "${domain}" ] && [ -t 0 ]; then
    read -rp "Enter domain name: " domain
  fi

  if [ -z "${domain}" ]; then
    log_error "Domain name required"
    return 1
  fi

  reject_path_in_domain "${domain}" || return 1
  reject_colliding_domain "${domain}" || return 1
  install_mkcert || return 1

  local safe_filename
  safe_filename="$(cert_safe_filename "${domain}")"

  log_info "Generating certificates for: ${domain}"
  log_info "Certificate files will be named: ${safe_filename}.pem and ${safe_filename}-key.pem"

  # The domain goes after --, or mkcert reads a name beginning with - as a flag,
  # prints its usage, writes nothing and still exits 0. The files are checked for
  # afterwards for the same reason.
  if ! mkcert -cert-file "${CERT_DIR}/${safe_filename}.pem" \
    -key-file "${CERT_DIR}/${safe_filename}-key.pem" \
    -- "${domain}" ||
    [[ ! -s "${CERT_DIR}/${safe_filename}.pem" || ! -s "${CERT_DIR}/${safe_filename}-key.pem" ]]; then
    log_error "mkcert could not generate a certificate for ${domain}"
    return 1
  fi

  log_success "Certificates generated successfully:"
  log_info "  Certificate: ${CERT_DIR}/${safe_filename}.pem"
  log_info "  Private key: ${CERT_DIR}/${safe_filename}-key.pem"

  apply_certificates
}

# Separate from certs_delete so it can be exercised: everything above it needs a
# terminal to confirm, which a test does not have.
certs_remove_files() {
  if ! rm -f "$@"; then
    log_error "Some files could not be removed, so nothing is being applied"
    log_info "Check what is left with: $(basename "${0}") certs list"
    return 1
  fi
  return 0
}

certs_delete() {
  if [ "$#" -eq 0 ]; then
    local -a prompted=()
    if [ -t 0 ]; then
      read -rp "Enter domain name(s) to remove: " -a prompted
      set -- "${prompted[@]}"
    fi
  fi

  if [ "$#" -eq 0 ]; then
    log_error "Domain name required"
    return 1
  fi

  local -a files=()
  local -a matched=()
  local -a missing=()
  local domain safe_filename cert_file key_file
  for domain in "$@"; do
    reject_path_in_domain "${domain}" || return 1
    reject_colliding_domain "${domain}" || return 1
    safe_filename="$(cert_safe_filename "${domain}")"
    cert_file="${CERT_DIR}/${safe_filename}.pem"
    key_file="${CERT_DIR}/${safe_filename}-key.pem"

    if [[ ! -f "${cert_file}" && ! -f "${key_file}" ]]; then
      missing+=("${domain}")
      continue
    fi

    log_warning "Will remove certificate for: ${domain}"
    [[ -f "${cert_file}" ]] && { log_info "  Certificate: ${cert_file}"; files+=("${cert_file}"); }
    [[ -f "${key_file}" ]] && { log_info "  Private key: ${key_file}"; files+=("${key_file}"); }
    matched+=("${domain}")
  done

  for domain in "${missing[@]}"; do
    log_error "No certificate found for domain: ${domain}"
  done

  if [[ "${#matched[@]}" -eq 0 ]]; then
    log_info "List installed certificates with: $(basename "${0}") certs list"
    return 1
  fi

  if [ ! -t 0 ]; then
    log_error "Cannot confirm removal without a terminal, so nothing was removed"
    log_info "Re-run this interactively to confirm"
    return 1
  fi

  read -p "Remove ${#matched[@]} certificate(s)? (y/N): " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    log_info "Removal cancelled"
    return 0
  fi

  certs_remove_files "${files[@]}" || return 1
  log_success "Removed ${#matched[@]} certificate(s): ${matched[*]}"

  apply_certificates
}

certs_command() {
  local subcommand="${1:-list}"

  case "${subcommand}" in
    -h | --help | help)
      certs_usage
      return 0
      ;;
    list)
      certs_list
      ;;
    describe)
      certs_describe "${2:-}"
      ;;
    generate)
      certs_generate "${2:-}"
      ;;
    delete)
      certs_delete "${@:2}"
      ;;
    *)
      log_error "Unknown certs subcommand: ${subcommand}"
      certs_usage
      return 1
      ;;
  esac
}
