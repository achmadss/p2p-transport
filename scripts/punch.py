#!/usr/bin/env python3
"""One UDP socket: STUN names it, then it punches at a peer.

usage: scripts/punch.py <packets> <ip:port> [bytes]

Sends one packet per second, <packets> times, and prints whatever comes
back from that peer. The socket that asked STUN is the socket that sends
and listens; an address measured on any other socket says nothing.

No libp2p. If bytes arrive, the carriers punch and the fault is ours.
"""
import os, socket, struct, sys, threading, time, select

COOKIE = 0x2112A442
SERVERS = [("stun.l.google.com", 19302), ("stun1.l.google.com", 19302),
           ("stun.cloudflare.com", 3478)]


def ask(sock, server, timeout=2.0):
    """One STUN binding request. Returns "ip:port" as the server saw us."""
    txid = os.urandom(12)
    sock.sendto(struct.pack("!HHI", 1, 0, COOKIE) + txid, server)
    end = time.time() + timeout
    while time.time() < end:
        if not select.select([sock], [], [], end - time.time())[0]:
            break
        data, _ = sock.recvfrom(2048)
        if len(data) < 20 or data[8:20] != txid:
            continue
        i, n = 20, struct.unpack("!H", data[2:4])[0]
        while i + 4 <= 20 + n:
            t, l = struct.unpack("!HH", data[i:i + 4])
            v = data[i + 4:i + 4 + l]
            if t == 0x0020 and len(v) >= 8:   # XOR-MAPPED-ADDRESS
                port = struct.unpack("!H", v[2:4])[0] ^ (COOKIE >> 16)
                ip = bytes(a ^ b for a, b in zip(v[4:8], struct.pack("!I", COOKIE)))
                return "%s:%d" % (socket.inet_ntoa(ip), port)
            if t == 0x0001 and len(v) >= 8:   # MAPPED-ADDRESS
                return "%s:%d" % (socket.inet_ntoa(v[4:8]),
                                  struct.unpack("!H", v[2:4])[0])
            i += 4 + l + (-l % 4)             # attributes are 4-byte padded
    return None


def main():
    if len(sys.argv) < 3:
        print(__doc__.strip())
        return 2
    count = int(sys.argv[1])
    host, port = sys.argv[2].rsplit(":", 1)
    peer = (socket.gethostbyname(host), int(port))
    size = int(sys.argv[3]) if len(sys.argv) > 3 else 15

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("", 0))

    mapped = reflector = None
    for name, p in SERVERS:
        try:
            srv = (socket.gethostbyname(name), p)
        except socket.gaierror:
            continue
        mapped = ask(sock, srv)
        if mapped:
            reflector = srv
            break
    if not mapped:
        print("no reflector answered, so this machine cannot name itself")
        return 1

    print("\n  MY PUNCH ADDRESS:  %s\n" % mapped)

    # A NAT forgets an idle UDP mapping in about a minute, and carrying an
    # address to the other machine by hand takes longer than that.
    stop = threading.Event()
    threading.Thread(target=lambda: [sock.sendto(b"\x00", reflector)
                                     for _ in iter(lambda: stop.wait(15), True)],
                     daemon=True).start()
    input("press enter together with the other side to fire at %s:%d ... "
          % peer)
    stop.set()

    now = ask(sock, reflector)
    if now and now != mapped:
        print("\n  WARNING: my address changed while waiting, %s -> %s" % (mapped, now))
        print("  the other side is aiming at the old one; start over.\n")

    payload = b"P" + bytes(size - 1)
    print("\nsending %d packets of %d bytes, one per second.\n" % (count, size))
    got = 0
    for _ in range(count):
        sock.sendto(payload, peer)
        end = time.time() + 1
        while time.time() < end:
            if not select.select([sock], [], [], end - time.time())[0]:
                break
            data, frm = sock.recvfrom(2048)
            if frm[0] != peer[0]:
                continue          # a reflector, not a punch
            got += 1
            if got <= 3:
                print("  RECEIVED %d bytes from %s:%d" % (len(data), frm[0], frm[1]))

    print("\n  %d of %d packets arrived.\n" % (got, count))
    print("The path punches." if got else
          "Nothing arrived: the far side never sent, or a NAT dropped it.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
