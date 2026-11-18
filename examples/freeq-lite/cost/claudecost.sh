#!/bin/bash
# usage: claudecost.sh <model>; prompt on stdin; appends total_cost_usd to $COSTFILE
out=$(claude -p --tools "" --strict-mcp-config --model "$1" --output-format json)
rc=$?
printf '%s' "$out" | jq -r '.total_cost_usd // 0' >> "$COSTFILE"
printf '%s' "$out" | jq -r '.result // empty'
exit $rc
