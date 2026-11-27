#!/bin/sh
set -eu
repo=$(pwd -P)
cd "$1"
shift
echo "[$PWD] go $*"

# Test consumers against the checked-out bridge in a temporary workspace.
export GOWORK=off
if grep -q 'github.com/arcships/rutis-network/link/tcp[[:space:]]' go.mod; then
    module=$(pwd -P)
    workspace=$(mktemp -d)
    trap 'rm -r "$workspace"' EXIT
    (cd "$workspace" && go work init "$module" "$repo/link/tcp")
    export GOWORK="$workspace/go.work"
fi
go "$@"
