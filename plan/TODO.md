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
- [ ] `listing push`: mode enforcement (`--dry-run | --skip-commit | --yes`), identity guard, text application, image add/replace/delete + ZIP upload, locale add, `--allow-locale-removal` guard, `--release-notes`, `--dry-run` diff output
- [ ] V1: verify locale add/remove via update PUT with a live `--skip-commit` draft; document result
- [ ] Encode confirmed image limits (PNG only, ≤50 MB, desktop ≥1366×768, ≤10 desktop / ≤8 other per locale, caption ≤200 chars — doc 05); V2 residual: confirm the API enforces the same limits on first live push
- [ ] Hardware fields (`recommendedHardware`/`minimumHardware`): raw passthrough on pull, lenient validation (docs type ambiguity — doc 05); confirm shape from first live pull
- [ ] V3: verify a metadata-only submission can carry `packageDeliveryOptions` through unchanged

Acceptance:
- [ ] `pull` on FeedFlow produces a complete metadata dir; immediate `push --dry-run` reports "no changes"
- [ ] `push` against a wrong/empty dir fails on the `store.json` guard (test + manual check)
- [ ] A text change + screenshot add/remove produces a correct live draft (`--skip-commit`, inspected, then deleted)

## M5 — Release + feed-flow CI swap

- [ ] **Maintainer step**: create the public GitHub repo (`pcenter-cli`) and push — Marco does this himself; everything below depends on it (CI matrix runs, goreleaser releases, feed-flow download)
- [ ] Verify the 3-OS CI matrix is green on GitHub (deferred from M1)
- [ ] goreleaser config (darwin/arm64, windows/amd64, linux/amd64), version ldflags, checksums, release workflow on tag
- [ ] Tag `v0.1.0`; README install instructions (pinned download + checksum)
- [ ] docs/: per-command reference + metadata dir format
- [ ] feed-flow: seed metadata dir via `listing pull`, commit ([07-feedflow-integration.md](07-feedflow-integration.md) §1)
- [ ] feed-flow: swap `windows-release.yml` to pinned `pcenter` (§3)
- [ ] After first green real release: delete the 3 ps1 scripts + `list-microsoft-store-locales.sh`, update docs/CLAUDE.md (§4–5)

Acceptance:
- [ ] One real FeedFlow release published end-to-end through `pcenter` in CI
- [ ] ps1 scripts removed from feed-flow

## Verify against the live API (referenced above)

- **V1** Locale add/remove via update PUT (M4)
- **V2** Screenshot limits: docs-confirmed values are encoded client-side; confirm the API enforces the same on the first live push, and that caption (`description`) edits on `Uploaded` images are accepted in place (M4)
- **V3** Metadata-only submission: `packageDeliveryOptions` carried through unchanged (M4)

Resolved during planning (2026-07-09/10, against Learn docs):
- ~~Reviews paging~~ → `@nextLink` + `TotalCount`; `top` default/max 10000.
- ~~Rollout endpoint shapes~~ → `updatepackagerolloutpercentage?percentage=<n>` (query param, no body); `haltpackagerollout`, `finalizepackagerollout` bodyless POSTs; rollout GET includes `fallbackSubmissionId`.
- ~~Halt semantics~~ → after-state `PackageRolloutStopped`/0%; next submission clones the **halted** submission; 409 on invalid state (permanent, no retry).
- ~~Submission status enum~~ → full 15-value list + watch classification in doc 03.
- ~~Screenshot constraints~~ → PNG only, ≤50 MB, desktop ≥1366×768, ≤10 desktop / ≤8 other, caption ≤200 chars (doc 05); only API-enforcement check remains (V2).
- ~~All submission/rollout endpoint paths~~ → match the ps1 exactly (GET/PUT/POST/DELETE table in manage-app-submissions).
- ~~V4 blob single-PUT size limit~~ → resolved by design: upload via the Go `azblob` SDK (chunked block upload, same approach as msstore-cli's `BlobClient.UploadAsync`); includes the SAS `+`→`%2B` re-encoding quirk from msstore-cli's source.
- ~~ZIP image layout~~ → `{locale}/{filename}` convention confirmed against msstore-cli's bundle code (partially de-risks V1/V2).
