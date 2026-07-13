# 02 — Architecture and Locked Decisions

## Locked decisions

| Decision | Choice |
| --- | --- |
| Language | Go (latest stable), heavy TDD |
| CLI framework | `spf13/cobra` |
| HTTP | stdlib `net/http` for the Store API; custom retry logic (semantics are endpoint-specific, no generic retry lib). One exception: blob uploads use the Azure `azblob` SDK for chunked upload (doc 03) |
| Binary name | `pcenter` (= Partner Center, the portal/API this drives; deliberately distinct from Microsoft's official `msstore` CLI) |
| Repo | This repo (`pcenter-cli`), standalone, app-agnostic |
| Distribution | GitHub Releases via goreleaser: `darwin/arm64`, `windows/amd64`, `linux/amd64`. Consumers pin a version and verify checksums |
| Metadata model | File-based pull/push; the canonical metadata dir lives in the consuming app's repo (feed-flow) and is passed via `--dir` |
| Interactivity | None. JSON-first, no prompts (like the ASC CLI). Destructive ops require explicit flags |
| License | Apache-2.0 (matches feed-flow); repo is public/open source from the start |

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
  .goreleaser.yaml
  .github/workflows/ci.yml       # go test + golangci-lint on linux/macos/windows
  .github/workflows/release.yml  # goreleaser on tag
```

## Config & auth

Resolution order (first wins): explicit flags → process env vars (`MS_STORE_*`) → env file.

- `--env-file` flag, default `~/.config/pcenter/credentials.env` on every OS (`~` = `os.UserHomeDir()`; deliberately not `os.UserConfigDir()`, so the path is identical in docs for macOS/Linux/Windows), overridable via `PCENTER_ENV_FILE`. Format: plain `KEY=VALUE` lines. A missing default file is not an error when the `MS_STORE_*` vars are already set in the environment (the CI case).
- `--app-id` overrides `MS_STORE_APP_ID`.
- Token: `POST https://login.microsoftonline.com/{tenant}/oauth2/token`, form-urlencoded, `grant_type=client_credentials`, `resource=https://manage.devcenter.microsoft.com`. Acquire once per process; no disk caching (tokens last ~60 min, processes are short).
- `PCENTER_API_BASE` / `PCENTER_LOGIN_BASE` env overrides (undocumented; used by e2e tests to point at fakestore).

## Output conventions

- Global `--output json|table`; TTY-aware default (table on TTY, json when piped).
- `--verbose` logs requests/responses to stderr with secrets redacted (centralized redaction layer, see doc 03 §Security).
- Exit codes: 0 success; 1 API/validation failure; 2 usage error.
- With `--output json`, errors go to stderr as one-line JSON.
- `pcenter version` / `--version`: version, commit, build date injected via goreleaser ldflags.

## Guardrails

- Mutating commands never prompt; destructive ones (`submission delete-draft`, `rollout halt`) require `--yes`; `listing push` requires exactly one of `--dry-run | --skip-commit | --yes`.
- `publish msix --skip-commit` / `listing push --skip-commit` leave an uncommitted draft for later `pcenter submission commit`.
- Any failure after creating a submission but before commit deletes the draft (best-effort, warn on failure) — same as the ps1.
- Secrets never reach logs or output: `client_secret`, bearer tokens, and the SAS query string of `fileUploadUrl` are redacted centrally (doc 03 §Security).

## Supply-chain & repo security

- GitHub Actions pinned to commit SHAs; workflow `permissions` minimal (`contents: read` for CI; release workflow gets `contents: write` only).
- goreleaser publishes `checksums.txt` with the archives; consumers (feed-flow CI) verify sha256 of pinned downloads.
- Renovate (or Dependabot) enabled for Go modules and Actions from day one.
- `plan/LOCAL.md` and `*.env` are gitignored; nothing machine- or credential-specific may be committed (public repo).
