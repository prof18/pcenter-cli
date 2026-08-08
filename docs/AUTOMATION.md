# Automation contract — agents, scripts, CI

`pcenter`'s primary consumers are agents and CI jobs, so the machine surface is the designed one, not an afterthought. This page is the contract: what you get on stdout, what you get on stderr when things fail, and what `$?` means.

- [Output format](#output-format)
- [Exit codes](#exit-codes)
- [Error payloads](#error-payloads)
- [Success payloads](#success-payloads)
- [Warnings](#warnings)
- [Credentials](#credentials)
- [Rules for agents](#rules-for-agents)
- [Recipes](#recipes)

---

## Output format

Table on a terminal, **JSON when piped**. An agent invoking `pcenter` through a pipe gets machine-readable output without knowing to ask for it. `--output json` or `--output table` forces either.

Results are **bare data, not an envelope** — the same idiom as `gh` and `aws`, and jq-friendly. There is no `{"data": …}` wrapper to unwrap.

`--verbose` writes requests and response bodies to **stderr**, never stdout, so it never corrupts a JSON parse. The client secret, bearer tokens, and the SAS signature on upload URLs are redacted centrally; verbose output is safe to paste into an issue.

---

## Exit codes

Branch on `$?` alone, without reading stdout:

| Code | Meaning | What to do |
| --- | --- | --- |
| `0` | Success | — |
| `1` | Unclassified failure | Read the error; may be transient. |
| `2` | Usage, or configuration that must be fixed first | Fix the invocation or the credentials. Never retry unchanged. |
| `3` | Credentials rejected | Permanent. Needs a human — re-authenticate. |
| `4` | Operation invalid for the current state (HTTP 409) | Permanent. **Never retry unchanged**; read the actual state first. |
| `5` | Throttled beyond the retry budget | Retry later with backoff. |

The 3/4/5 split exists because those three demand different responses. Collapsing them into `1` would force you to regex the message.

---

## Error payloads

With JSON output, a failure writes **one JSON object to stderr** and nothing to stdout:

```json
{"error":{"code":"state_conflict","message":"…one line…","correlationId":"…","statusCode":409,"remedy":"…"}}
```

- `code` is **stable** — it may not change meaning once released. Branch on it.
- `message` is a single line and may be reworded freely. Do not parse it.
- Additional fields carry anything you would otherwise have to extract from the text: `missing`, `envFile`, `remedy`, `correlationId`, `retryAfter`, `warnings`, …
- The long human explanation appears **only in table mode**, where a person is reading it.

| `code` | Exit | Meaning |
| --- | --- | --- |
| `usage` | 2 | Invalid flags or argument values. |
| `missing_configuration` | 2 | Credentials could not be resolved. The payload names which. |
| `env_file` | 2 | A credentials file was named explicitly but is absent or unreadable. |
| `validation` | 2 | Input rejected before any request was sent. |
| `auth_failed` | 3 | The Store rejected the credentials. |
| `state_conflict` | 4 | HTTP 409 — invalid for the resource's current state. |
| `not_found` | 1 | HTTP 404 — missing app, submission, or rollout. |
| `rate_limited` | 5 | HTTP 429 that outlasted the retry budget. |
| `api_error` | 1 | Any other unsuccessful Store response. |
| `failure` | 1 | Unclassified runtime failure. |

---

## Success payloads

Typed, never stringly: booleans stay booleans (`draft`, `accepted`, `hasChanges`, `isPackageRollout`), numbers stay numbers (`packageRolloutPercentage`). `submission get` and `reviews list` pass the API's own JSON through untouched.

**A list that is always present is always a list.** `listingChanges` and `imageChanges` are `[]` when empty, never `null`, so `len()` on them is safe without a nil check.

`warnings` is different: it is **omitted entirely** when there are none, in every result type that has it. Read it with a default (`.warnings // []` in jq, `d.get("warnings", [])` in Python) rather than indexing it directly.

| Command | Shape |
| --- | --- |
| `version` | `{version, commit, buildDate}` |
| `auth login` | `{envFile, stored[], kept[], validated}` |
| `auth status` | `{envFile, envFileExists, sources{MS_STORE_*: "flag"\|"environment"\|"env-file"\|""}, application?}` |
| `auth doctor` | `{ok, checks[{check, status, detail, source?, value?, path?, mode?, remedy?}]}` |
| `auth logout` | `{envFile, removed}` |
| `app info` | The application resource: `{id, primaryName, packageFamilyName, packageIdentityName, publisherName, firstPublishedDate, hasAdvancedListingPermission, lastPublishedApplicationSubmission, pendingApplicationSubmission}` |
| `locales list` | `["en-us", "it", …]` — a bare string array |
| `reviews list` | The API's review objects, unmodified |
| `submission status` | `[{type: "published"\|"pending", id, status, statusDetails}]` |
| `submission get` | The raw submission JSON (`fileUploadUrl` redacted unless `--include-upload-url`) |
| `submission watch` | `{status, statusDetails, classification: "success"\|"failed"\|"neutral"\|"in-progress"}` |
| `submission commit` | `{status, statusDetails, accepted, warning?}` |
| `submission delete-draft` | `{deletedSubmissionId}` |
| `rollout status\|finalize\|set-percentage` | `{isPackageRollout, packageRolloutPercentage, packageRolloutStatus, fallbackSubmissionId}` |
| `rollout halt` | `{rollout{…}, note}` |
| `publish msix` | `{submissionId, packageFileName, rolloutPercentage, draft, nextCommand?, commit{…}, warnings?}` |
| `listing show` | `{source, submissionId, localeCount, listings{<locale>: {…listing fields, imageCount, images?}}}` |
| `listing pull` | `{directory, source, submissionId, listingCount, remoteOnlyImages, matchedLocalImages}` |
| `listing push` | `{submissionId, draft, nextCommand?, plan{body, listingChanges, imageChanges}, commit{…}, warnings?}` |
| `listing push --dry-run` | `{dryRun, hasChanges, listingChanges, imageChanges, uploadCount, releaseNotes, body, warnings?}` |

Where a payload carries a human sentence — `auth doctor`'s `detail` — the same facts are repeated as fields, so nothing has to be parsed out of prose. A command whose whole point is a verdict says so directly: `auth doctor` returns `ok`, rather than making you scan statuses.

---

## Warnings

Warnings live in the **result payload's `warnings` array** in JSON mode, never as stderr prose — one place to look, no double-counting.

If a command fails before rendering a result, warnings collected during the attempt are attached to the **error object** instead. A "could not delete the draft I just created" is exactly the warning you must not lose, so it is never dropped.

---

## Credentials

Resolution order, first hit wins:

1. Flags (`--app-id`, and on `auth login` the credential flags)
2. `MS_STORE_*` environment variables
3. The credentials file

| Variable | Meaning |
| --- | --- |
| `MS_STORE_TENANT_ID` | Azure AD tenant id |
| `MS_STORE_CLIENT_ID` | Azure AD application (client) id |
| `MS_STORE_CLIENT_SECRET` | Azure AD client secret |
| `MS_STORE_APP_ID` | Microsoft Store product id |
| `PCENTER_ENV_FILE` | Credentials file path; default `~/.config/pcenter/credentials.env` (a leading `~` is expanded) |

In CI, use the environment variables from your secrets: they take precedence over the file and leave nothing on the runner to clean up.

A missing credential tells you where `pcenter` looked and what to run. It never prompts, with a single exception: `auth login` prompts when — and only when — stdin is a terminal, so it cannot hang an unattended job.

---

## Rules for agents

1. **Read before writing.** `listing show`, `submission status`, `rollout status` and `app info` are free and change nothing. Use them to establish state before any mutation.
2. **`--dry-run` first.** `listing push --dry-run` prints the exact diff and the would-be request body without creating anything. There is no reason to skip it.
3. **Never retry on exit 4.** A 409 means the operation is invalid for the current state. Fetch the state, decide again.
4. **One pending submission exists at a time.** The Store allows exactly one. If a mutation reports an existing draft, resolve it (`submission commit`, or `submission delete-draft --yes`) rather than passing `--replace-pending` blindly — that flag deletes whatever draft is there, including one a human left for inspection.
5. **`--skip-commit` is the safe half.** `publish msix --skip-commit` and `listing push --skip-commit` create a draft a human can inspect in Partner Center. Committing is a separate, explicit command.
6. **Ask before committing.** `listing push --yes`, `publish msix` without `--skip-commit`, `submission commit`, `rollout halt --yes` and `submission delete-draft --yes` change what the public sees or destroy work. Confirm with the human first.
7. **Timeouts are not failures.** The Store API returns 504 for operations that in fact succeeded, so every mutation verifies the resulting state rather than trusting the response code. Likewise, exhausting `--poll-attempts` means pcenter stopped watching, not that the submission failed — resume with `submission watch`.
8. **Don't print `--include-upload-url` output.** It contains a live SAS credential.

---

## Recipes

**Preflight in CI, with a readable diagnosis instead of a 401 halfway through a release:**

```bash
pcenter auth doctor --output json || exit 1
```

**Is anything in flight right now?**

```bash
pcenter submission status --output json | jq -r '.[] | "\(.type)\t\(.id)\t\(.status)"'
```

**What would this listing change actually do?**

```bash
pcenter listing push --dir assets/storecopy/microsoft-store --dry-run \
  | jq '{hasChanges, listingChanges, imageChanges, uploadCount}'
```

**Release, then hand the watch off:**

```bash
pcenter publish msix --path FeedFlow.msix --skip-commit \
  --release-notes assets/storecopy/microsoft-store-release-notes.json
# inspect the draft in Partner Center, then:
pcenter submission commit
```

**Rescue a rollout that stopped responding:**

```bash
pcenter rollout status --output json | jq '{packageRolloutStatus, packageRolloutPercentage}'
pcenter rollout finalize
```

**Branch on the failure class:**

```bash
pcenter listing push --dir ./store --yes
case $? in
  0) echo "shipped" ;;
  4) echo "state conflict — read submission status, do not retry"; pcenter submission status ;;
  5) echo "throttled — retry later" ;;
  3) echo "credentials rejected — needs a human" ;;
  *) echo "failed" ;;
esac
```

**Every review since a date, as JSON:**

```bash
pcenter reviews list --from 2026-01-01 --all --output json | jq -r '.[] | [.date, .market, .rating] | @tsv'
```
