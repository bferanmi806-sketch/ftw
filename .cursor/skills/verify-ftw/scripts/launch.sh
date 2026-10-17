#!/usr/bin/env bash
# Builds Core and the two simulators, starts them on isolated ports with an
# empty state directory, and waits until Core reports live site telemetry.
set -euo pipefail
source "$(dirname "$0")/env.sh"
if [ -f "$RUN/pids" ]; then echo "already running in $RUN; run cleanup.sh first" >&2; exit 1; fi
mkdir -p "$RUN/bin" "$RUN/data" "$EVIDENCE"
ls "$REPO"/drivers/ferroamp.lua "$REPO"/drivers/sungrow.lua >/dev/null 2>&1 || bash "$REPO/scripts/sync-bundled-drivers.sh"
(cd "$REPO/go" && go build -o "$RUN/bin/" ./cmd/ftw ./cmd/sim-ferroamp ./cmd/sim-sungrow)
sed -e "s/port: 1883/port: ${MQTT_PORT}/" -e "s/port: 5502/port: ${MODBUS_PORT}/" \
    -e "s/^  port: 8080/  port: ${API_PORT}/" -e "s/port: 8887/port: ${OCPP_PORT}/" \
    -e "s/port_v201: 8888/port_v201: ${OCPP201_PORT}/" -e "s#path: ../dev-data/state.db#path: ${RUN}/data/state.db#" \
    -e "s#lua: drivers/#lua: ${REPO}/drivers/#" \
    "$REPO/config.local.example.yaml" > "$RUN/config.yaml"
"$RUN/bin/sim-ferroamp" -addr ":${MQTT_PORT}" > "$RUN/sim-ferroamp.log" 2>&1 & echo $! >> "$RUN/pids"
"$RUN/bin/sim-sungrow" -addr "tcp://127.0.0.1:${MODBUS_PORT}" > "$RUN/sim-sungrow.log" 2>&1 & echo $! >> "$RUN/pids"
sleep 1
"$RUN/bin/ftw" -config "$RUN/config.yaml" -drivers "$REPO/drivers" -web "$REPO/web" > "$RUN/ftw.log" 2>&1 & echo $! >> "$RUN/pids"
for _ in $(seq 1 90); do
  if curl -fsS "$BASE/api/status" -o "$RUN/status.json" 2>/dev/null && python3 - "$RUN/status.json" <<'P'
import json, sys
s = json.load(open(sys.argv[1]))
drivers = s.get("drivers") or {}
ok = isinstance(drivers, dict) and any((d or {}).get("status") == "ok" for d in drivers.values())
sys.exit(0 if ok and s.get("grid_w") is not None else 1)
P
  then echo "ready: $BASE (state in $RUN, logs in $RUN/*.log)"; exit 0; fi
  sleep 1
done
echo "not ready after 90 s; see $RUN/ftw.log" >&2; exit 1
