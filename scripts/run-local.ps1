param(
    [string]$EnvFile,
    [string]$Binary
)

$ErrorActionPreference = "Stop"
$RootDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

if ([string]::IsNullOrWhiteSpace($EnvFile)) {
    if (-not [string]::IsNullOrWhiteSpace($env:PHLOX_GW_ENV_FILE)) {
        $EnvFile = $env:PHLOX_GW_ENV_FILE
    } else {
        $EnvFile = Join-Path $RootDir ".env"
    }
}

if (Test-Path $EnvFile) {
    Get-Content $EnvFile | ForEach-Object {
        $Line = $_.Trim()
        if ($Line.Length -eq 0 -or $Line.StartsWith("#")) {
            return
        }
        $Parts = $Line.Split("=", 2)
        if ($Parts.Count -ne 2) {
            return
        }
        $Name = $Parts[0].Trim()
        $Value = $Parts[1].Trim().Trim('"').Trim("'")
        if ($Name.Length -gt 0) {
            [Environment]::SetEnvironmentVariable($Name, $Value, "Process")
        }
    }
}

if ([string]::IsNullOrWhiteSpace($env:PHLOX_GW_ADDR)) {
    $env:PHLOX_GW_ADDR = "127.0.0.1:8080"
}
if ([string]::IsNullOrWhiteSpace($env:PHLOX_GW_DATA_DIR)) {
    $env:PHLOX_GW_DATA_DIR = Join-Path $RootDir ".phlox-gw-data"
}
if (-not [IO.Path]::IsPathRooted($env:PHLOX_GW_DATA_DIR)) {
    $env:PHLOX_GW_DATA_DIR = Join-Path $RootDir $env:PHLOX_GW_DATA_DIR
}

New-Item -ItemType Directory -Force -Path $env:PHLOX_GW_DATA_DIR | Out-Null

if ([string]::IsNullOrWhiteSpace($env:PHLOX_GW_SESSION_SECRET)) {
    if ([string]::IsNullOrWhiteSpace($env:PHLOX_GW_SESSION_SECRET_FILE)) {
        $SecretFile = Join-Path $env:PHLOX_GW_DATA_DIR "session-secret"
    } else {
        $SecretFile = $env:PHLOX_GW_SESSION_SECRET_FILE
    }
    if (-not (Test-Path $SecretFile)) {
        $Bytes = New-Object byte[] 48
        [Security.Cryptography.RandomNumberGenerator]::Fill($Bytes)
        [Convert]::ToBase64String($Bytes) | Set-Content -NoNewline -Path $SecretFile
    }
    $env:PHLOX_GW_SESSION_SECRET = Get-Content -Raw -Path $SecretFile
}

if ([string]::IsNullOrWhiteSpace($Binary)) {
    if (-not [string]::IsNullOrWhiteSpace($env:PHLOX_GW_BINARY)) {
        $Binary = $env:PHLOX_GW_BINARY
    } elseif (Test-Path (Join-Path $RootDir "phlox-gw.exe")) {
        $Binary = Join-Path $RootDir "phlox-gw.exe"
    } elseif (Test-Path (Join-Path $RootDir "dist/phlox-gw-windows-arm64.exe") -and $env:PROCESSOR_ARCHITECTURE -match "ARM64") {
        $Binary = Join-Path $RootDir "dist/phlox-gw-windows-arm64.exe"
    } else {
        $Binary = Join-Path $RootDir "dist/phlox-gw-windows-amd64.exe"
    }
}

if (-not (Test-Path $Binary)) {
    throw "Phlox-GW binary not found: $Binary. Build it with scripts/build-release.ps1 or go build -o phlox-gw.exe ./cmd/phlox-gw."
}

Write-Host "Starting Phlox-GW"
Write-Host "  binary: $Binary"
Write-Host "  addr:   $env:PHLOX_GW_ADDR"
Write-Host "  data:   $env:PHLOX_GW_DATA_DIR"
& $Binary
