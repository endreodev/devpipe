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

rem 3) Inicia
for /f "delims=" %%V in ('"dist\devpipe-windows-amd64.exe" -version') do echo %%V
start "" "dist\devpipe-windows-amd64.exe"
