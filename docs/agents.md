# Agents and support access

Proposed. Nothing here has shipped. This document is normative: "must" is a
requirement on the box, FTW Cloud or the app. It extends the app protocol
([srcfl/ftw-webapp docs/protocol.md](https://github.com/srcfl/ftw-webapp/blob/main/docs/protocol.md))
and the [FTW Cloud protocol](protocol-cloud.md). Names in `code` that are
scopes, roles, operations, error codes, capabilities or push kinds live in
`contract/registry.yaml` and `contract/push-catalogue.yaml`. The message
shapes and the approval digest live in `go/protocol/agents.go`, with golden
vectors in `go/protocol/testdata/approval-vectors.json`.

## What it is for

An owner can let something other than a paired phone reach the box:

- a personal assistant (ChatGPT, Claude, Grok) through MCP at
  `https://cloud.ftw.energy/mcp`;
- Sourceful support, for one ticket;
- the release check, which reads a health summary while the box runs a beta.

FTW Cloud holds the agent's private key and opens its session. The box
decides everything: the role, the scopes, the operations, the caps and the
expiry. The cloud can admit an agent; only the box can let it act. Each
check runs again on every call, so a revoke or an expiry bites at the next
call whatever the cloud does.

## Principals

| Role | Who | Granted by | Longest grant | Ends |
|---|---|---|---|---|
| `agent` | A personal assistant | An owner, in the app or on the box page | 30 days, renewable | Expiry, revoke, or removal of the approving phone |
| `support` | Sourceful, for one ticket | An owner, from "Get help" | 14 days | Expiry, revoke, the ticket closing, or removal of the approving phone |
| `release` | The release check | An owner, on joining the beta | 90 days, renewable | Expiry, revoke, or removal of the approving phone |

- Every agent row has a wall-clock expiry. There is no grant without one.
- **Scopes come from the approver at call time.** An agent holds the scopes
  its row lists that its approver holds now: the approving phone's role
  today, or every scope for a grant made on the box page. A phone demoted to
  viewer narrows its agents at the next call. Removing the phone removes its
  agents, as it removes its watches.
- **A support call names a person.** Each envelope a `support` session sends
  carries `by`: the staff member's id at Sourceful, at most 64 bytes. The box
  refuses a call without it and writes it to the audit log. The box cannot
  check it; FTW Cloud signs the staff member in and asserts it.
- **The ticket closing ends the grant.** When a ticket closes, Sourceful's
  tooling sends `agent.revoke` for its own row. An agent may always revoke
  itself. The expiry is the backstop.
- **The release check reads one thing.** Its role carries `ftw.health.read`
  and nothing else. Beyond `hello` and `health.get` it may only list and
  revoke its own row.

## Scopes

Agent scopes reuse the phone scopes where they mean the same thing and add
these:

| Scope | Lets an agent |
|---|---|
| `ftw.logs.read` | Read a log excerpt that the box redacts before it leaves |
| `ftw.config.read` | Read the configuration, redacted on the box |
| `ftw.diag.run` | Run checks that change nothing |
| `ftw.dispatch.hold` | Hold a device for a capped time; the hold ends unless renewed |
| `ftw.goals.request` | Ask for a durable change, which waits for an owner |
| `ftw.update.request` | Later: ask to install a version, which an owner confirms |

The roles `agent` and `support` carry at most `ftw.live.read`,
`ftw.history.read`, `ftw.plan.read`, `ftw.health.read`, `ftw.events.read`,
`ftw.logs.read`, `ftw.config.read`, `ftw.diag.run`, `ftw.dispatch.hold` and
`ftw.goals.request`. A grant names a subset. Agents read by default; an owner
ticks control to add `ftw.dispatch.hold` or `ftw.goals.request`.

## What each scope opens

An agent session is the app session, `ftw.session.v1`, between the agent's
key and the box. It uses the messages below and no others. The box answers
any other request from an agent with `E_SCOPE_DENIED`. A phone's session is
unchanged by this document, except that the new reads below check their
scope for phones too.

| Message | Needs | Answer |
|---|---|---|
| `hello` | — | `hello_ok`, with the role and the scopes the agent holds now |
| `sub` | `ftw.live.read` | The live stream, as for a phone |
| `plan.get`, `price.get` | `ftw.plan.read` | `plan`, `price` |
| `hist.query` | `ftw.history.read` | `hist.chunk` |
| `health.get` | `ftw.health.read` | `health` |
| `events.get` | `ftw.events.read` | `events` |
| `logs.get` | `ftw.logs.read` | `logs` |
| `config.get` | `ftw.config.read` | `config` |
| `diag.run` | `ftw.diag.run` | `diag` |
| `cmd` | The op's agent scope, `ftw.dispatch.hold` | `cmd.ack`, `cmd.result` |
| `agent.request` | The op's agent scope, `ftw.goals.request` | `agent.ok` |
| `agent.list` | — | `agent.ok` with the agent's own row and requests |
| `agent.revoke` | — | `agent.ok`; only for the agent's own id |

`api.req`, the passthrough, is refused to every agent with `E_SCOPE_DENIED`:
its routes are priced for an owner, not for an agent. A box that lacks one of
the reads answers `E_UNKNOWN_OP`, as it does for any message it does not
know.

## Operations

Each op in the registry may name an `agent` scope. An op without one is never
open to an agent.

| Op | Phone scope | Agent scope | For an agent |
|---|---|---|---|
| `battery.hold` | `ftw.dispatch.write` | `ftw.dispatch.hold` | `cmd`, under the caps |
| `loadpoint.hold` | `ftw.dispatch.write` | `ftw.dispatch.hold` | `cmd`; `hold_s` from 1 to the cap, never 0 |
| `loadpoint.boost` | `ftw.dispatch.write` | `ftw.dispatch.hold` | `cmd`; `duration_s` from 1 to the cap, or `expires_at_ms` within it |
| `site.mode.set` | `ftw.mode.write` | `ftw.goals.request` | `agent.request` only |
| `planner.prefs.set` | `ftw.mode.write` | `ftw.goals.request` | `agent.request` only |
| `loadpoint.soc.set` | `ftw.dispatch.write` | `ftw.goals.request` | `agent.request` only |
| `loadpoint.surplus_only.set` | `ftw.dispatch.write` | `ftw.goals.request` | `agent.request` only |

- **A per-grant allowlist.** A grant lists `ops`, a subset of the ops whose
  agent scope the grant holds. The box refuses an op the grant does not list
  with `E_OP_NOT_GRANTED`, even when the agent holds its scope.
- **A hold ends unless renewed.** `ftw.dispatch.hold` ops must carry an end
  within `caps.holdS`. A `loadpoint.hold` with `hold_s` 0 is the persistent
  operator hold, and an agent may never send one. Releasing a hold (`clear`,
  `cancel`) needs only the op in the allowlist.
- **A durable change waits.** An agent that sends a `ftw.goals.request` op as
  `cmd` gets `E_NEEDS_OWNER`, with `{use: "agent.request"}`.
- **The checks run in Core's order:** the command's expiry, a known op, the
  live grant (revoke and expiry), the scope, then for an agent the
  allowlist, the rate and the caps, then `expect.rev`, the dispatch gate and
  the guards.
- **Its own command log.** An agent's command ids live under its own root,
  `agent:<id>`, never an owner's.

## Caps

| Cap | Meaning | Box limits |
|---|---|---|
| `holdS` | The longest hold, in seconds | 60 to 1,800. Required with `ftw.dispatch.hold` |
| `maxW` | The largest power a hold may set, in W | 1 to 100,000. Required with `ftw.dispatch.hold` |
| `perMin` | `cmd` and `agent.request` per rolling minute | 1 to 60. Default 6 |

- A command past `holdS` or `maxW` gets `E_CAP_EXCEEDED`, with
  `{cap, max}`. The box never clips an argument; it refuses, so the agent
  knows what happened.
- A command past `perMin` gets `E_RATE_LIMITED`, with `retryAfterMs` in the
  error body.
- Reads are limited to 60 a minute per agent, by the box, whatever the caps
  say.

## Durable changes

1. The agent sends `agent.request {op, args, why}`. `why` is at most 280
   bytes of the agent's own words, shown to the owner as the agent's, never
   as the box's.
2. The box checks the op as for a `cmd`: known, granted, scope, rate. It
   stores the request and answers `agent.ok {reqId, expiresAtMs}`.
3. The box pushes `agent.request` to every owner through its event path, and
   writes an audit row.
4. An owner reads it with `agent.list` and answers with `agent.confirm`.
   Approving needs an approval signature; declining needs none.
5. On approval the box runs the op on the confirming owner's session as a
   command with id `req:<reqId>`, under the owner's scopes and against fresh
   state, and sends that session `cmd.ack` and `cmd.result`. Nothing the
   agent sent decides whether it may act; the owner's confirmation does.
6. A request no owner answers within 12 hours expires. The box keeps a
   decided or expired request for 24 hours, so the agent can read its state.

Pending requests live in memory. A restart drops them, and the agent asks
again.

## Messages

All of these ride in the app session. Each request carries an envelope `id`
and gets `agent.ok` with that id, or `error` with that id and a code.

### `approval.key.set` — an owner phone registers its approval key

| Field | Type | Meaning |
|---|---|---|
| `pub` | bstr, 65 | P-256 public key, SEC1 uncompressed |
| `atMs` | int | The phone's wall clock |
| `sig` | bstr, 64 | Proof of possession: the approval signature over `pub` |

Answer `{deviceId, setAtMs}`. Only an owner phone may send it, and only for
itself. A new key replaces the phone's old one and writes an audit row.
Errors: `E_SCOPE_DENIED` (not an owner), `E_BAD_SIGNATURE`,
`E_UNKNOWN_OP` with `{field}` for a malformed body.

### `agent.add` — an owner grants access

| Field | Type | Meaning |
|---|---|---|
| `key` | bstr, 32 | The agent's X25519 Noise static key |
| `role` | text | `agent`, `support` or `release` |
| `scopes` | [text] | Sorted, no repeats, within the role |
| `ops` | [text] | Sorted, no repeats, each with an agent scope in `scopes` |
| `caps` | `{holdS, maxW, perMin}` | Uints; 0 where not used |
| `expiresAtMs` | int | Wall clock, within the role's longest grant |
| `principal` | `{client, clientId, ticket}` | Text, each at most 256 bytes, empty where not used. `ticket` is required for `support` |
| `atMs` | int | The phone's wall clock |
| `sig` | bstr, 64 | Approval signature |

Answer `{agent}`, the new [agent row](#agent-row). The box signs a new
roster that lists it, pushes `agent.added` to every owner and writes an audit
row. Errors: `E_SCOPE_DENIED` (not an owner phone), `E_NO_APPROVAL_KEY`,
`E_BAD_SIGNATURE`, `E_CAP_EXCEEDED` (`{cap, max}`, for a cap, the expiry, or
16 agent rows on the box), `E_UNAVAILABLE` (`{subsystem: "clock"}`),
`E_UNKNOWN_OP` (`{field}`). A key already paired as a phone or an agent is
`E_UNKNOWN_OP` with `{field: "key"}`.

### `agent.widen` — an owner changes or renews a grant

| Field | Type | Meaning |
|---|---|---|
| `id` | text | The agent row's id |
| `epoch` | uint | The row's epoch as the owner saw it |
| `scopes`, `ops`, `caps`, `expiresAtMs` | | The whole new grant, as in `agent.add` |
| `atMs`, `sig` | | As in `agent.add` |

Any change other than a revoke needs a signature, narrowing included; one
rule is easier to check than two. The epoch rises. Errors as `agent.add`,
plus `E_NOT_FOUND` and `E_CONFLICT` (`{expected, actual}`) for a stale epoch.

### `agent.revoke` — remove a grant

`{id}`. From an owner, any agent row; from an agent, its own. No signature.
The row goes at once, its sessions end with `session.terminate {reason:
"revoked"}`, its pending requests are dropped, and the next roster leaves it
out. Answer `{id}`. Error: `E_NOT_FOUND`.

### `agent.list` — who has access

`{}`. To an owner: `{agents: [agent row], requests: [request], keys:
[{deviceId, setAtMs}]}`, every row on the box. To an agent: its own row and
its own requests. A viewer gets `E_SCOPE_DENIED`.

### `agent.request` — an agent asks for a durable change

`{op, args, why}`. `args` is the op's argument map, at most 1 KiB encoded.
Answer `{reqId, expiresAtMs}`. Errors: `E_OP_NOT_GRANTED`, `E_SCOPE_DENIED`,
`E_RATE_LIMITED`, `E_CAP_EXCEEDED` (`{cap: "pending", max: 4}` per agent,
16 per box), `E_UNKNOWN_OP`.

### `agent.confirm` — an owner answers a request

| Field | Type | Meaning |
|---|---|---|
| `reqId` | text | The request |
| `approve` | bool | Run it, or decline it |
| `atMs`, `sig` | | Required to approve. Absent to decline |

Answer `{reqId, state, cmdId}`: `state` is `approved` or `declined`, and
`cmdId` is the command the box now runs on this session. Errors:
`E_NOT_FOUND` (unknown, decided or expired), `E_NO_APPROVAL_KEY`,
`E_BAD_SIGNATURE`, `E_SCOPE_DENIED`.

### `audit.get` — what agents and owners did

`{beforeSeq, n}`; `n` at most 200. To an owner phone:
`{rows: [audit row], more}`, newest first. Agents and viewers get
`E_SCOPE_DENIED`.

### Agent row

| Field | Type | Meaning |
|---|---|---|
| `id` | text | First 8 characters of the key in base64url, like a phone's device id |
| `key` | bstr, 32 | X25519 Noise static key |
| `role` | text | `agent`, `support` or `release` |
| `scopes`, `ops` | [text] | As granted |
| `caps` | `{holdS, maxW, perMin}` | |
| `expiresAtMs` | int | Wall clock |
| `principal` | `{client, clientId, ticket}` | |
| `approvedBy` | text | `app:<deviceId>` or `lan` |
| `addedAtMs`, `lastSeenMs` | int | Wall clock, 0 when unknown |
| `epoch` | uint | Rises with every change |

### Request

`{reqId, agent, op, args, why, atMs, expiresAtMs, state}`. `state` is
`pending`, `approved`, `declined` or `expired`.

### Audit row

`{seq, atMs, actor, role, principal, by, what, target, result}`. `actor` is
the caller subject (`agent:<id>`, `app:<id>`, `lan:<address>` or `box`);
`principal` is the client or ticket; `by` is the staff id on a support call;
`what` is the message type or op; `result` is `ok` or the error code.

### Reads

| Message | Request | Answer |
|---|---|---|
| `health.get` | `{}` | `health {version, uptimeS, clock, drivers: {ok, degraded, faulted, offline}, meter: {srcState, ageMs}, dispatchBlockedBy}` |
| `events.get` | `{n}`, at most 50 | `events {events}`, each the FTW Cloud `event` body |
| `logs.get` | `{n, level}`; `n` at most 200, `level` `WARN` by default | `logs {lines: [{ts, level, src, msg}], dropped}` |
| `config.get` | `{section}` | `config {sections}` without a section; `config {section, doc}` with one |
| `diag.run` | `{check, src}`; `check` is `clock`, `link`, `meter`, `planner` or `driver` | `diag {check, ok, facts}` |

- Each answer fits one bulk frame (16 KiB). `logs.get` drops its oldest
  lines to fit and counts them in `dropped`. A config section that does not
  fit gets `E_RANGE_TOO_LARGE`; an unknown one `E_NOT_FOUND`.
- `health` carries no readings and no names: drivers are counted, and a
  source that blocks dispatch goes by an opaque id. Planner solve times,
  fallbacks, refusals and plan against actual join it when the box keeps
  them; a field the box cannot fill is absent.
- The box answers `events.get` and `diag.run` with `E_UNKNOWN_OP` until it
  builds them.

## Redaction on the box

`logs.get` and `config.get` build their answer on the box. Nothing leaves it
unredacted, and nothing is stored in the cloud.

- **Logs.** The box keeps the time, the level, the message and, for a
  driver's line, the driver's opaque source id in place of its name. It
  replaces every name the configuration knows (site, drivers, devices,
  loadpoints, vehicles), and removes IP and MAC addresses, host names, URLs,
  e-mail addresses, quoted text, serial-like words (letters and digits mixed,
  six or more), runs of seven or more digits, and anything that looks like a
  credential: bearer and basic headers, secret-named keys and long tokens. A
  line's attributes keep their keys; a value stays only when it is a number,
  a boolean, a duration or a short lowercase word, and is otherwise replaced
  by `[omitted]`. At most 200 lines, in one bulk frame.
- **Config.** Every secret-named key is replaced by `***`. Names become
  opaque ids. Location, addresses, host names, URLs, serials, MAC addresses,
  vehicle ids, e-mail addresses and notification targets are removed.

Redaction is a list of patterns, so text a driver writes in a shape none of
them matches can pass. Tests hold the cases above; a driver that logs
something new needs a new case.

## The agent's roster entry

The roster the box signs for FTW Cloud lists each agent row as a `Device`
with one extra map, `agent`:

| Field | Type | Meaning |
|---|---|---|
| `id` | text | The row's id |
| `key` | bstr, 32 | X25519 Noise static key |
| `role` | text | `agent`, `support` or `release` |
| `scopes` | [text] | The readable copy's filter, as for a phone |
| `epoch` | uint | |
| `tun` | bool | Always true for a live agent row |
| `agent.scopes`, `agent.ops` | [text] | The grant, at the approver's scopes now |
| `agent.caps` | `{holdS, maxW, perMin}` | |
| `agent.expiresAtMs` | int | Wall clock |
| `agent.principal` | `{client, clientId, ticket}` | |

- The box leaves out a row that has expired by its clock. While its clock is
  `none`, it lists rows as they are and refuses their calls.
- FTW Cloud must stop admitting a row at `expiresAtMs` by its own clock,
  without waiting for a roster. It must never admit an agent row through the
  relay, which an agent never uses, and it may tighten caps but never loosen
  them.

## Agents use the tunnel only

An agent's session reaches the box through FTW Cloud as `tun`. The relay
refuses it: a handshake over the relay from an agent's key ends as from a
stranger. The watch exchange refuses it too.

## Expiry and revoke

- **Expiry is wall clock.** The box checks it at the handshake, on every
  call and once a second for an open session. An expired row ends its
  session with `session.terminate {reason: "expired"}`, answers its next
  call with `E_GRANT_EXPIRED` and leaves the roster. A sweep removes it and
  writes an audit row.
- **No clock, no agents.** While the box clock is `none` (NTP has not
  synchronised it), every call from an agent gets `E_UNAVAILABLE` with
  `{subsystem: "clock"}`, and `agent.add` and `agent.widen` are refused the
  same way. Owners' phones are not affected.
- **Revoke is immediate.** The row goes, the sessions end, and the next call
  fails with `E_GRANT_REVOKED`, whatever the cloud admits.

## The approval signature

The passkey is the web app's (`app.ftw.energy`) and the box cannot check it,
so `stepUp` is only the app's word. Agent grants need something the box can
check.

- **The key.** Each owner phone makes a P-256 key in the Secure Enclave,
  usable only after Face ID or Touch ID with the current enrolment
  (`biometryCurrentSet`). It cannot leave the phone. The phone registers the
  public half with `approval.key.set` over its owner session.
- **What it signs.** `agent.add`, `agent.widen`, `agent.confirm` to approve,
  and `approval.key.set` itself as proof of possession. `agent.revoke` and a
  decline need no signature.
- **The bytes.** The signature is ECDSA P-256 with SHA-256 over the
  deterministic CBOR (RFC 8949, section 4.2.1) of one array, encoded `r ‖ s`
  in 64 bytes. CryptoKit's `signature(for:)` hashes the bytes itself, so the
  phone passes the array's bytes, not their digest.

  ```
  ["ftw.approval.v1", what, boxKey, deviceId, atMs, ...fields]
  ```

  | `what` | Fields after `atMs` |
  |---|---|
  | `approval.key.set` | `pub` |
  | `agent.add` | `key, role, scopes, ops, [holdS, maxW, perMin], expiresAtMs, client, clientId, ticket` |
  | `agent.widen` | `id, epoch, scopes, ops, [holdS, maxW, perMin], expiresAtMs` |
  | `agent.confirm` | `reqId` |

  `boxKey` is the box's X25519 static key (32 bytes, as in the QR code), so a
  signature for one box is worth nothing at another. `deviceId` is the
  signing phone's id, and the box checks it is the session's. Lists are
  sorted with no repeats, empty text is `""`, and integers take their
  shortest form.
- **Freshness.** `atMs` must be within 10 minutes of the box's wall clock,
  and the box keeps each signature it accepted for 20 minutes and refuses it
  a second time. A replayed `agent.confirm` finds no pending request; a
  replayed `agent.widen` names an old epoch.
- **What it proves.** Someone passed Face ID on that phone and approved
  exactly these bytes. It does not stop a modified app on an enrolled owner
  phone from registering a new key of its own: that app could already send
  commands. It stops a phone left unlocked on a table, and it lets the box
  check approval without trusting the app or the cloud.

## Approval on the box page

On the home network an owner manages agents at `/api/agents`. These routes
are `local`: the passthrough never carries them, so a phone uses the messages
above and its approval key, and an agent can never reach them.

| Route | Who |
|---|---|
| `GET /api/agents` | A client on the LAN. Rows, requests and keys |
| `GET /api/agents/audit?before=&n=` | A client on the LAN |
| `POST /api/agents {offer, scopes, ops, caps, ttlS}` | The box itself, or a LAN client signed in with the house password |
| `POST /api/agents/{id}/widen {epoch, scopes, ops, caps, ttlS}` | The same |
| `POST /api/agent-requests/{reqId} {approve: true}` | The same |
| `POST /api/agent-requests/{reqId} {approve: false}` | A client on the LAN |
| `DELETE /api/agents/{id}` | A client on the LAN |

`offer` is the [grant offer](#the-grant-offer) as the consent page shows
it. The owner may grant less: `scopes`, `ops`, `caps` and `ttlS` replace the
offer's when given. The box refuses an offer for another home id.

Widening needs the house password because the LAN sign-in is otherwise
nothing at all. What the box has today:

- **Off by default.** With `api.lan_auth` off, which is the default, every
  client on the home network is an owner with no sign-in. Granting a remote
  agent from there would let any device on the Wi-Fi give the outside world
  a way in, so those routes refuse with 403 until the house password is on.
- **With the house password on.** The password has at least 10 characters
  and is stored as argon2id (64 MiB, 3 passes). Five wrong guesses lock every
  sign-in for 30 seconds, box-wide. A sign-in sets a random 32-byte cookie,
  `HttpOnly` and `SameSite=Strict`, that lasts 12 hours and dies with a
  restart. Browser requests from other sites are refused by Origin and
  `Sec-Fetch-Site` checks.
- **What it lacks.** The page is plain HTTP, so anyone who can read the home
  network's traffic can read the password and the cookie. There is one
  shared password and no person behind it, so the audit row names the
  address. Requests from the box itself (loopback) need no password, since
  whoever can run code on the box already owns it.

Narrowing stays open to any LAN client, as `DELETE /api/cloud/grant` does,
because removing access is safe.

## The grant offer

The MCP consent page shows a short code, a QR code and a universal link. All
three carry one offer, which the app shows the owner before Face ID. It is a
CBOR map, base64url without padding, in the link's fragment:

| Field | Type | Meaning |
|---|---|---|
| `v` | uint | 1 |
| `home` | text | The home id |
| `key` | bstr, 32 | The agent's X25519 key, made by FTW Cloud's `keys` part |
| `role` | text | `agent`, `support` or `release` |
| `client` | text | The client's name, from its metadata document |
| `clientId` | text | The client id |
| `scopes`, `ops` | [text] | What the client asks for |
| `caps` | `{holdS, maxW, perMin}` | Proposed caps |
| `ttlS` | uint | Proposed length |
| `ticket` | text | The ticket id, for `support` |
| `code` | text | The code on the consent page, for the owner to compare |

The owner may grant less than the offer asks. FTW Cloud learns the outcome
from the roster: the grant is whatever the roster lists for that key.

## Push kinds

- `agent.added` goes to every owner when an agent row is added, as
  `cloud.granted` does: access from outside is news every owner should hear.
- `agent.request` goes to every owner when an agent asks for a durable change.

Both travel as events. With the readable copy off, or without events in it,
FTW Cloud carries no events, so an owner learns of a request when the app
next opens. That gap is open.

## The audit log

The box writes a row for every call an agent makes and for every change to
an agent grant or an approval key, whoever makes it. Rows live in the box's
state store, at most 10,000 and at most 90 days. An owner reads them with
`audit.get` or on the box page. FTW Cloud keeps its own log of what it
forwarded; neither replaces the other.

## Numbers in one place

| What | Value |
|---|---|
| Longest grant | `agent` 30 days, `support` 14 days, `release` 90 days |
| Agent rows per box | 16 |
| `holdS` | 60 to 1,800 s |
| `maxW` | 1 to 100,000 W |
| `perMin` | 1 to 60, default 6 |
| Reads per agent | 60 a minute |
| Pending requests | 4 per agent, 16 per box |
| Request lifetime | 12 hours; kept 24 hours after it ends |
| `agent.request` args, `why` | 1 KiB, 280 bytes |
| Approval `atMs` window | 10 minutes either way |
| Signature replay memory | 20 minutes |
| `logs.get` | 200 lines, one 16 KiB frame |
| `events.get` | 50 events |
| `audit.get` | 200 rows a page; 10,000 rows and 90 days kept |
| `by` on a support call | 64 bytes |
