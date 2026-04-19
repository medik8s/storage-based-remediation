#!/usr/bin/env bash
set -euo pipefail

# Extract Timestamp, Level, and Message from structured JSON log lines read from stdin.
# - Supports common timestamp fields: ts, timestamp, @timestamp, time
# - Supports common level fields: level, lvl, severity
# - Supports common message fields: message, msg
# - Non-JSON lines are ignored
# - Optional filters can be provided as key=value pairs to match structured fields (top-level keys)
#
# Usage examples:
#   cat app.log | scripts/extract-structured-logs.sh
#   kubectl logs -n ns pod | scripts/extract-structured-logs.sh
#   cat app.log | scripts/extract-structured-logs.sh controllerKind=StorageBasedRemediation controller="storagebasedremediationconfig-controller"

if ! command -v jq >/dev/null 2>&1; then
	echo "Error: jq is required on PATH" >&2
	exit 1
fi

# Build filters JSON array from key=value args (strings only; types parsed in jq)
filters_json="[]"
if [ "$#" -gt 0 ]; then
	items=()
	for kv in "$@"; do
		if [[ "$kv" != *"="* ]]; then
			echo "Warning: ignoring invalid filter '$kv' (expected key=value)" >&2
			continue
		fi
		key=${kv%%=*}
		val=${kv#*=}
		# Escape backslashes and double quotes for JSON safety
		key_esc=$(printf '%s' "$key" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
		val_esc=$(printf '%s' "$val" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
		items+=("{\"k\":\"$key_esc\",\"v\":\"$val_esc\"}")
	done
	if [ ${#items[@]} -gt 0 ]; then
		filters_json="[$(IFS=,; echo "${items[*]}")]"
	fi
fi

jq -Rr --argjson filters "$filters_json" '
  def parseval($s):
    if $s == "true" then true
    elif $s == "false" then false
    elif $s == "null" then null
    elif ($s | test("^-?[0-9]+(\\.[0-9]+)?$")) then ($s | tonumber)
    else $s end;

  fromjson?
  | select( ($filters|length) == 0 or ( [ $filters[] as $f | .[$f.k] == parseval($f.v) ] | all ) )
  | ( .ts // .timestamp // .["@timestamp"] // .time ) as $t
  | [ (if ($t|type) == "number" then ($t|todate) else $t end)
    , ( .level // .lvl // .severity // "" )
    , ( .message // .msg // "" ) ]
  | @tsv
' 