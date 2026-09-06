#!/usr/bin/env bash
# Watch a hole punch on the wire.
#
#   sudo scripts/punchwatch.sh <peer-ip> [seconds]
#
# Every log we have says the punch is aimed correctly and every measured
# property of both NATs says it should land. What no log can show is
# whether the packets physically arrive, so this reads the interface.
#
# Run it on the machine serving, start the dial on the other machine, and
# it reports what left and what came back.
set -u
ip=${1:?usage: sudo $0 <peer-ip> [seconds]}
secs=${2:-120}
out=${TMPDIR:-/tmp}/punchwatch.pcap

echo "capturing udp with $ip for ${secs}s -> $out"
echo "start the dial on the other machine now."
tcpdump -n -i any -w "$out" -G "$secs" -W 1 "udp and host $ip" 2>/dev/null

echo
echo "=== packets we sent ==="
tcpdump -n -r "$out" "src net 0.0.0.0/0 and dst host $ip" 2>/dev/null |
	awk '{print $3, "->", $5, $NF}' | sort | uniq -c | sort -rn | head
echo
echo "=== packets that came back ==="
tcpdump -n -r "$out" "src host $ip" 2>/dev/null |
	awk '{print $3, "->", $5, $NF}' | sort | uniq -c | sort -rn | head
echo
sent=$(tcpdump -n -r "$out" "dst host $ip" 2>/dev/null | wc -l)
back=$(tcpdump -n -r "$out" "src host $ip" 2>/dev/null | wc -l)
icmp=$(tcpdump -n -r "$out" icmp 2>/dev/null | wc -l)
printf "sent %s, received %s, icmp %s\n\n" "$sent" "$back" "$icmp"
if [ "$back" -eq 0 ]; then
	echo "Nothing came back. Our packets leave and the far side's never"
	echo "arrive, on a path that a bare punch crosses in under a second."
else
	echo "Packets did arrive. Compare their source port with the address"
	echo "libp2p published: if they differ, the punch was aimed at a port"
	echo "the far machine does not send from."
fi
