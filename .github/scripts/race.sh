#!/usr/bin/env bash
# Runs the Go race detector on the packages whose goroutines share state
# with the control loop. Same command locally and in CI:
#   bash .github/scripts/race.sh
# Races in #787 and #789 merged because no check ran -race; on their parent
# commits this command fails with WARNING: DATA RACE.
set -euo pipefail
cd "$(dirname "$0")/../../go"
exec go test -race -count=1 -timeout 900s \
  ./internal/control/... ./internal/loadpoint/... ./internal/telemetry/... \
  ./internal/drivers/... ./internal/api/... ./internal/state/... \
  ./internal/mpc/... ./internal/cars/... ./internal/appproto/... \
  ./internal/appuplink/... ./internal/ocpp/... ./internal/ha/... \
  ./internal/config/... ./internal/backup/... ./internal/notifications/... \
  ./cmd/ftw/...
