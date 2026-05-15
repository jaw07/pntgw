# pntgw — Starshield/Starlink PNT gateway

Single static Go daemon that runs on an EdgeRouter X (or any small Linux box) and exposes the PNT data from an attached Starshield/Starlink dish as a standard NMEA-0183 stream — so any consumer that already speaks NMEA (gpsd, navigation apps, autopilots, ADS-B receivers, EFBs) can use it without knowing anything about SpaceX's gRPC API.

## What it does

```
                          +------------------------+
  Starshield dish         |        pntgw           |     +-------------+
  (192.168.100.1:9200)    |  ---- gRPC poller ---- | --> | TCP server  | <- multiple clients
       gRPC  ──>          |  ---- NMEA encoder --- | --> | UDP unicast | -> N destinations
                          |  ---- HTTP web UI ---- |     +-------------+
                          +------------------------+
                                       |
                                YAML config on disk
```

- Polls `SpaceX.API.Device.Device/Handle` (`get_status` + `get_diagnostics`) via gRPC reflection — no SpaceX proto files embedded
- Generates the four core NMEA-0183 sentences (`$GPGGA`, `$GPRMC`, `$GPZDA`, `$GPGSA`) with correct XOR checksums and proper DMM coordinates
- Fans the stream out to:
  - A TCP listener (multiple clients can connect; `gpsd tcp://erx:port` works)
  - Any number of configured UDP unicast destinations (handy for ForeFlight, an autopilot, a remote NMEA logger)
- Serves a small embedded web UI on the management interface for live status + config edits, persisted to disk

Single static binary, 14 MB, MIPS little-endian build runs comfortably on a 256 MB EdgeRouter X.

## Build

Requires Go 1.22+ on the build host.

```sh
make build      # build for the current host
make erx        # cross-compile static MIPS LE binary for an EdgeRouter X (CGO_ENABLED=0 GOOS=linux GOARCH=mipsle)
make deploy     # build + scp + restart on the ER-X at 192.168.10.1
```

Any other Linux target: set `GOARCH` to whatever the box is (`amd64`, `arm64`, `arm`, etc.) and use the equivalent of `make erx` with the right flag.

## Config

YAML at `/config/pntgw.yaml` (auto-created with defaults on first run):

```yaml
dish_addr: "192.168.100.1:9200"
poll_interval_ms: 1000
tcp:
  enabled: true
  bind: "0.0.0.0:10110"          # standard NMEA TCP port
udp_destinations:
  - "192.168.10.5:10110"         # any number; format host:port
web:
  enabled: true
  bind: "0.0.0.0:8080"
```

The web UI's PUT to `/api/config` rewrites this file in place and re-applies the running configuration immediately — TCP server rebinds, UDP destination list updates, no restart needed.

## Web UI

`http://<er-x-ip>:8080` from any device on the management LAN. Shows:

- Live PNT status: lat/lon/altitude, uncertainty, satellite count, fix validity, last poll age
- Last NMEA sentences emitted (live tail)
- TCP client count, UDP destination count
- Editable config: poll interval, dish address, TCP bind, UDP destinations (add/remove)

## NMEA output

```
$GPGGA,104507.42,5000.6485,N,00817.1657,E,1,20,0.3,140.3,M,0.0,M,,*5B
$GPRMC,104507.42,A,5000.6485,N,00817.1657,E,0.0,0.0,150526,,,A*5B
$GPZDA,104507.42,15,05,2026,00,00*60
$GPGSA,A,3,,,,,,,,,,,,,0.3,0.3,0.3*31
```

All four sentences are emitted on every poll. The dish doesn't expose per-satellite information so `$GPGSV` is intentionally omitted (rather than fabricated).

## Consuming the stream

### From any gpsd-aware tool

```sh
gpsd -N -n tcp://<er-x-ip>:10110
```

then `gpsmon`, `cgps`, anything that speaks gpsd.

### Raw

```sh
nc <er-x-ip> 10110
```

### ForeFlight / EFB

Add the iPad's IP as a UDP destination in the web UI on port 49002 with format-shim later (XGPS/GDL90 emit is a planned addition; currently UDP carries raw NMEA, which ForeFlight does not consume directly).

## Deploying on an EdgeRouter X

There are a few EdgeOS-specific quirks that matter:

1. **SSH key auth must be declared in Vyatta config**, not just dropped in `~/.ssh/authorized_keys`. EdgeOS's config-management *regenerates* `authorized_keys` from `system login user ... authentication public-keys` on every commit and wipes anything else you put there. To install a key persistently:

   ```sh
   ssh ubnt@<er-x> 'bash -s' <<REMOTE
   . /opt/vyatta/etc/functions/script-template
   configure
   set system login user ubnt authentication public-keys laptop key '<base64-key>'
   set system login user ubnt authentication public-keys laptop type ssh-ed25519
   commit
   save
   exit
   REMOTE
   ```

2. **Persistent service install** lives in `/config/scripts/post-config.d/pntgw.sh`. That directory runs as root on every boot after the Vyatta config is applied, and survives firmware upgrades. The hook (re)installs `/etc/systemd/system/pntgw.service` and restarts the service. Same pattern works for any other custom daemon on EdgeOS.

3. **Watch for subnet collisions with the Starshield**. The dish NATs to `192.168.1.0/24` by default on its LAN-side. If you also use `192.168.1.0/24` as your ER-X LAN, ARP/routing breaks the moment eth1 comes up. Renumber the ER-X LAN to e.g. `192.168.10.0/24` to keep them disjoint.

4. **NTP rate-limit kiss-o'-death.** EdgeOS's autogenerated `/etc/ntp.conf` includes `limited` in the default restrict line, which sends KoD packets to legitimate clients that poll too often. There's a sibling hook at `/config/scripts/post-config.d/ntp-no-rate-limit.sh` that strips `limited` after every config commit:

   ```sh
   #!/bin/sh
   NTPCONF=/etc/ntp.conf
   if grep -qE '^restrict -[46] default.* limited' "$NTPCONF"; then
     sed -i 's/^\(restrict -[46] default.*\) limited/\1/' "$NTPCONF"
     systemctl restart ntp 2>/dev/null || /etc/init.d/ntp restart 2>/dev/null || true
   fi
   ```

5. **Use the dish as upstream NTP**. Add `set system ntp server 192.168.100.1` — it's stratum 1 (GPS-derived) and physically attached to your router. Pool servers are kept as fallback so a dish reboot doesn't break time sync.

## Topology after install

```
[ISP] -> Starshield dish -> eth1 (192.168.1.x from dish DHCP)
                              |
                           ER-X
   eth0 192.168.10.1/24      |     pntgw on :10110 (NMEA TCP)
   eth2 192.168.2.1/24       |     pntgw on :8080  (web UI)
   eth3 192.168.3.1/24       |     ntpd on :123     (NTP from dish)
   eth4 192.168.4.1/24       |     cloudflared      (private network tunnel)
```

The ER-X reaches the dish's gRPC at `192.168.100.1:9200` automatically — Starlink dishes always expose that subnet over their LAN-side regardless of which network they're handing out for DHCP.

## Layout

```
pntgw/
  cmd/pntgw/main.go            entry point, supervisor, polling loop
  internal/config/             YAML load/save with hot reload
  internal/dish/               gRPC reflection client to the dish
  internal/nmea/               NMEA sentence builders with checksums
  internal/tcpsrv/             TCP fanout listener (multi-client)
  internal/udpsink/            UDP unicast sender (N destinations)
  internal/web/                HTTP server + embedded UI assets
  Makefile                     build / erx / deploy targets
```

## Roadmap

- XGPS/GDL90 UDP framing for native ForeFlight discovery
- MAVLink GPS injection (Pixhawk/Cube via UDP)
- Track upstream tunnel-protocol fallback if dish reports MASQUE only
- Optional Prometheus `/metrics` endpoint
