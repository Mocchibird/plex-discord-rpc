@echo off
setlocal

set "DEST=%localappdata%\Plex"
set "DEST_SCRIPTS=%DEST%\scripts"
set "SCRIPT_OPTS=%DEST%\script-opts"
set "SOURCE_SCRIPTS=%~dp0scripts"
set "SOURCE_SCRIPT_OPTS=%~dp0script-opts"
set "SOURCE_BINARY=%~dp0scripts\plex-discord-rpc.exe"

if not exist "%SOURCE_SCRIPTS%" set "SOURCE_SCRIPTS=%~dp0..\plugin\scripts"
if not exist "%SOURCE_SCRIPT_OPTS%" set "SOURCE_SCRIPT_OPTS=%~dp0..\plugin\script-opts"
if not exist "%SOURCE_BINARY%" set "SOURCE_BINARY=%~dp0..\build\windows\scripts\plex-discord-rpc.exe"

if not exist "%DEST%" mkdir "%DEST%"
if not exist "%DEST_SCRIPTS%" mkdir "%DEST_SCRIPTS%"
if not exist "%SCRIPT_OPTS%" mkdir "%SCRIPT_OPTS%"

if not exist "%SOURCE_SCRIPTS%" (
	echo Could not find scripts source folder.
	echo Checked:
	echo   %~dp0scripts
	echo   %~dp0..\plugin\scripts
	exit /b 1
)

if not exist "%SOURCE_SCRIPT_OPTS%" (
	echo Could not find script-opts source folder.
	echo Checked:
	echo   %~dp0script-opts
	echo   %~dp0..\plugin\script-opts
	exit /b 1
)

if not exist "%SOURCE_BINARY%" (
	echo Could not find plex-discord-rpc.exe.
	echo Checked:
	echo   %~dp0scripts\plex-discord-rpc.exe
	echo   %~dp0..\build\windows\scripts\plex-discord-rpc.exe
	echo Build the Windows binary first or use the packaged release bundle.
	exit /b 1
)

echo Copying files...
xcopy /E /Y /I "%SOURCE_SCRIPTS%\*" "%DEST_SCRIPTS%\"
copy /Y "%SOURCE_BINARY%" "%DEST_SCRIPTS%\plex-discord-rpc.exe"
xcopy /E /Y /I "%SOURCE_SCRIPT_OPTS%\*" "%SCRIPT_OPTS%\"

echo Done. Files installed to %DEST%
pause
