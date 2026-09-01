#!/usr/bin/env bash
# Implements `spark-http-proxy certs`, reading the certificates in CERT_DIR.

CERTS_USAGE="Usage: $(basename "${0}") certs <list|describe|delete|generate> [domain...]

  list                 Installed certificates, with expiry and trust
  describe <domain>    One certificate in full, including what it covers
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

certs_have_openssl() {
  command -v openssl >/dev/null 2>&1
}

# openssl prints "Oct  1 22:08:33 2028 GMT". Converted here rather than with
# date, whose parsing flags differ between BSD and GNU.
certs_iso_date() {
  local raw="$1" month day year

  read -r month day _ year _ <<<"${raw}"
  case "${month}" in
    Jan) month=01 ;; Feb) month=02 ;; Mar) month=03 ;; Apr) month=04 ;;
    May) month=05 ;; Jun) month=06 ;; Jul) month=07 ;; Aug) month=08 ;;
    Sep) month=09 ;; Oct) month=10 ;; Nov) month=11 ;; Dec) month=12 ;;
    *) return 1 ;;
  esac

  [[ -n "${year}" && -n "${day}" ]] || return 1
  printf '%s-%s-%02d' "${year}" "${month}" "${day}"
}

# Days until a date, or nothing when neither date dialect parses it. GNU takes
# -d, BSD takes -j -f, and the capability is tried rather than guessed.
certs_days_until() {
  local iso="$1" then now

  then="$(date -d "${iso}" +%s 2>/dev/null)" ||
    then="$(date -j -f '%Y-%m-%d' "${iso}" +%s 2>/dev/null)" ||
    return 0

  now="$(date +%s)"
  printf '%s' "$(((then - now) / 86400))"
}

# One openssl call per certificate, emitting tab-separated
# expiry-date, expired-flag, issuer and comma-joined SANs.
certs_read() {
  local cert_file="$1" out enddate issuer sans expired=no iso

  # Expiry and issuer only, with options every openssl and LibreSSL has.
  out="$(openssl x509 -in "${cert_file}" -noout -enddate -issuer 2>/dev/null)" || return 1
  enddate="$(sed -n 's/^notAfter=//p' <<<"${out}")"
  issuer="$(sed -n 's/^issuer=//p' <<<"${out}")"

  # -ext is newer than the LibreSSL macOS ships, so -text is the fallback rather
  # than letting the whole record fail with it.
  sans="$(openssl x509 -in "${cert_file}" -noout -ext subjectAltName 2>/dev/null |
    grep -o 'DNS:[^,]*' | sed 's/^DNS://' | paste -sd, -)"
  if [[ -z "${sans}" ]]; then
    sans="$(openssl x509 -in "${cert_file}" -noout -text 2>/dev/null |
      grep -A1 'Subject Alternative Name' | grep -o 'DNS:[^,]*' | sed 's/^DNS://' | paste -sd, -)"
  fi

  iso="$(certs_iso_date "${enddate}")" || iso=""
  openssl x509 -in "${cert_file}" -noout -checkend 0 >/dev/null 2>&1 || expired=yes

  printf '%s\t%s\t%s\t%s\n' "${iso}" "${expired}" "${issuer}" "${sans}"
}

# The path to the mkcert CA, or nothing when it cannot be read. Without it there
# is no way to tell a trusted certificate from any other, so the column goes.
certs_ca_path() {
  local caroot ca

  command -v mkcert >/dev/null 2>&1 || return 0
  caroot="$(mkcert -CAROOT 2>/dev/null)" || return 0
  ca="${caroot}/rootCA.pem"
  [[ -r "${ca}" ]] || return 0

  printf '%s' "${ca}"
}

# Whether the CA actually signed this certificate. Comparing issuer names would
# accept anything carrying the right CN, including a self-signed certificate
# that simply claims it, and would keep trusting certificates from a CA that has
# since been regenerated under the same name.
certs_trusted() {
  local ca="$1" cert="$2"

  # -no_check_time asks only who signed it. Without it an expired certificate
  # this CA really did sign is reported as not signed by it, contradicting the
  # expiry field beside it. The option is probed rather than assumed, because the
  # LibreSSL macOS ships is older: a self-signed CA verifies against itself, so a
  # failure there means the option is unsupported.
  if openssl verify -no_check_time -CAfile "${ca}" "${ca}" >/dev/null 2>&1; then
    openssl verify -no_check_time -CAfile "${ca}" "${cert}" >/dev/null 2>&1
    return $?
  fi

  openssl verify -CAfile "${ca}" "${cert}" >/dev/null 2>&1
}

# The CN out of an issuer distinguished name, which is what names the CA.
certs_issuer_name() {
  local dn="$1" cn
  cn="$(sed -n 's/.*CN *= *//p' <<<"${dn}")"
  printf '%s' "${cn:-${dn}}"
}

certs_list() {
  local -a domains=() expiries=() trusts=()
  local cert_file domain record iso expired issuer ca_path shown
  local expired_count=0 total=0
  local wide_domain=6 wide_expiry=7
  local have_openssl=true show_trust=false

  certs_have_openssl || have_openssl=false
  ca_path="$(certs_ca_path)"
  [[ "${have_openssl}" == "true" && -n "${ca_path}" ]] && show_trust=true

  while IFS=$'\t' read -r domain cert_file; do
    [[ -z "${cert_file}" ]] && continue
    total=$((total + 1))
    domains+=("${domain}")
    [[ "${#domain}" -gt "${wide_domain}" ]] && wide_domain="${#domain}"

    if [[ "${have_openssl}" != "true" ]]; then
      continue
    fi

    if ! record="$(certs_read "${cert_file}")"; then
      expiries+=("unreadable")
      trusts+=("-")
      continue
    fi

    IFS=$'\t' read -r iso expired issuer _ <<<"${record}"
    if [[ "${expired}" == "yes" ]]; then
      shown="expired"
      expired_count=$((expired_count + 1))
    else
      shown="${iso:-unknown}"
    fi
    expiries+=("${shown}")
    [[ "${#shown}" -gt "${wide_expiry}" ]] && wide_expiry="${#shown}"

    if [[ "${show_trust}" == "true" ]]; then
      if certs_trusted "${ca_path}" "${cert_file}"; then
        trusts+=("yes")
      else
        trusts+=("no")
      fi
    fi
  done < <(certs_files)

  if [[ "${total}" -eq 0 ]]; then
    log_warning "No certificates found"
    log_info "Generate one with: $(basename "${0}") certs generate '*.spark.loc'"
    return 0
  fi

  local i
  if [[ "${have_openssl}" != "true" ]]; then
    echo "DOMAIN"
    for ((i = 0; i < total; i++)); do
      printf '%s\n' "${domains[i]}"
    done
    echo ""
    log_info "${total} certificates. Install openssl to see expiry and trust."
    return 0
  fi

  if [[ "${show_trust}" == "true" ]]; then
    printf '%-*s  %-*s  %s\n' "${wide_domain}" "DOMAIN" "${wide_expiry}" "EXPIRES" "TRUSTED"
    for ((i = 0; i < total; i++)); do
      printf '%-*s  %-*s  %s\n' "${wide_domain}" "${domains[i]}" "${wide_expiry}" "${expiries[i]}" "${trusts[i]}"
    done
  else
    printf '%-*s  %s\n' "${wide_domain}" "DOMAIN" "EXPIRES"
    for ((i = 0; i < total; i++)); do
      printf '%-*s  %s\n' "${wide_domain}" "${domains[i]}" "${expiries[i]}"
    done
  fi

  echo ""
  if [[ "${expired_count}" -eq 0 ]]; then
    log_info "${total} certificates, none expired."
  else
    log_info "${total} certificates, ${expired_count} expired."
  fi
  [[ "${show_trust}" != "true" ]] && log_info "Trust is not shown: the mkcert CA could not be read."

  return 0
}

certs_describe() {
  local wanted="$1" cert_file key_file safe_filename record iso expired issuer sans ca_path days

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

  if ! certs_have_openssl; then
    echo ""
    log_info "Install openssl to see what this certificate covers and when it expires."
    return 0
  fi

  if ! record="$(certs_read "${cert_file}")"; then
    echo ""
    log_warning "The certificate could not be read"
    return 1
  fi

  IFS=$'\t' read -r iso expired issuer sans <<<"${record}"

  [[ -n "${sans}" ]] && echo "  covers         ${sans//,/, }"

  if [[ "${expired}" == "yes" ]]; then
    echo "  expires        ${iso:-unknown} (expired)"
  else
    days="$(certs_days_until "${iso}")"
    if [[ -n "${days}" ]]; then
      echo "  expires        ${iso} (in ${days} days)"
    else
      echo "  expires        ${iso}"
    fi
  fi

  echo "  issuer         $(certs_issuer_name "${issuer}")"

  ca_path="$(certs_ca_path)"
  if [[ -z "${ca_path}" ]]; then
    echo "  trusted        unknown, the mkcert CA could not be read"
  elif certs_trusted "${ca_path}" "${cert_file}"; then
    echo "  trusted        yes, signed by this machine's CA"
  else
    echo "  trusted        no, not signed by this machine's CA"
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

  # Staged, so a failure cannot truncate a pair that is currently working, and
  # the proxy's watcher never sees a half-written file.
  local staging
  staging="$(mktemp -d "${CERT_DIR}/.generate.XXXXXX")" || {
    log_error "Could not create a staging directory in ${CERT_DIR}"
    return 1
  }

  if ! mkcert -cert-file "${staging}/cert.pem" -key-file "${staging}/key.pem" -- "${domain}" ||
    [[ ! -s "${staging}/cert.pem" || ! -s "${staging}/key.pem" ]]; then
    rm -rf "${staging}"
    log_error "mkcert could not generate a certificate for ${domain}"
    log_info "Nothing was changed, so any certificate already installed still works"
    return 1
  fi

  # Both files or neither: a certificate installed beside the previous key is a
  # mismatched pair the proxy would serve.
  # Nothing is overwritten until it can be put back. Proceeding without a
  # rollback copy risks the worse outcome: a removed new certificate and no old
  # one either, where before there was a working pair.
  local previous=""
  if [[ -f "${CERT_DIR}/${safe_filename}.pem" ]]; then
    previous="${staging}/previous-cert.pem"
    if ! cp "${CERT_DIR}/${safe_filename}.pem" "${previous}"; then
      rm -rf "${staging}"
      log_error "The certificate already installed could not be copied aside, so it was left alone"
      log_info "Nothing was changed. The proxy still serves what it did before."
      return 1
    fi
  fi

  if ! mv "${staging}/cert.pem" "${CERT_DIR}/${safe_filename}.pem"; then
    rm -rf "${staging}"
    log_error "The certificate was generated but could not be installed in ${CERT_DIR}"
    return 1
  fi

  if ! mv "${staging}/key.pem" "${CERT_DIR}/${safe_filename}-key.pem"; then
    if [[ -n "${previous}" ]]; then
      if mv "${previous}" "${CERT_DIR}/${safe_filename}.pem"; then
        log_error "The key could not be installed, so the previous certificate was put back"
      else
        # The staging directory is deliberately left in place: the copy named
        # here is inside it, and removing it would delete the only remaining
        # copy of the certificate the user is being told to restore.
        log_error "The key could not be installed and the previous certificate could not be restored"
        log_info "A copy of it is at ${previous}. Put it back before the proxy reloads."
        return 1
      fi
    else
      rm -f "${CERT_DIR}/${safe_filename}.pem"
      log_error "The key could not be installed, so the new certificate was removed"
    fi
    log_info "Nothing is left half-installed. The proxy still serves what it did before."
    rm -rf "${staging}"
    return 1
  fi
  rm -rf "${staging}"

  log_success "Certificates generated successfully:"
  log_info "  Certificate: ${CERT_DIR}/${safe_filename}.pem"
  log_info "  Private key: ${CERT_DIR}/${safe_filename}-key.pem"

  apply_certificates
}

# Separate from certs_delete so it can be exercised: everything above it needs a
# terminal to confirm, which a test does not have.
certs_remove_files() {
  # All or nothing. A plain rm over several files can delete the certificate and
  # fail on its key, leaving a pair the proxy would try to serve. The files are
  # moved aside first and only discarded once every move has succeeded; a move
  # that fails puts back the ones already moved.
  local holding file base moved=()
  holding="$(mktemp -d "${CERT_DIR}/.remove.XXXXXX")" || {
    log_error "Could not create a working directory in ${CERT_DIR}, so nothing was removed"
    return 1
  }

  for file in "$@"; do
    base="$(basename "${file}")"
    if ! mv "${file}" "${holding}/${base}"; then
      local undo stranded=0
      for undo in "${moved[@]}"; do
        if ! mv "${holding}/$(basename "${undo}")" "${undo}"; then
          stranded=$((stranded + 1))
        fi
      done

      log_error "${base} could not be removed, so nothing was removed"
      if [[ "${stranded}" -gt 0 ]]; then
        # The directory stays: it holds the only copy of what could not go back,
        # and removing it would turn a failed removal into a real deletion.
        log_error "${stranded} file(s) could not be put back and are in ${holding}"
        log_info "Move them back before the proxy reloads"
      else
        rm -rf "${holding}"
      fi
      log_info "Check what is installed with: $(basename "${0}") certs list"
      return 1
    fi
    moved+=("${file}")
  done

  rm -rf "${holding}"
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
