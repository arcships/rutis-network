#!/usr/bin/env bash
set -euo pipefail
assets=${1:?usage: test-tcp-archive.sh ASSET_DIR VERSION}
version=${2:?missing version}
repo=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$assets"
archive="link-tcp-${version}-linux-amd64.tar.gz"
awk -v archive="$archive" '$2 == archive' SHA256SUMS | sha256sum -c -
install_dir=$(mktemp -d)
trap 'rm -rf "$install_dir"' EXIT
tar -xzf "$archive" -C "$install_dir"
test -x "$install_dir/plugins/go/link-tcp"
TCP_INSTALL_DIR="$install_dir" node --test --test-reporter="$repo/scripts/integration-reporter.mjs" \
    "$repo/link/tcp/node/test/release.mjs"
