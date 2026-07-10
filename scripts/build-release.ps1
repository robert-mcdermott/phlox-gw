param(
    [switch]$SkipFrontend,
    [switch]$Clean,
    [string]$DistDir
)

$ErrorActionPreference = "Stop"

$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$VersionFile = Join-Path $RootDir "VERSION"
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

if (-not (Test-Path $VersionFile)) {
    throw "Missing version file: $VersionFile"
}
$Version = (Get-Content -Raw -Path $VersionFile).Trim()
if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$') {
    throw "Invalid version in VERSION: $Version. Expected a semantic version such as v0.1.0 or v0.2.0-rc.1."
}

$BuildCommit = $env:PHLOX_GW_BUILD_COMMIT
if ([string]::IsNullOrWhiteSpace($BuildCommit) -and (Get-Command git -ErrorAction SilentlyContinue)) {
    $DetectedCommit = & git -C $RootDir rev-parse --short=12 HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $null -ne $DetectedCommit) {
        $BuildCommit = ([string]$DetectedCommit).Trim()
        & git -C $RootDir diff --quiet --ignore-submodules HEAD --
        if ($LASTEXITCODE -ne 0) {
            $BuildCommit = "$BuildCommit-dirty"
        }
    }
}
if ([string]::IsNullOrWhiteSpace($BuildCommit)) {
    $BuildCommit = "unknown"
}
$BuildDate = $env:PHLOX_GW_BUILD_DATE
if ([string]::IsNullOrWhiteSpace($BuildDate)) {
    $BuildDate = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
}
$LdFlags = "-s -w -X github.com/robert-mcdermott/phlox-gw.BuildVersion=$Version -X github.com/robert-mcdermott/phlox-gw.BuildCommit=$BuildCommit -X github.com/robert-mcdermott/phlox-gw.BuildDate=$BuildDate"

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

Write-Host "==> Version: $Version"
Write-Host "==> Commit: $BuildCommit"
Write-Host "==> Build date: $BuildDate"

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
        go build -trimpath -ldflags="$LdFlags" -o $OutputPath ./cmd/phlox-gw
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
