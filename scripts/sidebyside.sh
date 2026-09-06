#!/usr/bin/env bash
# Run the punch that works and the punch that does not, at the same
# moment, on one wire.
#
#   sudo scripts/sidebyside.sh [seconds]
#
# Everything measurable about the two machines agrees that libp2p's punch
# should land: right addresses, right ports, right socket, right packet
# sizes, endpoint-independent mapping on both NATs proven against a third
# host at the same instant the peer saw the same port. It does not land,
# and a bare punch across the same two carriers lands in 288 ms.
#
# The two have never been on the wire together. This puts them there:
# libp2p serving, punchtest punching, one capture over both, counted by
# which local port the arriving packets reached.
set -u

[ "$(id -u)" -eq 0 ] || { echo "needs the wire: sudo $0 ${1:-150}" >&2; exit 2; }
me=${SUDO_USER:-}
[ -n "$me" ] || { echo "run through sudo from your own account, not as root" >&2; exit 2; }

secs=${1:-150}
here=$(cd "$(dirname "$0")/.." && pwd)
relay=${RELAY:-103.181.143.222}
witness=${WITNESS:-$relay:9500}
scratch=$(mktemp -d)
pcap=$scratch/both.pcap

# Everything but tcpdump runs as the operator: the agent's identity and
# config live in their home directory, and so does the key this script
# reads the witness log with.
run() { sudo -u "$me" -- "$@"; }
addr() { grep -oE '[0-9]+(\.[0-9]+){3}:[0-9]+' "$@"; }

cleanup() { pkill -f 'ratatoskr run' 2>/dev/null; pkill -f 'ratatoskr punchtest' 2>/dev/null; }
trap cleanup EXIT

cd "$here"
rm -f nattest-serve.log

echo "starting libp2p..."
run ./scripts/nattest.sh serve > /dev/null 2>&1 &
until grep -q 'relay reservation: yes' nattest-serve.log 2>/dev/null; do sleep 3; done
# The local port is what the capture sees; the published one is what the
# far side dials. Both are needed, and the relay's own address sits in
# the same log and must not be mistaken for either.
libport=$(grep -o '/ip4/127.0.0.1/udp/[0-9]*/quic-v1' nattest-serve.log | tail -1 | awk -F/ '{print $5}')
pub=$(grep -oE '/ip4/[0-9.]+/udp/[0-9]+/quic-v1' nattest-serve.log |
	grep -vE "127\.0\.0\.1|192\.168\.|10\.|$relay" | tail -1)
echo "  libp2p listens on $libport and publishes ${pub:-nothing}"

mkfifo "$scratch/in"
sleep "$((secs + 120))" > "$scratch/in" &
echo "starting punchtest..."
run env RATATOSKR_PUNCH_WITNESS="$witness" RATATOSKR_PUNCH_SECONDS="${secs}s" \
	./dist/ratatoskr punchtest < "$scratch/in" > "$scratch/punch.log" 2>&1 &
until grep -q 'PUNCH ADDRESS' "$scratch/punch.log" 2>/dev/null; do sleep 1; done
mine=$(grep 'PUNCH ADDRESS' "$scratch/punch.log" | addr -)
myip=${mine%%:*}
id=$(run ./dist/ratatoskr id --full | awk '/peer id/ {print $3}')

cat <<TEXT

===================================================================
  On the other machine, run these two, in two windows:

  1)  RATATOSKR_PUNCH_WITNESS=$witness RATATOSKR_PUNCH_SECONDS=${secs}s \\
        ./rata-new.exe punchtest
      paste:  $mine

  2)  RATATOSKR=./rata-new.exe ./nattest.sh dial $id
===================================================================

waiting for the far side to reach the witness (up to 2 minutes)...
TEXT

# The far side names itself by writing to the witness on the very socket
# it punches with, so there is no address to carry by hand. Our own
# witness packets are in that same log and are excluded by address.
#
# Only lines written after this moment count. The log keeps every earlier
# run, and a previous punchtest's port sits in the last few lines looking
# exactly like a live one; aiming at it sends the whole test to a mapping
# that closed minutes ago and reports nothing arrived, which is the
# failure being investigated. Read the length first, then read past it.
mark=$(run ssh -o BatchMode=yes -o ConnectTimeout=5 "$me@$relay" \
	'wc -l < ~/r9500.log' 2>/dev/null | tr -d ' ')
: "${mark:=0}"
peer=""
for _ in $(seq 1 60); do
	peer=$(run ssh -o BatchMode=yes -o ConnectTimeout=5 "$me@$relay" \
		"tail -n +$((mark + 1)) ~/r9500.log | grep 'probe from' | grep ' 7 bytes'" 2>/dev/null |
		addr - | grep -v "^$myip:" | tail -1)
	[ -n "$peer" ] && break
	sleep 2
done
[ -n "$peer" ] || { echo "the far side never reached the witness; is its punchtest running?" >&2; exit 1; }
echo "  far side punchtest is $peer"

ip=${peer%%:*}
echo "capturing udp with $ip for ${secs}s"
tcpdump -n -i any -w "$pcap" "udp and host $ip" 2>/dev/null &
tcp=$!
sleep 2
echo "$peer" > "$scratch/in"
sleep "$secs"
kill "$tcp" 2>/dev/null
wait "$tcp" 2>/dev/null

port() { awk '{for(i=1;i<=NF;i++) if($i==">"){d=$(i+1); sub(/:$/,"",d); n=split(d,a,"."); print a[n]}}'; }
punchport=${mine##*:}

echo
echo "=== arriving from $ip, counted by the local port they reached ==="
tcpdump -n -r "$pcap" "src host $ip" 2>/dev/null | port | sort | uniq -c | sort -rn
echo
echo "=== leaving for $ip, counted by the port we sent to ==="
tcpdump -n -r "$pcap" "dst host $ip" 2>/dev/null | port | sort | uniq -c | sort -rn
echo
echo "libp2p's local port is $libport, punchtest's is $punchport."
echo "A count beside one and nothing beside the other is the answer: the"
echo "path is open in that same second, and one of the two sockets is not"
echo "being reached."
cp "$pcap" "$here/sidebyside.pcap" 2>/dev/null && chown "$me" "$here/sidebyside.pcap"
echo "capture kept at $here/sidebyside.pcap"
