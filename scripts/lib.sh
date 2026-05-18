#!/bin/bash
# Common helpers, sourced by every deploy script.
# Not meant to be run directly.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

CONFIG_FILE="${CONFIG_FILE:-$SCRIPT_DIR/config.env}"
if [ -f "$CONFIG_FILE" ]; then
  # shellcheck disable=SC1090
  source "$CONFIG_FILE"
fi

: "${ER_HOST:=192.168.10.1}"
: "${ER_USER:=ubnt}"
: "${GOOS:=linux}"
: "${GOARCH:=mipsle}"
: "${LAN_MGMT_GATEWAY:=192.168.10.1}"
: "${LAN_MGMT_CIDR:=192.168.10.0/24}"
: "${LAN2_GATEWAY:=192.168.2.1}"
: "${LAN3_GATEWAY:=192.168.3.1}"
: "${LAN4_GATEWAY:=192.168.4.1}"
: "${TUNNEL_NAME:=er-x-home}"

# Default LAN CIDR set advertised to WARP. Override in config.env or shell env
# with: CF_LAN_CIDRS=( 192.168.10.0/24 ... )
if [ -z "${CF_LAN_CIDRS+x}" ]; then
  CF_LAN_CIDRS=( 192.168.1.0/24 192.168.2.0/24 192.168.3.0/24 192.168.4.0/24 192.168.10.0/24 )
fi
: "${CF_PIN_WIREGUARD:=1}"
: "${CF_APPLY_SPLIT_TUNNEL:=0}"

log()  { printf '\033[1;32m==> %s\033[0m\n' "$*" >&2; }
warn() { printf '\033[1;33m==> %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[1;31mxx  %s\033[0m\n' "$*" >&2; exit 1; }

require_env() {
  local v
  for v in "$@"; do
    if [ -z "${!v:-}" ]; then die "env var $v is required (set in $CONFIG_FILE or export it)"; fi
  done
}

# Interactive fallback for missing values. Use these at the top of a script
# before require_env; they short-circuit if the var is already set, or if
# stdin isn't a tty (so CI / non-interactive runs still hard-fail via require_env).
prompt_value() {
  local var="$1" desc="$2"
  [ -n "${!var:-}" ] && return 0
  [ -t 0 ] || return 0
  local val
  read -r -p "$desc: " val
  export "$var=$val"
}

prompt_secret() {
  local var="$1" desc="$2"
  [ -n "${!var:-}" ] && return 0
  [ -t 0 ] || return 0
  local val
  read -rs -p "$desc: " val
  echo
  export "$var=$val"
}

# Run a command on the ER-X (key auth assumed once bootstrap finishes).
er_ssh() {
  ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 \
    "$ER_USER@$ER_HOST" "$@"
}

# Same but allow password auth (used before key auth exists).
er_ssh_pw() {
  require_env ER_PASS
  sshpass -p "$ER_PASS" ssh -o StrictHostKeyChecking=accept-new \
    -o PreferredAuthentications=password -o PubkeyAuthentication=no \
    -o ConnectTimeout=8 "$ER_USER@$ER_HOST" "$@"
}

# SCP to /tmp on the ER-X. Always uses key auth (post-bootstrap).
er_scp() {
  scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
    "$1" "$ER_USER@$ER_HOST:$2"
}

# Run a command as root on the ER-X. Pipes the password to sudo -S.
er_sudo() {
  require_env ER_PASS
  ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$ER_USER@$ER_HOST" \
    "sudo -S -p '' sh -c $(printf '%q' "$*")" <<<"$ER_PASS"
}

# Run a Vyatta config session. Pass each `set/delete` line as a positional arg.
er_vyatta() {
  local cmds=""
  local c
  for c in "$@"; do
    cmds+="$c"$'\n'
  done
  er_ssh "bash -s" <<REMOTE
. /opt/vyatta/etc/functions/script-template
configure
$cmds
commit
save
exit
REMOTE
}

# Same but using password auth (pre-key-auth).
er_vyatta_pw() {
  local cmds=""
  local c
  for c in "$@"; do
    cmds+="$c"$'\n'
  done
  er_ssh_pw "bash -s" <<REMOTE
. /opt/vyatta/etc/functions/script-template
configure
$cmds
commit
save
exit
REMOTE
}

# Check we can reach the ER-X over key auth. Returns 0 if yes.
can_key_ssh() {
  er_ssh "true" >/dev/null 2>&1
}

# TCP reachability with zero external binaries — bash's /dev/tcp works on
# Linux, macOS (bash 3.2), and Git Bash. No `nc` (absent in Git Bash),
# no `timeout` (absent on macOS), no `ping` (BSD/iputils flag drift).
# For a LAN host this connects or refuses near-instantly; the only slow
# case is a DROP firewall, which doesn't apply to your own router.
tcp_open() {
  local host="$1" port="$2"
  ( exec 3<>"/dev/tcp/$host/$port" ) 2>/dev/null && return 0
  return 1
}

# CF v4 API helper. Takes METHOD PATH [BODY].
cf_api() {
  require_env CF_TOKEN
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -X "$method" "https://api.cloudflare.com/client/v4$path" \
      -H "Authorization: Bearer $CF_TOKEN" \
      -H "Content-Type: application/json" \
      --data "$body"
  else
    curl -sS -X "$method" "https://api.cloudflare.com/client/v4$path" \
      -H "Authorization: Bearer $CF_TOKEN"
  fi
}
