# Troubleshooting

Failures you are likely to hit, what they mean, and what to do. Start with `pcenter auth doctor` for anything credential-shaped — it checks the whole setup at once and tells you which part is broken.

- [Credentials](#credentials)
- [Submissions](#submissions)
- [Rollouts](#rollouts)
- [Listings and metadata](#listings-and-metadata)
- [Publishing](#publishing)
- [Reading the output](#reading-the-output)

---

## Credentials

### "missing configuration" / exit 2

One or more of `MS_STORE_TENANT_ID`, `MS_STORE_CLIENT_ID`, `MS_STORE_CLIENT_SECRET`, `MS_STORE_APP_ID` could not be resolved. The error names which ones and where pcenter looked. Fix with either:

```bash
pcenter auth login
```

or by exporting the `MS_STORE_*` variables — they take precedence over the file.

### "env file … does not exist" / exit 2

You named a credentials file explicitly (`--env-file` or `PCENTER_ENV_FILE`) and it is not there. pcenter refuses to silently fall back, because failing later on a missing credential is worse than failing here. Create it with `pcenter auth login --env-file <path>`, or drop the flag to use the default.

A leading `~` is expanded, so `PCENTER_ENV_FILE=~/creds.env` works — but a quoted `'~/creds.env'` passed through a CI YAML value can still arrive as a literal path segment. Prefer an absolute path in CI, or just use the environment variables.

### Exit 3 — credentials rejected

The tenant, client id, or secret is wrong, or the secret expired. Azure AD client secrets expire; a setup that worked for months can stop overnight. Regenerate the secret in Azure AD and re-run `pcenter auth login`.

This is permanent — retrying will not fix it.

### `auth doctor` says the credentials file is world-readable

The file holds a client secret. Fix it with the `remedy` the check prints:

```bash
chmod 600 ~/.config/pcenter/credentials.env
```

### `auth login` exits instead of prompting

By design. Prompting happens only when stdin is a terminal, so the command can never hang a script or a CI job. The message names the flag to pass. Supply every value as a flag to run it unattended — or, in CI, don't use it at all.

---

## Submissions

### "app already has pending submission … resolve it or pass `--replace-pending`"

The Store allows **exactly one** pending submission per app. Something is already in flight. Look at it first:

```bash
pcenter submission status
```

Then choose:

- It is yours and finished → `pcenter submission commit`
- It is an abandoned draft → `pcenter submission delete-draft --yes`
- You know it is disposable and you are in CI → re-run with `--replace-pending`

Be careful with `--replace-pending`: it deletes *any* uncommitted draft, including one a human left in Partner Center for inspection.

### "refusing to delete submission … only PendingCommit drafts can be deleted"

The submission has moved past `PendingCommit` — it is in certification or already publishing. Deleting it is not yours to do from the API. Wait for it to reach a terminal status (`pcenter submission watch`), or cancel it in Partner Center.

### `submission watch` stops without a verdict

Reaching `--poll-attempts` means pcenter stopped watching, **not** that the submission failed. Certification can take hours. Re-run `pcenter submission watch`, or raise `--poll-attempts` / `--poll-seconds`.

### A commit "timed out" but the submission looks committed

The Store API returns 504 for operations that in fact succeeded. Every mutation in pcenter verifies the resulting state instead of trusting the response code, so this usually resolves itself. If you see it anyway, check the truth:

```bash
pcenter submission status
```

### The submission failed certification

`pcenter submission status` renders `statusDetails` — the errors, warnings and certification reports the Store attached. That is the actual reason; the status word alone (`CertificationFailed`) never is.

---

## Rollouts

### Exit 4 on any rollout command

A 409 means the operation is invalid for the current state: the submission is not published, or no rollout is in progress. **Do not retry** — read the state:

```bash
pcenter rollout status
```

### The rollout is stuck / the release never reached 100%

This is the failure the CLI was built for:

```bash
pcenter rollout status      # what does the Store actually think?
pcenter rollout finalize    # verified, so a 504 mid-finalize still resolves
```

### After halting a rollout, the next release looks wrong

Halting sets the rollout to `PackageRolloutStopped` at 0% — but **the next submission clones the halted submission, not the fallback**. `rollout halt` prints this as a note for exactly this reason. If you want the fallback's content, base the next submission on it deliberately.

---

## Listings and metadata

### "store.json is required; run listing pull for this app first"

`listing push` will not touch a directory without an identity marker. An empty or wrong directory would otherwise read as "delete everything". Seed it:

```bash
pcenter listing pull --dir <dir>
```

### The `store.json` appId does not match

You are pushing one app's metadata to another. Check `--app-id` / `MS_STORE_APP_ID` against `store.json`. Nothing was sent — the check runs before any request.

### "local metadata is missing Store locale(s) … pass `--allow-locale-removal`"

A locale exists in the Store but has no `listings/<locale>.json`. Usually that means a file was deleted or never pulled — not that you want to drop a Store language. Restore the file, or pass `--allow-locale-removal` if removing the locale is genuinely what you want.

### Screenshots I did not touch show up as changes

Check the manifest. A changed `sha256` means the file was edited (that is a `replace`: delete + upload). Images the Store holds with no local counterpart are `remoteOnly` and are left alone — as is any server image absent from the manifest, so a damaged manifest cannot mass-delete screenshots.

### An image is rejected before anything is created

Validation runs locally, before a submission exists. The limits: PNG only, ≤ 50 MB, desktop screenshots ≥ 1366×768, icons exactly 300×300, ≤ 10 desktop screenshots per locale (≤ 8 for other device families), captions ≤ 200 characters. Full table in [Metadata directory](METADATA.md#validated-locally-before-anything-is-created).

### `listing pull` wrote no image files

Correct — the Submission API does not expose image binaries for download. Pull records what the Store holds in `images-manifest.json` so push can tell your files apart from the server's, but the files themselves have to come from your repo.

### "exactly one of --dry-run, --skip-commit, or --yes is required"

`listing push` has no default mode. The difference between them is the difference between printing a diff and shipping to the Store, so it has to be stated. Start with `--dry-run`.

---

## Publishing

### "exactly one of --release-notes or --keep-existing-release-notes is required"

`publish msix` refuses to guess. Without this guard, a forgotten flag would silently ship the *previous* version's changelog. Pass the notes file, or say explicitly that keeping the cloned notes is what you meant.

### "release notes file … is missing notes for Store listing locale(s): …"

A locale exists in the Store but not in your notes file. This is a hard failure on purpose — an empty changelog in someone's language is worse than a failed command. Add the locales, or check what the Store actually has:

```bash
pcenter locales list
```

The reverse (a locale in your file that the Store does not have) is only a warning.

### A publish failed — is there a draft left behind?

Any failure after the draft was created but before the commit started deletes the draft automatically. If that cleanup itself fails, it is reported as a warning (in the `warnings` array in JSON, on stderr in table mode) — never silently dropped. Confirm with `pcenter submission status`.

### The upload took a long time and then failed

Package uploads refresh the SAS URL if it expires mid-upload, so long uploads are handled. A persistent failure is worth re-running with `--verbose`: the request log is redacted (secrets, tokens, and the SAS signature) and safe to paste into an issue.

---

## Reading the output

### I piped the output and got JSON instead of a table

That is the design: table on a terminal, JSON when piped, so an agent or a script gets machine-readable output without knowing to ask. Force either with `--output table` / `--output json`.

### The error message is one line and I want the explanation

The long human explanation is printed in **table mode** only. In JSON mode the same facts are structured fields on the error object (`missing`, `envFile`, `remedy`, `correlationId`, …) so nothing has to be parsed out of prose. Re-run with `--output table` to read it as a person.

### Which exit code meant what?

`0` success · `1` unclassified · `2` fix your usage or configuration · `3` credentials rejected · `4` invalid for the current state, never retry · `5` throttled, retry later. Details in [the automation contract](AUTOMATION.md#exit-codes).
