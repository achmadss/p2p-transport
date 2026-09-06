#!/usr/bin/env bash
# Does this machine reach the other one directly, or only through the relay?
#
#   scripts/nattest.sh nat            what kind of NAT is this network
#   scripts/nattest.sh serve          on the machine that will be dialled
#   scripts/nattest.sh dial <id>      on the other machine
#
# serve prints an id to carry over by hand. dial connects through the
# relay on purpose, moves 1 MB, and then reports whether DCUtR turned
# that relayed connection into a direct one.
#
#   "upgraded to direct"   pass. The punch landed and no file byte
#                          needs the relay again.
#   "still relayed"        fail. Send both logs.
#
# The relay is used for signalling and for the first connection only.
# The line that decides the test is the one about the upgrade.
set -u

# git-bash rewrites anything shaped like a unix path, multiaddrs
# included, into C:/Program Files/Git/... before the program sees it.
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

RELAY=${RELAY:-/ip4/103.181.143.222/udp/4001/quic-v1/p2p/12D3KooWLD13rmEfjNt8DJd3c86x93LHUGyQLizjvoXZsS3cPk5w}
export RATATOSKR_RELAYS=$RELAY
export RATATOSKR_DIAG=1
export RATATOSKR_PUNCH_WINDOW=${RATATOSKR_PUNCH_WINDOW:-60s}

# libp2p's own account of the punch. Our diag says what we advertised;
# these four subsystems say what was offered, what was dialled, and how
# it failed, which is the difference between a closed network and a
# wrong configuration. Everything else stays at error.
export GOLOG_LOG_LEVEL=${GOLOG_LOG_LEVEL:-error,p2p-holepunch=debug,autorelay=debug,autonat=debug,net/identify=debug}

BIN=${RATATOSKR:-}
if [ -z "$BIN" ]; then
	for c in ./dist/ratatoskr ./ratatoskr "$HOME/Downloads/ratatoskr.exe" "$HOME/ratatoskr.exe"; do
		[ -x "$c" ] && BIN=$c && break
	done
fi
[ -n "$BIN" ] || { echo "no ratatoskr binary found; set RATATOSKR=/path/to/it" >&2; exit 1; }

mode=${1:-help}
log=${LOG:-nattest-$mode.log}

case "$mode" in
nat)
	"$BIN" natcheck 2>&1 | tee "$log"
	;;
serve)
	echo "MY ID: $("$BIN" id --full)"
	echo
	echo "1. send that id to the other machine"
	echo "2. leave this window running until the other side finishes"
	echo "   log: $log"
	echo
	"$BIN" run 2>&1 | tee "$log"
	;;
dial)
	[ $# -ge 2 ] || { echo "usage: $0 dial <id>" >&2; exit 2; }
	"$BIN" bench "$2" --via relay --mb 1 2>&1 | tee "$log"
	echo
	echo "--- what libp2p offered and dialled ---"
	grep -E "initiating hole punch|received hole punch|hole punch attempt|attempting direct dial|no public address" "$log" | tail -12
	echo
	echo "send $log and the other machine's nattest-serve.log"
	;;
*)
	sed -n '2,20p' "$0"
	exit 2
	;;
esac
