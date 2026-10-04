@echo off
rem ==========================================================================
rem  aster-onboard build (double-click friendly) -- pinned to win64 (x86-64).
rem
rem  Single-file GUI exe: no cgo, no external DLL. The target machine is
rem  expected to have the WebView2 Runtime (ships with Win11, and with any
rem  Edge-updated Win10).
rem
rem    build.cmd    regenerate resources, build aster-onboard.exe, run tests
rem
rem  GOARCH is pinned on purpose: a build started on another machine (e.g. an
rem  ARM64 laptop) must not silently produce a non-x64 binary.
rem  Set ASTER_ONBOARD_NOPAUSE=1 to skip the closing pause (for automation).
rem
rem  Requires Go on PATH. If you are behind a restricted network, set GOPROXY
rem  first, e.g.:  set GOPROXY=https://goproxy.cn,direct
rem ==========================================================================
setlocal
chcp 65001 >nul
set "GOOS=windows"
set "GOARCH=amd64"
cd /d "%~dp0"

rem rsrc_windows_amd64.syso = app.manifest (DPI awareness + comctl v6) + app.ico +
rem versioninfo.json (VS_VERSIONINFO). Regenerate it whenever the manifest, the icon
rem or the version info changes. The file name MUST keep the *_windows_amd64.syso
rem suffix so the darwin build never links a Windows COFF object.
rem
rem goversioninfo is used instead of rsrc because all three resources must land in
rem ONE .rsrc section: two .syso files side by side make the linker fail with
rem "too many .rsrc sections", so the version info cannot be a separate object.
rem
rem Two goversioninfo gotchas, both learned the hard way:
rem   * the CLI only understands -manifest; -ico/-icon/-o are NOT flags of the cmd
rem     (they exist on the library) and are silently ignored. Icon and manifest
rem     paths go in versioninfo.json as IconPath / ManifestPath instead.
rem   * -o is ignored too, so the output is always ./resource.syso -- rename it by
rem     hand, otherwise the missing *_windows_amd64 suffix breaks the mac build.
rem
rem The Comments/CompanyName/ProductName fields are read by AV heuristic engines;
rem keep them filled in. An unsigned exe with no version info is the worst
rem possible reputation profile for SmartScreen / 360.
go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest versioninfo.json || goto :fail
if not exist resource.syso (echo ERROR: resource.syso was not produced & goto :fail)
move /y resource.syso rsrc_windows_amd64.syso >nul || goto :fail
go build -p 2 -trimpath -ldflags "-H=windowsgui -s -w" -o aster-onboard.exe . || goto :fail
go test ./... || goto :fail

echo.
echo OK - aster-onboard.exe (windows/amd64) built.
if not defined ASTER_ONBOARD_NOPAUSE pause
exit /b 0

:fail
echo.
echo BUILD FAILED - see the errors above.
if not defined ASTER_ONBOARD_NOPAUSE pause
exit /b 1
