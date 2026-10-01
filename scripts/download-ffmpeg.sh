#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

# Pinned month-end LGPL builds from a provider linked by ffmpeg.org/download.html.
# Extract only the unchanged executable and its license after verification.
BASE=https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-09-30-13-08
BUILD=n9.0.2-17-g2a571b6068
downloads=$(mktemp -d)
trap 'rm -rf "$downloads"' 0

download() {
	platform=$1
	hash=$2
	file=$3
	root="ffmpeg-$BUILD-$platform-lgpl-9.0"
	archive="$downloads/$root.zip"
	curl --fail --silent --show-error --location --retry 3 \
		--proto '=https' --proto-redir '=https' \
		"$BASE/$root.zip" -o "$archive"
	printf '%s  %s\n' "$hash" "$archive" | sha256sum --check --strict
	python3 - "$archive" "$root" "dist/$file" <<'PY'
import shutil, sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as archive:
    for source, destination in [("bin/ffmpeg.exe", sys.argv[3]), ("LICENSE.txt", "dist/FFMPEG-LICENSE.txt")]:
        with archive.open(sys.argv[2] + "/" + source) as src, open(destination, "wb") as dst:
            shutil.copyfileobj(src, dst)
PY
}

download win64 6b264b9e6019103f601d98c292bd332fd87acf1c5e941ddff4fb71760fe63432 ffmpeg.exe
download winarm64 d189d06c804b01e60e9677901f410f85900682f6a59f476b933a7fa48176b0c6 ffmpeg-arm64.exe

(cd dist && sha256sum ffmpeg.exe ffmpeg-arm64.exe FFMPEG-LICENSE.txt) >> dist/SHA256SUMS
