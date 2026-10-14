# ADR 0009: An Apple Watch commands the box through a one-shot exchange

- Status: proposed. Nothing described here has shipped.
- Date: 2026-10-14
- Decided by: Fredrik on 14 October 2026, that a watch controls the box
  directly as well as through the phone, that an unlocked watch on the wrist
  counts as unlocked, and that each watch has its own key, which the owner can
  remove alone. Open decisions A–F below are recommendations until he answers.
- Pair: the wire, the relay route and the client changes are in
  [ftw-webapp `docs/watch-exchange.md`](https://github.com/srcfl/ftw-webapp/blob/main/docs/watch-exchange.md).
  Neither side works without the other.
- Builds on: [ADR 0006](0006-app-uplink.md). The relay stays the only path that
  carries commands. [ADR 0008](0008-ftw-cloud.md) does not change: FTW Cloud
  still never controls.

## Context

watchOS gives an ordinary app HTTPS through `URLSession` and nothing lower;
WebSocket and Network.framework are off limits
([TN3135](https://developer.apple.com/documentation/technotes/tn3135-low-level-networking-on-watchos)).
The relay speaks only WebSocket. So the W1 watch app sends its intent to the
phone, and nothing works when the phone is away.

Four facts in Core shape the answer:

- Every session arrives on the one uplink, shares `MaxSessions = 4`, and a
  fifth evicts the oldest (`appuplink/session.go`). A watch session there
  would push a phone out.
- The command log lives in the handler (`cmds: newCmdLog()` in
  `appproto/handler.go`), and a handler lives as long as its session.
  `docs/protocol.md` in ftw-webapp says the box keeps a `cmdId` for 24 h.
  Today it keeps it for one session, so a retry in a new session would act
  again.
- `notValidAfterMs` counts in box uptime. A client learns the uptime from
  `hello_ok`, which travels on the bulk lane because its length varies.
- The roles are `owner` and `viewer`. Over a session a phone may mint viewer
  codes only; an owner code comes from the LAN, at home
  (`appLinkRoleAllowed` in `api/api_app_link.go`).

## Decision

**The box opens a second relay socket for one-shot exchanges. A watch sends
Noise IK message 1, gets message 2, then sends `cmd` and gets `cmd.ack` with
`cmd.result`, each step one HTTPS request the relay hands to that socket. The
watch is its own device with role `watch`. An owner phone adds it, and it never
holds more than that phone.**

1. **An exchange socket.** `appuplink` opens
   `/r/<epoch>/<handle>/exchange` beside the uplink, with the same handle,
   epoch, rotation and backoff. Each message from the relay is
   `id(16) ‖ Noise message`, and each reply starts with the same id. The id is
   the relay's; Core copies it back and reads nothing else into it. The Noise
   prologue is `ftw.exchange.v1:` and the box's static key, so a stream
   handshake and an exchange handshake can never stand in for each other.
   Losing this socket changes nothing for the uplink, local control or the
   planner.

2. **Exchange sessions are a class of their own.** A table of at most 8,
   apart from the four stream sessions, so a watch never evicts a phone. A
   session ends after four exchanges, 60 s idle, or 3 minutes. Its handler
   takes `cmd` and nothing else: `sub`, `api.req`, `hist.query`, `plan.get`
   and `price.get` get `E_UNKNOWN_OP`. It never streams and skips the plan
   push that follows an applied command on the stream. Every frame is lane 0,
   512 B:
   - The answer to message 1 is message 2, whose payload is `hello_ok` cut to
     lane 0: proto, mode, clock, role, scopes and `rev` (the site's
     `controlRev`).
   - The answer to `cmd` is two transport messages: `cmd.ack`, then
     `cmd.result`, or `tick` in its place if the result has not settled
     within 3 s. The same `cmd` again returns the stored outcome.
   - At most 60 opening messages a minute; the box drops the rest unanswered.
     A stranger or a removed key gets no reply, as on the uplink.

3. **The command log belongs to the box.** It moves out of the handler, keyed
   by root device and `cmdId`, and keeps each entry 24 h of uptime, as
   protocol.md already promises. A watch's root device is the phone that added
   it, so one intent sent by both paths acts once. This also closes the gap
   for phones: a retry after a reconnect returns the first outcome instead of
   acting again.

4. **A watch is a device with role `watch`.**
   - Registry role `watch`: `ftw.live.read` and `ftw.dispatch.write`.
   - `POST /api/app-link/pairing` accepts role `watch` only over a session,
     only from an owner, and only with `stepUp`.
   - The code is bound to the calling phone's device id and has its own slot,
     so minting it does not cancel a pending phone code.
   - The watch's `hello` carries the code in message 1. `Authorise` stores the
     watch's key with role `watch` and the phone as parent.
   - The live caller for a watch holds its role's scopes, minus any scope the
     parent does not hold at that moment. A watch never does more than its
     phone can do now.
   - Owner codes still come only from the LAN. A watch is not an owner.

5. **Removal runs from phone to watch, never back.** Removing a phone removes its watches and
   drops their sessions. Removing a watch touches nothing else. The LAN page
   and `GET /api/app-link/devices` list each watch under its phone. A watch
   holds no members scope, so it can neither add nor remove anyone.

6. **Capability `cmd.exchange`.** A box with the exchange socket and watch
   codes says so in `hello_ok`. Without it the phone offers no "Add watch".

## What does not change

- Every exchange command takes the same `onCmd` path as the stream: expiry,
  scope, `rev`, dispatch gate, guards and read-back. Planner output never
  reaches hardware, and stale site-meter data still stops dispatch.
- FTW Cloud gets nothing new and controls nothing.
- The relay stays blind. The paired doc lists what it reads and what it still
  knows.
- Local operation. The exchange is one more remote door; without the relay
  the box runs as before.

## Open decisions for Fredrik

- **A. Transport.** A second socket from the box and an HTTPS route on the
  relay, with IK opened early. Recommended over a command in message 1
  (0-RTT), a mailbox the box polls, and a push through FTW Cloud. The paired
  doc gives the reasons.
- **B. What a watch may do.** Charging commands only. No site mode and no
  settings.
- **C. Who adds a watch.** An owner phone, with one passkey step-up. A viewer
  cannot.
- **D. Removal.** Removing a phone removes its watches.
- **E. Order.** The watch tries the direct path first and the phone second.
- **F. Timing floor.** A refusal answers in milliseconds; an applied command
  waits up to 2 s for the EV save. If the relay can see that gap through
  network jitter, the box answers no sooner than a fixed floor. That hides
  the outcome and slows every command by up to the floor. Recommended: measure
  first (test 6 in the paired doc) and add the floor only if the gap shows.

## Consequences

- One more idle socket per box on the relay.
- Opening an exchange session costs the box four X25519 operations, bounded
  by the relay's limits and the 60 a minute here.
- The command log grows per household rather than per session, and still
  prunes at 24 h.
- Three repositories change: this one, ftw-webapp (relay and registry) and
  ftw-app (phone and watch).

## Tests

Core's share of the paired doc's list, by its numbers:

- 6, timing: time to answer for applied and refused commands, at the box and
  through the relay.
- 7, replay: a recorded message 1 draws a message 2 nobody else can use; a
  recorded `cmd` fails in its own session and returns the stored outcome in a
  fresh one.
- 8, both paths: one `cmdId` from the exchange and from the parent phone's
  stream acts once.
- 9, role: a watch cannot use `api.req`, `sub`, `hist.query` or
  `site.mode.set`, and demoting its phone to viewer turns its next command into
  `E_SCOPE_DENIED`.
- 10, removal: removing the watch, or its phone, ends its session and silences
  its next message 1.
- 11, isolation: eight exchange sessions leave four stream sessions running.
- 12, vectors: Swift and Go agree on the bytes of every step.
