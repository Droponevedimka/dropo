# Scripts

`filters/update-blocked-lists.ps1` checks Re-filter on every build, stores the
normalized source catalogs and compiled rule-sets in `dependencies/filters`,
and supports `-CheckOnly` for CI/release gates. The application itself never
downloads these catalogs during startup.

- `build/build.ps1` — Windows and Android build orchestration.
- `release/bump-version.ps1` — synchronized application version update.
- `signing/certificate/` — public pet-certificate helpers bundled with Windows builds.

Diagnostics and release validation remain in `tools/`.
