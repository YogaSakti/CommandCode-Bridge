#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TMP=$(mktemp -d)
SERVER_PID=
trap 'test -z "$SERVER_PID" || kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP"' EXIT INT TERM
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

"$ROOT/scripts/build.sh" dev "$TMP/build" >/dev/null
case "$(go env GOOS)" in darwin) EXT=dylib ;; windows) EXT=dll ;; *) EXT=so ;; esac
LIB="$TMP/build/commandcode-bridge.$EXT"
[ -f "$LIB" ] || fail "build artifact missing"
[ ! -f "$TMP/build/commandcode-bridge.h" ] || fail "generated header was retained"
for legacy in commandcode.dylib commandcode.so commandcode.dll; do
  [ ! -e "$TMP/build/$legacy" ] || fail "legacy build artifact retained: $legacy"
done

"$ROOT/scripts/package.sh" dev "$LIB" "$TMP/dist" >/dev/null
GOOS=$(go env GOOS); GOARCH=$(go env GOARCH)
ASSET="$TMP/dist/commandcode-bridge_dev_${GOOS}_${GOARCH}.zip"
[ -f "$ASSET" ] || fail "package asset missing"
[ "$(unzip -Z1 "$ASSET")" = "commandcode-bridge.$EXT" ] || fail "archive must contain one root library"
[ ! -e "$TMP/dist/commandcode_dev_${GOOS}_${GOARCH}.zip" ] || fail "legacy package asset retained"
(
  cd "$TMP/dist"
  if command -v sha256sum >/dev/null 2>&1; then sha256sum -c checksums.txt >/dev/null; else shasum -a 256 -c checksums.txt >/dev/null; fi
) || fail "checksum validation failed"

case "$EXT" in so) WRONG=dll ;; *) WRONG=so ;; esac
cp "$LIB" "$TMP/build/commandcode-bridge.$WRONG"
if "$ROOT/scripts/package.sh" dev "$TMP/build/commandcode-bridge.$WRONG" "$TMP/dist" >/dev/null 2>&1; then fail "package accepted wrong platform extension"; fi

PAGE="$ROOT/web/accounts.html"
for want in \
  '<title>CommandCode Bridge Accounts</title>' \
  '<h1>CommandCode Bridge Accounts</h1>' \
  '/v0/management/plugins/commandcode-bridge/accounts' \
  '/v0/management/plugins/commandcode-bridge/import-local' \
  '/v0/management/plugins/commandcode-bridge/validate'; do
  grep -Fq "$want" "$PAGE" || fail "accounts page missing $want"
done
for legacy in '/v0/management/plugins/commandcode/' '/v0/resource/plugins/commandcode/'; do
  ! grep -Fq "$legacy" "$PAGE" || fail "accounts page retains legacy route: $legacy"
done
for source in "$ROOT/scripts/build.sh" "$ROOT/scripts/package.sh" "$ROOT/.github/workflows/release.yml"; do
  for legacy in commandcode.dylib commandcode.so commandcode.dll commandcode_; do
    ! grep -Fq "$legacy" "$source" || fail "delivery source retains legacy artifact: $legacy"
  done
done
"$ROOT/scripts/refresh-model-snapshot.sh" --check >/dev/null
[ -f "$ROOT/DISCLAIMER.md" ] || fail "DISCLAIMER.md missing"
for want in \
  '> **Unofficial project.**' \
  '[DISCLAIMER.md](DISCLAIMER.md)' \
  'https://commandcode.ai/terms' \
  'https://commandcode.ai/privacy' \
  'not affiliated' \
  'not endorsed' \
  'own authorized'; do
  grep -Fiq "$want" "$ROOT/README.md" || fail "README disclaimer missing $want"
done


python3 - "$ROOT/plugin-store.json" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
data = json.loads(p.read_text())
assert set(data) == {"schema_version", "plugins"}
assert data["schema_version"] == 1
assert len(data["plugins"]) == 1
plugin = data["plugins"][0]
assert plugin == {
    "id": "commandcode-bridge",
    "name": "CommandCode Bridge",
    "description": "Unofficial CLIProxyAPI bridge for using your own Command Code account through OpenAI-compatible APIs.",
    "author": "YogaSakti",
    "repository": "https://github.com/YogaSakti/CommandCode-Bridge",
    "homepage": "https://github.com/YogaSakti/CommandCode-Bridge",
    "license": "MIT",
    "tags": ["cliproxyapi", "commandcode", "bridge", "provider"],
}
for field in ("version", "unofficial", "targets", "publication_blocked_by"):
    assert field not in plugin
PY
PORT_FILE="$TMP/port"
python3 - "$PORT_FILE" <<'PY' &
import http.server,json,pathlib,socketserver,sys
port_file=pathlib.Path(sys.argv[1])
class Handler(http.server.BaseHTTPRequestHandler):
  def do_GET(self):
    if self.path=="/v0/management/plugins":
      body=json.dumps({"plugins_enabled":True,"plugins":[{"id":"commandcode-bridge","registered":True,"effective_enabled":True}]}).encode()
      self.send_response(200); self.send_header("Content-Type","application/json"); self.send_header("X-CPA-SUPPORT-PLUGIN","1"); self.end_headers(); self.wfile.write(body); return
    if self.path in ("/v0/management/plugins/commandcode-bridge/accounts","/v0/resource/plugins/commandcode-bridge/accounts"):
      self.send_response(200); self.end_headers(); return
    self.send_response(404); self.end_headers()
  def log_message(self,*_): pass
with socketserver.TCPServer(("127.0.0.1",0),Handler) as server:
  port_file.write_text(str(server.server_address[1])); server.serve_forever()
PY
SERVER_PID=$!
count=0
while [ ! -s "$PORT_FILE" ]; do count=$((count+1)); [ "$count" -lt 100 ] || fail "stub server did not start"; sleep 0.05; done
PORT=$(cat "$PORT_FILE")
SMOKE_OUTPUT=$(CPA_MANAGEMENT_KEY=secret CPA_MANAGEMENT_PATH=/v0/management/plugins/commandcode-bridge/accounts CPA_RESOURCE_PATH=/v0/resource/plugins/commandcode-bridge/accounts "$ROOT/scripts/smoke.sh" "http://127.0.0.1:$PORT")
[ "$SMOKE_OUTPUT" = 'plugin commandcode-bridge is registered and effectively enabled' ] || fail "unexpected smoke output"
printf '%s' "$SMOKE_OUTPUT" | grep -q secret && fail "smoke output leaked secret"

for section in Prerequisites Build Install Configuration Enrollment 'Account Routing' Usage Security Troubleshooting Releases Verification; do
  grep -q "## $section" "$ROOT/README.md" || fail "README missing $section"
done
grep -Fq 'When both `plan` and `priority_override` are absent, a valid legacy top-level `priority` from `1` to `10` is preserved as the override.' "$ROOT/README.md" || fail "README missing legacy priority preservation"
[ -f "$ROOT/LICENSE" ] || fail "LICENSE missing"
! grep -Eq 'github\.com/(OWNER|your-|example)|published to|live CommandCode E2E passed' "$ROOT/README.md" || fail "README contains placeholder or unverified claim"

printf 'PASS: build, package, smoke, metadata, docs\n'
