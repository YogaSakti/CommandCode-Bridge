#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: build.sh <version> [output-dir]" >&2
  exit 2
fi
version=$1
output_dir=${2:-dist}
case "$version" in ''|*[!0-9A-Za-z.+-]*) echo "invalid version: $version" >&2; exit 2 ;; esac
case "$(go env GOOS)" in darwin) ext=dylib ;; windows) ext=dll ;; *) ext=so ;; esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
artifact="$output_dir/commandcode-bridge.$ext"
commit=${COMMIT:-$(git -C "$root" rev-parse --short HEAD 2>/dev/null || printf none)}
build_date=${BUILD_DATE:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}
(
  cd "$root"
  CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
    -ldflags "-X main.Version=$version -X main.Commit=$commit -X main.BuildDate=$build_date" \
    -o "$artifact" .
)
rm -f "$output_dir/commandcode-bridge.h"
printf '%s\n' "$artifact"
