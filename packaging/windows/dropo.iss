#ifndef SourceDir
  #error SourceDir is required
#endif
#ifndef OutputDir
  #error OutputDir is required
#endif
#ifndef AppVersion
  #error AppVersion is required
#endif
#ifndef SetupIconFile
  #error SetupIconFile is required
#endif
#ifndef SetupBaseName
  #define SetupBaseName "dropo-Windows-Setup-x64"
#endif

[Setup]
AppId={{D493210B-63F8-4CA8-B97D-FED5B9E6711E}
AppName=dropo
AppVersion={#AppVersion}
AppVerName=dropo {#AppVersion}
AppPublisher=Droponevedimka
AppPublisherURL=https://github.com/Droponevedimka/dropo
AppSupportURL=https://github.com/Droponevedimka/dropo/issues
AppUpdatesURL=https://downloads.droponevedimka.ru/
DefaultDirName={autopf}\dropo
DefaultGroupName=dropo
DisableProgramGroupPage=yes
OutputDir={#OutputDir}
OutputBaseFilename={#SetupBaseName}
SetupIconFile={#SetupIconFile}
UninstallDisplayIcon={app}\dropo.exe
Compression=lzma2/normal
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=commandline
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
CloseApplications=force
CloseApplicationsFilter=dropo.exe,dropo-ui.exe,dropo-core.exe
RestartApplications=yes
SetupLogging=yes
UsePreviousTasks=yes
VersionInfoVersion={#AppVersion}.0
VersionInfoProductName=dropo
VersionInfoProductVersion={#AppVersion}
VersionInfoDescription=dropo Windows installer

[Languages]
Name: "russian"; MessagesFile: "compiler:Languages\Russian.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Создать ярлык на рабочем столе"; GroupDescription: "Ярлыки:"; Flags: unchecked
Name: "autostart"; Description: "Запускать dropo при входе в Windows"; GroupDescription: "Автозапуск:"; Flags: checkedonce
Name: "backgroundcore"; Description: "Заранее запускать защищённый фоновый core (быстрее подключение, без повторного UAC)"; GroupDescription: "Автозапуск:"; Flags: checkedonce

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs notimestamp
Source: "{#SourcePath}\install-mode.json"; DestDir: "{app}"; Flags: ignoreversion notimestamp

[Icons]
Name: "{autoprograms}\dropo"; Filename: "{app}\dropo.exe"; WorkingDir: "{app}"
Name: "{autodesktop}\dropo"; Filename: "{app}\dropo.exe"; WorkingDir: "{app}"; Tasks: desktopicon

[Run]
Filename: "{sys}\schtasks.exe"; Parameters: "/Delete /TN ""dropo-background-core"" /F"; Flags: runhidden waituntilterminated logoutput 64bit; Check: IsWin64
Filename: "{sys}\schtasks.exe"; Parameters: "/Create /F /TN ""dropo-background-core"" /SC ONLOGON /RL HIGHEST /TR ""\""{app}\resources\dropo-core.exe\"" --listen 127.0.0.1:17890 --no-tray"""; Flags: runhidden waituntilterminated logoutput 64bit; Check: ShouldCreateBackgroundTask
Filename: "{app}\dropo.exe"; Description: "Запустить dropo"; WorkingDir: "{app}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM dropo-ui.exe"; Flags: runhidden waituntilterminated; RunOnceId: "StopDropoUI"
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM dropo-core.exe"; Flags: runhidden waituntilterminated; RunOnceId: "StopDropoCore"
Filename: "{sys}\schtasks.exe"; Parameters: "/Delete /TN ""dropo-background-core"" /F"; Flags: runhidden waituntilterminated logoutput 64bit; Check: IsWin64; RunOnceId: "DeleteDropoCoreTask"

[UninstallDelete]
Type: filesandordirs; Name: "{app}\updates"

[Code]
const
  DropoRegistryPath = 'Software\dropo';
  DropoRunRegistryPath = 'Software\Microsoft\Windows\CurrentVersion\Run';

var
  PreserveInstallerChoices: Boolean;
  PreviousBackgroundCoreChoice: Boolean;

function IsUpgradeInstall(): Boolean;
begin
  Result :=
    RegKeyExists(HKLM64, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{D493210B-63F8-4CA8-B97D-FED5B9E6711E}_is1') or
    RegKeyExists(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{D493210B-63F8-4CA8-B97D-FED5B9E6711E}_is1');
end;

function HasTaskSelectionParameter(): Boolean;
var
  ParameterIndex: Integer;
begin
  Result := False;
  for ParameterIndex := 1 to ParamCount do
    if Pos('/tasks=', LowerCase(ParamStr(ParameterIndex))) = 1 then begin
      Result := True;
      exit;
    end;
end;

function InitializeSetup(): Boolean;
var
  StoredChoice: Cardinal;
begin
  PreserveInstallerChoices := IsUpgradeInstall() and WizardSilent() and not HasTaskSelectionParameter();
  if RegQueryDWordValue(HKCU, DropoRegistryPath, 'InstallerBackgroundCoreChoice', StoredChoice) then
    PreviousBackgroundCoreChoice := StoredChoice <> 0
  else
    PreviousBackgroundCoreChoice := FileExists(ExpandConstant('{sys}\Tasks\dropo-background-core'));
  Result := True;
end;

function ShouldCreateBackgroundTask(): Boolean;
begin
  Result := IsWin64 and
    ((PreserveInstallerChoices and PreviousBackgroundCoreChoice) or
     ((not PreserveInstallerChoices) and WizardIsTaskSelected('backgroundcore')));
end;

procedure ConfigureAutoStart();
var
  Enabled: Cardinal;
  Command: String;
begin
  if PreserveInstallerChoices then
    exit;

  if WizardIsTaskSelected('autostart') then begin
    Enabled := 1;
    Command := '"' + ExpandConstant('{app}\dropo.exe') + '" --autostart';
    RegWriteStringValue(HKCU, DropoRunRegistryPath, 'dropo', Command);
  end else begin
    Enabled := 0;
    RegDeleteValue(HKCU, DropoRunRegistryPath, 'dropo');
  end;
  RegWriteDWordValue(HKCU, DropoRegistryPath, 'InstallerAutoStartChoice', Enabled);

  if WizardIsTaskSelected('backgroundcore') then
    Enabled := 1
  else
    Enabled := 0;
  RegWriteDWordValue(HKCU, DropoRegistryPath, 'InstallerBackgroundCoreChoice', Enabled);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    ConfigureAutoStart();
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then begin
    RegDeleteValue(HKCU, DropoRunRegistryPath, 'dropo');
    RegDeleteValue(HKCU, DropoRegistryPath, 'InstallerAutoStartChoice');
    RegDeleteValue(HKCU, DropoRegistryPath, 'InstallerBackgroundCoreChoice');
  end;
end;
