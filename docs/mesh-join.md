# Joining the ER-X into a WARP Connector mesh on another account

`docs/edgerouter.md` covers a tunnel that lives in **your** Cloudflare
account. This covers the other case: the ER-X has to be reachable from a
**WARP Connector mesh that lives in someone else's account** — a
collaborator invited you in as an admin member, their mesh (a WARP
Connector plus their own tunnels) already exists there, and the ER-X
joins it as a routed leaf.

## What this does and does not do

- **Does:** make the ER-X LANs reachable *from* the mesh — a mesh host
  can hit the ER-X sensors, the pntgw UI, NMEA, the dish. Inbound to the
  ER-X.
- **Does NOT:** make the ER-X a mesh *node*. `cloudflared` on MIPS is
  inbound-only — it advertises routes but installs none, so the ER-X
  cannot initiate back into the mesh and **never appears in the WARP
  Connector mesh diagram**. A true bidirectional mesh node needs a WARP
  Connector running on a non-MIPS host at the site; the ER-X (MIPS,
  256 MB) cannot run WARP Connector. This is a hard architectural limit,
  not a misconfiguration to fix.

## 0. Get the account right — the #1 time-sink

Cloudflare auto-creates a personal account named **`<your-email>'s
Account`** for every user. The account you were *invited to* is a
**different account with a different ID**. Building everything in your
own empty personal account and then wondering why the mesh is empty is
the single most common mistake here.

Always confirm which account a token/ID actually points at *before*
doing anything:

```sh
curl -s https://api.cloudflare.com/client/v4/accounts \
  -H "Authorization: Bearer <ADMIN_API_TOKEN>" | python3 -m json.tool
# result[].name: "<someone>'s Account" == that someone's personal account.
```

Dashboard equivalent: the top-left account switcher lists every account
you can access by name; `dash.cloudflare.com/<ACCOUNT_ID>/...` changes
per account. Use the **invited** account's ID, and an API token that was
**created while in that account** (tokens are bound to the account they
were minted in — being added as a member does not widen an existing
token).

## 1. Token scopes (on the invited account)

`verify.sh`/`cloudflared.sh` need Tunnel:Edit + Zero Trust:Edit for the
single-account path. Mesh-join also touches the connector, network and
device-profile APIs. Mint a custom token **on the invited account** with:

- Cloudflare Tunnel — Edit
- Cloudflare One Connector: WARP — Edit
- Cloudflare One Networks — Edit
- Zero Trust — Edit
- DNS — Edit, Zone — Read

## 2. Find the existing mesh and its virtual network

The ER-X routes must land in the **same virtual network the mesh already
uses**, or you are routed but not in the mesh. Inventory first:

```sh
A=<INVITED_ACCT_ID>; H="Authorization: Bearer <ADMIN_API_TOKEN>"
for ep in cfd_tunnel warp_connector teamnet/virtual_networks \
          teamnet/routes devices/policies; do
  echo "== $ep =="
  curl -s "https://api.cloudflare.com/client/v4/accounts/$A/$ep" -H "$H" \
    | python3 -m json.tool
done
```

Record the `virtual_network` id the existing WARP Connector / tunnels
use (commonly the one named `default`). Call it `<MESH_VNET_ID>`.

## 3. Create the ER-X tunnel on the invited account

Same as the `cloudflared.sh` tunnel step, pointed at the invited
account. `config_src=cloudflare` (remotely-managed — routes/config via
API, the ER-X just runs the connector with a token):

```sh
curl -s -X POST "https://api.cloudflare.com/client/v4/accounts/$A/cfd_tunnel" \
  -H "$H" -H 'Content-Type: application/json' \
  --data '{"name":"er-x-home","config_src":"cloudflare"}'        # -> result.id = <TUNNEL_ID>
curl -s "https://api.cloudflare.com/client/v4/accounts/$A/cfd_tunnel/<TUNNEL_ID>/token" -H "$H"
# the connector token (~240 chars) — write to a 0600 file, keep out of shell history
```

## 4. Route the LANs into the mesh vnet

Every `CF_LAN_CIDRS` entry, **plus the dish subnet if you want the
Starshield gRPC reachable from the mesh** — `192.168.100.0/24` is *not*
in the default `CF_LAN_CIDRS`:

```sh
for net in 192.168.1.0/24 192.168.2.0/24 192.168.3.0/24 \
           192.168.4.0/24 192.168.10.0/24 192.168.100.0/24; do
  curl -s -X POST "https://api.cloudflare.com/client/v4/accounts/$A/teamnet/routes" \
    -H "$H" -H 'Content-Type: application/json' \
    --data "{\"network\":\"$net\",\"tunnel_id\":\"<TUNNEL_ID>\",\"virtual_network_id\":\"<MESH_VNET_ID>\",\"comment\":\"er-x\"}"
done
```

## 5. Repoint the running ER-X (surgical, revertible)

The ER-X is already running `cloudflared` against some other
tunnel/account. Swap **only the token file** — the binary, systemd unit
and post-config.d hook all stay as `cloudflared.sh` installed them:

```sh
ER=ubnt@192.168.10.1
ssh "$ER" 'cat > /tmp/t && chmod 600 /tmp/t' < connector_token_file   # over the ssh pipe, not argv
ssh "$ER" 'sudo -n cp -a /config/cf_tunnel_token /config/cf_tunnel_token.bak'   # revert path
ssh "$ER" 'sudo -n cp /tmp/t /config/cf_tunnel_token \
  && sudo -n chown root:root /config/cf_tunnel_token \
  && sudo -n chmod 600 /config/cf_tunnel_token && sudo -n rm -f /tmp/t'
ssh "$ER" 'sudo -n systemctl restart cloudflared'
```

Revert = restore the `.bak` token, `systemctl restart cloudflared`.

Verify it registered on the new account:

```sh
curl -s "https://api.cloudflare.com/client/v4/accounts/$A/cfd_tunnel/<TUNNEL_ID>" -H "$H" \
 | python3 -c 'import sys,json;d=json.load(sys.stdin)["result"];print(d["status"],len(d.get("connections") or []),"conns")'
# want: healthy 4 conns
```

## 6. Wire the routes into the mesh — not the default policy

Routes alone are reachable at the routing layer, but the WARP Connector
will not forward to them until they are in the connector's device
profile. The mesh connector runs under a profile named **`Mesh Network
Profile`** (matched to the `warp_connector@<team>.cloudflareaccess.com`
identity), in **Include** mode. Append the ER-X CIDRs to *that* profile's
include list — **never** the account default policy:

```sh
curl -s "https://api.cloudflare.com/client/v4/accounts/$A/devices/policies" -H "$H" \
 | python3 -c 'import sys,json;[print(p["policy_id"],p["name"]) for p in json.load(sys.stdin)["result"]]'
# GET .../devices/policy/<MESH_POLICY_ID>/include  -> read current entries
# PUT .../devices/policy/<MESH_POLICY_ID>/include  -> existing entries + ER-X CIDRs (full-list replace)
```

The include list is a full-list replace: read the existing entries and
PUT them back together with the new `{"address":"192.168.X.0/24",
"description":"er-x"}` entries, or you wipe what was there.

### Optional: let a specific collaborator's WARP device reach the ER-X

Add the same CIDRs to *their* identity-matched include profile
(`identity.email in {...}`, also Include mode) — again **not** the
account default exclude policy.

> **Local-overlap warning.** Include mode means only those subnets
> tunnel, so normal traffic is unaffected — *unless* that person's own
> LAN uses one of these ranges. `192.168.1.0/24` (the most common home
> subnet) and `192.168.100.0/24` (the Starlink/Starshield default
> management subnet) are the realistic collisions: with WARP up, their
> local access to that range breaks because it now routes into the
> tunnel. Prefer host `/32`s (`192.168.10.1/32`, `192.168.100.1/32`)
> over whole `/24`s when only specific devices matter. Fully reversible:
> remove the entries.

Dashboard equivalent for either profile: **Zero Trust → Settings → WARP
Client → Device profiles → `<profile>` → Split Tunnels (Include)**.
Changes take ~30–60 s and need a WARP-client reconnect to take effect.

## 7. Verify from the mesh

From a host inside the mesh (behind their WARP Connector, or a
WARP-enrolled device carrying the profile from step 6):

```sh
ping 192.168.10.1                 # ER-X mgmt — always up
ping 192.168.100.1                # Starshield dish (only if .100.0/24 was routed)
curl http://192.168.10.1:8080     # pntgw web UI
# NMEA:  gpsd tcp://192.168.10.1:10110     EdgeOS GUI: https://192.168.10.1
```

ICMP over the tunnel needs `net.ipv4.ping_group_range` set on the ER-X.
`cloudflared.sh` installs `/etc/sysctl.d/99-cloudflared-icmp.conf` for
this; if pings fail but TCP works, that sysctl is not applied.

## Reachable services recap

| Service | Endpoint (on any routed ER-X IP) |
|---|---|
| pntgw web UI | `http://<er-x-ip>:8080` |
| pntgw NMEA TCP | `<er-x-ip>:10110` (`gpsd tcp://<er-x-ip>:10110`) |
| EdgeOS GUI | `https://<er-x-ip>` |
| Sensors on LAN2/3/4 | plug into eth2/3/4, DHCP `192.168.{2,3,4}.50-150` |

Any device that comes up on a routed `/24` is reachable from the mesh
the moment it gets an address — no per-device Cloudflare config.

## Gotchas

- **Wrong account.** See §0. If inventories come back empty, you are
  almost certainly in your own personal `<email>'s Account`, not the
  invited one.
- **Wrong virtual network.** Routes added without an explicit
  `virtual_network_id` land in an auto-created `default` vnet. If the
  mesh uses a different vnet, you are routed but invisible to the mesh.
  Always pin `<MESH_VNET_ID>` from step 2.
- **Default policy vs profile.** The account default policy is usually
  exclude-mode covering all RFC1918. Editing it is account-wide and can
  break unrelated users. The mesh connector and per-user devices run
  under their own *identity-matched Include* profiles — that is the only
  place to add ER-X routes.
- **cloudflared is not a mesh node.** Restated because it keeps biting:
  inbound-only, no diagram presence, no return path. Want a real node?
  WARP Connector on a non-MIPS box at the site.
