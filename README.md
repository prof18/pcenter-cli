# pcenter

A command-line tool for the **Microsoft Store**. Publish an MSIX, manage listing content and screenshots from files in your repository, read reviews, and recover a stuck submission or rollout.

Use it at a terminal or in CI: it prints tables for people, JSON for scripts, and structured errors and exit codes for reliable automation.

> **Status:** pcenter has created, uploaded, committed, and staged Microsoft Store releases from GitHub Actions. The command surface is stable.
>
> It covers publishing, listings, submissions, rollouts, and reviews. See [Scope](#scope) for what remains in Partner Center.

---

## Install

**Homebrew** (macOS and Linux):

```bash
brew install prof18/tap/pcenter
```

**From a release** — download the archive for your platform from [releases](https://github.com/prof18/pcenter-cli/releases) and verify its checksum. For CI runners, including Windows, see [docs/CI.md](docs/CI.md).

**From source:**

```bash
go install github.com/prof18/pcenter-cli/cmd/pcenter@latest
```

---

## Get started

### Before you log in

`pcenter` does not create or grant credentials. In Partner Center, create or add a Microsoft Entra application and give it access to your account. For submission API access, Microsoft calls out the **Manager** role; follow [Microsoft's setup guide](https://learn.microsoft.com/en-us/partner-center/marketplace-offers/submission-api-onboard).

You need these values:

| Value | Where to find it |
| --- | --- |
| Tenant ID | The Microsoft Entra directory associated with your Partner Center account |
| Client ID | The Microsoft Entra application added to Partner Center |
| Client secret | A key created for that application; copy it when you create it |
| Store product ID | The 12-character ID for the app in Partner Center, for example `9NXXXXXXXXXX` |

The first three authenticate the application. The product ID selects the Store app; you can provide it during login or later with `--app-id`.

```bash
# 1. Store your credentials (prompts only when you're at a terminal)
pcenter auth login

# 2. Check your setup
pcenter auth doctor

# 3. Inspect the app — none of these commands make changes
pcenter app info
pcenter locales list
pcenter listing show --locale en-us
pcenter submission status
```

For non-interactive setup, pass `--tenant-id`, `--client-id`, `--client-secret`, and optionally `--app-id` to `pcenter auth login`. See `pcenter auth login --help` for details.

In CI, skip `auth login` and set `MS_STORE_TENANT_ID`, `MS_STORE_CLIENT_ID`, `MS_STORE_CLIENT_SECRET` and `MS_STORE_APP_ID` from your secrets — they take precedence over the credentials file and leave nothing on the runner.

---

## Common workflows

### Publish a release

```bash
pcenter publish msix --path MyApp.msix \
  --release-notes store/microsoft-release-notes.json \
  --rollout-percentage 90
```

This creates the submission, uploads the package, and commits it. Add `--skip-commit` to leave the submission as a draft and commit it later with `pcenter submission commit`.

You must provide a notes file or explicitly pass `--keep-existing-release-notes`. The file must cover every Store locale. Its format and validation rules are in [Release notes](docs/METADATA.md#release-notes-file).

### Manage your listing from files

```bash
pcenter listing pull --dir store/microsoft          # snapshot into your repo

# edit store/microsoft/listings/en-us.json, commit it, review the diff

pcenter listing push --dir store/microsoft --dry-run
pcenter listing push --dir store/microsoft --yes
```

`push` requires one of `--dry-run`, `--skip-commit`, or `--yes`. Before sending a request, pcenter checks that the metadata directory belongs to the selected app. Locales and screenshots that are not in that directory are left unchanged.

---

## What it does

| | |
| --- | --- |
| `auth login \| status \| doctor \| logout` | Set up credentials, check how they are resolved, and verify the connection |
| `app info` | Show app details |
| `locales list` | List Store locales |
| `reviews list` | List reviews with date, market, sorting, and paging options |
| `listing show` | Print the listing to stdout — no directory, no cleanup |
| `listing pull \| push` | Pull or update listing files in your repository, with a diff before changes are sent |
| `publish msix` | Create, upload, and commit an MSIX submission |
| `submission status \| get \| watch \| commit \| delete-draft` | Inspect and drive submissions |
| `rollout status \| set-percentage \| finalize \| halt` | Manage staged rollouts, including stuck ones |
| `completion` | Generate completion scripts for Bash, Zsh, Fish, and PowerShell |
| `version` | Version, commit and build date |

Full reference: [docs/COMMANDS.md](docs/COMMANDS.md), or `pcenter <command> --help`.

---

## Scope

`pcenter` publishes builds and manages listing content. It preserves unsupported settings from the previous submission, so releases do not reset settings you made in Partner Center. Change those settings in Partner Center.

Not covered, deliberately:

| | |
| --- | --- |
| Pricing, availability, markets, visibility | Preserved. Submissions created by pcenter always publish immediately; scheduled publish dates are not supported. |
| Properties — category, privacy policy, support contact, website | The category is preserved. Manage URLs on Partner Center's Properties page. |
| Age ratings and content declarations | Set once in Partner Center. |
| Add-ons (in-app products) and package flights | Separate API surfaces, not this one. |
| Package formats other than MSIX | `publish msix` is the only publish path. |
| Creating an app, and analytics beyond `reviews list` | Out of scope. |

If you need one of these often, please open an issue.

---

## Built for automation

- **Output:** tables at a terminal and JSON when piped. Use `--output` to choose either format explicitly.
- **Errors:** a stable `code`, a one-line `message`, and useful fields such as missing settings, a correlation ID, and a suggested fix.
- **Exit codes:** `2` configuration problem, `3` rejected credentials, `4` invalid current state (do not retry), `5` throttled (retry later).
- **Changes are verified.** The Store API can return a 504 after an operation succeeds, so pcenter checks the resulting state instead of trusting the response alone.
- **Secrets are redacted** from verbose output, including client secrets, bearer tokens, and upload URL signatures.
- **No prompts** except `auth login`, and then only at a terminal. Destructive operations need an explicit flag.

For JSON shapes, exit codes, and safe automation rules, see [docs/AUTOMATION.md](docs/AUTOMATION.md).

---

## Documentation

| | |
| --- | --- |
| [Commands](docs/COMMANDS.md) | Every command and flag, and the behavior behind them |
| [Metadata directory](docs/METADATA.md) | Listing files, image manifest, release-notes contract |
| [Automation](docs/AUTOMATION.md) | JSON output, errors, exit codes, and safe automation rules |
| [CI](docs/CI.md) | Installing on a runner, credentials from secrets, release workflows |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | What each failure means and what to do |
| [Changelog](CHANGELOG.md) | What changed in each release |

Design notes and implementation history live in [`plan/`](plan/INDEX.md).

---

## Acknowledgements

The command structure, terminal-aware output, and documentation approach were inspired by [**App Store Connect CLI** (`asc`)](https://github.com/rorkai/App-Store-Connect-CLI) by [@rorkai](https://github.com/rorkai). If you also ship to the App Store, it is well worth using.

Prior art on the Microsoft side: [StoreBroker](https://github.com/microsoft/StoreBroker) for submission payload and ZIP semantics, and Microsoft's own [msstore-cli](https://github.com/microsoft/msstore-cli) for blob upload and image layout conventions.

---

Licensed under the [Apache License 2.0](LICENSE).
