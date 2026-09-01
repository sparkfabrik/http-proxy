#!/usr/bin/env bash
# Renders `spark-http-proxy hosts` from the two state files the services write.

# Emits hostname, container, directory and routing per local hostname.
hosts_local_rows() {
  local file="${HOSTS_STATE_FILE}" line tabs hostname container directory routing rest

  [[ -r "${file}" ]] || return 0

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue

    # Split by hand: read collapses two tabs, losing an empty directory.
    tabs="${line//[^$'\t']/}"
    if [[ "${#tabs}" -ne 3 ]]; then
      log_error "The hosts record has a shape this version does not recognise"
      log_info "Nothing is shown rather than a wrong directory. Try: ${0} self-update"
      return 1
    fi

    hostname="${line%%$'\t'*}"
    rest="${line#*$'\t'}"
    container="${rest%%$'\t'*}"
    rest="${rest#*$'\t'}"
    directory="${rest%%$'\t'*}"
    routing="${rest#*$'\t'}"

    if [[ -z "${hostname}" || -z "${container}" || -z "${routing}" ]]; then
      log_error "The hosts record has a shape this version does not recognise"
      log_info "Nothing is shown rather than a wrong directory. Try: ${0} self-update"
      return 1
    fi

    printf '%s\t%s\t%s\t%s\n' "${hostname}" "${container}" "${directory}" "${routing}"
  done <"${file}"
}

# Emits hostname and machine per hostname a peer forwards.
hosts_remote_rows() {
  local file="${TAILSCALE_SUMMARY_FILE}" machine hosts trailing hostname

  [[ -r "${file}" ]] || return 0

  while IFS=$'\t' read -r machine hosts trailing; do
    [[ -z "${machine}" || -z "${hosts}" || -n "${trailing}" ]] && continue
    while IFS= read -r hostname; do
      [[ -n "${hostname}" ]] && printf '%s\t%s\n' "${hostname}" "${machine}"
    done < <(tr ',' '\n' <<<"${hosts}")
  done < <(tail -n +3 "${file}" 2>/dev/null)
}

# An unescaped ~ on the replacement side expands back to the home directory.
hosts_abbreviate() {
  local path="$1"
  [[ -z "${path}" ]] && return 0
  printf '%s' "${path/#${HOME}/\~}"
}

hosts_list() {
  local rows=() hostname container directory routing machine local_out line rest
  local w_host=8 w_machine=7 w_container=9

  # Command substitution: a `return 1` inside `< <(...)` never reaches here.
  if ! local_out="$(hosts_local_rows)"; then
    return 1
  fi

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    hostname="${line%%$'\t'*}"
    rest="${line#*$'\t'}"
    container="${rest%%$'\t'*}"
    rest="${rest#*$'\t'}"
    directory="$(hosts_abbreviate "${rest%%$'\t'*}")"
    routing="${rest#*$'\t'}"
    rows+=("${hostname}"$'\t'"local"$'\t'"${container}"$'\t'"${directory:--}"$'\t'"${routing}")
  done <<<"${local_out}"

  while IFS=$'\t' read -r hostname machine; do
    [[ -z "${hostname}" ]] && continue
    rows+=("${hostname}"$'\t'"${machine}"$'\t'"-"$'\t'"-"$'\t'"remote")
  done < <(hosts_remote_rows)

  if [[ "${#rows[@]}" -eq 0 ]]; then
    log_info "Nothing is being served through this proxy"
    log_info "Expose a container with VIRTUAL_HOST, or start peer routing with: ${0} start-with-tailscale"
    return 0
  fi

  local row fields
  for row in "${rows[@]}"; do
    IFS=$'\t' read -r -a fields <<<"${row}"
    [[ "${#fields[0]}" -gt "${w_host}" ]] && w_host="${#fields[0]}"
    [[ "${#fields[1]}" -gt "${w_machine}" ]] && w_machine="${#fields[1]}"
    [[ "${#fields[2]}" -gt "${w_container}" ]] && w_container="${#fields[2]}"
  done

  printf '%-*s  %-*s  %-*s  %s\n' "${w_host}" "HOSTNAME" "${w_machine}" "MACHINE" "${w_container}" "CONTAINER" "DIRECTORY"
  for row in "${rows[@]}"; do
    IFS=$'\t' read -r -a fields <<<"${row}"
    printf '%-*s  %-*s  %-*s  %s\n' "${w_host}" "${fields[0]}" "${w_machine}" "${fields[1]}" "${w_container}" "${fields[2]}" "${fields[3]}"
  done
}

hosts_describe() {
  local wanted="$1" hostname container directory routing machine found=false local_out line rest

  if [[ -z "${wanted}" ]]; then
    log_error "Which hostname? Usage: ${0} hosts describe <hostname>"
    return 1
  fi

  if ! local_out="$(hosts_local_rows)"; then
    return 1
  fi

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    hostname="${line%%$'\t'*}"
    rest="${line#*$'\t'}"
    container="${rest%%$'\t'*}"
    rest="${rest#*$'\t'}"
    directory="${rest%%$'\t'*}"
    routing="${rest#*$'\t'}"
    [[ "${hostname}" != "${wanted}" ]] && continue
    found=true
    echo "${hostname}"
    echo "  served by      this machine"
    echo "  container      ${container}"
    echo "  directory      $(hosts_abbreviate "${directory}")"
    echo "  routed by      ${routing}"
  done <<<"${local_out}"

  while IFS=$'\t' read -r hostname machine; do
    [[ "${hostname}" != "${wanted}" ]] && continue
    found=true
    echo "${hostname}"
    echo "  served by      ${machine}"
    echo "  directory      not published, because it is on another machine"
  done < <(hosts_remote_rows)

  if [[ "${found}" != "true" ]]; then
    log_error "Nothing serves ${wanted}"
    log_info "See what does with: ${0} hosts"
    return 1
  fi
}

hosts_command() {
  local subcommand="${1:-list}"

  case "${subcommand}" in
  list) hosts_list ;;
  describe) hosts_describe "${2:-}" ;;
  --json)
    # The state file itself, as tailscale-peers --json does.
    [[ -r "${HOSTS_STATE_FILE}" ]] && cat "${HOSTS_STATE_FILE}"
    ;;
  *)
    log_error "Unknown option: ${subcommand}"
    log_info "Usage: ${0} hosts [list|describe <hostname>|--json]"
    return 1
    ;;
  esac
}
