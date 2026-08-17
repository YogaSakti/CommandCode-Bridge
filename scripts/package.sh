#!/bin/sh
set -eu

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: package.sh <version> <library> [output-dir]" >&2
  exit 2
fi
version=$1
library=$2
output_dir=${3:-dist}
case "$version" in ''|*[!0-9A-Za-z.+-]*) echo "invalid version: $version" >&2; exit 2 ;; esac
[ -f "$library" ] || { echo "library not found: $library" >&2; exit 2; }
goos=$(go env GOOS); goarch=$(go env GOARCH)
case "$goos" in darwin) ext=dylib ;; windows) ext=dll ;; *) ext=so ;; esac
base=$(basename "$library")
[ "$base" = "commandcode-bridge.$ext" ] || { echo "library must be commandcode-bridge.$ext for $goos/$goarch" >&2; exit 2; }
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
asset="commandcode-bridge_${version}_${goos}_${goarch}.zip"
asset_path="$output_dir/$asset"
rm -f "$asset_path"
(cd "$(dirname "$library")" && zip -X -q "$asset_path" "$base")
if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum "$asset_path" | cut -d ' ' -f 1); else digest=$(shasum -a 256 "$asset_path" | cut -d ' ' -f 1); fi
checksums="$output_dir/checksums.txt"; tmp="$checksums.tmp"
if [ -f "$checksums" ]; then sed "/  $asset$/d" "$checksums" > "$tmp"; else : > "$tmp"; fi
printf '%s  %s\n' "$digest" "$asset" >> "$tmp"; mv "$tmp" "$checksums"
printf '%s\n' "$asset_path"
