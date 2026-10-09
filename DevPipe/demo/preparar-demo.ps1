# Uso: .\demo\preparar-demo.ps1 C:\repos
# Cria C:\repos\loja-demo como repositório Git com as branches main, release e stage.
param([Parameter(Mandatory = $true)][string]$Destino)
$ErrorActionPreference = "Stop"
$src = Join-Path $PSScriptRoot "loja-demo"
$target = Join-Path $Destino "loja-demo"
if (Test-Path $target) { Write-Host "Já existe: $target (nada foi alterado)"; exit 1 }
New-Item -ItemType Directory -Force -Path $Destino | Out-Null
Copy-Item -Recurse $src $target
Push-Location $target
try {
  git init -q -b main
  git add .
  git -c user.name="DevPipe Demo" -c user.email="demo@localhost" commit -q -m "Versão inicial da Loja Demo"
  git branch release
  git branch stage
} finally { Pop-Location }
Write-Host "Repositório de demonstração criado em: $target" -ForegroundColor Cyan
Write-Host "Especificação de exemplo: $(Join-Path $PSScriptRoot 'demanda-exemplo\CH-1001-cupom-desconto.docx')"
