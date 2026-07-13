# 03 — Store API Client Spec

Base: `https://manage.devcenter.microsoft.com/v1.0/my`.

The flow-level verify semantics come from production failures encoded in `store-submission-common.ps1` and `publish-msix-to-store.ps1` — **port the verify logic exactly**. The backoff schedule deliberately differs from the ps1 (which used linear waits): Partner Center and the Azure gateway are flaky (random 504s, occasional 429/503), so everything retryable uses **exponential backoff with jitter** (decided 2026-07-09).

## Backoff policy (single implementation, used everywhere)

```
delay(attempt) = rand(0.5, 1.0) * min(cap, base * 2^(attempt-1))
```

- Full-jitter exponential: `attempt` starts at 1; `rand` uses an injectable source so tests are deterministic.
- **`Retry-After`**: when a 429 or 503 response carries a `Retry-After` header, use that value instead of the computed delay (capped at 5 minutes).
- All sleeping and randomness go through injectable clock/rand interfaces — zero real sleeping in tests.

Schedules (base / cap / max attempts):

| Operation | Base | Cap | Attempts | Worst-case wait |
| --- | --- | --- | --- | --- |
| Token acquisition | 2s | 30s | 4 | ~1 min |
| Transport GET/PUT | 5s | 60s | 4 | ~2 min |
| Verify loop: finalize rollout | 20s | 240s | 5 | ~10 min |
| Verify loop: create submission | 20s | 120s | 4 | ~5 min |
| Verify loop: commit | 15s | 120s | 4 | ~4 min |
| Verify loop: delete draft | 10s | 60s | 3 | ~2 min |
| Blob upload | 15s | 120s | 4 | ~5 min |

## Auth resilience

- Token endpoint (`POST https://login.microsoftonline.com/{tenant}/oauth2/token`) is idempotent — retry on transient errors with the token schedule.
- Tokens last ~60 minutes. Long flows (publish with many retries + polling) can cross expiry: on a **401** from any Store API call, re-acquire the token once and replay the request; a second 401 is a real auth failure.

## Transport-level retries

- **GET and PUT**: up to 4 attempts on transient errors — HTTP 408, 429, ≥500, or network-level failure (no HTTP response counts as transient). GET is naturally idempotent; PUT here is a full-document replace, safe to repeat.
- **POST and DELETE**: exactly **1** transport attempt. Retrying is only done by the flow-level verify loops below — a blind POST retry can double-create.
- Per-request timeout 300s for JSON API calls (blob upload has its own policy, below).
- Error messages must include the response body (Partner Center puts real diagnostics there) — with SAS/token redaction applied (see Security).

## Bodyless non-GET quirk

Non-GET requests with no payload must send an **empty** body with `Content-Type: application/json`. Never send `{}` (the create-submission endpoint validates it as submission data and rejects it with "The size of Listings must be 1 or more").

## Flow-level verify loops (the 504 lessons)

The gateway regularly answers 504 while the operation still succeeds server-side. Each mutating flow verifies state between attempts instead of trusting responses:

- **Finalize rollout** (`POST .../submissions/{id}/finalizepackagerollout`): after each failed attempt, wait per schedule, then re-`GET .../packagerollout`; if status is no longer `PackageRolloutInProgress`, treat as success.
- **Create submission** (`POST applications/{appId}/submissions`): after each failed attempt, wait, then `GET applications/{appId}`; if `pendingApplicationSubmission.id` appeared, **adopt it** (GET that submission and continue) instead of retrying.
- **Commit** (`POST .../submissions/{id}/commit`): after each failed attempt, wait, then `GET .../submissions/{id}/status`; if status ≠ `PendingCommit`, the commit was accepted.
- **Delete draft** (`DELETE .../submissions/{id}`): after each attempt, verify via `GET applications/{appId}` that the pending submission is gone before waiting/retrying.
- **Post-commit poll**: poll `GET .../submissions/{id}/status` every `--poll-seconds` (default 30) up to `--poll-attempts` (default 20); fail on `CommitFailed` / `PreProcessingFailed` (include `statusDetails` JSON in the error); succeed once status is past `PendingCommit` / `CommitStarted`; if still in startup after all attempts, **warn and exit 0** (matches the ps1 — Partner Center takes over from there).

The verification GETs inside these loops use the transport retry policy themselves; a transient failure of a verification GET must not be confused with "operation failed".

## Rollout endpoints (confirmed against Learn docs, 2026-07-09/10)

- `GET  .../submissions/{id}/packagerollout` — returns `{isPackageRollout, packageRolloutPercentage, packageRolloutStatus, fallbackSubmissionId}`. Surface `fallbackSubmissionId` in `rollout status` output.
- `POST .../submissions/{id}/updatepackagerolloutpercentage?percentage=<n>` — percentage is a **query parameter**, no request body.
- `POST .../submissions/{id}/haltpackagerollout` — no body. On success the rollout becomes `{packageRolloutPercentage: 0.0, packageRolloutStatus: "PackageRolloutStopped"}`. Semantics: a **new submission created after a halt clones the halted submission**, not the fallback.
- `POST .../submissions/{id}/finalizepackagerollout` — no body.
- Rollout ops return **404** (submission not found) and **409** (submission not published, or rollout not `PackageRolloutInProgress`). 409 is a **permanent** state error — never retried, mapped to a clear message telling the user the actual rollout state.

## Submission status taxonomy (confirmed against Learn docs, 2026-07-10)

Full enum: `None, Canceled, PendingCommit, CommitStarted, CommitFailed, PendingPublication, Publishing, Published, PublishFailed, PreProcessing, PreProcessingFailed, Certification, CertificationFailed, Release, ReleaseFailed`.

Classification used by `submission watch` and status rendering:

- **Failed (exit 1, print `statusDetails`)**: `CommitFailed`, `PreProcessingFailed`, `CertificationFailed`, `PublishFailed`, `ReleaseFailed`.
- **Terminal success**: `Published`. **Terminal neutral**: `Canceled`.
- **In progress (keep polling)**: everything else (`PendingCommit`, `CommitStarted`, `PreProcessing`, `Certification`, `PendingPublication`, `Publishing`, `Release`, `None`).

The post-commit poll (above) intentionally uses a narrower rule — it only waits for commit startup to complete, it does not wait for certification.

`targetPublishMode` values: `Immediate`, `Manual`, `SpecificDate` (+ `targetPublishDate` ISO 8601 when `SpecificDate`). The CLI defaults to `Immediate` and doesn't expose the others in v1.

## Submission PUT body construction

Clone **only** this allowlist from the GET response (drop everything else):

```
applicationCategory, pricing, visibility, targetPublishDate, listings,
hardwarePreferences, automaticBackupEnabled, canInstallOnRemovableMedia,
isGameDvrEnabled, gamingOptions, hasExternalInAppProducts,
meetAccessibilityGuidelines, notesForCertification, enterpriseLicensing,
allowMicrosoftDecideAppAvailabilityToFutureDeviceFamilies,
allowTargetFutureDeviceFamilies, trailers
```

Then set `targetPublishMode` (default `Immediate`).

For `publish msix` additionally:

- Sort existing `applicationPackages` by `version` (parsed as a 4-part version, descending); keep the newest as-is; mark all older ones `fileStatus: "PendingDelete"`. Rationale: customers outside the rollout group must keep a valid package.
- Append the new package: `{fileName, fileStatus: "PendingUpload", minimumDirectXVersion: "None", minimumSystemRam: "None"}`.
- Set `packageDeliveryOptions.packageRollout = {isPackageRollout: true, packageRolloutPercentage: <n>}` while preserving the other `packageDeliveryOptions` fields (`isMandatoryUpdate`, `mandatoryUpdateEffectiveDate`).

For `listing push`: keep `applicationPackages` exactly as returned (all `Uploaded`, untouched) and apply metadata changes only. Verify during implementation whether `packageDeliveryOptions` can be carried through unchanged (TODO item V3).

Use JSON handling that **round-trips unknown fields** (`json.RawMessage`-based composition) — the allowlist copy must preserve fields the CLI doesn't model, and Partner Center adds fields over time.

## Package/image upload (Azure blob)

- Create a ZIP containing the new MSIX and/or new listing images. Use **Store (no compression)** for the entries — MSIX and PNG are already compressed; deflating them again wastes CI minutes for ~0% gain.
- Upload to the submission's `fileUploadUrl` (Azure blob SAS) using the **Azure Blob SDK for Go (`azblob`)**, not a hand-rolled PUT. The SDK does chunked block upload (`Put Block`/`Put Block List`) automatically for large files with parallelism and its own retries — this is what Microsoft's own msstore-cli does (`BlobClient.UploadAsync`) and it removes the single-PUT size-limit concern entirely (former TODO item V4, now resolved by design). Set `Content-Type: application/zip`; configure the SDK's retry policy to align with the blob schedule above.
- **SAS `+` quirk (from msstore-cli source)**: Partner Center can return a `fileUploadUrl` containing a literal `+` in the SAS signature; it must be re-encoded as `%2B` before use or blob auth fails (msstore-cli does `blobUri.Replace("+", "%2B")`). In Go, take care not to round-trip the URL through query parsing (`+` decodes as space). Unit-test this.
- **Timeout**: scale to payload: `max(10 min, size / 200 KiB/s)` as the overall bound. The ps1 used an unlimited upload timeout; a fixed 300s would break large MSIX uploads on slow links.
- **Expired SAS**: the SAS URL is time-limited. On a 403 during upload (e.g., after long retries elsewhere), re-`GET` the submission to obtain a fresh `fileUploadUrl` and resume attempts.

## Reviews endpoint (confirmed against Learn docs, 2026-07-09)

`GET analytics/reviews?applicationId={appId}` with optional:

- `startDate` / `endDate` — the CLI accepts ISO (`2026-07-09`) on `--from/--to` and converts to the documented `M/D/YYYY` format when building the query. Both default to the current date server-side, so **omitting dates returns almost nothing** — `reviews list` with no date flags should default to a wide range (e.g. `1/1/2000` → today), not mirror the API default.
- `top` — default and max **10000**; `skip` for offset paging.
- `filter` — OData-ish; string values in single quotes. Useful fields: `market`, `rating` (supports gt/lt/ge/le), `packageVersion`, `reviewText`/`reviewTitle` (support `contains`), `id`.
- `orderby` — e.g. `orderby=date desc`; fields include `date`, `rating`, `market`, `helpfulCount`.

Response: `Value` array + `TotalCount` + optional `@nextLink` (relative continuation URI) when more pages exist. `--all` follows `@nextLink` to exhaustion. Review `id` is a GUID (needed later if respond-to-reviews is added).

## Support correlation

Send an `MS-CorrelationId` header (one GUID per CLI invocation) on every Store API request, and include it in error output — msstore-cli and StoreBroker do the same, and it's what Partner Center support asks for when investigating server-side failures.

## Security (applies to every code path)

- Never log `client_secret`, bearer tokens, or the `Authorization` header — including `--verbose` mode and error messages.
- `fileUploadUrl` **contains a SAS signature and is a credential**: redact the query string (`sig=...`) in all logs, verbose output, and error messages. `submission get` redacts it from stdout by default; `--include-upload-url` opts in.
- Redaction is centralized in the logging layer, not left to call sites.
