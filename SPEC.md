# p2p-transport — Specification

This repository is a **transport**. It moves bytes between two machines
that belong to the same person, over the best path it can find, and it
tells the caller nothing about how it did that beyond what the caller
genuinely needs.

It is not an application. There are no files in it, no folders, no
accounts, no user interface. Those belong to whatever is built on top,
and this document is written so that thing can be built without ever
learning what QUIC is.

An earlier version of this file specified a whole product — a private
network drive with WebDAV, a coordinator with accounts, thumbnails, a
web UI. That product is still the reason this transport exists, and its
requirement now lives with the code that implements it. What survives
here is only the part that binds layer 4. See §11 for the list of what
moved out; `git log` has the original if the wording is ever needed
again.

---

## 1. What this is

A Go package, plus one server binary.

```text
   application  (files, WebDAV, accounts, UI)   ← another repository
        │
        │  peer id · stream · path
        ▼
   p2p-transport                                ← this repository
        │
        ▼
   the network
```

The package gives an application three things and hides everything else:
a **verified peer identity**, a **stream** to any peer that identity can
reach, and an honest **report of the path** the bytes are taking. The
server binary is a relay, for the pairs that cannot reach each other any
other way.

The application decides what to send. This decides how it gets there.

---

## 2. The rule that decides everything

> **Bytes travel directly between the two machines whenever that is
> possible. A server may help two peers find each other, and may forward
> ciphertext when no direct path exists, but it must never be the normal
> data path.**

Every other requirement in this document is downstream of that one. If a
change would make the relay the ordinary way two machines talk, it is
wrong, however much simpler it looks.

Relaying is not a failure. Carrying real data over the relay is expected
for the fraction of pairs that cannot punch through their networks, and
must work correctly and be reported honestly. What is forbidden is
relaying being *normal*.

The relays are fleet infrastructure and belong to layer 4: how many
exist, where a peer is placed, and how much bandwidth it may use are all
questions this repository answers. It answers them about **subjects** —
opaque identifiers carrying a minimum and a maximum rate — never about
accounts. Who owns a subject is §11's business, and stays out.

---

## 3. What the caller gets

Exactly three things. Everything else is an implementation detail and
must be reachable only through a diagnostics surface.

### 3.1 An identity, and proof of the other one

Each machine has a persistent keypair, generated on first run and kept
in a file only its owner can read. The identity derived from it is
stable across restarts, address changes, and moves between networks.

When a stream arrives, the identity of the peer that opened it is
already proven — the transport will not deliver a stream it could not
authenticate. The application never needs a challenge-response of its
own.

The transport says **who**. It does not say **may they**. Authorisation
is the application's, and this document says nothing more about it.

### 3.2 A stream

Given a peer identity, the caller can open a stream and write to it. It
is an ordered, reliable, end-to-end encrypted byte stream with working
backpressure: a writer that outruns its reader blocks, without buffering
the difference in memory.

Streams are cheap and many may be open at once. Cancelling a transfer is
closing its stream.

A stream can die. The transport reconnects the peer underneath, but the
stream that was open when the network changed is gone, and the request
that was travelling on it is gone with it. The transport must **report**
that rather than hide it. Recovering the work — resuming a half-finished
transfer from an offset — is the application's, because only the
application knows what a byte offset means.

### 3.3 A path

The caller can ask which path a peer is currently reached by, and be
told one of:

```text
lan      a private address on this network
direct   a public address, across the Internet
relay    forwarded by a server, encrypted end to end
```

This is topology, not mechanism. It says where the bytes go, never how
the connection was made.

Two reasons the transport must expose it, and they are the only two.
First, a person troubleshooting a slow transfer deserves to know it is
relayed. Second, bulk transfers cost differently on different paths, so
a caller moving gigabytes may want to know when a better one appears.
Small requests should never consult it: a kilobyte over the relay costs
nothing worth the code.

The path must be **measured from the live connection, never guessed**
from the intent that opened it.

---

## 4. What the caller never sees

These words must not appear in any exported name, any error returned to
a caller, or any output an ordinary user reads:

```text
QUIC · TCP · Noise · multiaddr · circuit · DCUtR · AutoNAT
STUN · reservation · hole punch · NAT · mDNS
```

They may appear — in full detail — behind an explicit diagnostics flag,
which is what makes a bug report useful. Everywhere else they are a leak
of layer 4 into layer 7, and the point of this repository is that the
leak does not happen.

An application that has to reason about any of the above to work
correctly is evidence of a bug here, not a shortcoming there.

---

## 5. Connection strategy

The transport tries the cheapest path that works, in this order:

```text
1. LAN               a private address on the same network
2. Direct Internet   a public address, punching through NAT if needed
3. Relay             a server forwarding ciphertext
```

It walks that ladder in **both** directions, without being asked.

Upward: a pair that started on the relay keeps trying for something
better for as long as it stays there, and moves to it when it lands. The
attempt runs from both ends at once, timed from the same event, because
two simultaneous outbound attempts are what gets through a NAT that
would refuse either one alone.

Downward: a pair whose path dies falls to the next one that works — LAN
to direct, direct to relay — and, having landed, immediately starts
climbing again.

Neither direction is a caller's decision, and no caller is required to
call anything to make it happen. Existing streams are not migrated; only
the connection under future streams changes.

A connection discovered on the LAN stays on the LAN. It must be possible
to unplug the Internet and have two machines on one switch keep working,
with no server contacted at all.

---

## 6. Security

The Internet and any relay are untrusted.

Encryption is end to end, between the two machines, and is established
before any application byte is written. A relay forwards ciphertext it
cannot read; it learns that two identities are talking and how much, and
nothing else.

The identity check happens during the handshake, not after it. A
connection to a peer whose key does not match the identity dialled must
fail, not warn.

Key material never leaves the machine that generated it, and the file
holding it is refused if other users on that machine can read it.

---

## 7. Large transfers

Transfers may be tens of gigabytes.

The transport must stream, never buffer a whole transfer, apply
backpressure from the reader to the writer, and allow a transfer to be
cancelled at once. Memory use on both sides must be flat and independent
of transfer size.

Integrity, resume, and retry are the application's. The transport's
obligation is to make them possible: report the failure clearly and
promptly, keep the peer reachable so the application can reissue, and
never silently truncate a stream as though it had completed.

---

## 8. Performance

The direct paths should reach approximately the speed of the underlying
network. On a 1 Gbps LAN, a transfer should be limited by the LAN.

The relay is explicitly a degraded mode — more latency, less throughput,
and a bandwidth bill for whoever runs it. It must work; it must not be
where an ordinary transfer ends up.

Every performance claim in this repository must be a number someone
measured on real hardware and wrote down. An estimate presented as a
measurement is a defect.

---

## 9. Zero configuration

A user must not have to configure port forwarding, dynamic DNS, a static
address, a VPN, or a firewall rule for the normal case to work. The
transport discovers what it needs about its own network by itself, and
re-discovers it when the network changes underneath it.

Where a firewall or an endpoint security product does block it, the
failure must be legible rather than a hang.

---

## 10. Portability

One codebase runs on Windows, macOS and Linux, on amd64 and arm64, and
cross-compiles to all of them from any one of them. That constraint is
load-bearing: it rules out cgo, and every dependency that needs it.

---

## 11. Out of scope

None of the following belongs in this repository. Each is the job of the
application built on top:

```text
files, folders, listings, metadata, thumbnails
a file protocol or RPC surface of any kind
WebDAV, SFTP, FUSE, SMB, HTTP APIs
accounts, sign-in, device registries, presence
authorisation policy, sharing, revocation
resume state, transfer history, integrity hashes
any user interface
```

The transport gives that application a peer id, a stream, and a path.
That is the whole contract, and it is deliberately small enough to
freeze.
