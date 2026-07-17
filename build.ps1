# Builds rds-bridge.exe on Windows (PowerShell). Mirrors `make build`.
# Requires the Go toolchain on PATH. No `make` needed.
$ErrorActionPreference = "Stop"

$version = "dev"
try { $version = (git describe --tags --always --dirty 2>$null) } catch {}
if (-not $version) { $version = "dev" }

$ldflags = "-s -w -X main.version=$version"

New-Item -ItemType Directory -Force -Path bin | Out-Null
go build -ldflags $ldflags -o bin/rds-bridge.exe .

Write-Host "built bin/rds-bridge.exe ($version)"
