# Release process

This document is the short operational source of truth for publishing dropo.
The detailed dependency/update architecture remains in `docs/UPDATE.md`.

## Principle

Releases are published from `main` by GitHub Actions. Local machines may build
test artifacts, but the public GitHub release is created by
`.github/workflows/release.yml`.

The release version is manual and comes only from `version.json`:

```json
{
  "version": "2.1.2"
}
```

The workflow creates tag `v<version>` and publishes the Windows app archive. If
that tag already has a release, the workflow exits without overwriting it. To
publish again, bump `version.json`.

## Artifacts

Every app release publishes the small Windows app archive:

| Platform | Asset | Published by |
| --- | --- | --- |
| Windows x64 | `dropo-Windows-Portable-x64.zip` | GitHub Actions via `build.ps1 -AppOnly` |

The heavy engine archive is not rebuilt on every release. It is tracked by
`deps-lock.json` and normally reused from the release tag stored there:

| Platform | Asset | Hosted at |
| --- | --- | --- |
| Windows x64 dependencies | `dropo-Windows-Dependencies-x64.zip` | `deps-lock.json.tag` |

The app archive contains `resources/dependencies.json`; it points to the hosted
dependencies archive and includes its `sha256` and size. On first launch, the
app downloads and verifies dependencies if `bin/` is missing or stale.

## Normal release

1. Make the product changes.
2. Update `version.json`.
3. Run the release gate locally:

```powershell
go test ./...
cd ..\flutter_app
E:\flutter-sdk\flutter\bin\flutter.bat analyze
E:\flutter-sdk\flutter\bin\flutter.bat test
cd ..
.\build.ps1 -AppOnly
git diff --check
```

4. Commit the source, tests, docs, and `version.json`.
5. Push to `main`.
6. GitHub Actions builds from that commit and publishes `v<version>`.
7. Verify the release page contains the download table and
   `dropo-Windows-Portable-x64.zip`.

## Release notes

Release notes are generated in the workflow before publishing and then applied
with `gh release edit` on the GitHub runner. This avoids relying on local `gh`
and keeps the published release description consistent.

The release notes must include:

- a short summary;
- a download table with Windows Portable, Windows Dependencies, and future
  platform rows;
- a concise change list;
- the dependency asset link from `deps-lock.json`.

## Dependency updates

Only update `deps-lock.json` when bundled engine versions actually change.

For dependency changes:

1. Run a full local build with dependencies available:

```powershell
.\build.ps1
```

2. Upload the produced `dropo-Windows-Dependencies-x64.zip` once to the release
   tag recorded in the updated `deps-lock.json`.
3. Commit the updated `deps-lock.json`.
4. Future app-only releases reuse that hosted dependency archive.

## Local fallback

`release.ps1` is a local owner-only fallback. It requires GitHub CLI
authentication (`gh auth login`) and should not be the default path. Prefer the
GitHub Actions flow because it builds and tags from the exact pushed commit.

