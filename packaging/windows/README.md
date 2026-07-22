# Windows packages

`dropo.iss` builds the offline x64 installer with Inno Setup 6. The installer
copies the same immutable application tree used by the portable ZIP, writes the
installed-mode marker, and optionally configures UI autostart and an elevated
per-user background-core task.

The task is deliberately not a LocalSystem service: the core owns user VPN
settings and its authenticated localhost bridge. Running it as SYSTEM would mix
security principals and user profiles.

Install Inno Setup 6 before a local release build:

```powershell
winget install --id JRSoftware.InnoSetup -e
```

When `winget` is unavailable, download the current signed installer from
<https://jrsoftware.org/isdl.php> and install it for the current user.
