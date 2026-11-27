#!/usr/bin/env bash
# check-hist-metering.sh — S05 (AUDIT-HIST) frontend metering-default gate.
#
# S05 retired with the companion (in-place SS, 2026-11-22): the old
# companion-era switch (meteringEnabled, default-on) is dead — the
# backend accepts it but ignores it, and the form never sends it. The
# new invariant this gate pins on vite-frontend/src/pages/inbound/presets.ts:
#   1. emptyForm (the new-inbound form) defaults ssSubscriptionUsers to
#      false. In-place metering is explicit opt-in: enabling it cuts the
#      owner's bare shared links the moment a share subscription
#      attaches (V Q1), so it must never default on.
#   2. emptyForm defaults meteringEnabled to false. The field is a
#      read-only legacy bit (it only feeds the migration hint for a
#      legacy-metered origin); a default-on here would resurrect the
#      dead switch.
#   3. toPayload never sends meteringEnabled. Sending the form's value
#      served nothing (the backend ignores it) and kept the dead switch
#      looking alive.
#   4. toPayload gates ssSubscriptionUsers by kernel eligibility (SS
#      only, Xray / sing-box only); any other shape sends false so a
#      stale true never reaches the backend. (The nft / kernel-version
#      gate stays server-side and refuses the save with its own reason.)
# The backend half (create-omitted defaults OFF, explicit-on stores ON)
# is pinned by the Go test TestS05CreateDefaultsInplaceMeteringOff, not
# by this script.
#
# Usage: check-hist-metering.sh [REPO_ROOT] (default: script's repo root).
# Exit code: 0 = all assertions hold; 1 = drift; 2 = premise failure.
set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
FILE="$ROOT/vite-frontend/src/pages/inbound/presets.ts"
fail=0

[ -f "$FILE" ] || { echo "FAIL: 前提失效，$FILE 不存在"; exit 2; }

# Extract one top-level function body (from its export line to the next
# closing brace at column 0) so assertions cannot be satisfied by a
# same-named field in another function.
func_body() {
	awk -v fn="$1" '
		$0 ~ "^export function " fn "\\(" { in_fn = 1 }
		in_fn { print }
		in_fn && /^}$/ { exit }
	' "$FILE"
}

EMPTY="$(func_body emptyForm)"
[ -n "$EMPTY" ] || { echo "FAIL: 前提失效，$FILE 中未找到 emptyForm"; exit 2; }
if printf '%s\n' "$EMPTY" | grep -q 'ssSubscriptionUsers: false,'; then
	echo "OK: emptyForm defaults ssSubscriptionUsers to false"
else
	echo "FAIL: emptyForm must default ssSubscriptionUsers to false (S05)"
	fail=1
fi
if printf '%s\n' "$EMPTY" | grep -q 'ssSubscriptionUsers: true,'; then
	echo "FAIL: emptyForm still carries ssSubscriptionUsers: true (S05)"
	fail=1
fi
if printf '%s\n' "$EMPTY" | grep -q 'meteringEnabled: false,'; then
	echo "OK: emptyForm defaults meteringEnabled to false (legacy read-only)"
else
	echo "FAIL: emptyForm must default meteringEnabled to false (S05)"
	fail=1
fi
if printf '%s\n' "$EMPTY" | grep -q 'meteringEnabled: true,'; then
	echo "FAIL: emptyForm still carries meteringEnabled: true (S05)"
	fail=1
fi

PAYLOAD="$(func_body toPayload)"
[ -n "$PAYLOAD" ] || { echo "FAIL: 前提失效，$FILE 中未找到 toPayload"; exit 2; }
if printf '%s\n' "$PAYLOAD" | grep -q 'form.protocol === "shadowsocks"' &&
	printf '%s\n' "$PAYLOAD" | grep -q 'form.kernelType === "xray"' &&
	printf '%s\n' "$PAYLOAD" | grep -q 'form.kernelType === "singbox"'; then
	echo "OK: toPayload gates ssSubscriptionUsers by SS + Xray/sing-box eligibility"
else
	echo "FAIL: toPayload must gate ssSubscriptionUsers by SS + Xray/sing-box eligibility (S05)"
	fail=1
fi
if printf '%s\n' "$PAYLOAD" | grep -q '^[[:space:]]*ssSubscriptionUsers: form.ssSubscriptionUsers,$'; then
	echo "FAIL: toPayload sends ssSubscriptionUsers ungated (S05)"
	fail=1
fi
if printf '%s\n' "$PAYLOAD" | grep -q '^[[:space:]]*meteringEnabled:'; then
	echo "FAIL: toPayload must not send meteringEnabled (S05)"
	fail=1
fi

exit "$fail"
