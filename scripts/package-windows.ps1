# Builds the Windows desktop app and packages it: dist\medialib-desktop.exe, a portable zip and a setup installer.
#   powershell -File scripts\package-windows.ps1 [-Version 3.4.0] [-Arch amd64|arm64]
# Needs Go. The installer also needs Inno Setup 6 (iscc); without it only the exe and zip are made. A -Version with a
# version number in it is also stamped into resource_windows_<arch>.syso (git checkout it afterwards to undo that).
param([string]$Version = "", [string]$Arch = "amd64")
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
if (-not $Version) { $Version = (git describe --tags --always --dirty 2>$null); if (-not $Version) { $Version = "dev" } }
$num = if ($Version -match '(\d+\.\d+\.\d+)') { $Matches[1] } else { (Get-Content -Raw VERSION).Trim() }
New-Item -ItemType Directory -Force dist | Out-Null
if ($Version -match '(\d+)\.(\d+)\.(\d+)') {
  # Stamp the version into the exe's Properties > Details (as `make winres` does), before GOOS/GOARCH point go run elsewhere.
  $ma, $mi, $pa = $Matches[1], $Matches[2], $Matches[3]
  $armFlag = @(if ($Arch -eq "arm64") { "-arm" })  # always an array: splatting a bare string passes its characters
  Push-Location cmd\medialib
  go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo -64 @armFlag "-ver-major=$ma" "-ver-minor=$mi" "-ver-patch=$pa" `
    "-product-ver-major=$ma" "-product-ver-minor=$mi" "-product-ver-patch=$pa" "-file-version=$num" "-product-version=$num" `
    -o "resource_windows_$Arch.syso" winres\versioninfo.json
  $failed = $LASTEXITCODE
  Pop-Location
  if ($failed) { throw "goversioninfo failed" }
  Write-Host "version resource: $num"
}
$env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = $Arch
$exe = "dist\medialib-desktop-windows-$Arch.exe"
go build -tags desktop -trimpath -ldflags "-s -w -H windowsgui -X github.com/demogest/medialib/internal/version.Version=$Version" -o $exe ./cmd/medialib
if ($LASTEXITCODE) { throw "go build failed" }
Write-Host "built $exe"
$zip = "dist\medialib-desktop-windows-$Arch.zip"
if (Test-Path $zip) { Remove-Item $zip }
Compress-Archive -Path $exe -DestinationPath $zip
Write-Host "zipped $zip"
$iscc = Get-Command iscc -ErrorAction SilentlyContinue
if (-not $iscc) { foreach ($p in "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe", "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe") { if (Test-Path $p) { $iscc = Get-Item $p; break } } }
if ($iscc) {
  $src = (Resolve-Path $exe).Path
  $isccPath = if ($iscc.Source) { $iscc.Source } else { $iscc.FullName }
  & $isccPath "/DAppVersion=$num" "/DSourceExe=$src" "/DOutName=medialib-setup-windows-$Arch" installer\medialib.iss
  if ($LASTEXITCODE) { throw "iscc failed" }
} else { Write-Host "Inno Setup not found: skipping the installer (winget install JRSoftware.InnoSetup)" }
