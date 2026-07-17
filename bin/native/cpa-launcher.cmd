@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0cpa-launcher.ps1" %*
exit /b %ERRORLEVEL%
