; Inno Setup script for the Windows desktop app. Build with scripts/package-windows.ps1 (it passes /DAppVersion and /DSourceExe).
#ifndef AppVersion
  #define AppVersion "3.5.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\medialib-desktop.exe"
#endif
#ifndef OutName
  #define OutName "medialib-setup"
#endif

[Setup]
AppId={{6F1C7B52-3A0E-4C57-9E4B-5D2A9C0B7A11}
AppName=Media Library
AppVersion={#AppVersion}
; The setup program's own Properties > Details (Inno Setup leaves its file version at 0.0.0.0 otherwise).
VersionInfoVersion={#AppVersion}
VersionInfoDescription=Media Library Setup
AppPublisher=demogest
AppPublisherURL=https://github.com/demogest/medialib
DefaultDirName={autopf}\Media Library
DefaultGroupName=Media Library
DisableProgramGroupPage=yes
; Per-user by default (no admin prompt); the dialog lets a user choose all users.
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
OutputDir=..\dist
OutputBaseFilename={#OutName}
SetupIconFile=..\cmd\medialib\winres\icon.ico
UninstallDisplayIcon={app}\medialib.exe
Compression=lzma2/ultra
SolidCompression=yes
WizardStyle=modern
ArchitecturesInstallIn64BitMode=x64compatible
CloseApplications=yes
RestartApplications=no
MinVersion=10.0.17763

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; Flags: unchecked
Name: "ffmpeg"; Description: "Install &ffmpeg (needed to make cover images) with winget"; Check: NeedFfmpeg

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "medialib.exe"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Media Library"; Filename: "{app}\medialib.exe"; AppUserModelID: "Demogest.MediaLibrary"
Name: "{autodesktop}\Media Library"; Filename: "{app}\medialib.exe"; AppUserModelID: "Demogest.MediaLibrary"; Tasks: desktopicon

[Run]
Filename: "winget.exe"; Parameters: "install --id Gyan.FFmpeg -e --accept-source-agreements --accept-package-agreements"; StatusMsg: "Installing ffmpeg..."; Flags: runhidden waituntilterminated; Tasks: ffmpeg
Filename: "{app}\medialib.exe"; Description: "Start Media Library"; Flags: nowait postinstall skipifsilent
; An update from inside the app runs this setup silently with /relaunch=yes: the app starts again once it is installed.
Filename: "{app}\medialib.exe"; Flags: nowait; Check: RelaunchAfterUpdate

[Code]
function RelaunchAfterUpdate: Boolean;
begin
  Result := WizardSilent and (CompareText(ExpandConstant('{param:relaunch|no}'), 'yes') = 0);
end;

function NeedFfmpeg: Boolean;
var
  Dummy: Integer;
begin
  Result := not Exec(ExpandConstant('{sys}\cmd.exe'), '/c ffmpeg -version', '', SW_HIDE, ewWaitUntilTerminated, Dummy) or (Dummy <> 0);
end;

function WebView2Present: Boolean;
var
  V: String;
begin
  Result := RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', V)
         or RegQueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', V)
         or RegQueryStringValue(HKCU, 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', V);
end;

function InitializeSetup: Boolean;
begin
  Result := True;
  if not WebView2Present then
    MsgBox('The Microsoft Edge WebView2 Runtime was not found. Media Library will still run, in an Edge or Chrome window, but the native window needs the runtime (included in Windows 11): https://developer.microsoft.com/microsoft-edge/webview2/', mbInformation, MB_OK);
end;
