## Windows FFmpeg downloads

Windows emitters need FFmpeg. Download `ffmpeg.exe` alongside
`patvs-windows-amd64.exe`, or use `ffmpeg-arm64.exe` with
`patvs-windows-arm64.exe` for Windows on ARM and rename it to `ffmpeg.exe`.
Put FFmpeg beside the patvs executable or on `PATH`, and keep the release's
`FFMPEG-LICENSE.txt` with it. Controllers only need patvs; receivers also need
[VLC](https://www.videolan.org/vlc/).

These are unmodified static LGPL builds from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds), a Windows build
provider linked on [FFmpeg's download page](https://ffmpeg.org/download.html).
The release workflow downloads upstream archives over HTTPS and checks their
pinned SHA-256 hashes before extracting only `bin/ffmpeg.exe` and `LICENSE.txt`.
The executable bytes are unchanged; `SHA256SUMS` covers both executables and
the license. No FFmpeg download
occurs when running patvs or doing a normal local build.

Upstream version and source references:

- Binary release: [autobuild-2026-09-30-13-08](https://github.com/BtbN/FFmpeg-Builds/releases/tag/autobuild-2026-09-30-13-08),
  FFmpeg `n9.0.2-17-g2a571b6068`, `win64-lgpl-9.0` and `winarm64-lgpl-9.0`.
- [FFmpeg source at the binary's commit](https://github.com/FFmpeg/FFmpeg/tree/2a571b606854520cf89804d8030c8b328e621689).
- [Build recipes, patches, dependency versions and source download locations](https://github.com/BtbN/FFmpeg-Builds/tree/6c9aec5fc9a72ec3abedd1fa84db141fa18cf52b).
  The upstream `download.sh` downloads the dependency sources, and `build.sh`
  builds the selected variant. Use the FFmpeg commit above when reproducing
  this binary, rather than the moving `release/9.0` branch.

The included `FFMPEG-LICENSE.txt` contains LGPLv3. FFmpeg is distributed separately
from patvs and executed as a subprocess.
