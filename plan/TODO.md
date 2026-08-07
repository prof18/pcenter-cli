# TODO — `pcenter` Implementation

Work top to bottom. Do not start a milestone before the previous one's acceptance criteria are checked. Update this file as you go (check boxes, add discovered tasks under the right milestone).

## M1 — Scaffold + read-only commands

- [x] Init git repo and first commit (done 2026-07-10; `plan/LOCAL.md` is gitignored and must never be committed)
- [x] **Local-only until M5**: do NOT create a GitHub repo or push — the maintainer publishes the repo himself at the end. Still author everything as public-ready from commit one (no secrets, no machine paths)
- [x] Init Go module, repo layout from [02-architecture.md](02-architecture.md), CI workflow files (test + golangci-lint on ubuntu/macos/windows; actions pinned to SHAs, minimal permissions) — they'll only run once the repo is pushed
- [x] Renovate config for Go modules + GitHub Actions
- [x] `internal/config`: flag → env → env-file resolution, `PCENTER_ENV_FILE`, validation errors
- [x] `internal/store`: token acquisition (with retry), backoff policy (exponential + full jitter, `Retry-After` override, injectable clock/rand), transport retry for GET/PUT, transient-error classification, 401 → single token refresh + replay, bodyless-non-GET quirk, error messages including response bodies, centralized redaction (tokens, `client_secret`, SAS `sig=`)
- [x] `internal/fakestore` v1: token endpoint, app + submission + packagerollout GET endpoints, reviews with `@nextLink` paging, failure injection (504/429+Retry-After/401), request journal
- [x] `internal/output`: table/json renderers, TTY detection, exit-code conventions
- [x] Commands: `version`, `auth status`, `app info`, `locales list`, `submission status`, `submission get` (SAS redacted by default), `rollout status` (incl. `fallbackSubmissionId`)
- [x] `reviews list` with date/market/filter/orderby flags, wide default date range, `--all` via `@nextLink`
- [x] E2E harness (`test/e2e`) running the compiled binary against fakestore

Acceptance:
- [x] All tests + `golangci-lint` green locally (the 3-OS CI matrix validates later, once the repo is pushed in M5)
- [x] Backoff unit tests prove exponential growth, cap, jitter bounds, and `Retry-After` override
- [x] Live smoke (`PCENTER_LIVE_SMOKE=1`, read-only): `locales list` returns FeedFlow's 25 locales; `reviews list` returns real reviews (validates the M/D/YYYY date conversion)

## M2 — Submission & rollout mutations

- [x] Fakestore failure injection: 504-but-succeeded scenarios for finalize/create/commit/delete + genuine-failure variants
- [x] Verify loops from [03-store-api-client.md](03-store-api-client.md) with the exponential schedules: finalize, create-with-adoption, commit, delete-verify, post-commit poll
- [x] Commands: `rollout finalize`, `rollout set-percentage` (query param, confirmed), `rollout halt --yes` (prints after-state + clone-of-halted warning), `submission delete-draft --yes`, `submission commit`, `submission watch` (full 15-value status taxonomy from doc 03)
- [x] 409 from rollout ops mapped to a permanent state error (no retry, actual rollout state in the message)

Acceptance:
- [x] Flow tests cover happy / 504-but-succeeded / genuinely-failed for every loop, plus 401-refresh and 429/Retry-After paths
- [x] The 2026-07-08 scenario (rollout stuck at 90%, finalize 504s while state flips) passes via `rollout finalize` against fakestore

## M3 — `publish msix`

- [x] PUT-body builder: allowlist clone, unknown-field round-trip, package sort/PendingDelete rules, rollout options (golden JSON tests)
- [x] Release-notes parsing/validation (string|array, `\r\n` join, missing-locale hard fail, unused-locale warning, case-insensitive locale matching); `--release-notes` xor `--keep-existing-release-notes` enforcement
- [x] ZIP creation (Store/no-compression entries) + blob upload via the Go `azblob` SDK (chunked, SDK retries aligned with the blob schedule), size-scaled overall timeout, 403 → `fileUploadUrl` refresh, SAS `+`→`%2B` re-encoding (unit-tested)
- [x] `MS-CorrelationId` header (one GUID per invocation) on all Store API calls, included in error output
- [x] Full `publish msix` flow incl. `--skip-commit`, `--replace-pending`, cleanup-on-failure, temp-file cleanup
- [x] Parity tests: request sequence matches `publish-msix-to-store.ps1` for happy path, pending-draft path, failure-cleanup path
- [x] Resolve the live-acceptance contradiction: Microsoft's [StoreBroker usage guide](https://github.com/microsoft/StoreBroker/blob/master/Documentation/USAGE.md#creating-a-new-application-submission) says the Dev Portal remains out of sync with Submission API changes until a submission enters certification. Plan decision (2026-07-14): inspect uncommitted API-created drafts through the Submission API; defer Portal verification to the first committed release in M5.

Acceptance:
- [x] Parity tests green
- [x] Live: `publish msix --skip-commit` with a real FeedFlow MSIX creates a correct draft (API inspection confirmed 25 listings, exact release-note matches, pending package, and 90% rollout), then `submission delete-draft --yes` removes it

## M4 — `listing pull` / `listing push`

- [x] `internal/metadata`: dir read/write, `store.json` identity marker, listing file schema + client-side limits, images manifest, sha256 diffing, case-insensitive locale handling
- [x] Resolve the `remote-only` no-op contradiction. Plan decision (2026-07-14): retained `remoteOnly` and unmentioned server entries are kept unchanged; a missing managed local file is an error; only an explicit `delete: true` entry with `storeId` becomes `PendingDelete`.
- [x] `listing pull` (published/pending source, `remote-only` image entries)
- [x] `listing push`: mode enforcement (`--dry-run | --skip-commit | --yes`), identity guard, text application, image add/replace/delete + ZIP upload, locale add, `--allow-locale-removal` guard, `--release-notes`, `--dry-run` diff output
- [x] V1 add: a live `--skip-commit` draft added `en-gb`; API inspection confirmed 26 locales (2026-07-14)
- [x] Encode confirmed image limits (PNG only, ≤50 MB, desktop ≥1366×768, ≤10 desktop / ≤8 other per locale, caption ≤200 chars — doc 05)
- [x] V2 image upload: the live API accepted two 1920×1080 PNG screenshots as `PendingUpload` (2026-07-14)
- [x] Hardware fields (`recommendedHardware`/`minimumHardware`): raw passthrough on pull and lenient validation (docs type ambiguity — doc 05)
- [x] Confirm the hardware field shape from the first live pull: FeedFlow returned arrays for both fields (2026-07-14)
- [x] V3: metadata-only PUT accepted cloned `packageDeliveryOptions`; both existing packages kept the same IDs/files and remained `Uploaded` (2026-07-14)

Acceptance:
- [x] `pull` on FeedFlow produces a complete metadata dir; immediate `push --dry-run` reports "no changes" (25 locales, 101 `remoteOnly` images, zero listing/image/upload changes; 2026-07-14)
- [x] `push` against a wrong/empty dir fails on the `store.json` guard (wrong-app test plus live empty-directory manual check; 2026-07-14)
- [x] An additive-only text + screenshot change produced a correct live draft (`--skip-commit`), with 101 existing images still `Uploaded`, two additions `PendingUpload`, and zero `PendingDelete`; the draft was inspected and deleted (2026-07-14)

## M5 — Release + feed-flow CI swap

Distribution changed on 2026-08-03: goreleaser is out, replaced by a hand-rolled release workflow plus a Homebrew formula in `prof18/homebrew-tap` — same shape as `regesto`. Rationale and the artifact/consumer split are in [02-architecture.md](02-architecture.md) §Distribution.

Repo-independent (can be authored before the repo exists):

- [x] `CHANGELOG.md` with a `## 0.0.1` section — the release workflow reads notes from it and fails without one
- [x] `.github/workflows/release.yml`: cross-compile darwin arm64/amd64 + linux arm64/amd64 (`.tar.gz`) and windows/amd64 (`.zip`), `-X main.version/commit/buildDate` ldflags, `checksums.txt`, artifact smoke test, CHANGELOG-gated `gh release create`
- [x] `script/rehearse-release.sh` — runs the whole release locally except `gh release create`; all steps pass (2026-08-03)
- [x] Pre-flight the tap without a release: `update-formula.py` works on a `pcenter` formula unmodified, and the formula installs + `brew test` passes against `file://` URLs (2026-08-03, [08-release-testing.md](08-release-testing.md))
- [x] docs/: per-command reference (`COMMANDS.md`), metadata dir + release-notes format (`METADATA.md`), machine contract for agents (`AUTOMATION.md`), CI integration (`CI.md`), failure guide (`TROUBLESHOOTING.md`), index (`README.md`)
- [x] README: rewritten public-facing — install via `brew install prof18/tap/pcenter`, the pinned-download + checksum path for CI lives in `docs/CI.md`

Needs the repo to exist — test order and open questions in [08-release-testing.md](08-release-testing.md):

- [ ] **Maintainer step**: create the public GitHub repo (`pcenter-cli`) and push — Marco does this himself; everything below depends on it
- [ ] Verify the 3-OS CI matrix is green on GitHub (deferred from M1)
- [ ] Walk the Tier 0 and Tier 1 sweep in [09-manual-live-testing.md](09-manual-live-testing.md) against FeedFlow; record results in its table
- [ ] Tag `v0.0.1` — a real release, not an RC (maintainer direction 2026-08-07, rationale in [08-release-testing.md](08-release-testing.md) §"`v0.0.1` instead of a release candidate"); walk the five checks in §"What only a real release can prove"; confirm the release carries all five archives + `checksums.txt` and that the published binary reports the tagged version
- [ ] Settle the remaining open question: where the windows zip gets smoke-tested (the pre-release question is moot — `v0.0.1` is a normal release)
- [ ] `prof18/homebrew-tap`: add `Formula/pcenter.rb` (darwin+linux only — Homebrew has no Windows channel) and `.github/workflows/update-pcenter.yml`, both cloned from the regesto pair; `script/update-formula.py` is already tool-agnostic and needs no change
- [ ] Formula `test do`: assert the stamped version, then two credential-free real-code paths — `listing push --dry-run --yes` (exit 2, mode enforcement) and `listing push --dry-run` on a dir with no `store.json` (exit 1, identity guard). Both fire before any network call
- [ ] Tap README: add a `pcenter` section alongside `regesto`
- [ ] Verify end to end: `brew install prof18/tap/pcenter` on a clean machine, then `pcenter version` reports the tagged version
- [ ] feed-flow: seed metadata dir via `listing pull`, commit ([07-feedflow-integration.md](07-feedflow-integration.md) §1)
- [ ] feed-flow: swap `windows-release.yml` to pinned `pcenter` (§3)
- [ ] After first green real release: delete the 3 ps1 scripts + `list-microsoft-store-locales.sh`, update docs/CLAUDE.md (§4–5)
- [ ] During that release, walk Tier 2 of [09-manual-live-testing.md](09-manual-live-testing.md) (commit, watch, rollout status/set-percentage/finalize) — the only paths that cannot be exercised on demand
- [ ] After the first committed CLI-managed image exists, verify caption-only edits on an `Uploaded` image; fall back to replace if the API rejects them
- [ ] Verify locale removal only when the maintainer explicitly authorizes deleting a Store listing locale

Acceptance:
- [ ] One real FeedFlow release published end-to-end through `pcenter` in CI
- [ ] ps1 scripts removed from feed-flow

## Verify against the live API (referenced above)

- **V1** Locale add via update PUT verified in M4; removal deferred to M5 by maintainer safety direction
- **V2** Screenshot upload limits verified in M4; caption edit deferred until a CLI-managed image is committed in M5
- **V3** Metadata-only submission accepted cloned `packageDeliveryOptions` in M4

Resolved during planning (2026-07-09/10, against Learn docs):
- ~~Reviews paging~~ → `@nextLink` + `TotalCount`; `top` default/max 10000.
- ~~Rollout endpoint shapes~~ → `updatepackagerolloutpercentage?percentage=<n>` (query param, no body); `haltpackagerollout`, `finalizepackagerollout` bodyless POSTs; rollout GET includes `fallbackSubmissionId`.
- ~~Halt semantics~~ → after-state `PackageRolloutStopped`/0%; next submission clones the **halted** submission; 409 on invalid state (permanent, no retry).
- ~~Submission status enum~~ → full 15-value list + watch classification in doc 03.
- ~~Screenshot constraints~~ → PNG only, ≤50 MB, desktop ≥1366×768, ≤10 desktop / ≤8 other, caption ≤200 chars (doc 05); only API-enforcement check remains (V2).
- ~~All submission/rollout endpoint paths~~ → match the ps1 exactly (GET/PUT/POST/DELETE table in manage-app-submissions).
- ~~V4 blob single-PUT size limit~~ → resolved by design: upload via the Go `azblob` SDK (chunked block upload, same approach as msstore-cli's `BlobClient.UploadAsync`); includes the SAS `+`→`%2B` re-encoding quirk from msstore-cli's source.
- ~~ZIP image layout~~ → `{locale}/{filename}` convention confirmed against msstore-cli's bundle code (partially de-risks V1/V2).
