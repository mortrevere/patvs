# patvs development plan

This file is the durable implementation and handoff record for **Portable All
Terrain Video Streaming**. Update the checkboxes and session log whenever a
milestone changes state. A milestone is complete only when its acceptance
criteria pass.

## Fixed decisions

- One Go executable with `emitter`, `receiver`, and `controller` modes.
- Linux targets: `amd64`, `arm64`, and `armv7` (Raspberry Pi 4 included).
- Windows targets: `amd64` and `arm64`, published as `.exe` release assets.
- FFmpeg captures V4L2 video on Linux and DirectShow on Windows; VLC provides
  fullscreen receiver playback.
- Default video profile: MJPEG, 640x480, at most 25 fps, without audio.
- Discovery covers all active interfaces, IPv4 LANs, IPv6 link-local networks,
  and optional build-time receiver seeds for routed networks.
- TCP `7411` carries the receiver API and emitter sessions. UDP `7412` carries
  discovery. Loopback HTTP `7413` serves received MJPEG streams.
- `--secret` defaults to `patvs`; operators should set a custom value outside a
  trusted installation LAN. Discovery never contains the secret.
- Receiver state, including requested streams and selected fullscreen source,
  survives restart.
- Internet relays and NAT traversal are deferred. The session boundary must
  allow an outbound relay transport to be added without changing commands or
  video framing.

## Milestone 1: foundation and portable builds

- [x] Create the Go module and one `patvs` command with the three modes.
- [x] Define shared device identity, configuration, logging, and atomic JSON
      state helpers.
- [x] Add `scripts/build.sh` producing versioned `linux-amd64`, `linux-arm64`,
      and `linux-armv7` binaries plus SHA-256 checksums under `dist/`.
- [x] Expand the README with usage, dependencies, ports, and provisioning
      assumptions.

Acceptance:

- `go test ./...` and `go vet ./...` pass.
- All target binaries cross-compile from one command and report their embedded
  version with `patvs version`.
- Two daemon processes can use separate state files on one machine.

## Milestone 2: discovery and connectivity

- [x] Implement one discovery engine used by emitters and controllers.
- [x] Send versioned UDP probes on every useful IPv4 broadcast and IPv6
      link-local multicast interface; receivers answer by unicast.
- [x] Include loopback discovery for colocated processes and refresh promptly
      when interfaces or addresses change.
- [x] Add bounded direct IPv4 probing after multicast/broadcast discovery,
      prioritizing remembered addresses and the local `/24`.
- [x] Add build-time and runtime receiver seeds; receivers share verified,
      routable receiver hints.
- [x] Deduplicate by persistent device ID, never by address.

Acceptance:

- Peers appear within three seconds on an ordinary LAN and without DHCP over
  IPv6 link-local networking.
- Discovery continues after the first peer and handles address replacement.
- Direct probing is rate/concurrency bounded and accepts only a valid patvs
  response.

## Milestone 3: authenticated sessions and receiver state

- [x] Add versioned HTTP endpoints for health, receiver status, emitter lists,
      snapshots, stream intent, and playback intent.
- [x] Authenticate API requests and persistent emitter sessions using the
      shared secret without exposing it in discovery.
- [x] Maintain emitter registration, five-second heartbeats, a fifteen-second
      offline threshold, and jittered reconnects capped at five seconds.
- [x] Persist receiver identity, last-seen emitter metadata, desired streams,
      and playback selection with atomic writes and batched heartbeat updates.
- [x] Restore desired state after receiver restart.

Acceptance:

- Wrong secrets cannot read state or issue commands.
- Disconnects, duplicate registrations, restart, and malformed messages do not
  crash either daemon or corrupt state.
- Controller exit has no effect on receiver intent.

## Milestone 4: camera capture and video transport

- [x] Enumerate V4L2 capture devices, excluding metadata/output-only nodes.
- [x] Prefer native MJPEG at the smallest resolution meeting 640x480, use a
      stable tie-break, and fall back to the best available mode.
- [x] Run one FFmpeg process only while snapshots or streams are requested;
      pass native MJPEG through and encode raw formats when required.
- [x] Parse complete JPEG frames, cap delivery at 25 fps, and retain only the
      newest pending frame per receiver.
- [x] Fan one capture out to several receivers without allowing a slow peer to
      block or build an old-frame queue.
- [x] Serve each enabled stream on a stable loopback MJPEG URL and save fresh
      snapshots atomically on the receiver.
- [x] Keep emitters alive and retry when cameras disappear or capture fails.

Acceptance:

- The attached WSL camera captures native MJPEG at 640x480 (a three-frame
  passthrough feasibility check already passed on 2026-09-28).
- Synthetic red and green FFmpeg sources route independently; decoded
  snapshots have the expected colour.
- Fan-out, slow consumers, camera unplug/replug, and concurrent snapshots keep
  memory bounded and recover automatically.

## Milestone 5: controller CLI and TUI

- [x] Implement `receivers`, `emitters`, `status`, `snapshot`, `stream
      start|stop`, `play`, and `stop` CLI commands.
- [x] Accept stable IDs or unambiguous names and provide `--json` output.
- [x] Build a compact keyboard-driven TUI over the same API, showing receiver
      reachability, emitters, last seen, stream/playback intent, and errors.
- [x] Keep TUI refresh asynchronous so one unreachable receiver cannot freeze
      navigation.

Acceptance:

- Every TUI action has an equivalent scriptable command.
- Repeated commands are idempotent and status separates desired from observed
  state.

## Milestone 6: playback and unattended startup

- [x] Supervise one VLC process started by patvs; start, replace, stop, and
      bounded-retry it without touching unrelated VLC processes.
- [x] Prefer the active X11/Wayland session, then try installed DRM/KMS output,
      then framebuffer output when available.
- [x] Add systemd service templates for emitter and receiver and a graphical
      session environment hook.
- [x] Document device permissions, autologin desktop setup, direct display,
      HDMI, and Raspberry Pi composite provisioning.
- [x] Restore the selected stream and fullscreen playback after reboot.

Acceptance:

- A mocked player proves lifecycle supervision; Raspberry Pi hardware proves
  desktop, direct HDMI, and configured composite playback.
- Playback failure remains visible in status and does not interrupt receiving
  or snapshots.

## Milestone 7: integration and field acceptance

- [x] Automate two synthetic emitters and multiple receivers through discovery,
      auth, routing, snapshots, fan-out, restart, and reconnection.
- [x] Test malformed protocol limits, wrong secrets, changing addresses,
      multicast filtering, seed exchange, and bounded subnet probing.
- [ ] Perform two-Pi and three-device field tests across Wi-Fi, a DHCP-free
      Ethernet switch, reboots, camera removal, and route changes.
- [ ] Measure discovery, reconnect, and display latency; record hardware and OS
      versions in the session log.
- [x] Document the future relay seam and the evidence needed before choosing a
      relay protocol.

Acceptance targets on a healthy small LAN:

- Broadcast/multicast discovery within 3 seconds.
- `/24` direct-probe fallback within 10 seconds.
- Recovery within 15 seconds after connectivity returns.
- Approximately 500 ms or less camera-to-display latency, confirmed on target
  hardware.

## Session handoff log

### 2026-09-28 — initial planning and environment checks

- Confirmed the WSL kernel sees the attached integrated camera as `/dev/video0`
  plus metadata and greyscale companion nodes.
- Confirmed `/dev/video0` supports native MJPEG at 640x480 and 30 fps; FFmpeg
  captured three complete frames without re-encoding.
- Confirmed FFmpeg is installed. VLC and `v4l2loopback` are absent in WSL;
  synthetic FFmpeg inputs will cover routing tests, and Raspberry Pi hardware
  is required for direct display acceptance.
- Next action: complete Milestone 1, then implement receiver advertisement and
  shared discovery from Milestone 2.

### 2026-09-29 — foundation and first end-to-end stream

- Implemented the portable build, shared discovery, authenticated receiver API,
  persistent emitter sessions, V4L2/lavfi capture, snapshots, loopback MJPEG,
  controller commands, a terminal menu, and VLC supervision.
- `go test ./...` and `go vet ./...` pass. Static amd64, arm64, and armv7
  binaries cross-compile and the amd64 binary reports the embedded version.
- Ran one receiver plus red and green synthetic emitters. Saved snapshots
  decoded to RGB `(255,0,0)` and `(2,129,2)`. A live loopback MJPEG frame
  decoded to `(255,0,0)`. A wrong controller secret returned HTTP 401.
- Fixed a startup panic in the stream URL handler and connection flapping when
  one receiver is discovered over alternating IPv4 and IPv6 addresses.
- Next action: add bounded IPv4 direct probing and receiver hint exchange, then
  automate the synthetic integration test and test restart restoration.

### 2026-09-29 — fallback discovery, deployment, and repeatable test

- Added the delayed IPv4 direct-probe fallback. It scans each active local
  `/24` at 128 new addresses per second with at most 64 workers, short
  timeouts, and patvs health-response validation.
- Added systemd emitter/receiver units, environment configuration, runtime
  environment defaults, direct-display provisioning notes, and Raspberry Pi
  composite guidance.
- Added `scripts/integration-test.sh`; it launches a receiver and red/green
  emitters, waits for both registrations, validates snapshot pixel colours,
  and confirms that a wrong shared secret is rejected. The test passes.
- Next action: exchange verified receiver hints for routed networks, exercise
  state restoration across receiver restart, and add bounded VLC retry tests.

### 2026-09-29 — routed receiver hints

- Receivers now discover other receivers and expose only routable peer hints.
  They send the same hints to connected emitters over authenticated sessions.
- Emitters deduplicate hints by receiver ID and establish independent sessions;
  controllers recursively query and identity-check hints before presenting
  them.
- Unit, vet, and synthetic end-to-end checks pass after the change.
- Next action: extend the integration harness to two receivers, test persisted
  stream restoration after restart, and add bounded VLC retry behavior.

### 2026-09-29 — receiver lifecycle hardening

- Timed-out and cancelled snapshot requests now remove their waiter channels.
- Receiver-owned VLC playback now retries unexpected exits with exponential
  delays capped at five attempts; stop and a new play command reset the budget.
- Next action: test two-receiver fan-out and receiver restart restoration, then
  validate desktop and DRM/KMS playback on Raspberry Pi hardware.

### 2026-09-29 — remembered receivers and two-receiver recovery

- Migrated emitter state from a flat identity to identity plus receiver records;
  old state files keep their ID. Remembered addresses are probed immediately
  as seeds before delayed `/24` scanning.
- Expanded the integration harness to two receivers. Both synthetic emitters
  register with both receivers, snapshots retain their colours, and one source
  streams concurrently through both receiver loopback endpoints.
- The harness restarts the red emitter without seed arguments and verifies it
  reconnects to both remembered receivers. It then restarts one receiver and
  confirms persisted stream intent is still enabled.
- Next action: make TUI refresh non-blocking, test malformed protocol limits,
  and exercise fullscreen playback restoration with a controllable fake VLC.

### 2026-09-29 — responsive controller and protocol boundary

- TUI discovery, receiver status refresh, and actions now run asynchronously;
  terminal input remains responsive while a receiver is slow or unreachable.
- Added API tests for incorrect secrets and oversized JSON. Existing frame
  limits cap WebSocket messages and reject non-JPEG binary payloads.
- Added `docs/PROTOCOL.md` describing local wire behavior and the future relay
  boundary, including the measurements and security properties needed before
  selecting a relay protocol.
- Next action: test fullscreen intent restoration with a fake VLC and add
  targeted address-change and malformed WebSocket integration cases.

### 2026-09-29 — playback restoration

- The two-receiver harness now uses a controllable fake VLC. It starts
  fullscreen playback, verifies the receiver owns a player process, restarts
  the receiver, and confirms the saved source, stream intent, and player
  process return after emitter reconnection.
- Desktop sessions remain the default VLC output. Headless receivers select
  DRM/KMS when available, then framebuffer output.
- Next action: add malformed WebSocket and address-change cases, then move to
  Raspberry Pi field acceptance for the actual VLC display paths.

### 2026-09-29 — attached V4L2 camera path

- Ran the current patvs receiver, emitter, and controller with the USB/IP
  webcam attached to WSL. Automatic selection chose `/dev/video0`, native
  MJPEG, 640x480, and the camera's supported 30 fps input rate.
- Requested a snapshot through the controller and receiver session. `ffprobe`
  confirmed the saved image is MJPEG at 640x480; temporary daemons were then
  stopped cleanly.
- Raspberry Pi display hardware remains required for HDMI/composite and
  camera-to-screen latency acceptance.
- Added live WebSocket tests proving malformed registration closes the session
  and malformed binary frames are dropped while later heartbeats still work.
  `go test -race ./...` and the full integration harness pass.
- Corrected automatic mode ranking so meeting the 640x480 minimum takes
  precedence over compressed format; among suitable modes MJPEG remains
  preferred. Added a regression test and repeated all cross-build checks.

### 2026-09-29 — isolated network fallback

- Added `scripts/network-test.sh`, which creates two unprivileged network
  namespaces joined by a veth pair. Mismatched UDP ports suppress normal
  discovery, proving the bounded `/24` health probe finds the receiver.
- The test removes receiver address `10.55.0.1`, adds `10.55.0.3`, restarts the
  receiver, and verifies the emitter reconnects in 9 seconds.
- Found and fixed Gorilla's long default handshake delay on stale addresses;
  emitter TCP connection attempts now time out after two seconds and WebSocket
  handshakes after three seconds.
- Added a session test proving verified routable receiver hints are sent to an
  emitter. Together with malformed-frame, wrong-secret, seed, and namespace
  tests, the automated adverse-network matrix is complete.
- Remaining acceptance requires Raspberry Pi devices and real display outputs.

### 2026-09-29 — three-laptop LAN field test

- Deployed the static amd64 binary to `blue`, `red`, and `black`, all running
  NixOS 25.11 on Linux 7.1.1. Each host ran an emitter and receiver from
  `/tmp/patvs-field`; temporary Nix shells supplied FFmpeg, V4L2 utilities,
  and VLC.
- NixOS initially blocked TCP 7411 and UDP 7412. After adding temporary,
  port-specific firewall rules, an unseeded controller on `blue` discovered
  all three receivers and every receiver registered all three emitters. A
  controller in WSL used the three DNS names as seeds because WSL does not
  share the physical LAN broadcast domain.
- `blue` and `black` automatically selected `/dev/video0`, native MJPEG at
  640x480 and 30 fps. Cross-host snapshots succeeded from `black` to `blue`
  and `red`, and from `blue` to `black`. A live `black` to `blue` stream
  delivered 757,760 bytes in three seconds.
- `red` exposes 32 Intel IPU6 nodes but none reports a usable V4L2 capture
  format. Its emitter remains connected with a 0x0 camera profile and keeps
  retrying; IPU6/libcamera support remains a separate compatibility task.
- Real VLC testing found two headless-launch issues. VLC now uses its dummy
  interface so closed daemon stdin cannot terminate it, and headless playback
  requests the native `drm_vout,fb` fallback chain with framebuffer TTY
  handling disabled. On `black`, the framebuffer probe initialized at
  1920x1080 and supervised playback remained alive with a player PID.
- The temporary playback and stream requests were stopped after testing. All
  six daemons were left running on the final build. Raspberry Pi output,
  physical visual confirmation, reboots, camera removal, and latency
  measurements remain outstanding.

### 2026-09-29 — controller keyboard handling

- Replaced the line scanner and direct terminal clearing with Bubble Tea.
  Discovery and status requests remain asynchronous; arrow keys or `j`/`k`
  select rows, and single keys operate the selected emitter. Refreshes can no
  longer erase a partly typed command because the controller uses key events.
- The selected row stays with the same device ID when discovery updates
  reorder peers. The screen also shows camera errors and last-seen times.
- Updated the README with the controller keys. The CLI commands and wire
  protocol remain unchanged.
- A final process check found `red` receiver had crashed: the IPv4/IPv6 UDP
  discovery readers could still send after the shared result channel closed.
  Discovery now waits for both readers to exit before returning, so result
  closure cannot race an active sender.

### 2026-09-29 — blind private-network controller scan

- Added a controller-only RFC1918 probe pool: 256 concurrent workers, at most
  512 new TCP probes per second, and a 500 ms per-host HTTP limit. It starts
  with common private `/24`s, samples likely host addresses across the rest,
  then covers every remaining host address. The TUI reports phase, current
  subnet, completed probes, and discovered receivers.
- One-shot controller commands scan in parallel with UDP discovery for three
  seconds. From WSL, `patvs controller receivers` found `blue`, `red`, and
  `black` at `10.0.0.30`, `.19`, and `.29` without seeds in 2.9 seconds.
- This is a progressive search over 17,891,328 private addresses; uncommon
  subnets can take much longer. The scan still requires IP routing and an
  open receiver TCP port. It does not implement NAT traversal.

### 2026-09-29 — operator scan control

- TUI selection uses arrow keys only. `k` cancels the RFC1918 scan and
  periodic LAN discovery, retaining the receivers already found. Selected
  receiver status keeps refreshing. `r` still performs one explicit discovery
  refresh after the scan has stopped.

### 2026-09-29 — playback owns its feed

- The TUI now selects a receiver, then uses Enter on an emitter to start
  fullscreen VLC and its video feed together. `x` stops both. The separate
  stream toggle is removed from the TUI; the CLI stream command remains for
  clients reading the loopback MJPEG socket.
- Receiver playback no longer creates a persistent stream intent. Stopping or
  switching VLC clears the old source's stream intent and tells that emitter
  to stop capture. A standalone stream request remains possible through the
  CLI/API, and disabling one while VLC is playing cannot interrupt playback.
- Built the x64 binary and deployed it to `blue`, `red`, and `black`. Restarted
  their receivers, cleared legacy stream requests, and confirmed all three
  status APIs respond. Blue restored its Black-camera VLC playback and kept
  receiving frames with the explicit stream flag disabled; Red and Black have
  no active playback or streaming emitters.

### 2026-09-29 — refresh field and controller binaries

- A controller launched as `patvs controller` still showed the older `s`
  stream toggle because the `dist/` binary had not been rebuilt after the TUI
  change. A standalone Blue-camera stream on Red was stopped through the CLI.
- Rebuilt all three `dist/` targets from commit `3053a08`. Replaced and
  restarted both emitter and receiver processes on Blue, Red, and Black with
  the same x64 binary. All six running executable hashes match
  `fdb55ea30c1b2a01b52d20b178d55d80b266dfae38edb28cefe01685b4a8761f`,
  and all receivers see all three emitters online. Run the controller from
  `./dist/patvs-linux-amd64 controller` in this checkout to use the new keys.

### 2026-09-29 — receiver list shows active source

- The controller's receiver list now reads each discovered receiver's status
  in parallel and refreshes it every few seconds. Rows show the active emitter
  name and whether VLC is playing it, or `waiting`/`idle` when appropriate.
  Status fetches continue after the operator stops network discovery with `k`.
- Rebuilt x64, ARM64, and ARMv7 distribution binaries. Restarted both daemons
  on Blue, Red, and Black with the x64 build; all six running images match
  `42a576e57d20bf8c5c02f473887ac61ab510a0615ad2de949b5396a9668ab193`.
  All three receivers report all three emitters online.

### 2026-09-29 — restore camera probe tools on field emitters

- The emitter restart used `nix shell nixpkgs#ffmpeg` without `v4l2-ctl`.
  Auto camera selection therefore reported `no usable V4L2 capture device
  found` on every host. No leftover patvs FFmpeg capture process held a camera.
- Restart field emitters with both tools in the environment:
  `nix shell nixpkgs#ffmpeg nixpkgs#v4l-utils -c /tmp/patvs-field-bin emitter ...`.
  Blue and Black again advertise `/dev/video0` at 640x480 MJPEG/30 fps.
  Red's receiver reports active Black-camera frames for VLC. Red's own IPU6
  camera remains unavailable as before.

### 2026-09-29 — inspect local media processes from controller

- Receiver status now scans `/proc` for live FFmpeg and VLC processes and
  includes their PIDs and command lines. The selected-receiver TUI screen
  shows them, marking the VLC PID owned by that receiver. Nix's
  `.vlc-wrapped` process is recognized by its `vlc` command line.
- Rebuilt x64, ARM64, and ARMv7 binaries and restarted both daemons on Blue,
  Red, and Black. All six running images match
  `25dcaeacce95a092a67fd968c9ca51e7b79580a78809680f90925782e43475be`.
  Live status showed Blue's patvs FFmpeg plus an unrelated Jellyfin FFmpeg,
  Black's managed VLC, and an orphaned patvs VLC on Red. The Red orphan was
  stopped; Red's process list is now empty.

### 2026-10-01 — Windows port

- Added native Windows DirectShow auto-selection and named-camera capture,
  exact fractional input rates, Windows UDP broadcast sockets, LocalAppData
  state, FFmpeg/VLC executable discovery, and CIM media-process inspection.
  Windows VLC runs in a separate instance and stops via process termination.
  Emitter shutdown now waits for its cancelled capture processes to exit.
- Linux cross-builds produce the three existing Linux targets and
  `patvs-windows-amd64.exe`/`patvs-windows-arm64.exe` with one checksum file.
  Release CI runs Linux and Windows tests/vet before publishing all five.
- Linux race tests, vet, and the existing synthetic integration harness pass.
  The full Windows unit suite passes natively through WSL host interop.
  Native Windows end-to-end tests cover synthetic red JPEG snapshots, decoded
  MJPEG streams, real VLC playback/process inspection, and the integrated
  DirectShow webcam at 640x480 YUYV/30 fps. The webcam was returned to WSL.
- The Windows controller discovers Black and Blue at `10.0.0.29` and `.30`.
  After the user approved the Windows firewall prompts, the final LAN check
  on standard ports passed in both directions: both Linux cameras registered
  with Windows and delivered snapshots, Black's feed played in native Windows
  VLC, and the Windows emitter delivered snapshots to both Linux receivers.
  All temporary test processes and playback requests were stopped. Host Wi-Fi
  IPv6 is disabled, so Windows IPv6 field acceptance remains unverified.
- Windows ARM64 is cross-built and vetted; native ARM64 hardware acceptance
  remains outstanding.
- Releases also carry only the unmodified BtbN static LGPL `ffmpeg.exe` for
  each Windows architecture, with a license file and source/build references
  in the repository. Release notes use one line crediting the upstream build.
  The Linux release job downloads a pinned month-end build and rejects a
  SHA-256 mismatch before publishing. Its FFmpeg passed native Windows
  synthetic snapshots/streaming; a corrupted-download check was rejected.
