# Using pcenter in CI

`pcenter` is built to run unattended: it never prompts, it emits JSON when its output is piped, and its exit codes distinguish the failures that need different responses. This page covers getting the binary onto a runner, giving it credentials, and the two workflows worth automating.

- [Credentials](#credentials)
- [Installing on a runner](#installing-on-a-runner)
- [Preflight](#preflight)
- [Publishing a release](#publishing-a-release)
- [Pushing listing changes](#pushing-listing-changes)
- [Operational caveats](#operational-caveats)

---

## Credentials

Set the four `MS_STORE_*` variables from your repository secrets. They take precedence over the credentials file and leave nothing on the runner to clean up — do **not** run `pcenter auth login` in CI.

```yaml
env:
  MS_STORE_TENANT_ID: ${{ secrets.MS_STORE_TENANT_ID }}
  MS_STORE_CLIENT_ID: ${{ secrets.MS_STORE_CLIENT_ID }}
  MS_STORE_CLIENT_SECRET: ${{ secrets.MS_STORE_CLIENT_SECRET }}
  MS_STORE_APP_ID: ${{ secrets.MS_STORE_APP_ID }}
```

The client secret is redacted from all output, including `--verbose`, so a verbose log is safe to keep.

---

## Installing on a runner

Releases publish five archives plus `checksums.txt`. **Pin a version and verify the checksum** — the URL alone is not a guarantee.

Archive names are `pcenter_<tag>_<os>_<arch>.<ext>`:

| Platform | Archive |
| --- | --- |
| `darwin/arm64`, `darwin/amd64`, `linux/arm64`, `linux/amd64` | `.tar.gz` |
| `windows/amd64` | `.zip` |

**Linux / macOS runner:**

```yaml
- name: Install pcenter
  run: |
    set -euo pipefail
    VERSION=v0.0.1
    curl -fsSL -O "https://github.com/prof18/pcenter-cli/releases/download/${VERSION}/pcenter_${VERSION}_linux_amd64.tar.gz"
    curl -fsSL -O "https://github.com/prof18/pcenter-cli/releases/download/${VERSION}/checksums.txt"
    grep " pcenter_${VERSION}_linux_amd64.tar.gz\$" checksums.txt | sha256sum -c -
    tar -xzf "pcenter_${VERSION}_linux_amd64.tar.gz"
    install -m 0755 pcenter /usr/local/bin/pcenter
    pcenter version
```

**Windows runner** (PowerShell) — Homebrew has no Windows channel, so a pinned download is the path there:

```yaml
- name: Install pcenter
  shell: pwsh
  run: |
    $Version = 'v0.0.1'
    $Archive = "pcenter_${Version}_windows_amd64.zip"
    $Base    = "https://github.com/prof18/pcenter-cli/releases/download/$Version"
    Invoke-WebRequest "$Base/$Archive" -OutFile $Archive
    Invoke-WebRequest "$Base/checksums.txt" -OutFile checksums.txt
    $Expected = (Select-String -Path checksums.txt -Pattern ([regex]::Escape($Archive))).Line.Split(' ')[0]
    $Actual   = (Get-FileHash $Archive -Algorithm SHA256).Hash.ToLower()
    if ($Expected -ne $Actual) { throw "checksum mismatch for $Archive" }
    Expand-Archive $Archive -DestinationPath "$env:RUNNER_TEMP\pcenter"
    echo "$env:RUNNER_TEMP\pcenter" | Out-File -FilePath $env:GITHUB_PATH -Append
```

For running commands by hand, `brew install prof18/tap/pcenter` is the channel to point humans at.

---

## Preflight

Fail early with a diagnosis instead of a 401 halfway through a release:

```yaml
- name: Check Store credentials
  run: pcenter auth doctor
```

`auth doctor` reports every check — file permissions, each setting and where it resolved from, token acquisition, app reachability — and exits non-zero when the setup is unusable. It never prints the secret.

---

## Publishing a release

```yaml
- name: Publish to the Microsoft Store
  run: |
    pcenter publish msix \
      --path "${{ steps.build.outputs.msix }}" \
      --rollout-percentage 90 \
      --release-notes assets/storecopy/microsoft-store-release-notes.json \
      --replace-pending
```

(On a Windows runner the same command runs under `shell: pwsh` with backtick continuations, or on one line.)

What that does, and why each flag is there:

- `--release-notes` (or `--keep-existing-release-notes`) is **mandatory**. Without it a forgotten flag would silently ship the previous version's changelog. A Store locale missing from the file fails the command.
- `--rollout-percentage 90` starts a staged rollout. Finish it later with `pcenter rollout finalize`.
- `--replace-pending` clears an existing uncommitted draft, since the Store allows only one pending submission.
- The command commits by default. Add `--skip-commit` to leave a draft for a human, and commit it with `pcenter submission commit`.

Failures before the commit started delete the draft automatically, so a failed run does not leave the next one blocked.

To finish a staged rollout in a later job or a manual dispatch:

```yaml
- run: pcenter rollout finalize
```

---

## Pushing listing changes

Listing text and screenshots live in a directory in your repo ([Metadata directory](METADATA.md)). A pull-request check that shows what a change would do:

```yaml
- name: Show pending Store listing changes
  run: pcenter listing push --dir assets/storecopy/microsoft-store --dry-run
```

`--dry-run` creates nothing — it prints the per-locale diff and the request body that would be sent. Push for real on merge with `--yes` (creates and commits) or `--skip-commit` (creates a draft for review).

---

## Operational caveats

**`--replace-pending` deletes *any* uncommitted draft** — including one a human left in Partner Center for inspection. Only one pending submission can exist, so a CI release and an in-flight local draft are mutually exclusive: commit or delete local drafts before tagging a release.

**Timeouts are not failures.** The Store API returns 504 for operations that in fact succeeded. Every mutation verifies the resulting state rather than trusting the response code, so a timeout mid-finalize resolves instead of leaving the job guessing.

**Exhausting the poll budget is not a failure either.** `--poll-attempts` bounds how long pcenter waits after a commit; reaching it means it stopped watching. Resume with `pcenter submission watch`.

**Exit codes** distinguish what to do next — `3` re-authenticate, `4` never retry unchanged, `5` back off. The full table is in [the automation contract](AUTOMATION.md#exit-codes).
