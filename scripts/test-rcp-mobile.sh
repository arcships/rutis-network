#!/bin/sh
set -eu
repo=$(pwd -P)
kotlin="$repo/link/rcp/kotlin"
swift="$repo/link/rcp/swift"
"$kotlin/gradlew" -p "$kotlin" test
swift test --package-path "$swift" --skip InteropTests
RCP_KOTLIN_CLASSPATH=$("$kotlin/gradlew" -q -p "$kotlin" interopClasspath)
export RCP_KOTLIN_CLASSPATH
sh scripts/in-go-module.sh link/rcp go test -mod=readonly -race -v -count=1 -run '^TestMobile(Interop|Faults|RelayResponder|KotlinWSSClose)$' .
