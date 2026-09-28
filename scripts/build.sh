#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
mkdir -p dist

VERSION=${VERSION:-dev}
SEEDS=${SEEDS:-}
LDFLAGS="-s -w -X main.version=$VERSION -X main.builtSeeds=$SEEDS"

build() {
	name=$1
	arch=$2
	arm=${3:-}
	if [ -n "$arm" ]; then
		GOOS=linux GOARCH="$arch" GOARM="$arm" CGO_ENABLED=0 \
			go build -trimpath -ldflags "$LDFLAGS" -o "dist/patvs-linux-$name" ./cmd/patvs
	else
		GOOS=linux GOARCH="$arch" CGO_ENABLED=0 \
			go build -trimpath -ldflags "$LDFLAGS" -o "dist/patvs-linux-$name" ./cmd/patvs
	fi
}

build amd64 amd64
build arm64 arm64
build armv7 arm 7
(cd dist && sha256sum patvs-linux-* > SHA256SUMS)

