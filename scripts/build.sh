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
	os=$3
	arm=${4:-}
	suffix=
	if [ "$os" = windows ]; then suffix=.exe; fi
	if [ -n "$arm" ]; then
		GOOS="$os" GOARCH="$arch" GOARM="$arm" CGO_ENABLED=0 \
			go build -trimpath -ldflags "$LDFLAGS" -o "dist/patvs-$os-$name$suffix" ./cmd/patvs
	else
		GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
			go build -trimpath -ldflags "$LDFLAGS" -o "dist/patvs-$os-$name$suffix" ./cmd/patvs
	fi
}

build amd64 amd64 linux
build arm64 arm64 linux
build armv7 arm linux 7
build amd64 amd64 windows
build arm64 arm64 windows
(cd dist && sha256sum patvs-linux-amd64 patvs-linux-arm64 patvs-linux-armv7 \
	patvs-windows-amd64.exe patvs-windows-arm64.exe > SHA256SUMS)
