# 07 — FeedFlow Integration and CI Swap

Happens after the CLI reaches M4 (see [TODO.md](TODO.md)). All changes below land in the [feed-flow repo](https://github.com/prof18/feed-flow) (local checkout path in `plan/LOCAL.md`).

## 1. Seed the metadata dir

Run `pcenter listing pull --dir assets/storecopy/microsoft-store` and commit the result. `assets/storecopy/microsoft-store-release-notes.json` stays where it is, unchanged, as the release-notes source.

## 2. Validation run (before touching CI)

From the maintainer's machine (or a manual `workflow_dispatch`):

```
pcenter publish msix --path <built msix> --skip-commit \
  --release-notes assets/storecopy/microsoft-store-release-notes.json
```

against the real Store. Inspect the draft in Partner Center (package attached, release notes on all 25 locales, 90% rollout configured). Then `pcenter submission delete-draft --yes` — or commit it if a release is actually due.

## 3. Swap `windows-release.yml`

Replace the `publish-msix-to-store.ps1` invocation with:

1. Download the pinned `pcenter` release (windows/amd64 zip from GitHub Releases) and verify its sha256. Not Homebrew — this runner is Windows, and the tap covers macOS and Linux only. `brew install prof18/tap/pcenter` is the channel for running these commands by hand.
2. Run:
   ```
   pcenter publish msix --path <msix> --rollout-percentage 90 \
     --release-notes assets/storecopy/microsoft-store-release-notes.json \
     --replace-pending
   ```
   Credentials flow in as `MS_STORE_*` env vars from the existing GitHub secrets — no more parameter plumbing.

Update `release.yml` only if the secret forwarding into the reusable workflow changes shape.

**Operational caveat**: `--replace-pending` in CI will delete *any* uncommitted draft — including a `listing push --skip-commit` or `publish --skip-commit` draft sitting in Partner Center awaiting inspection. Only one pending submission can exist, so a CI release and an in-flight local draft are mutually exclusive: commit or delete local drafts before tagging a release.

## 4. Retire the PowerShell scripts

After the **first green real release** through the CLI:

- Delete `.github/scripts/publish-msix-to-store.ps1`, `store-submission-common.ps1`, `commit-store-submission.ps1`.
- Delete `.scripts/list-microsoft-store-locales.sh` (superseded by `pcenter locales list`).
- Update anything referencing them (workflows, docs, the maintainer's planning notes).

## 5. Document for agents

Add a short `pcenter` section to feed-flow's `CLAUDE.md`: how to run `listing pull/push`, `reviews list`, and the rollout-rescue commands (`rollout finalize` etc.), including the pinned version and the env-file convention.
