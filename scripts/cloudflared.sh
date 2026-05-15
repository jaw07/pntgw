#!/bin/bash
# Cross-compile cloudflared for MIPS LE, deploy to ER-X, create tunnel via
# Cloudflare API, add private network routes, install persistent systemd
# service. Idempotent — re-running on an existing tunnel is fine.
#
# Pre-reqs: bootstrap.sh has run successfully (key auth + LANs working).

set -euo pipefail
source "$(dirname "$0")/lib.sh"

prompt_secret ER_PASS  "ER-X admin password (ubnt user)"
prompt_value  CF_ACCT  "Cloudflare account ID"
prompt_secret CF_TOKEN "Cloudflare scoped API token (Tunnel:Edit + Zero Trust:Edit)"
require_env ER_PASS CF_TOKEN CF_ACCT
: "${TUNNEL_NAME:=er-x-home}"
: "${CF_PIN_WIREGUARD:=1}"
: "${CF_APPLY_SPLIT_TUNNEL:=0}"

CLOUDFLARED_SRC="${CLOUDFLARED_SRC:-$REPO_DIR/../cloudflared}"
CLOUDFLARED_BIN="$REPO_DIR/cloudflared-mipsle"

# --- 1. Build the MIPS LE binary ---
if [ ! -d "$CLOUDFLARED_SRC" ]; then
  log "Cloning cloudflared into $CLOUDFLARED_SRC"
  git clone --depth 1 https://github.com/cloudflare/cloudflared.git "$CLOUDFLARED_SRC"
fi

log "Cross-compiling cloudflared ($GOOS/$GOARCH)"
(
  cd "$CLOUDFLARED_SRC"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -mod=vendor -ldflags="-s -w" \
    -o "$CLOUDFLARED_BIN" ./cmd/cloudflared
)
ls -lh "$CLOUDFLARED_BIN"

# --- 2. Push binary ---
log "Pushing cloudflared to ER-X"
er_scp "$CLOUDFLARED_BIN" "/tmp/cloudflared.new"
er_sudo "mv /tmp/cloudflared.new /config/cloudflared && chmod +x /config/cloudflared"

# --- 3. Tunnel: find or create ---
log "Looking up tunnel '$TUNNEL_NAME'"
TUN_ID=$(cf_api GET "/accounts/$CF_ACCT/cfd_tunnel?name=$TUNNEL_NAME&is_deleted=false" \
  | python3 -c "import sys,json; r=json.load(sys.stdin).get('result') or []; print(r[0]['id'] if r else '')")
if [ -z "$TUN_ID" ]; then
  log "Creating tunnel '$TUNNEL_NAME'"
  TUN_ID=$(cf_api POST "/accounts/$CF_ACCT/cfd_tunnel" \
    "{\"name\":\"$TUNNEL_NAME\",\"config_src\":\"cloudflare\"}" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['result']['id'])")
fi
log "Tunnel ID: $TUN_ID"

# --- 4. Fetch connector token ---
log "Fetching connector token"
TOKEN_TMP=$(mktemp)
chmod 600 "$TOKEN_TMP"
cf_api GET "/accounts/$CF_ACCT/cfd_tunnel/$TUN_ID/token" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['result'])" \
  > "$TOKEN_TMP"

log "Pushing token to ER-X"
er_scp "$TOKEN_TMP" "/tmp/cf_tunnel_token"
er_sudo "mv /tmp/cf_tunnel_token /config/cf_tunnel_token && chown root:root /config/cf_tunnel_token && chmod 600 /config/cf_tunnel_token"
rm -f "$TOKEN_TMP"

# --- 5. Private network routes ---
log "Adding private network routes for ${CF_LAN_CIDRS[*]}"
EXISTING=$(cf_api GET "/accounts/$CF_ACCT/teamnet/routes?tunnel_id=$TUN_ID&is_deleted=false" \
  | python3 -c "import sys,json; [print(r['network']) for r in (json.load(sys.stdin).get('result') or [])]")
for NET in "${CF_LAN_CIDRS[@]}"; do
  if grep -qxF "$NET" <<<"$EXISTING"; then
    log "  $NET already routed, skipping"
  else
    cf_api POST "/accounts/$CF_ACCT/teamnet/routes" \
      "{\"network\":\"$NET\",\"tunnel_id\":\"$TUN_ID\",\"comment\":\"er-x\"}" >/dev/null
    log "  added $NET"
  fi
done

# --- 6. Optional: pin WireGuard ---
if [ "$CF_PIN_WIREGUARD" = "1" ]; then
  log "Pinning device tunnel_protocol = wireguard, auto_fallback off"
  cf_api PATCH "/accounts/$CF_ACCT/devices/policy" \
    '{"tunnel_protocol":"wireguard","disable_auto_fallback":true}' >/dev/null
fi

# --- 7. Optional: split-tunnel surgery (replace 192.168.0.0/16 with complement) ---
if [ "$CF_APPLY_SPLIT_TUNNEL" = "1" ]; then
  log "Applying split-tunnel surgery (excludes everything in 192.168.0.0/16 except .1, .2, .3, .4, .10)"
  cf_api PUT "/accounts/$CF_ACCT/devices/policy/exclude" '[
    {"address":"10.0.0.0/8"},{"address":"100.64.0.0/10"},
    {"address":"169.254.0.0/16"},{"address":"172.16.0.0/12"},
    {"address":"192.0.0.0/24"},
    {"address":"192.168.0.0/24"},{"address":"192.168.5.0/24"},
    {"address":"192.168.6.0/23"},{"address":"192.168.8.0/23"},
    {"address":"192.168.11.0/24"},{"address":"192.168.12.0/22"},
    {"address":"192.168.16.0/20"},{"address":"192.168.32.0/19"},
    {"address":"192.168.64.0/18"},{"address":"192.168.128.0/17"},
    {"address":"224.0.0.0/24"},{"address":"240.0.0.0/4"},
    {"address":"255.255.255.255/32"},
    {"address":"fe80::/10"},{"address":"fd00::/8"},
    {"address":"ff01::/16"},{"address":"ff02::/16"},
    {"address":"ff03::/16"},{"address":"ff04::/16"},{"address":"ff05::/16"}
  ]' >/dev/null
fi

# --- 8. Persistent service via post-config.d hook ---
log "Installing cloudflared post-config.d hook + systemd unit"
er_ssh 'cat > /config/scripts/post-config.d/cloudflared.sh' <<'HOOK'
#!/bin/sh
# Idempotent boot-time installer for cloudflared on the ER-X.

# Unprivileged ICMP for cloudflared's ping proxy.
SYSCTL=/etc/sysctl.d/99-cloudflared-icmp.conf
echo "net.ipv4.ping_group_range = 0 2147483647" > "$SYSCTL"
sysctl -p "$SYSCTL" >/dev/null 2>&1 || true

UNIT=/etc/systemd/system/cloudflared.service
cat > "$UNIT" <<'UNITEOF'
[Unit]
Description=cloudflared tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/config/cloudflared --no-autoupdate tunnel run --token-file /config/cf_tunnel_token
Restart=always
RestartSec=10
User=root
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNITEOF
chmod 644 "$UNIT"
systemctl daemon-reload
systemctl enable cloudflared.service >/dev/null 2>&1 || true
systemctl restart cloudflared.service
HOOK
er_ssh "chmod +x /config/scripts/post-config.d/cloudflared.sh"
er_sudo "/config/scripts/post-config.d/cloudflared.sh"

log "Waiting 10s for tunnel to come up"
sleep 10
STATUS=$(cf_api GET "/accounts/$CF_ACCT/cfd_tunnel/$TUN_ID" \
  | python3 -c "import sys,json; d=json.load(sys.stdin)['result']; print(d.get('status','?'), len(d.get('connections',[])),'conns')")
log "Tunnel: $STATUS"
