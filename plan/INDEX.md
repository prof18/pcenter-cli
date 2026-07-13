# `pcenter` Implementation Plan — Index

Single entrypoint for implementing `pcenter`, the Microsoft Partner Center CLI for Microsoft Store apps. Plan approved 2026-07-09 (renamed from `wstore` 2026-07-10); decisions here are final unless a doc marks them "verify".

## How to work

1. Read the docs below **in order** before writing code — they are short and each one is load-bearing.
2. Work milestone by milestone from [TODO.md](TODO.md). Check off tasks as you complete them; do not start a milestone before the previous one's acceptance criteria pass.
3. TDD is mandatory: the fake Partner Center server ([06-testing.md](06-testing.md)) is built in M1 and everything is test-driven through it. No real sleeping in tests.
4. Never run mutating commands against the live Store unless a TODO task explicitly says so (always via `--skip-commit` drafts first).
5. The repo is **local-only**: do not create a GitHub repo or push — the maintainer publishes it himself at the start of M5. Commit locally as you go, and keep every commit public-ready (no secrets, no machine paths).

## Reading order

| Doc | Contents |
| --- | --- |
| [01-context-and-constraints.md](01-context-and-constraints.md) | Goal, prior art to port, credentials, hard API facts (504 semantics, one-pending-submission, no metadata-only channel) |
| [02-architecture.md](02-architecture.md) | Locked tech decisions, repo layout, config & auth resolution, output conventions, guardrails, distribution |
| [03-store-api-client.md](03-store-api-client.md) | API client spec: exponential backoff + jitter policy, flow-level verify loops, auth resilience, submission PUT body construction, redaction rules — **the resilience heart of the CLI** |
| [04-commands.md](04-commands.md) | Full command tree with flags and per-command behavior |
| [05-metadata-directory.md](05-metadata-directory.md) | File-based listing/screenshot metadata format and pull/push diff semantics |
| [06-testing.md](06-testing.md) | Fakestore design, test layers, parity tests, live smoke rules |
| [07-feedflow-integration.md](07-feedflow-integration.md) | How feed-flow adopts the CLI and the CI swap that retires the PowerShell scripts |
| [TODO.md](TODO.md) | Milestones M1–M5 with tasks, acceptance criteria, and open items to verify against the live API |

## Feature coverage map (original goals → commands)

| Goal | Command(s) |
| --- | --- |
| Publish MSIX / create release (as the CI workflow does today) | `publish msix` (+ `submission commit` for two-phase) |
| Get reviews | `reviews list` |
| Get current languages | `locales list` |
| Update changelog | `publish msix --release-notes` at release time; standalone: `listing push --release-notes <json> --yes` with an otherwise-unchanged metadata dir |
| Get listing | `listing pull` |
| Update listing | `listing push` |
| Update screenshots | edit `images/` + `listing push` |
| Rescue a stuck rollout (2026-07-08 class of failure) | `rollout status / finalize / set-percentage / halt` |
| Inspect/fix stuck submissions | `submission status / get / watch / delete-draft / commit` |

## Key source material (outside this repo)

In the [feed-flow repo](https://github.com/prof18/feed-flow):

- `.github/scripts/publish-msix-to-store.ps1` — the battle-tested publish flow being ported.
- `.github/scripts/store-submission-common.ps1` — auth, retry classification, commit polling.
- `.github/scripts/commit-store-submission.ps1` — commit of a prepared draft.
- `assets/storecopy/microsoft-store-release-notes.json` — release-notes contract.

Machine-specific paths (local checkouts, the credentials env file, planning history) live in `plan/LOCAL.md`, which is gitignored — check it when working on the maintainer's machine; never commit it or print credentials.

## External references

- Submission API overview & resources: https://learn.microsoft.com/en-us/windows/uwp/monetize/manage-app-submissions
- Create/commit/update submission flow: https://learn.microsoft.com/en-us/windows/uwp/monetize/create-an-app-submission
- Rollout methods: https://learn.microsoft.com/en-us/windows/uwp/monetize/get-package-rollout-info-for-an-app-submission and https://learn.microsoft.com/en-us/windows/uwp/monetize/update-the-package-rollout-percentage-for-an-app-submission (halt/finalize pages are siblings)
- Reviews: https://learn.microsoft.com/en-us/windows/uwp/monetize/get-app-reviews
- Screenshot/image requirements: https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/screenshots-and-images?pivots=store-installer-msix
- StoreBroker (prior art for payload/zip semantics): https://github.com/microsoft/StoreBroker/blob/master/Documentation/USAGE.md
- msstore-cli (official CLI; prior art for blob upload via Azure SDK, SAS `+` quirk, ZIP image layout; also the emergency fallback tool — see doc 01): https://github.com/microsoft/msstore-cli
- CLI shape inspiration: https://github.com/rorkai/App-Store-Connect-CLI
