#!/usr/bin/env bash
# Both machines run the same line. No addresses to carry, no counting to three.
#
#   scripts/punchpair.sh <room> [bare-seconds]
#
# <room> is any shared word, the same on both machines. The rendezvous
# pairs whoever joins it and both sides start within a second of each
# other, which is the difference this test could never control by hand.
#
# [bare-seconds] is how long to punch with plain packets before QUIC.
# That number is the finding: 15 crosses, 0 does not. Walk it down to
# learn where the path stops opening.
#
#   scripts/punchpair.sh lab        15 seconds of bare packets, then QUIC
#   scripts/punchpair.sh lab 5      five
#   scripts/punchpair.sh lab 0      none: QUIC first, as the agent does
#
# Set PUNCH_ROLE=listen on one machine and leave the other at its default.
set -u
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

[ $# -ge 1 ] || { sed -n '2,15p' "$0"; exit 2; }
room=$1
bare=${2:-15}

# QUIC still needs one listener and one dialler. The punch is symmetric,
# so it does not matter which machine takes which — only that they differ.
role=${PUNCH_ROLE:-dial}

# One published port is wrong on a carrier that gives the next port to
# the next destination, and the real one is a step or two away. Setting
# this writes to that many ports above the published one, which opens a
# filter entry for each and lets the peer's real port answer.
export RATATOSKR_PUNCH_SPREAD=${RATATOSKR_PUNCH_SPREAD:-0}

RATATOSKR=${RATATOSKR:-./dist/ratatoskr}
export RATATOSKR_PUNCH_ROOM=$room
export RATATOSKR_PUNCH_RAW=$bare
export RATATOSKR_PUNCH_SECONDS=${RATATOSKR_PUNCH_SECONDS:-60s}

echo "room $room, $bare seconds of bare packets, role $role"
exec "$RATATOSKR" punch-quic "$role"
