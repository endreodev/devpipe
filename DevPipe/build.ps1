# Gera dist\devpipe.exe (usa vendor\: não precisa de internet). Requer Go 1.24+.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
New-Item -ItemType Directory -Force -Path dist | Out-Null
$env:CGO_ENABLED = "0"
go build -mod=vendor -trimpath -ldflags "-s -w" -o dist\devpipe.exe .\cmd\devpipe
if ($LASTEXITCODE -ne 0) { throw "falha na compilação" }
Write-Host "Pronto: dist\devpipe.exe" -ForegroundColor Cyan
