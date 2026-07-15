@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0Start-CPA.ps1" %*
exit /b %ERRORLEVEL%
