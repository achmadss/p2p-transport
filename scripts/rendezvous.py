#!/usr/bin/env python3
"""Pair two punching machines without an operator on each end.

Every punch test so far needed two people, or one person and an agent,
carrying addresses between machines and pressing enter together. That is
slow, and worse, it is a variable: a run that fails because the two sides
started ninety seconds apart looks exactly like a run that fails because
the path is shut.

This is the smallest thing that removes both. Each side sends a room name
once a second; the server answers with the sender's own public address
and, once a second machine has joined the same room, that machine's
address. Both sides learn it within one poll of each other, so they start
together by construction.

    python3 rendezvous.py [port]        default 9600
"""
import socket
import sys
import time

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 9600
STALE = 120  # a room member that has not been heard from in two minutes is gone


def main():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("0.0.0.0", PORT))
    print(f"rendezvous on udp {PORT}", flush=True)

    rooms = {}  # name -> {addr: last seen}
    while True:
        data, addr = s.recvfrom(512)
        room = data.decode("utf-8", "replace").strip()[:64]
        if not room:
            continue
        now = time.time()
        members = rooms.setdefault(room, {})
        members[addr] = now
        for old, seen in list(members.items()):
            if now - seen > STALE:
                del members[old]

        peer = next((a for a in members if a != addr), None)
        mine = f"{addr[0]}:{addr[1]}"
        theirs = f"{peer[0]}:{peer[1]}" if peer else "-"
        s.sendto(f"{mine} {theirs}".encode(), addr)
        print(f"{room}: {mine} <- peer {theirs}", flush=True)


if __name__ == "__main__":
    main()
