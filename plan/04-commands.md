# 04 — Command Tree (v1)

Global flags: `--output json|table`, `--env-file`, `--app-id`, `--verbose`. Conventions in [02-architecture.md](02-architecture.md); API semantics in [03-store-api-client.md](03-store-api-client.md).

```
pcenter version
pcenter auth login [--tenant-id --client-id --client-secret --app-id] [--skip-validation]
pcenter auth status [--offline]
pcenter auth doctor
pcenter auth logout --yes
pcenter app info
pcenter locales list
pcenter reviews list [--from --to --top --skip --all --market --filter --orderby]
pcenter submission status
pcenter submission get [--id | --published] [--include-upload-url]
pcenter submission watch [--id] [--poll-seconds --poll-attempts]
pcenter submission delete-draft --yes
pcenter submission commit [--id] [--poll-seconds --poll-attempts]
pcenter rollout status
pcenter rollout finalize
pcenter rollout set-percentage <n>
pcenter rollout halt --yes
pcenter publish msix --path <msix> (--release-notes <json> | --keep-existing-release-notes)
                    [--rollout-percentage 90] [--skip-commit] [--replace-pending]
                    [--poll-seconds 30] [--poll-attempts 20]
pcenter listing show [--locale <locale>] [--published | --pending] [--images]
pcenter listing pull --dir <metadata-dir> [--published | --pending]
pcenter listing push --dir <metadata-dir> (--dry-run | --skip-commit | --yes)
                    [--release-notes <json>] [--replace-pending] [--allow-locale-removal]
```

## Per-command behavior

### `version`
Print version, commit, and build date (injected via `-X` ldflags at release time). Also available as `--version`.

### `auth login`
Write credentials to the env file (`--env-file` → `PCENTER_ENV_FILE` → `~/.config/pcenter/credentials.env`), created `0600` in a `0700` directory. Values come from flags; anything missing is **prompted for only when stdin is a terminal**, so scripts and CI fail with a message naming the flag instead of hanging. The client secret is read without echo.

Credentials are verified against the Store before the file is written (`--skip-validation` opts out), so a typo fails here rather than in the next command. Writing merges: unlisted keys already in the file are kept, so adding an app id later does not discard the account credentials.

The app id is optional — one account can publish several apps, so it is normally passed per command with `--app-id`. In CI prefer the `MS_STORE_*` environment variables straight from secrets; they take precedence over the file and need nothing written to the runner.

### `auth status [--offline]`
Print where each setting resolved from (flag / environment / env-file / not set) without printing values, then acquire a token and `GET applications/{appId}`. `--offline` stops after resolution. Distinct non-zero exits per failure stage: env/config missing → usage error; token rejected → auth error; app fetch failed → API error.

### `auth doctor`
Health check across the env file (existence, permissions — it holds a client secret), each `MS_STORE_*` setting and its source, token acquisition, and app reachability. Reports every check rather than stopping at the first problem, and exits non-zero when the setup is unusable, which makes it a CI preflight. Never prints the secret.

JSON output is `{"ok": bool, "checks": [...]}` — a caller reads `.ok` instead of scanning statuses. Each check carries the human sentence in `detail` **and** the same facts as fields: `source`, `value` (omitted for secrets), `path`, `mode`, `remedy`. A failing check's `remedy` is a runnable command.

### `auth logout --yes`
Delete the credentials file. Idempotent — a missing file is not an error.

### `app info`
`GET applications/{appId}` rendered as-is: id, name, package family/identity, publisher, first published date, advanced-listing permission, and the two submission ids.

**Response shape, verified live 2026-08-06.** The display name is `primaryName`, **not** `name` — modelling it as `name` printed a blank name from every command until this was caught. Each submission reference carries only `{id, resourceLocation}` and **no status**, so a status column here could never be filled. Anything needing a status must fetch the submission itself; `submission status` does, at one request per referenced submission.

### `locales list`
Keys of `listings` from the last published submission (or pending if no published exists). Replaces feed-flow's `.scripts/list-microsoft-store-locales.sh`.

### `reviews list`
`GET analytics/reviews`. `--from/--to` accept ISO dates, converted to the API's `M/D/YYYY`; **when omitted, default to a wide range** (`1/1/2000` → today) because the API's own default (current day only) returns almost nothing. `--market` composes into `filter`; `--filter` passes raw OData; `--orderby` passes through (default `date desc`). `--top` ≤ 10000 (API max); `--all` follows `@nextLink` to exhaustion. Table columns: date, market, rating, title, truncated text, package version. JSON: raw review items.

### `submission status`
Pending + last-published submission ids with their **real** statuses, fetched per submission via `GET .../submissions/{id}/status` because the application resource carries none. Renders `statusDetails` errors/warnings/certification reports.

### `submission get`
Raw submission JSON to stdout (pending by default, `--published` for the published one, `--id` for any). `fileUploadUrl` is **redacted by default** (it contains a live SAS credential); `--include-upload-url` prints it.

### `submission watch`
Poll status until a terminal state using the full status taxonomy in doc 03 (`Published` = success; `Canceled` = neutral; any `*Failed` = exit 1 with `statusDetails`; everything else keeps polling). Defaults to the current pending submission, like `submission commit`.

### `submission delete-draft --yes`
Only deletes a draft in `PendingCommit`; refuses otherwise (mirrors ps1 guard). Uses the delete-verify loop.

### `submission commit`
Port of `commit-store-submission.ps1`: commit verify loop + post-commit poll. Defaults to the current pending submission.

### `rollout status | finalize | set-percentage | halt`
Operate on the last published submission's package rollout. `status` prints `packageRolloutStatus`, percentage, and `fallbackSubmissionId`. `finalize` uses the verify loop — this command alone would have fixed the 2026-07-08 CI failure. `set-percentage <n>` validates 0 < n ≤ 100 and posts `updatepackagerolloutpercentage?percentage=<n>` (query param, no body — confirmed); `halt --yes` posts `haltpackagerollout` and prints the confirmed after-state (`PackageRolloutStopped`, 0%) plus a note that **the next submission will clone the halted submission, not the fallback**. A 409 from any rollout op is a permanent state error (submission not published / rollout not in progress) — report the actual state, never retry.

### `publish msix`
1:1 port of `publish-msix-to-store.ps1` (flow semantics; backoff schedule per doc 03):

1. Validate MSIX path and rollout percentage (0 < n ≤ 100).
2. Release-notes intent is **mandatory**: either `--release-notes <json>` or an explicit `--keep-existing-release-notes` (which leaves the notes cloned from the previous submission). Without this guard, a forgotten flag would silently ship the *previous* version's release notes.
3. Authenticate; `GET applications/{appId}`.
4. Finalize previous rollout if `PackageRolloutInProgress`.
5. Pending-draft handling: fail if a pending submission exists, unless `--replace-pending` and its status is `PendingCommit`, in which case delete it (verify loop).
6. Create submission (with adoption recovery).
7. If `--release-notes`: apply notes to **every** listing locale — hard-fail listing missing locales, warn on unused file locales; values are strings or arrays of lines; arrays joined with `\r\n`; locale matching case-insensitive.
8. Build the PUT body (package rules + rollout, doc 03), PUT it.
9. ZIP the MSIX (no compression), upload to `fileUploadUrl` (blob policy incl. expired-SAS refresh, doc 03).
10. `--skip-commit`: stop, print how to commit later. Otherwise: commit verify loop + post-commit poll.
11. On any failure before commit started: delete the draft (best-effort, warn if that fails). Always clean up the temp ZIP.

No `--yes` here by design: this command's whole purpose is the mutation, it runs unattended in CI, and step 2 already forces explicit intent for the risky part.

### `listing show`
Print the listing to stdout without writing anything. Same content `listing pull` writes, so reading the current text costs no directory and no cleanup — the case an agent hits constantly, and the reason this exists alongside `pull`. `--locale` limits to one locale (case-insensitive; an unknown one is an error naming `locales list`, not an empty result a caller could misread as "the listing is empty"). `--images` adds each image's type, caption and Store id; the binaries are not downloadable through the API, so a count is always present but the files never are. JSON returns `{source, submissionId, localeCount, listings}`; table gives one row per locale, or the full field values when `--locale` selects one.

### `listing pull`
Snapshot the published (default) or pending submission into the metadata dir: `store.json` marker, per-locale listing files, images manifest. No binary downloads (the API provides none). Format in [05-metadata-directory.md](05-metadata-directory.md).

### `listing push`
Same submission lifecycle as `publish msix` (steps 3–6, 10–11 — including finalizing an in-progress previous rollout, since no new submission can be created while one is running) but packages stay untouched. Exactly one mode flag is required:

- `--dry-run` — print a per-locale diff (field changes, image adds/deletes, locale adds/removals) plus the would-be PUT body; create **nothing**.
- `--skip-commit` — create the draft submission with all changes but leave it uncommitted.
- `--yes` — create and **commit** (goes live after certification).

Safety rails:
- Refuses to run if `store.json` in the metadata dir doesn't match the target `--app-id` (prevents pushing one app's metadata to another, or pushing from an empty/wrong dir — which would otherwise mass-delete images).
- A locale present on the server but missing as a local file is an **error** unless `--allow-locale-removal` is passed (prevents an accidentally deleted file from dropping a Store language).
- Adding a new `listings/<locale>.json` adds that locale (pending live verification — TODO item V1).
- Optional `--release-notes` applies the notes file on top; otherwise notes are left as cloned.
