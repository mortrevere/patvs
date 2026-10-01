$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
New-Item -ItemType Directory -Force dist | Out-Null

$version = if ($env:VERSION) { $env:VERSION } else { 'dev' }
$ldflags = "-s -w -X main.version=$version -X main.builtSeeds=$env:SEEDS"
$targets = @(
    @('linux', 'amd64', 'amd64', ''),
    @('linux', 'arm64', 'arm64', ''),
    @('linux', 'arm', 'armv7', '7'),
    @('windows', 'amd64', 'amd64', ''),
    @('windows', 'arm64', 'arm64', '')
)
$saved = @{}
foreach ($key in @('GOOS', 'GOARCH', 'GOARM', 'CGO_ENABLED')) {
    $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
    $env:CGO_ENABLED = '0'
    $files = foreach ($target in $targets) {
        $env:GOOS, $env:GOARCH, $name, $env:GOARM = $target
        $suffix = if ($env:GOOS -eq 'windows') { '.exe' } else { '' }
        $file = "patvs-$env:GOOS-$name$suffix"
        & go build -trimpath -ldflags $ldflags -o "dist/$file" ./cmd/patvs
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $file" }
        $file
    }
    $sums = foreach ($file in $files) {
        $hash = (Get-FileHash "dist/$file" -Algorithm SHA256).Hash.ToLowerInvariant()
        "$hash  $file"
    }
    [IO.File]::WriteAllLines((Join-Path (Get-Location) 'dist/SHA256SUMS'), $sums, [Text.UTF8Encoding]::new($false))
} finally {
    foreach ($key in $saved.Keys) {
        [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process')
    }
}
