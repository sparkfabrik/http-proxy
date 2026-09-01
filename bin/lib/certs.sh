#!/usr/bin/env bash
# The `spark-http-proxy certs` topic: a wrapper around mkcert that names,
# lists, describes and removes the certificates Traefik serves.

install_mkcert() {
  if command -v mkcert >/dev/null 2>&1; then
    log_info "Running mkcert -install to ensure root CA is installed..."
    mkcert -install
    return 0
  fi

  # Try different package managers based on availability
  if command -v brew >/dev/null 2>&1; then
    log_info "Installing mkcert with Homebrew..."
    if brew install mkcert nss && mkcert -install; then
      log_success "mkcert installed successfully"
      return 0
    else
      log_error "Failed to install mkcert with Homebrew"
      return 1
    fi
  elif command -v pacman >/dev/null 2>&1; then
    log_info "Installing mkcert with pacman..."
    if sudo pacman -S --noconfirm nss mkcert && mkcert -install; then
      log_success "mkcert installed successfully"
      return 0
    else
      log_error "Failed to install mkcert with pacman"
      return 1
    fi
  else
    log_error "mkcert not found and no supported package manager available"
    log_info "Please install mkcert manually:"
    log_info "  - Arch Linux: sudo pacman -S nss mkcert"
    log_info "  - macOS: brew install mkcert nss"
    return 1
  fi
}

# Applies a certificate change to the proxy. Traefik serves a certificate only
# when the dynamic configuration references it, and that reference is written by
# the container's certificate scan, so re-running the scan is what applies a
# change. A restart applies it too, by running the same scan on the way up, but
# it drops every connection on the machine.
apply_certificates() {
  if ! is_running http-proxy; then
    log_info "The proxy is not running. The change applies when it starts."
    return 0
  fi

  # An image predating the scan guard would pass --tls-only through to Traefik
  # and run the rest of its entrypoint on the way, withdrawing peer routes.
  # The guard itself, not the flag anywhere in the file. A mention in a comment
  # would otherwise read as support and send the scan to an entrypoint that runs
  # its whole body, withdrawing peer routes on the way. A pattern that stops
  # matching costs a restart, which still applies the certificate, so the
  # tighter test fails in the safe direction.
  if docker exec http-proxy grep -qE '^[[:space:]]*if .*--tls-only' /entrypoint.sh 2>/dev/null; then
    log_info "Applying certificates..."
    # Only the proxy knows why a scan failed, and without its message the
    # warning says a thing did not work and nothing about what to look at.
    local scan_error
    if ! scan_error="$(docker exec http-proxy /entrypoint.sh --tls-only 2>&1 >/dev/null)"; then
      log_warning "The proxy did not apply the certificates"
      if [ -n "${scan_error}" ]; then
        log_info "The proxy said: ${scan_error}"
      fi
      log_info "Apply them with: $(basename "${0}") restart"
      # The certificate exists but the proxy is not serving it, so the command
      # did not do what it says it does. Reporting success here would have a
      # script move on to a hostname that is not answering yet.
      return 1
    fi
    return 0
  fi

  # An older proxy cannot run the scan on its own, and nothing references a new
  # certificate, so nothing would ever apply it. The restart runs the scan.
  log_info "Restarting Traefik to apply the change..."
  dc_cmd restart traefik
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

# An unescaped ~ on the replacement side expands back to the home directory.
cert_abbreviate_home() {
  local path="$1"
  if [[ "${path}" == "${HOME}" || "${path}" == "${HOME}/"* ]]; then
    printf '~%s' "${path#"${HOME}"}"
    return 0
  fi
  printf '%s' "${path}"
}

# The domain a certificate file was generated for, read back from its name.
cert_domain_from_file() {
  local safe_filename
  safe_filename="$(basename "${1}" .pem)"
  echo "${safe_filename//_wildcard_/\*}"
}

certs_generate() {
  local domain="$1"

  if [ -z "${domain}" ] && [ -t 0 ]; then
    read -rp "Enter domain name: " domain
  fi

  if [ -z "${domain}" ]; then
    log_error "Domain name required"
    exit 1
  fi

  if ! reject_path_in_domain "${domain}"; then
    exit 1
  fi

  if ! install_mkcert; then
    exit 1
  fi

  # Create a safe filename for wildcard domains
  local safe_filename
  safe_filename="$(cert_safe_filename "${domain}")"

  log_info "Generating certificates for: ${domain}"
  log_info "Certificate files will be named: ${safe_filename}.pem and ${safe_filename}-key.pem"

  mkcert -cert-file "${CERT_DIR}/${safe_filename}.pem" \
    -key-file "${CERT_DIR}/${safe_filename}-key.pem" \
    "${domain}"

  log_success "Certificates generated successfully:"
  log_info "  Certificate: ${CERT_DIR}/${safe_filename}.pem"
  log_info "  Private key: ${CERT_DIR}/${safe_filename}-key.pem"

  apply_certificates
}

# Certificate files, keys excluded, in directory order. Empty when there are none.
cert_files() {
  local cert_file
  for cert_file in "${CERT_DIR}"/*.pem; do
    [[ -e "${cert_file}" ]] || break
    [[ "${cert_file}" == *-key.pem ]] && continue
    echo "${cert_file}"
  done
}

certs_list() {
  local cert_file domain cert_name key_name rows=() row fields
  local found=0 missing=0 w_domain=6 w_cert=11

  while IFS= read -r cert_file; do
    [[ -z "${cert_file}" ]] && continue
    domain="$(cert_domain_from_file "${cert_file}")"
    cert_name="$(basename "${cert_file}")"
    key_name="${cert_name%.pem}-key.pem"
    if [[ ! -f "${CERT_DIR}/${key_name}" ]]; then
      key_name="missing"
      missing=$((missing + 1))
    fi
    rows+=("${domain}"$'\t'"${cert_name}"$'\t'"${key_name}")
    [[ "${#domain}" -gt "${w_domain}" ]] && w_domain="${#domain}"
    [[ "${#cert_name}" -gt "${w_cert}" ]] && w_cert="${#cert_name}"
    found=$((found + 1))
  done < <(cert_files)

  if [[ "${found}" -eq 0 ]]; then
    log_warning "No certificates found. Generate one with: $(basename "${0}") certs generate '*.spark.loc'"
    return 0
  fi

  echo "Certificates in $(cert_abbreviate_home "${CERT_DIR}")"
  echo ""
  printf '%-*s  %-*s  %s\n' "${w_domain}" "DOMAIN" "${w_cert}" "CERTIFICATE" "KEY"
  for row in "${rows[@]}"; do
    IFS=$'\t' read -r -a fields <<<"${row}"
    printf '%-*s  %-*s  %s\n' "${w_domain}" "${fields[0]}" "${w_cert}" "${fields[1]}" "${fields[2]}"
  done
  echo ""

  local summary
  summary="$(count_noun "${found}" certificate)"
  [[ "${missing}" -gt 0 ]] && summary="${summary}, ${missing} without a private key"
  echo "${summary}. Remove one with: $(basename "${0}") certs delete '${domain}'"
}

# One check before describe prints anything: is openssl there, and, when a
# certificate is named, can it read that file, extensions included? The
# invocations used below exist in both OpenSSL and LibreSSL, so this fails on a
# missing openssl or an unreadable file, and describe stops rather than
# printing a record with holes in it.
require_openssl() {
  local cert_file="${1:-}" err

  if ! command -v openssl >/dev/null 2>&1; then
    log_error "openssl is not installed, and describe reads the certificate with it"
    log_info "sparkdock installs it as openssl@3; on Arch: pacman -S openssl. list, generate and delete work without it"
    return 1
  fi

  [[ -z "${cert_file}" ]] && return 0

  if ! err="$(openssl x509 -in "${cert_file}" -noout -text -startdate -enddate -issuer 2>&1 >/dev/null)" ||
    ! openssl x509 -in "${cert_file}" -noout -text 2>/dev/null | grep -q 'Subject Alternative Name'; then
    log_error "$(command -v openssl) ($(openssl version 2>/dev/null)) could not read $(cert_abbreviate_home "${cert_file}")${err:+: ${err%%$'\n'*}}"
    log_info "describe needs an openssl that prints x509 extensions; sparkdock provides one as openssl@3"
    return 1
  fi
}

# The DNS names a certificate covers, one per line.
cert_dns_names() {
  openssl x509 -in "$1" -noout -text 2>/dev/null |
    grep -A1 'Subject Alternative Name' | tail -n 1 | tr ',' '\n' |
    sed -n 's/^[[:space:]]*DNS://p'
}

# "Oct 13 16:33:10 2027 GMT", as openssl prints it, to 2027-10-13.
cert_iso_date() {
  local month day year m
  read -r month day _ year _ <<<"$1"
  case "${month}" in
  Jan) m=01 ;; Feb) m=02 ;; Mar) m=03 ;; Apr) m=04 ;; May) m=05 ;; Jun) m=06 ;;
  Jul) m=07 ;; Aug) m=08 ;; Sep) m=09 ;; Oct) m=10 ;; Nov) m=11 ;; Dec) m=12 ;;
  *) echo "$1"; return 0 ;;
  esac
  printf '%s-%s-%02d\n' "${year}" "${m}" "${day}"
}

# Whether a DNS name from a certificate covers a hostname. A wildcard matches
# exactly one label, which is the rule TLS clients apply.
cert_name_covers() {
  local name="$1" hostname="$2"
  [[ "${name}" == "${hostname}" ]] && return 0
  [[ "${name}" == \*.* && "${hostname}" == *.* ]] || return 1
  [[ "${hostname#*.}" == "${name#\*.}" ]]
}

# Prints one certificate's record. The caller has run require_openssl.
cert_describe_file() {
  local cert_file="$1" key_file="${1%.pem}-key.pem" names not_before not_after issuer state

  names="$(cert_dns_names "${cert_file}" | paste -sd ',' -)"
  not_before="$(openssl x509 -in "${cert_file}" -noout -startdate | sed 's/^notBefore=//')"
  not_after="$(openssl x509 -in "${cert_file}" -noout -enddate | sed 's/^notAfter=//')"
  issuer="$(openssl x509 -in "${cert_file}" -noout -issuer | sed -E 's/.*CN ?= ?//')"
  state="valid"
  if ! openssl x509 -in "${cert_file}" -noout -checkend 0 >/dev/null 2>&1; then
    state="expired"
  fi

  cert_domain_from_file "${cert_file}"
  echo "  certificate    $(cert_abbreviate_home "${cert_file}")"
  if [[ -f "${key_file}" ]]; then
    echo "  private key    $(cert_abbreviate_home "${key_file}")"
  else
    echo "  private key    missing, expected $(cert_abbreviate_home "${key_file}")"
  fi
  echo "  covers         ${names//,/, }"
  printf '  %-15s%s to %s\n' "${state}" "$(cert_iso_date "${not_before}")" "$(cert_iso_date "${not_after}")"
  echo "  issued by      ${issuer}"

  # The only field that needs the proxy, so it degrades instead of failing.
  if docker info >/dev/null 2>&1 && is_running http-proxy; then
    if docker exec http-proxy grep -qF "certFile: /traefik/certs/$(basename "${cert_file}")" /traefik/dynamic/auto-tls.yml 2>/dev/null; then
      echo "  served         yes, by the running proxy"
    else
      echo "  served         no, the proxy has not applied it. Apply with: $(basename "${0}") restart"
    fi
  else
    echo "  served         unknown, the proxy is not running"
  fi
}

certs_describe() {
  local wanted="$1" cert_file name names near_miss=""

  if [[ -z "${wanted}" ]]; then
    log_error "Which domain? For example: $(basename "${0}") certs describe '*.spark.loc'"
    return 1
  fi

  if ! reject_path_in_domain "${wanted}"; then
    return 1
  fi

  cert_file="${CERT_DIR}/$(cert_safe_filename "${wanted}").pem"
  if [[ -f "${cert_file}" ]]; then
    require_openssl "${cert_file}" || return 1
    cert_describe_file "${cert_file}"
    return $?
  fi

  # No certificate by that name. One may still cover it, and the answer comes
  # from the names inside the certificates, not from their filenames.
  require_openssl || return 1
  while IFS= read -r cert_file; do
    [[ -z "${cert_file}" ]] && continue
    names="$(cert_dns_names "${cert_file}")"
    if [[ -z "${names}" ]]; then
      # Said out loud, or a broken file would read as "nothing covers it".
      log_warning "Skipping $(cert_abbreviate_home "${cert_file}"): openssl found no DNS names in it"
      continue
    fi
    while IFS= read -r name; do
      if cert_name_covers "${name}" "${wanted}"; then
        log_info "No certificate is named ${wanted}. It is covered by ${name}:"
        echo ""
        cert_describe_file "${cert_file}"
        return $?
      fi
      # A wildcard higher up the tree that the hostname sits under but too
      # deep for: the confusion this command exists to clear up.
      if [[ "${name}" == \*.* && "${wanted}" == *".${name#\*.}" ]]; then
        near_miss="${name}"
      fi
    done <<<"${names}"
  done < <(cert_files)

  log_error "No certificate covers ${wanted}"
  if [[ -n "${near_miss}" ]]; then
    log_info "${near_miss} does not cover it: a wildcard matches one label only"
  fi
  if [[ "${wanted}" == *.* ]]; then
    log_info "Generate one with: $(basename "${0}") certs generate '*.${wanted#*.}'"
  fi
  log_info "List installed certificates with: $(basename "${0}") certs list"
  return 1
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
    exit 1
  fi

  local -a files=()
  local -a matched=()
  local -a missing=()
  local domain safe_filename cert_file key_file
  for domain in "$@"; do
    if ! reject_path_in_domain "${domain}"; then
      exit 1
    fi
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
    exit 1
  fi

  if [ ! -t 0 ]; then
    log_error "Cannot confirm removal without a terminal, so nothing was removed"
    log_info "Re-run this interactively to confirm"
    exit 1
  fi

  read -p "Remove ${#matched[@]} certificate(s)? (y/N): " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    log_info "Removal cancelled"
    return 0
  fi

  rm -f "${files[@]}"
  log_success "Removed ${#matched[@]} certificate(s): ${matched[*]}"

  apply_certificates
}

# One usage string, so the help and the error cannot drift apart.
certs_usage() {
  echo "Usage: $(basename "${0}") certs [list|describe <domain>|generate <domain>|delete <domain>...]"
  echo ""
  echo "  list                 Installed certificates and the files each one is stored in (the default)"
  echo "  describe <domain>    What one certificate covers, its dates, its issuer, and whether the proxy serves it"
  echo "  generate <domain>    Generate a certificate with mkcert (quote wildcards: '*.spark.loc')"
  echo "  delete <domain>...   Remove certificates, asking once before deleting"
  echo ""
  echo "Certificate directory: $(cert_abbreviate_home "${CERT_DIR}")"
}

certs_command() {
  local subcommand="${1:-list}"

  case "${subcommand}" in
  list) certs_list ;;
  describe) certs_describe "${2:-}" ;;
  generate) certs_generate "${2:-}" ;;
  delete) certs_delete "${@:2}" ;;
  help | -h | --help) certs_usage ;;
  *)
    log_error "Unknown option: ${subcommand}"
    certs_usage >&2
    return 1
    ;;
  esac
}

# The old top-level names still work, warn once, and stay out of the help and
# the completion, so a note or a script written against them survives the update.
certs_deprecated() {
  local old="$1" new
  case "${old}" in
  generate-mkcert) new="generate" ;;
  list-certs) new="list" ;;
  remove-cert) new="delete" ;;
  esac
  log_warning "${old} is deprecated, use: $(basename "${0}") certs ${new}"
  certs_command "${new}" "${@:2}"
}
