# Ratatoskr — TODO

See `PLAN.md` for the why. This file is the what, in order.

Rule: do not start a step until the one above it passes its check.

---

## Step 0 — Two agents talk, no server ✅

- [x] `go mod init github.com/achmadss/ratatoskr`
- [x] Add `github.com/pion/webrtc/v4`
- [x] `cmd/ratatoskr/main.go` with a subcommand router
- [x] `internal/transport`: peer connection, one `ctrl` DataChannel
- [x] `ratatoskr dev-offer` / `ratatoskr dev-answer`, SDP pasted by hand
- [x] Echo any string received on `ctrl` back to the sender
- [x] Report the selected ICE candidate pair on connect

**Check:** a string comes back.
**PASSED.** Both sides exit 0, pair reported as `direct: host <-> host`.

---

## Step 1 — Device identity

- [ ] `internal/config`: per-OS config dir via `os.UserConfigDir()`
- [ ] Create the dir at 0700 on first run
- [ ] `internal/identity`: generate an Ed25519 keypair on first run
- [ ] Write `identity.key` at mode 0600; refuse to start if it is world readable
- [ ] Peer id = `base32(sha256(pubkey)[:16])`, lowercase, no padding, `rt-` prefix
- [ ] Parse and format helpers, including the grouped display form
- [ ] `Sign(msg)` and `Verify(peerID, pubkey, msg, sig)`; Verify must also
      confirm the pubkey actually hashes to that peer id
- [ ] `ratatoskr id` prints the peer id and the public key
- [ ] Tests: id is stable across restarts; a mismatched pubkey fails Verify

**Check:** `ratatoskr id` prints the same id twice, and on all three machines.

---

## Step 2 — Mutual auth over the control channel

- [ ] `internal/protocol`: envelope, version constant, request and reply types
- [ ] `HELLO` carrying peer id, public key, nonce, version
- [ ] Reject a version mismatch before anything else
- [ ] `AUTH` signature over `"ratatoskr-auth-v1" || peer nonce || DTLS fingerprint`
- [ ] Read the local DTLS fingerprint out of Pion and include it
- [ ] Both directions must pass before any other message is served
- [ ] `internal/config`: trusted peer list, persisted
- [ ] `ratatoskr trust ID [--name N]`, `untrust`, `trusted`
- [ ] Close the connection on: bad signature, id/pubkey mismatch, untrusted
      id, auth timeout, any message sent before auth completes
- [ ] Tests: tampered signature, replayed nonce, wrong peer id, unknown peer

**Check:** an untrusted id is refused. A tampered signature is refused.

---

## Step 3 — heimdall

- [ ] `cmd/heimdall/main.go`, flag `--addr`
- [ ] `internal/signal`: message types shared by client and server
- [ ] Server: WebSocket endpoint, nonce challenge on connect
- [ ] Server: verify `register` signature and that pubkey hashes to the peer id
- [ ] Server: peer registry; relay `offer` / `answer` / `ice` by peer id
- [ ] Server: `not_found` and `peer_gone`
- [ ] Server: ping every 20 s, drop after two misses
- [ ] Server: re-`register` of a live peer id replaces the old socket
- [ ] Server: rate limit per socket and per IP
- [ ] `internal/discovery`: the `Discovery` interface
- [ ] `internal/discovery/net`: heimdall client, reconnect with backoff
- [ ] Trickle ICE: send candidates as they are gathered, do not wait
- [ ] Wire `ratatoskr run` and `ratatoskr connect PEER_ID --via net`
- [ ] Retire `dev-offer` and `dev-answer`

**Check:** `ratatoskr connect <id> --via net` links up with no pasting.

---

## Step 4 — LAN discovery

- [ ] Pick a pure-Go mDNS library; confirm no cgo (`CGO_ENABLED=0` build)
- [ ] `internal/discovery/lan`: advertise `_ratatoskr._udp.local`
- [ ] TXT record: `id`, `pk`, `sp` (signal port), `v`
- [ ] Browse and filter by peer id
- [ ] Local signal endpoint: `POST /v1/signal` on the LAN interface
- [ ] The endpoint does exactly one thing: take an offer, return an answer
- [ ] Rate limit it; cap concurrent handshakes; small body cap
- [ ] Re-advertise when the network interface changes
- [ ] `ratatoskr discover` lists agents seen on this network
- [ ] `ratatoskr connect PEER_ID --via lan` (no fallback)
- [ ] Build LAN sessions with an empty ICE server list
- [ ] Test on macOS, Windows and Linux; note every firewall prompt
- [ ] Test with the router's Internet uplink physically unplugged

**Check:** `discover` sees the other machine, and `--via lan` transfers with
no Internet at all.

---

## Step 5 — LAN-first discovery

- [ ] Start mDNS at t=0; hold heimdall until t=400 ms
- [ ] If the LAN answers in time, never open a heimdall session at all
- [ ] LAN-discovered sessions use an **empty ICE server list**: host candidates
      only, no STUN, no TURN — so nothing leaves the network
- [ ] Internet-discovered sessions use the normal STUN/TURN list
- [ ] Fall back to heimdall on either trigger: mDNS timeout, **or** peer found
      on the LAN but the handshake with it failed
- [ ] Cancel the loser cleanly; no leaked goroutine, socket or peer connection
- [ ] Timeouts: mDNS ~500 ms, heimdall ~5 s
- [ ] Record which method won and expose it in `status`
- [ ] Both fail → one clear error, not two confusing ones
- [ ] `--via lan` fails instead of falling back, so the offline test is real
- [ ] Test: capture traffic and confirm zero packets leave the LAN on a local
      connect

**Check:** on a LAN, heimdall is never contacted. Multicast blocked falls
through. A found-but-unreachable peer falls through. Neither path hangs.

---

## Step 6 — File API, LIST and STAT

- [ ] `internal/transport`: extract the `Transport` interface — `Call`,
      `OpenStream`, `Path`, `Close`. Move the Pion code under `transport/webrtc`
- [ ] `internal/transport/loopback`: in-process transport, so the whole file
      layer is testable with no network at all
- [ ] `internal/fileapi`: the verb surface. Nothing in it may import Pion
- [ ] `PING` / `PONG` with round-trip time
- [ ] `internal/fsroot`: clean, join, `EvalSymlinks`, verify inside root
- [ ] `internal/fsroot`: the **write** variant — resolve the parent, then check
      the final element has no separator
- [ ] `internal/fsroot` tests: `..`, absolute paths, symlink escape, null
      bytes, Windows reserved names, alternate data streams, `\\?\` prefixes,
      trailing dots and spaces
- [ ] Shared roots carry a mode: `ro` or `rw`
- [ ] `LIST` / `LIST_RESULT`, paged
- [ ] `STAT` / `STAT_RESULT`
- [ ] `DF` free space on a share
- [ ] `ERROR` codes; never leak a real path or a stack trace
- [ ] Enforce the 64 KB control message cap
- [ ] `ratatoskr connect ID ls PATH`

**Check:** a real directory prints over both transports. Every hostile path in
the table is refused.

---

## Step 7 — Agent control API

- [ ] `internal/control`: HTTP server on `127.0.0.1`, random free port
- [ ] Random token; write `control.json` at mode 0600
- [ ] Bearer token check on every route
- [ ] `GET /v1/status`, `/v1/peers`, `/v1/discover`, `/v1/events`
- [ ] `GET|POST|DELETE /v1/folders` (with `--rw`) and `/v1/trusted`
- [ ] Rewire every CLI subcommand as an HTTP client
- [ ] Clear message when no agent is running
- [ ] Verify the config dir path on all three OSes

**Check:** `ratatoskr status` reports a running `ratatoskr run`.

---

## Step 8 — Download a small file

- [ ] `READ` / `READ_OK` with size and BLAKE3 hash
- [ ] `READ` with an offset and length — needed for resume, media seek and
      type sniffing. Not optional, and cheap to add now
- [ ] One `xfer-<id>` DataChannel per transfer
- [ ] Frame format: uint32 sequence + payload, 16 KB chunks
- [ ] Empty frame means end of stream, then close the channel
- [ ] `CANCEL` from either side; both ends clean up
- [ ] Receiver verifies the hash and fails loudly on a mismatch
- [ ] Cap concurrent transfers per peer at 4
- [ ] `ratatoskr connect ID get PATH OUT` with a progress line

**Check:** a 10 MB file arrives and the hash matches.

---

## Step 9 — Backpressure and a large file

- [ ] Sender: `SetBufferedAmountLowThreshold` 256 KB, pause above 1 MB
- [ ] `CREDIT`; receiver starts at 64 chunks, tops up at half spent
- [ ] Sender blocks when credits run out
- [ ] Receiver writes straight to disk; never buffers the whole file
- [ ] Handle a file that shrinks, grows or vanishes mid-transfer
- [ ] Measure and log throughput
- [ ] Test: 10 GB over LAN, watch RSS on both sides
- [ ] Test: 4 simultaneous transfers stay stable
- [ ] `THUMBNAIL`: the agent renders a 256 px preview, so a gallery costs
      kilobytes instead of megabytes
- [ ] `COPY`: server-side. Prove a 4 GB copy moves ~200 bytes over the wire
- [ ] `HASH`: checksum a file without transferring it

**Check:** 10 GB completes, memory flat on both sides. **Transport milestone.**

---

## Step 10 — mimir, the control plane

- [ ] `cmd/mimir`: HTTP service, database, sessions
- [ ] Schema: accounts, devices, clients, shares, grants
- [ ] Mimir signing keypair; publish its public key
- [ ] `internal/grant`: issue and verify, shared by mimir and the agent
- [ ] Pairing: agent shows a short-lived single-use code; `POST /v1/pair`
      redeems it; agent pins mimir's public key
- [ ] `ratatoskr pair CODE`
- [ ] `GET /v1/devices` — requirement 1
- [ ] `GET /v1/devices/{id}` — requirement 2
- [ ] `POST /v1/devices/{id}/session` — signal ticket, grant, ICE servers
- [ ] `GET|DELETE /v1/clients` — revoke a client
- [ ] Heimdall accepts a mimir ticket as a `register`
- [ ] Heimdall pushes presence to mimir; full reconcile every 30 s
- [ ] `network_hint` from comparing public IPs — label it a hint, not a fact
- [ ] Agent verifies grants: signature, device id, expiry, account, client key
- [ ] Agent caches the last grant so a LAN connection survives a mimir outage
- [ ] Tests: expired grant, wrong device, forged signature, revoked client

**Check:** `GET /v1/devices` lists a paired machine with correct presence, and
the agent accepts a real grant while refusing every forged one.

---

## Step 11 — Web app

- [ ] Client identity: WebCrypto Ed25519, **non-extractable**, in IndexedDB
- [ ] Login, then device list and per-device status (requirements 1 and 2)
- [ ] Browser WebRTC peer: heimdall via ticket, ctrl channel, HELLO + AUTH
- [ ] `web/src/transport.ts` and `web/src/fileapi.ts` — the same interface and
      verbs as the Go side
- [ ] `web/src/fs-adapter.ts` — our verbs only. Nothing else touches the
      file-manager library
- [ ] Thumbnails in grid view come from `THUMBNAIL`, never from a full download
- [ ] Mount `@cubone/react-file-manager` on the adapter
- [ ] Service worker streaming download sink
- [ ] File System Access API sink where available; feature-detect
- [ ] Blob fallback for small files only
- [ ] Progress, cancel, and browser-side backpressure
- [ ] Status UI: Online / Local network / Direct / Relayed / Offline
- [ ] Re-pair flow when the browser key is gone — one click
- [ ] Test the streaming sink on Chrome, Firefox and Safari

**Check:** log in, see the machines, open one, browse it, download a 5 GB file.
**First usable release.**

---

## Step 12 — Real NAT

- [ ] Deploy heimdall and mimir to a VPS behind TLS
- [ ] Public STUN configured
- [ ] IPv6 enabled and confirmed to be tried
- [ ] Report the measured path per session, from the real candidate pair
- [ ] Test: home Wi-Fi to phone hotspot
- [ ] Test: macOS↔Windows, macOS↔Linux, Windows↔Linux
- [ ] Test: both peers behind the same NAT
- [ ] Log connection type per session so the relay share can be counted

**Check:** a direct connection forms across the Internet, proven by the log.

---

## Step 13 — TURN fallback

- [ ] coturn on the VPS, `use-auth-secret` mode
- [ ] mimir mints short-lived TURN credentials per session
- [ ] Listen on UDP, TCP and TLS 443
- [ ] `--force-relay` test flag
- [ ] Test: 1 GB transfer with STUN disabled
- [ ] Rate limit relay use per account

**Check:** a 1 GB file transfers over the relay only.

---

## Step 14 — Write operations

The first step that can destroy data. Section 10 of `PLAN.md` is the spec.

- [ ] `ro` roots refuse every write before any path work happens
- [ ] A grant may narrow a root's mode, never widen it
- [ ] `internal/fsops`: atomic write — temp file in the destination dir,
      fsync file, fsync dir, rename over the target
- [ ] Clean up stale temp files on startup
- [ ] `WRITE` / `WRITE_OK`: upload reusing the xfer channel and credit window
- [ ] Free-space check and size cap before an upload starts
- [ ] `MKDIR` — no `-p` by default; refuse if the parent is missing
- [ ] `MOVE` — validate source and destination separately; both must be `rw`;
      no overwrite without `overwrite: true`; cross-filesystem becomes
      copy-then-delete with a hash check
- [ ] `COPY` — server-side, same destination rules
- [ ] `DELETE` — never recursive without `recursive: true`; never follow a
      symlink out of the root; refuse to delete a share root
- [ ] Wire all of it through `fs-adapter.ts` into the web UI
- [ ] `ratatoskr connect ID put|mkdir|mv|rm`
- [ ] Destructive-action tests: kill mid-upload, disk full, permission denied,
      target vanished, symlinked destination, path traversal on every verb

**Check:** the whole of requirement 3. No half-written file survives a kill.

---

## Step 15 — Survival

- [ ] Sleep and wake the agent machine
- [ ] Switch Wi-Fi to hotspot mid-connection; ICE restart
- [ ] Move between LAN and Internet; discovery re-picks the right path
- [ ] Restart the agent; re-register the same peer id
- [ ] Restart heimdall; everything reconnects
- [ ] mimir down: existing grants still work on the LAN; UI says so
- [ ] Cancel a transfer at 50%; both sides clean up
- [ ] Kill the client mid-transfer; the agent frees the file handle
- [ ] Every failure path ends in a working connection or an honest error.
      Never a hang.

**Check:** the whole list, on all three machines.

---

## Step 16 — Mobile app

- [ ] Client identity in Keychain / Keystore
- [ ] WebRTC peer, same protocol
- [ ] LAN discovery — mobile **can** do this, unlike the web
- [ ] iOS: local network permission + multicast entitlement
- [ ] Android: NSD
- [ ] File manager UI, built rather than adopted
- [ ] Background transfer behaviour on both platforms

**Check:** browse and transfer over the LAN with the phone in aeroplane mode
apart from Wi-Fi.

---

## Cross-cutting, do continuously

- [x] `Makefile`: `build`, `test`, `vet`, `cross`
- [x] `CGO_ENABLED=0` enforced in the Makefile
- [ ] `CGO_ENABLED=0` enforced in CI
- [ ] `go vet` and `staticcheck` clean
- [ ] Structured logging with levels; `--verbose` for ICE and discovery detail
- [ ] Unit tests beside each package. `internal/identity`, `internal/fsroot`
      and `internal/protocol` are the ones that must be thorough
- [ ] `README.md` once step 9 passes

## Deliberately not now

`ratatoskr mount` (local WebDAV bridge) · desktop UI wrapper · installers and
autostart · file index and search · version history · sync · sharing between
accounts · public links · thumbnails · browser on a LAN with no Internet
