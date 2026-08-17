#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then echo "usage: smoke.sh <base-url>" >&2; exit 2; fi
base_url=${1%/}
: "${CPA_MANAGEMENT_KEY:?CPA_MANAGEMENT_KEY is required}"
headers=$(mktemp); body=$(mktemp)
trap 'rm -f "$headers" "$body"' EXIT INT TERM
curl -fsS -D "$headers" -o "$body" -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" "$base_url/v0/management/plugins"
support=$(sed -n 's/^[Xx]-[Cc][Pp][Aa]-[Ss][Uu][Pp][Pp][Oo][Rr][Tt]-[Pp][Ll][Uu][Gg][Ii][Nn]:[[:space:]]*\([01]\).*/\1/p' "$headers" | tr -d '\r')
[ -z "$support" ] || [ "$support" = 1 ] || { echo "CPA binary reports no plugin support" >&2; exit 1; }
python3 - "$body" <<'PY'
import json,sys
data=json.load(open(sys.argv[1],encoding="utf-8")); plugins=data.get("plugins",data if isinstance(data,list) else [])
for plugin in plugins:
  if plugin.get("id",plugin.get("ID",plugin.get("plugin_id")))=="commandcode-bridge":
    if plugin.get("registered") is not True: raise SystemExit("commandcode-bridge is not registered")
    if plugin.get("effective_enabled") is not True: raise SystemExit("commandcode-bridge is not effectively enabled")
    break
else: raise SystemExit("commandcode-bridge not found")
PY
if [ -n "${CPA_MANAGEMENT_PATH:-}" ]; then curl -fsS -o /dev/null -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" "$base_url$CPA_MANAGEMENT_PATH"; fi
if [ -n "${CPA_RESOURCE_PATH:-}" ]; then curl -fsS -o /dev/null "$base_url$CPA_RESOURCE_PATH"; fi
printf 'plugin commandcode-bridge is registered and effectively enabled\n'
