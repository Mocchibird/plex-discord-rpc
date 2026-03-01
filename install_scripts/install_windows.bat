@echo off
setlocal

set "DEST=%localappdata%\Plex"
set "SCRIPT_OPTS=%DEST%\script-opts"

if not exist "%DEST%" mkdir "%DEST%"
if not exist "%SCRIPT_OPTS%" mkdir "%SCRIPT_OPTS%"

echo Copying files...
xcopy /E /Y /I "%~dp0scripts\*" "%DEST%\scripts\"
xcopy /E /Y /I "%~dp0script-opts\*" "%SCRIPT_OPTS%\"

echo Done. Files installed to %DEST%
pause