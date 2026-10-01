#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

# Pinned month-end LGPL builds from a provider linked by ffmpeg.org/download.html.
# Keep the upstream archives intact, including their license and documentation.
BASE=https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-09-30-13-08
BUILD=n9.0.2-17-g2a571b6068

download() {
	arch=$1
	platform=$2
	hash=$3
	file="dist/ffmpeg-windows-$arch.zip"
	curl --fail --silent --show-error --location --retry 3 \
		--proto '=https' --proto-redir '=https' \
		"$BASE/ffmpeg-$BUILD-$platform-lgpl-9.0.zip" -o "$file"
	printf '%s  %s\n' "$hash" "$file" | sha256sum --check --strict
}

download amd64 win64 6b264b9e6019103f601d98c292bd332fd87acf1c5e941ddff4fb71760fe63432
download arm64 winarm64 d189d06c804b01e60e9677901f410f85900682f6a59f476b933a7fa48176b0c6

(cd dist && sha256sum ffmpeg-windows-amd64.zip ffmpeg-windows-arm64.zip) >> dist/SHA256SUMS
cp docs/FFMPEG.md dist/FFMPEG.md
