# patvs local protocol and relay seam

patvs version 1 uses three network surfaces:

- UDP discovery packets contain `magic`, protocol version, packet kind, and a
  receiver's persistent ID, display name, and API port. They never contain the
  shared secret.
- The receiver HTTP API carries controller commands and status. Every endpoint
  except `/v1/health` requires `Authorization: Bearer <secret>`.
- An emitter opens an authenticated WebSocket at `/v1/session`. JSON text
  messages carry registration, heartbeat, demand, snapshot, and receiver-hint
  control data. Each binary message is one complete JPEG frame, limited to 8
  MiB. A receiver keeps only the newest frame per stream consumer.

`PUT /v1/playback` selects one emitter for local VLC playback and requests its
feed. `DELETE /v1/playback` stops VLC and releases that feed; selecting another
emitter releases the previous one. `PUT /v1/streams/{id}` is a separate intent
for clients reading the receiver's loopback MJPEG socket directly. Disabling
that intent does not interrupt an active VLC selection.

`DELETE /v1/emitters/{id}` forgets an offline emitter and persists removal of
its stream/playback settings. It stops VLC if that emitter was selected and
discards its cached frame; saved snapshots remain. Success returns `204`, an
unknown ID returns `404`, and an online emitter or one with an active session
returns `409`. Forgetting does not block a later emitter registration.

`GET /v1/status` includes a fresh `processes` list of running local FFmpeg and
VLC processes. Each entry has a PID, process name, and command line. The list
may include processes unrelated to patvs; `player_pid` identifies the VLC
process owned by this receiver.

Linux and Windows use the same version 1 wire protocol. Windows cameras report
`dshow:<device name>` and may include the optional `camera.frame_rate` string
to preserve a fractional DirectShow input rate; `camera.fps` remains an
integer for existing clients. Receivers do not need to interpret the capture
device or format to route JPEG frames.

The persistent ID identifies a device. Addresses are replaceable connection
candidates and must never become database keys. Messages are versioned at the
discovery boundary; incompatible protocol versions are ignored.

## Future Internet relay

The relay boundary is the emitter-to-receiver session implemented by
`connectEmitter` and `handleSession`. A relay transport must preserve the same
ordered JSON controls and individual binary JPEG frames. Discovery may return
a relay route beside direct addresses, but controller commands and capture
logic should remain unchanged.

Before choosing a relay protocol, measure installation bandwidth, concurrent
streams, camera-to-screen latency, reconnect behavior on mobile links, and
whether relay operators require end-to-end encryption. The first relay should
use outbound connections from both NATed sides, authenticate device IDs in
addition to the installation secret, encrypt transport, bound queues exactly
as the direct session does, and allow direct LAN sessions to remain preferred.
