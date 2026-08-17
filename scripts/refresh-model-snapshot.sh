#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
target="$root/internal/modelsnapshot/snapshot.json"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT INT TERM
if [ "${1:-}" = "--check" ]; then cp "$target" "$tmp"; else curl -fsS "https://api.commandcode.ai/provider/v1/models" > "$tmp"; fi
python3 - "$tmp" "$target" "${1:-}" <<'PY'
import json,pathlib,sys
source = pathlib.Path(sys.argv[1])
target = pathlib.Path(sys.argv[2])
mode = sys.argv[3]
data=json.loads(source.read_text())
if isinstance(data,dict): data=data.get("data")
if not isinstance(data,list): raise SystemExit("model catalog must be an array or object.data")
models={}
for item in data:
  if not isinstance(item,dict) or not str(item.get("id","")).strip(): raise SystemExit("model entry missing id")
  ident=str(item["id"]).strip()
  models[ident]={"id":ident,"object":item.get("object","model"),"created":int(item.get("created",0)),"owned_by":item.get("owned_by","command-code"),"name":item.get("name",ident),"context_length":int(item.get("context_length",0))}
normalized=[models[key] for key in sorted(models)]
text=json.dumps(normalized,ensure_ascii=False,separators=(",",":"))+"\n"
if mode=="--check":
  existing=json.loads(target.read_text())
  ids=[str(x.get("id","")).strip() for x in existing]
  if not ids or len(ids)!=len(set(ids)) or any(not x for x in ids): raise SystemExit("snapshot contains invalid or duplicate ids")
else:
  source.write_text(text)
PY
if [ "${1:-}" != "--check" ]; then mv "$tmp" "$target"; trap - EXIT INT TERM; fi
