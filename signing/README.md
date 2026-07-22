# Release signing

Private keys and passwords must never be stored in this repository. Only public
certificate material and fingerprints are committed here.

## Local Android release

Gradle reads `%USERPROFILE%\.dropo-signing\android-signing.properties` by
default. The local publisher uses these values to build and verify the APK:

- `DROPO_ANDROID_KEYSTORE_PATH`
- `DROPO_ANDROID_STORE_PASSWORD`
- `DROPO_ANDROID_KEY_ALIAS`
- `DROPO_ANDROID_KEY_PASSWORD`

The expected release certificate SHA-256 is stored in
`android-release-cert.sha256`.

## Local Windows release

Windows builds are left unsigned when no publicly trusted signing identity is
configured. To sign them, use either a certificate already installed in the
Windows certificate store (`DROPO_WINDOWS_CERT_SHA1`) or:

- `DROPO_WINDOWS_PFX_PATH`
- `DROPO_WINDOWS_PFX_PASSWORD`

Use `-RequireWindowsSigning` (or `DROPO_REQUIRE_WINDOWS_SIGNING=1`) in a release
environment that must fail closed. Self-signed certificates are not bundled or
offered to public users.

For an OSI-licensed, fully open-source release, the preferred free option is the
SignPath Foundation program. Until a project is accepted, unsigned artifacts
are safer and less misleading than installing a private root certificate on a
user's machine.

## GitHub release publishing

GitHub Actions creates only the tag and release information page. It does not
receive signing keys or build release artifacts. Build and validate Windows/Android
artifacts locally, then use `tools/publish-release-assets.ps1` to upload them.
The script obtains GitHub authentication from `GH_TOKEN` or the local Git
credential manager; it never reads or writes private signing material in GitHub.
