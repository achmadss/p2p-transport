# PLAN.md vs SPEC.md — alignment review

Verdict: **the plan agrees with the spec on principles, and diverges on
two structural things.** One of those two is a genuine fork in the road
and needs a decision before more code is written.

---

## 1. Already aligned

No change needed for any of these.

| Spec | Where the plan covers it |
|------|--------------------------|
| §1, §30.1 P2P first, relay only as fallback | §3, §7 |
| §24, §30.2 Coordinator knows devices, not files | §2, §5.1 — mimir stores no filesystem metadata |
| §13 Cryptographic device identity, not IP or MAC | §4.1 — Ed25519, and the plan argues the same case against hardware fingerprints |
| §14 End-to-end encryption; relay cannot inspect | §4.3, §8 — neither server holds a key, TURN forwards DTLS it cannot read |
| §14 Coordinator should not see file names | §2 — satisfied, and worth stating as a property rather than an aspiration |
| §15 Device authorization and pairing codes | §4.3 grants, §4.5 trust list, §5.2 pairing |
| §16 Show Direct vs Relayed to the user | §6.2, §6.3 — and the plan insists it be measured from the real ICE pair, not guessed |
| §30.4 Hide networking complexity | §6.3 — no ICE/STUN/TURN vocabulary in the UI |
| §10, §30.5 WebDAV is an adapter, never the internal protocol | §10.1 — the same layering, arrived at independently |
| §11 Internal protocol is filesystem verbs | §9.1 |
| §20 Streaming, backpressure, cancel, integrity | §13 |
| §22 Go for the agent | §16, and no cgo so one machine cross-builds |
| §29 MVP flow: laptop A connects to laptop B, lists, downloads | Build order steps 0-9 |
| §5 Connection order: LAN, direct, hole punch, relay | §7.2, §6.2 |

The plan also adds three things the spec does not mention, all of which
survive review:

- **Server-side COPY and MOVE.** Duplicating a 4 GB file costs ~200 bytes
  on the wire. Without this, a file manager silently turns a rename into
  a 4 GB round trip.
- **THUMBNAIL.** A gallery view over a relay is otherwise a bandwidth
  disaster.
- **The four operation classes** (§10.2), which is what makes it obvious
  that browsing over a relay is fine and only bulk transfer costs money.

---

## 2. Divergence 1 — where the client lives

**This is the important one.**

### What the spec says

Spec §8, §26 and §27: the client device runs a **local application** with
a WebDAV gateway on `localhost:9832`. Even the web UI goes through it:

```
Browser  ->  Local App  ->  P2P  ->  Home Agent
```

Spec §28 puts the local WebDAV endpoint **in the MVP**.

### What the plan says

The plan makes the **browser itself** a WebRTC peer:

```
Browser  ==== WebRTC ====>  Home Agent
```

and files `ratatoskr mount` (the local WebDAV bridge) under "Not now".

### Which is right

The spec's model, and by a wide margin. The plan's browser-as-peer design
was carrying four problems that the spec's model simply deletes:

| Plan's problem | Under the spec's model |
|----------------|------------------------|
| Browsers cannot do mDNS, so LAN discovery is impossible for them | The local app does mDNS. Every client gets LAN discovery |
| Streaming a 10 GB download to disk needs a service worker hack | The local app writes to disk directly |
| The client key dies when browser data is cleared | The key lives in a config file |
| Resumable transfers are hard to hold across a page reload | The local app owns transfer state |

It also delivers WebDAV, which the spec wants in the MVP and which brings
Finder, Explorer, rclone and Cyberduck for free.

### The cost, stated plainly

You lose **zero-install access**. Under the spec's model you cannot walk
up to a borrowed computer, open a URL, and get your files. Software must
be installed first.

That is a real product trade, and it is the spec's call to make. It is
also recoverable later: browser-as-peer can be added as an extra client
type without changing the File API or the agent.

### What changes in the plan

1. **One binary, two modes.** `ratatoskr` already acts as both agent and
   client. Formalise it: `ratatoskr run` serves files, `ratatoskr connect`
   consumes them, and both link the same File API.
2. **`internal/webdav` moves into the MVP.** A WebDAV server on
   `127.0.0.1:9832` that translates to the File API. It is the first
   frontend, not a "later" nicety.
3. **The web UI becomes a local UI.** It talks to the agent's control API
   on `127.0.0.1`, not to a remote peer over WebRTC. This removes the
   whole browser-WebRTC layer from the build order.
4. **`@cubone/react-file-manager` survives**, still behind
   `fs-adapter.ts`. Only the thing underneath the adapter changes: a
   `fetch` to `127.0.0.1` instead of a DataChannel.
5. **Mimir shrinks.** No signal tickets for browsers, no non-extractable
   WebCrypto keys, no browser client identity. It keeps accounts,
   devices, presence, pairing and grants.
6. **The mobile app** embeds the client library rather than being a
   browser peer. It can then do mDNS too.

---

## 3. Divergence 2 — the transport

Spec §22 suggests **libp2p**. The plan uses **Pion WebRTC** and has a
working step 0 on it.

This only became a real question once divergence 1 was resolved. WebRTC
was mandatory while the browser had to be a peer. Under the spec's model
the browser is no longer a peer, so the field is open.

### What libp2p would give us, that we would otherwise build

| Plan step | libp2p equivalent |
|-----------|-------------------|
| 1 — device identity | Peer IDs are already a hash of a public key. The same design, already written |
| 3 — heimdall rendezvous | Still needed, but smaller: a rendezvous point rather than an SDP relay |
| 4 — mDNS discovery | `p2p/discovery/mdns`, built in |
| 5 — LAN-first policy | Partly built in; the preference policy is still ours |
| 12 — NAT traversal | AutoNAT plus DCUtR hole punching |
| 13 — TURN and coturn | Circuit Relay v2, with reservations and limits. No coturn, no TURN credential minting |

That is a large amount of the build order.

It also brings **QUIC**, which matters for spec §21: near line rate on a
1 Gbps LAN. WebRTC DataChannels run SCTP over DTLS, which has a known
throughput ceiling that QUIC does not share. The plan already schedules
this measurement at step 9 — but if SCTP falls short, switching transport
after step 9 is far more expensive than choosing now.

QUIC also gives real streams, which makes resumable transfers (spec §20)
more natural than framing them over a DataChannel.

### What it costs

- A large, opinionated framework instead of a focused library. Harder to
  debug when it misbehaves.
- The browser-as-peer door narrows. js-libp2p can reach go-libp2p, but it
  is more work than plain WebRTC.
- Step 0's Pion code is discarded. That is about 250 lines.
  `internal/identity`, `internal/fsroot`, `internal/protocol` and
  `internal/fileapi` are all transport-independent and unaffected.

### Recommendation

**Move to libp2p**, on the condition that the spec's client model in §2
above is accepted. If browser-as-peer is ever wanted back, keep Pion.

The Transport interface in plan §10.9 is what makes this survivable
either way: the file layer does not know which one is underneath.

---

## 4. Smaller gaps

Each of these is a real omission in the plan, and none is contentious.

### 4.1 Resumable transfers are under-specified

Spec §20 asks explicitly for resume after an interruption. The plan has
ranged `READ`, which covers download resume, but never designs the state
that makes it work.

Needs:

- A transfer record persisted on the client: `{device, path, size, hash,
  bytes_done, temp_path}`
- Uploads resume with `WRITE` at an offset against the same temp file
- Verify with a hash of the bytes already written before continuing, so a
  changed source file is caught rather than producing a corrupt result
- Automatic resume on reconnect, not just a manual retry
- `ratatoskr transfers` to list and resume interrupted work

### 4.2 Connection recovery

Spec §20 lists it. The plan handles reconnect at step 15 but does not say
that an in-flight transfer survives it. It should: the transfer record
outlives the connection, and a new session picks it up.

### 4.3 Metadata fields

Spec §11 wants created time and permissions. The plan's `LIST_RESULT` has
name, kind, size and modified. Add `created` and `mode`, both optional —
creation time is not portable across filesystems, so it must be allowed
to be absent rather than faked.

### 4.4 Write operations are scheduled too late

Spec §28 puts upload, rename, delete and mkdir **in the MVP**. The plan
has them at step 14, after the web app, NAT and TURN.

The plan's reasoning was sound — every write verb reuses the same path
validation, and it is cheaper to find a hole there while nothing can be
destroyed. But that argues for writes coming immediately after LIST and
STAT, not for burying them behind the network work.

Move writes to directly after the read path is solid.

### 4.5 CLI syntax

Spec §27 uses `app ls home:/Documents`. That is better than the plan's
`ratatoskr connect ID ls PATH`. Adopt `device:/path`.

### 4.6 Vocabulary

The spec says **Coordinator** where the plan says **mimir** plus
**heimdall**. The split is worth keeping — one has a database and scales
on rows, the other holds sockets and scales on connections — but the plan
should say plainly that together they are the spec's Coordinator.

---

## 5. What the plan got right that the spec should adopt

Offered back the other way.

- **Presence and connection path are two different questions** (plan
  §6). The spec's §16 shows "Direct connection" in the device list, but
  that cannot be known before connecting. Only ICE knows, and only after
  the fact.
- **Operation classes** (plan §10.2). The spec treats the relay as
  uniformly degraded. It is not: browsing over a relay costs kilobytes
  and is perfectly pleasant. Only bulk transfer is expensive. This should
  drive where warnings and metering go.
- **Server-side COPY and MOVE** (plan §10.5). Absent from spec §11's
  operation list, and expensive to retrofit into a UI that has already
  learned to copy by downloading.
- **Path safety** (plan §12). The spec's security model does not mention
  path traversal or symlink escape, which is the most likely way this
  system gets breached.
- **No cgo in the agent** (plan §16). One machine builds all platforms.
  Worth writing down as a constraint before a dependency breaks it.

---

## 6. Decision required

Everything in §2 follows from the spec and can proceed.

Everything in §3 needs one answer first:

> **libp2p, or stay on Pion WebRTC?**

The build order after step 1 depends on it.
