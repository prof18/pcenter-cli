# Metadata directory and release notes

Two file formats matter to `pcenter`: the **metadata directory** that holds your Store listing as text and images, and the **release-notes file** that supplies the changelog at release time. They are independent — release notes are never stored in the metadata directory, because a changelog belongs to a release and a listing does not.

- [Metadata directory](#metadata-directory)
- [`store.json`](#storejson--identity-marker)
- [`listings/<locale>.json`](#listingslocalejson)
- [`images/` and `images-manifest.json`](#images-and-images-manifestjson)
- [What push does with the diff](#what-push-does-with-the-diff)
- [Release notes file](#release-notes-file)

---

## Metadata directory

`pcenter` is app-agnostic and receives the directory through `--dir`, so where it lives and whether you commit it are your choice. Two models both work:

- **Committed** — the directory lives in your repo and is the source of truth for your Store copy. Listing changes then arrive as reviewable diffs, and `listing push --dry-run` makes a good pull-request check.
- **Scratch** — the directory is a gitignored working area (`.pcenter/`, say) that you regenerate with `listing pull` whenever you need it. Right when your Store copy is already maintained elsewhere — a translation pipeline, for instance — and committing this would create a second source of truth in a second format.

Pick the scratch model if something else already owns the text; pick the committed model if nothing does. Either way, create the directory with `pcenter listing pull`:

```bash
pcenter listing pull --dir assets/storecopy/microsoft-store --app-id 9NXXXXXXXXXX
```

```
microsoft-store/
  store.json              # identity marker, written by pull
  listings/
    en-us.json            # one file per Store locale, lowercase filenames
    it.json
    ...
  images/
    en-us/
      screenshot-01.png
      ...
    it/
      ...
  images-manifest.json    # maps local files to Store image resources
```

Locale handling is case-insensitive throughout; the canonical form on disk is lowercase (`en-us.json`).

---

## `store.json` — identity marker

```json
{
  "appId": "9NXXXXXXXXXX",
  "pulledAt": "2026-07-09T12:00:00Z",
  "sourceSubmissionId": "1152921504621442252",
  "generatedBy": "pcenter 0.0.1"
}
```

`listing push` **refuses to run** when `store.json` is missing or its `appId` does not match the target app, before any request is sent. That single check is what prevents pushing one app's metadata to another — and what prevents an empty or wrong directory being interpreted as "delete everything".

---

## `listings/<locale>.json`

Editable base-listing text fields only:

```json
{
  "title": "FeedFlow",
  "description": "…",
  "shortDescription": "…",
  "features": ["…", "…"],
  "keywords": ["…"],
  "copyrightAndTrademarkInfo": "",
  "licenseTerms": "",
  "recommendedHardware": [],
  "minimumHardware": []
}
```

| Field | Notes |
| --- | --- |
| `title` | The reserved product name, chosen in Partner Center rather than written here. Some locales carry it and some are empty; leave existing ones alone. A locale you are **adding** must have one, or the Store rejects the submission with `MissingTitle`. |
| `description` | Up to 10,000 characters. |
| `shortDescription` | The catchy line at the top of the listing. **Up to 500 characters** — not the 1,000 Microsoft's docs state; the API rejects 501+. Only the first ~270 are shown in some views. A separate field from `description`. |
| `features` | At most **20** items — validated locally before any submission is created. |
| `keywords` | Search terms, not shown to customers. See the cross-locale cap below. |
| `copyrightAndTrademarkInfo`, `licenseTerms` | Plain strings, usually empty. |
| `recommendedHardware`, `minimumHardware` | Raw JSON passthrough; validated leniently. The API reference is ambiguous about the type, and live responses return arrays. |

Deliberately **not** in these files:

- `privacyPolicy`, `supportContact`, `websiteUrl` — the API ignores them; they live on Partner Center's Properties page.
- `releaseNotes` — owned by the [release-notes file](#release-notes-file).
- `platformOverrides` — not modeled locally; `push` carries whatever the server has through unchanged.

### Validated locally, before anything is created

| Rule | Limit |
| --- | --- |
| `description` | ≤ 10,000 characters |
| `shortDescription` | ≤ **500** characters (the docs say 1,000; the API says 500) |
| `features` | ≤ 20 items |
| `recommendedHardware` / `minimumHardware` | ≤ 11 items |
| Locales carrying keywords | ≤ **21** across the whole submission |

That last one is the surprising one, and it is undocumented. The cap is not on keywords per
locale, nor on keywords in total — it is on **how many locales have keywords at all**. Adding
keywords to a 22nd locale fails with `The size of KeywordsTotalCount must be 21 or less`,
even though every locale is individually valid. To add a new one, clear the keywords on
another locale first.

pcenter checks all of these before creating a submission, because the alternative is a 400
from the Ingestion API *after* a draft exists — which then has to be cleaned up.

**Adding a locale** is adding a `listings/<locale>.json` file. **Removing a locale** — deleting the file — is an error unless `listing push --allow-locale-removal` is passed, so an accidental deletion cannot drop a Store language.

---

## `images/` and `images-manifest.json`

Image binaries **cannot be downloaded** through the Submission API. `pull` therefore writes no image files: it records what the Store holds so `push` can tell your files apart from the server's.

```json
{
  "images": {
    "en-us": [
      {
        "localPath": "en-us/screenshot-01.png",
        "imageType": "Screenshot",
        "description": "Your feeds, one place",
        "storeId": "abc123",
        "sha256": "…"
      },
      {
        "imageType": "Screenshot",
        "storeId": "def456",
        "remoteOnly": true
      }
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| `localPath` | Relative to `images/`, **locale-prefixed** (`en-us/screenshot-01.png`). This is also the file's name inside the upload ZIP, which is how the Store identifies it. |
| `imageType` | `Screenshot` (desktop), `MobileScreenshot`, `XboxScreenshot`, …, or `Icon`. |
| `description` | The caption. Max 200 characters. |
| `storeId` | The Store's id for an image it already holds. |
| `sha256` | Recorded at pull/push time; a changed hash means the file was edited. |
| `remoteOnly` | The Store holds this image but no local file corresponds to it. Requires `storeId`. |
| `delete` | Set `true` to remove the image. Requires `storeId`; mutually exclusive with `remoteOnly`. |

`remoteOnly` and `delete` are omitted when false.

Images uploaded through the Partner Center UI have arbitrary server-side filenames, so they stay `remoteOnly`: they can be **replaced** (delete + upload) but not updated in place.

Image order in the listing is the display order in the Store; within a locale, filename sort decides it.

### Validated locally, before anything is created

| Rule | Limit |
| --- | --- |
| Format | PNG only |
| File size | ≤ 50 MB |
| Desktop screenshot (`Screenshot`) | ≥ 1366×768, landscape or portrait |
| Icon (`Icon`) | exactly 300×300 |
| Desktop screenshots per locale | ≤ 10 |
| Other screenshot types per locale | ≤ 8 each |
| Caption (`description`) | ≤ 200 characters |
| At least one screenshot overall | required |
| `localPath` | must be a safe path starting with its locale directory |

---

## What push does with the diff

`listing push` compares the directory to the last published submission and derives the change set. Run it with `--dry-run` first — it prints exactly this, and creates nothing.

**Listing text**, per locale: `add` (a new `listings/<locale>.json`), `update` (with the field that changed), `remove` (a missing file, gated by `--allow-locale-removal`).

**Images**, per locale:

| Situation | Result |
| --- | --- |
| Local file with no Store counterpart | `add` → uploaded, `fileStatus: PendingUpload` |
| Matched entry, same `sha256` | untouched, stays `Uploaded` |
| Matched entry, changed `sha256` | `replace` — delete + upload |
| Matched entry, edited `description` only | `caption` — kept `Uploaded`, new caption sent with the image id |
| Entry with `delete: true` and a `storeId` | `delete` → `fileStatus: PendingDelete` |
| `remoteOnly` entry, retained | kept as is |
| Server image absent from the manifest | **retained** — a damaged manifest cannot mass-delete screenshots |
| Manifest entry whose local file is missing | validation error, before any submission is created |

Deletion is always something you asked for explicitly.

---

## Release notes file

A JSON file keyed by locale, passed to `publish msix --release-notes` or `listing push --release-notes`:

```json
{
  "notes": {
    "en-us": "Bug fixes and performance improvements.",
    "it": [
      "Correzione di bug",
      "Miglioramenti di prestazioni"
    ],
    "de": "Fehlerbehebungen."
  }
}
```

- The top-level `notes` object is required.
- A value is either a **string** or an **array of lines**. Arrays are joined with CRLF (`\r\n`), which is what the Store renders as line breaks.
- Locale keys are matched **case-insensitively** against the Store's listing locales. Two keys that collide case-insensitively are an error.
- Empty or blank notes are an error.

**A Store locale missing from the file is a hard failure.** The alternative — shipping an empty changelog for a language — is worse than a failed command. A locale in the file that the Store does not have is a warning, so pruning a language from the Store does not break your release.

Keep this file wherever your release process wants it; it is unrelated to the metadata directory. FeedFlow keeps it at `assets/storecopy/microsoft-store-release-notes.json`.
