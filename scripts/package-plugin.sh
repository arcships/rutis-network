#!/usr/bin/env bash
# Cross-compile a single module tag on the Ubuntu release runner.
set -euo pipefail
export LC_ALL=C TZ=UTC
shopt -s nullglob nocaseglob
tag=${1:?usage: package-plugin.sh MODULE/VERSION OUTPUT_DIR}
output=${2:?missing output directory}
module=${tag%/*}
version=${tag##*/}
[[ $module =~ ^(link|discovery|group)/[a-z0-9-]+$ && $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || {
  echo "Invalid plugin tag: $tag" >&2
  exit 1
}
repo=$(cd "$(dirname "$0")/.." && pwd -P)
commands=("$repo/$module"/cmd/*/main.go)
if [[ ${#commands[@]} != 1 ]]; then
  echo "Expected exactly one plugin command under $module/cmd" >&2
  exit 1
fi
binary=$(basename "$(dirname "${commands[0]}")")
package="github.com/arcships/rutis-network/$module/cmd/$binary"
mkdir "$output"
output=$(cd "$output" && pwd -P)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
export CGO_ENABLED=0 GOWORK=off GOAMD64=v1
# A private GOPATH isolates installed commands while reusing downloaded modules.
GOMODCACHE=$(go env GOMODCACHE)
export GOMODCACHE GOPATH="$work/go" GOBIN='' GOCACHE="$work/build-cache"
host=$(go env GOHOSTOS)_$(go env GOHOSTARCH)
common="$work/common"
mkdir -p "$common/plugins/go" "$common/licenses"
cp "$repo/LICENSE" "$common/LICENSE"
cp "$(go env GOROOT)/LICENSE" "$common/licenses/Go.txt"

copy_notices() {
  local source=$1 dest=$2
  local notices=("$source"/{LICENSE*,LICENCE*,COPYING*,NOTICE*,PATENTS*})
  if [[ ${#notices[@]} == 0 ]]; then
    echo "No license notice in $source" >&2
    return 1
  fi
  mkdir -p "$dest"
  cp -R "${notices[@]}" "$dest/"
}

if [[ $module == link/tcp ]]; then
  cp "$repo/$module/release/rutis.json" "$common/"
elif [[ $module == link/webrtc ]]; then
  web="$repo/$module/web"
  cp -R "$web/dist" "$common/browser"
  for dep in @arcships/rutis-runtime buffer base64-js ieee754; do
    copy_notices "$web/node_modules/$dep" "$common/licenses/browser/$dep"
  done
fi

archives=()
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  export GOOS=${target%/*} GOARCH=${target#*/}
  echo "Packaging $tag for $target"
  go install -trimpath "$package@$version"
  bindir="$GOPATH/bin"
  if [[ ${GOOS}_${GOARCH} != "$host" ]]; then bindir+="/${GOOS}_${GOARCH}"; fi
  executable=$binary
  if [[ $GOOS == windows ]]; then executable+=.exe; fi
  bundle="$work/bundle"
  cp -R "$common" "$bundle"
  mv "$bindir/$executable" "$bundle/plugins/go/"

  # Include the notices for dependencies actually linked into this target.
  go version -m -json "$bundle/plugins/go/$executable" \
    | jq -r '.Deps[] | "\(.Path)@\(.Version)"' | xargs -r go mod download -json \
    | jq -r '[.Path, .Dir] | @tsv' | while IFS=$'\t' read -r dep source; do
        copy_notices "$source" "$bundle/licenses/go/$dep"
      done
  chmod -R u+rwX "$bundle"

  name="$binary-$version-$GOOS-$GOARCH"
  if [[ $GOOS == windows ]]; then
    archive="$name.zip"
    (cd "$bundle" && find . -type f -exec touch -d '1980-01-01 UTC' {} + && \
      find . -type f -printf '%P\n' | sort | zip -Xq "$output/$archive" -@)
  else
    archive="$name.tar.gz"
    (cd "$bundle" && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf - -- *) \
      | gzip -n > "$output/$archive"
  fi
  archives+=("$archive")
  # Bound disk use for large dependency trees such as tsnet across five targets.
  rm -rf "$bundle" "$GOCACHE"
done
(cd "$output" && sha256sum "${archives[@]}" > SHA256SUMS)
