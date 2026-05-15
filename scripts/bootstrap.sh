#!/bin/bash
# Initial ER-X setup: install SSH key, renumber the management LAN if
# needed, configure WAN/LANs/NAT/DHCP/DNS, install the NTP rate-limit
# hook, point ntpd at the Starshield as stratum-1 upstream.
#
# Idempotent. Safe to re-run.
#
# Pre-reqs:
#   - ER-X reachable at $ER_HOST (factory reset: 192.168.1.1)
#   - $ER_PASS set (admin password set in EdgeOS GUI on first login)
#   - sshpass installed locally (brew install sshpass)

set -euo pipefail
source "$(dirname "$0")/lib.sh"

prompt_secret ER_PASS "ER-X admin password (ubnt user)"
require_env ER_PASS LAN_MGMT_GATEWAY LAN_MGMT_CIDR

PUBKEY_FILE="${PUBKEY_FILE:-$HOME/.ssh/id_ed25519.pub}"
[ -f "$PUBKEY_FILE" ] || die "no pubkey at $PUBKEY_FILE (override with PUBKEY_FILE)"

PUBKEY_BLOB=$(awk '{print $2}' "$PUBKEY_FILE")
PUBKEY_TYPE=$(awk '{print $1}' "$PUBKEY_FILE")
PUBKEY_NAME="${PUBKEY_NAME:-$(whoami)-$(hostname -s)}"

log "Bootstrapping ER-X at $ER_HOST"

# --- 1. SSH key auth via Vyatta config ---
if can_key_ssh; then
  log "Key auth already works, skipping key install"
else
  log "Installing SSH key into Vyatta config"
  er_vyatta_pw \
    "set system login user $ER_USER authentication public-keys $PUBKEY_NAME key '$PUBKEY_BLOB'" \
    "set system login user $ER_USER authentication public-keys $PUBKEY_NAME type $PUBKEY_TYPE"
  can_key_ssh || die "key auth still fails after install"
fi

# --- 2. LAN renumber if currently on 192.168.1.0/24 ---
CURRENT_ETH0=$(er_ssh "ip -4 -o addr show eth0 | awk '{print \$4}'")
log "eth0 currently: $CURRENT_ETH0"
WANT_ETH0="$LAN_MGMT_GATEWAY/$(echo "$LAN_MGMT_CIDR" | cut -d/ -f2)"
if [ "$CURRENT_ETH0" != "$WANT_ETH0" ]; then
  warn "Renumbering eth0 -> $WANT_ETH0. SSH will drop; reconnect via DHCP on the new subnet."
  IFS=/ read -r NETBASE MASK <<< "$LAN_MGMT_CIDR"
  PREFIX="${NETBASE%.0}"  # e.g. 192.168.10
  er_vyatta \
    "delete interfaces ethernet eth0 address $CURRENT_ETH0" \
    "set interfaces ethernet eth0 address $WANT_ETH0" \
    "delete service dhcp-server shared-network-name LAN" \
    "set service dhcp-server shared-network-name LAN authoritative enable" \
    "set service dhcp-server shared-network-name LAN subnet $LAN_MGMT_CIDR default-router $LAN_MGMT_GATEWAY" \
    "set service dhcp-server shared-network-name LAN subnet $LAN_MGMT_CIDR dns-server $LAN_MGMT_GATEWAY" \
    "set service dhcp-server shared-network-name LAN subnet $LAN_MGMT_CIDR lease 86400" \
    "set service dhcp-server shared-network-name LAN subnet $LAN_MGMT_CIDR start ${PREFIX}.50 stop ${PREFIX}.150" \
    || true
  warn "If your laptop was on the old subnet, switch its interface to DHCP now."
  warn "Update ER_HOST=$LAN_MGMT_GATEWAY in config.env, then re-run this script."
  exit 0
fi

# --- 3. WAN + 4 LANs + NAT + DHCP + DNS forwarder ---
log "Configuring NAT + DNS forwarder + LAN2/3/4"
VYATTA_CMDS=(
  "set service nat rule 5010 description 'masquerade lan to wan'"
  "set service nat rule 5010 outbound-interface eth1"
  "set service nat rule 5010 type masquerade"
  "set service dns forwarding cache-size 300"
  "set service dns forwarding name-server 1.1.1.1"
  "set service dns forwarding name-server 1.0.0.1"
  "set service dns forwarding listen-on eth0"
)
for i in 2 3 4; do
  GW_VAR="LAN${i}_GATEWAY"
  GW="${!GW_VAR:-192.168.$i.1}"
  PREFIX="${GW%.1}"
  VYATTA_CMDS+=(
    "set interfaces ethernet eth$i address ${GW}/24"
    "set interfaces ethernet eth$i description LAN$i"
    "set service dhcp-server shared-network-name LAN$i authoritative enable"
    "set service dhcp-server shared-network-name LAN$i subnet ${PREFIX}.0/24 default-router $GW"
    "set service dhcp-server shared-network-name LAN$i subnet ${PREFIX}.0/24 dns-server $GW"
    "set service dhcp-server shared-network-name LAN$i subnet ${PREFIX}.0/24 lease 86400"
    "set service dhcp-server shared-network-name LAN$i subnet ${PREFIX}.0/24 start ${PREFIX}.50 stop ${PREFIX}.150"
    "set service dns forwarding listen-on eth$i"
  )
done
er_vyatta "${VYATTA_CMDS[@]}"

# --- 4. NTP: Starshield as stratum-1 upstream + rate-limit hook ---
log "Configuring NTP (Starshield 192.168.100.1 as stratum-1 upstream)"
er_vyatta "set system ntp server 192.168.100.1"

log "Installing NTP rate-limit-strip hook"
er_ssh 'cat > /config/scripts/post-config.d/ntp-no-rate-limit.sh' <<'HOOK'
#!/bin/sh
NTPCONF=/etc/ntp.conf
[ -f "$NTPCONF" ] || exit 0
if grep -qE '^restrict -[46] default.* limited' "$NTPCONF"; then
  sed -i 's/^\(restrict -[46] default.*\) limited/\1/' "$NTPCONF"
  systemctl restart ntp 2>/dev/null || /etc/init.d/ntp restart 2>/dev/null || true
fi
HOOK
er_ssh "chmod +x /config/scripts/post-config.d/ntp-no-rate-limit.sh"
er_sudo "/config/scripts/post-config.d/ntp-no-rate-limit.sh"

log "Bootstrap complete. Network state:"
er_ssh "ip -4 -br addr show | grep -v ' DOWN'"
