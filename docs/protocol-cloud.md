# FTW Cloud protocol

Proposed with [ADR 0008](adr/0008-ftw-cloud.md). Nothing here has shipped.
This document is normative: "must" is a requirement on the box, the service
or the phone. Byte-level test vectors land with the code, in this repository
and in `srcfl/ftw-cloud`. Until then the text below is the reference, and the
vectors described in [Freshness](#freshness) are the cases they must cover.

The app protocol, which runs inside Noise between a phone and its box, is
specified in
[srcfl/ftw-webapp docs/protocol.md](https://github.com/srcfl/ftw-webapp/blob/main/docs/protocol.md).
FTW Cloud reuses its frame layout, CBOR rules and tile format, and changes
nothing in it. Lane 0 and the relay stay as they are. The Go types for both
live in `go/protocol`, under Apache-2.0
([#1588](https://github.com/srcfl/ftw/pull/1588)).

## Parties and connections

- **Box:** FTW Core with an FTW Cloud grant.
- **Service:** `ftwcloudd`, at `cloud.ftw.energy`.
- **Phone:** the iPhone app, paired with the box over Noise as today.
- **Extension:** a widget, notification service extension or Live Activity
  on that phone.

| Connection | Opened by | Transport | What protects it |
|---|---|---|---|
| Uplink | Box | `wss://cloud.ftw.energy/v1/box` | TLS 1.3, a handshake bound to the TLS session, every box message signed |
| App session | Phone | `wss://cloud.ftw.energy/v1/app` | TLS, then Noise IK to the service's static key |
| Extension read | Extension | `GET https://cloud.ftw.energy/v1/now` | TLS and an extension token; the data inside is box-signed |
| Push | Service | APNs | Box-signed payloads where they carry data |

**Uplink rules.**

- The endpoint is a compile-time constant in Core, as the relay's is.
- TLS 1.3 is the minimum. The dialer uses no proxy, whatever the environment
  says.
- Reconnects back off with full jitter from 0.5 s to 60 s. The attempt
  counter resets after 60 s connected.
- The service sends a WebSocket ping every 15 s. The box's read deadline is
  45 s and its write deadline 10 s.
- WebSocket compression is off.
- A box keeps one uplink. A second one from the same home replaces the first.

## Frames

Each WebSocket binary message carries exactly one frame in the app
protocol's layout:

```
offset  field    type     value on FTW Cloud
0       ver      u8       1
1       lane     u8       1, always
2       flags    u8       0
3       rsvd     u8       0
4       len      u16 BE   payload bytes
6       payload  u8[len]  CBOR envelope
```

- **No padding.** The message is exactly `6 + len` bytes. Lane 0 never
  appears on an FTW Cloud connection.
- A receiver closes the connection on a lane 0 frame, a nonzero flag, bytes
  after `len`, or a text message.
- Inside the app session the same frames ride as Noise transport messages,
  also without padding.

Lane 0 is padded to a fixed size and cadence because the relay must learn
nothing from the stream. The service reads the content, so padding would hide
only timing from the network, at about three times the data. The network sees
when bursts happen, and the privacy notice says so (ADR 0008, decision 5).

## Envelope and signature

```
Envelope = { t: text, b: bstr, sig: bstr }
```

- CBOR, core deterministic encoding, text keys. No `id`: nothing on this link
  is a request with a reply id.
- `b` is the body, itself deterministic CBOR, carried as a byte string so that
  the signature covers exact bytes and the service can forward them unchanged.
- `sig` is ES256 (ECDSA on P-256 with SHA-256), 64 bytes, `r ‖ s`
  big-endian.
- **Signed input:** the bytes `ftw.cloud.v1\0` (12 ASCII bytes and a zero
  byte), then the deterministic CBOR encoding of the array `[home, t, b]`,
  where `home` is the text home id.
- The box signs with `cloud.key`. The service signs `hello_ok` and `purged`
  with its service key. Every other envelope the service sends has an empty
  `sig`.
- A receiver verifies over the bytes it received, never over a re-encoding.
- The box's public key reaches phones over Noise, beside the table of opaque
  ids, in the session information every role receives. It never comes from
  the service.
- Unknown keys in a body are ignored, as in the app protocol. An unknown `t`
  from the box is dropped and counted by the service, so a newer box works
  with an older service. An unknown `t` from the service closes the uplink.
- The service forwards a home's messages in the order it received them.

## Keys and ids

| Name | What it is |
|---|---|
| `cloud.key` | ES256 (P-256) key the box makes when an owner grants FTW Cloud. Never backed up |
| `pk` | Its public key as a 65-byte uncompressed SEC1 point |
| `home` | Lowercase base32, no padding, of the first 16 bytes of SHA-256(`ftw.cloud.home.v1` ‖ `pk`). 26 characters |
| Service key | P-256. Core carries up to two public halves, so the key can rotate across a release |
| Service Noise key | X25519 static key. The app carries up to two |
| `grantKey` | 32 random bytes in the grant record. A new grant draws a new one |
| Opaque source id | First 8 bytes of HMAC-SHA256(`grantKey`, `src\0` ‖ driver name), base64url without padding |
| Opaque loadpoint id | First 8 bytes of HMAC-SHA256(`grantKey`, `lp\0` ‖ loadpoint id), base64url without padding |

A home id comes only from the key that signed `hello` on this connection,
never from a body field.

## Common body fields

Every body the box sends carries these four fields. The class tables below do
not repeat them.

| Field | Type | Meaning |
|---|---|---|
| `seq` | uint | Starts at 1 and rises by 1 per message, per boot |
| `boot` | bstr, 16 B | Random, drawn at each Core start |
| `uptimeMs` | uint | Box monotonic time since start, at send |
| `wallMs` | uint | Box wall time at send. Present only while NTP has synced the box clock |

## Handshake

1. **Service → box: `challenge`** `{v: 1, nonce: bstr32}`, unsigned.
2. **Box → service: `hello`**, signed. The service computes `home` from `pk`,
   checks the signature and that `nonce` is its own, or closes.

   | Field | Type | Meaning |
   |---|---|---|
   | `v` | uint | 1 |
   | `pk` | bstr, 65 B | The box's public key |
   | `nonce` | bstr, 32 B | Echo of the challenge |
   | `boxNonce` | bstr, 32 B | Random, for the service to sign |
   | `build` | text | Core version |
   | `clock` | text | `ntp` or `none` |
   | `caps` | [text] | Uplink features this box speaks |
   | `rosterSeq` | uint | The box's current roster number |
   | `grantId` | bstr, 16 B | The grant this connection serves |

3. **Service → box: `hello_ok`**, signed by the service key.

   | Field | Type | Meaning |
   |---|---|---|
   | `boxNonce` | bstr, 32 B | Echo |
   | `ekm` | bstr, 32 B | The TLS exporter value (RFC 8446 §7.5), label `EXPORTER-ftw-cloud-v1`, empty context |
   | `have` | map text→text | Tile id to etag, for the tiles the service holds |
   | `rosterSeq` | uint | The roster number the service holds |

   The box checks the signature against a service key it carries, that
   `boxNonce` is its own, and that `ekm` equals its own exporter value for
   this TLS session. A proxy that ends TLS in the middle has a different
   exporter value. If any check fails, the box closes and backs off. **It
   sends nothing but `hello` before this check passes.** This is why the
   service must end TLS itself rather than behind a proxy.
4. **Box → service:** `roster`, when the service's number is lower; then one
   full message of each class the grant allows; then the tiles missing from
   `have`.
5. After that the box sends on change, by the cadence rules.

## Classes

The box sends these. Each class belongs to one scope, and the service
forwards a class only to devices whose effective scope includes it. These
tables are the allowlist: a field the box sends must have a row here, and a
test fails on any field without one. Driver names, device capabilities and
inventory have no rows.

### `live`: `ftw.live.read`

| Field | Type | Meaning |
|---|---|---|
| `fields` | map uint→int | Frozen field ids 1–6 and 10, in the registry's units |
| `fieldSources` | map uint→[text] | The sources each field's freshness follows |
| `sources` | map text→Source | The source table. Sent when `srcRev` changed since the box's previous `live`, and in every full message |
| `srcRev` | uint | Rises by 1, per boot, each time the source table changes |
| `dispatchBlockedBy` | [text] | Sources whose staleness stops dispatch |

**Source**

| Field | Type | Meaning |
|---|---|---|
| `kind` | text | As in the app protocol |
| `state` | text | `live`, `lagging`, `stale`, `down` or `never`, as the box grades it |
| `lastOkMs` | int | Box uptime at the last good reading |
| `staleAfterMs` | uint | How long a reading from this source stays current |

### `lp`: `ftw.live.read`

| Field | Type | Meaning |
|---|---|---|
| `controlRev` | uint | The box's control revision, as a `cmd` guard needs it |
| `loadpoints` | [Loadpoint] | One per loadpoint |
| `feedback` | [Feedback] | One per device function FTW commands |

**Loadpoint**

| Field | Type | Meaning |
|---|---|---|
| `id` | text | Opaque loadpoint id |
| `plugged` | bool | A car is connected |
| `socPermille` | uint or null | Car charge level, null when unknown |
| `socSource` | text | `vehicle`, `inferred` or `assumed`; empty when unplugged |
| `socAgeS` | uint or null | Seconds since the level was last confirmed, at send |
| `targetPermille` | uint or null | The level to reach |
| `readyByMs` | int or null | Wall time the car should be ready |
| `recurring` | bool | The target comes from a weekly schedule |
| `powerW` | int | Charging power, positive while charging |
| `sessionWh` | uint | Energy delivered this session |
| `session` | text | Session id; changes at each plug-in |
| `manual` | Manual or null | Manual charging state, null when none |
| `commandedW` | int or null | Power last commanded |
| `commandedReason` | text | Stable code for that command |
| `surplusOnly` | bool | Charging from surplus solar only |
| `gridDeferred` | bool | Grid charging deferred by the plan |
| `planPending` | bool | A replan is due |
| `boost` | bool | Battery boost is on |
| `nextStartMs` | int or null | Wall time the next planned charge starts |
| `nextEndMs` | int or null | Wall time it ends |
| `nextWh` | uint or null | Energy it plans to deliver |

**Manual**

| Field | Type | Meaning |
|---|---|---|
| `state` | text | The manual state |
| `sinceAgeMs` | uint | Time in that state, at send |
| `requestedW` | int or null | Power the user asked for |
| `commandedW` | int or null | Power commanded for it |
| `reason` | text | Stable code |

**Feedback**

| Field | Type | Meaning |
|---|---|---|
| `kind` | text | The device function, such as battery or charger |
| `src` | text | Opaque source id |
| `status` | text | `following`, `waiting`, `limited`, `not_following`, `no_contact` or `not_controlled` |
| `reason` | text | Stable code |
| `severity` | text | As on the box |
| `evidence` | text | `none`, `accepted`, `measured` or `confirmed` |
| `fresh` | bool | The readings behind the status are fresh |
| `actualW` | int or null | Measured power |
| `ageMs` | uint or null | Age of that reading by box uptime, at send |

The change test ignores `socAgeS`, `sinceAgeMs` and `ageMs`. The phone adds
the time since the message.

### `prices`: `ftw.live.read`

| Field | Type | Meaning |
|---|---|---|
| `zone` | text | Price zone |
| `currency` | text | ISO 4217 code |
| `t0Ms` | int | Wall time the first slot starts |
| `slotMs` | uint | Slot length. Where stored slots differ in length, the box repeats a longer slot's price across `slotMs` slots |
| `spotMinor` | [int or null] | Market price, minor units per kWh |
| `totalMinor` | [int or null] | What the household pays, tariff and VAT included |
| `stale` | bool | The slots do not cover the window the box holds prices for |

`totalMinor` shows the household's tariff. The privacy notice says so. The box
sends `prices` only when a hash of `t0Ms`, `spotMinor` and `totalMinor`
changes.

### `plan`: `ftw.plan.read`

| Field | Type | Meaning |
|---|---|---|
| `decisionId` | text | The plan's id. The box sends `plan` only when it changes |
| `genMs` | int | Wall time the plan was made |
| `stale` | bool | The planner could not run |
| `fallback` | bool | Core's Go fallback made this plan, not the solver |
| `t0Ms` | int | Wall time the first slot starts |
| `slotMs` | uint | Slot length |
| `batteryW` | [int or null] | Battery power per slot, site convention |
| `gridW` | [int or null] | Grid power per slot |
| `socPermille` | [uint or null] | Planned battery level at slot end |
| `evW` | [int or null] | Planned charging power |
| `pvLimitW` | [int or null] | Planned solar limit, null where none |
| `priceMinor` | [int or null] | Import price, minor units per kWh |
| `reason` | [text] | Stable code per slot |

The grid ceiling the plan defends is not sent.

### `health`: `ftw.health.read`

| Field | Type | Meaning |
|---|---|---|
| `ok` | uint | Drivers working |
| `degraded` | uint | Drivers working with faults |
| `faulted` | uint | Drivers failing |
| `offline` | uint | Drivers not answering |

### `event`: `ftw.events.read`

| Field | Type | Meaning |
|---|---|---|
| `eventId` | text | Unique per event |
| `kind` | text | A kind from `contract/push-catalogue.yaml` |
| `severity` | text | As on the box |
| `args` | map text→(text or int) | The catalogue's placeholders |
| `lp` | text | Opaque loadpoint id, when the event concerns one |

No argument carries a driver or device name. Where the catalogue text names a
device, the box sends `src`, an opaque source id, and the phone fills in the
name from its table. The box sends an event after its own rules (enabled,
cooldown). Delivery is best effort.

### `tiles`: `ftw.history.read`

The body is the app protocol's `hist.chunk` body: `tileId`, `etag`, `res`,
`startMs`, `stepMs`, `series`, `data` and `partial`, with the same meaning,
tile ids, etags and bytes as the box serves over Noise.

- `res` is `5m`. The service keeps 48 hours.
- `series` is drawn from `grid_w`, `pv_w`, `battery_w`, `load_w`, `ev_w` and
  `battery_soc`.
- A closed tile goes whole. When a 5-minute bucket closes, the box appends to
  the trailing tile: `partial` is true and `startMs` is the first new bucket.

### `hb`: every device in the roster

| Field | Type | Meaning |
|---|---|---|
| `clock` | text | `ntp` or `none` |
| `srcRev` | uint | The source table revision now in force |

### Service-only classes

The service never forwards these to a phone.

**`roster`**

| Field | Type | Meaning |
|---|---|---|
| `rosterSeq` | uint | Persisted on the box. Rises by 1 at each pair, role change, revoke or grant change |
| `ttlS` | uint | 86,400. Counted from when the service received the roster |
| `devices` | [Device] | Every device paired with the box |
| `grant` | Grant | What the owner granted |

**Device**

| Field | Type | Meaning |
|---|---|---|
| `id` | text | The box's device id |
| `key` | bstr, 32 B | The device's X25519 Noise static key |
| `role` | text | `owner` or `viewer` |
| `scopes` | [text] | Scopes the box derives from the role. The service holds no role table |
| `epoch` | uint | The device's epoch on the box |

**Grant**

| Field | Type | Meaning |
|---|---|---|
| `id` | bstr, 16 B | Grant id |
| `scopes` | [text] | A subset of `cloud_scopes` |
| `purposes` | [text] | A subset of `cloud_purposes` |
| `controller` | text | The data controller, set by the pairing flow |

**`stats`**, only while `grant.purposes` contains `stats`.

| Field | Type | Meaning |
|---|---|---|
| `statsId` | bstr, 16 B | Random, drawn by the box at consent |
| `policy` | text | Version of the consent text the owner accepted |
| `zone` | text | Price zone |
| `has` | [text] | Which of `battery`, `pv` and `ev_charger` the site has |
| `hours` | [Hour] | At most 168 per message |

**Hour.** `startMs`, then Wh totals for the site, rounded to 10 Wh, under the
energy ledger's flow names: `grid_import`, `grid_export`, `pv_generation`,
`battery_charge`, `battery_discharge`, `consumer_use`, `vehicle_charge` and
`vehicle_discharge`. A flow the site lacks is absent.

**`stats.withdraw`** `{statsId}`. The service deletes that id's rows within
24 hours.

**`bye`** `{why, purge}`. `why` is `revoked`, `shutdown` or `update`.
`purge: true` asks the service to delete the home. `why: "update"` holds
back `box.unreachable` until the box returns or an hour passes.

## Cadence

- The box keeps the latest value per class and sends a class only when its
  content changed. The change test ignores `lastOkMs` in sources, as the app
  protocol's source comparison does, and the age fields named under `lp`.
- Each class goes at most once per period: 5 s by default, 1 s while a
  `watch` is in force. `lp` goes at most every 2 s until a Pi 4 bench shows
  what composing it costs.
- `plan` goes only when `decisionId` changes, `prices` only when its hash
  changes, `tiles` when a bucket closes, and `event` at once.
- Every 15 minutes, and after a `resync`, the box sends a full `live` with
  `sources` and a full `lp`.
- **Heartbeat.** The box sends `hb` whenever neither `live` nor `hb` has left
  for H, where H = clamp(the smallest `staleAfterMs` among sources ÷ 2,
  2 s, 15 s), and 15 s when there are no sources.
- A source whose `staleAfterMs` is under 4 s can show lagging between
  heartbeats. The phone cannot see faster than the heartbeat, so that is
  honest.

## Service to box

The service may send the box only these. Anything else, a frame over 4 KiB,
a text message or a lane 0 frame closes the uplink. A fuzz test covers it.

| `t` | Body | Rule |
|---|---|---|
| `challenge` | `{v, nonce}` | First message. Unsigned |
| `hello_ok` | See Handshake | Signed by the service key |
| `watch` | `{hz: 1}` | Raises the period to 1 s for 60 s. The service resends it while a phone asks. Without it the box returns to 5 s |
| `resync` | `{what, tiles}` | `what` holds `full`, `tiles` or `roster`; `tiles` lists tile ids. The box honours at most one per 5 minutes and ignores the rest |
| `purged` | `{atMs, what}` | Signed by the service key. `what` is `home` or `stats`. Sent once the deletion is done |

The box acts on none of these beyond what the table says. It takes no time,
config, update or instruction from the service.

## Backpressure and limits

- One writer per socket. While a write blocks, the newest value per class
  replaces the one waiting. Events wait in a ring of 32; when it is full the
  oldest drops and the box counts the drop.
- A write blocked for 10 s closes the socket.
- A reconnect sends full state and the missing tiles. It replays no old
  `live`, `lp` or `event`.
- **Byte budget.** The box counts the bytes it sent over a rolling 24 hours
  of uptime. Past 20 MB, four times the measured 5 MB, it sends only `hb`,
  `event`, `roster`, `stats.withdraw` and `bye` until the count falls. `GET
  /api/cloud` shows the count and whether the budget is reached.

## Roster

- The box sends a roster at connect when the service's number is lower, on
  every change within one control tick, and every 6 hours to renew `ttlS`.
- The service accepts a roster whose `rosterSeq` is at least the one it
  holds. An equal number renews `ttlS`. A lower one is ignored.
- When `ttlS` runs out with no newer roster, the service serves nothing for
  the home and drops its sessions and tokens.
- **Effective scope** of a device is its `scopes` intersected with
  `grant.scopes`.
- A device that leaves the roster, or whose `epoch` changes, loses its
  sessions, tokens and APNs registrations within a second of the service
  receiving the roster.
- The service learns devices only from the roster. It never adds one.

## App session

1. The phone opens `wss://cloud.ftw.energy/v1/app` and runs
   `Noise_IK_25519_ChaChaPoly_SHA256` as initiator. The responder's static key
   is a service Noise key built into the app. The prologue is
   `ftw.cloud.v1:` followed by the home id.
2. The service accepts the handshake only if the phone's static key is in the
   home's current roster and not suspended.
3. Handshake message 1 carries `now {hz}`. Message 2 carries, for each class
   the device may read, the newest box envelope the service holds, the newest
   `live` that carried `sources`, the newest `hb`, and the service's own
   `{lastSeenMs, online}`.
4. The service then forwards each new box envelope the device may read,
   unchanged, as unpadded lane 1 frames.

**Phone to service.** The service acts on these and ignores other types.

| `t` | Who | Effect |
|---|---|---|
| `watch` | Any device | Ask for 1 s updates for 60 s |
| `token.get` | Any device | Returns `token {token, expiresInS}` for this device |
| `push.register` | Any device | `{apnsToken, environment, kinds}`; `kinds` from `alert`, `widgets`, `liveactivity` |
| `push.unregister` | Any device | Removes this device's APNs token |
| `suspend` | Owner | `{deviceId}`. Suspends a device |
| `unsuspend` | Owner | `{deviceId}`. Lifts a suspension |

A suspension can only narrow access, never beyond what the roster allows. It
holds until an owner lifts it or the roster drops the device, and it works
while the box is offline. Anything the service says itself, such as
`lastSeenMs`, carries no box signature, and the phone never shows it as a
reading.

## Extension token

- 32 random bytes, base64url. The service stores its SHA-256, never the token.
- Bound to one roster entry and valid for 7 days. The app renews it at each
  foreground and background refresh.
- It dies when the roster drops the device, the device's epoch changes, an
  owner suspends the device, or the grant ends.
- `GET /v1/now` with `Authorization: Bearer <token>` returns CBOR
  `{envelopes, lastSeenMs}`: the newest `live`, the newest `live` with
  `sources`, `prices` and `hb`, as the box signed them.
- A token never reads `lp`, `plan`, `event`, `tiles` or `health`. A test
  proves it.
- An extension checks signatures with the box key, and ages data with the
  anchor, that the app stored in its App Group.

## Push

| Type | Payload | Phone behaviour |
|---|---|---|
| `alert` | `aps` with the generic title "FTW" and `mutable-content: 1`, plus `home` and the `event` envelope in base64 | The extension checks the signature, drops an already seen `(boot, seq)`, renders text from the app's catalogue copy and picks the category, and so the buttons, from the verified `kind`. A failed check shows a generic line with no buttons |
| `widgets` | Content changed, no data | Widgets fetch with their token |
| `liveactivity` | `home` and the `lp` envelope in base64 | The view shows numbers only if the signature checks and the age is within the charger source's `staleAfterMs`; otherwise "Open FTW". `staleDate` follows the box age |
| `box.unreachable` | Generic, from the service | Sent after 600 s without the box's socket. No box signature, no arguments, no buttons |

Each push goes only to devices whose effective scope covers what it carries:
`alert` needs `ftw.events.read`, and `widgets` and `liveactivity` need
`ftw.live.read`. `box.unreachable` goes to devices registered for `alert`.
The service sends `widgets` when tomorrow's prices arrive and when a car is
plugged in. It derives no category, collapse id or priority from an event's
kind.

## Freshness

Carrier state `cloud` labels data read through the service. Source states
stay the box's, and the rules below can only make them worse.

**Accepting a message.** The phone accepts a box envelope only if its
signature verifies with the box key for that home. Within one boot it drops a
message whose `seq` is not above the highest it has accepted. A message from
another boot replaces the current boot only if it is anchored and its send
time is later than the newest message the phone holds.

**Anchors.** The phone trusts a send time only by one of two rules.

- **Rule N.** The message has `wallMs`. Send time = `wallMs`. If any message
  of a boot arrives with `wallMs` more than 2 s ahead of the phone clock, the
  phone stops using rule N for that boot.
- **Rule U.** During a Noise session with the box, the phone records phone
  time minus box uptime for messages that carry the box's `boot` and
  `uptimeMs`, and keeps the smallest offset per boot. Send time = offset +
  `uptimeMs`. This needs the box's `boot` in the Noise session information.
- When both apply, the phone uses the earlier send time. When neither does,
  the data is unanchored: it shows as "cloud, age unverified", at best
  `stale`, and never `live`.

**State.** Let T be the send time of the newest accepted, anchored `live` or
`hb`, and t = phone time − T, at least zero.

- If the latest accepted `live` or `hb` names a `srcRev` newer than the
  source table the phone holds, every source is `stale` until that table
  arrives.
- Otherwise, for each source with box state B and `staleAfterMs` S, the time
  grade is: no change while t ≤ S, `lagging` while S < t ≤ 2S, and `stale`
  when t > 2S.
- The effective state is the worse of B and the time grade, in the order
  `live`, `lagging`, `stale`, `down`, `never`.
- Fields follow their sources through `fieldSources`, as on Noise.

The service cannot sign, so it cannot make a source look fresher. It can only
delay, drop or replay, and each of those ages the data.

**Vectors.** Each case below becomes a byte vector, replayed in Go and Swift.

1. **A quiet live source stays live.** One source, S = 10 s, state `live`,
   `srcRev` 7, so H = 5 s. The box sends a full `live` at uptime 0, then only
   `hb` naming `srcRev` 7 every 5 s for 120 s, under rule N with 100 ms in
   transit. At every moment t ≤ 5.1 s, and the source shows `live`.
2. **A dead uplink ages on time.** As case 1, but the socket dies after the
   `hb` at uptime 60 s. The source shows `live` up to t = 10 s, `lagging` from
   just past 10 s, and `stale` from just past 20 s.
3. **A replay goes stale.** The service replays case 1's first `live` and
   `hb` an hour later to a phone that has seen nothing from that boot. Their
   send time is an hour old, so the source shows `stale`. A phone that holds a
   higher `seq` from that boot drops them.
4. **A withheld table goes stale.** The box moves the source to `lagging` with
   `srcRev` 8, and the service drops that `live`. The next `hb` names
   `srcRev` 8. The phone holds revision 7, so the source shows `stale` until
   revision 8 arrives.
5. **No anchor, never live.** The box reports `clock: "none"`, and the phone
   has had no Noise session. The data shows as "cloud, age unverified" and
   never `live`.
6. **A fast box clock loses rule N.** A message arrives with `wallMs` 3 s
   ahead of the phone clock. Rule N stops for that boot, and without rule U
   the data is unanchored, as in case 5.
7. **A changed byte is dropped.** The service flips one byte of `b`. The
   signature fails, the phone drops the message, and the source ages from the
   last good message, as in case 2.
8. **The box's state wins.** The box reports a source as `down`, and t = 1 s.
   The source shows `down`.

## Grant on the box

The grant lives in the box's state store under `cloud/grant_v1`, not in the
config document, so a whole-document config write cannot reset it. It holds
the grant id, scopes, purposes, a fingerprint of `cloud.key`, `grantKey`, the
granting device, a hash of the policy text, the time or `clock-unknown`, the
data controller and, with statistics on, the `statsId`.

- A missing record, or a fingerprint that does not match `cloud.key`, reads
  as **off**, and the box says so. This is the opposite of `app_link` and
  `fleet_ping`, where a missing section reads as on.
- `cloud.key` is never backed up. A restored box has FTW Cloud off until an
  owner turns it on again, under a new home id.
- A grant works with the relay off.

| Route | Who may call it |
|---|---|
| `GET /api/cloud` | Owner. State, scopes, bytes sent in the last 24 hours of uptime, whether the byte budget is reached |
| `GET /api/cloud/preview` | Owner. The exact bytes that leave the house |
| `POST /api/cloud/grant` | Owner over an app session. On the LAN, only from loopback or with the house password |
| `POST /api/cloud/stats` | Same gate, as a separate action |
| `DELETE /api/cloud/grant`, `DELETE /api/cloud/stats` | Any door, because narrowing access is safe |

Every change pushes `cloud.granted` or `cloud.revoked` to all owners and
writes an audit line on the box. The box cannot verify the app's step-up, so
the grant screen claims no more than an owner session.

## Revocation and deletion

- **Device revoke** on the box: a roster without the device. See Roster.
- **Grant revoke:** the box sends `bye {why: "revoked", purge: true}`. The
  service deletes the home: registry row, tokens, APNs registrations, live
  copy, events, tiles and statistics rows. It sends `purged {what: "home"}`
  and closes. The box keeps `cloud.key` until it holds a valid `purged`,
  retrying at each connect, then deletes the key and the grant and shows the
  receipt time.
- **Statistics withdrawal:** `stats.withdraw`. The service deletes the rows
  within 24 hours and sends `purged {what: "stats"}` when done, on the same or
  a later connection.
- **Silence:** after an hour the service serves only `lastSeenMs` and no
  readings. After 30 days it drops tokens and APNs registrations. After 90
  days without a valid roster it deletes the home as in a purge.
- **A new home** that sends no roster within 24 hours is deleted.

## Service state

Per home, the service keeps in memory the newest envelope of each class, the
newest `live` that carried `sources`, 48 hours of tiles and a one-hour ring of
events. It keeps no history of readings and writes none to disk. Its registry
holds the home id, the box key, the roster, the current `statsId`, APNs
tokens and token hashes.

## Registry names

The contract registry does not hold these names yet. A separate pair of pull
requests adds them in `srcfl/ftw-webapp` first, then copies them here and to
`srcfl/ftw-app`.

| Name | Kind | Goes into |
|---|---|---|
| `ftw.events.read` | Scope: events behind notifications | `registry.yaml`, `scopes` |
| `cloud_scopes` | List: `ftw.live.read`, `ftw.history.read`, `ftw.plan.read`, `ftw.health.read`, `ftw.events.read` | `registry.yaml`, new block |
| `cloud_purposes` | List: `serve`, `stats` | `registry.yaml`, new block |
| `cloud.link` | Capability: the box can run the FTW Cloud uplink. Absent means the app offers no FTW Cloud | `registry.yaml`, `capabilities` |
| `cloud` | Carrier state, between `relay` and `cache` | `registry.yaml`, `carrier_states` |
| `cloud.granted`, `cloud.revoked` | Push kinds, sent to every owner when the grant changes | `push-catalogue.yaml` |

Class names, field names and the signing tags are not registry names. They
live here and in `go/protocol`, held by golden vectors in both repositories.

## Numbers in one place

| What | Value |
|---|---|
| Reconnect backoff | 0.5 s to 60 s, full jitter |
| Ping, read deadline, write deadline | 15 s, 45 s, 10 s |
| Period per class | 5 s; 1 s while watched; `lp` at least 2 s |
| `watch` lasts | 60 s |
| Heartbeat H | clamp(smallest `staleAfterMs` ÷ 2, 2 s, 15 s) |
| Full state | Every 15 minutes |
| `resync` honoured | At most one per 5 minutes |
| Event ring on the box | 32 |
| Byte budget | 20 MB per 24 hours of uptime |
| Largest frame from the service | 4 KiB |
| Roster `ttlS`, renewal | 24 hours, every 6 hours |
| Extension token | 7 days |
| `box.unreachable` after | 600 s |
| Readings served after silence | 1 hour |
| Tokens and APNs dropped after | 30 days |
| Home deleted after | 90 days without a roster; 24 hours for a new home without one |
| Statistics rows kept | 13 months from the hour they describe |
| Clock lead allowed under rule N | 2 s |
