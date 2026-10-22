@echo off
set "EMULATOR=%LOCALAPPDATA%\Android\Sdk\emulator\emulator.exe"
if not exist "%EMULATOR%" (
  echo emulator.exe not found: "%EMULATOR%"
  exit /b 1
)
echo Starting AVD Jerry. Requires cherry.exe running; host DNS 192.168.1.11 must be this machine.
start "Jerry AVD" "%EMULATOR%" -avd Jerry -netdelay none -netspeed full -gpu swiftshader_indirect -no-snapshot-load -dns-server 192.168.1.11
