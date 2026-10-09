#!/bin/sh
set -eu

# Compile the pinned module using Go's checksum-verified module cache. The cache
# is shared with normal Go builds, so no second toolchain checkout is needed.
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
exec go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 "$@"
