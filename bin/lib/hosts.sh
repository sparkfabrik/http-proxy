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
    directory="$(abbreviate_home "${rest%%$'\t'*}")"
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

# Redacts credential values in a command line. Two mechanisms, because a
# credential appears two ways: as its own argument after a flag, and inside one
# argument such as the script passed to sh -c. Arguments arrive one per line so
# the first case can replace the whole value however many spaces it contains, and
# the second is applied within each argument. Matched on the flag NAME: guessing
# from the shape of a value hides the wrong things.
HOSTS_SECRET='[Aa][Uu][Tt][Hh]|[Tt][Oo][Kk][Ee][Nn]|[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]|[Pp][Aa][Ss][Ss][Pp][Hh][Rr][Aa][Ss][Ee]|[Ss][Ee][Cc][Rr][Ee][Tt]|[Kk][Ee][Yy]|[Cc][Rr][Ee][Dd][Ee][Nn][Tt][Ii][Aa][Ll]|[Bb][Ee][Aa][Rr][Ee][Rr]'

hosts_redact_args() {
  local arg original prev="" out=""
  local flag="(--?[A-Za-z0-9_-]*(${HOSTS_SECRET})[A-Za-z0-9_-]*)"

  while IFS= read -r arg; do
    [[ -z "${arg}" ]] && continue
    original="${arg}"
    if [[ "${prev}" =~ ^--?[A-Za-z0-9_-]*(${HOSTS_SECRET})[A-Za-z0-9_-]*$ ]]; then
      # Its own argument, so the whole thing goes whatever it contains.
      arg="<redacted>"
    else
      arg="$(sed -E \
        -e "s/${flag}=[^[:space:]]*/\1=<redacted>/g" \
        -e "s/${flag}([[:space:]]+)'[^']*'/\1\3<redacted>/g" \
        -e "s/${flag}([[:space:]]+)\"[^\"]*\"/\1\3<redacted>/g" \
        -e "s/${flag}([[:space:]]+)[^[:space:]-][^[:space:]]*/\1\3<redacted>/g" \
        <<<"${arg}")"
    fi
    if [[ -z "${out}" ]]; then
      out="${arg}"
    else
      out="${out} ${arg}"
    fi
    prev="${original}"
  done

  printf '%s' "${out}"
}

# Whether the hostname answers through the proxy. Probed on the proxy's own
# published port with a Host header, not on the container's bridge address: that
# address lives inside the Docker VM on macOS and is unreachable from the host,
# so probing it would report every healthy backend as silent. Bounded, because a
# hung backend must not hang describe.
hosts_probe() {
  local hostname="$1" port code

  command -v curl >/dev/null 2>&1 || return 0
  port="$(get_service_port traefik 80 2>/dev/null)"
  [[ -n "${port}" ]] || return 0

  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 \
    -H "Host: ${hostname}" "http://127.0.0.1:${port}" 2>/dev/null)" || return 0
  [[ "${code}" == "000" ]] && return 0
  printf '%s' "${code}"
}

# Renders one local hostname from the container itself. The state file says which
# container serves it; everything below is read live, because uptime and
# reachability are not facts a file can hold.
hosts_describe_local() {
  local hostname="$1" container="$2" directory="$3" routing="$4"
  local detail image status netmode ip path uptime port url code mounts command_line

  detail="$(docker inspect -f '{{.Config.Image}}
{{.State.Status}}
{{.HostConfig.NetworkMode}}
{{range $k,$v := .NetworkSettings.Networks}}{{$v.IPAddress}} {{end}}
{{.Path}}' "${container}" 2>/dev/null)" || detail=""

  if [[ -z "${detail}" ]]; then
    echo "${hostname}"
    echo "  container      ${container}, which no longer exists"
    log_warning "This record is stale: the container is gone but the proxy has not rescanned yet"
    log_info "See what is served now with: ${0} hosts list"
    return 1
  fi

  {
    read -r image
    read -r status
    read -r netmode
    read -r ip
    read -r path
  } <<<"${detail}"
  ip="${ip% }"
  ip="${ip%% *}"

  # VIRTUAL_PORT is filtered here rather than in the template: Docker's template
  # functions have no hasPrefix.
  port="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${container}" 2>/dev/null |
    sed -n 's/^VIRTUAL_PORT=//p' | head -1)"
  # A label-routed container declares its port as a traefik service label
  # instead, and some declare neither and rely on the image's exposed port.
  if [[ -z "${port}" ]]; then
    port="$(docker inspect -f '{{range $k,$v := .Config.Labels}}{{$k}}={{$v}}{{println}}{{end}}' "${container}" 2>/dev/null |
      sed -n 's/^traefik\.http\.services\..*\.loadbalancer\.server\.port=//p' | head -1)"
  fi

  # docker ps renders the uptime, which avoids date arithmetic that differs
  # between BSD and GNU.
  uptime="$(docker ps --filter "name=^${container}$" --format '{{.Status}}' 2>/dev/null | head -1)"

  echo "${hostname}"
  echo "  container      ${container}"
  echo "  image          ${image}"
  if [[ -n "${uptime}" ]]; then
    echo "  status         ${status}, up ${uptime#Up }"
  else
    echo "  status         ${status}"
  fi
  [[ -n "${directory}" ]] && echo "  directory      $(abbreviate_home "${directory}")"

  case "${routing}" in
    virtual-host)
      if [[ -n "${port}" ]]; then
        echo "  routed by      VIRTUAL_HOST, port ${port}"
      else
        echo "  routed by      VIRTUAL_HOST"
      fi
      ;;
    traefik-labels)
      echo "  routed by      its own traefik labels"
      ;;
    *)
      echo "  routed by      ${routing}"
      ;;
  esac

  if [[ -n "${ip}" && -n "${port}" ]]; then
    url="http://${ip}:${port}"
    echo "  backend        ${url}"
  fi
  [[ -n "${netmode}" ]] && echo "  network        ${netmode}"

  # Probed through the proxy by hostname, so a container that declares no port
  # is still reported rather than silently missing the field.
  code="$(hosts_probe "${hostname}")"
  if [[ -n "${code}" ]]; then
    echo "  reachable      ${code}"
  else
    echo "  reachable      no answer"
  fi

  # Volumes carry a name and binds do not, so both are read and the name wins.
  mounts="$(docker inspect -f '{{range .Mounts}}{{.Type}}|{{.Name}}|{{.Source}}|{{.Destination}}|{{if .RW}}rw{{else}}ro{{end}}{{println}}{{end}}' "${container}" 2>/dev/null)"
  local first=true kind name source dest rw from to
  while IFS='|' read -r kind name source dest rw; do
    [[ -z "${kind}" ]] && continue
    from="${name:-$(abbreviate_home "${source}")}"
    if [[ "${source}" == "${dest}" ]]; then
      to="same path"
    else
      to="${dest}"
    fi
    if [[ "${first}" == "true" ]]; then
      printf '  mounts         %s -> %s (%s)\n' "${from}" "${to}" "${rw}"
      first=false
    else
      printf '                 %s -> %s (%s)\n' "${from}" "${to}" "${rw}"
    fi
  done <<<"${mounts}"

  if [[ -n "${path}" ]]; then
    command_line="$(docker inspect -f '{{.Path}}{{println}}{{range .Args}}{{println .}}{{end}}' "${container}" 2>/dev/null |
      hosts_redact_args)"
    printf '  command        %s\n' "${command_line}"
  fi

  return 0
}

hosts_describe() {
  local wanted="$1" hostname container directory routing machine found=false local_out line rest

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
    hosts_describe_local "${hostname}" "${container}" "${directory}" "${routing}" || return 1
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

hosts_usage() {
  echo "Usage: ${0} hosts [list|describe <hostname>|--json]"
  echo ""
  echo "  list                 Every hostname served, local and from peers (the default)"
  echo "  describe <hostname>  One hostname: its container, directory and routing"
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
