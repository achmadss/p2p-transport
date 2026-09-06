# P2P Personal File Server --- Implementation Plan

## 1. Goal

Build a personal file-access application where:

-   The user's files remain on their own laptop.
-   A browser can access those files remotely over the Internet.
-   File bytes travel **peer-to-peer whenever possible**.
-   The user does not need to understand WebRTC, NAT, STUN, TURN, port
    forwarding, tunnels, or certificates.
-   The application handles connection setup, authentication, discovery,
    and retries automatically.
-   Your own infrastructure is limited to lightweight
    coordination/static hosting rather than file storage or file
    bandwidth.

Target user experience:

``` text
Install desktop app
        ↓
Select folders
        ↓
Sign in / pair device
        ↓
App says "Online"
        ↓
Open files.diewax.dev
        ↓
Authenticate
        ↓
Browse laptop files
        ↓
Download directly from laptop
```

------------------------------------------------------------------------

## 2. Recommended Architecture

Use **WebRTC DataChannels** as the transport layer and reuse existing
open-source projects rather than implementing WebRTC protocol handling
from scratch.

Recommended stack:

  -----------------------------------------------------------------------
  Component               Recommended technology  Purpose
  ----------------------- ----------------------- -----------------------
  Web frontend            React / Next.js         File browser UI

  P2P transport           WebRTC DataChannel      Actual file transfer

  WebRTC abstraction      PeerJS                  Simplify WebRTC
                                                  connection management

  Signaling               PeerServer              Exchange WebRTC
                                                  connection information

  NAT traversal           STUN                    Establish direct
                                                  connections

  NAT fallback            TURN / coturn or        Relay only when direct
                          managed TURN            P2P fails

  Desktop agent           Go                      Local filesystem
                                                  access + P2P peer

  Authentication          App-level               Authorize access to the
                          capability/token        desktop agent

  Metadata                Laptop-local            File tree, metadata,
                          database/index          settings

  Public hosting          Static hosting /        Serve the web client
                          Cloudflare Pages        

  DNS                     Cloudflare DNS          `files.diewax.dev`
  -----------------------------------------------------------------------

Core principle:

``` text
                         ┌───────────────────────┐
                         │ files.diewax.dev      │
                         │ Static Web App        │
                         └───────────┬───────────┘
                                     │
                                     │ HTTPS
                                     ▼
                              ┌─────────────┐
                              │   Browser   │
                              └──────┬──────┘
                                     │
                         WebRTC P2P  │
                                     │
                         ════════════╪════════════
                                     │
                              ┌──────▼──────┐
                              │   Laptop    │
                              │             │
                              │ Go Agent    │
                              │             │
                              │ Local Files │
                              └─────────────┘
```

Signaling/STUN/TURN are auxiliary services. They should not be in the
normal file-data path.

------------------------------------------------------------------------

## 3. Existing Open-Source Projects to Reuse

### 3.1 FilePizza --- reference implementation

Repository:

https://github.com/kern/filepizza

FilePizza is the closest existing project to the desired browser-side
behavior. It uses WebRTC for direct browser-to-browser file transfer and
PeerJS for the WebRTC layer.

Use it primarily as:

-   Architecture reference
-   Browser-side transfer implementation reference
-   Chunking/progress/error-handling reference
-   Security model reference
-   WebRTC configuration reference

Do **not** blindly make FilePizza the entire application. Its primary
abstraction is "browser uploads a file and another browser downloads
it", while this project needs:

``` text
Browser
   ↓
Laptop filesystem
   ↓
File listing
   ↓
File metadata
   ↓
Random-access / streamed downloads
```

The laptop therefore needs to become a first-class P2P peer.

License: BSD 3-Clause.

------------------------------------------------------------------------

### 3.2 PeerJS

Repository:

https://github.com/peers/peerjs

PeerJS provides a higher-level API over WebRTC and supports data
channels.

Use it for:

-   Peer creation
-   Peer IDs
-   Data connections
-   WebRTC connection lifecycle
-   DataChannel communication
-   ICE configuration

This avoids implementing raw browser WebRTC signaling and DataChannel
management initially.

License: MIT.

------------------------------------------------------------------------

### 3.3 PeerServer

Repository:

https://github.com/peers/peerjs-server

PeerServer provides the signaling layer required to establish PeerJS
connections.

Important:

> PeerServer does not proxy the actual P2P data.

It is therefore suitable for the architecture because the server can
remain tiny even if users transfer large files.

License: MIT.

Potential deployment:

``` text
peer.diewax.dev
        │
        └── PeerServer
```

This can initially run on a very small instance or another low-cost
server.

Later, the signaling service can be replaced with another signaling
mechanism if desired.

------------------------------------------------------------------------

### 3.4 LocalSend --- useful reference, not the primary transport

Repository:

https://github.com/localsend/localsend

LocalSend is an excellent reference for:

-   Desktop UX
-   Device identity
-   Pairing
-   Local device discovery
-   File permission handling
-   Cross-platform desktop behavior
-   Security UX

However, LocalSend primarily targets direct communication over the local
network rather than remote Internet access.

Therefore:

-   Reuse concepts and UX patterns.
-   Do not make LocalSend the core Internet transport.
-   Consider integrating local-network optimization later.

License: Apache-2.0.

------------------------------------------------------------------------

## 4. Why the Desktop Agent Should Be Custom

The critical difference from FilePizza is that the "source" is not
another browser.

The laptop needs a long-running local application:

``` text
┌────────────────────────────────────────────┐
│ Diewax File Agent                          │
│                                            │
│ ┌──────────────┐  ┌─────────────────────┐ │
│ │ WebRTC       │  │ Authentication      │ │
│ │ Connection   │  │                     │ │
│ └──────┬───────┘  └─────────────────────┘ │
│        │                                   │
│ ┌──────▼───────┐  ┌─────────────────────┐ │
│ │ RPC Protocol │  │ File Index          │ │
│ └──────┬───────┘  └─────────────────────┘ │
│        │                                   │
│ ┌──────▼─────────────────────────────────┐ │
│ │ Local Filesystem                       │ │
│ └────────────────────────────────────────┘ │
└────────────────────────────────────────────┘
```

Go is a good fit because the agent needs:

-   Native filesystem access
-   Low memory usage
-   Easy background service/tray application
-   Windows/macOS/Linux support
-   Straightforward distribution
-   Good concurrency
-   Easy integration with a WebRTC library

For the desktop agent, evaluate Go WebRTC libraries such as Pion WebRTC
if PeerJS cannot be used directly from the desktop side.

Pion WebRTC:

https://github.com/pion/webrtc

This allows:

``` text
Browser
  │
  │ WebRTC
  ▼
PeerJS
  │
  │
  ▼
Pion WebRTC
  │
  ▼
Go File Agent
```

The browser and desktop agent can therefore share the same WebRTC
transport concept while using platform-appropriate libraries.

------------------------------------------------------------------------

# 5. Application Components

## 5.1 Web Client

Responsibilities:

-   Login/authentication
-   Connect to the user's laptop
-   Display device status
-   Browse directories
-   Display file metadata
-   Start downloads
-   Display transfer progress
-   Cancel transfers
-   Handle reconnects
-   Display connection type/status

The web client should know as little as possible about networking.

Instead of exposing:

``` text
ICE
STUN
TURN
SDP
candidate
DataChannel
```

the UI should expose:

``` text
Connecting...
Connected directly
Connected through relay
Laptop offline
Reconnecting...
```

------------------------------------------------------------------------

## 5.2 Desktop Agent

Responsibilities:

1.  Maintain device identity.
2.  Connect to the signaling service.
3.  Register as a PeerJS/WebRTC peer.
4.  Accept authorized browser connections.
5.  Expose a small RPC protocol.
6.  Read local filesystem metadata.
7.  Stream file contents.
8.  Enforce access permissions.
9.  Handle multiple connections.
10. Run in the background.
11. Reconnect automatically.

The agent should never expose the entire filesystem by default.

Example:

``` text
Allowed folders:

~/Documents
~/Downloads
~/Videos
```

Internally:

``` text
filesystem root
    │
    ├── Documents
    ├── Downloads
    └── Videos
```

The application should reject requests outside the configured roots.

------------------------------------------------------------------------

# 6. P2P Protocol

Do not send arbitrary JavaScript objects over the DataChannel without
defining a protocol.

Define a small versioned protocol.

Example:

``` text
HELLO
AUTH
LIST
STAT
DOWNLOAD
DOWNLOAD_CHUNK
CANCEL
ERROR
PING
```

Example request:

``` json
{
  "version": 1,
  "request_id": "abc123",
  "type": "LIST",
  "path": "/Videos"
}
```

Response:

``` json
{
  "version": 1,
  "request_id": "abc123",
  "type": "LIST_RESULT",
  "entries": [
    {
      "name": "movie.mkv",
      "type": "file",
      "size": 4294967296,
      "modified": "2026-09-05T08:00:00Z"
    }
  ]
}
```

For downloads:

``` text
Browser
   │
   │ DOWNLOAD(path, offset, length)
   ▼
Laptop
   │
   │ binary chunks
   ▼
Browser
```

Use bounded chunks rather than attempting to send an entire large file
as one DataChannel message.

------------------------------------------------------------------------

# 7. File Transfer Design

The browser should stream the received data directly into a download
mechanism.

Avoid:

``` text
Entire 20 GB file
        ↓
Browser RAM
        ↓
Save
```

Prefer:

``` text
Laptop
   ↓
WebRTC chunks
   ↓
Browser stream
   ↓
Disk
```

For very large files, investigate:

-   Streams API
-   Service Worker
-   File System Access API where supported
-   Browser-compatible streaming download techniques

FilePizza is useful as a reference for browser-side streaming and
large-transfer behavior.

------------------------------------------------------------------------

# 8. Connection Establishment

The desired flow is:

``` text
Browser
   │
   │ 1. Authenticate
   ▼
Signaling
   │
   │ 2. Locate laptop peer
   ▼
Laptop
   │
   │ 3. Exchange WebRTC information
   ▼
STUN
   │
   │ 4. Discover network paths
   ▼
Browser ═══════════════ Laptop
             P2P
```

The application should attempt connections in this order:

1.  Direct WebRTC connection.
2.  STUN-assisted NAT traversal.
3.  TURN relay if direct connectivity fails.
4.  Retry with another ICE configuration if available.

The user should not have to configure:

-   Port forwarding
-   Static IP
-   Dynamic DNS
-   Router settings
-   Firewall rules
-   Cloudflare Tunnel

------------------------------------------------------------------------

# 9. TURN Strategy

TURN is the important exception to "no infrastructure".

Some networks will not permit a direct WebRTC connection.

Therefore:

``` text
Best case:

Browser ═════════════════ Laptop
             P2P


Fallback:

Browser ───────── TURN ───────── Laptop
```

TURN should be treated as a fallback, not the default.

Potential options:

### Option A --- Run coturn

Open-source TURN server:

https://github.com/coturn/coturn

Pros:

-   Full control
-   Predictable behavior
-   Open source
-   Can be hosted cheaply

Cons:

-   Requires a public server
-   TURN traffic consumes bandwidth
-   Requires operational maintenance

### Option B --- Managed TURN

Use a third-party managed TURN provider.

Pros:

-   No TURN server maintenance
-   Easy scaling
-   Potentially better global connectivity

Cons:

-   Costs money
-   Adds a third-party dependency

Recommendation for MVP:

> Use a managed/public TURN service or a tiny coturn server. Measure how
> often TURN is actually required before optimizing this component.

------------------------------------------------------------------------

# 10. Signaling Architecture

Start with PeerServer.

``` text
                    ┌────────────────┐
                    │ PeerServer     │
                    │ peer.diewax.dev│
                    └───────┬────────┘
                            │
                  signaling only
                            │
             ┌──────────────┴──────────────┐
             ▼                             ▼
         Browser                         Laptop
             │                             │
             └═════════════════════════════┘
                       WebRTC
```

The signaling server should not receive:

-   File contents
-   File chunks
-   File uploads
-   Large metadata payloads

It should only coordinate connection establishment.

------------------------------------------------------------------------

# 11. Authentication Model

Do not make the WebRTC peer ID itself the authentication credential.

Peer IDs should be considered discoverable.

Use a separate application-level authentication mechanism.

Recommended model:

## Device identity

When the desktop app is installed:

``` text
Generate device keypair
        ↓
Store private key locally
        ↓
Register public identity
```

The private key should never leave the laptop.

## Browser authentication

Possible MVP:

``` text
User logs into web app
        ↓
Selects their device
        ↓
Application creates short-lived access token
        ↓
Browser connects to laptop
        ↓
Laptop validates token
```

More advanced:

Use public-key challenge/response:

``` text
Browser
   │
   │ challenge
   ▼
Laptop
   │
   │ signed response
   ▼
Browser
```

This removes the need for the laptop to trust the signaling server as an
authentication authority.

------------------------------------------------------------------------

# 12. Pairing

For the first installation, avoid asking users to manually enter WebRTC
details.

Use a pairing flow such as:

``` text
Desktop App

Your pairing code:

  A7K4-P2M9

Open:
  files.diewax.dev/pair

or scan QR code
```

Browser:

``` text
Enter pairing code
        ↓
Authenticate
        ↓
Approve "My Laptop"
        ↓
Device paired
```

After pairing, store the device relationship.

The pairing code should:

-   Be short-lived
-   Be single-use
-   Have sufficient entropy
-   Not contain sensitive information
-   Be invalidated after successful pairing

------------------------------------------------------------------------

# 13. Device State

The application should have explicit device states:

``` text
OFFLINE
CONNECTING
ONLINE
DEGRADED
DISCONNECTED
```

Connection status can additionally expose:

``` text
DIRECT
RELAYED
UNKNOWN
```

Example UI:

``` text
My Laptop
● Online
Direct connection

Documents
Downloads
Videos
```

If TURN is being used:

``` text
My Laptop
● Online
Relay connection
```

Do not expose technical ICE/STUN/TURN terminology to normal users.

------------------------------------------------------------------------

# 14. File Index

Do not scan the entire filesystem on every browser request.

Maintain a local index.

Possible implementation:

``` text
SQLite
```

Example schema:

``` text
files
-----
id
parent_id
name
path
type
size
modified_at
inode/file_id
```

The agent can:

-   Scan configured directories
-   Detect changes
-   Update metadata
-   Serve directory listings immediately

For MVP, a simple on-demand directory read may be sufficient.

Recommended progression:

### MVP

``` text
LIST → read directory directly
```

### Later

``` text
Filesystem watcher
       ↓
SQLite index
       ↓
Fast browsing
```

------------------------------------------------------------------------

# 15. Security Model

The security model should assume the public Internet is hostile.

## Never expose

-   Arbitrary filesystem paths
-   Shell commands
-   OS APIs
-   Arbitrary TCP forwarding
-   Arbitrary localhost access

## Only expose explicit RPC operations

``` text
LIST
STAT
DOWNLOAD
```

Later:

``` text
UPLOAD
DELETE
MOVE
RENAME
```

Uploads and destructive operations should not be part of the first
release.

------------------------------------------------------------------------

# 16. Path Security

Never trust a browser-supplied path.

Reject:

``` text
../../etc/passwd
```

and equivalent traversal attempts.

Resolve paths against an allowed root and verify that the final resolved
path remains within that root.

Conceptually:

``` text
requested path
      ↓
clean
      ↓
resolve absolute path
      ↓
verify allowed root
      ↓
open file
```

This check must happen on every filesystem operation.

------------------------------------------------------------------------

# 17. Encryption

WebRTC provides encrypted transport through DTLS.

However, do not treat transport encryption as the complete application
security model.

You still need:

-   Authentication
-   Authorization
-   Device identity
-   Token expiration
-   Pairing expiration
-   Access control
-   Path validation

Optional later enhancement:

Add application-level encryption for particularly sensitive deployments.

------------------------------------------------------------------------

# 18. Desktop Application UX

The desktop app should feel like:

``` text
Diewax Files

Status
● Online

Connected account
user@example.com

Shared folders

☑ Documents
☑ Downloads
☐ Pictures
☑ Videos

[ Add folder ]

Network
Direct connection

Security
2 paired devices

[ Revoke all devices ]

Settings
[ Start on system startup ]
[ Minimize to tray ]
```

The user should never see:

``` text
Peer ID
ICE candidate
SDP
STUN
TURN
WebSocket
NAT
Port
Tunnel
```

unless viewing an advanced diagnostics screen.

------------------------------------------------------------------------

# 19. Distribution

Target platforms in stages.

## Phase 1

-   macOS
-   Windows
-   Linux

## Phase 2

-   Android
-   iOS

The browser can remain the primary client.

Desktop application installation should:

1.  Install the agent.
2.  Create device identity.
3.  Start the background process.
4.  Open the pairing page.
5.  Ask for folders.
6.  Pair the device.
7.  Start automatically.

No terminal commands should be required.

------------------------------------------------------------------------

# 20. Repository Structure

Recommended monorepo:

``` text
diewax-files/
│
├── apps/
│   ├── web/
│   │   ├── src/
│   │   └── ...
│   │
│   └── desktop/
│       ├── cmd/
│       ├── internal/
│       └── ...
│
├── packages/
│   ├── protocol/
│   └── web-rtc/
│
├── services/
│   └── signaling/
│
├── docs/
│   ├── architecture.md
│   ├── protocol.md
│   └── security.md
│
└── README.md
```

Potential implementation:

``` text
web/
  TypeScript
  React/Next.js
  PeerJS

desktop/
  Go
  Pion WebRTC

signaling/
  PeerServer
```

------------------------------------------------------------------------

# 21. Development Phases

## Phase 0 --- Prototype WebRTC

Goal:

``` text
Browser A ═════ Browser B
             file
```

Use:

-   FilePizza code as reference
-   PeerJS
-   Public STUN

Success criteria:

-   Connect two browsers
-   Transfer a small file
-   Transfer a multi-GB file
-   Show progress
-   Cancel transfer

------------------------------------------------------------------------

## Phase 1 --- Browser → Go Agent

Replace Browser B with the desktop agent.

``` text
Browser
   │
   │ WebRTC
   ▼
Go Agent
```

Implement:

-   Peer registration
-   DataChannel
-   Authentication handshake
-   RPC protocol
-   LIST
-   STAT
-   DOWNLOAD

Success criteria:

``` text
Browser → connect → laptop → browse files → download
```

------------------------------------------------------------------------

## Phase 2 --- Pairing

Implement:

-   Device identity
-   Pairing codes
-   QR pairing
-   Device revocation
-   Short-lived tokens

Success criteria:

> A non-technical user can install the desktop app and pair it without
> reading documentation.

------------------------------------------------------------------------

## Phase 3 --- Production Networking

Implement:

-   STUN configuration
-   TURN fallback
-   ICE retries
-   Reconnection
-   Connection health
-   Direct/relay detection

Test:

-   Home Wi-Fi
-   Mobile hotspot
-   CGNAT
-   Corporate network
-   IPv4
-   IPv6
-   Symmetric NAT
-   Restricted firewall

------------------------------------------------------------------------

## Phase 4 --- Production File Browser

Implement:

-   Folder navigation
-   File search
-   Sorting
-   File metadata
-   Download queue
-   Progress
-   Cancellation
-   Multiple simultaneous transfers

------------------------------------------------------------------------

## Phase 5 --- Desktop UX

Implement:

-   Installer
-   Tray application
-   Autostart
-   Folder selection
-   Account/device management
-   Diagnostics
-   Automatic updates

------------------------------------------------------------------------

## Phase 6 --- Reliability

Test:

-   Laptop sleeps
-   Laptop wakes
-   Internet changes
-   Wi-Fi → mobile hotspot
-   IP changes
-   Browser refresh
-   Browser closes
-   Agent restarts
-   Signaling service unavailable
-   TURN unavailable
-   Large files
-   Many simultaneous downloads

------------------------------------------------------------------------

# 22. MVP Definition

The first useful release should support exactly this:

``` text
1. Install desktop application
2. Select one or more folders
3. Pair device
4. Open files.diewax.dev
5. Authenticate
6. See folders
7. Browse files
8. Download files
9. Transfer directly over WebRTC
10. Fall back to TURN when necessary
```

Do NOT initially implement:

-   File upload
-   File deletion
-   File editing
-   File sharing with arbitrary third parties
-   Public links
-   Multi-user ACLs
-   Mobile desktop-agent support
-   File synchronization
-   Version history
-   Cloud storage

Keep the first version read-only.

------------------------------------------------------------------------

# 23. Important Architectural Decision

There are two possible product directions.

## Option A --- Build around FilePizza

``` text
FilePizza
   +
custom desktop peer
   +
custom UI
```

Pros:

-   Closest to existing functionality
-   Less browser transfer code
-   Proven architecture
-   BSD license

Cons:

-   FilePizza's architecture is optimized around browser-to-browser
    transfers
-   More work to adapt it into a persistent personal file server

## Option B --- Use FilePizza as reference and build the protocol around PeerJS

``` text
Browser
   │
PeerJS
   │
WebRTC
   │
Pion
   │
Go Agent
   │
Filesystem
```

Pros:

-   Cleaner architecture
-   Desktop agent becomes a first-class peer
-   Easier to control authentication and filesystem access
-   Easier to evolve into a personal cloud drive

Cons:

-   More custom code
-   Need to implement transfer protocol

### Recommendation

Choose **Option B**.

Reuse:

-   PeerJS
-   PeerServer
-   Pion WebRTC
-   FilePizza's transfer techniques
-   LocalSend's UX/security concepts

Do not attempt to bundle an entire existing application if the existing
application's abstraction does not match the product.

------------------------------------------------------------------------

# 24. Infrastructure Cost Model

Normal file traffic should be:

``` text
Laptop ═══════════════════ Browser
```

Therefore infrastructure bandwidth is approximately:

``` text
signaling traffic
+
authentication traffic
+
optional TURN traffic
```

rather than:

``` text
total file download traffic
```

This is the main economic advantage of the architecture.

Worst case:

``` text
Laptop ───── TURN ───── Browser
```

In that situation the TURN provider carries the file traffic.

Therefore the application should collect anonymous operational metrics
such as:

``` text
direct_connection_success_rate
turn_fallback_rate
average_transfer_speed
transfer_failure_rate
connection_time
```

Avoid collecting file names/content unless explicitly required.

------------------------------------------------------------------------

# 25. Failure Handling

The application should automatically handle:

``` text
Signaling unavailable
        ↓
Retry


Direct WebRTC failed
        ↓
Try TURN


Laptop temporarily offline
        ↓
Show offline


Laptop network changed
        ↓
Reconnect


Browser refreshed
        ↓
Re-authenticate / reconnect


Agent restarted
        ↓
Re-register peer
```

The user should generally see:

``` text
Connecting...
```

rather than a WebRTC error.

------------------------------------------------------------------------

# 26. Diagnostics Mode

Provide an optional diagnostics screen for development/support.

Example:

``` text
Device
Online

Signaling
Connected

WebRTC
Connected

Connection
Direct

ICE
Connected

STUN
Working

TURN
Not used

RTT
42 ms

Download
86 MB/s
```

This makes network problems much easier to diagnose without exposing
technical details to ordinary users.

------------------------------------------------------------------------

# 27. Testing Matrix

At minimum test:

  Environment                       Expected
  --------------------------------- ----------------
  Same LAN                          Direct
  Home IPv4 NAT                     Direct
  IPv6                              Direct
  Mobile hotspot                    Direct or TURN
  CGNAT                             Direct or TURN
  Symmetric NAT                     TURN likely
  Corporate firewall                TURN likely
  IPv4-only laptop + IPv6 browser   ICE handles
  Laptop sleeps                     Reconnect
  IP changes                        Reconnect
  Agent restarts                    Reconnect
  Browser refreshes                 Reconnect
  1 GB file                         Complete
  10 GB file                        Complete
  Multiple files                    Complete
  Multiple simultaneous downloads   Stable

------------------------------------------------------------------------

# 28. Security Testing

Test specifically for:

-   Path traversal
-   Unauthorized peer connections
-   Expired tokens
-   Replayed pairing codes
-   Revoked devices
-   Malformed RPC messages
-   Oversized messages
-   Resource exhaustion
-   Too many concurrent downloads
-   Symlink escapes
-   Junction/mount escapes
-   Files disappearing during transfer
-   Permission changes while running

The desktop agent should fail closed.

------------------------------------------------------------------------

# 29. Open-Source Compliance

Track every bundled dependency.

Initial dependency list:

  Project       Role                                  License
  ------------- ------------------------------------- --------------
  FilePizza     Reference / transferable components   BSD-3-Clause
  PeerJS        Browser WebRTC abstraction            MIT
  PeerServer    Signaling                             MIT
  Pion WebRTC   Go WebRTC implementation              MIT
  coturn        TURN server if self-hosted            MIT
  LocalSend     UX/protocol reference                 Apache-2.0

Before distributing binaries:

-   Preserve required copyright notices.
-   Preserve license texts.
-   Review transitive dependencies.
-   Generate a third-party notices file.
-   Do not assume that "open source" means unrestricted commercial
    redistribution.

------------------------------------------------------------------------

# 30. Suggested First Milestone

Build this exact vertical slice before doing anything else:

``` text
                  Internet
                     │
              ┌──────▼──────┐
              │ PeerServer  │
              └──────┬──────┘
                     │
              signaling only
                     │
             ┌───────┴────────┐
             │                │
        Browser           Go Agent
             │                │
             │  WebRTC P2P    │
             └════════════════┘
                              │
                              ▼
                         ~/TestFiles
```

The browser should show:

``` text
TestFiles/
├── hello.txt
├── image.jpg
└── large.iso
```

and successfully download `large.iso` directly from the laptop.

Only after this works reliably should you build:

-   Authentication
-   Pairing
-   File indexing
-   Polished UI
-   Installers
-   TURN optimization

------------------------------------------------------------------------

# 31. Final Target Architecture

``` text
                         ┌───────────────────────────┐
                         │     files.diewax.dev      │
                         │                           │
                         │ Static Web Application    │
                         └─────────────┬─────────────┘
                                       │
                                       │ HTTPS
                                       ▼
                                ┌─────────────┐
                                │   Browser   │
                                └──────┬──────┘
                                       │
                         ┌─────────────┴─────────────┐
                         │                           │
                         │      WebRTC / P2P         │
                         │                           │
                         └─────────────┬─────────────┘
                                       │
                              ═════════╪═════════
                                       │
                                ┌──────▼──────┐
                                │ Diewax Agent│
                                │             │
                                │ Go + Pion   │
                                │             │
                                │ Auth        │
                                │ RPC         │
                                │ Filesystem  │
                                └──────┬──────┘
                                       │
                              ┌────────▼────────┐
                              │ User-selected   │
                              │ folders         │
                              └─────────────────┘


Supporting infrastructure:

       ┌──────────────────┐
       │ PeerServer       │
       │ Signaling only   │
       └──────────────────┘

       ┌──────────────────┐
       │ STUN             │
       │ NAT discovery    │
       └──────────────────┘

       ┌──────────────────┐
       │ TURN / coturn    │
       │ Fallback only    │
       └──────────────────┘
```

## Core design rule

> **Your infrastructure should coordinate the connection, not carry the
> files.**

The desktop agent owns the filesystem. The browser owns the UI. WebRTC
owns the data path. PeerServer owns signaling. STUN/TURN handle network
traversal.

That separation keeps the system inexpensive, private, and simple for
the end user while allowing the implementation to reuse mature
open-source components instead of reinventing the networking stack.
