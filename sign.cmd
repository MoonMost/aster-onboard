@echo off
rem ==========================================================================
rem  aster-onboard code signing (Authenticode) -- run AFTER build.cmd.
rem
rem  Version info alone (versioninfo.json) only lowers the false-positive rate;
rem  a real signature is what actually stops SmartScreen / 360 from warning.
rem
rem  You need a certificate first. Options:
rem
rem    A) OV code signing certificate (paid, ~CNY 2000/yr, needs a registered
rem       company or sole proprietorship). Import it into the Windows cert
rem       store, then pass its thumbprint:
rem
rem         set ASTER_SIGN_SHA1=<40-hex thumbprint>
rem         sign.cmd
rem
rem    B) Azure Trusted Signing (cloud, ~USD 10/mo, individuals allowed, but
rem       the private key stays in Azure so it has to run in CI). This local
rem       script does not cover that option -- see the README.
rem
rem  Usage:
rem    sign.cmd                       sign aster-onboard.exe
rem    sign.cmd path\to\other.exe     sign a specific file
rem
rem  Set ASTER_ONBOARD_NOPAUSE=1 to skip the closing pause (for automation).
rem
rem  ASCII only, CRLF only -- Chinese text or bare LF both make cmd.exe split
rem  lines and run fragments as commands (burned once already).
rem ==========================================================================
setlocal EnableDelayedExpansion
cd /d "%~dp0"

set "TARGET=%~1"
if "%TARGET%"=="" set "TARGET=aster-onboard.exe"
if not exist "%TARGET%" (echo ERROR: %TARGET% not found - run build.cmd first. & goto :fail)

rem --- locate signtool from the Windows SDK ---
rem Delayed expansion (!VAR!) is required here: %ProgramFiles(x86)% contains a
rem ")" that would otherwise close the for-block early and cmd would abort with
rem "\Windows was unexpected at this time".
set "PF86=%ProgramFiles(x86)%"
set "PF64=%ProgramFiles%"
set "SIGNTOOL="
for %%R in ("!PF86!" "!PF64!") do (
    if not defined SIGNTOOL (
        for /d %%v in ("%%~fR\Windows Kits\10\bin\10.*") do (
            if exist "%%~fv\x64\signtool.exe" set "SIGNTOOL=%%~fv\x64\signtool.exe"
        )
    )
)
if not defined SIGNTOOL (
    echo ERROR: signtool.exe not found.
    echo   Install the Windows SDK "Signing Tools" component, or point
    echo   ASTER_SIGNTOOL at an existing signtool.exe.
    goto :fail
)
if defined ASTER_SIGNTOOL set "SIGNTOOL=!ASTER_SIGNTOOL!"
echo using: !SIGNTOOL!

rem --- pick the signing credential ---
if not "%ASTER_SIGN_SHA1%"=="" goto :sign_sha1
echo ERROR: no certificate configured.
echo.
echo   Set the certificate thumbprint, then re-run:
echo     set ASTER_SIGN_SHA1=^<40-hex thumbprint from certmgr.msc^>
echo.
echo   Find the thumbprint with:
echo     powershell -c "Get-ChildItem Cert:\CurrentUser\My ^| Select Subject,Thumbprint"
echo.
echo   No certificate yet? A signature is the only real fix for the 360 /
echo   SmartScreen false positive. See the README section on version info
echo   and AV false positives for the two options (OV cert vs Azure Trusted
echo   Signing).
goto :fail

:sign_sha1
rem /fd SHA256 = file digest; /tr + /td = RFC3161 timestamp, so the signature
rem stays valid after the certificate itself expires.
"!SIGNTOOL!" sign /sha1 "%ASTER_SIGN_SHA1%" /fd SHA256 /td SHA256 /tr http://timestamp.digicert.com "%TARGET%" || goto :fail
"!SIGNTOOL!" verify /pa /v "%TARGET%" || goto :fail

echo.
echo OK - signed: %TARGET%
powershell -NoProfile -Command "(Get-AuthenticodeSignature '%TARGET%') | Select-Object Status,SignerCertificate | Format-List"
if not defined ASTER_ONBOARD_NOPAUSE pause
exit /b 0

:fail
echo.
echo SIGNING FAILED - see the errors above.
if not defined ASTER_ONBOARD_NOPAUSE pause
exit /b 1
