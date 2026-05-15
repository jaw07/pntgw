# Deploy scripts

Idempotent bash scripts that turn a fresh EdgeRouter X into the full edge stack: network + NTP + cloudflared tunnel + pntgw. Each script is safe to re-run.

## Two ways to supply credentials

**Option 1 — interactive prompts (default).** Just run the script. It prompts for any missing values:

```sh
brew install sshpass         # needed only by bootstrap.sh for password-auth before key install
./bootstrap.sh
ER-X admin password (ubnt user): ****
```

**Option 2 — config file (better for repeat runs).**

```sh
cp config.env.example config.env
$EDITOR config.env           # fill in ER_PASS, CF_TOKEN, CF_ACCT
./bootstrap.sh
```

`config.env` is `.gitignore`d. Env vars exported in the shell also work (`export CF_TOKEN=...; ./cloudflared.sh`).

## Run everything

```sh
./deploy-all.sh
```

Order: `bootstrap.sh` → `cloudflared.sh` → `pntgw.sh`. Each can also be run individually.

## What each script does

- **verify.sh** — sanity-checks credentials and toolchain before anything destructive: pings the ER-X, confirms SSH key auth and sudo password, validates the Cloudflare API token + account ID + required scopes (Tunnel:Edit, Zero Trust:Edit), checks `go`/`ssh`/`curl`/`python3`/`sshpass` are installed. Exits non-zero if anything fails.
- **bootstrap.sh** — installs SSH key into Vyatta config, renumbers eth0 if it collides with the Starshield's 192.168.1.0/24, configures NAT + DHCP + DNS forwarder + LAN2/3/4, points ntpd at the dish, installs the NTP rate-limit hook.
- **cloudflared.sh** — clones cloudflared if needed, cross-compiles MIPSLE static binary, pushes to `/config/cloudflared`, finds-or-creates the named tunnel via the Cloudflare API, fetches its connector token, advertises every CIDR in `CF_LAN_CIDRS` as a private network, pins WireGuard, optionally rewrites the device split-tunnel exclude list, installs the systemd unit + post-config.d hook + ICMP sysctl.
- **pntgw.sh** — `make erx` from the repo root, pushes the binary to `/config/pntgw`, installs the systemd unit + post-config.d hook.

## Re-runs

All scripts detect existing state and skip the destructive parts:
- `bootstrap.sh` skips key install if key auth already works; skips renumber if eth0 is already on the target subnet.
- `cloudflared.sh` reuses an existing tunnel with the same name (no duplicate creation), skips already-advertised CIDRs.
- `pntgw.sh` always rebuilds and pushes (intentional — that's the upgrade path).

## Renumber gotcha

`bootstrap.sh` renumbers eth0 atomically as part of its config commit. Your laptop's SSH session will drop the moment the commit fires. Switch the laptop's NIC to DHCP, get a new lease from the new subnet, update `ER_HOST` in `config.env`, and re-run the script.

## Tearing down

There's no `uninstall.sh` yet. To remove cleanly:

```sh
ssh ubnt@$ER_HOST 'sudo bash -c "
  systemctl disable --now cloudflared pntgw 2>/dev/null
  rm -f /etc/systemd/system/{cloudflared,pntgw}.service
  rm -f /config/{cloudflared,cf_tunnel_token,pntgw,pntgw.yaml}
  rm -f /config/scripts/post-config.d/{cloudflared,pntgw,ntp-no-rate-limit}.sh
  rm -f /etc/sysctl.d/99-cloudflared-icmp.conf
  systemctl daemon-reload
"'
```

…and on the Cloudflare side, delete the tunnel + its private network routes.
