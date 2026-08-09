# Matura Files

Static source files for the Matura application: converted exam JSON, task images, listening audio, and shared exam material. The repository is published as a file host through GitHub Pages.

## Source layout

```text
source/
  Json/<year>/<term>/<subject>/<level>.json
  Raw/<year>/<term>/<subject>/<level>/exam.pdf
  Raw/<year>/<term>/<subject>/<level>/answers.pdf
  <year>/<term>/<subject>/<level>/     # media for one paper
  All/<subject>/                       # material shared between papers
html/                                  # static files copied into the published source
main.go                                # media conversion
studies.go                              # live Postani Student downloader/parser
```

The study-program dataset is published beside the exam files:

```text
source/programi.json                    # one combined catalog + structured requirements file
```

The file is an object with `generated_at`, the four-date `refresh_schedule`, source
metadata, and a `programi` array. Each array entry keeps the searchable basic fields
at the top level and nests the full parser result under `detalji`. The original HTML
pages are build-only temporary files and are not published or committed.

To refresh it locally:

```sh
go run . --refresh-programs
```

`studies.go` fetches the live catalog from the Postani Student API, downloads each
detail page into a temporary directory, parses it sequentially, and deletes that
directory when finished. It does not discard source cells or footnotes: it records
typed values, alternatives, thresholds, grouped rules, cross-section caps, original
HTML cells, and a limitation report whenever the source wording cannot be safely
reduced to an algorithm. `.github/workflows/refresh-programs.yml` refreshes
it only on January 1, March 1, June 1, October 1, or a manual run. That workflow
rebuilds and publishes the complete static source tree so the catalog refresh
cannot replace the exam files with a partial deployment.

Years are grouped by exam term: `Ljeto`, `Jesen`, and, where available, `Probna`. Subject directories use Croatian abbreviations such as `Hrv`, `Eng`, `Mat`, and `Inf`. Papers with levels use `A` and `B`; subjects without levels use `base`.

Examples:

```text
source/Json/2026/Ljeto/Eng/A.json
source/2026/Ljeto/Eng/A/Task1.opus
source/All/Mat/
```

## Content rules

- JSON represents an individual official exam paper in the format consumed by the Matura app.
- Store only task-relevant media beside its paper. Images are WebP and audio is Opus.
- English listening assets are named `Task1.opus`, `Task2.opus`, and so on. Archive introductions, pauses, and outros are not task assets.
- Shared assets belong in `source/All/<subject>/`, not duplicated into every year.
- Optimized official booklets and answer keys belong in `source/Raw/`; the site’s
  developer raw view embeds `exam.pdf` and opens `answers.pdf` in a drawer.
- Fill-in answer slots use exactly `___`.
- Do not store NCVVO logos, answer sheets, marking keys, or other non-task material as assets.

## Publishing

## Extraction verification

The repository-wide structural check is:

```sh
node scripts/audit-json.mjs source/Json
```

The application schema is defined in `matura-site/app/other/zod.ts`; it should be run through the project's TypeScript tool after changing JSON. `EXTRACTION_AUDIT.md` records the current scope and any papers that still need semantic/manual review. The PDF-driven English rebuild helpers are in `scripts/rebuild-english-keys.mjs` and `scripts/create-english-2020-21.mjs`; they are reproducible utilities, not a substitute for checking the source paper.

On every push to `main`, GitHub Actions:

1. Runs `go run .`.
2. Converts any PNG/JPEG files to WebP, MP3/WAV files to Opus, and PDF files to
   Ghostscript `/ebook`-optimized PDFs in place.
3. Copies `html/` into `source/` and deploys the complete `source/` tree through
   GitHub Pages Actions. The published `programi.json` is restored before an
   ordinary deploy so a media-only push cannot remove it.

The Pages publishing source should be set to **GitHub Actions** in the repository
settings. The old `gh-pages` branch can remain as history, but it is no longer
used by these workflows.

`main.go` removes the original PNG/JPEG/MP3/WAV file after a successful conversion. Commit media in its final `.webp` or `.opus` form whenever possible; do not run the converter on the only copy of an asset unless that removal is intended.

The conversion helpers used for the recent paper pass are kept in `scripts/`:

- `rebuild-english-keys.mjs` rebuilds English reading/listening groups from the IK PDFs,
  preserving official task types and answer mappings.
- `rebuild-english-listening-modern.mjs` rebuilds 2024–2026 listening tasks from IK-2
  source text, including task-level audio, matching, and single-choice interactions.
- `repair-english-gap-prompts.mjs` restores sentence context for selectable gaps that
  were reduced to a bare question number; `repair-english-match-number-options.mjs`
  restores the numbered side and missing example letters of matching interactions.
- `create-math-2020-21.mjs` rebuilds Math question text and keys; for 2020–2021 it
  prefers the MuPDF text layer because the standard PDF layer drops formula glyphs.
- `normalize-json-text.mjs` enforces four-space JSON formatting, removes control
  characters, and normalizes every fill-in run to exactly `___`.

The deployed files are addressed relative to `source/`, for example:

```text
https://files.matura.top/2026/Ljeto/Eng/A/Task1.opus
```
