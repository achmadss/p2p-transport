#!/usr/bin/env bash
# Run the dial where a crash cannot take the terminal with it.
#
#   scripts/safedial.sh <peer id>
#
# Twice now the dial has taken every terminal window on the machine down
# with it and left no binary behind, which destroys the evidence and the
# tool in one go. So: a copy of the binary somewhere the run cannot
# delete, the run detached from this shell, and the output on disk from
# the first line. What survives is then readable whatever happens.
set -u
[ $# -ge 1 ] || { sed -n '2,12p' "$0"; exit 2; }

id=$1
out=${LOG:-safedial.log}

cp dist/ratatoskr /tmp/rata-safe || exit 1
chmod +x /tmp/rata-safe

# setsid is Linux and git-bash; macOS does not ship it. nohup alone is
# enough on every one of them: it detaches the run from this shell's
# hangup, which is the whole requirement.
detach=nohup
command -v setsid >/dev/null && detach="setsid nohup"

echo "dialling $id; output in $out"
RATATOSKR=/tmp/rata-safe RATATOSKR_DIAG=1 \
	$detach ./scripts/nattest.sh dial "$id" </dev/null >"$out" 2>&1 &

echo "started as pid $!. watch it with:  tail -f $out"
