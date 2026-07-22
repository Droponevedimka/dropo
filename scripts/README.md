# Scripts

`filters/update-blocked-lists.ps1` checks Re-filter on every build, stores the
normalized source catalogs and compiled rule-sets in `dependencies/filters`,
and supports `-CheckOnly` for CI/release gates. The application itself never
downloads these catalogs during startup.

- `build/build.ps1` — Windows and Android build orchestration.
- `release/bump-version.ps1` — synchronized application version update.
- `../packaging/windows/` — Inno Setup source for installer and portable packaging.

Windows release packages require a clean Git worktree. Their source revision,
timestamps, Go build IDs, ZIP entry order/times and Inno file timestamps are
fixed so CI can compare a second byte-for-byte rebuild. Use
`-AllowDirtySource` only for local development output that will not be published.

Diagnostics and release validation remain in `tools/`.
