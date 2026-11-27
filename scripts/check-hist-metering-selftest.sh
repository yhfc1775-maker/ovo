#!/usr/bin/env bash
# check-hist-metering-selftest.sh — S05 frontend gate mutation selftest:
# mirror presets.ts to a temp repo, require baseline PASS, then require
# FAIL on (1) emptyForm default flipped to ssSubscriptionUsers true,
# (2) toPayload sending ssSubscriptionUsers ungated, (3) toPayload
# re-sending the dead meteringEnabled field.
#
# Usage: check-hist-metering-selftest.sh (no arguments, acts on this repo).
# Exit code: 0 = baseline PASS and all variants caught; 1 = otherwise.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-hist-metering.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

mkdir -p "$MIR/vite-frontend/src/pages/inbound"
cp "$ROOT/vite-frontend/src/pages/inbound/presets.ts" "$MIR/vite-frontend/src/pages/inbound/"

echo "== baseline: unmutated mirror must PASS =="
if bash "$GATE" "$MIR" > "$TMP/base.log" 2>&1; then
	echo "OK: baseline PASS"
else
	echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: emptyForm ssSubscriptionUsers false -> true must FAIL =="
python3 - "$MIR/vite-frontend/src/pages/inbound/presets.ts" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
start = s.index("export function emptyForm(")
end = s.index("\n}\n", start)
body = s[start:end]
assert "ssSubscriptionUsers: false," in body, "baseline shape changed"
body = body.replace("ssSubscriptionUsers: false,", "ssSubscriptionUsers: true,", 1)
open(p, "w").write(s[:start] + body + s[end:])
PYEOF
if bash "$GATE" "$MIR" > "$TMP/v1.log" 2>&1; then
	echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "must default ssSubscriptionUsers" "$TMP/v1.log" && echo "OK: variant 1 caught" || { echo "FAIL: variant 1 caught but not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
cp "$ROOT/vite-frontend/src/pages/inbound/presets.ts" "$MIR/vite-frontend/src/pages/inbound/"

echo "== variant 2: toPayload ungated ssSubscriptionUsers must FAIL =="
python3 - "$MIR/vite-frontend/src/pages/inbound/presets.ts" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
start = s.index("export function toPayload(")
end = s.index("\n}\n", start)
body = s[start:end]
old = """    ssSubscriptionUsers:
      form.protocol === "shadowsocks" &&
      (form.kernelType === "xray" || form.kernelType === "singbox")
        ? form.ssSubscriptionUsers
        : false,"""
assert old in body, "baseline gate shape changed"
body = body.replace(old, "    ssSubscriptionUsers: form.ssSubscriptionUsers,", 1)
open(p, "w").write(s[:start] + body + s[end:])
PYEOF
if bash "$GATE" "$MIR" > "$TMP/v2.log" 2>&1; then
	echo "FAIL: variant 2 was not caught"; exit 1
fi
grep -q "ungated" "$TMP/v2.log" && echo "OK: variant 2 caught" || { echo "FAIL: variant 2 caught but not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }
cp "$ROOT/vite-frontend/src/pages/inbound/presets.ts" "$MIR/vite-frontend/src/pages/inbound/"

echo "== variant 3: toPayload re-sending meteringEnabled must FAIL =="
python3 - "$MIR/vite-frontend/src/pages/inbound/presets.ts" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
start = s.index("export function toPayload(")
end = s.index("\n}\n", start)
body = s[start:end]
assert "    meteringEnabled: form.meteringEnabled,\n" not in body, "baseline already sends meteringEnabled"
assert "    enabled: form.enabled,\n" in body, "baseline shape changed"
body = body.replace("    enabled: form.enabled,\n", "    enabled: form.enabled,\n    meteringEnabled: form.meteringEnabled,\n", 1)
open(p, "w").write(s[:start] + body + s[end:])
PYEOF
if bash "$GATE" "$MIR" > "$TMP/v3.log" 2>&1; then
	echo "FAIL: variant 3 was not caught"; exit 1
fi
grep -q "must not send meteringEnabled" "$TMP/v3.log" && echo "OK: variant 3 caught" || { echo "FAIL: variant 3 caught but not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1; }

echo "OK: check-hist-metering gate + selftest green"
