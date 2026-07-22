# Code signing policy

The canonical source and release location is
[`Droponevedimka/dropo`](https://github.com/Droponevedimka/dropo). Release
artifacts are produced by the versioned build scripts in this repository and
must pass the repository's manifest, SHA-256, clean-Windows and Microsoft
Defender gates before publication.

Windows releases remain unsigned unless a publicly trusted identity is
available. The project never asks users to install a private root certificate
and never presents a self-signed executable as a trusted public release.

The project is applying for the open-source program whose required attribution
is: **Free code signing provided by [SignPath.io](https://signpath.io/),
certificate by [SignPath Foundation](https://signpath.org/).** This statement
describes the intended signing provider; a release is signed by that provider
only when Windows shows a valid SignPath Foundation Authenticode signature.

## Roles and controls

- author/committer and reviewer: [Droponevedimka](https://github.com/Droponevedimka);
- signing approver: [Droponevedimka](https://github.com/Droponevedimka);
- changes from external contributors require maintainer review;
- every signing request requires manual approval;
- source repository and signing accounts must use multi-factor authentication;
- upstream binaries may be included and described by the SBOM, but are never
  re-signed as if they were authored by dropo.

See the [privacy policy](PRIVACY.md), [MIT license](LICENSE), release SHA-256
files, SPDX SBOM and build provenance included with each release.
