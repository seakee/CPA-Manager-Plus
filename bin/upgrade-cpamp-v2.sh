#!/usr/bin/env bash
# One host invocation; Docker metadata selects the source, the image upgrade
# engine owns data qualification, backup integrity and operation checkpoints.
set -euo pipefail
umask 077
export LC_ALL=C

fail() { printf 'Upgrade stopped: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
usage() {
  cat <<'USAGE'
Usage: bash bin/upgrade-cpamp-v2.sh --image REPOSITORY:EXPLICIT_V2_TAG [options]
  --manager NAME_OR_ID    select old Manager (otherwise unique Compose service)
  --source-mount PATH     old Manager mount destination (default: database parent)
  --source-version V     actual known Manager release; omit if unknown
  --cpa NAME_OR_ID        select associated CPA instead of metadata discovery
  --cpa-version V        actual observed CPA release if metadata lacks it
  --mode managed|external default: managed when exact CPA mounts are available
  --network NAME         extra existing network for external CPA reachability
  --inherit-env NAME     pass selected runtime env var from old Manager (repeatable)
  --public-host HOST     browser-reachable host if the published IP is wildcard
  --public-origin ORIGIN canonical http(s)://host[:port] behind a TLS/reverse proxy
  --state-dir PATH       isolated private state directory outside source/CPA mounts
  --rollback             explicit, identity-checked restart of original objects
  --confirm              accept the printed stop/backup/deployment plan

An explicit v2 image is mandatory on first use; mutable latest/slim/full tags
are not accepted. Release tags/digests are pulled before services stop, then
used by immutable ID. An already-local sha256:<64hex> image ID needs no pull.
No secrets are printed or saved. Login with the EXISTING Manager admin key.
The old containers and original source are never deleted or changed. Separate
Docker volumes retain the verified backup, new data and runtime. Re-run using
the same --state-dir to resume; the database operation is authoritative.
Rollback is never automatic; v2 writes are NOT copied back to the old database.
For remote Docker daemons set --public-host or --public-origin for browser access.
Behind a controlled TLS reverse proxy set --public-origin (or CPAMP_PUBLIC_ORIGIN).
USAGE
}

# Values are never evaluated as shell. Reject controls and Docker CSV separators
# at the boundary; mounts are always discrete array arguments, never shell text.
scalar() {
  [ -n "$2" ] || fail "$1 is empty"
  [[ "$2" != *[$'\001'-$'\037'$'\177']* ]] || fail "$1 contains control characters"
}
mount_value() { scalar "$1" "$2"; [[ "$2" != *,* ]] || fail "$1 contains a Docker mount separator"; }
object_name() { [[ "$2" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || fail "Invalid $1"; }
semver_re='^[vV]?([0-9]{1,9})\.([0-9]{1,9})\.([0-9]{1,9})(-[0-9A-Za-z._-]+)?(\+[0-9A-Za-z.+_-]+)?$'
manager_v1_14_re='^[vV]?1\.14\.(0|[1-9][0-9]{0,2})$'
v2_release_re='^[vV]?2\.[0-9]{1,9}\.[0-9]{1,9}(-[0-9A-Za-z._-]+)?(\+[0-9A-Za-z.+_-]+)?$'
image_ref_re='^[A-Za-z0-9][A-Za-z0-9._/@:-]*$'
release() { [[ "$2" =~ $semver_re ]] || fail "Invalid $1 release"; }
cpa_v8_supported() {
  [[ "$1" =~ $semver_re ]] || fail 'Invalid CPA release'
  local major=$((10#${BASH_REMATCH[1]})) minor=$((10#${BASH_REMATCH[2]})) patch=$((10#${BASH_REMATCH[3]})) pre="${BASH_REMATCH[4]-}"
  if [ "$major" -lt 8 ] || { [ "$major" -eq 8 ] && [ "$minor" -eq 0 ] && [ "$patch" -eq 0 ] && [ -n "$pre" ]; }; then
    fail 'CPA must first be upgraded independently to a supported v8 release (v8.0.0 or later v8); Manager version does not qualify CPA'
  fi
  [ "$major" -eq 8 ] || fail 'Unsupported CPA major release; only CPA v8 releases (v8.0.0 or later v8) are supported'
}
cpa_image_tag_release() {
  local ref="$1" last repo tag
  [ -n "$ref" ] && [[ "$ref" != *@* ]] && [[ "$ref" != sha256:* ]] || return 1
  last="${ref##*/}"
  [[ "$last" == *:* ]] || return 1
  repo="${last%%:*}"; tag="${last#*:}"
  [ -n "$repo" ] && [ -n "$tag" ] && [[ "$tag" != *:* ]] && [ "$tag" != latest ] || return 1
  [[ "$tag" =~ $semver_re ]] || return 1
  printf '%s\n' "$tag"
}
valid_ipv4() {
  local ip="$1" rest octet count=0 octet_re='^(0|[1-9][0-9]{0,2})$'
  [[ "$ip" == *.* ]] && [[ "$ip" != .* ]] && [[ "$ip" != *. ]] && [[ "$ip" != *..* ]] || return 1
  rest="$ip."
  while [ -n "$rest" ]; do
    octet="${rest%%.*}"; rest="${rest#*.}"
    [[ "$octet" =~ $octet_re ]] && [ "$octet" -le 255 ] || return 1
    count=$((count + 1))
  done
  [ "$count" -eq 4 ]
}
valid_ipv6() {
  local addr="$1" left right part rest groups=0 compressed=0 hex_re='^[0-9a-f]{1,4}$' nonzero=0
  [ -n "$addr" ] && [[ "$addr" != *:::* ]] || return 1
  if [[ "$addr" == *::* ]]; then
    compressed=1
    left="${addr%%::*}"; right="${addr#*::}"
    [[ "$right" != *::* ]] || return 1
  else
    left="$addr"; right=''
  fi
  if [ -n "$left" ]; then
    [[ "$left" != :* ]] && [[ "$left" != *: ]] || return 1
    rest="$left:"
    while [ -n "$rest" ]; do
      part="${rest%%:*}"; rest="${rest#*:}"
      [[ "$part" =~ $hex_re ]] || return 1
      [ "$((16#$part))" -eq 0 ] || nonzero=1
      groups=$((groups + 1))
    done
  fi
  if [ -n "$right" ]; then
    [[ "$right" != :* ]] && [[ "$right" != *: ]] || return 1
    rest="$right:"
    while [ -n "$rest" ]; do
      part="${rest%%:*}"; rest="${rest#*:}"
      if [ -z "$rest" ] && [[ "$part" == *.* ]]; then
        valid_ipv4 "$part" || return 1
        [ "$part" = 0.0.0.0 ] || nonzero=1
        groups=$((groups + 2))
      else
        [[ "$part" =~ $hex_re ]] || return 1
        [ "$((16#$part))" -eq 0 ] || nonzero=1
        groups=$((groups + 1))
      fi
    done
  fi
  [ "$nonzero" = 1 ] || return 1
  if [ "$compressed" = 1 ]; then [ "$groups" -le 7 ]; else [ "$groups" -eq 8 ]; fi
}
canon_ipv6() {
  local lowered left right rest part words=() left_words=() right_words=()
  local oct1 oct2 oct3 oct4 fill i run_start=-1 run_len=0 best_start=-1 best_len=0 out=''
  lowered="$(printf '%s' "$1" | tr 'A-Z' 'a-z')"
  valid_ipv6 "$lowered" || return 1
  if [[ "$lowered" == *::* ]]; then
    left="${lowered%%::*}"
    right="${lowered#*::}"
  else
    left="$lowered"
    right=''
  fi
  if [ -n "$left" ]; then
    rest="$left:"
    while [ -n "$rest" ]; do
      part="${rest%%:*}"; rest="${rest#*:}"
      if [ -z "$rest" ] && [[ "$lowered" != *::* ]] && [[ "$part" == *.* ]]; then
        IFS=. read -r oct1 oct2 oct3 oct4 <<< "$part"
        left_words+=("$(( (10#$oct1 << 8) | 10#$oct2 ))" "$(( (10#$oct3 << 8) | 10#$oct4 ))")
      else
        left_words+=("$((16#$part))")
      fi
    done
  fi
  if [ -n "$right" ]; then
    rest="$right:"
    while [ -n "$rest" ]; do
      part="${rest%%:*}"; rest="${rest#*:}"
      if [ -z "$rest" ] && [[ "$part" == *.* ]]; then
        IFS=. read -r oct1 oct2 oct3 oct4 <<< "$part"
        right_words+=("$(( (10#$oct1 << 8) | 10#$oct2 ))" "$(( (10#$oct3 << 8) | 10#$oct4 ))")
      else
        right_words+=("$((16#$part))")
      fi
    done
  fi
  if [[ "$lowered" == *::* ]]; then
    fill=$((8 - ${#left_words[@]} - ${#right_words[@]}))
    [ "${#left_words[@]}" -eq 0 ] || words+=("${left_words[@]}")
    for ((i=0; i<fill; i++)); do words+=(0); done
    [ "${#right_words[@]}" -eq 0 ] || words+=("${right_words[@]}")
  else
    words=("${left_words[@]}")
  fi
  [ "${#words[@]}" -eq 8 ] || return 1
  for ((i=0; i<8; i++)); do
    if [ "${words[i]}" -eq 0 ]; then
      if [ "$run_len" -eq 0 ]; then run_start=$i; fi
      run_len=$((run_len + 1))
      if [ "$run_len" -ge 2 ] && [ "$run_len" -gt "$best_len" ]; then
        best_start=$run_start
        best_len=$run_len
      fi
    else
      run_len=0
    fi
  done
  i=0
  while [ "$i" -lt 8 ]; do
    if [ "$i" -eq "$best_start" ]; then
      if [ "$i" -eq 0 ]; then out='::'; else out="${out}::"; fi
      i=$((i + best_len))
      continue
    fi
    if [ -n "$out" ] && [[ "$out" != *:: ]]; then out="${out}:"; fi
    out="${out}$(printf '%x' "${words[i]}")"
    i=$((i + 1))
  done
  printf '%s\n' "$out"
}
canon_concrete_ip() {
  local raw="$1"
  raw="${raw#[}"; raw="${raw%]}"
  if [[ "$raw" == *:* ]]; then
    [ "$raw" != '::' ] && canon_ipv6 "$raw"
  else
    valid_ipv4 "$raw" && [ "$raw" != 0.0.0.0 ] && printf '%s\n' "$raw"
  fi
}
valid_origin_hostname() {
  local host="$1" rest label last='' count=0 label_re='^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' digits_re='^[0-9]+$'
  [ -n "$host" ] && [ "${#host}" -le 253 ] || return 1
  [[ "$host" != .* ]] && [[ "$host" != *. ]] && [[ "$host" != *..* ]] || return 1
  rest="$host."
  while [ -n "$rest" ]; do
    label="${rest%%.*}"; rest="${rest#*.}"
    [[ "$label" =~ $label_re ]] || return 1
    last="$label"; count=$((count + 1))
  done
  if [ "$count" -gt 1 ] && [[ "$last" =~ $digits_re ]]; then return 1; fi
  return 0
}
normalize_public_origin() {
  local name="$1" raw="$2" lowered scheme authority ipv6_addr rest host port port_re='^[1-9][0-9]{0,4}$' digits_dots_re='^[0-9.]+$'
  scalar "$name" "$raw"
  [[ "$raw" != *[[:space:],@?#\\]* ]] || fail "$name must be a bare http(s)://host[:port] origin without credentials, path, query or fragment"
  [[ "$raw" != *[!$'\041'-$'\176']* ]] || fail "$name must use ASCII origin syntax (use punycode for IDNs)"
  lowered="$(printf '%s' "$raw" | tr 'A-Z' 'a-z')"
  [ "${lowered%/}" = "$lowered" ] || lowered="${lowered%/}"
  case "$lowered" in
    http://*) scheme=http; authority="${lowered#http://}" ;;
    https://*) scheme=https; authority="${lowered#https://}" ;;
    *) fail "$name must start with http:// or https://" ;;
  esac
  [ -n "$authority" ] && [[ "$authority" != */* ]] || fail "$name must not include a path"
  port=''
  if [[ "$authority" == \[* ]]; then
    [[ "$authority" == *\]* ]] || fail "Invalid IPv6 host or port in $name"
    ipv6_addr="${authority#\[}"; ipv6_addr="${ipv6_addr%%\]*}"
    rest="${authority#*\]}"
    [[ "$rest" != *\[* ]] && [[ "$rest" != *\]* ]] || fail "Invalid brackets in $name"
    if [ -n "$rest" ]; then
      [[ "$rest" == :* ]] || fail "Invalid IPv6 authority in $name"
      port="${rest#:}"
      [ -n "$port" ] || fail "Invalid port in $name"
    fi
    valid_ipv6 "$ipv6_addr" || fail "Invalid IPv6 literal in $name"
    host="[$ipv6_addr]"
  else
    [[ "$authority" != *\[* ]] && [[ "$authority" != *\]* ]] || fail "Invalid brackets in $name"
    if [[ "$authority" == *:* ]]; then
      host="${authority%%:*}"; port="${authority#*:}"
      [ -n "$port" ] && [[ "$port" != *:* ]] || fail "Invalid host or port in $name (wrap IPv6 in brackets)"
    else
      host="$authority"
    fi
    if [[ "$host" =~ $digits_dots_re ]]; then
      valid_ipv4 "$host" && [ "$host" != 0.0.0.0 ] || fail "Invalid IPv4 literal in $name"
    else
      valid_origin_hostname "$host" || fail "Invalid hostname in $name"
    fi
  fi
  if [ -n "$port" ]; then
    [[ "$port" =~ $port_re ]] && [ "$port" -le 65535 ] || fail "Invalid port in $name"
    if { [ "$scheme" = http ] && [ "$port" = 80 ]; } || { [ "$scheme" = https ] && [ "$port" = 443 ]; }; then port=''; fi
  fi
  normalized_origin="$scheme://$host${port:+:$port}"
}
normalize_public_host() {
  local name="$1" raw="$2" lowered candidate digits_dots_re='^[0-9.]+$'
  scalar "$name" "$raw"
  [[ "$raw" =~ ^[[:graph:]]+$ ]] || fail "$name must not contain whitespace or control characters"
  [[ "$raw" != *'://'* ]] && [[ "$raw" != */* ]] && [[ "$raw" != *\?* ]] && [[ "$raw" != *#* ]] && [[ "$raw" != *@* ]] && [[ "$raw" != *\\* ]] && [[ "$raw" != *,* ]] ||
    fail "$name must be a bare hostname or IP literal, not a URL"
  [[ "$raw" != *[!$'\041'-$'\176']* ]] || fail "$name must use ASCII syntax (use punycode for IDNs)"
  lowered="$(printf '%s' "$raw" | tr 'A-Z' 'a-z')"
  if [[ "$lowered" == \[*\] ]]; then
    candidate="${lowered#\[}"
    candidate="${candidate%\]}"
    [[ "$candidate" != *\[* ]] && [[ "$candidate" != *\]* ]] && valid_ipv6 "$candidate" ||
      fail "Invalid bracketed IPv6 literal in $name"
    normalized_public_host="$candidate"
    return 0
  fi
  [[ "$lowered" != *\[* ]] && [[ "$lowered" != *\]* ]] || fail "Invalid brackets in $name"
  if [[ "$lowered" == *:* ]]; then
    valid_ipv6 "$lowered" || fail "$name must be a hostname or non-wildcard IP without a port"
    normalized_public_host="$lowered"
    return 0
  fi
  if [[ "$lowered" =~ $digits_dots_re ]]; then
    valid_ipv4 "$lowered" && [ "$lowered" != 0.0.0.0 ] || fail "Invalid non-wildcard IPv4 literal in $name"
    normalized_public_host="$lowered"
    return 0
  fi
  valid_origin_hostname "$lowered" || fail "Invalid hostname in $name"
  normalized_public_host="$lowered"
}
valid_image_ref() {
  [[ "$1" =~ $image_ref_re ]] || fail 'Invalid image reference'
  case "$1" in
    sha256:*) [[ "$1" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid image digest identity' ;;
    *@sha256:*) [[ "${1#*@}" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid pinned image digest' ;;
    *:latest|*:slim|*:full) fail 'Choose an explicit v2 release tag or digest' ;;
    *:*) ;;
    *) fail 'Explicit image tag or digest required' ;;
  esac
}
clean_container_path() {
  local name="$1" p="$2"
  [[ "$p" == /* ]] || fail "$name must be an absolute container path"
  [[ "$p" != *//* ]] && [[ "$p" != */. ]] && [[ "$p" != */.. ]] &&
    [[ "$p" != */./* ]] && [[ "$p" != */../* ]] || fail "$name must be a clean container path without dot segments"
  [ "$p" = / ] || p="${p%/}"
  printf '%s\n' "$p"
}
canonical_path() {
  local input="$1" hops="${2:-0}" p cur=/ rest seg next link_target missing=0
  [ "$hops" -le 20 ] || fail "Too many symbolic links while resolving path: $input"
  if [[ "$input" == /* ]]; then p="$input"; else p="$(pwd -P)/$input"; fi
  rest="${p#/}/"
  while [ -n "$rest" ]; do
    seg="${rest%%/*}"; rest="${rest#*/}"
    [ -n "$seg" ] && [ "$seg" != . ] || continue
    if [ "$seg" = .. ]; then
      cur="${cur%/*}"; [ -n "$cur" ] || cur=/
      [ ! -d "$cur" ] || missing=0
      continue
    fi
    next="${cur%/}/$seg"
    if [ "$missing" = 0 ]; then
      if [ -d "$next" ]; then
        cur="$(cd -P -- "$next" 2>/dev/null && pwd -P)" || fail "Cannot resolve directory: $input"
      elif [ -L "$next" ]; then
        link_target="$(readlink "$next")" || fail "Cannot read symbolic link: $next"
        if [[ "$link_target" == /* ]]; then
          cur="$(canonical_path "$link_target" "$((hops + 1))")" || exit 1
        else
          cur="$(canonical_path "${cur%/}/$link_target" "$((hops + 1))")" || exit 1
        fi
        [ -d "$cur" ] || missing=1
      else
        cur="$next"; missing=1
      fi
    else
      cur="$next"
    fi
  done
  printf '%s\n' "$cur"
}
paths_overlap() {
  local left="$1" right="$2"
  [ "$left" = "$right" ] || [ "$left" = / ] || [ "$right" = / ] || [[ "$left" == "$right/"* ]] || [[ "$right" == "$left/"* ]]
}
valid_control_prefix() {
  local p="$1" prefix_re='^/[A-Za-z0-9._~$&+,/:;=@-]+$'
  [ -n "$p" ] && [ "$p" != / ] && [[ "$p" =~ $prefix_re ]] || return 1
  [[ "$p" != *//* ]] && [[ "$p" != */ ]] && [[ "$p" != */. ]] && [[ "$p" != */.. ]] &&
    [[ "$p" != */./* ]] && [[ "$p" != */../* ]]
}
decode_json_prefix() {
  local v="$1" esc='\u0026'
  while [[ "$v" == *"$esc"* ]]; do
    v="${v%%"$esc"*}&${v#*"$esc"}"
  done
  printf '%s\n' "$v"
}
inspect() { docker inspect --format "$2" "$1"; }
label() { inspect "$1" "{{with index .Config.Labels \"$2\"}}{{.}}{{end}}"; }
image_label() { docker image inspect --format "{{with index .Config.Labels \"$2\"}}{{.}}{{end}}" "$1"; }
env_value() {
  # Deliberately select one non-secret field. Never persist raw inspect/env.
  local raw line result='' found=0
  raw="$(inspect "$1" "{{range .Config.Env}}{{if eq (index (split . \"=\") 0) \"$2\"}}{{println .}}{{end}}{{end}}")"
  while IFS= read -r line; do
    case "$line" in "$2="*) result="${line#*=}"; found=1 ;; esac
  done <<< "$raw"
  [ "$found" = 0 ] || printf '%s\n' "$result"
}
env_nonempty() {
  # Detect presence of a variable without reading or emitting its secret value.
  [ -n "$(inspect "$1" "{{range .Config.Env}}{{if eq (index (split . \"=\") 0) \"$2\"}}{{if ne . \"$2=\"}}{{println \"1\"}}{{end}}{{end}}{{end}}")" ]
}
resolved_env_defined=0
resolved_env_value=''
env_scalar_value() {
  # Loss-free read of a single env entry: propagates inspect exit status, requires
  # the trailing X sentinel before interpreting empty output as absence, and
  # preserves embedded/trailing newlines without echoing secret values.
  local label="$1" container_ref="$2" key="$3" raw entry val rc=0
  raw="$(inspect "$container_ref" "{{range .Config.Env}}{{if eq (index (split . \"=\") 0) \"$key\"}}{{printf \"%s\\n\" .}}{{end}}{{end}}X")" || rc=$?
  [ "$rc" -eq 0 ] && [[ "$raw" == *X ]] || fail "Cannot inspect environment entry for $label"
  raw="${raw%X}"
  if [ -z "$raw" ]; then
    resolved_env_defined=0
    resolved_env_value=''
    return 0
  fi
  entry="${raw%$'\n'}"
  [[ "$entry" != *$'\n'* ]] && [[ "$entry" != *$'\r'* ]] || fail "$label has a multiline or duplicate environment entry"
  val="${entry#*=}"
  if [ -n "$val" ]; then
    [[ "$val" != *$'\t'* ]] && [[ "$val" != *[$'\001'-$'\037'$'\177']* ]] || fail "$label contains control characters"
  fi
  resolved_env_defined=1
  resolved_env_value="$val"
}
valid_tz_value() {
  local v="$1"
  [ -n "$v" ] && [ "${#v}" -le 128 ] || return 1
  [[ "$v" =~ ^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)*$ ]] || return 1
  [[ "$v" != .* ]] && [[ "$v" != */.* ]] && [[ "$v" != *..* ]]
}
valid_inherited_env_name() {
  local k="$1"
  [[ "$k" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "Invalid --inherit-env variable name: $k"
  case "$k" in
    PATH|HOME|USER|LOGNAME|SHELL|PWD|OLDPWD|TMPDIR|TZ|IFS|ENV|BASH_ENV|PORT|HOST|HOSTNAME|HTTP_ADDR|PANEL_PATH|LISTEN_ADDR|GO*|LD_*|DYLD_*|USAGE_*|CPA_*|CPAMP_*)
      fail "--inherit-env forbids reserved or internal variable: $k" ;;
  esac
}
resolved_restart_policy=''
read_restart_policy() {
  local raw name count rc=0
  raw="$(inspect "$1" '{{.HostConfig.RestartPolicy.Name}}:{{.HostConfig.RestartPolicy.MaximumRetryCount}}')" || rc=$?
  [ "$rc" -eq 0 ] || fail 'Cannot inspect container restart policy'
  name="${raw%%:*}"; count="${raw#*:}"
  case "$name" in
    ''|no) resolved_restart_policy=no ;;
    always) resolved_restart_policy=always ;;
    unless-stopped) resolved_restart_policy=unless-stopped ;;
    on-failure)
      if [ -z "$count" ] || [ "$count" = 0 ]; then
        resolved_restart_policy=on-failure
      elif [[ "$count" =~ ^[1-9][0-9]{0,5}$ ]]; then
        resolved_restart_policy="on-failure:$count"
      else
        fail "Unsupported container restart policy retry count: $raw"
      fi ;;
    *) fail "Unsupported container restart policy: $name" ;;
  esac
}
valid_restart_policy() {
  [[ "$1" =~ ^(no|always|unless-stopped|on-failure(:[1-9][0-9]{0,5})?)$ ]]
}
mounts() { inspect "$1" '{{range .Mounts}}{{printf "%s\t" .Type}}{{if .Name}}{{.Name}}{{else}}-{{end}}{{printf "\t"}}{{if .Source}}{{.Source}}{{else}}-{{end}}{{printf "\t%s\t%t\n" .Destination .RW}}{{end}}'; }
tmpfs_destinations() { inspect "$1" '{{range $dst,$opts := .HostConfig.Tmpfs}}{{println $dst}}{{end}}'; }
manager_port_signature() {
  inspect "$1" '{{if .HostConfig.PublishAllPorts}}publish-all;{{end}}{{range $p,$bindings := .HostConfig.PortBindings}}{{if eq $p "18317/tcp"}}{{range $bindings}}{{printf "%s:%s;" .HostIp .HostPort}}{{end}}{{end}}{{end}}'
}
mount_at() {
  local container="$1" destination="$2" mount_lines kind name source target rw found=0 matched_record=''
  mount_lines="$(mounts "$container")" || exit 1
  while IFS=$'\t' read -r kind name source target rw; do
    [ -n "$kind" ] || continue
    [ "$target" = "$destination" ] || continue
    [ "$found" = 0 ] || fail "Multiple mounts at $destination"
    found=1
    case "$rw" in true|false) ;; *) fail "Invalid mount RW state at $destination" ;; esac
    case "$kind" in
      volume) [ "$name" != - ] || fail "Invalid empty volume name at $destination"; mount_value volume "$name"; matched_record="volume"$'\t'"$name"$'\t'"$rw" ;;
      bind) [ "$source" != - ] || fail "Invalid empty bind source at $destination"; mount_value bind "$source"; [ "$source" != / ] || fail 'Root host mount is forbidden'; matched_record="bind"$'\t'"$source"$'\t'"$rw" ;;
      *) fail "Unsupported mount type $kind" ;;
    esac
  done <<< "$mount_lines"
  [ "$found" = 0 ] || printf '%s\n' "$matched_record"
}
resolve_mount() {
  local result rest rc=0
  result="$(mount_at "$1" "$2")" || rc=$?
  [ "$rc" -eq 0 ] || exit "$rc"
  [ -n "$result" ] || return 1
  resolved_type="${result%%$'\t'*}"
  rest="${result#*$'\t'}"
  resolved_source="${rest%%$'\t'*}"
  resolved_rw="${rest#*$'\t'}"
}
mount_arg() { printf 'type=%s,src=%s,dst=%s%s' "$1" "$2" "$3" "${4:-}"; }
active_probe_id=''
state_lock_dir=''
state_lock_held=0
state_dir_uncommitted=0
cleanup_mount_probe() {
  local pid="$active_probe_id"
  active_probe_id=''
  [ -n "$pid" ] || return 0
  if [ "$pid" != "${manager_id:-}" ] && [ "$pid" != "${cpa_id:-}" ] && [ "$pid" != "${target_id:-}" ] &&
     [ "$(label "$pid" dev.cpamp.upgrade.mount-probe 2>/dev/null || true)" = "${operation:-}" ] &&
     [ "$(inspect "$pid" '{{.State.Running}}' 2>/dev/null || true)" = false ]; then
    docker container rm "$pid" >/dev/null 2>&1 || true
  fi
}
remove_mount_probe() {
  local pid="$active_probe_id"
  active_probe_id=''
  [ -n "$pid" ] || return 0
  [ "$pid" != "${manager_id:-}" ] && [ "$pid" != "${cpa_id:-}" ] && [ "$pid" != "${target_id:-}" ] || fail 'Mount probe collided with service container'
  [ "$(label "$pid" dev.cpamp.upgrade.mount-probe)" = "$operation" ] || fail 'Mount probe label verification failed'
  [ "$(inspect "$pid" '{{.State.Running}}')" = false ] || fail 'Mount probe must never be started'
  docker container rm "$pid" >/dev/null
}
acquire_state_lock() {
  local holder_pid=''
  [ -n "${state_dir:-}" ] && [ ! -L "$state_dir" ] && [ -d "$state_dir" ] || fail "State directory is missing or invalid: ${state_dir:-}"
  state_lock_dir="$state_dir/.lock"
  [ ! -L "$state_lock_dir" ] || fail "State lock path must not be a symlink: $state_lock_dir"
  if ! mkdir -m 700 "$state_lock_dir" 2>/dev/null; then
    if [ ! -L "$state_lock_dir/pid" ]; then
      holder_pid="$(cat "$state_lock_dir/pid" 2>/dev/null || true)"
    fi
    [[ "$holder_pid" =~ ^[0-9]+$ ]] || holder_pid=''
    fail "Another upgrade or rollback operation${holder_pid:+ (PID $holder_pid)} already holds $state_lock_dir"
  fi
  state_lock_held=1
  printf '%s\n' "$$" > "$state_lock_dir/pid"
  chmod 600 "$state_lock_dir/pid"
}
check_unheld_state_lock() {
  local holder_pid='' candidate_lock="$state_dir/.lock"
  [ ! -L "$candidate_lock" ] || fail "State lock path must not be a symlink: $candidate_lock"
  if [ -e "$candidate_lock" ]; then
    if [ ! -L "$candidate_lock/pid" ]; then
      holder_pid="$(cat "$candidate_lock/pid" 2>/dev/null || true)"
    fi
    [[ "$holder_pid" =~ ^[0-9]+$ ]] || holder_pid=''
    fail "Another upgrade or rollback operation${holder_pid:+ (PID $holder_pid)} already holds $candidate_lock"
  fi
}
release_state_lock() {
  if [ "$state_lock_held" = 1 ] && [ -n "$state_lock_dir" ]; then
    if [ -d "$state_lock_dir" ] && [ ! -L "$state_lock_dir" ] && {
      [ "$(cat "$state_lock_dir/pid" 2>/dev/null || true)" = "$$" ] ||
      { [ ! -e "$state_lock_dir/pid" ] && [ ! -L "$state_lock_dir/pid" ]; }
    }; then
      rm -f "$state_lock_dir/pid" 2>/dev/null || true
      rmdir "$state_lock_dir" 2>/dev/null || true
    fi
    state_lock_held=0
  fi
  if [ "$state_dir_uncommitted" = 1 ] && [ -n "${state_dir:-}" ] && [ ! -L "$state_dir" ] && [ ! -e "$state_dir/manifest" ]; then
    rmdir "$state_dir" 2>/dev/null || true
    state_dir_uncommitted=0
  fi
}
cleanup_on_exit() {
  cleanup_mount_probe
  release_state_lock
}
trap cleanup_on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
volume_identity() {
  [ "$(docker volume inspect --format '{{with index .Labels "dev.cpamp.upgrade.operation"}}{{.}}{{end}}' "$1")" = "$operation" ] || fail "Volume belongs to another operation: $1"
}
ensure_volume() {
  if ! docker volume inspect "$1" >/dev/null 2>&1; then docker volume create --label "dev.cpamp.upgrade.operation=$operation" "$1" >/dev/null; fi
  volume_identity "$1"
}
assert_distinct_operation_volumes() {
  if [ "$source_type" = volume ]; then
    [ "$source_source" != "$target_volume" ] && [ "$source_source" != "$backup_volume" ] && [ "$source_source" != "$runtime_volume" ] || fail 'Source volume collides with target operation volumes'
  fi
  if [ "$cpa_auth_type" = volume ]; then
    [ "$cpa_auth_source" != "$target_volume" ] && [ "$cpa_auth_source" != "$backup_volume" ] && [ "$cpa_auth_source" != "$runtime_volume" ] || fail 'CPA auth volume collides with target operation volumes'
  fi
}
verify_existing_operation_volumes() {
  assert_distinct_operation_volumes
  local vol
  for vol in "$target_volume" "$backup_volume" "$runtime_volume"; do
    docker volume inspect "$vol" >/dev/null 2>&1 || fail "Missing required operation volume: $vol"
    volume_identity "$vol"
  done
}
target_mount_args=()
build_target_mounts() {
  target_mount_args=(
    --mount "type=volume,src=$target_volume,dst=/data"
    --mount "type=volume,src=$runtime_volume,dst=/runtime"
    --mount "type=volume,src=$backup_volume,dst=/upgrade/backup,readonly"
    --mount "$(mount_arg "$source_type" "$source_source" /upgrade/source ,readonly)"
  )
  if [ "$mode" = managed ]; then
    target_mount_args+=(--mount "$(mount_arg "$cpa_config_type" "$cpa_config_source" /upgrade/cpa/config.yaml ,readonly)")
    if [ -n "$cpa_auth_source" ]; then
      target_mount_args+=(--mount "$(mount_arg "$cpa_auth_type" "$cpa_auth_source" /upgrade/auths ,readonly)")
    fi
  fi
}
probe_daemon_bind_sources() {
  local probe_mode="$1" need_probe=0 probe_id
  target_source_source="$source_source"
  target_cpa_config_source=''
  target_cpa_auth_source=''
  if [ "$mode" = managed ]; then
    target_cpa_config_source="$cpa_config_source"
    target_cpa_auth_source="$cpa_auth_source"
  fi
  [ "$source_type" != bind ] || need_probe=1
  if [ "$mode" = managed ]; then
    [ "$cpa_config_type" != bind ] || [ -z "$cpa_config_source" ] || need_probe=1
    [ "$cpa_auth_type" != bind ] || [ -z "$cpa_auth_source" ] || need_probe=1
  fi
  if [ "$probe_mode" = resume ]; then
    verify_existing_operation_volumes
  fi
  [ "$need_probe" = 1 ] || return 0
  assert_distinct_operation_volumes
  case "$probe_mode" in
    init) ensure_volume "$target_volume"; ensure_volume "$backup_volume"; ensure_volume "$runtime_volume" ;;
    resume) ;;
    *) fail "Invalid mount probe mode: $probe_mode" ;;
  esac
  build_target_mounts
  local probe_args=(create --network none --label "dev.cpamp.upgrade.mount-probe=$operation" "${target_mount_args[@]}" --entrypoint /usr/local/bin/cpa-manager-plus "$image_id" upgrade --help)
  probe_id="$(docker "${probe_args[@]}")"
  [[ "$probe_id" =~ ^[a-f0-9]{64}$ ]] || fail 'Mount normalization probe did not return a valid container ID'
  active_probe_id="$probe_id"
  resolve_mount "$probe_id" /data || fail 'Mount probe missing /data'
  [ "$resolved_type:$resolved_source:$resolved_rw" = "volume:$target_volume:true" ] || fail 'Mount probe altered /data mount'
  resolve_mount "$probe_id" /runtime || fail 'Mount probe missing /runtime'
  [ "$resolved_type:$resolved_source:$resolved_rw" = "volume:$runtime_volume:true" ] || fail 'Mount probe altered /runtime mount'
  resolve_mount "$probe_id" /upgrade/backup || fail 'Mount probe missing /upgrade/backup'
  [ "$resolved_type:$resolved_source:$resolved_rw" = "volume:$backup_volume:false" ] || fail 'Mount probe altered /upgrade/backup mount'
  resolve_mount "$probe_id" /upgrade/source || fail 'Mount probe missing /upgrade/source'
  [ "$resolved_type:$resolved_rw" = "$source_type:false" ] || fail 'Mount probe altered /upgrade/source type or access mode'
  if [ "$source_type" = volume ]; then
    [ "$resolved_source" = "$source_source" ] || fail 'Mount probe altered /upgrade/source volume'
  fi
  target_source_source="$resolved_source"
  if [ "$mode" = managed ]; then
    if [ "$cpa_config_type" = bind ] && [ -n "$cpa_config_source" ]; then
      resolve_mount "$probe_id" /upgrade/cpa/config.yaml || fail 'Mount probe missing CPA config mount'
      [ "$resolved_type:$resolved_rw" = 'bind:false' ] || fail 'Mount probe altered CPA config type or access mode'
      target_cpa_config_source="$resolved_source"
    fi
    if [ -n "$cpa_auth_source" ]; then
      resolve_mount "$probe_id" /upgrade/auths || fail 'Mount probe missing CPA auth mount'
      [ "$resolved_type:$resolved_rw" = "$cpa_auth_type:false" ] || fail 'Mount probe altered CPA auth type or access mode'
      if [ "$cpa_auth_type" = volume ]; then
        [ "$resolved_source" = "$cpa_auth_source" ] || fail 'Mount probe altered CPA auth volume'
      fi
      target_cpa_auth_source="$resolved_source"
    fi
  fi
  remove_mount_probe
}
confirm() {
  local reply
  [ "$confirmed" = 1 ] && return
  [ -t 0 ] || fail 'Confirmation required; review the plan and pass --confirm'
  printf '%s ' "$1"; IFS= read -r reply
  [ "$reply" = "$2" ] || fail 'Not confirmed; no source service was stopped'
}

image=''; manager=''; source_destination=''; source_version=''; cpa=''; cpa_version=''
mode=''; extra_network=''; public_host=''; public_origin=''; env_public_origin="${CPAMP_PUBLIC_ORIGIN:-}"
inherited_env_names=''
state_dir=''; confirmed=0; rollback=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --confirm) confirmed=1; shift ;;
    --rollback) rollback=1; shift ;;
    --inherit-env)
      [ "$#" -ge 2 ] || fail "Missing value for $1"
      scalar "$1" "$2"
      valid_inherited_env_name "$2"
      if [[ ",$inherited_env_names," != *",$2,"* ]]; then
        inherited_env_names="${inherited_env_names:+$inherited_env_names,}$2"
      fi
      shift 2 ;;
    --image|--manager|--source-mount|--source-version|--cpa|--cpa-version|--mode|--network|--public-host|--public-origin|--state-dir)
      [ "$#" -ge 2 ] || fail "Missing value for $1"
      scalar "$1" "$2"
      case "$1" in
        --image) image="$2" ;; --manager) manager="$2" ;; --source-mount) source_destination="$2" ;;
        --source-version) source_version="$2" ;; --cpa) cpa="$2" ;; --cpa-version) cpa_version="$2" ;;
        --mode) mode="$2" ;; --network) extra_network="$2" ;; --public-host) public_host="$2" ;;
        --public-origin) public_origin="$2" ;; --state-dir) state_dir="$2" ;;
      esac
      shift 2 ;;
    *) fail "Unknown option: $1" ;;
  esac
done
command -v docker >/dev/null 2>&1 || fail 'Docker CLI is required'
[ -z "$image" ] || valid_image_ref "$image"
[ -z "$source_destination" ] || { mount_value source-mount "$source_destination"; source_destination="$(clean_container_path source-mount "$source_destination")"; [ "$source_destination" != / ] || fail 'source-mount cannot be /'; }
[ -z "$source_version" ] || release Manager "$source_version"
if [ -n "$cpa_version" ]; then release CPA "$cpa_version"; cpa_v8_supported "$cpa_version"; fi
[ -z "$mode" ] || case "$mode" in managed|external) ;; *) fail 'Mode must be managed or external' ;; esac
if [ -n "$extra_network" ]; then
  object_name network "$extra_network"
  case "$extra_network" in host|none) fail 'Host/none networking is unsupported; select a bridge-network deployment' ;; esac
fi
resolve_extra_network() {
  local raw="$1" resolved
  [ -n "$raw" ] || { resolved_extra_network=''; return 0; }
  object_name network "$raw"
  case "$raw" in host|none) fail 'Host/none networking is unsupported; select a bridge-network deployment' ;; esac
  resolved="$(docker network inspect --format '{{.Name}}' "$raw")" || fail "Docker network not found: $raw"
  object_name network "$resolved"
  case "$resolved" in host|none) fail 'Host/none networking is unsupported; select a bridge-network deployment' ;; esac
  resolved_extra_network="$resolved"
}
[ -z "$manager" ] || object_name Manager "$manager"
[ -z "$cpa" ] || object_name CPA "$cpa"
if [ -n "$public_host" ]; then
  normalize_public_host --public-host "$public_host"
  public_host="$normalized_public_host"
fi
if [ -n "$public_origin" ]; then
  normalize_public_origin --public-origin "$public_origin"
  public_origin="$normalized_origin"
fi
if [ -n "$env_public_origin" ]; then
  normalize_public_origin CPAMP_PUBLIC_ORIGIN "$env_public_origin"
  env_public_origin="$normalized_origin"
  if [ -n "$public_origin" ] && [ "$public_origin" != "$env_public_origin" ]; then
    fail 'Conflicting --public-origin and CPAMP_PUBLIC_ORIGIN values'
  fi
  public_origin="$env_public_origin"
fi
# The manifest is a data file, not an env file or a second migration journal.
# Docker labels bind these deployment resources; upgrade_operations remains the
# sole authority for data stages, gap decisions and completion.
keys=(operation manager_id manager_image source_destination source_type source_source target_source_source source_version manager_running manager_restart_policy cpa_id cpa_image cpa_running cpa_restart_policy cpa_version cpa_config_type cpa_config_source target_cpa_config_source cpa_auth_destination cpa_auth_type cpa_auth_source target_cpa_auth_source mode image_ref image_id image_revision image_version target_name target_id target_volume backup_volume runtime_volume host_ip host_ips host_port published_ports networks extra_network public_host public_origin manager_tz inherited_env_names phase)
write_state() {
  local key tmp="$state_dir/manifest.$$.new"
  [ ! -e "$tmp" ] && [ ! -L "$tmp" ] || fail 'Unexpected manifest staging file; preserve it and investigate'
  (
    set -o noclobber
    for key in "${keys[@]}"; do
      if [ "$has_target_mount_sources" = 0 ]; then
        case "$key" in
          target_source_source|target_cpa_config_source|target_cpa_auth_source) continue ;;
        esac
      fi
      if [ "$has_restart_policy_keys" = 0 ]; then
        case "$key" in
          manager_restart_policy|cpa_restart_policy) continue ;;
        esac
      fi
      printf '%s\t%s\n' "$key" "${!key}"
    done
  ) > "$tmp"
  mv "$tmp" "$state_dir/manifest"
  state_dir_uncommitted=0
}
has_target_mount_sources=0
has_restart_policy_keys=0
load_state() {
  local line key value allowed seen='|' target_mount_keys=0 restart_policy_keys=0
  [ -f "$state_dir/manifest" ] && [ ! -L "$state_dir/manifest" ] || fail 'State manifest is not a regular file'
  public_origin=''
  cpa_auth_destination=''
  manager_restart_policy=''; cpa_restart_policy=''
  host_ips=''; manager_tz=''; inherited_env_names=''
  target_source_source=''; target_cpa_config_source=''; target_cpa_auth_source=''
  has_target_mount_sources=0
  has_restart_policy_keys=0
  while IFS= read -r line || [ -n "$line" ]; do
    [[ "$line" == *$'\t'* ]] || fail 'Malformed manifest line'
    key="${line%%$'\t'*}"; value="${line#*$'\t'}"
    [ -n "$key" ] && [[ "$value" != *$'\t'* ]] || fail 'Malformed manifest column'
    allowed=0
    for candidate in "${keys[@]}"; do [ "$candidate" != "$key" ] || allowed=1; done
    [ "$allowed" = 1 ] || fail 'Unknown manifest key'
    [[ "$seen" != *"|$key|"* ]] || fail 'Repeated manifest key'
    seen="$seen$key|"
    [ -z "$value" ] || scalar "$key" "$value"
    printf -v "$key" '%s' "$value"
  done < "$state_dir/manifest"
  for key in "${keys[@]}"; do
    case "$key" in
      public_origin|cpa_auth_destination|target_source_source|target_cpa_config_source|target_cpa_auth_source|manager_restart_policy|cpa_restart_policy|host_ips|manager_tz|inherited_env_names) ;;
      *) [[ "$seen" == *"|$key|"* ]] || fail "Missing manifest key $key" ;;
    esac
  done
  for key in target_source_source target_cpa_config_source target_cpa_auth_source; do
    [[ "$seen" != *"|$key|"* ]] || target_mount_keys=$((target_mount_keys + 1))
  done
  if [ "$target_mount_keys" -eq 3 ]; then
    has_target_mount_sources=1
  elif [ "$target_mount_keys" -ne 0 ]; then
    fail 'Incomplete target mount identity keys in manifest'
  fi
  for key in manager_restart_policy cpa_restart_policy; do
    [[ "$seen" != *"|$key|"* ]] || restart_policy_keys=$((restart_policy_keys + 1))
  done
  if [ "$restart_policy_keys" -eq 2 ]; then
    has_restart_policy_keys=1
  elif [ "$restart_policy_keys" -ne 0 ]; then
    fail 'Incomplete restart policy keys in manifest'
  fi
  object_name operation "$operation"
  [ "$target_name" = "$operation" ] && [ "$target_volume" = "$operation-data" ] &&
    [ "$backup_volume" = "$operation-backup" ] && [ "$runtime_volume" = "$operation-runtime" ] || fail 'Manifest resource binding is invalid'
  valid_image_ref "$image_ref"
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid immutable image identity'
  [[ "$image_version" =~ $v2_release_re ]] || fail 'Invalid recorded v2 image version'
  [[ "$image_revision" =~ ^[a-f0-9]{40}$ ]] || fail 'Invalid recorded image revision'
  case "$mode" in managed|external) ;; *) fail 'Invalid recorded mode' ;; esac
  case "$phase" in prepared|stopped|preflighted|copied-in|reconcile-pending|reconciled|deployed|rolled-back) ;; *) fail 'Invalid recorded checkpoint' ;; esac
  [[ "$manager_id" =~ ^[a-f0-9]{64}$ ]] || fail 'Invalid recorded Manager identity'
  [[ "$manager_image" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid recorded Manager image identity'
  [ "$operation" = "cpamp-v2-${manager_id:0:16}" ] || fail 'Operation does not belong to recorded Manager'
  case "$manager_running" in true|false) ;; *) fail 'Invalid recorded Manager running state' ;; esac
  case "$cpa_running" in true|false) ;; *) fail 'Invalid recorded CPA running state' ;; esac
  if [ "$has_restart_policy_keys" = 1 ]; then
    valid_restart_policy "$manager_restart_policy" || fail 'Invalid recorded Manager restart policy'
    if [ -n "$cpa_id" ]; then
      valid_restart_policy "$cpa_restart_policy" || fail 'Invalid recorded CPA restart policy'
    else
      [ -z "$cpa_restart_policy" ] || fail 'Recorded CPA restart policy lacks CPA container identity'
    fi
  fi
  mount_value source-mount "$source_destination"
  source_destination="$(clean_container_path source-mount "$source_destination")"
  [ "$source_destination" != / ] || fail 'Invalid recorded source-mount'
  case "$source_type" in
    bind) mount_value source "$source_source"; [[ "$source_source" == /* ]] && [ "$source_source" != / ] || fail 'Invalid recorded source bind path' ;;
    volume) mount_value source "$source_source"; object_name source-volume "$source_source" ;;
    *) fail 'Invalid recorded source mount type' ;;
  esac
  if [ -n "$cpa_id" ]; then
    [[ "$cpa_id" =~ ^[a-f0-9]{64}$ ]] && [ "$cpa_id" != "$manager_id" ] || fail 'Invalid recorded CPA identity'
    [[ "$cpa_image" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid recorded CPA image identity'
  else
    [ -z "$cpa_image" ] && [ "$cpa_running" = false ] && [ -z "$cpa_config_type" ] &&
      [ -z "$cpa_config_source" ] && [ -z "$cpa_auth_destination" ] && [ -z "$cpa_auth_type" ] && [ -z "$cpa_auth_source" ] || fail 'Recorded CPA metadata lacks CPA container identity'
  fi
  if [ -n "$cpa_config_type" ] || [ -n "$cpa_config_source" ]; then
    [ "$cpa_config_type" = bind ] && [ -n "$cpa_config_source" ] || fail 'Invalid recorded CPA config mount'
    mount_value cpa-config "$cpa_config_source"
    [[ "$cpa_config_source" == /* ]] && [ "$cpa_config_source" != / ] || fail 'Invalid recorded CPA config path'
  fi
  if [ -n "$cpa_auth_type" ] || [ -n "$cpa_auth_source" ]; then
    [ -n "$cpa_auth_source" ] || fail 'Invalid recorded CPA auth mount'
    mount_value cpa-auth "$cpa_auth_source"
    case "$cpa_auth_type" in
      bind) [[ "$cpa_auth_source" == /* ]] && [ "$cpa_auth_source" != / ] || fail 'Invalid recorded CPA auth bind path' ;;
      volume) object_name cpa-auth-volume "$cpa_auth_source" ;;
      *) fail 'Invalid recorded CPA auth mount type' ;;
    esac
    if [[ "$seen" != *"|cpa_auth_destination|"* ]]; then
      cpa_auth_destination=/root/.cli-proxy-api
    fi
    case "$cpa_auth_destination" in
      /root/.cli-proxy-api|/root/auth-dir) ;;
      *) fail 'Invalid recorded CPA auth mount destination' ;;
    esac
  else
    [ -z "$cpa_auth_destination" ] || fail 'Recorded CPA auth destination lacks CPA auth mount source'
  fi
  [ -z "$source_version" ] || release Manager "$source_version"
  if [ -n "$cpa_version" ]; then release CPA "$cpa_version"; cpa_v8_supported "$cpa_version"; fi
  if [ "$mode" = managed ]; then
    [ -n "$cpa_id" ] && [ "$cpa_config_type" = bind ] && [ -n "$cpa_config_source" ] &&
      [ -n "$cpa_auth_type" ] && [ -n "$cpa_auth_source" ] && [ -n "$cpa_version" ] || fail 'Managed manifest lacks required CPA mounts or v8 version'
  fi
  if [ "$has_target_mount_sources" = 1 ]; then
    case "$source_type" in
      bind) mount_value target-source "$target_source_source"; [[ "$target_source_source" == /* ]] && [ "$target_source_source" != / ] || fail 'Invalid recorded target source bind path' ;;
      volume) [ "$target_source_source" = "$source_source" ] || fail 'Recorded target source volume differs' ;;
    esac
    if [ "$mode" = managed ]; then
      mount_value target-cpa-config "$target_cpa_config_source"
      [[ "$target_cpa_config_source" == /* ]] && [ "$target_cpa_config_source" != / ] || fail 'Invalid recorded target CPA config path'
      case "$cpa_auth_type" in
        bind) mount_value target-cpa-auth "$target_cpa_auth_source"; [[ "$target_cpa_auth_source" == /* ]] && [ "$target_cpa_auth_source" != / ] || fail 'Invalid recorded target CPA auth bind path' ;;
        volume) [ "$target_cpa_auth_source" = "$cpa_auth_source" ] || fail 'Recorded target CPA auth volume differs' ;;
      esac
    else
      [ -z "$target_cpa_config_source" ] && [ -z "$target_cpa_auth_source" ] || fail 'External manifest must not record target CPA mounts'
    fi
  else
    target_source_source="$source_source"
    if [ "$mode" = managed ]; then
      target_cpa_config_source="$cpa_config_source"
      target_cpa_auth_source="$cpa_auth_source"
    fi
  fi
  if [ -n "$target_id" ]; then
    [[ "$target_id" =~ ^[a-f0-9]{64}$ ]] && [ "$target_id" != "$manager_id" ] && [ "$target_id" != "$cpa_id" ] || fail 'Invalid recorded target identity'
  fi
  case "$phase" in
    prepared|stopped|preflighted|copied-in|reconcile-pending|reconciled) [ -z "$target_id" ] || fail 'Manifest records target container identity before reconciliation' ;;
    deployed) [ -n "$target_id" ] || fail 'Deployed manifest lacks target container identity' ;;
  esac
  [ -z "$host_ip" ] || [[ "$host_ip" =~ ^[A-Za-z0-9.:-]+$ ]] || fail 'Invalid recorded published IP'
  if [[ "$seen" != *"|host_ips|"* ]]; then
    if [ -z "$host_ip" ]; then
      host_ips='*'
    elif [ "$host_ip" = 0.0.0.0 ] || [ "$host_ip" = '::' ]; then
      host_ips="$host_ip"
    else
      local legacy_canon_ip
      legacy_canon_ip="$(canon_concrete_ip "$host_ip")" || fail 'Invalid recorded published IP'
      host_ip="$legacy_canon_ip"
      host_ips="$legacy_canon_ip"
    fi
  elif [ "$host_ips" = '*' ]; then
    [ -z "$host_ip" ] || [ "$host_ip" = 0.0.0.0 ] || fail 'Recorded host_ip disagrees with unspecified wildcard host_ips'
  elif [ "$host_ips" = 0.0.0.0 ]; then
    [ "$host_ip" = 0.0.0.0 ] || fail 'Recorded host_ip disagrees with IPv4 wildcard host_ips'
  elif [ "$host_ips" = '::' ]; then
    [ "$host_ip" = '::' ] || fail 'Recorded host_ip disagrees with IPv6 wildcard host_ips'
  elif [ "$host_ips" = '0.0.0.0,::' ]; then
    [ "$host_ip" = 0.0.0.0 ] || fail 'Recorded host_ip disagrees with dual-stack wildcard host_ips'
  else
    local recorded_ip canon_rec_ip seen_recorded_ips=',' first_recorded_ip=''
    IFS=, read -r -a recorded_ip_list <<< "$host_ips"
    [ "${#recorded_ip_list[@]}" -gt 0 ] || fail 'Invalid empty recorded host_ips'
    for recorded_ip in "${recorded_ip_list[@]}"; do
      [ -n "$recorded_ip" ] || fail 'Invalid empty IP in recorded host_ips'
      [ "$recorded_ip" != 0.0.0.0 ] && [ "$recorded_ip" != '::' ] && [ "$recorded_ip" != '*' ] ||
        fail 'Recorded host_ips mixes wildcard and concrete IPs'
      canon_rec_ip="$(canon_concrete_ip "$recorded_ip")" || fail 'Invalid recorded concrete IP in host_ips'
      [ "$canon_rec_ip" = "$recorded_ip" ] || fail 'Non-canonical IP in recorded host_ips'
      [[ "$seen_recorded_ips" != *",$recorded_ip,"* ]] || fail 'Duplicate IP in recorded host_ips'
      seen_recorded_ips="${seen_recorded_ips}${recorded_ip},"
      [ -n "$first_recorded_ip" ] || first_recorded_ip="$recorded_ip"
    done
    [ "$host_ip" = "$first_recorded_ip" ] || fail 'Recorded host_ip disagrees with primary entry in host_ips'
  fi
  [ -z "$manager_tz" ] || valid_tz_value "$manager_tz" || fail 'Invalid recorded Manager TZ'
  if [ -n "$inherited_env_names" ]; then
    local env_item seen_env_items=','
    IFS=, read -r -a env_item_list <<< "$inherited_env_names"
    for env_item in "${env_item_list[@]}"; do
      [ -n "$env_item" ] || fail 'Invalid empty entry in recorded inherited_env_names'
      valid_inherited_env_name "$env_item"
      [[ "$seen_env_items" != *",$env_item,"* ]] || fail 'Duplicate entry in recorded inherited_env_names'
      seen_env_items="${seen_env_items}${env_item},"
    done
  fi
  [[ "$host_port" =~ ^[0-9]+$ ]] && [ "$host_port" -gt 0 ] && [ "$host_port" -le 65535 ] || fail 'Invalid recorded published port'
  [ -n "$published_ports" ] || fail 'Missing recorded published port bindings'
  if [ -n "$extra_network" ]; then
    object_name network "$extra_network"
    case "$extra_network" in host|none) fail 'Host/none networking is unsupported; select a bridge-network deployment' ;; esac
  fi
  if [ -n "$public_host" ]; then
    normalize_public_host manifest-public-host "$public_host"
    [ "$normalized_public_host" = "$public_host" ] || fail 'Non-canonical recorded public-host'
  fi
  if [ -n "$public_origin" ]; then
    normalize_public_origin manifest-public-origin "$public_origin"
    [ "$normalized_origin" = "$public_origin" ] || fail 'Non-canonical recorded public-origin'
  fi
}
canonical_paths_overlap() {
  local state_candidate="$1" bind_candidate="$2" canon_s canon_b
  [ -n "$state_candidate" ] && [ -n "$bind_candidate" ] || return 1
  canon_s="$(canonical_path "$state_candidate")" || exit 1
  canon_b="$(canonical_path "$bind_candidate")" || exit 1
  paths_overlap "$canon_s" "$canon_b"
}
bind_overlaps_state_dir() {
  local canon_state="$1" raw_bind="$2" prefix alias_path drive rest state_alias wsl_drive_re='^/([a-zA-Z])(/.*)?$'
  [ -n "$raw_bind" ] || return 1
  if canonical_paths_overlap "$canon_state" "$raw_bind"; then return 0; fi
  # Conservative fail-closed rejection only (never used as mount identity):
  # if a VM-backed daemon reports a host-mount namespace prefix, also reject
  # overlap against stripped local host and WSL drive paths in either direction.
  for prefix in /host_mnt /run/desktop/mnt/host; do
    case "$raw_bind" in
      "$prefix"/*)
        alias_path="${raw_bind#"$prefix"}"
        if [ -n "$alias_path" ] && [ "$alias_path" != / ]; then
          if canonical_paths_overlap "$canon_state" "$alias_path"; then return 0; fi
          if [[ "$alias_path" =~ $wsl_drive_re ]]; then
            drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr 'A-Z' 'a-z')"
            rest="${BASH_REMATCH[2]:-}"
            if canonical_paths_overlap "$canon_state" "/mnt/${drive}${rest}"; then return 0; fi
          fi
        fi ;;
    esac
    case "$canon_state" in
      "$prefix"/*)
        state_alias="${canon_state#"$prefix"}"
        if [ -n "$state_alias" ] && [ "$state_alias" != / ]; then
          if canonical_paths_overlap "$state_alias" "$raw_bind"; then return 0; fi
        fi ;;
    esac
  done
  return 1
}
check_state_dir_isolation() {
  local canon_state
  canon_state="$(canonical_path "$state_dir")"
  [ "$canon_state" != / ] || fail 'State directory cannot be /'
  if [ "$source_type" = bind ]; then
    if bind_overlaps_state_dir "$canon_state" "$source_source" ||
       bind_overlaps_state_dir "$canon_state" "${target_source_source:-}"; then
      fail "State directory ($state_dir) overlaps original Manager source bind path ($source_source); run outside the source directory or choose an isolated --state-dir"
    fi
  fi
  if [ "$cpa_config_type" = bind ] && [ -n "$cpa_config_source" ]; then
    if bind_overlaps_state_dir "$canon_state" "$cpa_config_source" ||
       bind_overlaps_state_dir "$canon_state" "${target_cpa_config_source:-}"; then
      fail "State directory ($state_dir) overlaps original CPA config bind path ($cpa_config_source); choose an isolated --state-dir"
    fi
  fi
  if [ "$cpa_auth_type" = bind ] && [ -n "$cpa_auth_source" ]; then
    if bind_overlaps_state_dir "$canon_state" "$cpa_auth_source" ||
       bind_overlaps_state_dir "$canon_state" "${target_cpa_auth_source:-}"; then
      fail "State directory ($state_dir) overlaps original CPA auth bind path ($cpa_auth_source); run outside the CPA auth directory or choose an isolated --state-dir"
    fi
  fi
}
resolve_ui_origin() {
  local ui_host context
  if [ -n "$public_origin" ]; then
    printf '%s\n' "$public_origin"
    return 0
  fi
  ui_host="${public_host:-$host_ip}"
  case "$ui_host" in 0.0.0.0|::|'')
    context="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
    case "$context" in
      unix://*|npipe://*) ui_host=localhost ;;
      *) fail 'Remote Docker endpoint needs --public-host or --public-origin for a reachable browser URL' ;;
    esac ;;
  esac
  [[ "$ui_host" != *:* ]] || ui_host="[$ui_host]"
  printf 'http://%s:%s\n' "$ui_host" "$host_port"
}
check_source_identity() {
  [ "$(inspect "$manager_id" '{{.Id}}' 2>/dev/null || true)" = "$manager_id" ] || fail 'Original Manager identity changed'
  [ "$(inspect "$manager_id" '{{.Image}}' 2>/dev/null || true)" = "$manager_image" ] || fail 'Original Manager image changed'
  resolve_mount "$manager_id" "$source_destination" || fail 'Original Manager data mount disappeared'
  [ "$resolved_type:$resolved_source" = "$source_type:$source_source" ] || fail 'Original source mount changed'
  [ "$(manager_port_signature "$manager_id")" = "$published_ports" ] || fail 'Original Manager published port changed'
  if [ -n "$cpa_id" ]; then
    [ "$(inspect "$cpa_id" '{{.Id}}' 2>/dev/null || true)" = "$cpa_id" ] && [ "$(inspect "$cpa_id" '{{.Image}}' 2>/dev/null || true)" = "$cpa_image" ] || fail 'Original CPA identity changed'
    if [ -n "$cpa_config_source" ]; then
      resolve_mount "$cpa_id" /CLIProxyAPI/config.yaml || fail 'Original CPA config mount disappeared'
      [ "$resolved_type:$resolved_source" = "$cpa_config_type:$cpa_config_source" ] || fail 'Original CPA config mount changed'
    fi
    if [ -n "$cpa_auth_source" ]; then
      resolve_mount "$cpa_id" "${cpa_auth_destination:-/root/.cli-proxy-api}" || fail 'Original CPA auth mount disappeared'
      [ "$resolved_type:$resolved_source" = "$cpa_auth_type:$cpa_auth_source" ] || fail 'Original CPA auth mount changed'
    fi
  fi
}
verify_original_stopped_posture() {
  local context_mode="$1"
  if [ "$manager_running" = false ]; then
    [ "$(inspect "$manager_id" '{{.State.Running}}')" = false ] || fail 'Original Manager was recorded as stopped and is now running'
  elif [ "$context_mode" = active_upgrade ]; then
    [ "$(inspect "$manager_id" '{{.State.Running}}')" = false ] || fail 'Original Manager is running alongside an active upgrade state'
  fi
  if [ -n "$cpa_id" ] && [ "$mode" = managed ]; then
    if [ "$cpa_running" = false ]; then
      [ "$(inspect "$cpa_id" '{{.State.Running}}')" = false ] || fail 'Original CPA was recorded as stopped and is now running'
    elif [ "$context_mode" = active_upgrade ]; then
      [ "$(inspect "$cpa_id" '{{.State.Running}}')" = false ] || fail 'Original CPA is running alongside an active managed upgrade state'
    fi
  fi
}
verify_original_restart_policies() {
  local expectation="$1"
  [ "$has_restart_policy_keys" = 1 ] || return 0
  read_restart_policy "$manager_id"
  case "$expectation" in
    exact_recorded)
      [ "$resolved_restart_policy" = "$manager_restart_policy" ] || fail 'Original Manager restart policy changed unexpectedly' ;;
    transition_allowed)
      if [ "$manager_restart_policy" = always ]; then
        case "$resolved_restart_policy" in always|no) ;; *) fail 'Original Manager restart policy changed unexpectedly' ;; esac
      else
        [ "$resolved_restart_policy" = "$manager_restart_policy" ] || fail 'Original Manager restart policy changed unexpectedly'
      fi ;;
    neutralized)
      if [ "$manager_restart_policy" = always ]; then
        [ "$resolved_restart_policy" = no ] || fail 'Original Manager restart policy was not cleared from always'
      else
        [ "$resolved_restart_policy" = "$manager_restart_policy" ] || fail 'Original Manager restart policy changed unexpectedly'
      fi ;;
    *) fail "Internal restart policy verification mode error: $expectation" ;;
  esac
  if [ "$mode" = managed ] && [ -n "$cpa_id" ]; then
    read_restart_policy "$cpa_id"
    case "$expectation" in
      exact_recorded)
        [ "$resolved_restart_policy" = "$cpa_restart_policy" ] || fail 'Original CPA restart policy changed unexpectedly' ;;
      transition_allowed)
        if [ "$cpa_restart_policy" = always ]; then
          case "$resolved_restart_policy" in always|no) ;; *) fail 'Original CPA restart policy changed unexpectedly' ;; esac
        else
          [ "$resolved_restart_policy" = "$cpa_restart_policy" ] || fail 'Original CPA restart policy changed unexpectedly'
        fi ;;
      neutralized)
        if [ "$cpa_restart_policy" = always ]; then
          [ "$resolved_restart_policy" = no ] || fail 'Original CPA restart policy was not cleared from always'
        else
          [ "$resolved_restart_policy" = "$cpa_restart_policy" ] || fail 'Original CPA restart policy changed unexpectedly'
        fi ;;
    esac
  fi
}
hydrate_legacy_restart_policies_for_resume() {
  if [ "$has_restart_policy_keys" = 0 ]; then
    read_restart_policy "$manager_id"
    manager_restart_policy="$resolved_restart_policy"
    if [ -n "$cpa_id" ]; then
      read_restart_policy "$cpa_id"
      cpa_restart_policy="$resolved_restart_policy"
    else
      cpa_restart_policy=''
    fi
    has_restart_policy_keys=1
    write_state
  fi
}
neutralize_always_restart_policies() {
  hydrate_legacy_restart_policies_for_resume
  if [ "$manager_restart_policy" = always ]; then
    read_restart_policy "$manager_id"
    if [ "$resolved_restart_policy" = always ]; then
      docker update --restart no "$manager_id" >/dev/null
    fi
    read_restart_policy "$manager_id"
    [ "$resolved_restart_policy" = no ] || fail 'Original Manager restart policy could not be cleared from always'
  fi
  if [ "$mode" = managed ] && [ -n "$cpa_id" ] && [ "$cpa_restart_policy" = always ]; then
    read_restart_policy "$cpa_id"
    if [ "$resolved_restart_policy" = always ]; then
      docker update --restart no "$cpa_id" >/dev/null
    fi
    read_restart_policy "$cpa_id"
    [ "$resolved_restart_policy" = no ] || fail 'Original CPA restart policy could not be cleared from always'
  fi
}
restore_always_restart_policies() {
  [ "$has_restart_policy_keys" = 1 ] || return 0
  if [ "$mode" = managed ] && [ -n "$cpa_id" ] && [ "$cpa_restart_policy" = always ]; then
    read_restart_policy "$cpa_id"
    if [ "$resolved_restart_policy" != always ]; then
      docker update --restart always "$cpa_id" >/dev/null
    fi
    read_restart_policy "$cpa_id"
    [ "$resolved_restart_policy" = always ] || fail 'Original CPA restart policy could not be restored to always'
  fi
  if [ "$manager_restart_policy" = always ]; then
    read_restart_policy "$manager_id"
    if [ "$resolved_restart_policy" != always ]; then
      docker update --restart always "$manager_id" >/dev/null
    fi
    read_restart_policy "$manager_id"
    [ "$resolved_restart_policy" = always ] || fail 'Original Manager restart policy could not be restored to always'
  fi
}
if [ -z "$state_dir" ]; then
  if [ -z "$manager" ]; then
    candidates="$(docker ps -aq --filter label=com.docker.compose.service=cpa-manager-plus)"
    count=0
    while IFS= read -r candidate; do
      [ -n "$candidate" ] || continue
      [ -z "$(label "$candidate" dev.cpamp.upgrade.operation)" ] || continue
      manager="$candidate"; count=$((count + 1))
    done <<< "$candidates"
    [ "$count" = 1 ] || fail 'Select --manager explicitly: no unique old Compose Manager was found'
  fi
  selected_id="$(inspect "$manager" '{{.Id}}')"
  [[ "$selected_id" =~ ^[a-f0-9]{64}$ ]] || fail 'Invalid Manager container identity'
  state_dir="$(pwd -P)/.cpamp-v2-${selected_id:0:16}"
fi
req_image="$image"; req_manager="$manager"; req_source_destination="$source_destination"
req_source_version="$source_version"; req_cpa="$cpa"; req_cpa_version="$cpa_version"
req_mode="$mode"; req_extra_network="$extra_network"; req_public_host="$public_host"
req_public_origin="$public_origin"; req_inherited_env_names="$inherited_env_names"
if [ -n "$state_dir" ]; then
  mount_value state-dir "$state_dir"
  [ ! -L "$state_dir" ] || fail 'State directory must not be a symlink'
fi
case "$state_dir" in /*) ;; *) state_dir="$(pwd -P)/$state_dir" ;; esac
resume=0
persist_public_host_update=0
if [ -n "$state_dir" ] && [ -e "$state_dir/manifest" ]; then
  # State is operator-owned. Reject group/world-writable or foreign state before locking.
  [ -d "$state_dir" ] && [ ! -L "$state_dir" ] || fail 'State directory must be an unlinked directory'
  state_access="$(ls -ldn "$state_dir")"
  read -r permissions links owner rest <<< "$state_access"
  [ "$owner" = "$(id -u)" ] && [[ "$permissions" == drwx------* ]] || fail 'State directory must be owned by this user with mode 0700'
  if [ "$rollback" = 1 ]; then
    check_unheld_state_lock
  else
    acquire_state_lock
  fi
  load_state; resume=1
  check_state_dir_isolation
  if [ -n "$req_manager" ]; then
    [ "$(inspect "$req_manager" '{{.Id}}' 2>/dev/null || true)" = "$manager_id" ] || fail 'Conflicting --manager for existing state directory'
  fi
  if [ -n "$req_cpa" ]; then
    [ -n "$cpa_id" ] && [ "$(inspect "$req_cpa" '{{.Id}}' 2>/dev/null || true)" = "$cpa_id" ] || fail 'Conflicting --cpa for existing state directory'
  fi
  if [ -n "$req_image" ]; then
    [ "$(docker image inspect --format '{{.Id}}' "$req_image" 2>/dev/null || true)" = "$image_id" ] || fail 'Conflicting --image for existing state directory'
  fi
  [ -z "$req_source_destination" ] || [ "$req_source_destination" = "$source_destination" ] || fail 'Conflicting --source-mount for existing state directory'
  [ -z "$req_source_version" ] || [ "$req_source_version" = "$source_version" ] || fail 'Conflicting --source-version for existing state directory'
  [ -z "$req_cpa_version" ] || [ "$req_cpa_version" = "$cpa_version" ] || fail 'Conflicting --cpa-version for existing state directory'
  [ -z "$req_mode" ] || [ "$req_mode" = "$mode" ] || fail 'Conflicting --mode for existing state directory'
  if [ "$rollback" = 0 ] && [ -n "$extra_network" ]; then
    resolve_extra_network "$extra_network"
    if [ "$resolved_extra_network" != "$extra_network" ]; then
      extra_network="$resolved_extra_network"
      persist_public_host_update=1
    fi
  fi
  if [ -n "$req_extra_network" ]; then
    if [ -z "$extra_network" ]; then
      fail 'Conflicting --network for existing state directory'
    elif [ "$req_extra_network" != "$extra_network" ]; then
      [ "$(docker network inspect --format '{{.Name}}' "$req_extra_network" 2>/dev/null || true)" = "$extra_network" ] || fail 'Conflicting --network for existing state directory'
    fi
  fi
  [ -z "$req_public_origin" ] || [ "$req_public_origin" = "$public_origin" ] || fail 'Conflicting --public-origin for existing state directory'
  [ -z "$req_inherited_env_names" ] || [ "$req_inherited_env_names" = "$inherited_env_names" ] || fail 'Conflicting --inherit-env for existing state directory'
  if [ -n "$req_public_host" ]; then
    if [ "$rollback" = 1 ]; then
      [ "$req_public_host" = "$public_host" ] || fail 'Conflicting --public-host for existing state directory'
    elif [ -n "$public_host" ] && [ "$req_public_host" != "$public_host" ]; then
      fail 'Conflicting --public-host for existing state directory'
    elif [ -z "$public_host" ]; then
      public_host="$req_public_host"
      persist_public_host_update=1
    fi
  fi
  if [ "$rollback" = 0 ]; then
    ui_origin="$(resolve_ui_origin)"
  fi
else
  [ "$rollback" = 0 ] || fail 'Rollback requires the original --state-dir'
  [ -n "$image" ] || fail 'Choose an explicit v2 image with --image'
  valid_image_ref "$image"
  if [ -n "$extra_network" ]; then
    resolve_extra_network "$extra_network"
    extra_network="$resolved_extra_network"
  fi
  if [ -z "$manager" ]; then
    candidates="$(docker ps -aq --filter label=com.docker.compose.service=cpa-manager-plus)"
    count=0
    while IFS= read -r candidate; do
      [ -n "$candidate" ] || continue
      # Existing v2 operation containers are never rediscovered as old sources.
      [ -z "$(label "$candidate" dev.cpamp.upgrade.operation)" ] || continue
      manager="$candidate"; count=$((count + 1))
    done <<< "$candidates"
    [ "$count" = 1 ] || fail 'Select --manager explicitly: no unique old Compose Manager was found'
  fi
  scalar manager "$manager"
  manager_id="$(inspect "$manager" '{{.Id}}')"; manager_image="$(inspect "$manager_id" '{{.Image}}')"
  [[ "$manager_id" =~ ^[a-f0-9]{64}$ ]] || fail 'Invalid Manager container identity'
  [[ "$manager_image" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid Manager image identity'
  [ -z "$(label "$manager_id" dev.cpamp.upgrade.operation)" ] || fail 'Selected Manager is already a v2 upgrade deployment'
  manager_running="$(inspect "$manager_id" '{{.State.Running}}')"
  case "$manager_running" in true|false) ;; *) fail 'Invalid Manager running state' ;; esac
  read_restart_policy "$manager_id"
  manager_restart_policy="$resolved_restart_policy"
  has_restart_policy_keys=1
  env_scalar_value 'Original Manager TZ' "$manager_id" TZ
  if [ "$resolved_env_defined" = 1 ]; then
    valid_tz_value "$resolved_env_value" || fail 'Invalid TZ value on original Manager'
    manager_tz="$resolved_env_value"
  else
    manager_tz=''
  fi
  if [ -n "$inherited_env_names" ]; then
    IFS=, read -r -a inherited_check_list <<< "$inherited_env_names"
    for inherited_name in "${inherited_check_list[@]}"; do
      env_scalar_value "--inherit-env $inherited_name" "$manager_id" "$inherited_name"
      [ "$resolved_env_defined" = 1 ] || fail "Original Manager does not define --inherit-env variable: $inherited_name"
    done
  fi
  [ -z "$(env_value "$manager_id" CPA_MANAGER_CONFIG)" ] || fail 'Config-file Manager paths (CPA_MANAGER_CONFIG) cannot be verified safely from Docker metadata'
  [ -z "$(env_value "$manager_id" CPA_MANAGER_DATA_KEY_FILE)" ] && ! env_nonempty "$manager_id" CPA_MANAGER_DATA_KEY || fail 'External Manager data-key secret is unsupported; source requires colocated usage.sqlite and data.key'
  manager_mount_lines="$(mounts "$manager_id")"
  while IFS=$'\t' read -r kind name src destination rw; do
    [ -n "$kind" ] || continue
    [ "$destination" = / ] || destination="${destination%/}"
    case "$destination" in
      */config.json) fail 'Mounted Manager config.json paths cannot be verified safely from Docker metadata' ;;
      /run/secrets|/run/secrets/cpa_data_key|/run/secrets/cpa_data_key/*) fail 'External Manager data-key secret mount is unsupported; source requires colocated usage.sqlite and data.key' ;;
    esac
  done <<< "$manager_mount_lines"
  manager_tmpfs_lines="$(tmpfs_destinations "$manager_id")"
  while IFS= read -r tmpfs_dst; do
    [ -n "$tmpfs_dst" ] || continue
    [ "$tmpfs_dst" = / ] || tmpfs_dst="${tmpfs_dst%/}"
    case "$tmpfs_dst" in
      */config.json) fail 'Mounted Manager config.json paths cannot be verified safely from Docker metadata' ;;
      /run/secrets|/run/secrets/cpa_data_key|/run/secrets/cpa_data_key/*) fail 'External Manager data-key secret mount is unsupported; source requires colocated usage.sqlite and data.key' ;;
    esac
  done <<< "$manager_tmpfs_lines"
  data_dir="$(env_value "$manager_id" USAGE_DATA_DIR)"
  [ -z "$data_dir" ] || data_dir="$(clean_container_path USAGE_DATA_DIR "$data_dir")"
  db_path="$(env_value "$manager_id" USAGE_DB_PATH)"
  [ -z "$db_path" ] || db_path="$(clean_container_path USAGE_DB_PATH "$db_path")"
  db_path="${db_path:-${data_dir:-${source_destination:-/data}}/usage.sqlite}"
  data_dir="${data_dir:-${db_path%/*}}"
  key_path="$(env_value "$manager_id" CPA_MANAGER_DATA_KEY_PATH)"
  [ -z "$key_path" ] || key_path="$(clean_container_path CPA_MANAGER_DATA_KEY_PATH "$key_path")"
  key_path="${key_path:-$data_dir/data.key}"
  [[ "$data_dir" = /* ]] && [ "$data_dir" != / ] && [[ "$db_path" = /* ]] && [[ "$key_path" = /* ]] || fail 'Source paths must be absolute container paths'
  [ "$data_dir" = "${db_path%/*}" ] || fail 'Archives/data directory differs from database root; no partial migration is permitted'
  [ "${db_path##*/}" = usage.sqlite ] && [ "${key_path##*/}" = data.key ] && [ "${db_path%/*}" = "${key_path%/*}" ] || fail 'Source requires colocated usage.sqlite and data.key; select a supported deployment layout'
  source_destination="${source_destination:-${db_path%/*}}"
  [ "$source_destination" = "${db_path%/*}" ] || fail 'source-mount must match the configured database/key parent'
  resolve_mount "$manager_id" "$source_destination" || fail 'No exact persisted source mount; do not mount a broad host parent'
  source_type="$resolved_type"; source_source="$resolved_source"
  # Child mounts or tmpfs overlays could hide archives or credential members in the old view.
  while IFS=$'\t' read -r kind name src destination rw; do
    [ -n "$kind" ] || continue
    [ "$destination" = / ] || destination="${destination%/}"
    [[ "$destination" != "$source_destination/"* ]] || fail 'Nested source mounts need an explicit unified offline source; no partial copy is permitted'
  done <<< "$manager_mount_lines"
  while IFS= read -r tmpfs_dst; do
    [ -n "$tmpfs_dst" ] || continue
    [ "$tmpfs_dst" = / ] || tmpfs_dst="${tmpfs_dst%/}"
    [ "$tmpfs_dst" != "$source_destination" ] || fail 'Source data directory cannot be backed by tmpfs'
    [[ "$tmpfs_dst" != "$source_destination/"* ]] || fail 'Nested source mounts need an explicit unified offline source; no partial copy is permitted'
  done <<< "$manager_tmpfs_lines"
  if [ -z "$source_version" ]; then
    observed="$(image_label "$manager_image" org.opencontainers.image.version)"
    if [[ "$observed" =~ $manager_v1_14_re ]]; then source_version="v${observed#[vV]}"; fi
  fi
  networks="$(inspect "$manager_id" '{{range $name,$network := .NetworkSettings.Networks}}{{println $name}}{{end}}')"
  project="$(label "$manager_id" com.docker.compose.project)"
  if [ -z "$cpa" ] && [ -n "$project" ]; then
    candidates="$(docker ps -aq --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=cli-proxy-api)"
    count=0
    while IFS= read -r candidate; do [ -z "$candidate" ] || { cpa="$candidate"; count=$((count + 1)); }; done <<< "$candidates"
    [ "$count" -le 1 ] || fail 'Select --cpa explicitly: associated Compose CPA is ambiguous'
  fi
  cpa_id=''; cpa_image=''; cpa_running=false; cpa_restart_policy=''; cpa_config_type=''; cpa_config_source=''
  cpa_auth_destination=''; cpa_auth_type=''; cpa_auth_source=''
  if [ -n "$cpa" ]; then
    cpa_id="$(inspect "$cpa" '{{.Id}}')"; cpa_image="$(inspect "$cpa_id" '{{.Image}}')"; cpa_running="$(inspect "$cpa_id" '{{.State.Running}}')"
    [[ "$cpa_id" =~ ^[a-f0-9]{64}$ ]] && [[ "$cpa_image" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Invalid CPA container identity'
    case "$cpa_running" in true|false) ;; *) fail 'Invalid CPA running state' ;; esac
    read_restart_policy "$cpa_id"
    cpa_restart_policy="$resolved_restart_policy"
    [ "$cpa_id" != "$manager_id" ] || fail 'CPA cannot be the Manager container'
    if [ "$mode" != external ]; then
      if resolve_mount "$cpa_id" /CLIProxyAPI/config.yaml; then
        if [ "$resolved_type" = bind ]; then cpa_config_type="$resolved_type"; cpa_config_source="$resolved_source"; fi
      fi
      has_default_cpa_auth=0; default_cpa_auth_type=''; default_cpa_auth_source=''
      has_tilde_cpa_auth=0; tilde_cpa_auth_type=''; tilde_cpa_auth_source=''
      if resolve_mount "$cpa_id" /root/.cli-proxy-api; then
        has_default_cpa_auth=1; default_cpa_auth_type="$resolved_type"; default_cpa_auth_source="$resolved_source"
      fi
      if resolve_mount "$cpa_id" /root/auth-dir; then
        has_tilde_cpa_auth=1; tilde_cpa_auth_type="$resolved_type"; tilde_cpa_auth_source="$resolved_source"
      fi
      if [ "$has_default_cpa_auth" = 1 ] && [ "$has_tilde_cpa_auth" = 1 ]; then
        fail 'CPA container mounts both /root/.cli-proxy-api and /root/auth-dir (~/auth-dir); retain only the active CPA auth mount or pass --mode external'
      elif [ "$has_default_cpa_auth" = 1 ]; then
        cpa_auth_destination=/root/.cli-proxy-api; cpa_auth_type="$default_cpa_auth_type"; cpa_auth_source="$default_cpa_auth_source"
      elif [ "$has_tilde_cpa_auth" = 1 ]; then
        cpa_auth_destination=/root/auth-dir; cpa_auth_type="$tilde_cpa_auth_type"; cpa_auth_source="$tilde_cpa_auth_source"
      fi
      if [ -n "$cpa_auth_destination" ]; then
        cpa_mount_lines="$(mounts "$cpa_id")"
        while IFS=$'\t' read -r kind name src destination rw; do
          [ -n "$kind" ] || continue
          [ "$destination" = / ] || destination="${destination%/}"
          [[ "$destination" != "$cpa_auth_destination/"* ]] || fail 'Nested CPA auth mounts are unsupported; select a unified CPA auth directory or --mode external'
        done <<< "$cpa_mount_lines"
        cpa_tmpfs_lines="$(tmpfs_destinations "$cpa_id")"
        while IFS= read -r tmpfs_dst; do
          [ -n "$tmpfs_dst" ] || continue
          [ "$tmpfs_dst" = / ] || tmpfs_dst="${tmpfs_dst%/}"
          [ "$tmpfs_dst" != "$cpa_auth_destination" ] && [[ "$tmpfs_dst" != "$cpa_auth_destination/"* ]] ||
            fail 'CPA auth directory cannot be backed or overlaid by tmpfs'
        done <<< "$cpa_tmpfs_lines"
      fi
    fi
    if [ -z "$cpa_version" ]; then
      observed="$(image_label "$cpa_image" org.opencontainers.image.version)"
      if [[ "$observed" =~ $semver_re ]]; then cpa_version="$observed"; fi
      if [ -z "$cpa_version" ]; then
        observed_ref="$(inspect "$cpa_id" '{{.Config.Image}}')"
        if tag_release="$(cpa_image_tag_release "$observed_ref")"; then cpa_version="$tag_release"; fi
      fi
    fi
  fi
  if [ -z "$mode" ]; then
    if [ -n "$cpa_config_source" ] && [ -n "$cpa_auth_source" ]; then mode=managed; else mode=external; fi
  fi
  if [ "$mode" = managed ]; then
    [ -n "$cpa_config_source" ] && [ -n "$cpa_auth_source" ] || fail 'Managed import needs exact CPA config.yaml and auth mounts; select --cpa with those mounts or --mode external'
  fi
  if [ -n "$cpa_version" ]; then
    cpa_v8_supported "$cpa_version"
  elif [ "$mode" = managed ]; then
    fail 'CPA release is unknown; supply --cpa-version with the actual observed v8 release (v8.0.0 or later v8)'
  fi
  bindings="$(inspect "$manager_id" '{{range $p,$bindings := .NetworkSettings.Ports}}{{if eq $p "18317/tcp"}}{{range $bindings}}{{printf "%s\t%s\n" .HostIp .HostPort}}{{end}}{{end}}{{end}}')"
  if [ -z "$bindings" ]; then
    bindings="$(inspect "$manager_id" '{{range $p,$bindings := .HostConfig.PortBindings}}{{if eq $p "18317/tcp"}}{{range $bindings}}{{printf "%s\t%s\n" .HostIp .HostPort}}{{end}}{{end}}{{end}}')"
  fi
  host_ip=''; host_ips=''; host_port=''
  has_wildcard_unspecified=0; has_wildcard_v4=0; has_wildcard_v6=0; has_concrete_ip=0
  while IFS= read -r binding_line; do
    [ -n "$binding_line" ] || continue
    ip="${binding_line%%$'\t'*}"
    port="${binding_line#*$'\t'}"
    [ -n "$port" ] || continue
    if [ -z "$host_port" ]; then host_port="$port"
    elif [ "$port" != "$host_port" ]; then fail 'Multiple different published Manager ports require an explicit supported deployment'; fi
    ip="${ip#[}"; ip="${ip%]}"
    case "$ip" in
      '') has_wildcard_unspecified=1 ;;
      0.0.0.0) has_wildcard_v4=1 ;;
      '::') has_wildcard_v6=1 ;;
      *)
        has_concrete_ip=1
        canon_ip="$(canon_concrete_ip "$ip")" || fail "Invalid published IP binding on Manager: $ip"
        if [[ ",${host_ips}," != *",$canon_ip,"* ]]; then
          host_ips="${host_ips:+$host_ips,}$canon_ip"
        fi ;;
    esac
  done <<< "$bindings"
  [[ "$host_port" =~ ^[0-9]+$ ]] && [ "$host_port" -gt 0 ] && [ "$host_port" -le 65535 ] || fail 'Old Manager has no concrete published 18317/tcp port'
  if [ "$has_concrete_ip" = 1 ] && { [ "$has_wildcard_unspecified" = 1 ] || [ "$has_wildcard_v4" = 1 ] || [ "$has_wildcard_v6" = 1 ]; }; then
    fail 'Mixed wildcard and concrete IP bindings on 18317/tcp are unsupported'
  fi
  if [ "$has_concrete_ip" = 0 ]; then
    if [ "$has_wildcard_v4" = 1 ] && [ "$has_wildcard_v6" = 1 ]; then
      host_ip=0.0.0.0
      host_ips='0.0.0.0,::'
    elif [ "$has_wildcard_v4" = 1 ]; then
      host_ip=0.0.0.0
      host_ips=0.0.0.0
    elif [ "$has_wildcard_v6" = 1 ]; then
      host_ip='::'
      host_ips='::'
    else
      host_ip=''
      host_ips='*'
    fi
  else
    host_ip="${host_ips%%,*}"
  fi
  published_ports="$(manager_port_signature "$manager_id")"
  [ -n "$published_ports" ] || fail 'Old Manager has no recorded 18317/tcp port configuration'
  ui_origin="$(resolve_ui_origin)"
  operation="cpamp-v2-${manager_id:0:16}"
  target_name="$operation"; target_id=''; target_volume="$operation-data"; backup_volume="$operation-backup"; runtime_volume="$operation-runtime"
  assert_distinct_operation_volumes
  state_dir="${state_dir:-$(pwd -P)/.$operation}"
  check_state_dir_isolation
  [ ! -e "$state_dir" ] && [ ! -L "$state_dir" ] || fail "State directory exists without a manifest; preserve it: $state_dir"
  state_parent="${state_dir%/*}"; state_parent="${state_parent:-/}"
  [ -d "$state_parent" ] || fail "State directory parent does not exist: $state_parent"
  ! docker inspect "$target_name" >/dev/null 2>&1 || fail "Target container $target_name already exists without a state manifest; preserve it and use its original --state-dir"
  for vol in "$target_volume" "$backup_volume" "$runtime_volume"; do
    if docker volume inspect "$vol" >/dev/null 2>&1; then volume_identity "$vol"; fi
  done
  image_ref="$image"; image_id=''; image_version=''; image_revision=''
  if docker image inspect --format '{{.Id}}' "$image" >/dev/null 2>&1 &&
     { [[ "$image" =~ ^sha256:[a-f0-9]{64}$ ]] || [[ "$image" == *@sha256:* ]]; }; then
    image_id="$(docker image inspect --format '{{.Id}}' "$image")"
    image_version="$(image_label "$image_id" org.opencontainers.image.version)"; image_revision="$(image_label "$image_id" org.opencontainers.image.revision)"
    [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Image has no immutable content identity'
    [[ "$image_version" =~ $v2_release_re ]] || fail 'Image must identify a v2 Manager release'
    [[ "$image_revision" =~ ^[a-f0-9]{40}$ ]] || fail 'Image lacks a full source revision'
  elif [[ "$image" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    fail "Pinned local image digest is not locally available: $image"
  fi
  target_source_source="$source_source"
  target_cpa_config_source="$cpa_config_source"
  target_cpa_auth_source="$cpa_auth_source"
  phase=prepared
fi

# Newline-separated networks are encoded as a comma list in the manifest.
# Network names cannot contain comma or whitespace.
network_list=''
seen_networks=','
while IFS= read -r network; do
  [ -n "$network" ] || continue
  object_name network "$network"
  case "$network" in host|none) fail 'Host/none networking is unsupported; select a bridge-network deployment' ;; esac
  [[ "$seen_networks" != *",$network,"* ]] || continue
  seen_networks="$seen_networks$network,"
  network_list="${network_list:+$network_list,}$network"
done <<< "${networks//,/$'\n'}"
networks="$network_list"
[ -n "$networks" ] || networks=bridge
display_networks="$networks"
if [ -n "$extra_network" ] && [[ ",$networks," != *",$extra_network,"* ]]; then
  display_networks="$networks,$extra_network"
fi
# Bind rollback to durable HostConfig, not ephemeral NetworkSettings that Docker
# may clear after stop. The selected actual allocated host port is saved above.
check_source_identity
say "Original Manager: $manager_id ($manager_image)"
say "Source: $source_type:$source_source -> /upgrade/source:ro; Manager release: ${source_version:-unknown (engine must qualify source family)}"
if [ -n "$image_id" ]; then
  say "Target: $image_id; release $image_version; source revision $image_revision"
else
  say "Target: $image_ref (will pull and verify v2 release identity after confirmation, before stopping any source service)"
fi
say "Mode: $mode; associated CPA: ${cpa_id:-not identified (external connection must be proven in UI)}; observed release: ${cpa_version:-unknown}"
say "Will stop only original Manager (clearing --restart always until explicit rollback); CPA is stopped only for managed mode. Source data remains intact."
say "Separate new volumes: $target_volume, $backup_volume (verified bundle), $runtime_volume"
say "Published endpoint: ${host_ips:-${host_ip:-0.0.0.0}}:$host_port; public origin: ${public_origin:-request host (local HTTP)}; networks: $display_networks"
say "Private deployment state: $state_dir/manifest (no credentials); no automatic rollback after possible v2 writes."

target_identity() {
  local ref actual_id mount_lines kind name src dst rw destination expected expected_rw
  local target_data_dir target_db_path target_key_path target_runtime_dir target_ports
  if [ -n "$target_id" ]; then
    docker inspect "$target_id" >/dev/null 2>&1 || fail 'Recorded target container disappeared; preserve v2 volumes and inspect'
    [ "$(inspect "$target_name" '{{.Id}}' 2>/dev/null || true)" = "$target_id" ] || fail 'Target container name does not resolve to recorded target identity'
    ref="$target_id"
  else
    docker inspect "$target_name" >/dev/null 2>&1 || fail 'Target container is missing'
    ref="$target_name"
  fi
  [ "$(label "$ref" dev.cpamp.upgrade.operation)" = "$operation" ] &&
    [ "$(inspect "$ref" '{{.Image}}')" = "$image_id" ] || fail 'Target container identity differs from this operation'
  actual_id="$(inspect "$ref" '{{.Id}}')"
  [[ "$actual_id" =~ ^[a-f0-9]{64}$ ]] && [ "$actual_id" != "$manager_id" ] && [ "$actual_id" != "$cpa_id" ] || fail 'Invalid target container ID'
  [ -z "$target_id" ] || [ "$actual_id" = "$target_id" ] || fail 'Target container ID changed; refusing a different object'
  [ "$(inspect "$ref" '{{.HostConfig.Privileged}}')" = false ] || fail 'Target container must not run privileged'
  case "$(inspect "$ref" '{{.HostConfig.NetworkMode}}')" in host|none) fail 'Target container network mode was modified' ;; esac
  [ -z "$(tmpfs_destinations "$ref")" ] || fail 'Unexpected target tmpfs mount'
  volume_identity "$target_volume"; volume_identity "$backup_volume"; volume_identity "$runtime_volume"
  if [ "$source_type" = volume ]; then
    [ "$source_source" != "$target_volume" ] && [ "$source_source" != "$backup_volume" ] && [ "$source_source" != "$runtime_volume" ] || fail 'Source volume collides with target operation volumes'
  fi
  if [ "$cpa_auth_type" = volume ]; then
    [ "$cpa_auth_source" != "$target_volume" ] && [ "$cpa_auth_source" != "$backup_volume" ] && [ "$cpa_auth_source" != "$runtime_volume" ] || fail 'CPA auth volume collides with target operation volumes'
  fi
  mount_lines="$(mounts "$ref")"
  while IFS=$'\t' read -r kind name src dst rw; do
    [ -n "$kind" ] || continue
    if [ "$dst" = /legacy ] || [[ "$dst" == /legacy/* ]]; then fail 'Target container must not mount /legacy'; fi
    case "$dst" in
      /data|/runtime|/upgrade/backup|/upgrade/source) ;;
      /upgrade/cpa/config.yaml|/upgrade/auths) [ "$mode" = managed ] || fail "Unexpected target mount in external mode: $dst" ;;
      *) fail "Unexpected target mount: $dst" ;;
    esac
  done <<< "$mount_lines"
  for destination in /data /runtime /upgrade/backup /upgrade/source; do
    case "$destination" in
      /data) expected="volume:$target_volume"; expected_rw=true ;;
      /runtime) expected="volume:$runtime_volume"; expected_rw=true ;;
      /upgrade/backup) expected="volume:$backup_volume"; expected_rw=false ;;
      /upgrade/source) expected="$source_type:$target_source_source"; expected_rw=false ;;
    esac
    resolve_mount "$ref" "$destination" || fail "Target mount missing: $destination"
    [ "$resolved_type:$resolved_source" = "$expected" ] || fail "Target mount changed: $destination"
    [ "$resolved_rw" = "$expected_rw" ] || fail "Target mount access mode changed: $destination"
  done
  if [ "$mode" = managed ]; then
    resolve_mount "$ref" /upgrade/cpa/config.yaml || fail 'Target CPA config mount is missing'
    [ "$resolved_type:$resolved_source" = "$cpa_config_type:$target_cpa_config_source" ] || fail 'Target CPA config mount changed'
    [ "$resolved_rw" = false ] || fail 'Target CPA config mount must be read-only'
    resolve_mount "$ref" /upgrade/auths || fail 'Target CPA auth mount is missing'
    [ "$resolved_type:$resolved_source" = "$cpa_auth_type:$target_cpa_auth_source" ] || fail 'Target CPA auth mount changed'
    [ "$resolved_rw" = false ] || fail 'Target CPA auth mount must be read-only'
  fi
  [ "$(env_value "$ref" CPAMP_UPGRADE_WORKER_SOCKET)" = /run/cpamp-upgrade/worker.sock ] || fail 'Target worker socket env differs'
  [ "$(env_value "$ref" CPAMP_UPGRADE_SOURCE_ROOT)" = /upgrade/source ] || fail 'Target upgrade source env differs'
  [ -z "$(env_value "$ref" CPAMP_LEGACY_ROOT)" ] && ! env_nonempty "$ref" CPAMP_LEGACY_ROOT || fail 'Target must not set CPAMP_LEGACY_ROOT'
  [ "$(env_value "$ref" CPAMP_PUBLIC_ORIGIN)" = "$public_origin" ] || fail 'Target CPAMP_PUBLIC_ORIGIN env differs from recorded public-origin'
  [ "$(env_value "$ref" CPAMP_UPGRADE_CPA_VERSION)" = "$cpa_version" ] || fail 'Target CPAMP_UPGRADE_CPA_VERSION env differs'
  if [ "$mode" = managed ]; then
    [ "$(env_value "$ref" CPAMP_UPGRADE_CPA_ROOT)" = /upgrade/cpa ] || fail 'Target CPAMP_UPGRADE_CPA_ROOT env differs'
    [ "$(env_value "$ref" CPAMP_UPGRADE_CPA_AUTH_ROOT)" = /upgrade/auths ] || fail 'Target CPAMP_UPGRADE_CPA_AUTH_ROOT env differs'
  else
    [ -z "$(env_value "$ref" CPAMP_UPGRADE_CPA_ROOT)" ] && [ -z "$(env_value "$ref" CPAMP_UPGRADE_CPA_AUTH_ROOT)" ] || fail 'External target must not set managed CPA roots'
  fi
  target_data_dir="$(env_value "$ref" USAGE_DATA_DIR)"
  [ -z "$target_data_dir" ] || [ "$target_data_dir" = /data ] || fail 'Target USAGE_DATA_DIR env was modified'
  target_db_path="$(env_value "$ref" USAGE_DB_PATH)"
  [ -z "$target_db_path" ] || [ "$target_db_path" = /data/usage.sqlite ] || fail 'Target USAGE_DB_PATH env was modified'
  target_key_path="$(env_value "$ref" CPA_MANAGER_DATA_KEY_PATH)"
  [ -z "$target_key_path" ] || [ "$target_key_path" = /data/data.key ] || fail 'Target CPA_MANAGER_DATA_KEY_PATH env was modified'
  target_runtime_dir="$(env_value "$ref" CPAMP_RUNTIME_DATA_DIR)"
  [ -z "$target_runtime_dir" ] || [ "$target_runtime_dir" = /runtime ] || fail 'Target CPAMP_RUNTIME_DATA_DIR env was modified'
  [ -z "$(env_value "$ref" CPA_MANAGER_CONFIG)" ] || fail 'Target CPA_MANAGER_CONFIG env was modified'
  local target_cpa_upstream
  target_cpa_upstream="$(env_value "$ref" CPA_UPSTREAM_URL)"
  [ -z "$target_cpa_upstream" ] || [ "$target_cpa_upstream" = http://127.0.0.1:8317 ] || fail 'Target CPA_UPSTREAM_URL env was modified'
  [ -z "$(env_value "$ref" HTTP_ADDR)" ] && [ -z "$(env_value "$ref" PANEL_PATH)" ] &&
    [ -z "$(env_value "$ref" CPA_MANAGEMENT_KEY_FILE)" ] &&
    ! env_nonempty "$ref" CPA_MANAGEMENT_KEY || fail 'Target runtime listener or CPA override env was modified'
  env_scalar_value 'Target TZ' "$ref" TZ
  if [ -n "$manager_tz" ]; then
    [ "$resolved_env_defined" = 1 ] && [ "$resolved_env_value" = "$manager_tz" ] || fail 'Target TZ env differs from recorded timezone'
  else
    [ "$resolved_env_defined" = 0 ] || fail 'Target unexpectedly defines TZ'
  fi
  if [ -n "$inherited_env_names" ]; then
    local inherited_name expected_inherited_val
    IFS=, read -r -a target_inherited_list <<< "$inherited_env_names"
    for inherited_name in "${target_inherited_list[@]}"; do
      env_scalar_value "--inherit-env $inherited_name" "$manager_id" "$inherited_name"
      [ "$resolved_env_defined" = 1 ] || fail "Original Manager no longer defines inherited env: $inherited_name"
      expected_inherited_val="$resolved_env_value"
      env_scalar_value "Target $inherited_name" "$ref" "$inherited_name"
      [ "$resolved_env_defined" = 1 ] || fail "Target container missing inherited env: $inherited_name"
      [ "$resolved_env_value" = "$expected_inherited_val" ] || fail "Target inherited env differs from original Manager: $inherited_name"
    done
  fi
  local raw_target_ports tp_line tp_rest tp_proto tp_ip tp_port tp_canon tp_tuple
  local expected_binding_set=',' seen_actual_set=',' expected_binding_count=0 actual_binding_count=0 expected_ip
  IFS=, read -r -a expected_ip_list <<< "${host_ips:-*}"
  for expected_ip in "${expected_ip_list[@]}"; do
    tp_tuple="18317/tcp=${expected_ip:-*}:$host_port"
    [[ "$expected_binding_set" != *",$tp_tuple,"* ]] || continue
    expected_binding_set="${expected_binding_set}${tp_tuple},"
    expected_binding_count=$((expected_binding_count + 1))
  done
  raw_target_ports="$(inspect "$ref" '{{range $p,$bindings := .HostConfig.PortBindings}}{{range $bindings}}{{printf "%s\t%s\t%s\n" $p .HostIp .HostPort}}{{end}}{{end}}')"
  while IFS= read -r tp_line; do
    [ -n "$tp_line" ] || continue
    tp_proto="${tp_line%%$'\t'*}"
    tp_rest="${tp_line#*$'\t'}"
    tp_ip="${tp_rest%%$'\t'*}"
    tp_port="${tp_rest#*$'\t'}"
    tp_ip="${tp_ip#[}"; tp_ip="${tp_ip%]}"
    case "$tp_ip" in
      '') tp_canon='*' ;;
      0.0.0.0|'::') tp_canon="$tp_ip" ;;
      *) tp_canon="$(canon_concrete_ip "$tp_ip")" || fail 'Target container has invalid published IP binding' ;;
    esac
    tp_tuple="${tp_proto}=${tp_canon}:${tp_port}"
    [[ "$seen_actual_set" != *",$tp_tuple,"* ]] || fail 'Duplicate target published port binding'
    [[ "$expected_binding_set" == *",$tp_tuple,"* ]] || fail 'Target published port binding differs from recorded endpoint'
    seen_actual_set="${seen_actual_set}${tp_tuple},"
    actual_binding_count=$((actual_binding_count + 1))
  done <<< "$raw_target_ports"
  [ "$actual_binding_count" -eq "$expected_binding_count" ] || fail 'Target published port binding differs from recorded endpoint'
  target_id="$actual_id"
}
ensure_networks() {
  local network connected
  [ -n "$target_id" ] || fail 'Missing recorded target container identity'
  IFS=, read -r -a attach_networks <<< "$networks"
  if [ -n "$extra_network" ] && [[ ",$networks," != *",$extra_network,"* ]]; then
    attach_networks+=("$extra_network")
  fi
  for network in "${attach_networks[@]}"; do
    connected="$(inspect "$target_id" "{{with index .NetworkSettings.Networks \"$network\"}}yes{{end}}")"
    [ "$connected" = yes ] || docker network connect "$network" "$target_id"
  done
}
if [ "$rollback" = 1 ]; then
  check_source_identity
  case "$phase" in
    prepared|rolled-back) verify_original_stopped_posture recorded_only ;;
    *) verify_original_stopped_posture active_upgrade ;;
  esac
  verify_original_restart_policies transition_allowed
  has_rollback_target=0
  if [ -n "$target_id" ] || docker inspect "$target_name" >/dev/null 2>&1; then
    has_rollback_target=1
    case "$phase" in
      reconcile-pending|reconciled|deployed|rolled-back) ;;
      *) fail 'Target container already exists before offline migration completed; preserve state and inspect' ;;
    esac
    if [ "$has_target_mount_sources" = 1 ]; then
      target_identity
    fi
  fi
  say 'ROLLBACK discards all v2-only writes from the active service view; v2 volumes/backup remain untouched.'
  confirm 'Type ROLLBACK to stop v2 and restart the unchanged originals:' ROLLBACK
  acquire_state_lock
  load_state
  check_state_dir_isolation
  check_source_identity
  case "$phase" in
    prepared|rolled-back) verify_original_stopped_posture recorded_only ;;
    *) verify_original_stopped_posture active_upgrade ;;
  esac
  verify_original_restart_policies transition_allowed
  if [ "$has_rollback_target" = 1 ] || [ -n "$target_id" ] || docker inspect "$target_name" >/dev/null 2>&1; then
    case "$phase" in
      reconcile-pending|reconciled|deployed|rolled-back) ;;
      *) fail 'Target container already exists before offline migration completed; preserve state and inspect' ;;
    esac
    if [ "$has_target_mount_sources" = 0 ]; then
      [ "$(docker image inspect --format '{{.Id}}' "$image_id")" = "$image_id" ] || fail 'Pinned target image is not locally available; restore that exact image'
      probe_daemon_bind_sources resume
      has_target_mount_sources=1
      check_state_dir_isolation
    fi
    target_identity
    if [ "$(inspect "$target_id" '{{.State.Running}}')" = true ]; then
      docker stop --time 150 "$target_id" >/dev/null
    fi
    [ "$(inspect "$target_id" '{{.State.Running}}')" = false ] || fail 'Target container did not stop cleanly'
  fi
  # Persist the no-resume boundary before an original service can write again.
  phase=rolled-back; write_state
  restore_always_restart_policies
  if [ "$mode" = managed ] && [ "$cpa_running" = true ] && [ "$(inspect "$cpa_id" '{{.State.Running}}')" != true ]; then
    docker start "$cpa_id" >/dev/null
  fi
  if [ "$manager_running" = true ] && [ "$(inspect "$manager_id" '{{.State.Running}}')" != true ]; then
    docker start "$manager_id" >/dev/null
  fi
  verify_original_stopped_posture recorded_only
  verify_original_restart_policies exact_recorded
  say 'Original objects restarted only if previously running. Do not resume migration after original writes; use a new qualified operation.'
  say "Target volume: $target_volume"
  say "Backup volume: $backup_volume"
  say "Runtime volume: $runtime_volume"
  exit 0
fi
[ "$phase" != rolled-back ] || fail 'Original service may have written after rollback; this migration cannot resume'
if [ "$resume" = 0 ]; then
  confirm 'Type UPGRADE to stop the identified source and prepare the new deployment:' UPGRADE
  if [[ ! "$image" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    say "Prefetching explicit image $image before stopping any source service."
    docker pull "$image"
  fi
  image_ref="$image"; image_id="$(docker image inspect --format '{{.Id}}' "$image")"
  image_version="$(image_label "$image_id" org.opencontainers.image.version)"; image_revision="$(image_label "$image_id" org.opencontainers.image.revision)"
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'Image has no immutable content identity'
  [[ "$image_version" =~ $v2_release_re ]] || fail 'Image must identify a v2 Manager release'
  [[ "$image_revision" =~ ^[a-f0-9]{40}$ ]] || fail 'Image lacks a full source revision'
  if [ -n "$extra_network" ]; then
    resolve_extra_network "$extra_network"
    extra_network="$resolved_extra_network"
  fi
  [ "$(docker image inspect --format '{{.Id}}' "$image_id")" = "$image_id" ] || fail 'Pinned target image is not locally available; restore that exact image'
  check_source_identity
  verify_original_stopped_posture recorded_only
  verify_original_restart_policies exact_recorded
  mkdir -m 700 "$state_dir" || fail "State directory already exists; pass that exact --state-dir without conflicting discovery flags to resume: $state_dir"
  state_dir_uncommitted=1
  acquire_state_lock
  probe_daemon_bind_sources init
  has_target_mount_sources=1
  check_state_dir_isolation
  write_state
else
  if [ -n "$extra_network" ]; then
    resolve_extra_network "$extra_network"
    extra_network="$resolved_extra_network"
  fi
  [ "$(docker image inspect --format '{{.Id}}' "$image_id")" = "$image_id" ] || fail 'Pinned target image is not locally available; restore that exact image'
  if [ "$persist_public_host_update" = 1 ]; then write_state; fi
fi
if [ -n "$target_id" ] || docker inspect "$target_name" >/dev/null 2>&1; then
  case "$phase" in
    reconcile-pending|reconciled|deployed) ;;
    *) fail 'Target container already exists before offline migration completed; preserve state and inspect' ;;
  esac
  need_manifest_sync=0
  if [ "$has_target_mount_sources" = 0 ]; then
    probe_daemon_bind_sources resume
    has_target_mount_sources=1
    check_state_dir_isolation
    need_manifest_sync=1
  fi
  target_identity
  if [ "$phase" != deployed ] || [ "$need_manifest_sync" = 1 ]; then
    phase=deployed; write_state
  fi
  ensure_networks
  verify_original_stopped_posture active_upgrade
  verify_original_restart_policies transition_allowed
  neutralize_always_restart_policies
  verify_original_restart_policies neutralized
  verify_original_stopped_posture active_upgrade
  docker start "$target_id" >/dev/null
else
  [ "$phase" != deployed ] && [ -z "$target_id" ] || fail 'Recorded target disappeared; preserve v2 volumes and recover the original deployment, do not repeat migration'
  check_source_identity
  if [ "$phase" = prepared ]; then
    verify_original_stopped_posture recorded_only
    verify_original_restart_policies transition_allowed
    neutralize_always_restart_policies
    if [ "$manager_running" = true ] && [ "$(inspect "$manager_id" '{{.State.Running}}')" = true ]; then
      docker stop --time 150 "$manager_id" >/dev/null
    fi
    [ "$(inspect "$manager_id" '{{.State.Running}}')" = false ] || fail 'Original Manager did not stop cleanly'
    if [ "$mode" = managed ]; then
      if [ "$cpa_running" = true ] && [ "$(inspect "$cpa_id" '{{.State.Running}}')" = true ]; then
        docker stop --time 150 "$cpa_id" >/dev/null
      fi
      [ "$(inspect "$cpa_id" '{{.State.Running}}')" = false ] || fail 'Original CPA did not stop cleanly'
    fi
    verify_original_stopped_posture active_upgrade
    verify_original_restart_policies neutralized
    phase=stopped; write_state
  else
    verify_original_stopped_posture active_upgrade
    verify_original_restart_policies transition_allowed
    neutralize_always_restart_policies
    verify_original_restart_policies neutralized
    verify_original_stopped_posture active_upgrade
  fi
  assert_distinct_operation_volumes
  ensure_volume "$target_volume"; ensure_volume "$backup_volume"; ensure_volume "$runtime_volume"
  common=(--rm --network none --user 0:0 --mount "$(mount_arg "$source_type" "$source_source" /upgrade/source ,readonly)" --mount "type=volume,src=$target_volume,dst=/data" --mount "type=volume,src=$backup_volume,dst=/upgrade/backup" --env USAGE_DB_PATH=/data/usage.sqlite --env CPA_MANAGER_DATA_KEY_PATH=/data/data.key --entrypoint /usr/local/bin/cpa-manager-plus "$image_id")
  version_args=(); [ -z "$source_version" ] || version_args=(--source-version "$source_version")
  # No acknowledgement flags: any decision belongs to the authenticated UI.
  if [ "$phase" = stopped ]; then
    docker run "${common[@]}" upgrade preflight --legacy /upgrade/source --target-dir /data ${version_args[@]+"${version_args[@]}"}
    phase=preflighted; write_state
  fi
  docker run "${common[@]}" upgrade run --legacy /upgrade/source --target /data --bundle /upgrade/backup/bundle ${version_args[@]+"${version_args[@]}"}
  if [ "$phase" = preflighted ]; then phase=copied-in; write_state; fi
  reconcile_code=0
  reconcile_json="$(docker run "${common[@]}" upgrade reconcile --legacy /upgrade/source --target /data --bundle /upgrade/backup/bundle ${version_args[@]+"${version_args[@]}"} --json)" || reconcile_code=$?
  reconcile_error_code=''
  reconcile_code_re='"errorCode"[[:space:]]*:[[:space:]]*"([^"]+)"'
  reconcile_ready_re='"ready"[[:space:]]*:[[:space:]]*true'
  reconcile_recorded_true_re='"recorded"[[:space:]]*:[[:space:]]*true'
  reconcile_recorded_false_re='"recorded"[[:space:]]*:[[:space:]]*false'
  if [[ "$reconcile_json" =~ $reconcile_code_re ]]; then
    reconcile_error_code="${BASH_REMATCH[1]}"
  fi
  if [ "$reconcile_code" -eq 0 ]; then
    [[ "$reconcile_json" =~ $reconcile_ready_re ]] &&
      [[ "$reconcile_json" =~ $reconcile_recorded_true_re ]] &&
      [ -z "$reconcile_error_code" ] ||
      fail 'Reconcile returned exit 0 without a recorded ready report; originals remain stopped, inspect before resuming'
    phase=reconciled; write_state
  elif [ "$reconcile_code" -eq 1 ] &&
       [ "$reconcile_error_code" = upgrade_reconcile_mismatch ] &&
       [[ "$reconcile_json" =~ $reconcile_ready_re ]] &&
       [[ "$reconcile_json" =~ $reconcile_recorded_false_re ]]; then
    say 'Reconciliation requires a recorded decision (upgrade_reconcile_mismatch). Opening Upgrade-only UI; no gaps have been silently accepted.'
    phase=reconcile-pending; write_state
  else
    fail "Reconcile refused (${reconcile_error_code:-exit $reconcile_code}); originals remain stopped, resume the same state after correction"
  fi
  if [ "$has_target_mount_sources" = 0 ]; then
    probe_daemon_bind_sources resume
    has_target_mount_sources=1
    check_state_dir_isolation
    write_state
  fi
  publish_args=()
  if [ "${host_ips:-*}" = '*' ]; then
    publish_args=(--publish "$host_port:18317")
  else
    IFS=, read -r -a publish_ip_list <<< "$host_ips"
    for publish_ip in "${publish_ip_list[@]}"; do
      [[ "$publish_ip" != *:* ]] || publish_ip="[$publish_ip]"
      publish_args+=(--publish "${publish_ip}:$host_port:18317")
    done
  fi
  extra_env_args=()
  [ -z "$manager_tz" ] || extra_env_args+=(--env "TZ=$manager_tz")
  if [ -n "$inherited_env_names" ]; then
    IFS=, read -r -a create_inherited_list <<< "$inherited_env_names"
    for inherited_name in "${create_inherited_list[@]}"; do
      env_scalar_value "--inherit-env $inherited_name" "$manager_id" "$inherited_name"
      [ "$resolved_env_defined" = 1 ] || fail "Original Manager no longer defines --inherit-env variable: $inherited_name"
      extra_env_args+=(--env "$inherited_name=$resolved_env_value")
    done
  fi
  build_target_mounts
  deployment=(create --name "$target_name" --label "dev.cpamp.upgrade.operation=$operation" --restart unless-stopped --stop-timeout 150
    "${publish_args[@]}" "${target_mount_args[@]}"
    --env CPAMP_UPGRADE_WORKER_SOCKET=/run/cpamp-upgrade/worker.sock --env CPAMP_UPGRADE_SOURCE_ROOT=/upgrade/source
    --add-host host.docker.internal:host-gateway)
  if [ "$mode" = managed ]; then
    deployment+=(--env CPAMP_UPGRADE_CPA_ROOT=/upgrade/cpa)
    if [ -n "$cpa_auth_source" ]; then deployment+=(--env CPAMP_UPGRADE_CPA_AUTH_ROOT=/upgrade/auths); fi
  fi
  [ -z "$cpa_version" ] || deployment+=(--env "CPAMP_UPGRADE_CPA_VERSION=$cpa_version")
  [ -z "$public_origin" ] || deployment+=(--env "CPAMP_PUBLIC_ORIGIN=$public_origin")
  if [ "${#extra_env_args[@]}" -gt 0 ]; then
    deployment+=("${extra_env_args[@]}")
  fi
  IFS=, read -r -a attach_networks <<< "$networks"
  deployment+=(--network "${attach_networks[0]}" "$image_id")
  created_id="$(docker "${deployment[@]}")"
  [[ "$created_id" =~ ^[a-f0-9]{64}$ ]] || fail 'Docker create did not return a valid container identity'
  target_id="$created_id"; phase=deployed; write_state
  target_identity
  ensure_networks
  docker start "$target_id" >/dev/null
fi
# Bounded readiness observation, not an operation retry. No admin key is needed.
ready=0; ready_surface=''; active_control_path=''
target_json_re='"target"[[:space:]]*:[[:space:]]*"([^"]*)"'
ctrl_json_re='"controlBasePath"[[:space:]]*:[[:space:]]*"([^"]*)"'
committed_true_re='"committed"[[:space:]]*:[[:space:]]*true'
routing_committed_re='"routingCommitted"[[:space:]]*:[[:space:]]*true'
recorded_true_re='"recorded"[[:space:]]*:[[:space:]]*true'
stage_complete_re='"stage"[[:space:]]*:[[:space:]]*"complete"'
for ((attempt=0; attempt<30; attempt++)); do
  if docker exec "$target_id" wget -qO /dev/null http://127.0.0.1:18317/.well-known/cpamp-bootstrap/ui >/dev/null 2>&1; then
    ready=1; ready_surface=bootstrap; break
  fi
  if docker exec "$target_id" wget -qO /dev/null http://127.0.0.1:18317/health >/dev/null 2>&1; then
    status_json="$(docker exec "$target_id" /usr/local/bin/cpa-manager-plus upgrade status --json --db-path /data/usage.sqlite 2>/dev/null || true)"
    if [[ "$status_json" =~ $recorded_true_re ]] &&
       [[ "$status_json" =~ $stage_complete_re ]] &&
       [[ "$status_json" =~ $routing_committed_re ]]; then
      status_control_path=''
      if [[ "$status_json" =~ $ctrl_json_re ]]; then
        status_control_path="$(decode_json_prefix "${BASH_REMATCH[1]}")"
      fi
      candidate_path=''
      route_json="$(docker exec "$target_id" wget -qO- http://127.0.0.1:9082/internal/routing/fresh 2>/dev/null || true)"
      if [ -n "$route_json" ]; then
        if [[ "$route_json" =~ $committed_true_re ]] && [[ "$route_json" =~ $target_json_re ]]; then
          candidate_path="$(decode_json_prefix "${BASH_REMATCH[1]}")"
          if [ -n "$status_control_path" ] && [ "$candidate_path" != "$status_control_path" ]; then
            candidate_path=''
          fi
        fi
      else
        candidate_path="$status_control_path"
      fi
      if valid_control_prefix "$candidate_path" &&
         docker exec "$target_id" wget -qO /dev/null "http://127.0.0.1:18317${candidate_path}/api/v2/bootstrap/identity" >/dev/null 2>&1; then
        ready=1; ready_surface=activated; active_control_path="$candidate_path"; break
      fi
    fi
  fi
  [ "$(inspect "$target_id" '{{.State.Running}}')" = true ] || break
  sleep 2
done
[ "$ready" = 1 ] || fail 'Target UI or committed canonical management route is not ready; inspect only this target container and re-run the same --state-dir. No automatic rollback was attempted'
ui_origin="$(resolve_ui_origin)"
if [ "$ready_surface" = activated ]; then
  say 'Upgrade complete: v2 service is active (Upgrade-only bootstrap UI is retired).'
  say "Management console: ${ui_origin}${active_control_path}"
  say 'Authenticate with your Manager admin key.'
else
  say "Upgrade UI: $ui_origin/.well-known/cpamp-bootstrap/ui"
  say 'Authenticate with your existing Manager admin key. Complete reconciliation, choose CPA mode/plugins, verify and activate in the UI.'
  if [ "$mode" = external ]; then
    say 'External CPA stays running. Existing Docker DNS networks and host.docker.internal are retained. If the copied CPA URL uses localhost/127.0.0.1, choose a reachable external URL in the UI; that address now names the new container.'
  else
    say 'Original CPA is stopped for a stable managed import. The external alternative requires a separately running reachable CPA; original objects remain available for explicit recovery.'
  fi
fi
say "Target volume: $target_volume"
say "Backup volume: $backup_volume"
say "Runtime volume: $runtime_volume"
printf 'Resume: bash %q --state-dir %q\n' "$0" "$state_dir"
printf 'Explicit rollback (v2-only writes are not merged): bash %q --state-dir %q --rollback\n' "$0" "$state_dir"
