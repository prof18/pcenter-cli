# Command reference

Every `pcenter` command, its flags, what it does, and what it returns. `pcenter <command> --help` is the same information at the terminal and is always current with the binary you have; this page adds the behavior behind the flags.

- [Global flags](#global-flags)
- [`auth`](#auth) — credentials setup and diagnosis
- [`app info`](#app-info)
- [`locales list`](#locales-list)
- [`reviews list`](#reviews-list)
- [`listing`](#listing) — `show`, `pull`, `push`
- [`publish msix`](#publish-msix)
- [`submission`](#submission) — `status`, `get`, `watch`, `commit`, `delete-draft`
- [`rollout`](#rollout) — `status`, `set-percentage`, `finalize`, `halt`
- [`version`](#version)

Conventions used throughout: output is a table on a terminal and JSON when piped (the full machine contract is in [AUTOMATION.md](AUTOMATION.md)); mutating commands never prompt; destructive ones need an explicit flag.

---

## Global flags

| Flag | Meaning |
| --- | --- |
| `--app-id <product-id>` | Microsoft Store product id — the 12-character id from Partner Center, shaped like `9NXXXXXXXXXX`. Overrides `MS_STORE_APP_ID`. |
| `--env-file <path>` | Credentials file to read. Overrides `PCENTER_ENV_FILE`; default `~/.config/pcenter/credentials.env`. A leading `~` is expanded. |
| `--output json\|table` | Force an output format instead of the TTY-aware default. |
| `--verbose` | Log HTTP requests and response bodies to stderr, with secrets redacted. Safe to paste into an issue. |
| `--version` / `-v` | Print version, commit and build date. Same as `pcenter version`. |

Credentials resolve in this order, first hit wins: **flags → `MS_STORE_*` environment variables → credentials file**. See [Authentication](#auth).

---

## `auth`

### `auth login`

```
pcenter auth login [--tenant-id <id>] [--client-id <id>] [--client-secret <secret>]
                   [--app-id <product-id>] [--skip-validation]
```

Writes credentials to the credentials file (`0600`, inside a `0700` directory) so other commands can find them.

- Values come from flags. Anything missing is **prompted for only when stdin is a terminal**; without one the command exits with a message naming the flag to pass, so it can never hang a script or a CI job. The client secret is read without echo.
- Credentials are **verified against the Store before the file is written** — a mistyped secret fails here rather than halfway through a release. `--skip-validation` opts out.
- Writing **merges**: keys already in the file that no flag replaces are kept, so adding an app id later does not discard the account credentials.
- `--app-id` is optional. One account can publish several apps, so the app id is usually better passed per command.

In CI, don't use this command — set the `MS_STORE_*` variables from your secrets instead. They take precedence over the file and leave nothing on the runner.

### `auth status [--offline]`

Reports where each setting resolved from (`flag`, `environment`, `env-file`, or unset) **without printing values**, then acquires a token and fetches the app to prove the credentials work. `--offline` stops after resolution and contacts nothing.

Failure stages have distinct exit codes: missing configuration → `2`, token rejected → `3`, app fetch failed → `1`.

### `auth doctor`

The whole setup checked at once, reported in full rather than stopping at the first problem:

| Check | What it covers |
| --- | --- |
| `env file` | Whether the file exists, its path, and its permission mode — it holds a client secret. |
| `MS_STORE_TENANT_ID` … `MS_STORE_APP_ID` | One check per setting: whether it is set and which source won. |
| `token` | Whether a token can actually be acquired. |
| `app` | Whether the configured app is reachable. |

Exits non-zero when the setup is unusable, which makes it a preflight step in CI. Never prints the secret.

JSON is `{"ok": bool, "checks": [...]}` — read `.ok` rather than scanning statuses. Each check carries the human sentence in `detail` **and** the same facts as fields (`source`, `value` — omitted for secrets, `path`, `mode`, `remedy`). A failing check's `remedy` is a command you can run.

### `auth logout --yes`

Deletes the credentials file. Idempotent — a missing file is reported as `nothing to remove`, not an error. Refuses to run without `--yes`.

---

## `app info`

```
pcenter app info
```

The application resource as the API returns it: id, name, package family and identity names, publisher, first published date, advanced-listing permission, and the ids of the published and pending submissions.

It deliberately shows **no submission status**. The application resource carries only `{id, resourceLocation}` per submission, so a status column here could only ever be blank — [`submission status`](#submission-status) fetches the real thing.

---

## `locales list`

```
pcenter locales list
```

The Store listing locales, lowercased and sorted, taken from the last published submission (or the pending one if nothing is published yet). A bare JSON string array, so it pipes straight into a loop.

---

## `reviews list`

```
pcenter reviews list [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--top <n>] [--skip <n>]
                     [--all] [--market <code>] [--filter <odata>] [--orderby <expr>]
```

| Flag | Default | Notes |
| --- | --- | --- |
| `--from` / `--to` | `2000-01-01` → today | ISO dates, converted to the API's `M/D/YYYY`. The API's own default is *the current day only*, which returns almost nothing — hence the wide default. |
| `--top` | `10000` | Page size, 1–10000 (the API maximum). |
| `--skip` | `0` | Offset. |
| `--all` | off | Follow `@nextLink` until exhausted. |
| `--market` | — | Two-letter market code; composed into the filter for you. |
| `--filter` | — | Raw OData filter, combined with `--market` when both are given. |
| `--orderby` | `date desc` | Passed through. |

Table output is one row per review: date, market, rating, title, truncated text, package version. JSON output is the API's review objects untouched.

---

## `listing`

Three commands over the same content: `show` reads it to stdout, `pull` writes it to files, `push` sends files back. The file format is documented in [Metadata directory](METADATA.md).

### `listing show`

```
pcenter listing show [--locale <locale>] [--published | --pending] [--images]
```

Prints the current listing **without writing anything to disk** — the case you hit constantly when you just want to read the text. Same content `pull` writes, so there is no directory to create and nothing to clean up.

- `--locale` limits output to one locale, matched case-insensitively. An unknown locale is an error naming `pcenter locales list`, never an empty result that could be misread as "the listing is empty".
- `--images` adds each image's type, caption and Store id. Image **binaries cannot be downloaded** through the Submission API, so a count is always available but the files never are.
- `--published` (default) or `--pending` chooses the source submission.

JSON: `{source, submissionId, localeCount, listings}`. Table: one row per locale, or the full field values when `--locale` selects a single one.

### `listing pull`

```
pcenter listing pull --dir <metadata-dir> [--published | --pending]
```

Snapshots the listing into a directory you commit next to your app: `store.json` identity marker, one file per locale under `listings/`, and `images-manifest.json`. No binaries are downloaded (the API offers none) — server images with no local counterpart are recorded as `remoteOnly`.

Reports the directory, source, submission id, locale count, and how many images are remote-only versus matched to local files.

### `listing push`

```
pcenter listing push --dir <metadata-dir> (--dry-run | --skip-commit | --yes)
                     [--release-notes <json>] [--replace-pending] [--allow-locale-removal]
```

Applies the directory to a new submission cloned from the last published one. Packages are untouched — this changes listing text and images only.

**Exactly one mode is required**; there is no default, because the difference between them is the difference between printing a diff and shipping to the Store.

| Mode | What happens |
| --- | --- |
| `--dry-run` | Prints the per-locale diff and the would-be PUT body. Creates **nothing**, and works even when a pending submission exists — it reports that as a warning, since it would block a real push. |
| `--skip-commit` | Creates the draft submission with all changes and leaves it uncommitted for inspection in Partner Center. Commit it later with `pcenter submission commit`. |
| `--yes` | Creates **and commits**. Goes live after certification. |

Safety rails:

- **Identity marker.** A push into a directory whose `store.json` names a different app fails before any request is sent. This is what stops one app's metadata reaching another — or an empty directory being read as "delete everything".
- **Locale removal.** A locale on the server with no local file is an error unless `--allow-locale-removal` is passed, so an accidentally deleted file cannot drop a Store language.
- **Image deletion is explicit.** A server image absent from the manifest is *retained*; removing one takes a `"delete": true` entry. A missing or damaged manifest cannot mass-delete screenshots.
- **One pending submission.** The Store allows only one. An existing draft fails `--skip-commit` and `--yes` unless `--replace-pending` is given *and* that draft is in `PendingCommit`. `--dry-run` is unaffected — it creates nothing, so it previews and warns instead.
- **Previous rollout.** A rollout still in progress is finalized first — no new submission can be created while one is running.
- **Cleanup.** Any failure after the draft was created but before commit deletes the draft (best-effort; a failure to clean up is reported as a warning, never swallowed).

`--release-notes <json>` applies a [release-notes file](METADATA.md#release-notes-file) on top of the listing changes; without it, notes stay as cloned from the previous submission.

The diff reports listing changes as `add` / `update` (with the field name) / `remove` per locale, and image changes as `add` / `replace` / `caption` / `delete`.

---

## `publish msix`

```
pcenter publish msix --path <msix> (--release-notes <json> | --keep-existing-release-notes)
                     [--rollout-percentage 90] [--skip-commit] [--replace-pending]
                     [--poll-seconds 30] [--poll-attempts 20]
```

The full release path: create a submission, attach the package, upload it, commit it.

1. Validate the MSIX path and the rollout percentage (`0 < n ≤ 100`).
2. **Release-notes intent is mandatory** — either `--release-notes <json>` or an explicit `--keep-existing-release-notes`. Without this guard a forgotten flag would silently ship the *previous* version's changelog.
3. Authenticate and fetch the app.
4. Finalize the previous rollout if one is still in progress.
5. Handle an existing pending draft: fail, unless `--replace-pending` and its status is `PendingCommit`, in which case delete it.
6. Create the submission (cloned from the last published one).
7. Apply release notes to **every** listing locale. A locale present in the Store but missing from the file is a **hard failure**, not a silently empty changelog; locales in the file that the Store does not have are a warning. Values are a string or an array of lines (joined with CRLF); locale matching is case-insensitive.
8. Build and PUT the submission body with the package and rollout settings.
9. ZIP the MSIX (stored, no compression) and upload it, refreshing the SAS URL if it expired mid-upload.
10. `--skip-commit` stops here and prints the command to commit later. Otherwise: commit, verify, and poll to a terminal status.
11. On any failure before the commit started, delete the draft (best-effort, warn if that fails). The temporary ZIP is always removed.

There is no `--yes`: the whole point of the command is the mutation, it runs unattended in CI, and step 2 already forces explicit intent for the risky part.

`--poll-seconds` / `--poll-attempts` bound the post-commit wait. Reaching the attempt limit is not a failure of the submission — it means pcenter stopped watching; use [`submission watch`](#submission-watch) to resume.

Result: submission id, package file name, rollout percentage, whether it was left as a draft, the commit status, and any warnings.

---

## `submission`

### `submission status`

```
pcenter submission status
```

The pending and last-published submissions with their **real** statuses, fetched per submission because the application resource carries none. Renders `statusDetails` — the errors, warnings and certification reports the Store attaches to a failed or in-review submission.

### `submission get`

```
pcenter submission get [--id <id> | --published] [--include-upload-url]
```

The raw submission JSON, exactly as the API returns it: the pending submission by default, `--published` for the last published one, `--id` for any other.

`fileUploadUrl` is **redacted by default** — it carries a live SAS credential. `--include-upload-url` prints it; treat that output as a secret.

### `submission watch`

```
pcenter submission watch [--id <id>] [--poll-seconds 30] [--poll-attempts 20]
```

Polls until the submission reaches a terminal status. `Published` succeeds; `Canceled` is neutral; any `*Failed` status exits non-zero with `statusDetails` attached; anything else keeps polling. Defaults to the current pending submission.

Running out of attempts is **not** a failure — certification legitimately takes hours, and it means pcenter stopped watching rather than the Store stopping work. You get the last observed status with `classification: "in-progress"`, a `warning`, and exit 0. Re-run to keep watching. Only a genuinely failed status exits non-zero.

### `submission commit`

```
pcenter submission commit [--id <id>] [--poll-seconds 30] [--poll-attempts 20]
```

Commits a prepared draft — the second half of `publish msix --skip-commit` or `listing push --skip-commit`. Commits, verifies the resulting state rather than trusting the response code, then polls to a terminal status. Defaults to the current pending submission.

### `submission delete-draft --yes`

```
pcenter submission delete-draft --yes
```

Deletes the pending draft. Refuses unless its status is `PendingCommit` — a submission already in certification is not yours to delete. The deletion is verified, not assumed.

---

## `rollout`

All four operate on the last published submission's package rollout.

### `rollout status`

Prints `packageRolloutStatus`, the current percentage, and `fallbackSubmissionId`.

### `rollout set-percentage <n>`

Sets the rollout percentage; `n` must satisfy `0 < n ≤ 100`. Prints the confirmed after-state.

### `rollout finalize`

Takes the rollout to 100%. Uses the verify loop, so a timeout mid-finalize resolves to the actual state instead of leaving you guessing — this command alone would have fixed the CI failure that motivated the CLI.

### `rollout halt --yes`

Stops the rollout and prints the confirmed after-state (`PackageRolloutStopped`, 0%), plus the consequence that is easy to miss: **the next submission clones the halted submission, not the fallback.**

A `409` from any rollout operation means the operation is invalid for the current state (submission not published, or no rollout in progress). It is reported as the actual state and **never retried** — exit code `4`.

---

## `version`

```
pcenter version
```

Version, commit and build date, injected at release time. Local builds report `dev` / `unknown` / `unknown`. `pcenter --version` prints the same information on one line.
