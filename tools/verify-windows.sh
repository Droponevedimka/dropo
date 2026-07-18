#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
package_mode="${1:-}"

if [[ -n "$package_mode" && "$package_mode" != "--package" ]]; then
  echo "Usage: bash ./tools/verify-windows.sh [--package]" >&2
  exit 2
fi

export GOCACHE="$repo_root/.tmp/go-build-cache"
export GOMODCACHE="$repo_root/.tmp/go-mod-cache"
export PUB_CACHE="$repo_root/.tmp/pub-cache"
mkdir -p "$GOCACHE" "$GOMODCACHE" "$PUB_CACHE"

cd "$repo_root"
bash ./tools/check-version-sync.sh

go_modules=(
  "./app"
  "./launcher"
  "./bootstrap"
)

for module in "${go_modules[@]}"; do
  echo "[verify] go test: $module"
  (cd "$module" && go test ./...)
  echo "[verify] go vet: $module"
  (cd "$module" && go vet ./...)
done

echo "[verify] Flutter dependencies"
(cd flutter_app && flutter pub get)
echo "[verify] Flutter analyzer"
(cd flutter_app && flutter analyze)
echo "[verify] Flutter tests"
(cd flutter_app && flutter test)
echo "[verify] Flutter Windows release build"
(cd flutter_app && flutter build windows --release)

if [[ "$package_mode" != "--package" ]]; then
  echo "[verify] Component verification completed"
  exit 0
fi

# Package in a detached worktree so the repository's existing release output
# and the developer's working tree are not overwritten by a verification run.
worktree="$repo_root/.tmp/windows-package-worktree"
if [[ -e "$worktree" ]]; then
  echo "Refusing to reuse existing verification worktree: $worktree" >&2
  exit 1
fi

cleanup() {
  git -C "$repo_root" worktree remove --force "$worktree" >/dev/null 2>&1 || true
  # Git can leave ignored build caches behind after removing worktree metadata.
  # The exact-path guard keeps this recursive cleanup scoped to our own temp dir.
  if [[ "$worktree" == "$repo_root/.tmp/windows-package-worktree" ]]; then
    rm -rf -- "$worktree"
  fi
}
trap cleanup EXIT

git worktree add --detach "$worktree" HEAD

# Overlay tracked and untracked working-tree changes onto the detached copy so
# the signed package verifies exactly the code being reviewed.
while IFS= read -r status_line; do
  path="${status_line:3}"
  if [[ "$path" == *" -> "* ]]; then
    path="${path##* -> }"
  fi
  if [[ "${status_line:0:2}" == *D* ]]; then
    rm -f "$worktree/$path"
    continue
  fi
  mkdir -p "$(dirname "$worktree/$path")"
  cp -a "$repo_root/$path" "$worktree/$path"
done < <(git status --porcelain=v1 --untracked-files=all)

export GOCACHE="$worktree/.tmp/go-build-cache"
export GOMODCACHE="$worktree/.tmp/go-mod-cache"
export PUB_CACHE="$worktree/.tmp/pub-cache"
mkdir -p "$GOCACHE" "$GOMODCACHE" "$PUB_CACHE"

echo "[verify] Building Authenticode-signed Windows package"
(cd "$worktree" && powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/build/build.ps1 \
  -Build -AppOnly -AllowUntrustedSelfSignedWindows -SkipWindowsTimestamp)

package_exe="$(find "$worktree/release" -type f -name 'dropo-Windows-x64.exe' -print -quit)"
if [[ -z "$package_exe" ]]; then
  echo "Signed Windows single-file package was not produced" >&2
  exit 1
fi
powershell.exe -NoProfile -Command "if ((Get-AuthenticodeSignature -LiteralPath '$package_exe').Status -eq 'NotSigned') { exit 1 }"
echo "[verify] Signed package verification completed: $(basename "$package_exe")"
