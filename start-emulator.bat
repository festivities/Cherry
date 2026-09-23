@echo off
set "EMULATOR=%LOCALAPPDATA%\Android\Sdk\emulator\emulator.exe"
if not exist "%EMULATOR%" (
  echo emulator.exe not found: "%EMULATOR%"
  exit /b 1
)
echo Starting AVD Cherry. Requires cherry.exe running; host DNS 192.168.1.7 must be this machine.
start "Cherry AVD" "%EMULATOR%" -avd Cherry -netdelay none -netspeed full -gpu swiftshader_indirect -no-snapshot-load -dns-server 192.168.1.7
