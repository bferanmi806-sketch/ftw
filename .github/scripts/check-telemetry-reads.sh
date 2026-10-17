#!/usr/bin/env bash
# Fails when a change adds a direct read of cached telemetry outside
# go/internal/telemetry. A cached reading stays in the store after its
# driver goes stale, offline or faulted, so every direct reader has to
# remember to check health itself. Review found the forgotten check in
# #36, #85, #310, #647, #664 and #740. Existing reads are counted per file
# and may only go down.
#
#   bash .github/scripts/check-telemetry-reads.sh [base-ref]
#
# An exception goes on the offending line:
#   // telemetry-read-ok: <reason>; expires YYYY-MM-DD; approved-by <name>
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
BASE="${1:-${TELEMETRY_READS_BASE_SHA:-origin/master}}"
BASE="$(git merge-base "${BASE}" HEAD)"
PATTERN='\.(RawW|SmoothedW)\b|\.(ReadingsByType|ReadingsByDriver)\('

count() {
  git grep -n -E "${PATTERN}" "$1" -- 'go/*.go' ':!go/internal/telemetry/' ':!*_test.go' 2>/dev/null \
    | grep -v 'telemetry-read-ok:' \
    | sed -E "s/^$1://" | cut -d: -f1 | sort | uniq -c | awk '{print $2, $1}' || true
}

before="$(count "${BASE}")"
after="$(count HEAD)"
grew="$(join -a 2 -e 0 -o 0,1.2,2.2 <(echo "${before}" | sort) <(echo "${after}" | sort) | awk '$3 > $2')"

if [ -n "${grew}" ]; then
  echo "This change adds direct reads of cached telemetry:" >&2
  echo "${grew}" | awk '{printf "  %s: %s -> %s\n", $1, $2, $3}' >&2
  cat >&2 <<'MSG'
A cached reading survives its driver going stale, offline or faulted.
Read live values through a health-gated accessor in go/internal/telemetry
(Store.SumOnlineEVW and Store.SumOnlineV2XW are the existing ones), or gate
each reading on Store.DriverHealth(r.Driver).TelemetryLive() and add the
accessor to go/internal/telemetry so the next caller cannot forget it.
MSG
  exit 1
fi
total="$(echo "${after}" | awk '{s += $2} END {print s + 0}')"
echo "telemetry reads outside go/internal/telemetry: ${total}, none added"
