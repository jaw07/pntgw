# erx-edge

Everything that runs on the EdgeRouter X at the edge of this site. Source for two daemons plus all the operational docs to deploy them on EdgeOS.

| Component | What it does | Where |
|---|---|---|
| **pntgw** | Polls a Starlink/Starshield dish via gRPC and exposes the PNT data as NMEA over TCP + UDP, with a web UI for config | this repo |
| **cloudflared** | Cloudflare Tunnel connector, exposes the four LANs to WARP clients without inbound ports | upstream binary, cross-compiled and deployed per [docs/edgerouter.md](docs/edgerouter.md) |
| **ntpd** | Built-in EdgeOS NTP, configured to use the Starshield as stratum-1 upstream and serve LAN clients | EdgeOS Vyatta config + a small patch hook |
| **EdgeOS** | The router itself: 4 LANs, WAN on the dish, NAT, DHCP, DNS forwarder | Vyatta config |

ER-X is MIPS little-endian (MT7621A, 256 MB RAM / 256 MB flash). Anything you want to run on it has to either be in the EdgeOS package set or cross-compile to a static `linux/mipsle` binary. Both daemons here are pure Go with `CGO_ENABLED=0`, which makes that trivial.

## pntgw

Single static daemon, the only thing in this repo as actual source.

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
- Emits the four core NMEA-0183 sentences (`$GPGGA`, `$GPRMC`, `$GPZDA`, `$GPGSA`) with correct XOR checksums and DMM coordinates
- TCP listener (gpsd-compatible: `gpsd tcp://erx:port`, multi-client) plus N configured UDP unicast destinations
- Web UI on the management interface: live status, add/remove UDP destinations, edit TCP bind and poll interval, all persisted to YAML
- **PNT-source toggle**: switch the dish between GPS and Starshield-constellation PNT (`dish_inhibit_gps`). On a Starshield-provisioned terminal, inhibiting GPS falls back to LEO ranging — the fix stays valid and live but uncertainty rises from ~1.5 m (GPS) to ~5-16 m (Starshield). The toggle is not persisted and never auto-applied: pntgw only issues the switch on an explicit UI/API action. The dish holds it as runtime state, so a dish/router reboot returns to GPS.

### Build

```sh
make build      # current host
make erx        # static MIPS LE for the ER-X
make deploy     # build + scp + restart on 192.168.10.1
```

`make` is convenience only. The build is a single `go build` and works
from any host with Go — Linux, macOS, or native Windows (no make, no
bash needed for the build itself):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle go build -ldflags="-s -w" -o pntgw-mipsle ./cmd/pntgw
```
```powershell
# Windows PowerShell
$env:CGO_ENABLED=0; $env:GOOS="linux"; $env:GOARCH="mipsle"
go build -ldflags="-s -w" -o pntgw-mipsle ./cmd/pntgw
```

The deploy *scripts* (`scripts/`) are bash and need a POSIX shell — on
Windows use WSL2 or Git Bash. See [scripts/README.md](scripts/README.md).

### Config

YAML at `/config/pntgw.yaml`, auto-created with defaults on first run:

```yaml
dish_addr: "192.168.100.1:9200"
poll_interval_ms: 1000
tcp:
  enabled: true
  bind: "0.0.0.0:10110"
udp_destinations:
  - "192.168.10.5:10110"
web:
  enabled: true
  bind: "0.0.0.0:8080"
```

Edits through the web UI rewrite this file in place and re-apply at runtime — no restart.

### NMEA sample

```
$GPGGA,104507.42,5000.6485,N,00817.1657,E,1,20,0.3,140.3,M,0.0,M,,*5B
$GPRMC,104507.42,A,5000.6485,N,00817.1657,E,0.0,0.0,150526,,,A*5B
$GPZDA,104507.42,15,05,2026,00,00*60
$GPGSA,A,3,,,,,,,,,,,,,0.3,0.3,0.3*31
```

The dish doesn't expose per-satellite information, so `$GPGSV` is intentionally omitted rather than fabricated.

### Layout

```
cmd/pntgw/main.go            entry point, supervisor, polling loop
internal/config/             YAML load/save with hot reload
internal/dish/               gRPC reflection client to the dish
internal/nmea/               sentence builders with checksums
internal/tcpsrv/             TCP fanout listener (multi-client)
internal/udpsink/            UDP unicast sender (N destinations)
internal/web/                HTTP server + embedded UI assets
Makefile                     build / erx / deploy targets
docs/edgerouter.md           full ER-X deployment guide (pntgw + cloudflared)
```

## EdgeOS deployment

See [docs/edgerouter.md](docs/edgerouter.md) for the full end-to-end build: base ER-X setup, four-LAN config, Starshield wiring, NTP, cloudflared tunnel + WARP private networks + split-tunnel surgery, pntgw service, and account-swap procedures.

## Roadmap

- MAVLink GPS injection (Pixhawk / Cube)
- Optional `/metrics` Prometheus endpoint on pntgw
