#!/usr/bin/env python3
"""Pair two punching machines without an operator on each end.

Every punch test so far needed two people, or one person and an agent,
carrying addresses between machines and pressing enter together. That is
slow, and worse, it is a variable: a run that fails because the two sides
started ninety seconds apart looks exactly like a run that fails because
the path is shut.

This is the smallest thing that removes both. Each side sends a room name
and its own id once a second; the server answers with the sender's own
public address and, once a *different* machine has joined the same room,
that machine's address. Both sides learn it within one poll of each
other, so they start together by construction.

The id is what makes the answer trustworthy. Keying a room by source
address alone looks right and is not: every rerun gets a fresh source
port, so the previous run's entry is a different key and the server hands
a machine its own dead address back. It pairs instantly, punches at a
mapping that closed minutes ago, and reports that nothing arrived — the
exact failure under investigation. Two minutes of grace made that ghost
outlive most of a test session; ten seconds is longer than a poll and
shorter than a rerun.

    python3 rendezvous.py [port]        default 9600
    python3 rendezvous.py --selftest
"""
import socket
import sys
import time

PORT = 9600
STALE = 10  # a room member unheard from for ten seconds is gone


def join(rooms, room, who, addr, now):
    """Record a member and return the address of another one, or None."""
    members = rooms.setdefault(room, {})
    members[who] = (addr, now)
    for old, (_, seen) in list(members.items()):
        if now - seen > STALE:
            del members[old]
    fresh = [(seen, a) for k, (a, seen) in members.items() if k != who]
    return max(fresh)[1] if fresh else None


def main(port):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("0.0.0.0", port))
    print(f"rendezvous on udp {port}", flush=True)

    rooms = {}  # name -> {id: (addr, last seen)}
    while True:
        data, addr = s.recvfrom(512)
        parts = data.decode("utf-8", "replace").split()
        if not parts:
            continue
        room = parts[0][:64]
        # An older client sends the room alone. Its address is the only
        # id it has, which is the behaviour this replaced, so say so
        # rather than pairing it with a ghost and calling that a result.
        who = parts[1][:64] if len(parts) > 1 else f"legacy-{addr[0]}:{addr[1]}"

        peer = join(rooms, room, who, addr, time.time())
        mine = f"{addr[0]}:{addr[1]}"
        theirs = f"{peer[0]}:{peer[1]}" if peer else "-"
        s.sendto(f"{mine} {theirs}".encode(), addr)
        print(f"{room}: {mine} ({who}) <- peer {theirs}", flush=True)


def selftest():
    rooms, t = {}, 1000.0
    assert join(rooms, "lab", "mac", ("1.1.1.1", 100), t) is None
    # The same machine on a new port is still the same machine.
    assert join(rooms, "lab", "mac", ("1.1.1.1", 200), t + 1) is None
    assert join(rooms, "lab", "win", ("2.2.2.2", 300), t + 2) == ("1.1.1.1", 200)
    assert join(rooms, "lab", "mac", ("1.1.1.1", 200), t + 3) == ("2.2.2.2", 300)
    # A machine that stopped polling stops being a peer.
    assert join(rooms, "lab", "mac", ("1.1.1.1", 200), t + 30) is None
    assert join(rooms, "other", "win", ("2.2.2.2", 300), t + 30) is None
    print("selftest ok")


if __name__ == "__main__":
    if "--selftest" in sys.argv:
        selftest()
    else:
        main(int(sys.argv[1]) if len(sys.argv) > 1 else PORT)
