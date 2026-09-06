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
- [ ] `ratatoskr connect PEER_ID --via lan`
- [ ] Test on macOS, Windows and Linux; note every firewall prompt
- [ ] Test with the router's Internet uplink physically unplugged

**Check:** `discover` sees the other machine, and `--via lan` transfers with
no Internet at all.

---

## Step 5 — Discovery race

- [ ] Run LAN and heimdall discovery concurrently; first answer wins
- [ ] Cancel the loser cleanly; no leaked goroutine, socket or peer connection
- [ ] Separate timeouts: LAN short (~500 ms), heimdall longer (~5 s)
- [ ] Record which method won, and expose it in `status`
- [ ] Handle: mDNS answers but the peer is unreachable → fall back, do not fail
- [ ] Handle: both fail → one clear error, not two confusing ones
- [ ] Tests with each path forced off

**Check:** LAN wins when present; multicast blocked falls through to heimdall;
neither path hangs.

---

## Step 6 — Control protocol and LIST

- [ ] `PING` / `PONG` with round-trip time
- [ ] `internal/fsroot`: clean, join, `EvalSymlinks`, verify inside root
- [ ] `internal/fsroot` tests: `..`, absolute paths, symlink escape, null
      bytes, Windows reserved names, alternate data streams, `\\?\` prefixes
- [ ] `LIST` / `LIST_RESULT`
- [ ] `STAT` / `STAT_RESULT`
- [ ] `ERROR` codes; never leak a real path or a stack trace
- [ ] Enforce the 64 KB control message cap
- [ ] `ratatoskr connect PEER_ID ls PATH`

**Check:** a real directory prints. Every hostile path in the table is refused.

---

## Step 7 — Local control API

- [ ] `internal/control`: HTTP server on `127.0.0.1`, random free port
- [ ] Random token; write `control.json` at mode 0600
- [ ] Bearer token check on every route
- [ ] `GET /v1/status`, `/v1/peers`, `/v1/discover`
- [ ] `GET|POST|DELETE /v1/folders` and `/v1/trusted`
- [ ] `GET /v1/events` server-sent events
- [ ] Rewire `status`, `peers`, `folders`, `trusted`, `discover` as HTTP clients
- [ ] Clear message when no agent is running
- [ ] Verify the config dir path on all three OSes

**Check:** `ratatoskr status` reports a running `ratatoskr run`.

---

## Step 8 — Transfer a small file

- [ ] `OPEN` / `OPEN_OK` with size and BLAKE3 hash
- [ ] One `xfer-<id>` DataChannel per transfer
- [ ] Frame format: uint32 sequence + payload, 16 KB chunks
- [ ] Empty frame means end of file, then close the channel
- [ ] `CANCEL` from either side; both ends clean up
- [ ] Receiver verifies the hash and fails loudly on a mismatch
- [ ] Cap concurrent transfers per peer at 4
- [ ] `ratatoskr connect PEER_ID get PATH OUT` with a progress line

**Check:** a 10 MB file arrives and the hash matches.

---

## Step 9 — Backpressure and a large file

- [ ] Sender: `SetBufferedAmountLowThreshold` 256 KB, pause above 1 MB
- [ ] `CREDIT` message; receiver starts at 64 chunks, tops up at half spent
- [ ] Sender blocks when credits run out
- [ ] Receiver writes straight to disk; never buffers the whole file
- [ ] Handle a file that shrinks, grows or vanishes mid-transfer
- [ ] Measure and log throughput
- [ ] Test: 10 GB over LAN, watch RSS on both sides
- [ ] Test: 4 simultaneous transfers stay stable

**Check:** 10 GB completes, memory flat on both sides. **Milestone.**

---

## Step 10 — Real NAT

- [ ] Deploy heimdall to a VPS behind TLS (`wss://`)
- [ ] Configure public STUN servers
- [ ] Enable IPv6 and confirm it is actually tried
- [ ] Report `direct` vs `relay` in `status`, from the real candidate pair
- [ ] Test: home Wi-Fi to phone hotspot
- [ ] Test: macOS↔Windows, macOS↔Linux, Windows↔Linux
- [ ] Test: both peers behind the same NAT
- [ ] Log connection type per session so the relay rate can be counted

**Check:** a direct connection forms across the Internet, proven by the log.

---

## Step 11 — TURN fallback

- [ ] Install coturn on the VPS, `use-auth-secret` mode
- [ ] heimdall mints short-lived TURN credentials per session
- [ ] Listen on UDP, TCP, and TLS on 443
- [ ] `--force-relay` test flag
- [ ] Test: 1 GB transfer with STUN disabled
- [ ] Rate limit relay use per peer

**Check:** a 1 GB file transfers over the relay only.

---

## Step 12 — Survival

- [ ] Sleep and wake the serving machine
- [ ] Switch Wi-Fi to hotspot mid-connection; ICE restart
- [ ] Move between LAN and Internet; discovery re-races correctly
- [ ] Restart the agent; re-register the same peer id
- [ ] Restart heimdall; both agents reconnect
- [ ] heimdall unreachable; agent retries with backoff, LAN still works
- [ ] Cancel a transfer at 50%; both sides clean up
- [ ] Kill the client mid-transfer; the server frees the file handle
- [ ] Every failure path ends in a working connection or an honest error.
      Never a hang.

**Check:** the whole list, on all three machines.

---

## Cross-cutting, do continuously

- [x] `Makefile`: `build`, `test`, `vet`, `cross`
- [x] `CGO_ENABLED=0` enforced in the Makefile
- [ ] `CGO_ENABLED=0` enforced in CI
- [ ] `go vet` and `staticcheck` clean
- [ ] Structured logging with levels; `--verbose` for ICE and discovery detail
- [ ] Unit tests beside each package. `internal/identity`, `internal/fsroot`
      and `internal/protocol` are the ones that must be thorough
- [ ] `README.md` once step 8 passes

## Deliberately not now

Web client · UI wrapper · account dashboard and claim tokens · installers ·
autostart · file index · uploads, deletes, renames · mobile ·
browser on an offline LAN
