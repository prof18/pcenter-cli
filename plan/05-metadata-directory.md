# 05 — Metadata Directory Format

The canonical metadata dir lives in the consuming app's repo (for FeedFlow: `assets/storecopy/microsoft-store/` in feed-flow). The CLI receives it via `--dir` and stays app-agnostic.

```
microsoft-store/
  store.json            # identity marker, written by pull
  listings/
    en-us.json          # one file per Store locale (lowercase filenames)
    it.json
    ...
  images/
    en-us/
      screenshot-01.png # desktop screenshots (imageType: Screenshot)
      ...
    it/
      ...
  images-manifest.json  # written by pull / maintained by push
```

## `store.json` (identity marker)

```json
{
  "appId": "9N5T1RFBB6V5",
  "pulledAt": "2026-07-09T12:00:00Z",
  "sourceSubmissionId": "115292...",
  "generatedBy": "pcenter <version>"
}
```

`listing push` **refuses to run** when `store.json` is missing or its `appId` doesn't match the target app — this is what prevents pushing the wrong app's metadata (or an empty directory, which would otherwise be interpreted as "delete everything").

## `listings/<locale>.json`

Editable base-listing text fields only:

```json
{
  "title": "...",
  "description": "...",
  "features": ["...", "..."],
  "keywords": ["..."],
  "copyrightAndTrademarkInfo": "",
  "licenseTerms": "",
  "recommendedHardware": [],
  "minimumHardware": []
}
```

- Locale handling is **case-insensitive**; canonical form is lowercase (`en-us.json`). Push matches server locale keys case-insensitively (the ps1 does the same for release notes).
- Omit obsolete API fields (`privacyPolicy`, `supportContact`, `websiteUrl` — the API ignores them; they're managed in Partner Center's Properties page).
- Omit `releaseNotes` — owned by the release-notes JSON contract (`--release-notes` flag).
- `platformOverrides` are not modeled in files; `push` carries them through from the server unchanged.
- Field limits to validate client-side before any submission is created: `features` ≤ 20 items, `recommendedHardware`/`minimumHardware` ≤ 11 items.
- Docs ambiguity: `minimumHardware` is declared type `string` in the API reference table but described as an array. Treat both hardware fields as raw JSON passthrough on pull (write whatever the server returns) and validate leniently on push. Live FeedFlow pull on 2026-07-14 confirmed that both `recommendedHardware` and `minimumHardware` are arrays.

## `images-manifest.json`

Maps local files to Store image resources per locale. Per image: `{localPath, imageType, description, storeId (when Uploaded), sha256, remoteOnly, delete}`. `remoteOnly` and `delete` are omitted when false.

- `pull` writes/refreshes it from the server. Existing server images get their `storeId`; entries with no matching local file are marked `remote-only` (binaries are not downloadable via the API).
- **Matching rule**: `localPath` is relative to `images/` and locale-prefixed (`en-us/screenshot-01.png`); at upload time it is also the ZIP-internal `fileName`, which is how the Store identifies the file. Confirmed convention: Microsoft's msstore-cli bundles listing images the same way (`{locale}/{filename}` inside Upload.zip, with `image.fileName` set to that relative path). Images originally uploaded through the Partner Center UI have arbitrary server-side `fileName`s → they stay `remote-only` and can only be replaced (delete + upload), not updated in place.
- `push` computes the diff:
  - Local file with no matching store entry → `fileStatus: PendingUpload`; the file goes into the upload ZIP under its locale-prefixed name.
  - Retained `remote-only` entry → keep the Store image `Uploaded` and unchanged. A server image absent from the manifest is also retained as a safety measure, so a missing or damaged manifest cannot mass-delete screenshots.
  - Managed manifest entry whose local file is missing → validation error before creating a submission.
  - Entry with `delete: true` and a `storeId` → `fileStatus: PendingDelete`. Deletion is always explicit; `delete` is mutually exclusive with `remoteOnly` and does not require a local file.
  - Matched entries (same `localPath`, same `sha256` as recorded) → left `Uploaded`, untouched. A changed `sha256` = replace (delete + upload).
  - **Caption-only change** (edited `description` on a matched entry): keep `fileStatus: Uploaded` and send the new `description` in the PUT body alongside the image `id`. Existing FeedFlow images are all `remoteOnly`, so this cannot be verified without replacing or deleting current listing content. Per maintainer direction on 2026-07-14, defer this check until M5 has a committed CLI-managed image; if the API rejects the in-place edit, fall back to replace (delete + upload).
- Image order in the listing = display order in the Store; use filename sort within each locale directory.

## Image validation (client-side, before any submission is created)

Confirmed against the Learn "screenshots and images" page for MSIX (2026-07-10):

- Screenshots are **PNG only**, max **50 MB**, landscape or portrait.
- Desktop screenshots (`imageType: Screenshot`): **1366×768 or larger** (4K 3840×2160 supported). At least one screenshot (any device family) is required overall.
- Count limits: max **10** desktop screenshots per locale; max **8** per other device family (`MobileScreenshot`, `XboxScreenshot`, ...).
- Image `description` (caption) max **200 characters**.
- 1:1 app tile icon (`imageType: Icon`): 300×300 PNG.
- Live validation on 2026-07-14 accepted two 1920×1080 PNG screenshots as `PendingUpload` in an uncommitted draft. The draft was inspected and deleted; the published 101 images remained untouched.

## Locale add/remove

A new `listings/<locale>.json` adds that locale key to the submission's `listings` on push; a missing file removes the locale **only** when `--allow-locale-removal` is passed. A live `--skip-commit` draft on 2026-07-14 verified adding `en-gb`. Per maintainer direction, removal remains deferred because current listing content must not be deleted.
