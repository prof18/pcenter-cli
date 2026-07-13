# 01 — Context and Constraints

## Goal

Build `pcenter`, a Go CLI for the Microsoft Store (Partner Center) that:

1. Publishes FeedFlow MSIX releases — replacing `.github/scripts/*.ps1` in the feed-flow repo with identical behavior.
2. Manages the store listing from local files: get/update listing text, screenshots, release notes (changelog), list languages.
3. Fetches app reviews.
4. Manages rollouts and submissions (finalize, halt, set percentage, delete drafts) — operable from a developer machine when Partner Center misbehaves (see the 2026-07-08 CI failure: a 504 on `finalizepackagerollout` killed a release that one local command could have unblocked).

It must run **both** on macOS (arm64, the maintainer's machine) and on the Windows GitHub Actions runner used by feed-flow's `windows-release.yml`.

## Credentials and account model

- Auth: Entra client credentials. **Individual developer account** — never assume a business tenant or extra org setup.
- Credentials are four values: `MS_STORE_TENANT_ID`, `MS_STORE_CLIENT_ID`, `MS_STORE_CLIENT_SECRET`, `MS_STORE_APP_ID` — provided as env vars, or via an env file (`KEY=VALUE` lines, kept at 600 perms; see `plan/LOCAL.md` for the maintainer's location). The same four values exist as GitHub secrets in feed-flow.
- `MS_STORE_APP_ID` is the Store **product id** (`9N5T1RFBB6V5` for FeedFlow), not the Entra client id.
- One token serves everything: the submission API and the analytics/reviews API share base URL `https://manage.devcenter.microsoft.com/v1.0/my` and AAD resource `https://manage.devcenter.microsoft.com`.
- Entra client secrets expire (max 24 months); rotation touches the env file and the GitHub secrets. Document this in the README.

## Hard facts about the Store API (shape the whole design)

- **No metadata-only channel.** Every change — listing text, screenshots, release notes, or package — is a full submission: clone the currently-published state (POST create), mutate it (PUT), commit (POST), pass certification.
- **Only one pending submission** can exist at a time per app.
- **Gateway 504s that still succeed.** The API's gateway regularly returns 504 on `finalizepackagerollout`, `commit`, create-submission, and delete-submission while the operation completes server-side. Never blind-retry POST/DELETE; verify state between attempts (see [03-store-api-client.md](03-store-api-client.md)).
- **Bodyless non-GET quirk.** Requests without a body are rejected (`InvalidParameterValue`, "Only JSON content is accepted"); send an empty body with `Content-Type: application/json`. Do NOT send `{}` to create-submission — it gets validated as submission data and rejected ("The size of Listings must be 1 or more").
- **No image download URLs.** Image resources in submission JSON carry only `fileName`, `fileStatus`, `id`, `description`, `imageType`. `listing pull` cannot download screenshot binaries; local files are the binary source of truth.
- FeedFlow currently has **25 listing locales**: `bg, cs, de, de-de, en-us, es, es-es, et, fr, gl, gl-es, he, hu, it, ja, pl, pt-br, ru, sk, ta, tr, uk, vi, zh-cn, zh-hans`. Never hardcode this list — always read locales from the submission JSON.

## Prior art being ported (read the code, not just this summary)

- `publish-msix-to-store.ps1` — full publish flow: finalize previous rollout → pending-draft handling → create submission (with adoption recovery) → release notes application → package list management → ZIP upload → rollout config → commit → cleanup-on-failure. Its **verify-between-attempts logic** is the product of real production failures; port that 1:1. (The wait *schedule* is deliberately upgraded to exponential backoff + jitter — see doc 03.)
- `store-submission-common.ps1` — token acquisition, transient-error classification (408/429/≥500/network), per-method retry defaults, commit verify loop, status polling.
- `commit-store-submission.ps1` — standalone commit of a prepared draft.
- Release-notes contract (`assets/storecopy/microsoft-store-release-notes.json`): top-level `notes` object, one key per Store locale, value is a string or an array of bullet strings; arrays are joined with `\r\n` into `baseListing.releaseNotes`. Every listing locale must have notes (hard fail); unused locales in the file produce warnings.

## Why not existing tools (assessed 2026-07-10 against docs + source)

- **Microsoft's official `msstore` CLI** (preview, .NET 9): covers more than first assumed — publish with rollout % and `--noCommit`, submission status/get/poll/delete, `submission updateMetadata` (inline JSON blob), and `submission rollout get/update/halt/finalize` for regular submissions (in source; missing from its docs page). Still missing for this project: **reviews (zero support)**, stuck-rollout handling inside publish (`PackageRolloutInProgress` appears nowhere — the 2026-07-08 failure mode is unhandled), 504-but-succeeded verify loops, and any file-based per-locale metadata/screenshot workflow. Since `listing push` needs the full hardened submission machinery anyway, delegating publish to msstore-cli would save little while adding a second tool and a .NET runtime dependency to every environment.
- **Fallback value**: if `pcenter` is unavailable, `msstore submission rollout finalize <productId>` is a viable emergency rescue for a stuck rollout, and `msstore publish` a crude emergency publish path (after manually finalizing any in-progress rollout).
- **StoreBroker** (PowerShell): submissions incl. listings/screenshots but aging, clunky payload format, no reviews. Useful as documentation of zip/payload semantics only.
