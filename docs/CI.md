# Using pcenter in CI

This page covers installing pcenter on a runner, providing credentials, and automating releases and listing updates. For output formats and exit codes, see [Automation](AUTOMATION.md).

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
    VERSION=v0.0.3
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
    $Version = 'v0.0.3'
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

You can publish from a Linux job after building the MSIX on Windows and passing it through as a workflow artifact. This keeps building and publishing independently retryable.

```yaml
- name: Publish to the Microsoft Store
  run: |
    pcenter publish msix \
      --path "${{ steps.build.outputs.msix }}" \
      --rollout-percentage 90 \
      --release-notes store/microsoft-release-notes.json \
      --replace-pending
```

(On a Windows runner the same command runs under `shell: pwsh` with backtick continuations, or on one line.)

`--release-notes` is required unless you explicitly pass `--keep-existing-release-notes`. The file must include every Store locale; see [Release notes](METADATA.md#release-notes-file) for its format.

`--rollout-percentage 90` starts a staged rollout. Finish it later with `pcenter rollout finalize`. `--replace-pending` removes a disposable draft; see [Operational caveats](#operational-caveats) before using it. Add `--skip-commit` to leave a draft for review.

To finish a staged rollout in a later job or a manual dispatch:

```yaml
- run: pcenter rollout finalize
```

---

## Pushing listing changes

Listing text and screenshots live in a directory in your repo ([Metadata directory](METADATA.md)). A pull-request check that shows what a change would do:

```yaml
- name: Show pending Store listing changes
  run: pcenter listing push --dir store/microsoft --dry-run
```

`--dry-run` creates nothing — it prints the per-locale diff and the request body that would be sent. Push for real on merge with `--yes` (creates and commits) or `--skip-commit` (creates a draft for review).

---

## Operational caveats

**`--replace-pending` deletes any disposable draft**, including one someone left in Partner Center for inspection. Check for a pending submission before using it.

After a timeout, check the resulting state; pcenter verifies completed changes because the Store can return a 504 after accepting a request. If polling stops, resume with `pcenter submission watch`.

For failure handling and exit codes, see [Automation](AUTOMATION.md#exit-codes).
