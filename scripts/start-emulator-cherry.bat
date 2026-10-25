@echo off
set "EMULATOR=%LOCALAPPDATA%\Android\Sdk\emulator\emulator.exe"
if not exist "%EMULATOR%" (
  echo emulator.exe not found: "%EMULATOR%"
  exit /b 1
)
echo Starting AVD Cherry. Requires cherry.exe running; Cherry answers DNS on this machine (loopback).
start "Cherry AVD" "%EMULATOR%" -avd Cherry -netdelay none -netspeed full -gpu swiftshader_indirect -no-snapshot-load -dns-server 127.0.0.1
