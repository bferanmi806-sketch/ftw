#!/usr/bin/env bash
# Drives the operating-mode journey through the public API the web UI uses:
# POST /api/mode, then waits three control ticks and checks the result in
# GET /api/status. Usage: drive-mode.sh <mode> (default: idle).
set -euo pipefail
source "$(dirname "$0")/env.sh"
mode="${1:-idle}"
out="$EVIDENCE/mode-$mode-$(date +%Y%m%dT%H%M%S)"
mkdir -p "$out"
curl -fsS "$BASE/api/status" > "$out/before.json"
curl -fsS -X POST -H 'Content-Type: application/json' -d "{\"mode\":\"$mode\"}" "$BASE/api/mode" > "$out/response.json"
sleep 6
curl -fsS "$BASE/api/status" > "$out/after.json"
cp "$RUN/ftw.log" "$out/ftw.log"
python3 - "$out" "$mode" <<'P'
import json, sys
out, mode = sys.argv[1], sys.argv[2]
after = json.load(open(f"{out}/after.json"))
assert after["mode"] == mode, f"status reports mode {after['mode']!r}, wanted {mode!r}"
if mode == "idle":
    targets = {d["driver"]: d["target_w"] for d in after.get("dispatch") or []}
    assert targets and all(v == 0 for v in targets.values()), f"idle must hold every battery at 0 W, dispatch was {targets}"
    sent = {c["driver"]: c["sent_w"] for c in after["control_feedback"] if c["kind"] == "battery"}
    assert all(v == 0 for v in sent.values()), f"idle must send 0 W to every battery, sent {sent}"
print(f"mode {mode} confirmed in status and dispatch; evidence in {out}")
P
