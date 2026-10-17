#!/usr/bin/env bash
# Stops only the processes launch.sh started and removes their scratch state.
# Evidence in $EVIDENCE is kept.
set -uo pipefail
source "$(dirname "$0")/env.sh"
[ -f "$RUN/pids" ] || { echo "nothing to clean in $RUN"; exit 0; }
while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$RUN/pids"
sleep 2
while read -r pid; do kill -9 "$pid" 2>/dev/null || true; done < "$RUN/pids"
rm -rf "$RUN"
echo "stopped; evidence kept in $EVIDENCE"
