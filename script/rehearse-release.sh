#!/usr/bin/env bash
#
# Run .github/workflows/release.yml locally — every step except `gh release create`.
#
#     script/rehearse-release.sh [vX.Y.Z]
#
# The point is to find a broken release before a tag makes it public. A tag is
# awkward to take back once it has been pushed, and the release workflow is the
# one workflow that cannot be exercised by a pull request.
#
# Differences from the real thing, both deliberate:
#   - smoke-tests the host's own platform, since this machine cannot execute the
#     linux/amd64 binary the workflow tests. Same source, same build flags.
#   - leaves the archives in a temp dir instead of uploading them.
#
# Keep the build and smoke-test steps here in step with the workflow's. They are
# duplicated rather than shared because a workflow that sources a script from
# the repository it is releasing can be changed by the thing it is meant to
# check.
set -euo pipefail

cd "$(dirname "$0")/.."
VERSION="${1:-v0.0.0-rehearsal}"
COMMIT="$(git rev-parse HEAD)"
WORK="$(mktemp -d)"
trap 'echo; echo "artifacts: $WORK/dist"' EXIT

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64) HOST_TARGET=darwin_arm64 ;;
  Darwin/x86_64) HOST_TARGET=darwin_amd64 ;;
  Linux/aarch64) HOST_TARGET=linux_arm64 ;;
  Linux/x86_64) HOST_TARGET=linux_amd64 ;;
  *) echo "unsupported host $(uname -s)/$(uname -m)"; exit 1 ;;
esac

echo "=== build every platform ==="
mkdir -p "$WORK/dist"
built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
flags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${built_at}"

for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  echo "building ${os}/${arch}"
  binary="pcenter"
  [ "$os" = "windows" ] && binary="pcenter.exe"

  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$flags" -o "$WORK/dist/${binary}" ./cmd/pcenter

  if [ "$os" = "windows" ]; then
    (cd "$WORK/dist" && zip -q "pcenter_${VERSION}_${os}_${arch}.zip" "$binary")
  else
    tar -czf "$WORK/dist/pcenter_${VERSION}_${os}_${arch}.tar.gz" -C "$WORK/dist" "$binary"
  fi
  rm "$WORK/dist/${binary}"
done

(cd "$WORK/dist" && shasum -a 256 *.tar.gz *.zip > checksums.txt && cat checksums.txt)

echo
echo "=== smoke-test the artifact (${HOST_TARGET}) ==="
mkdir -p "$WORK/bin"
tar -xzf "$WORK/dist/pcenter_${VERSION}_${HOST_TARGET}.tar.gz" -C "$WORK/bin"
export PATH="$WORK/bin:$PATH"

got="$(pcenter version --output json | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')"
echo "reports: $got"
[ "$got" = "$VERSION" ] || { echo "FAIL: version mismatch, want $VERSION"; exit 1; }

export MS_STORE_APP_ID=000000000000 MS_STORE_CLIENT_ID=ci \
       MS_STORE_CLIENT_SECRET=ci MS_STORE_TENANT_ID=ci
export PCENTER_API_BASE=http://127.0.0.1:9 PCENTER_LOGIN_BASE=http://127.0.0.1:9

set +e
pcenter listing push --dir "$WORK" --dry-run --yes 2>"$WORK/err"; code=$?
set -e
cat "$WORK/err"
[ "$code" = "2" ] || { echo "FAIL: want usage exit 2, got $code"; exit 1; }

set +e
pcenter listing push --dir "$WORK" --dry-run 2>"$WORK/err"; code=$?
set -e
cat "$WORK/err"
[ "$code" = "1" ] || { echo "FAIL: want failure exit 1, got $code"; exit 1; }
grep -q "store.json" "$WORK/err" || { echo "FAIL: expected the store.json guard"; exit 1; }

echo
echo "=== take the notes from the changelog ==="
section="${VERSION#v}"
awk -v want="## $section" '
  $0 == want { found = 1; next }
  found && /^## / { exit }
  found { print }
' CHANGELOG.md > "$WORK/notes.md"
if [ ! -s "$WORK/notes.md" ]; then
  echo "NOTE: CHANGELOG.md has no '## $section' section."
  echo "The real release would stop here. Expected when rehearsing an unreleased version."
else
  echo "$(wc -l < "$WORK/notes.md") lines would be published as the release notes"
fi

echo
echo "=== would publish ==="
ls -1 "$WORK/dist"
echo
echo "ALL STEPS PASSED"
