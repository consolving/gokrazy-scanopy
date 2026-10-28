# gokrazy-scanopy

[Scanopy](https://scanopy.net) network discovery daemon for
[gokrazy](https://gokrazy.org), packaged for the Banana Pi R1 router
(armv7). The module ships the official `scanopy-daemon` (Rust, built
statically for `armv7-unknown-linux-musleabihf`) plus a small Go
wrapper that configures and supervises it.

## How it works

- `scanopy-daemon` — the official Scanopy discovery daemon, staged
  into the read-only root filesystem at
  `/usr/local/bin/scanopy-daemon` via `ExtraFilePaths`.
- `cmd/scnopy` — Go wrapper (gokrazy-supervised). It reads the daemon
  API key from `/etc/scanopy/daemon-api-key`, sets the `SCANOPY_*`
  environment variables and `exec()`s the daemon. All daemon state
  (`config.json`, daemon ID) lives in `/perm/scanopy` and survives
  OTA updates.

The daemon runs in `daemon_poll` mode: it connects **out** to the
Scanopy server, pulls scan tasks and pushes results — nothing has to
be reachable on the router itself.

## Instance wiring

```json
{
    "Packages": [
        "github.com/consolving/gokrazy-scanopy/cmd/scnopy"
    ],
    "PackageConfig": {
        "github.com/consolving/gokrazy-scanopy/cmd/scnopy": {
            "CommandLineFlags": [
                "-server-url=https://scanopy.example.com"
            ],
            "ExtraFilePaths": {
                "/usr/local/bin/scanopy-daemon": "scanopy-daemon"
            },
            "ExtraFileContents": {
                "/etc/scanopy/daemon-api-key": "<daemon API key, inline>"
            }
        }
    }
}
```

`-server-url` ist die URL deiner Scanopy-Instanz; ohne Flag nimmt der
Wrapper Scanopys dokumentierten Default (`http://127.0.0.1:60072`).
`SCANOPY_INTERFACES` lässt sich per `-interfaces=` setzen — auf dem
BPI-R1 aber weglassen: die VLAN-Bridges entstehen erst nach dem Boot,
und der Daemon bindet seine Server-Verbindung sonst an ein noch nicht
existierendes Interface. Die gescannten Subnets regelt man serverseitig
in den Daemon-Einstellungen.

`scanopy-daemon` lives in this repository and is staged into the image
via `ExtraFilePaths`. The API key goes **inline** into
`ExtraFileContents` (gokrazy treats that value as file content, not as
a filename) — keep the real key out of git by maintaining it in
`~/gokrazy/bpi-r1/config.json` only.

## Getting the API key

1. Open the Scanopy UI → **Discover → Daemons → Create Daemon**
2. Setup: OS *Linux*, site, polling mode *daemon connects to server*
   (`daemon_poll`), name e.g. `bpi-r1`
3. Copy the API key (shown only once) into `daemon-api-key`

### VLAN-Sync (optional, empfohlen)

Der zweite Service `cmd/vlansync` hält die VLAN-Modelle von Scanopy
aktuell: er liest die VLAN-Konfiguration des Routers
(`/etc/gokrazy-router.json`, staged aus der Instanz), wartet bis die
Bridges und das Netz stehen und legt in Scanopy die VLANs samt
Subnet- und Interface-Verknüpfung an (physische Darstellung). Das
braucht einen **User**-API-Token (Daemon-Keys dürfen VLANs nicht
anfassen) — Settings → API Keys, inline in `ExtraFileContents`:

```json
"github.com/consolving/gokrazy-scanopy/cmd/vlansync": {
    "CommandLineFlags": [
        "-server-url=https://scanopy.example.com",
        "-wifi-vlan=31"
    ],
    "ExtraFileContents": {
        "/etc/scanopy/user-api-key": "<user api token, inline>"
    }
}
```

`-wifi-vlan=31` ordnet das WLAN-Subnet (10.0.31.0/25) dem gleichen
VLAN zu wie das lan4-Subnet (10.0.31.128/25). Der Sync läuft alle
30 Minuten und verträgt sich mit dem Daemon.

## Rebuilding the daemon binary

There are no official `armv7` release assets upstream, so the binary
is cross-built with cargo-zigbuild:

```bash
git clone --depth 1 -b v0.18.1 https://github.com/scanopy/scanopy /tmp/scanopy-src
docker run --rm -v /tmp/scanopy-src:/src -v /tmp/scanopy-out:/out \
  -v scnopy-cargo:/usr/local/cargo -w /src/backend rust:bookworm bash -c '
    apt-get update -qq && apt-get install -y -qq llvm python3 python3-pip
    pip install --break-system-packages ziglang cargo-zigbuild
    rustup target add armv7-unknown-linux-musleabihf
    cargo zigbuild --release --target armv7-unknown-linux-musleabihf --bin daemon
    llvm-strip target/armv7-unknown-linux-musleabihf/release/daemon
    cp target/armv7-unknown-linux-musleabihf/release/daemon /out/scanopy-daemon-armv7
'
cp /tmp/scanopy-out/scanopy-daemon-armv7 scanopy-daemon
```

Pin the daemon version to the Scanopy **server** version deployed at
`<your-scanopy-server>` (currently 0.18.1) to avoid protocol
drift.
