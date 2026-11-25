@echo off
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0agentflow.ps1" %*
exit /b %ERRORLEVEL%
