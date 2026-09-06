#!/usr/bin/env bash
# Run the punch that works and the punch that does not, at the same
# moment, on one wire.
#
#   sudo scripts/sidebyside.sh [seconds]
#
# Everything measurable about the two machines now agrees that libp2p's
# punch should land: right addresses, right ports, right socket, right
# packet sizes, endpoint-independent mapping on both NATs proven against
# a third host. It does not land, and a bare punch across the same two
# carriers lands in under a second.
#
# The two have never been on the wire together. This puts them there:
# libp2p serving, punchtest punching, one capture over both. Whatever
# separates them is a difference in this output and nowhere else.
set -u
secs=${1:-150}
here=$(cd "$(dirname "$0")/.." && pwd)
me=${SUDO_USER:-$(id -un)}
scratch=$(mktemp -d)
pcap=$scratch/both.pcap
relay=${RELAY:-103.181.143.222}
witness=${WITNESS:-$relay:9500}

run() { sudo -u "$me" "$@"; }

cleanup() { pkill -f "ratatoskr run" 2>/dev/null; pkill -f "ratatoskr punchtest" 2>/dev/null; }
trap cleanup EXIT

cd "$here"
rm -f nattest-serve.log

echo "starting libp2p..."
run ./scripts/nattest.sh serve > /dev/null 2>&1 &
until grep -q "relay reservation: yes" nattest-serve.log 2>/dev/null; do sleep 3; done
lib=$(grep -o "/ip4/[0-9.]*/udp/[0-9]*/quic-v1" nattest-serve.log | grep -v 127.0.0.1 | tail -1)
echo "  libp2p publishes $lib"

mkfifo "$scratch/in"
sleep "$((secs + 60))" > "$scratch/in" &
echo "starting punchtest..."
run env RATATOSKR_PUNCH_WITNESS="$witness" RATATOSKR_PUNCH_SECONDS="${secs}s" \
	./dist/ratatoskr punchtest < "$scratch/in" > "$scratch/punch.log" 2>&1 &
until grep -q "PUNCH ADDRESS" "$scratch/punch.log" 2>/dev/null; do sleep 1; done
mine=$(grep -o "[0-9.]*:[0-9]*" "$scratch/punch.log" | head -1)

echo
echo "==================================================================="
echo "  On the other machine, run these two, in this order:"
echo
echo "    RATATOSKR_PUNCH_WITNESS=$witness RATATOSKR_PUNCH_SECONDS=${secs}s \\"
echo "      ./rata-new.exe punchtest        # paste $mine"
echo
echo "    RATATOSKR=./rata-new.exe ./nattest.sh dial \\"
echo "      $(run ./dist/ratatoskr id --full | head -1 | awk '{print $3}')"
echo "==================================================================="
echo
echo "waiting for the far side to report to the witness..."

peer=""
for _ in $(seq 1 60); do
	peer=$(ssh -o BatchMode=yes -o ConnectTimeout=5 "$me@$relay" \
		'tail -40 ~/r9500.log | grep "probe from" | grep " 7 bytes" | tail -1' 2>/dev/null |
		grep -o "[0-9.]*:[0-9]*" | head -1)
	[ -n "$peer" ] && break
	sleep 2
done
[ -n "$peer" ] || { echo "the far side never reached the witness; is it running?"; exit 1; }
echo "  far side punchtest is $peer"
echo "$peer" > "$scratch/in"

ip=${peer%%:*}
echo "capturing udp with $ip for ${secs}s"
tcpdump -n -i any -w "$pcap" -G "$secs" -W 1 "udp and host $ip" 2>/dev/null

libport=$(echo "$lib" | awk -F/ '{print $5}')
punchport=${mine##*:}

echo
echo "=== arriving from $ip, by our local port ==="
tcpdump -n -r "$pcap" "src host $ip" 2>/dev/null | awk '{print $5}' |
	awk -F. '{print $NF}' | sort | uniq -c | sort -rn
echo
echo "libp2p's port is $libport, punchtest's is $punchport."
echo "A count beside one and nothing beside the other is the whole answer:"
echo "the path is open, and only one of the two sockets is being reached."
