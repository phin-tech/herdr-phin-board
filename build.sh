#!/bin/sh
# Build step for `herdr plugin install`.
#
# Compiles from source when Go is available. Otherwise it falls back to the
# binary CI publishes, so the plugin installs on a machine without a toolchain.
set -eu

out="bin/herdr-phin-board"
repo="phin-tech/herdr-phin-board"

mkdir -p bin

if command -v go >/dev/null 2>&1; then
  exec go build -trimpath -o "$out" ./cmd/herdr-phin-board
fi

echo "go not found on PATH; downloading a prebuilt binary instead" >&2

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin | linux) ;;
  *)
    echo "no prebuilt binary for $os -- install Go and retry" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  arm64 | aarch64) arch="arm64" ;;
  x86_64 | amd64) arch="amd64" ;;
  *)
    echo "no prebuilt binary for $(uname -m) -- install Go and retry" >&2
    exit 1
    ;;
esac

name="herdr-phin-board-${os}-${arch}"
# The rolling build is a prerelease, and /releases/latest/ skips those, so the
# tag is named explicitly.
base="https://github.com/${repo}/releases/download/latest"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "${base}/${name}" -o "${tmp}/${name}"
curl -fsSL "${base}/${name}.sha256" -o "${tmp}/${name}.sha256"

# Never install a binary that does not match its published checksum.
( cd "$tmp" && shasum -a 256 -c "${name}.sha256" >/dev/null ) || {
  echo "checksum mismatch for ${name} -- refusing to install" >&2
  exit 1
}

mv "${tmp}/${name}" "$out"
chmod +x "$out"
echo "installed prebuilt ${name}" >&2
