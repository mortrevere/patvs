# patvs

Portable All Terrain Video Streaming routes Linux camera feeds between
unattended machines on an installation network. One executable runs as an
emitter, receiver, or controller.

## Runtime requirements

- Linux on x86-64, ARM64, or ARMv7
- FFmpeg and `v4l2-ctl` on emitters
- VLC on receivers that drive a screen

The daemons discover receivers automatically on connected LANs. The controller
also probes RFC1918 private addresses, so it can find routed private LANs even
when local broadcast does not reach them. It checks common `/24` networks
first, then samples and eventually covers the remaining private addresses in
parallel at a capped 512 probes per second. The TUI shows scan progress;
scanning all private addresses can take hours. Seeds remain useful for faster
discovery on unusual subnets. Internet relays and NAT traversal are planned
after the local network implementation.

## Build

```sh
go test ./...
./scripts/build.sh
```

The build creates static Linux binaries and checksums under `dist/`. Optional
build metadata can be supplied without editing source:

```sh
VERSION=0.1.0 SEEDS=receiver.example.net:7411 ./scripts/build.sh
```

## Run

```sh
patvs receiver --secret installation-secret
patvs emitter --secret installation-secret
patvs controller --secret installation-secret
```

`--secret` defaults to `patvs` for immediate setup on a trusted LAN. Use a
custom shared value for an installation. Run any mode with `-h` for common
options.

The controller starts its interactive terminal interface when no command is
given. Use ↑/↓ or `j`/`k` to select a receiver, Enter to open it, and the
same keys to select an emitter. `p` starts fullscreen playback, `s` toggles
its local stream, `n` saves a snapshot, `x` stops playback, Esc goes back,
`r` refreshes, and `q` quits. The scan status stays visible at the bottom.
Scriptable commands are:

```text
patvs controller receivers
patvs controller status <receiver>
patvs controller emitters <receiver>
patvs controller snapshot <receiver> <emitter>
patvs controller stream <receiver> <emitter> start|stop
patvs controller play <receiver> <emitter>
patvs controller stop <receiver>
```

Names, full IDs, unambiguous ID prefixes, and receiver addresses are accepted.
Add `--json` before the command for machine-readable output. For repeatable
video tests, an emitter can use an FFmpeg source such as
`--camera 'lavfi:color=c=red:s=640x480:r=25'`.

## Unattended installation

Install the binary as `/usr/local/bin/patvs`, create a locked `patvs` system
user, copy the required units from `deploy/` to `/etc/systemd/system/`, and copy
`deploy/patvs.env.example` to `/etc/patvs/patvs.env`. Set the same
`PATVS_SECRET` on every member of the installation, then enable either or both
services:

```sh
systemctl enable --now patvs-emitter.service patvs-receiver.service
```

The service account needs the `video` group for cameras and the receiver also
needs `render` for direct DRM/KMS output. For desktop playback, arrange for the
graphical login session to write its `DISPLAY`, `WAYLAND_DISPLAY`,
`XDG_RUNTIME_DIR`, and `DBUS_SESSION_BUS_ADDRESS` values to
`/run/patvs/display.env`, then restart `patvs-receiver`. Without those values,
patvs asks VLC to use DRM/KMS when `/dev/dri/card0` exists and framebuffer
output when `/dev/fb0` exists.

On Raspberry Pi OS, HDMI works through the normal KMS setup. Composite output
must be provisioned before installation by enabling the composite KMS overlay
and selecting the required PAL/NTSC mode in the Raspberry Pi boot
configuration; composite and HDMI behavior depends on the Pi model and OS
release.

Run the synthetic end-to-end check on a Linux development machine with FFmpeg:

```sh
./scripts/integration-test.sh
```

On Linux hosts that permit unprivileged network namespaces, the network test
disables UDP discovery by using mismatched ports, verifies bounded `/24`
fallback, changes the receiver address, and checks reconnection:

```sh
./scripts/network-test.sh
```

The default ports are TCP 7411 for the receiver API and emitter sessions, UDP
7412 for discovery, and loopback TCP 7413 for received MJPEG streams. State is
stored below `$XDG_STATE_HOME/patvs` or `~/.local/state/patvs`; emitter and
receiver modes use separate files and can run together.

See [DEVPLAN.md](DEVPLAN.md) for milestones, acceptance criteria, and current
implementation status. [docs/PROTOCOL.md](docs/PROTOCOL.md) records the local
wire behavior and the planned relay boundary.
