# Changelog

What changed, for the people using it. Each release's section is what the release
publishes as its notes — `release.yml` reads it from here and refuses to publish a tag
that has no section, so this file cannot fall behind.

## 0.0.3

Two Store limits are now checked before a submission is created, rather than arriving as an
opaque 400 from the Ingestion API after a draft exists — which then has to be cleaned up.

`shortDescription` is capped at **500** characters. Microsoft's published listing docs say
1,000; the API rejects anything longer than 500.

Keywords are capped at **21 locales carrying them** across the whole submission. This is the
surprising one: the cap is not per locale and not on the total number of keywords, but on how
many locales have keywords at all. A 22nd fails with `The size of KeywordsTotalCount must be
21 or less` while every locale is individually valid. The error names the locales so you can
see which to clear.

A failed submission can now be deleted. `submission delete-draft` and `--replace-pending`
previously accepted only `PendingCommit`, so a submission whose commit or certification
failed left the app wedged: it could not be deleted, it could not be replaced, and it still
counted as the app's one pending submission, so nothing new could be created either. Both
now accept any failed status as well, and still refuse anything genuinely in flight.

`submission watch` no longer exits non-zero when it runs out of poll attempts. Certification
takes hours, so reaching the attempt limit means pcenter stopped watching — not that the
submission is in trouble — and failing there turns a healthy release into a red CI job. It
now reports the last observed status with `classification: "in-progress"` and a warning, the
same way `submission commit` already handled the identical situation, and exits 0. A
genuinely failed status still exits non-zero. This also makes the documented `in-progress`
classification reachable, which it never was before.

Those checks, and the existing ones on features, hardware and images, now report the
documented `validation` code and exit 2 — "fix this and retry" — instead of the generic
failure exit, which told automation nothing about what to do.

## 0.0.2

Three things the first real use of `0.0.1` turned up — driving a live Store listing from
files, which is how each of these was found rather than reasoned about.

`listing push --dry-run` no longer refuses to run when a pending submission exists. A dry
run creates nothing, so a draft cannot get in its way — being unable to preview a change
until you had resolved a draft was exactly the wrong way round. The draft would still block
a real push, so it is reported as a warning instead of a failure.

`shortDescription` is now part of the listing model, so `listing pull` writes it and
`listing push` can change it. It was previously carried through from the Store untouched —
which meant a listing whose short description had drifted could not be fixed from files.

Empty change lists in JSON output are `[]` rather than `null`. `imageChanges` could come
back `null` while its sibling `listingChanges` was `[]`, so a caller taking the length of
one and not the other broke on a listing with nothing to change.

## 0.0.1

First release, deliberately numbered as a preview: everything below is implemented and
validated against the live Store API, but it has not yet driven a real release end to end
from CI. Expect the command surface to hold and the rough edges to be found. `pcenter`
drives a Microsoft Store app through the Partner Center Submission API from the command
line: publish an MSIX, manage the listing from files in your repo, read reviews, and rescue
a submission or rollout that has got stuck.

**Publishing.** `pcenter publish msix` creates a submission, uploads the package, and
commits it — or stops short with `--skip-commit` so you can inspect the draft first and
`pcenter submission commit` later. Release notes come from a JSON file keyed by locale;
a locale present in the Store but missing from that file is a hard failure rather than a
silently empty changelog. `--replace-pending` clears an existing uncommitted draft, since
the Store allows only one.

**Reading the listing.** `pcenter listing show` prints it to stdout — all locales, or one
with `--locale` — without writing anything to disk. `--images` adds each screenshot's
caption and Store id. Use it when you want to look; use `pull` when you want files.

**Listings as files.** `pcenter listing pull` writes the whole listing — text, images, and
an identity marker — into a directory you commit alongside your app. Edit it, then
`pcenter listing push`, which requires you to say which of `--dry-run`, `--skip-commit`,
or `--yes` you meant. `--dry-run` prints the diff and touches nothing. Locales and
screenshots you have not put under management are left alone; deleting something takes an
explicit entry, and removing a whole locale needs `--allow-locale-removal` on top of that.
A push into a directory belonging to a different app fails on the identity marker before
any request is sent.

**Stuck submissions and rollouts.** `pcenter rollout status / set-percentage / finalize /
halt` and `pcenter submission status / get / watch / commit / delete-draft` exist for the
day a rollout stops responding. They are built around the Store API's habit of returning
504 for an operation that in fact succeeded: every mutation verifies the resulting state
rather than trusting the response code, so a timeout mid-finalize resolves instead of
leaving you guessing.

**Reading.** `pcenter reviews list` (date, market, and rating filters, `--all` to follow
paging), `pcenter locales list`, `pcenter app info`, `pcenter auth status`.

**Getting set up.** `pcenter auth login` stores your Partner Center credentials so you do
not have to place an env file by hand. It prompts when run on a terminal — reading the
client secret without echo — and takes every value as a flag otherwise, so it never hangs a
script. Credentials are checked against the Store before anything is written, so a mistyped
secret fails there rather than during a release. `pcenter auth doctor` reports the whole
setup at once: which file was read, where each setting came from, whether a token can be
acquired, whether the app is reachable. It exits non-zero when the setup is unusable, which
makes it a preflight step in CI. `pcenter auth logout` removes the file again.

In CI, keep using `MS_STORE_*` environment variables from your secrets — they take
precedence over the file and leave nothing on the runner.

**Built for agents and CI.** Table output on a terminal, JSON when piped, `--output` to
force either — so a tool invoking pcenter through a pipe gets machine-readable output
without knowing to ask.

Failures are structured rather than prose. Every JSON error carries a stable `code`, a
one-line `message`, and fields for anything you would otherwise have to parse out of the
text — which settings are missing, the correlation id, a suggested remedy. Exit codes
separate the cases that need different responses: 2 fix your configuration, 3 credentials
rejected, 4 operation invalid for the current state (permanent — do not retry), 5 throttled
(retry later). The long human explanation appears only in table mode, where a human is
reading it. Warnings live in the result payload, not scattered on stderr.

Credentials come from flags, `MS_STORE_*` environment variables, or an env file, in that
order; a missing one tells you where pcenter looked and what to run. Secrets — client
secret, bearer tokens, and the SAS signature on upload URLs — are redacted centrally, so
`--verbose` output is safe to paste into an issue.
