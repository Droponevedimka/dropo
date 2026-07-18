#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
json_version=$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/p' "$repo_root/version.json" | head -n 1)
pubspec_version=$(sed -n 's/^version:[[:space:]]*\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)\(+[0-9][0-9]*\)\{0,1\}[[:space:]]*$/\1/p' "$repo_root/flutter_app/pubspec.yaml" | head -n 1)

if [[ -z "$json_version" || -z "$pubspec_version" ]]; then
  printf 'Could not parse version.json or flutter_app/pubspec.yaml\n' >&2
  exit 1
fi
if [[ "$json_version" != "$pubspec_version" ]]; then
  printf 'Version mismatch: version.json=%s, pubspec.yaml=%s\n' "$json_version" "$pubspec_version" >&2
  exit 1
fi

printf 'Version synchronization passed: %s\n' "$json_version"
