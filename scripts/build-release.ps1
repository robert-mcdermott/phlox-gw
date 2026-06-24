param(
    [switch]$SkipFrontend,
    [switch]$Clean,
    [string]$DistDir
)

$ErrorActionPreference = "Stop"

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($DistDir)) {
    $DistDir = Join-Path $RootDir "dist"
}

function Require-Command($Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Missing required command: $Name"
    }
}

Require-Command "go"
if (-not $SkipFrontend -and $env:PHLOX_GW_SKIP_FRONTEND_BUILD -ne "1") {
    Require-Command "npm"
}

if ($Clean -and (Test-Path $DistDir)) {
    Remove-Item -Recurse -Force $DistDir
}
New-Item -ItemType Directory -Force -Path $DistDir | Out-Null

if ($SkipFrontend -or $env:PHLOX_GW_SKIP_FRONTEND_BUILD -eq "1") {
    Write-Host "==> Skipping frontend build"
} else {
    Write-Host "==> Building frontend"
    Push-Location (Join-Path $RootDir "frontend")
    try {
        npm run build
    } finally {
        Pop-Location
    }
}

$Targets = @(
    @{ GOOS = "darwin";  GOARCH = "arm64"; Output = "phlox-gw-darwin-arm64" },
    @{ GOOS = "linux";   GOARCH = "amd64"; Output = "phlox-gw-linux-amd64" },
    @{ GOOS = "linux";   GOARCH = "arm64"; Output = "phlox-gw-linux-arm64" },
    @{ GOOS = "windows"; GOARCH = "amd64"; Output = "phlox-gw-windows-amd64.exe" },
    @{ GOOS = "windows"; GOARCH = "arm64"; Output = "phlox-gw-windows-arm64.exe" }
)

$ChecksumPath = Join-Path $DistDir "checksums.txt"
Set-Content -Path $ChecksumPath -Value "" -NoNewline

$OldGOOS = $env:GOOS
$OldGOARCH = $env:GOARCH
$OldCGO = $env:CGO_ENABLED

try {
    foreach ($Target in $Targets) {
        $env:GOOS = $Target.GOOS
        $env:GOARCH = $Target.GOARCH
        $env:CGO_ENABLED = "0"
        $OutputPath = Join-Path $DistDir $Target.Output
        Write-Host "==> Building $($Target.Output)"
        go build -trimpath -ldflags="-s -w" -o $OutputPath ./cmd/phlox-gw
        $Hash = Get-FileHash -Algorithm SHA256 -Path $OutputPath
        Add-Content -Path $ChecksumPath -Value "$($Hash.Hash.ToLowerInvariant())  $($Target.Output)"
    }
} finally {
    $env:GOOS = $OldGOOS
    $env:GOARCH = $OldGOARCH
    $env:CGO_ENABLED = $OldCGO
}

Write-Host "==> Wrote release binaries to $DistDir"
Write-Host "==> Checksums: $ChecksumPath"
