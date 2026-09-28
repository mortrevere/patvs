# patvs development plan

This file is the durable implementation and handoff record for **Portable All
Terrain Video Streaming**. Update the checkboxes and session log whenever a
milestone changes state. A milestone is complete only when its acceptance
criteria pass.

## Fixed decisions

- One Go executable with `emitter`, `receiver`, and `controller` modes.
- Linux targets: `amd64`, `arm64`, and `armv7` (Raspberry Pi 4 included).
- FFmpeg captures V4L2 video; VLC provides fullscreen receiver playback.
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
- [ ] Test malformed protocol limits, wrong secrets, changing addresses,
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
