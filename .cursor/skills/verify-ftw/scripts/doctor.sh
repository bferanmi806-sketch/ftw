#!/usr/bin/env bash
# Read-only: is the instance this skill started up, ours and worth driving?
set -uo pipefail
source "$(dirname "$0")/env.sh"
fail=0
[ -f "$RUN/pids" ] || { echo "no instance started by this skill ($RUN/pids missing)"; exit 1; }
while read -r pid; do kill -0 "$pid" 2>/dev/null || { echo "process $pid is gone"; fail=1; }; done < "$RUN/pids"
owner=$(ss -ltnp 2>/dev/null | awk -v p=":${API_PORT}" '$4 ~ p"$" {print $NF}')
grep -q "pid=$(sed -n 3p "$RUN/pids")," <<<"$owner" || { echo "port ${API_PORT} is not owned by our ftw process: ${owner:-nobody}"; fail=1; }
curl -fsS "$BASE/api/status" | python3 -c "$(cat <<'P'
import json, sys
s = json.load(sys.stdin)
for name, d in sorted((s.get("drivers") or {}).items()):
    print("driver", name, (d or {}).get("status"))
print("grid_w", s.get("grid_w"), "mode", s.get("mode"))
P
)" || { echo "GET /api/status failed"; fail=1; }
exit $fail
