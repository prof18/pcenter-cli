# 02 — Architecture and Locked Decisions

## Locked decisions

| Decision | Choice |
| --- | --- |
| Language | Go (latest stable), heavy TDD |
| CLI framework | `spf13/cobra` |
| HTTP | stdlib `net/http` for the Store API; custom retry logic (semantics are endpoint-specific, no generic retry lib). One exception: blob uploads use the Azure `azblob` SDK for chunked upload (doc 03) |
| Binary name | `pcenter` (= Partner Center, the portal/API this drives; deliberately distinct from Microsoft's official `msstore` CLI) |
| Repo | This repo (`pcenter-cli`), standalone, app-agnostic |
| Distribution | GitHub Releases built by the repo's own release workflow (no goreleaser), plus a Homebrew formula in [`prof18/homebrew-tap`](https://github.com/prof18/homebrew-tap) — see §Distribution below |
| Metadata model | File-based pull/push; the canonical metadata dir lives in the consuming app's repo (feed-flow) and is passed via `--dir` |
| Interactivity | None, with one exception: `auth login` may prompt, and only when stdin is a terminal (see §Credential setup). Everything else is JSON-first and never prompts. Destructive ops require explicit flags |
| License | Apache-2.0 (matches feed-flow). Repo is **local-only during development**; the maintainer publishes it to GitHub at the end (M5). Everything must still be written as public-ready from commit one — no secrets, no machine paths |

## Repo layout

```
pcenter-cli/
  cmd/pcenter/main.go
  internal/cli/            # cobra command tree, flag parsing
  internal/config/         # credential/flag/env resolution
  internal/store/          # API client: auth, transport, retry, typed endpoints
  internal/store/types/    # submission/listing/rollout/review JSON types
  internal/submission/     # higher-level flows: publish, push, draft adoption, rollout finalize
  internal/metadata/       # metadata dir read/write, manifest, image diffing
  internal/output/         # table/json renderers, TTY detection
  internal/fakestore/      # httptest fake Partner Center for tests (scenario-scriptable)
  test/e2e/                # compiled-binary tests against fakestore
  plan/                    # this plan
  docs/                    # per-command docs + metadata dir format spec
  CHANGELOG.md             # release notes source; the release workflow refuses to publish without a section
  .github/workflows/ci.yml       # go test + golangci-lint on linux/macos/windows
  .github/workflows/release.yml  # cross-compile + publish on tag
```

## Config & auth

Resolution order (first wins): explicit flags → process env vars (`MS_STORE_*`) → env file.

- `--env-file` flag, default `~/.config/pcenter/credentials.env` on every OS (`~` = `os.UserHomeDir()`; deliberately not `os.UserConfigDir()`, so the path is identical in docs for macOS/Linux/Windows), overridable via `PCENTER_ENV_FILE`. Format: plain `KEY=VALUE` lines. A missing default file is not an error when the `MS_STORE_*` vars are already set in the environment (the CI case).
- `--app-id` overrides `MS_STORE_APP_ID`.
- Token: `POST https://login.microsoftonline.com/{tenant}/oauth2/token`, form-urlencoded, `grant_type=client_credentials`, `resource=https://manage.devcenter.microsoft.com`. Acquire once per process; no disk caching (tokens last ~60 min, processes are short).
- `PCENTER_API_BASE` / `PCENTER_LOGIN_BASE` env overrides (undocumented; used by e2e tests to point at fakestore).

## Credential setup

Resolution order stays flags → `MS_STORE_*` env vars → env file, but the file no longer has to be placed by hand. `pcenter auth login` writes it (`0600`, in a `0700` directory), `auth doctor` diagnoses it, `auth logout` removes it. A leading `~` in `--env-file`/`PCENTER_ENV_FILE` is expanded, since quoting or a CI YAML value otherwise leaves a literal tilde that silently matches nothing.

**The prompt exception.** The "no interactivity" rule exists so a command can never hang a CI job waiting on input. `auth login` prompts only when stdin is a terminal; without one it exits with a message naming the flag to pass. Every value is also a flag, so the command is fully scriptable, and the client secret is read without echo.

**In CI, don't use the file.** `MS_STORE_*` from repository secrets already take precedence and leave nothing on the runner to clean up. `auth doctor` is the CI-appropriate use of this surface: a preflight that fails with a readable diagnosis instead of a 401 halfway through a publish.

Credentials are verified against the Store before being written, so a mistyped secret fails at setup rather than during a release.

## Output conventions

- Global `--output json|table`; TTY-aware default (table on TTY, json when piped). Agents and CI invoke through a pipe, so they get JSON without having to ask.
- `--verbose` logs requests **and response bodies** to stderr with secrets redacted (centralized redaction layer, see doc 03 §Security).
- Results are bare data, not an envelope — same idiom as `gh` and `aws`, and jq-friendly.
- `pcenter version` / `--version`: version, commit, build date injected into `main` via `-X` ldflags at release time (defaults `dev`/`unknown` for local builds).

### The machine contract

The primary consumers are agents and CI, so the failure surface is structured, not prose.

**Errors.** With `--output json`, stderr gets one JSON object:

```json
{"error":{"code":"state_conflict","message":"…one line…","correlationId":"…","statusCode":409,"remedy":"…"}}
```

`code` is stable and may not change meaning once released; `message` is a single line and may be reworded freely. Error-specific fields (`missing`, `envFile`, `remedy`, `correlationId`, `retryAfter`, …) carry anything a caller would otherwise have to parse out of the text. The multi-line human explanation appears **only in table mode**.

Codes: `usage`, `missing_configuration`, `env_file`, `auth_failed`, `state_conflict`, `not_found`, `rate_limited`, `api_error`, `validation`, `failure`.

**Exit codes** let a shell branch without reading stdout at all:

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | unclassified failure |
| 2 | usage, or configuration that must be fixed before retrying |
| 3 | credentials rejected — permanent, needs a human |
| 4 | operation invalid for current state (409) — permanent, never retry unchanged |
| 5 | throttled beyond the retry budget — retry later |

The 3/4/5 split exists because those demand different responses: re-authenticate, give up, or back off. Collapsing them into 1 forces agents to regex the message.

**Success payloads** are typed, never stringly. Booleans stay booleans (`isPackageRollout`, `draft`, `accepted`, `hasChanges`), numbers stay numbers (`packageRolloutPercentage`), `locales list` is a bare string array, and `submission get`/`reviews list` pass the API's own JSON through untouched. Where a payload carries a human sentence — `auth doctor`'s `detail` — the same facts are repeated as fields (`source`, `value`, `path`, `mode`, `remedy`) so nothing has to be parsed out of prose. A command whose whole point is a verdict says so directly: `auth doctor` returns `{"ok": false, "checks": [...]}` rather than making a caller scan statuses.

**Warnings** live in the result payload's `warnings` array in JSON mode, never as stderr prose — one place to look, no double-counting. If a command fails before rendering a result, warnings collected during the attempt are attached to the error object instead, so a "could not delete the draft I just created" is never lost.

## Guardrails

- Mutating commands never prompt; destructive ones (`submission delete-draft`, `rollout halt`) require `--yes`; `listing push` requires exactly one of `--dry-run | --skip-commit | --yes`.
- `publish msix --skip-commit` / `listing push --skip-commit` leave an uncommitted draft for later `pcenter submission commit`.
- Any failure after creating a submission but before commit deletes the draft (best-effort, warn on failure) — same as the ps1.
- Secrets never reach logs or output: `client_secret`, bearer tokens, and the SAS query string of `fileUploadUrl` are redacted centrally (doc 03 §Security).

## Distribution

Two channels, one set of artifacts. Mirrors how [`regesto`](https://github.com/prof18/regesto) ships, so both Go tools have the same release idiom.

**Artifacts** — `release.yml` fires on a `v*` tag and cross-compiles in a plain shell loop (no goreleaser: the build is four `go build` calls, and a release tool would be one more thing to pin and upgrade):

| Target | Archive | Consumer |
| --- | --- | --- |
| `darwin/arm64`, `darwin/amd64`, `linux/arm64`, `linux/amd64` | `.tar.gz` | Homebrew formula |
| `windows/amd64` | `.zip` | feed-flow CI pinned download (doc 07 §3) |

The workflow stamps the version via ldflags, smoke-tests the built `linux/amd64` artifact before publishing, takes release notes from the matching `CHANGELOG.md` section and **fails the release if that section is missing**, then publishes archives + `checksums.txt` with `gh release create`.

**Homebrew** — `brew install prof18/tap/pcenter` is the channel humans are pointed at. The formula ships the release binaries rather than building from source, so the reported version matches the release it came from. It covers macOS and Linux only; Homebrew has no Windows channel, so feed-flow's Windows CI keeps the pinned download + sha256 verification.

The tap keeps itself current: a workflow *in the tap repo* watches for new releases and rewrites the formula from the release's own `checksums.txt`, pushing only after the formula parses as Ruby, every archive URL answers 200, and `brew install` + `brew test` succeed on the runner. Watching from that side means neither repo stores a credential for the other — a goreleaser `brews:` block would have needed a long-lived cross-repo token.

## Supply-chain & repo security

- GitHub Actions pinned to commit SHAs; workflow `permissions` minimal (`contents: read` for CI; release workflow gets `contents: write` only).
- The release publishes `checksums.txt` with the archives; consumers (feed-flow CI, the tap updater) verify sha256 rather than trusting the URL.
- Renovate (or Dependabot) enabled for Go modules and Actions from day one.
- `plan/LOCAL.md` and `*.env` are gitignored; nothing machine- or credential-specific may be committed (public repo).
