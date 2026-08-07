# pcenter

A command-line tool for the **Microsoft Store**, driving the Partner Center Submission API: publish an MSIX, manage your listing and screenshots from files in your repo, read reviews, and rescue a submission or rollout that has got stuck.

Built for [FeedFlow](https://github.com/prof18/feed-flow) and app-agnostic. Written for CI jobs and agents as much as for people at a terminal — table output on a terminal, JSON when piped, structured errors and exit codes you can branch on.

> **Status:** `v0.0.1` — a preview release. Every command is implemented and validated against the live Store API, but the tool has not yet driven a real release end to end from CI. The command surface is expected to hold; report the rough edges.

---

## Install

**Homebrew** (macOS and Linux):

```bash
brew install prof18/tap/pcenter
```

**From a release** — five archives (`darwin` arm64/amd64, `linux` arm64/amd64, `windows` amd64) plus `checksums.txt` on every [release](https://github.com/prof18/pcenter-cli/releases). Pin a version and verify the checksum; the recipes for CI runners, including Windows, are in [docs/CI.md](docs/CI.md).

**From source:**

```bash
go install github.com/prof18/pcenter-cli/cmd/pcenter@latest
```

---

## Quick start

You need an Azure AD tenant id, client id and client secret with Partner Center access, and your Store **product id** — the 12-character id from Partner Center, shaped like `9NXXXXXXXXXX`.

```bash
# 1. Store your credentials (prompts only when you're at a terminal)
pcenter auth login

# 2. Prove the whole setup works
pcenter auth doctor

# 3. Look around — none of these change anything
pcenter app info
pcenter locales list
pcenter listing show --locale en-us
pcenter submission status
```

In CI, skip `auth login` and set `MS_STORE_TENANT_ID`, `MS_STORE_CLIENT_ID`, `MS_STORE_CLIENT_SECRET` and `MS_STORE_APP_ID` from your secrets — they take precedence over the credentials file and leave nothing on the runner.

### Publish a release

```bash
pcenter publish msix --path FeedFlow.msix \
  --release-notes assets/storecopy/microsoft-store-release-notes.json \
  --rollout-percentage 90
```

That creates the submission, uploads the package, and commits it. Add `--skip-commit` to stop at an inspectable draft and commit later with `pcenter submission commit`. Release-notes intent is mandatory — either a notes file or an explicit `--keep-existing-release-notes` — so a forgotten flag can't silently ship the previous version's changelog.

### Manage your listing from files

```bash
pcenter listing pull --dir assets/storecopy/microsoft-store   # snapshot into your repo
$EDITOR assets/storecopy/microsoft-store/listings/en-us.json  # edit, commit, review
pcenter listing push --dir assets/storecopy/microsoft-store --dry-run
pcenter listing push --dir assets/storecopy/microsoft-store --yes
```

`push` requires you to say which of `--dry-run`, `--skip-commit` or `--yes` you meant. A push into a directory belonging to a different app fails on the identity marker before any request is sent; locales and screenshots you have not put under management are left alone.

---

## What it does

| | |
| --- | --- |
| `auth login \| status \| doctor \| logout` | Credential setup, resolution, and a full health check that works as a CI preflight |
| `app info` | The application resource as the API returns it |
| `locales list` | The Store listing locales |
| `reviews list` | Reviews, with date, market and rating filters and paging |
| `listing show` | Print the listing to stdout — no directory, no cleanup |
| `listing pull \| push` | The listing as files in your repo, with a diff before you send it |
| `publish msix` | Create, upload, and commit an MSIX submission |
| `submission status \| get \| watch \| commit \| delete-draft` | Inspect and drive submissions |
| `rollout status \| set-percentage \| finalize \| halt` | Staged rollouts, including rescuing a stuck one |

Full reference: [docs/COMMANDS.md](docs/COMMANDS.md), or `pcenter <command> --help`.

---

## Built for automation

- **Output** is a table on a terminal and JSON when piped, so an agent or a CI job gets machine-readable results without knowing to ask. `--output` forces either.
- **Errors** are structured, not prose: a stable `code`, a one-line `message`, and fields for anything you would otherwise have to parse out of text — which settings are missing, the correlation id, a suggested remedy.
- **Exit codes** separate the cases that need different responses: `2` fix your configuration, `3` credentials rejected, `4` invalid for the current state (permanent — do not retry), `5` throttled (retry later).
- **Mutations verify.** The Store API returns 504 for operations that in fact succeeded, so every mutation checks the resulting state rather than trusting the response code — a timeout mid-finalize resolves instead of leaving you guessing.
- **Secrets are redacted centrally** — the client secret, bearer tokens, and the SAS signature on upload URLs — so `--verbose` output is safe to paste into an issue.
- **Nothing prompts** except `auth login`, and only when stdin is a terminal. Destructive operations require an explicit flag.

The full contract, including JSON shapes per command and rules for agents driving the CLI: [docs/AUTOMATION.md](docs/AUTOMATION.md).

---

## Documentation

| | |
| --- | --- |
| [Commands](docs/COMMANDS.md) | Every command and flag, and the behavior behind them |
| [Metadata directory](docs/METADATA.md) | Listing files, image manifest, release-notes contract |
| [Automation contract](docs/AUTOMATION.md) | JSON shapes, error codes, exit codes, agent rules |
| [CI](docs/CI.md) | Installing on a runner, credentials from secrets, release workflows |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | What each failure means and what to do |
| [Changelog](CHANGELOG.md) | What changed in each release |

Design notes and implementation history live in [`plan/`](plan/INDEX.md).

---

## Acknowledgements

The shape of this CLI owes a lot to [**App Store Connect CLI** (`asc`)](https://github.com/rorkai/App-Store-Connect-CLI) by [@rorkai](https://github.com/rorkai) — the same job on the other side of the fence. Its command hierarchy, TTY-aware output defaults, and treatment of documentation as part of the tool rather than an afterthought all shaped the decisions here. If you ship to the App Store as well, go use it.

Prior art on the Microsoft side: [StoreBroker](https://github.com/microsoft/StoreBroker) for submission payload and ZIP semantics, and Microsoft's own [msstore-cli](https://github.com/microsoft/msstore-cli) for blob upload and image layout conventions.

---

Licensed under the [Apache License 2.0](LICENSE).
