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
    if [[ -z "${machine}" || -z "${hosts}" || -n "${trailing}" ]]; then
      # An omission, not a wrong answer, so it is reported and skipped.
      log_warning "Skipping a peer record this version does not recognise" >&2
      continue
    fi
    while IFS= read -r hostname; do
      [[ -n "${hostname}" ]] && printf '%s\t%s\n' "${hostname}" "${machine}"
    done < <(tr ',' '\n' <<<"${hosts}")
  done < <(tail -n +3 "${file}" 2>/dev/null)
}

# An unescaped ~ on the replacement side expands back to the home directory.
hosts_abbreviate() {
  local path="$1"
  [[ -z "${path}" ]] && return 0
  if [[ "${path}" == "${HOME}" || "${path}" == "${HOME}/"* ]]; then
    printf '~%s' "${path#"${HOME}"}"
    return 0
  fi
  printf '%s' "${path}"
}

# An absent state file means the services image predates this command, which is
# routine: the CLI updates over git and the images from the registry.
hosts_report_missing_state() {
  if declare -F is_running >/dev/null 2>&1 && is_running http-proxy; then
    log_error "The proxy is running but its services image is older than this command"
    log_info "Update the images with: ${0} upgrade"
    return 0
  fi
  log_error "No record of what is being served, because the proxy is not running"
  log_info "Start it with: ${0} start"
}

hosts_list() {
  local rows=() hostname container directory routing machine local_out line rest
  local w_host=8 w_machine=7 w_container=9

  # Command substitution: a `return 1` inside `< <(...)` never reaches here.
  if [[ ! -r "${HOSTS_STATE_FILE}" ]]; then
    hosts_report_missing_state
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

# Redacts secret-bearing values in a command line: by flag name (bare, quoted
# or --flag=value), by assignment name, and the userinfo of a URL. Never by the
# shape of a value, so a secret passed some other way is still printed.
hosts_redact_command() {
  local flags='auth|token|password|passwd|pass|secret|api-key|apikey|access-key|secret-key|client-secret|credentials|bearer'
  sed -E \
    -e "s#(--?(${flags})[= ])'[^']*'#\\1'<redacted>'#g" \
    -e "s#(--?(${flags})[= ])\"[^\"]*\"#\\1\"<redacted>\"#g" \
    -e "s#(--?(${flags})[= ])[^'\" ]+#\\1<redacted>#g" \
    -e "s#([A-Za-z0-9_]*(TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|APIKEY|ACCESS_KEY|CREDENTIALS)[A-Za-z0-9_]*=)'[^']*'#\\1'<redacted>'#g" \
    -e "s#([A-Za-z0-9_]*(TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|APIKEY|ACCESS_KEY|CREDENTIALS)[A-Za-z0-9_]*=)\"[^\"]*\"#\\1\"<redacted>\"#g" \
    -e "s#([A-Za-z0-9_]*(TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|APIKEY|ACCESS_KEY|CREDENTIALS)[A-Za-z0-9_]*=)[^'\" ]+#\\1<redacted>#g" \
    -e 's#(://)[^/@ ]+:[^/@ ]+@#\1<redacted>@#g'
}

# The published host port of one proxy port, empty when the proxy is not up.
hosts_proxy_port() {
  docker port http-proxy "$1/tcp" 2>/dev/null | head -n 1 | sed 's/.*://'
}

# The backend URL Traefik routes a hostname to, read from its API. Empty when
# the proxy is not running or nothing routes the hostname.
hosts_backend_url() {
  local hostname="$1" api_port router service provider
  api_port="$(hosts_proxy_port 8080)"
  [[ -z "${api_port}" ]] && return 0
  # One router per line, then the one whose rule names this exact host. A rule
  # quotes the host with backticks or, escaped in the JSON, double quotes.
  router="$(curl -s --max-time 5 "http://127.0.0.1:${api_port}/api/http/routers?search=${hostname}" 2>/dev/null |
    sed 's/},{/}\n{/g' | grep -F -e "Host(\`${hostname}\`)" -e "Host(\\\"${hostname}\\\")" | head -n 1)"
  [[ -z "${router}" ]] && return 0
  service="$(grep -o '"service":"[^"]*"' <<<"${router}" | head -n 1 | cut -d'"' -f4)"
  provider="$(grep -o '"provider":"[^"]*"' <<<"${router}" | head -n 1 | cut -d'"' -f4)"
  [[ -z "${service}" || -z "${provider}" ]] && return 0
  [[ "${service}" == *@* ]] || service="${service}@${provider}"
  curl -s --max-time 5 "http://127.0.0.1:${api_port}/api/http/services/${service}" 2>/dev/null |
    grep -o '"url":"[^"]*"' | head -n 1 | cut -d'"' -f4
}

# The record for a hostname served on this machine, read from Docker live.
hosts_describe_local() {
  local hostname="$1" container="$2" directory="$3" routing="$4"
  local info image state command networks uptime status backend port http_port code
  local mounts line type name source destination rw first=true

  echo "${hostname}"

  if ! info="$(docker inspect --format '{{.Config.Image}}{{"\n"}}{{.State.Status}}{{"\n"}}{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}{{"\n"}}{{.Path}} {{join .Args " "}}' "${container}" 2>/dev/null)"; then
    echo "  container      ${container}, not found"
    echo "  directory      $(hosts_abbreviate "${directory:--}")"
    echo "  routed by      ${routing}"
    log_error "The record names a container Docker no longer has; the proxy drops it on its next event"
    return 1
  fi
  image="$(sed -n 1p <<<"${info}")"
  state="$(sed -n 2p <<<"${info}")"
  networks="$(sed -n 3p <<<"${info}" | sed 's/ $//; s/ /, /g')"
  command="$(sed -n '4,$p' <<<"${info}" | hosts_redact_command)"

  # docker ps renders the uptime, so no date arithmetic on either platform.
  uptime="$(docker ps -a --filter "name=^${container}$" --format '{{.Status}}' 2>/dev/null | head -n 1)"
  status="${state}"
  if [[ -n "${uptime}" ]]; then
    uptime="$(tr '[:upper:]' '[:lower:]' <<<"${uptime:0:1}")${uptime:1}"
    [[ "${uptime}" == "${state}"* ]] && status="${uptime}" || status="${state}, ${uptime}"
  fi

  backend="$(hosts_backend_url "${hostname}")"
  port=""
  if [[ "${backend}" =~ ^[a-z]+://[^/]*:([0-9]+)(/|$) ]]; then
    port="${BASH_REMATCH[1]}"
  fi

  echo "  container      ${container}"
  echo "  image          ${image}"
  echo "  status         ${status}"
  echo "  directory      $(hosts_abbreviate "${directory:--}")"
  case "${routing}" in
  virtual-host) echo "  routed by      VIRTUAL_HOST${port:+, port ${port}}" ;;
  traefik-labels) echo "  routed by      traefik.* labels${port:+, port ${port}}" ;;
  *) echo "  routed by      ${routing}" ;;
  esac

  http_port="$(hosts_proxy_port 80)"
  if [[ -z "${http_port}" ]]; then
    echo "  backend        unknown, the proxy is not running"
    echo "  network        ${networks:-none}"
    echo "  reachable      unknown, the proxy is not running"
  else
    echo "  backend        ${backend:-none, the proxy has no route for this hostname}"
    echo "  network        ${networks:-none}"
    # Through the proxy, as a browser would go: on Docker Desktop the
    # container's own address lives inside the VM and does not answer the host.
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Host: ${hostname}" "http://127.0.0.1:${http_port}/" 2>/dev/null)"
    if [[ -z "${code}" || "${code}" == "000" ]]; then
      echo "  reachable      no answer within 5s"
    else
      echo "  reachable      ${code}"
    fi
  fi

  # A bind mount has no name, and read collapses adjacent tabs, so the fields
  # are separated by a character that is not whitespace.
  mounts="$(docker inspect --format '{{range .Mounts}}{{.Type}}{{"\x1f"}}{{.Name}}{{"\x1f"}}{{.Source}}{{"\x1f"}}{{.Destination}}{{"\x1f"}}{{.RW}}{{"\n"}}{{end}}' "${container}" 2>/dev/null)"
  if [[ -z "${mounts}" ]]; then
    echo "  mounts         none"
  fi
  while IFS=$'\x1f' read -r type name source destination rw; do
    [[ -z "${destination}" ]] && continue
    [[ "${rw}" == "true" ]] && rw="rw" || rw="ro"
    if [[ "${type}" == "volume" && -n "${name}" ]]; then
      line="${name} -> ${destination} (${rw})"
    elif [[ "${source}" == "${destination}" ]]; then
      line="$(hosts_abbreviate "${source}") -> same path (${rw})"
    else
      line="$(hosts_abbreviate "${source}") -> ${destination} (${rw})"
    fi
    if [[ "${first}" == "true" ]]; then
      echo "  mounts         ${line}"
      first=false
    else
      echo "                 ${line}"
    fi
  done <<<"${mounts}"

  echo "  command        ${command}"
}

hosts_describe() {
  local wanted="$1" hostname container directory routing machine found=false local_out line rest rc=0

  if [[ -z "${wanted}" ]]; then
    log_error "Which hostname? Usage: ${0} hosts describe <hostname>"
    return 1
  fi

  if [[ ! -r "${HOSTS_STATE_FILE}" ]]; then
    hosts_report_missing_state
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
    hosts_describe_local "${hostname}" "${container}" "${directory}" "${routing}" || rc=1
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
  return "${rc}"
}

# One usage string, so the help and the error cannot drift apart.
hosts_usage() {
  echo "Usage: ${0} hosts [list|describe <hostname>|--json]"
  echo ""
  echo "  list                 Every hostname served, local and from peers (the default)"
  echo "  describe <hostname>  One hostname's container, read live: image, status, routing, backend, mounts, command"
  echo "  --json               The local records, machine-readable"
  echo ""
  echo "Hostnames served by other machines carry no directory, and are listed in full by:"
  echo "  ${0} tailscale-peers --json"
}

hosts_command() {
  local subcommand="${1:-list}"

  case "${subcommand}" in
  list) hosts_list ;;
  describe) hosts_describe "${2:-}" ;;
  --json)
    if [[ ! -r "${HOSTS_JSON_FILE}" ]]; then
      hosts_report_missing_state
      return 1
    fi
    cat "${HOSTS_JSON_FILE}"
    ;;
  -h | --help)
    hosts_usage
    ;;
  *)
    log_error "Unknown option: ${subcommand}"
    hosts_usage >&2
    return 1
    ;;
  esac
}
