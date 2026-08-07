# 09 — Manual Live Testing Against FeedFlow

A pass over every command and path against the real Store, by hand. The automated suite
proves behaviour against the fakestore; this proves the fakestore was right about the real
API.

M3 and M4 already validated the headline flows live (see [TODO.md](TODO.md)). This is the
systematic sweep: every flag, every guard, every error path.

## Ground rules

1. **Only one pending submission can exist.** Every Tier 1 scenario creates a draft and must delete it before the next one starts. If a real FeedFlow release is in flight, stop — none of this can run.
2. **Never commit in Tier 1.** A committed submission enters certification and publishes to real users; there is no clean undo. `--skip-commit` on everything, `submission delete-draft --yes` after.
3. **The published listing is the thing to protect.** 25 locales, 101 images, all `remoteOnly`. A `--yes` push with a bad metadata dir would rewrite it.
4. Log the outcome of each check in the results table at the bottom, with the date.

## Setup

### Getting `pcenter` on your PATH

Until the first release lands there is nothing to `brew install`, so put the locally built
binary on PATH yourself:

```bash
script/dev-install.sh
```

That builds to the repo root (gitignored) and symlinks `~/.local/bin/pcenter` at it, so
`pcenter` resolves from any directory. Because it links rather than copies, a later
`go build -o pcenter ./cmd/pcenter` is picked up with no re-linking.

It also stamps the version from `git describe` instead of leaving it `dev`, so
`pcenter version` names the exact build — worth having when a result in the log needs
tracing back to a commit. Check it before you start:

```bash
pcenter version --output table
```

After M5 this whole step becomes `brew install prof18/tap/pcenter`.

### Credentials

There is no credentials file on this machine (`~/.feedflow/microsoft-store.env`, which
`plan/LOCAL.md` names, does not exist). Create one:

```bash
pcenter auth login
```

It prompts for tenant id, client id and client secret — the secret without echo — verifies
them against the Store before writing anything, and stores them `0600` at
`~/.config/pcenter/credentials.env`. Pass `--tenant-id/--client-id/--client-secret` to run
it unattended instead.

Leave the app id out: it is passed per command with `--app-id`, or set once in the
environment. Then confirm the whole setup:

```bash
pcenter auth doctor
```

### A scratch directory

Work in a scratch directory, **not** feed-flow's repo — seeding the real
`assets/storecopy/microsoft-store/` is a separate M5 task and shouldn't be entangled with
testing.

```bash
mkdir -p ~/tmp/pcenter-live && cd ~/tmp/pcenter-live
```

Take a pristine baseline first. This is both the first test and the restore path if
anything goes wrong later:

```bash
pcenter listing pull --dir ./baseline
cp -R baseline work        # edit `work`, keep `baseline` untouched
```

Also needed for the publish scenarios: a real FeedFlow MSIX with a version above the
published one. Build it, or reuse the artifact from a recent CI run.

---

## Tier 0 — Read-only

Safe to run at any time, in any order, including during a release.

**Credential setup** (see also Setup above)

- [ ] `auth login` with no flags on a terminal → prompts, secret not echoed, file written `0600`
- [ ] `auth login --tenant-id ... --client-id ... --client-secret ...` unattended → no prompt
- [ ] `auth login` with a deliberately wrong secret → fails, and **nothing is written**
- [ ] `auth login --app-id <id>` afterwards → app id added, account credentials preserved
- [ ] `auth login` piped from `/dev/null` (no TTY, no flags) → exits 2 naming the flag, does not hang
- [ ] `auth status --offline` → resolution table only, no network
- [ ] `auth doctor` with everything set → all checks ok, exit 0
- [ ] `auth doctor` with `MS_STORE_CLIENT_SECRET` unset → reports it, exit 1, secret never printed
- [ ] `auth doctor` after `chmod 644` on the credentials file → warns about permissions
- [ ] `PCENTER_ENV_FILE='~/...'` quoted → tilde expanded, file found
- [ ] `auth logout` without `--yes` → refuses; with `--yes` → removes; twice → still exit 0

**Commands**

- [ ] `auth status` — succeeds with a token
- [ ] `app info`
- [ ] `locales list` — expect 25 locales
- [ ] `submission status` — pending + published
- [ ] `submission get` — pending draft (or a clear "none" when there isn't one)
- [ ] `submission get --published`
- [ ] `submission get --id <published id>`
- [ ] `rollout status` — includes `fallbackSubmissionId`
- [ ] `version`

**`listing show` — read-only, writes nothing**

- [ ] `show` with no flags → every locale; confirm no files were created anywhere
- [ ] `show --locale en-us` → full text for that locale only
- [ ] `show --locale EN-US` → same result (case-insensitive)
- [ ] `show --locale zz` → error naming `locales list`, not an empty result
- [ ] `show --images` → captions and Store ids present; counts match `pull`'s manifest
- [ ] `show --published --pending` → exit 2
- [ ] `show --pending` while a draft exists (Tier 1) → draft text, `source: pending`

**`reviews list` — the flag surface**

- [ ] bare (wide default date range) returns real reviews
- [ ] `--from 2026-01-01 --to 2026-06-30` — confirms the M/D/YYYY conversion the API wants
- [ ] `--market US`
- [ ] `--top 5` — page size respected
- [ ] `--skip 5` — offset, second page differs from the first
- [ ] `--all` — follows `@nextLink` past the first page
- [ ] `--orderby "date asc"` — order flips
- [ ] `--filter <raw>` — passthrough works

**Secret redaction — do this one carefully**

- [ ] `submission get` **without** `--include-upload-url` → no SAS signature in output
- [ ] `submission get --include-upload-url` → URL present (this is the opt-in)
- [ ] `--verbose` on any command, output piped to a file → grep it for the client secret, the bearer token, and `sig=`. None may appear. This is the check that decides whether `--verbose` output is safe to paste into an issue.

**Output and config resolution**

- [ ] `--output table` and `--output json` both render; bare command on a TTY gives table, piped gives JSON
- [ ] `--app-id` overrides the env file
- [ ] `MS_STORE_*` process env vars win over the env file
- [ ] `--env-file <path>` and `PCENTER_ENV_FILE` both resolve
- [ ] Missing env file **and** no env vars → clear error, exit 2
- [ ] Bad credentials → auth failure names the problem, no stack trace, secret not echoed
- [ ] `--app-id` pointing at a non-existent product → clear API error including the correlation id

---

## Tier 1 — Draft-creating

Each scenario: create the draft, inspect it with `submission get`, then delete it.

```bash
pcenter submission delete-draft --yes     # between every scenario
```

### `listing pull`

- [ ] `--dir ./baseline` (done in setup) — 25 locale files, `images-manifest.json` with 101 `remoteOnly` entries, `store.json` with the right `appId`
- [ ] Second pull into a fresh dir → byte-identical to the first (idempotent)
- [ ] `--pending` while a draft exists → pulls the draft, not the published listing
- [ ] `--published` explicitly → same as the default

### `listing push` — guards (no draft created; these should all fail early)

- [ ] no mode flag → exit 2, "exactly one of"
- [ ] two mode flags (`--dry-run --yes`) → exit 2
- [ ] `--dir` at an empty directory → refused on the missing `store.json`
- [ ] `--dir` at a dir whose `store.json` has a different `appId` → refused
- [ ] managed manifest entry whose local file was deleted → validation error, **no submission created** (confirm with `submission status`)

### `listing push` — client-side limits

All of these must fail before any API call. Edit `work/`, run `--dry-run`, confirm the
error and that no draft appeared.

- [ ] `features` with 21 items
- [ ] `recommendedHardware` with 12 items
- [ ] image `description` over 200 characters
- [ ] a non-PNG image file
- [ ] a desktop screenshot under 1366×768
- [ ] an 11th desktop screenshot in one locale

### `listing push` — the real paths

- [ ] `--dry-run` on the pristine `baseline` → "no changes", zero listing/image/upload changes
- [ ] `--dry-run` after a text edit → diff names the locale and field
- [ ] `--skip-commit` with a text edit → inspect the draft: edited locale changed, other 24 untouched, all 101 images still `Uploaded`, zero `PendingDelete`
- [ ] add a screenshot → `PendingUpload`, ZIP uploaded, existing images untouched
- [ ] replace a screenshot (same path, new content → new sha256) → old one `PendingDelete`, new one `PendingUpload`
- [ ] `delete: true` on a manifest entry with a `storeId` → that image `PendingDelete`, nothing else touched
- [ ] add `listings/en-gb.json` → 26 locales in the draft (repeat of the M4 check, confirming it still holds)
- [ ] a locale file removed **without** `--allow-locale-removal` → locale retained, no removal
- [ ] `--release-notes <file>` on an otherwise-unchanged dir → notes applied, nothing else changes
- [ ] `--replace-pending` with a draft already present → old draft deleted, new one created
- [ ] Two changes at once (text + image) in one push → both land in a single submission

**Deferred by maintainer direction:** `--allow-locale-removal` actually removing a locale,
and the caption-only edit on an `Uploaded` image — both need real listing content to be
destroyed or a CLI-managed image to exist first (docs [05](05-metadata-directory.md) §Locale
add/remove, TODO M5).

### `publish msix`

- [ ] `--release-notes` and `--keep-existing-release-notes` together → exit 2
- [ ] neither → exit 2
- [ ] release-notes file missing a Store locale → hard fail, no submission created
- [ ] release-notes file with an extra unused locale → warning, proceeds
- [ ] `--skip-commit --release-notes <file>` with a real MSIX → inspect the draft: package attached and pending, 25 locales with exact release-note text, rollout at 90%
- [ ] `--rollout-percentage 25` → reflected in the draft
- [ ] `--keep-existing-release-notes` → cloned notes retained unchanged
- [ ] `--replace-pending` with a draft present → replaces it
- [ ] **Cleanup on failure**: point `--path` at a corrupt or non-MSIX file so the flow fails after the submission is created → confirm the draft was deleted (`submission status` shows no pending), and the temp ZIP is gone

### `submission`

- [ ] `watch` against a pending draft → reports `PendingCommit`, doesn't hang forever
- [ ] `watch --poll-attempts 2 --poll-seconds 5` → gives up cleanly at the limit
- [ ] `delete-draft` **without** `--yes` → refuses
- [ ] `delete-draft --yes` when no draft exists → clear message, not a crash
- [ ] `get --id <a deleted submission id>` → clean 404-shaped error

---

## Tier 2 — Irreversible

These publish to real users or change a live rollout. They can only be exercised as part of
an actual FeedFlow release, not on demand.

**During the next real release, in this order:**

- [ ] `publish msix` without `--skip-commit` (or `--skip-commit` then `submission commit`) — the two-phase path is the one feed-flow CI will use
- [ ] `submission watch` through the real status transitions into certification
- [ ] `rollout status` once the release is live at 90%
- [ ] `rollout set-percentage 95` → verify the new percentage reads back
- [ ] `rollout finalize` → rollout completes at 100%

**`rollout halt` — the one to think about.** It cannot be undone, and the trap is what
happens next: the *following* submission clones the **halted** one, not the last good one.
Either accept a release you're willing to halt, or leave this to the first real incident and
treat the fakestore coverage as sufficient until then. Decide deliberately rather than
discovering it mid-incident.

---

## What live testing cannot cover

Worth being explicit, so nobody reads a green sweep as more than it is:

- **The 504-but-succeeded paths** — the resilience the whole client is built around. They cannot be provoked on demand; the fakestore is the only coverage and that is fine.
- **429 / `Retry-After`, 401-refresh-and-replay** — same. Rate limits won't cooperate on schedule.
- **Genuine mid-upload blob failures** and the `fileUploadUrl` 403 refresh.

---

## Results

| Date | Tier | Scenario | Result | Notes |
| --- | --- | --- | --- | --- |
| | | | | |
