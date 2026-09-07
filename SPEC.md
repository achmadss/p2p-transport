# P2P File Management App

## 1. Product Overview

The goal is to build a file-management application that allows a user to access and manage files on another computer—typically a laptop or home server—from either:

- The same local network (LAN), or
- The public Internet.

The application should feel like a normal remote file manager. The user should not need to understand:

- NAT traversal
- Port forwarding
- Public IP addresses
- VPN configuration
- WebRTC/libp2p
- Relay servers
- Signaling
- Peer discovery
- Firewall configuration

The key architectural requirement is:

> **File data should travel directly between the user's devices whenever possible. A central server should only coordinate the connection and act as a fallback relay when a direct P2P connection cannot be established.**

**Amended 7 Sep 2026, by the owner.** No user file data travels through
the relay. Ever, and not as a fallback. The relay coordinates: presence,
addresses, signalling, and the metadata that is measured in kilobytes.
Bulk transfer is direct or it does not happen, and a peer that cannot be
reached directly is reported as unreachable rather than served slowly at
the owner's expense.

This is stricter than the paragraph above and replaces the part of it
that permits a fallback relay for file data. Step 3 is what forced the
question: a phone on a mobile carrier reaching a laptop at home is the
ordinary use of this product, and on the carrier measured it is exactly
the case a fallback would capture — so the fallback would be the normal
path, which the requirement in §6 already forbids. Section 6 is amended
in the same way where it describes the fallback.

Conceptually, this is similar to Tailscale, but specialized for **remote file management** rather than providing a general-purpose private network.

---

# 2. Core User Experience

A user should be able to install the application on a computer containing files.

For example:

```text
Home Laptop
└── Agent
    └── Exposes the laptop's filesystem
```

The user can then open the application from another device:

```text
Remote Laptop / Desktop / Phone
└── App
    └── Select "Home Laptop"
        └── Browse files
        └── Upload
        └── Download
        └── Rename
        └── Move
        └── Delete
        └── Create folders
```

The user should experience the remote computer as if it were a normal network file server.

The underlying networking should be invisible.

---

# 3. Example Use Case

Assume the user has a laptop at home:

```text
Home Laptop
192.168.1.30
```

The laptop is running the application's agent.

While outside the home, the user opens the application on another computer.

They select:

```text
Home Laptop
```

and see:

```text
Home Laptop
├── Documents
├── Downloads
├── Pictures
├── Projects
└── Videos
```

The user can then download:

```text
Projects/my-project.zip
```

The ideal network path is:

```text
Remote Device
      │
      │ Direct encrypted P2P connection
      │
      ▼
Home Laptop
```

The central infrastructure should not carry the file.

---

# 4. High-Level Architecture

The system consists of three major components:

```text
                         ┌─────────────────────────┐
                         │       Coordinator       │
                         │                         │
                         │ • Authentication        │
                         │ • Device registration   │
                         │ • Peer discovery        │
                         │ • Presence               │
                         │ • Signaling             │
                         │ • NAT traversal assist  │
                         │ • Relay fallback        │
                         └────────────┬────────────┘
                                      │
                         Connection setup only
                                      │
                    ┌─────────────────┴─────────────────┐
                    │                                   │
                    ▼                                   ▼
          ┌──────────────────┐                ┌──────────────────┐
          │ Client Device    │                │ Home Device      │
          │                  │                │                  │
          │ App              │                │ Agent            │
          │ WebDAV Gateway   │                │ Filesystem       │
          │ P2P Client       │◄══════════════►│ P2P Server       │
          └──────────────────┘   Direct P2P   └──────────────────┘
```

The central server should not be the normal data path.

Its primary role is to help two peers find and connect to each other.

---

# 5. Network Connection Strategy

The application should attempt connections in this order:

```text
1. LAN direct connection
2. Internet direct connection
3. NAT hole punching
4. Encrypted relay fallback
```

## 5.1 LAN Direct

When both devices are on the same network:

```text
Client
192.168.1.20
      │
      │ LAN
      ▼
Home Agent
192.168.1.30
```

The system should prefer the local connection.

No Internet traffic should be required for transferring the actual files.

The coordinator may still be used for authentication/device discovery, but the file traffic should stay on the LAN.

---

## 5.2 Direct Internet Connection

If the devices are on different networks, the application should attempt to establish a direct connection.

Example:

```text
Remote Laptop
      │
      │ Internet
      │
      └─────────────────────────►
                                Home Laptop
```

If the home device has a reachable endpoint, the peers can establish a direct encrypted connection.

---

## 5.3 NAT Traversal / Hole Punching

Many home networks do not expose the laptop directly to the Internet.

The application therefore needs NAT traversal.

Conceptually:

```text
Remote Device
      │
      │
      ▼
   Internet
      │
      │
      ▼
Home NAT
      │
      ▼
Home Laptop
```

The coordinator helps both peers discover how they can reach each other.

The peers then attempt to establish a direct connection.

A P2P networking framework such as **libp2p** can be considered for this layer because it provides functionality around peer identities, transports, NAT traversal, hole punching, and relay mechanisms.

---

# 6. Relay Fallback

Direct P2P connectivity cannot always be guaranteed.

Examples include:

- Symmetric NAT
- Strict corporate firewalls
- Blocked UDP
- Restricted networks
- Certain CGNAT configurations
- Other network policies that prevent inbound or peer-to-peer connections

Therefore, the application should support an encrypted relay.

```text
Client
   │
   │ encrypted traffic
   ▼
Relay Server
   │
   │ encrypted traffic
   ▼
Home Agent
```

The important requirement is:

> **The relay must not become the normal data path.**

**Amended 7 Sep 2026, by the owner: the relay is not a data path at
all.** It carries no user file data, including when direct connectivity
is impossible. What it carries is coordination — presence, addresses,
signalling, hole-punch arrangement — and nothing measured in megabytes.

The listed cases above are therefore not a reason to relay bytes. They
are the work: symmetric NAT is one of them, it is the case step 3
measured, and the answer is to keep opening the path rather than to
route around it at the operator's expense. When no direct path can be
opened, a transfer fails and says so.

The encryption below still holds for what the relay does carry, and the
diagram stays because the property it describes — the relay cannot read
what passes through it — remains a requirement of the coordination
traffic.

The relay should also ideally be unable to inspect file contents.

The end-to-end encryption should exist between the client and home agent:

```text
Client
  │
  │ encrypted
  ▼
Relay
  │
  │ encrypted
  ▼
Agent
```

The relay only forwards encrypted packets.

---

# 7. Central Infrastructure

The central server can be thought of as a **Coordinator** rather than a traditional backend that stores or serves files.

Its responsibilities should include:

## 7.1 Authentication

Authenticate users and devices.

Example:

```text
User
 ├── Desktop
 ├── Laptop
 └── Home Server
```

Each device should have a cryptographic identity.

---

## 7.2 Device Registration

When an agent starts:

```text
Home Agent
    │
    ├── Load/generate device identity
    │
    ├── Connect to coordinator
    │
    └── Register device
```

The coordinator can maintain:

```text
Device ID
Peer ID
Online/offline state
Reachability information
Supported transports
```

---

## 7.3 Presence

The coordinator should know whether a device is currently online.

Example:

```text
Home Laptop     ONLINE
Office Desktop  ONLINE
NAS             OFFLINE
```

---

## 7.4 Peer Discovery

When the user selects:

```text
Home Laptop
```

the client asks the coordinator:

```text
Where is Home Laptop?
```

The coordinator returns the information necessary to attempt a P2P connection.

It should not return or transfer files.

---

## 7.5 Signaling

The coordinator can exchange connection metadata between peers.

Conceptually:

```text
Client ──► Coordinator
             │
             │ connection information
             ▼
          Client + Agent
             │
             ▼
        P2P negotiation
```

Once the P2P connection is established, the coordinator should no longer be involved in the data path.

---

## 7.6 Relay

The coordinator infrastructure may also provide relay servers.

However, relaying should be considered a fallback mechanism.

---

# 8. Client Architecture

The client device should contain several logical components.

```text
┌─────────────────────────────┐
│            App              │
├─────────────────────────────┤
│ UI                          │
│                             │
│ Device Manager              │
│                             │
│ WebDAV Gateway              │
│                             │
│ P2P Client                  │
└─────────────────────────────┘
```

The UI is not required to understand the underlying network protocol.

The WebDAV layer and P2P layer should be separated.

---

# 9. WebDAV as the User-Facing Interface

One important requirement is that the system should support **WebDAV**.

WebDAV is useful because many existing applications can interact with it.

Potential clients include:

- File managers
- Desktop operating systems
- rclone
- Cyberduck
- Other WebDAV-compatible applications

The application can expose a local WebDAV endpoint:

```text
http://localhost:9832/
```

The user-facing application then translates WebDAV operations into the internal P2P file protocol.

For example:

```text
File manager
      │
      │ WebDAV
      ▼
localhost:9832
      │
      ▼
WebDAV Gateway
      │
      │ Internal file protocol
      ▼
P2P Connection
      │
      ▼
Home Agent
      │
      ▼
Filesystem
```

This allows existing WebDAV-compatible software to work without understanding P2P networking.

---

# 10. Do Not Make WebDAV the Internal Protocol

WebDAV should ideally be treated as an adapter rather than the fundamental protocol.

The architecture should instead be:

```text
                WebDAV
                   │
                   ▼
            WebDAV Adapter
                   │
                   ▼
             File API / RPC
                   │
                   ▼
               P2P Layer
                   │
                   ▼
                Agent
                   │
                   ▼
             Filesystem
```

This provides more flexibility.

In the future, additional interfaces could be added:

```text
WebDAV
SFTP
FUSE
SMB
Native UI
Web UI
Mobile UI
CLI
```

All of them could use the same internal file-management API.

---

# 11. Internal File Protocol

The internal protocol should represent filesystem operations rather than WebDAV-specific concepts.

For example:

```text
LIST /Documents
GET /Documents/report.pdf
PUT /Documents/report.pdf
DELETE /Documents/old-report.pdf
MOVE /Documents/a.txt /Archive/a.txt
MKDIR /Documents/New Folder
STAT /Documents/report.pdf
```

The exact protocol is an implementation detail, but it should support at least:

## File Operations

- List directory
- Get file metadata
- Download file
- Upload file
- Delete file
- Rename file
- Move file
- Copy file
- Create directory
- Delete directory

## Metadata

Potential metadata:

```text
name
path
size
modified time
created time
directory/file type
permissions
```

The protocol should also be designed for large files and resumable transfers.

---

# 12. Home Agent

The home computer runs a small background agent.

Example:

```text
home-agent
```

The agent is responsible for:

```text
Filesystem access
      │
      ├── File operations
      ├── Directory operations
      ├── Metadata
      └── Streaming
              │
              ▼
          P2P Server
```

The agent should not require the user to configure:

- Port forwarding
- Dynamic DNS
- Public IP addresses
- VPNs
- Firewall rules, where avoidable

The goal is for installation to be as close as possible to:

```text
Install → Sign in → Device appears online
```

---

# 13. Device Identity

Each device should have a persistent cryptographic identity.

Example:

```text
User
  │
  ├── Device A
  │     └── cryptographic identity
  │
  ├── Device B
  │     └── cryptographic identity
  │
  └── Device C
        └── cryptographic identity
```

This is preferable to relying only on:

```text
IP address
hostname
MAC address
```

because IP addresses change and devices frequently move between networks.

A peer identity should allow the application to recognize:

```text
"This is my Home Laptop"
```

even if its network address changes.

---

# 14. Security Model

Security is a major part of the design.

The system should assume:

> The Internet and relay infrastructure are untrusted.

The desired trust model is:

```text
Client
   │
   │ End-to-end encrypted
   │
   ▼
Agent
```

The coordinator should primarily establish identity and connectivity.

The relay should only forward encrypted traffic.

The coordinator should not need access to:

- Files
- File contents
- File names, if the protocol can avoid exposing them
- Filesystem contents

At minimum, the system should provide:

- Device authentication
- Peer authentication
- Encrypted P2P transport
- Authorization between devices
- Secure device enrollment
- Ability to revoke devices

---

# 15. Device Authorization

The user should explicitly control which devices can access another device.

Example:

```text
Home Laptop

Allowed devices:
[x] My Desktop
[x] My Phone
[ ] Office Laptop
```

Alternatively, access can be managed through a device-pairing flow.

Example:

```text
Home Laptop
    │
    │ displays pairing code
    ▼
123-456

Remote Device
    │
    └── Enter pairing code
```

After pairing, the devices trust each other.

---

# 16. Important Distinction: P2P vs Relay

The application should clearly distinguish:

### Direct

```text
Client ═══════════════ Agent
```

File traffic goes directly between devices.

### Relay

```text
Client ───── Relay ───── Agent
```

File traffic passes through the relay, but remains end-to-end encrypted.

The application can expose connection status to the user:

```text
Home Laptop
● Direct connection
```

or:

```text
Home Laptop
● Relayed connection
```

This is useful for troubleshooting and bandwidth awareness.

---

# 17. Desired Connection Lifecycle

A typical connection should look like this:

```text
1. User opens application

2. Client authenticates

3. Client retrieves user's devices

4. User selects "Home Laptop"

5. Client asks coordinator for peer information

6. Client attempts LAN/direct connection

7. If necessary, peers perform NAT traversal

8. If direct connection succeeds:

       Client ═════════ Agent

9. If direct connection fails:

       Client ── Relay ── Agent

10. Establish encrypted session

11. Client exposes filesystem through WebDAV/file API

12. User performs file operations

13. File bytes travel through the P2P path

14. Session closes when finished
```

---

# 18. Example: Downloading a File

Suppose the user wants:

```text
Home Laptop
└── Documents
    └── project.zip
```

The flow is:

```text
User
 │
 ▼
WebDAV client
 │
 │ GET /Documents/project.zip
 ▼
WebDAV Gateway
 │
 ▼
Internal GET request
 │
 ▼
P2P connection
 │
 ═══════════════════════════════
 │
 ▼
Home Agent
 │
 ▼
Filesystem
 │
 ▼
project.zip
```

The file should stream directly back over the established encrypted P2P connection.

The coordinator is not involved.

---

# 19. Example: Uploading a File

```text
Remote Device
 │
 │ PUT /Documents/photo.jpg
 ▼
WebDAV Gateway
 │
 ▼
P2P File Protocol
 │
 ═══════════════════════════════
 │
 ▼
Home Agent
 │
 ▼
Filesystem
 │
 └── Documents/photo.jpg
```

Again, the coordinator is not part of the normal file transfer path.

---

# 20. Large File Requirements

The implementation should assume files may be very large.

The transfer protocol should support:

- Streaming
- Backpressure
- Resumable transfers
- Partial reads
- Partial writes
- Transfer cancellation
- Connection recovery
- Integrity verification

For example:

```text
10 GB file

Client ───────────────► Agent

Transferred:
4.2 GB

Connection interrupted.

Resume:
4.2 GB ───────────────► 10 GB
```

The system should not require restarting a large transfer from zero whenever a connection briefly fails.

---

# 21. Performance Goals

The ideal path should have approximately the performance characteristics of the underlying network connection.

For example:

```text
Same LAN:

Client ─────────────── Agent
        1 Gbps LAN

Remote Internet:

Client ═══════════════ Agent
        Internet bandwidth
```

The relay path should be considered a degraded mode:

```text
Client ──► Relay ──► Agent
```

This introduces:

- Additional latency
- Additional bandwidth consumption
- Potential throughput limitations

Therefore, direct connectivity should always be preferred.

---

# 22. Technology Direction

A possible implementation stack is:

## Networking

Consider:

- libp2p
- QUIC
- NAT traversal
- Hole punching
- Relay support

libp2p is particularly interesting because it provides much of the lower-level P2P infrastructure required for this architecture.

The application should avoid implementing a complete NAT traversal system from scratch unless there is a strong reason to do so.

---

## Backend / Agent

Go is a strong candidate because:

- Excellent networking support
- Good concurrency model
- Easy cross-platform compilation
- Mature filesystem APIs
- Good support for QUIC/libp2p
- Suitable for lightweight background agents

Possible structure:

```text
cmd/
  agent/
  client/

internal/
  auth/
  coordinator/
  filesystem/
  fileprotocol/
  p2p/
  relay/
  webdav/
```

---

# 23. Possible Component Structure

A more complete architecture could look like:

```text
                         ┌─────────────────────┐
                         │     Coordinator     │
                         │                     │
                         │ Auth                │
                         │ Device Registry     │
                         │ Presence            │
                         │ Discovery           │
                         │ Signaling           │
                         └──────────┬──────────┘
                                    │
                                    │
                 ┌──────────────────┴──────────────────┐
                 │                                     │
                 ▼                                     ▼

        ┌──────────────────┐                  ┌──────────────────┐
        │ Client           │                  │ Home Agent       │
        │                  │                  │                  │
        │ UI               │                  │ Device Identity  │
        │                  │                  │                  │
        │ WebDAV Gateway   │                  │ P2P Server       │
        │                  │                  │                  │
        │ File API Client  │◄════════════════►│ File API Server  │
        │                  │                  │                  │
        │ P2P Client       │                  │ Filesystem       │
        └──────────────────┘                  └──────────────────┘
                    ╲                                ╱
                     ╲                              ╱
                      ╲──── Relay if necessary ───╱
```

---

# 24. Coordinator Should Be Stateless About File Data

A fundamental design principle should be:

> **The coordinator knows about devices, not files.**

The coordinator may know:

```text
User A
 ├── Device 1: online
 ├── Device 2: offline
 └── Device 3: online
```

It should not need to know:

```text
Device 1
 ├── Documents
 │   ├── secret.pdf
 │   └── passwords.txt
 └── Photos
```

Filesystem metadata should remain between the client and agent.

---

# 25. Why This Is Different From a Traditional File Server

A traditional architecture would look like:

```text
Client
   │
   ▼
Central Server
   │
   ▼
Home Device
```

The server becomes a mandatory data path.

That creates:

- Infrastructure bandwidth costs
- Additional latency
- Centralized data flow
- Scaling requirements
- Potential privacy concerns

The proposed architecture is:

```text
                    Coordinator
                   /            \
                  /              \
             signaling        signaling
                /                  \
               ▼                    ▼
          Client ═══════════════ Home Agent
                    P2P
```

The central infrastructure does not need to scale with the total volume of files transferred during successful direct connections.

---

# 26. WebDAV Compatibility Constraint

There is one important limitation.

If an arbitrary WebDAV client connects directly to a public URL:

```text
WebDAV Client
      │
      ▼
https://example.com/home-laptop/
```

then the public server necessarily becomes part of the data path unless that WebDAV client itself understands the P2P protocol.

Therefore, the preferred model is:

```text
Arbitrary WebDAV software
          │
          ▼
Local WebDAV endpoint
          │
          ▼
Your P2P client
          │
          ═══════════════
          │
          ▼
Home Agent
```

For example:

```text
http://localhost:9832/home-laptop/
```

The local component translates WebDAV into P2P.

This preserves both goals:

1. Compatibility with existing WebDAV clients.
2. Direct P2P file transfer.

---

# 27. Potential Product Forms

The same core system could eventually support multiple interfaces.

## Desktop Application

```text
┌─────────────────────────────┐
│ My Devices                  │
├─────────────────────────────┤
│ ● Home Laptop               │
│ ● Desktop                   │
│ ○ NAS                       │
└─────────────────────────────┘
```

Selecting a device opens its filesystem.

---

## Web UI

The web UI could communicate with a locally running client/agent.

```text
Browser
   │
   ▼
Local App
   │
   ▼
P2P
   │
   ▼
Home Agent
```

---

## WebDAV

```text
File Manager
   │
   ▼
localhost:9832
   │
   ▼
P2P
```

---

## CLI

A CLI could provide:

```text
app devices
app ls home:/Documents
app get home:/Documents/report.pdf
app put report.pdf home:/Documents/
app rm home:/Documents/old.pdf
```

---

# 28. MVP Scope

The first version should avoid trying to build every possible interface.

A sensible MVP would be:

## Coordinator

- User authentication
- Device registration
- Device discovery
- Presence
- Signaling

## Agent

- Device identity
- Filesystem access
- P2P listener
- File protocol
- Basic authorization

## Client

- Device list
- Device connection
- Directory browsing
- Upload
- Download
- Rename
- Delete
- Create directory

## WebDAV

- Local WebDAV endpoint
- Translate WebDAV operations into the internal file protocol

## Networking

- LAN direct connection
- Internet direct connection
- NAT traversal
- Relay fallback

---

# 29. MVP Connection Flow

The first working milestone should be:

```text
Laptop A
   │
   │ App
   │
   ▼
Connect to Laptop B
   │
   ▼
Discover peer
   │
   ▼
Establish P2P connection
   │
   ▼
List filesystem
   │
   ▼
Download file
```

Then test:

```text
Same LAN
```

followed by:

```text
Different networks
```

followed by:

```text
NAT traversal
```

followed by:

```text
Relay fallback
```

---

# 30. Important Design Principles

## Principle 1 — P2P First

Always attempt direct connectivity before using a relay.

```text
Direct > Relay
```

---

## Principle 2 — Coordinator Is Not a File Server

The coordinator should not normally see file traffic.

```text
Coordinator:
    connection metadata

Client ↔ Agent:
    file data
```

---

## Principle 3 — End-to-End Encryption

Even relay traffic should remain encrypted between the client and agent.

```text
Client
  │
  │ encrypted
  ▼
Relay
  │
  │ encrypted
  ▼
Agent
```

---

## Principle 4 — Hide Networking Complexity

The user should not need to know whether the connection is:

- LAN
- Direct Internet
- Hole-punched
- Relayed

The application should handle this automatically.

---

## Principle 5 — WebDAV Is an Interface

WebDAV should not define the internal architecture.

Use:

```text
WebDAV
   ↓
File API
   ↓
P2P
```

rather than:

```text
WebDAV
   ↓
P2P
```

This allows additional interfaces later.

---

## Principle 6 — Optimize for Large Transfers

The protocol should be stream-oriented and support resumable transfers.

---

# 31. Long-Term Vision

The final product should feel like:

> **A private, P2P network drive for your own devices.**

A user installs the software on their home laptop.

The laptop appears in their device list:

```text
My Devices

● Home Laptop
● Desktop
● Server
● NAS
```

From anywhere, the user can select:

```text
Home Laptop
```

and access its files.

The underlying system automatically determines:

```text
Are we on the same LAN?
        │
        ├── Yes → LAN connection
        │
        └── No
             │
             ▼
       Can we connect directly?
             │
             ├── Yes → Direct P2P
             │
             └── No → Encrypted Relay
```

The user sees none of this complexity.

They simply see:

```text
Home Laptop
├── Documents
├── Downloads
├── Pictures
├── Projects
└── Videos
```

---

# 32. Summary Architecture

The core concept can be summarized as:

```text
                         CENTRAL INFRASTRUCTURE
                    ┌────────────────────────────┐
                    │        Coordinator         │
                    │                            │
                    │ Authentication             │
                    │ Device discovery           │
                    │ Presence                   │
                    │ Signaling                  │
                    │ NAT traversal coordination │
                    │ Relay fallback             │
                    └─────────────┬──────────────┘
                                  │
                           setup / signaling
                                  │
                 ┌────────────────┴────────────────┐
                 │                                 │
                 ▼                                 ▼
        ┌──────────────────┐              ┌──────────────────┐
        │ Client Device    │              │ Home Device      │
        │                  │              │                  │
        │ WebDAV Gateway   │              │ Agent            │
        │ File API Client  │              │ File API Server  │
        │ P2P Client       │              │ P2P Server       │
        └────────┬─────────┘              └────────┬─────────┘
                 │                                 │
                 └══════════ DIRECT P2P ═══════════┘
                              preferred

                              OR

                 ┌───────────────────────────────┐
                 │        Encrypted Relay        │
                 └───────────────────────────────┘
                         fallback only
```

The fundamental architecture is therefore:

```text
                WebDAV / UI / CLI
                       │
                       ▼
                  Local Client
                       │
                       ▼
                Internal File API
                       │
                       ▼
                Encrypted P2P
                       │
              ┌────────┴────────┐
              │                 │
          Direct P2P          Relay
          preferred          fallback
              │                 │
              └────────┬────────┘
                       ▼
                   Home Agent
                       │
                       ▼
                  Filesystem
```

This architecture gives the application the core properties desired:

- Remote file management
- LAN support
- Internet support
- P2P data transfer
- Centralized signaling
- Relay fallback
- End-to-end encryption
- WebDAV compatibility
- No mandatory central data path
- No port-forwarding requirement for the user
- Ability to reuse existing P2P networking technology
- Ability to add other file-management interfaces later
