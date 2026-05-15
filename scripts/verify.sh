#!/bin/bash
# Verify the supplied credentials, network reach, and toolchain before any
# destructive operation. Exit 0 only if every check passes.

set -euo pipefail
source "$(dirname "$0")/lib.sh"

prompt_secret ER_PASS  "ER-X admin password (ubnt user)"
prompt_value  CF_ACCT  "Cloudflare account ID"
prompt_secret CF_TOKEN "Cloudflare scoped API token"

fail=0
pass=0

green() { printf '  \033[1;32mOK\033[0m  %s\n' "$1"; pass=$((pass+1)); }
red()   { printf '  \033[1;31mXX\033[0m  %s — %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
skip()  { printf '  \033[1;33m--\033[0m  %s — %s\n' "$1" "${2:-skipped}"; }

log "Verifying environment"

# --- local toolchain ---
command -v go >/dev/null      && green "go available ($(go version | awk '{print $3}'))" || red "go not installed" "needed for cross-compile"
command -v ssh >/dev/null     && green "ssh available" || red "ssh not installed"
command -v curl >/dev/null    && green "curl available" || red "curl not installed"
command -v python3 >/dev/null && green "python3 available" || red "python3 not installed" "needed for JSON parsing"
command -v sshpass >/dev/null && green "sshpass available (needed for bootstrap before key auth)" || skip "sshpass not installed" "needed only for first-time key install"

# --- ER-X reach ---
log "Checking ER-X at $ER_HOST"
if ping -c 1 -W 1 -t 1 "$ER_HOST" >/dev/null 2>&1; then
  green "ER-X pings on $ER_HOST"
else
  red "ER-X unreachable on $ER_HOST" "check ER_HOST in config.env"
fi

if can_key_ssh; then
  green "ER-X SSH key auth works"
  KEY_AUTH=1
else
  KEY_AUTH=0
  warn "Key auth not yet installed (normal pre-bootstrap)"
fi

# --- ER-X password ---
if [ -n "${ER_PASS:-}" ]; then
  if [ "$KEY_AUTH" = "1" ]; then
    if er_ssh "echo $(printf %q "$ER_PASS") | sudo -S -p '' -n true 2>&1" 2>/dev/null | grep -qE '^$|sudo:'; then
      # sudo with -n fails if password is needed but it'll accept our stdin; test by running real cmd
      if WHOAMI=$(er_sudo "whoami") && [ "$WHOAMI" = "root" ]; then
        green "ER-X sudo password accepted (root via sudo)"
      else
        red "ER-X sudo password rejected" "got: $WHOAMI"
      fi
    else
      if WHOAMI=$(er_sudo "whoami" 2>/dev/null) && [ "$WHOAMI" = "root" ]; then
        green "ER-X sudo password accepted (root via sudo)"
      else
        red "ER-X sudo password rejected"
      fi
    fi
  else
    # Pre-bootstrap: test password via ssh login itself
    if command -v sshpass >/dev/null && er_ssh_pw "true" 2>/dev/null; then
      green "ER-X password authenticates (pre-bootstrap)"
    else
      red "ER-X password auth failed" "check ER_PASS or install sshpass"
    fi
  fi
fi

# --- Cloudflare ---
log "Checking Cloudflare API access"
if [ -n "${CF_TOKEN:-}" ]; then
  TOK_RESP=$(curl -sS "https://api.cloudflare.com/client/v4/user/tokens/verify" \
    -H "Authorization: Bearer $CF_TOKEN")
  if printf '%s' "$TOK_RESP" | python3 -c "import sys,json; sys.exit(0 if json.load(sys.stdin)['success'] else 1)" 2>/dev/null; then
    STATUS=$(printf '%s' "$TOK_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['status'])")
    green "CF_TOKEN valid (status=$STATUS)"
  else
    ERR=$(printf '%s' "$TOK_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('errors',[{}])[0].get('message','unknown'))" 2>/dev/null || echo "parse failed")
    red "CF_TOKEN invalid" "$ERR"
  fi
fi

if [ -n "${CF_ACCT:-}" ] && [ -n "${CF_TOKEN:-}" ]; then
  ACCT_RESP=$(curl -sS "https://api.cloudflare.com/client/v4/accounts/$CF_ACCT" \
    -H "Authorization: Bearer $CF_TOKEN")
  if printf '%s' "$ACCT_RESP" | python3 -c "import sys,json; sys.exit(0 if json.load(sys.stdin)['success'] else 1)" 2>/dev/null; then
    NAME=$(printf '%s' "$ACCT_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['name'])")
    green "CF_ACCT reachable ($NAME)"
  else
    red "CF_ACCT not reachable with this token" "check account ID + token scopes"
  fi

  # Required scopes: list tunnels (cfd_tunnel:read) and list teamnet routes (zero_trust:read)
  TUN_RESP=$(curl -sS "https://api.cloudflare.com/client/v4/accounts/$CF_ACCT/cfd_tunnel?per_page=1" \
    -H "Authorization: Bearer $CF_TOKEN")
  if printf '%s' "$TUN_RESP" | python3 -c "import sys,json; sys.exit(0 if json.load(sys.stdin)['success'] else 1)" 2>/dev/null; then
    green "CF token has Cloudflare Tunnel scope"
  else
    red "CF token missing Cloudflare Tunnel scope" "need Account.Cloudflare Tunnel:Edit"
  fi

  ZT_RESP=$(curl -sS "https://api.cloudflare.com/client/v4/accounts/$CF_ACCT/teamnet/routes?per_page=1" \
    -H "Authorization: Bearer $CF_TOKEN")
  if printf '%s' "$ZT_RESP" | python3 -c "import sys,json; sys.exit(0 if json.load(sys.stdin)['success'] else 1)" 2>/dev/null; then
    green "CF token has Zero Trust scope"
  else
    red "CF token missing Zero Trust scope" "need Account.Zero Trust:Edit"
  fi
fi

echo
log "Result: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
