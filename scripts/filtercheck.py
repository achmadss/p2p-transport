#!/usr/bin/env python3
"""Which packets is a NAT willing to let back in?

usage: filtercheck.py reflect <port>        on a host with a public ip
       filtercheck.py probe <ip:port> [s]   behind the NAT under test

natcheck answers what external port this NAT gives a socket. It says
nothing about who may then use it, and that is the other half of RFC
4787. Three filtering behaviours exist, and only the probe can tell them
apart:

  endpoint-independent   once you have sent anywhere, anyone may reply.
  address-dependent      only hosts you have sent to may reply, from
                         any of their ports.
  address-and-port-dep.  only the exact ip:port you sent to may reply.

The reflector answers one probe three ways: from the port that was
written to, from a second port on the same host, and from the second
port again after a delay. What comes back names the behaviour.

A hole punch survives all three, so a probe that gets nothing back at
all is the interesting result: it means this NAT is dropping inbound UDP
on a mapping it just created, and no amount of coordination opens it.
"""
import os, socket, sys, time, select

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from punch import ask, SERVERS  # one reflector probe, shared with punch.py


def reflect(port):
    a = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    a.bind(("", port))
    b = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    b.bind(("", port + 1))
    print("reflecting on %d and %d" % (port, port + 1), flush=True)
    while True:
        data, addr = a.recvfrom(2048)
        print("probe from %s:%d" % addr, flush=True)
        a.sendto(b"SAME-PORT", addr)          # the port they wrote to
        b.sendto(b"OTHER-PORT", addr)         # a second port, same host
        time.sleep(1.0)
        b.sendto(b"OTHER-PORT-LATE", addr)    # again, after the mapping settles


def probe(host, port, seconds):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("", 0))
    dest = (socket.gethostbyname(host), port)
    print("probing %s:%d from local port %d" % (dest[0], dest[1], s.getsockname()[1]))

    # Ask a public reflector where this socket appears to be, on this
    # socket, before writing to the host under test. The reflector's
    # answer is what a peer would be told to dial; the reflect log on the
    # other side records what this socket really looks like from a third
    # address. Endpoint-independent mapping means the two agree, and if
    # they do not, every published address is a lie and no punch can land.
    mapped = None
    for name, sp in SERVERS:
        try:
            mapped = ask(s, (socket.gethostbyname(name), sp))
        except socket.gaierror:
            continue
        if mapped:
            print("  STUN says this socket is %s" % mapped)
            break
    if not mapped:
        print("  no reflector answered")

    s.sendto(b"hello", dest)

    got = {}
    end = time.time() + seconds
    while time.time() < end:
        if not select.select([s], [], [], end - time.time())[0]:
            break
        data, frm = s.recvfrom(2048)
        got[data.decode("ascii", "replace")] = frm
        print("  <- %-16s from %s:%d" % (data.decode("ascii", "replace"), frm[0], frm[1]))

    print()
    print("compare the STUN answer above with the \"probe from\" line in the")
    print("reflector's log. Two different ports mean the mapping is not")
    print("endpoint-independent after all, and every published address is wrong.")
    print()
    if "SAME-PORT" not in got:
        print("Nothing came back from the port we just wrote to.")
        print("This NAT is dropping the reply to a mapping it created one")
        print("moment ago. Hole punching cannot work while this is true, and")
        print("neither can anything else that needs an inbound packet.")
        return 1
    if "OTHER-PORT" in got or "OTHER-PORT-LATE" in got:
        print("Address-Dependent Filtering: any port on a host we have written")
        print("to may reply. Hole punching works comfortably here.")
    else:
        print("Address-and-Port-Dependent Filtering: only the exact ip:port we")
        print("wrote to may reply. Hole punching still works, but both sides")
        print("must aim at the address the other actually published.")
    return 0


def main():
    if len(sys.argv) < 3:
        print(__doc__.strip())
        return 2
    if sys.argv[1] == "reflect":
        reflect(int(sys.argv[2]))
        return 0
    if sys.argv[1] == "probe":
        host, port = sys.argv[2].rsplit(":", 1)
        return probe(host, int(port), int(sys.argv[3]) if len(sys.argv) > 3 else 8)
    print(__doc__.strip())
    return 2


if __name__ == "__main__":
    sys.exit(main())
