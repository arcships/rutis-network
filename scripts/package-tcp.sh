#!/usr/bin/env bash
# Build exactly the Go module a consumer installs. Run on Linux/amd64.
set -euo pipefail
version=${1:?usage: package-tcp.sh VERSION OUTPUT_DIR}
output=${2:?missing output directory}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]
[[ $(go env GOHOSTOS)/$(go env GOHOSTARCH) == linux/amd64 ]]
mkdir -p "$output"
output=$(cd "$output" && pwd -P)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
export GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1
module=github.com/arcships/rutis-network/link/tcp

go mod init example.org/tcp-release-consumer
cat > main.go <<'GO'
package main

import "github.com/arcships/rutis-network/link/tcp"

var _ = tcp.Open

func main() {}
GO
go get "$module@$version"
test "$(go list -m -f '{{if not .Replace}}{{.Version}}{{end}}' "$module")" = "$version"
go build -mod=readonly .
bundle="$work/bundle"
export GOBIN="$bundle/plugins/go"
mkdir -p "$GOBIN" "$bundle/licenses"
go install -trimpath "$module/cmd/link-tcp@$version"

source=$(go list -m -f '{{.Dir}}' "$module")
sdk=$(go list -m -f '{{.Dir}}' github.com/arcships/rutis/go/rutis)
cp "$source/LICENSE" "$source/release/rutis.json" "$bundle/"
cp "$sdk/LICENSE" "$bundle/licenses/rutis.txt"
cp "$(go env GOROOT)/LICENSE" "$bundle/licenses/Go.txt"

archive="link-tcp-${version}-linux-amd64.tar.gz"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -C "$bundle" \
    -cf - LICENSE licenses plugins rutis.json | gzip -n > "$output/$archive"
(cd "$output" && sha256sum "$archive" > SHA256SUMS)
