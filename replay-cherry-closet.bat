@echo off
setlocal

set "SERIAL=emulator-5554"
set "PACKAGE=jp.naver.lineplay.android"
set "PREF=/data/data/jp.naver.lineplay.android/shared_prefs/Cocos2dxPrefsFile.xml"
set "BACKUP=/data/local/tmp/Cocos2dxPrefsFile.xml.before-closet-replay.bak"
set "EDITED=/data/local/tmp/Cocos2dxPrefsFile.xml.closet-replay.new"
set "ADB=%LOCALAPPDATA%\Android\Sdk\platform-tools\adb.exe"
if not exist "%ADB%" set "ADB=adb"

"%ADB%" -s "%SERIAL%" get-state >nul 2>&1
if errorlevel 1 (
  echo Emulator %SERIAL% is not connected. Start it first.
  goto failed
)

for /f "delims=" %%P in ('"%ADB%" -s "%SERIAL%" shell pidof %PACKAGE% 2^>nul') do (
  echo LINE PLAY is running with PID %%P. Close it completely, then run this again.
  goto failed
)

"%ADB%" -s "%SERIAL%" shell "su 0 grep -F EXPERIENCE_OF_APP %PREF% | grep -F false" >nul 2>&1
if not errorlevel 1 (
  echo EXPERIENCE_OF_APP is already false. Start LINE PLAY to replay Cherry's closet.
  goto success
)

"%ADB%" -s "%SERIAL%" shell "su 0 grep -F EXPERIENCE_OF_APP %PREF% | grep -F true" >nul 2>&1
if errorlevel 1 (
  echo Could not find EXPERIENCE_OF_APP=true in %PREF%. No changes made.
  goto failed
)

"%ADB%" -s "%SERIAL%" shell su 0 cp -p "%PREF%" "%BACKUP%"
if errorlevel 1 (
  echo Could not back up the preferences. No changes made.
  goto failed
)

"%ADB%" -s "%SERIAL%" shell "su 0 sh -c 'sed /EXPERIENCE_OF_APP/s/true/false/ %PREF% > %EDITED%'"
if errorlevel 1 (
  echo Could not prepare the preference edit. Original file is unchanged.
  goto failed
)

"%ADB%" -s "%SERIAL%" shell "su 0 grep -F EXPERIENCE_OF_APP %EDITED% | grep -F false" >nul 2>&1
if errorlevel 1 (
  echo The edited copy did not verify. Original file is unchanged.
  goto failed
)

"%ADB%" -s "%SERIAL%" shell "su 0 sh -c 'cat %EDITED% > %PREF%'"
if errorlevel 1 (
  echo Could not write the preference. Restore backup if needed: %BACKUP%
  goto failed
)

"%ADB%" -s "%SERIAL%" shell "su 0 grep -F EXPERIENCE_OF_APP %PREF% | grep -F false" >nul 2>&1
if errorlevel 1 (
  echo Final verification failed. Restore backup if needed: %BACKUP%
  goto failed
)

"%ADB%" -s "%SERIAL%" shell su 0 rm -f "%EDITED%"
echo EXPERIENCE_OF_APP is false. Backup: %BACKUP%
echo Start LINE PLAY yourself to replay Cherry's closet.
goto success

:failed
pause
exit /b 1

:success
pause
exit /b 0
