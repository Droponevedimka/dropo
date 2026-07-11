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

Production builds require either a certificate already installed in the
Windows certificate store (`DROPO_WINDOWS_CERT_SHA1`) or:

- `DROPO_WINDOWS_PFX_PATH`
- `DROPO_WINDOWS_PFX_PASSWORD`

`-AllowUnsignedWindows` is only for local development and must not be used for
published artifacts.

For this pet project, `dropo-pet-code-signing.cer` is the public self-signed
certificate bundled under `resources/cert/` in the portable app. Its private `.pfx` backup and random
password are stored only in `%USERPROFILE%\.dropo-signing`. Build with the
certificate thumbprint and `-AllowUntrustedSelfSignedWindows`; this permits only
the expected untrusted-root result before the public certificate is installed.

On an interactive launch, the Windows launcher validates the bundled public
certificate against the pinned SHA-1 fingerprint and offers once to install it
into the current user's `Root` and `TrustedPublisher` stores. The launcher never
installs trust silently, and it skips the prompt during autostart. If the user
declines, the app continues and Windows may keep showing an unknown-publisher
warning.

As a manual fallback, users can inspect and run
`resources/cert/install-dropo-pet-certificate.cmd` to trust the certificate for their current
Windows account, and `resources/cert/remove-dropo-pet-certificate.cmd` to remove it later.
The CMD wrappers avoid downloaded-script execution-policy problems. Installing
a self-signed root means trusting every binary signed by the corresponding
private key, so the private `.pfx` must never be distributed.

## GitHub release publishing

GitHub Actions creates only the tag and release information page. It does not
receive signing keys or build release artifacts. Build and validate Windows/Android
artifacts locally, then use `tools/publish-release-assets.ps1` to upload them.
The script obtains GitHub authentication from `GH_TOKEN` or the local Git
credential manager; it never reads or writes private signing material in GitHub.
