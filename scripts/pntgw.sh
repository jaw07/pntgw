#!/bin/bash
# Cross-compile pntgw for MIPS LE, deploy to ER-X, install persistent
# systemd service via post-config.d hook. Re-running upgrades in place.
#
# Pre-reqs: bootstrap.sh has run (key auth + LAN reach the dish).

set -euo pipefail
source "$(dirname "$0")/lib.sh"

prompt_secret ER_PASS "ER-X admin password (ubnt user)"
require_env ER_PASS

BIN="$REPO_DIR/pntgw-mipsle"

log "Cross-compiling pntgw ($GOOS/$GOARCH)"
# Direct `go build` (no make dependency) so this works on any host with
# Go — including Git Bash on Windows where `make` is absent.
( cd "$REPO_DIR" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -ldflags="-s -w" -o "$BIN" ./cmd/pntgw )
ls -lh "$BIN"

log "Pushing binary to ER-X"
er_scp "$BIN" "/tmp/pntgw.new"
er_sudo "mv /tmp/pntgw.new /config/pntgw && chmod +x /config/pntgw"

log "Installing pntgw post-config.d hook + systemd unit"
er_ssh 'cat > /config/scripts/post-config.d/pntgw.sh' <<'HOOK'
#!/bin/sh
UNIT=/etc/systemd/system/pntgw.service
cat > "$UNIT" <<'UNITEOF'
[Unit]
Description=pntgw — Starshield PNT gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/config/pntgw --config /config/pntgw.yaml
Restart=always
RestartSec=5
User=root
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNITEOF
chmod 644 "$UNIT"
systemctl daemon-reload
systemctl enable pntgw.service >/dev/null 2>&1 || true
systemctl restart pntgw.service
HOOK
er_ssh "chmod +x /config/scripts/post-config.d/pntgw.sh"
er_sudo "/config/scripts/post-config.d/pntgw.sh"

sleep 3
log "Verifying pntgw is up"
er_ssh "systemctl is-active pntgw"
log "Web UI: http://$ER_HOST:8080"
log "NMEA TCP: nc $ER_HOST 10110"
