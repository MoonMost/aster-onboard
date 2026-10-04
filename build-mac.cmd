@echo off
rem ==========================================================================
rem  aster-onboard macOS build -- cross-compiled from Windows (pure Go, no cgo).
rem
rem    build-mac.cmd   -> dist\AsterOnboard-mac-arm64.zip (Apple Silicon)
rem                       dist\AsterOnboard-mac-intel.zip  (Intel)
rem
rem  The mac build has no WebView2: the UI runs as a local server + browser
rem  app window (a Chromium --app= window, falling back to the default
rem  browser). A page heartbeat shuts the process down when that window closes.
rem
rem  The artifacts are unsigned / not notarized. macOS refuses the first launch
rem  of an unsigned app, so open it via right-click -> Open, or run once:
rem    xattr -dr com.apple.quarantine /Applications/AsterOnboard.app
rem  (Unix permission bits are already written into the zip, so the binary IS
rem   executable after extraction; only Apple's signature is missing.)
rem
rem  Set ASTER_ONBOARD_NOPAUSE=1 to skip the closing pause.
rem  Requires Go and Python 3 on PATH. Behind a restricted network, set GOPROXY
rem  first, e.g.:  set GOPROXY=https://goproxy.cn,direct
rem ==========================================================================
setlocal
cd /d "%~dp0"

echo == [1/3] darwin/arm64 (Apple Silicon)
set "GOOS=darwin"
set "GOARCH=arm64"
go build -p 2 -trimpath -ldflags "-s -w" -o "dist\mac-arm64\AsterOnboard" . || goto :fail

echo == [2/3] darwin/amd64 (Intel)
set "GOARCH=amd64"
go build -p 2 -trimpath -ldflags "-s -w" -o "dist\mac-intel\AsterOnboard" . || goto :fail

echo == [3/3] assemble .app bundles and zip
python pack-mac.py || goto :fail

echo.
echo DONE: dist\AsterOnboard-mac-arm64.zip / dist\AsterOnboard-mac-intel.zip
if not defined ASTER_ONBOARD_NOPAUSE pause
exit /b 0

:fail
echo.
echo BUILD FAILED - see errors above.
if not defined ASTER_ONBOARD_NOPAUSE pause
exit /b 1
