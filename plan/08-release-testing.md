# 08 — Release Testing

How the release pipeline gets proven before `v0.0.1` is cut. The release workflow is the
one workflow a pull request cannot exercise, and a pushed tag is awkward to take back — so
as much as possible is checked before any tag exists.

Distribution design is in [02-architecture.md](02-architecture.md) §Distribution.

## Already verified locally (2026-08-03, before the repo existed)

Re-runnable any time with `script/rehearse-release.sh [vX.Y.Z]`, which runs every step of
`release.yml` except `gh release create`:

- All five targets cross-compile; `.tar.gz` for darwin/linux, `.zip` for windows; `checksums.txt` covers all five.
- `-X main.version` reaches the binary — the artifact reports the tag, not `dev`. This is the assertion that catches a renamed symbol, which `-X` would otherwise ignore in silence.
- The three credential-free smoke assertions hold: version stamp, `listing push` mode enforcement (exit 2), `store.json` identity guard (exit 1, decided before any request).
- `CHANGELOG.md` extraction yields the `## 0.0.1` section; a missing section stops the release.

The tap side, verified against the rehearsal's own archives:

- The tap's existing `script/update-formula.py` handles a `pcenter` formula **unmodified** — 4 platforms rewritten, checksums matching the build, the windows `.zip` correctly ignored (its regex only matches `.tar.gz`), and a second run is a no-op.
- The formula installs and `brew test` passes all three assertions, exercised through a real `brew tap` / `brew install` / `brew test` against `file://` URLs.

The validated formula draft is ready to drop into `prof18/homebrew-tap` as `Formula/pcenter.rb`.

## What only a real release can prove

In order. Everything here needs the repo pushed (TODO M5).

1. **CI matrix** — `ci.yml` green on ubuntu/macos/windows. Deferred from M1; it has never run.
2. **The release workflow itself** — `gh release create` uploading five archives plus `checksums.txt` under the expected names, with the changelog section rendering as the release notes.
3. **The windows zip actually runs on Windows.** Rehearsal cross-compiled it but never executed it, and nothing else covers this: the CI matrix runs `go test`, not the release artifact. feed-flow's release CI is the only consumer of this archive, so a broken one is found at the worst moment. See "Open question" below.
4. **The tap updater end to end** — `update-pcenter.yml` finding the release, rewriting the formula, and passing its own gate (Ruby parse → every URL 200 → `brew install` → `brew test`) before pushing.
5. **A clean-machine install** — `brew install prof18/tap/pcenter` then `pcenter version` reporting the tagged version.

## `v0.0.1` instead of a release candidate

Superseded on 2026-08-07 by maintainer direction: the first tag is **`v0.0.1`**, a real
release rather than an RC, so it can be consumed by feed-flow CI and the tap exactly as a
later version would be. The version number carries the "this is a preview" message that a
`-rc` suffix would have carried, without any of the RC machinery.

This is strictly simpler than the RC plan it replaces:

- No temporary changelog section and no delete-the-tag-afterwards step — `## 0.0.1` is a
  permanent section like any other.
- No pre-release flag, which **settles the first open question by construction**: the tap
  updater's `gh release view --json tagName` sees a normal release, so there is nothing to
  find out about how it treats pre-releases.
- Items 1–5 above are walked once, against `v0.0.1`. Anything they turn up is fixed and
  released as `v0.0.2`, which is what a two-digit-zero version is for.

## Open question

- **Where should the windows artifact be smoke-tested?** Either add a second `runs-on: windows-latest` job to `release.yml` that downloads the zip from the just-created release and runs the same three assertions, or check it by hand once. The first is worth it if feed-flow's CI is going to depend on this archive every release — and with `v0.0.1` going straight into that CI, the by-hand check happens there either way.
