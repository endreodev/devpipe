@echo off
chcp 65001 >nul
title DevPipe
cd /d "%~dp0"

rem 1) Fecha qualquer DevPipe aberto (versões antigas seguram a porta 7420)
taskkill /F /FI "IMAGENAME eq devpipe*" >nul 2>&1
timeout /t 2 /nobreak >nul

rem 2) Se houver uma versão nova em dist\devpipe-v*.exe, instala no nome padrão
set "NOVA="
for /f "delims=" %%F in ('dir /b /o:d "dist\devpipe-v*.exe" 2^>nul') do set "NOVA=%%F"
if defined NOVA (
  copy /y "dist\%NOVA%" "dist\devpipe-windows-amd64.exe" >nul
  if errorlevel 1 (
    echo Nao foi possivel atualizar o executavel. Feche o DevPipe no Gerenciador de Tarefas e tente de novo.
    pause
    exit /b 1
  )
  rem remove todas as versoes pendentes (evita reinstalar uma versao antiga depois)
  del "dist\devpipe-v*.exe" >nul 2>&1
  echo Atualizado para %NOVA%
)

rem 3) Se o executavel nao existir, compila a partir do codigo-fonte (usa vendor\, sem internet)
if not exist "dist\devpipe-windows-amd64.exe" (
  echo Executavel nao encontrado. Compilando o DevPipe...
  where go >nul 2>&1
  if errorlevel 1 (
    echo.
    echo O Go nao esta instalado ou nao esta no PATH.
    echo Instale o Go 1.24+ em https://go.dev/dl/ e rode este script de novo.
    pause
    exit /b 1
  )
  if not exist "dist" mkdir "dist"
  set "CGO_ENABLED=0"
  go build -mod=vendor -trimpath -ldflags "-s -w" -o "dist\devpipe-windows-amd64.exe" .\cmd\devpipe
  if errorlevel 1 (
    echo.
    echo Falha na compilacao. Veja as mensagens acima.
    pause
    exit /b 1
  )
  echo Compilado: dist\devpipe-windows-amd64.exe
)

rem 4) Inicia
for /f "delims=" %%V in ('"dist\devpipe-windows-amd64.exe" -version') do echo %%V
start "" "dist\devpipe-windows-amd64.exe"
