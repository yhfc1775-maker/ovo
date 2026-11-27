#!/bin/bash
set -euo pipefail
repo=yhfc1775-maker/ovo
metadata=$(curl -fsSL "https://api.github.com/repos/$repo")
printf '%s' "$metadata" | python3 -c 'import json,sys; assert json.load(sys.stdin)["private"] is False'
for package in ovo-panel-backend ovo-vite-frontend ovo-agent; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "https://ghcr.io/token?service=ghcr.io&scope=repository:yhfc1775-maker/${package}:pull")
  if [[ "$code" != 200 ]]; then echo "FAIL: anonymous $package pull token HTTP $code" >&2; exit 1; fi
  echo "PASS: anonymous $package pull token HTTP 200"
done
