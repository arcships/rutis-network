#!/bin/sh
set -eu
repo=$(pwd -P)
cd "$1"
shift
echo "[$PWD] go $*"

# Select the module's own toolchain and use the checked-out bridge when required.
export GOWORK=off
if grep -q 'github.com/arcships/rutis-network/link/tcp[[:space:]]' go.mod; then
    module=$(pwd -P)
    goroot=$(go env GOROOT)
    workspace=$(mktemp -d)
    trap 'rm -r "$workspace"' EXIT
    (cd "$workspace" && "$goroot/bin/go" work init "$module" "$repo/link/tcp")
    export GOWORK="$workspace/go.work"
fi
go "$@"
