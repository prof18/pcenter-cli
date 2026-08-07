# 06 — Testing Strategy (TDD)

TDD is mandatory. Build the fake server first (M1); everything else is test-driven through it.

## `internal/fakestore` — the core testing asset

An `httptest.Server` implementing every Partner Center endpoint the CLI uses, with:

- Scenario scripting: per-endpoint canned responses and stateful app/submission models (create → pending; commit → status transitions).
- Failure injection, specifically the "504-but-succeeded" family:
  - return 504 on the first N `finalizepackagerollout` calls **while flipping the rollout state anyway**;
  - 504 on create **while materializing the pending submission**;
  - 504 on delete **while actually deleting**;
  - genuine persistent failures (state never changes);
  - 429 with a `Retry-After` header;
  - 401 after N successful calls (token expiry mid-flow);
  - 403 on blob upload (expired SAS), healed after the client re-GETs the submission.
- A request journal (method, path, decoded body, auth header presence, `MS-CorrelationId` presence) for sequence assertions.
- Fake token endpoint and fake blob endpoint for `fileUploadUrl` ZIP uploads, capturing uploaded content for inspection.
- Fake reviews endpoint with `@nextLink` paging across multiple pages.

## Test layers

1. **Unit**: config resolution order; env-file parsing; release-notes parsing/validation (string vs array, missing/unused locales, case-insensitive matching, `\r\n` join); PUT-body builder against golden JSON files (unknown-field round-trip included); image manifest diffing (add/replace-by-sha/delete/remote-only); package version sort; **backoff schedule** (deterministic rand: exponential growth, cap, jitter bounds, `Retry-After` override); SAS `+`→`%2B` re-encoding of `fileUploadUrl`; redaction (tokens, `client_secret`, SAS `sig=`); output renderers; screenshot validation; `store.json` identity guard; locale-removal guard.
2. **Flow** (`internal/submission` against fakestore): every verify loop in [03-store-api-client.md](03-store-api-client.md) gets happy-path, 504-but-succeeded, and genuinely-failed cases; plus 401 → single token refresh and replay; 429 → `Retry-After` honored; blob 403 → `fileUploadUrl` refresh; 409 from rollout ops → permanent error, zero retries. `submission watch` classification covers all 15 status values (table-driven). Injected clock and rand — zero real sleeping anywhere in the test suite.
3. **E2E** (`test/e2e`): compile the real binary, run it against fakestore via `PCENTER_API_BASE`/`PCENTER_LOGIN_BASE`, assert stdout, stderr, and exit codes in both `--output` modes. Include `reviews list --all` paging and `listing push --dry-run` diff output.
4. **Parity tests for `publish msix`**: assert the exact request sequence (method + path + body) matches what `publish-msix-to-store.ps1` produces for the same scenario — happy path, pending-draft handling, and failure-cleanup (draft deleted after a post-create failure).
5. **Regression scenario**: the 2026-07-08 failure (previous rollout at 90%, `finalizepackagerollout` 504s repeatedly, state flips server-side) must pass through `rollout finalize` and `publish msix`.
6. **Upload content check**: the ZIP built for upload uses Store (no compression) entries and locale-prefixed image names; verify by inspecting the captured blob body.

## Live smoke (manual only, never in CI)

Read-only commands only — `auth status`, `app info`, `locales list`, `reviews list`, `rollout status`, `submission status` — gated behind `PCENTER_LIVE_SMOKE=1`, using real credentials via `PCENTER_ENV_FILE` (maintainer's file location in `plan/LOCAL.md`). Mutating live tests happen only as explicit TODO milestone tasks via `--skip-commit` drafts.

## CI

GitHub Actions matrix: ubuntu + macos + windows. `go test ./...` + `golangci-lint`. Release workflow runs goreleaser on tags. Actions pinned to SHAs, minimal permissions (doc 02 §Supply-chain).

## Fixtures must match the real API

The fakestore originally modelled an application as `{name, lastPublishedApplicationSubmission: {id, status}}`. The live API returns neither of those field names: it is `primaryName`, and submission references carry no status at all. Tests passed for months against a shape the Store never sends, while `app info` printed a blank name and `submission status` printed blank statuses (found 2026-08-06, doc 04).

Two rules follow:

- **Capture a real response before modelling it.** `--verbose` logs response bodies for this reason; a field nobody has seen the Store send is a guess.
- **Tests must not read the developer's own credentials.** The CLI test harness pins `PCENTER_ENV_FILE` at an empty temp file, because a test that passes no environment otherwise falls back to `~/.config/pcenter/credentials.env` and talks to the live Store. That was harmless only while nobody had such a file; `auth login` changed that, and one test was silently hitting the real API before the harness was fixed.
