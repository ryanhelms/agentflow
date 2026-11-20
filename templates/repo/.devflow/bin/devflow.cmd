@echo off
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0devflow.ps1" %*
exit /b %ERRORLEVEL%
