# Ratatoskr — TODO

See `PLAN.md` for the why. This file is the what, in order.

Rule: do not start a step until the one above it passes its check.

---

## Step 0 — Two agents talk, no server

- [x] `go mod init github.com/achmadss/ratatoskr`
- [x] Add `github.com/pion/webrtc/v4`
- [x] `cmd/ratatoskr/main.go` with a subcommand router
- [x] `internal/transport`: create a peer connection, one `ctrl` DataChannel
- [x] `ratatoskr dev-offer` — print an SDP offer, wait for an answer on stdin
- [x] `ratatoskr dev-answer` — read an offer from stdin, print an answer
- [x] Echo any string received on `ctrl` back to the sender
- [x] Log the selected ICE candidate pair on connect

**Check:** two terminals, offer and answer pasted by hand, a string comes back.

**PASSED.** Both sides exit 0. Selected pair reported as
`direct: local host 192.168.2.228 <-> remote host 192.168.2.141`.

---

## Step 1 — heimdall

- [ ] `cmd/heimdall/main.go`, flag `--addr`
- [ ] `internal/signal`: message types shared by client and server
- [ ] Server: WebSocket endpoint, peer id registry, relay `offer` / `answer` / `ice`
- [ ] Server: ping every 20 s, drop after two misses
- [ ] Server: re-`register` of a live peer id replaces the old socket
- [ ] Server: per-socket rate limit
- [ ] Server: `peer_gone` notice when a peer disconnects
- [ ] Client: connect, register, reconnect with exponential backoff
- [ ] `internal/config`: generate and persist a device id (peer id)
- [ ] Wire `ratatoskr run` and `ratatoskr connect PEER_ID` to use it

**Check:** `ratatoskr connect <id>` links up in under 2 seconds with no pasting.

---

## Step 2 — Control protocol and LIST

- [ ] `internal/protocol`: envelope, request and reply types, version constant
- [ ] `HELLO` / `HELLO_OK` version negotiation, refuse a mismatch
- [ ] `PING` / `PONG` with round-trip time
- [ ] `internal/fsroot`: clean, join, `EvalSymlinks`, verify inside root
- [ ] `internal/fsroot`: table tests for `..`, absolute paths, symlink escape,
      null bytes, Windows reserved names, alternate data streams
- [ ] `LIST` / `LIST_RESULT`
- [ ] `STAT` / `STAT_RESULT`
- [ ] `ERROR` with codes; never leak a real path or a stack trace
- [ ] Enforce the 64 KB control message cap
- [ ] `ratatoskr connect PEER_ID ls PATH`

**Check:** a real directory prints. Every hostile path in the test table is refused.

---

## Step 3 — Local control API

- [ ] `internal/control`: HTTP server bound to `127.0.0.1`, random free port
- [ ] Random token; write `control.json` (port + token) at mode `0600`
- [ ] Per-OS config dir via `os.UserConfigDir()`; verify on all three machines
- [ ] Bearer token check on every route
- [ ] `GET /v1/status`, `GET /v1/peers`
- [ ] `GET|POST|DELETE /v1/folders`, persisted to config
- [ ] `GET /v1/events` server-sent events
- [ ] Rewire `status`, `peers`, `folders`, `id` to be HTTP clients
- [ ] Clear error when no agent is running

**Check:** `ratatoskr status` reports a running `ratatoskr run`.

---

## Step 4 — Transfer a small file

- [ ] `OPEN` / `OPEN_OK` with size and BLAKE3 hash
- [ ] Open an `xfer-<id>` DataChannel per transfer
- [ ] Binary frame format: uint32 sequence + payload, 16 KB chunks
- [ ] Empty frame means end of file, then close the channel
- [ ] `CANCEL` from either side, clean up both ends
- [ ] Receiver verifies the hash and fails loudly on a mismatch
- [ ] Cap concurrent transfers per peer at 4
- [ ] `ratatoskr connect PEER_ID get PATH OUT` with a progress line

**Check:** a 10 MB file arrives and the hash matches.

---

## Step 5 — Backpressure and a large file

- [ ] Sender: `SetBufferedAmountLowThreshold` 256 KB, pause above 1 MB
- [ ] `CREDIT` message; receiver starts at 64 chunks, tops up at half spent
- [ ] Sender blocks when credits run out
- [ ] Receiver writes straight to disk, never buffers the whole file
- [ ] Handle a file that shrinks, grows or vanishes mid-transfer
- [ ] Measure and log throughput
- [ ] Test: 10 GB over LAN, watch RSS on both sides
- [ ] Test: 4 simultaneous transfers stay stable

**Check:** 10 GB completes and memory stays flat on both sides. **Milestone.**

---

## Step 6 — Real NAT

- [ ] Deploy heimdall to a VPS behind TLS (`wss://`)
- [ ] Configure public STUN servers
- [ ] Enable IPv6 and confirm it is actually tried
- [ ] Report `direct` vs `relay` in `status`, from the real candidate pair
- [ ] Test: home Wi-Fi to phone hotspot
- [ ] Test: macOS to Windows, macOS to Linux, Windows to Linux
- [ ] Test: both peers behind the same NAT
- [ ] Log the connection type per session so the relay rate can be counted

**Check:** a direct connection forms across the Internet, proven by the log.

---

## Step 7 — TURN fallback

- [ ] Install coturn on the VPS, `use-auth-secret` mode
- [ ] heimdall mints short-lived TURN credentials per session
- [ ] Listen on UDP, TCP, and TLS on 443
- [ ] `--force-relay` test flag (`iceTransportPolicy: relay`)
- [ ] Test: 1 GB transfer with STUN disabled
- [ ] Rate limit relay use per peer

**Check:** a 1 GB file transfers over the relay only.

---

## Step 8 — Survival

- [ ] Sleep and wake the serving machine
- [ ] Switch Wi-Fi to hotspot mid-connection; ICE restart
- [ ] Restart the agent; re-register the same peer id
- [ ] Restart heimdall; both agents reconnect
- [ ] heimdall unreachable; agent retries with backoff and reports offline
- [ ] Cancel a transfer at 50%; both sides clean up
- [ ] Kill the client mid-transfer; the server frees the file handle
- [ ] Every failure path ends in a working connection or an honest error.
      Never a hang.

**Check:** the whole list above, on all three machines.

---

## Cross-cutting, do continuously

- [x] `Makefile`: `build`, `test`, `cross` for all targets
- [x] `CGO_ENABLED=0` enforced in the Makefile
- [ ] `CGO_ENABLED=0` enforced in CI
- [ ] `go vet` and `staticcheck` clean
- [ ] Structured logging with levels; `--verbose` for ICE detail
- [ ] Unit tests beside each package; `internal/fsroot` and
      `internal/protocol` are the ones that must be thorough
- [ ] `README.md` once step 4 passes

## Deliberately not now

Web client · UI wrapper · installers · autostart · pairing and auth ·
file index · uploads, deletes, renames · mobile
