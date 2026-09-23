#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
go_version=1.27.1
go_dir="$script_dir/build/go$go_version"

# Run on Linux x64; cross-compile both release binaries with the same toolchain.
if [[ ! -x "$go_dir/bin/go" ]]; then
  archive="$script_dir/build/go$go_version.linux-amd64.tar.gz"
  mkdir -p "$go_dir"
  wget -q "https://go.dev/dl/go$go_version.linux-amd64.tar.gz" -O "$archive"
  tar --strip-components=1 -xzf "$archive" -C "$go_dir"
fi

export CGO_ENABLED=0
export GOARCH=amd64
export GOTOOLCHAIN=local

cp "$script_dir/caddy/go.mod.initial" "$script_dir/caddy/go.mod"
"$go_dir/bin/go" -C "$script_dir/caddy" mod tidy

mkdir -p "$script_dir/bin"
for target in linux windows; do
  output="$script_dir/bin/caddy-$target-amd64"
  if [[ "$target" == windows ]]; then
    output+=.exe
  fi
  GOOS="$target" "$go_dir/bin/go" -C "$script_dir/caddy" build \
    -mod=readonly -trimpath -ldflags="-s -w" -o "$output" .
done

"$script_dir/bin/caddy-linux-amd64" version
"$script_dir/bin/caddy-linux-amd64" list-modules
