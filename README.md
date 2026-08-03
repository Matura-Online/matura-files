# Matura Files

Static source files for the Matura application: converted exam JSON, task images, listening audio, and shared exam material. The repository is published as a file host through GitHub Pages.

## Source layout

```text
source/
  Json/<year>/<term>/<subject>/<level>.json
  <year>/<term>/<subject>/<level>/     # media for one paper
  All/<subject>/                       # material shared between papers
  files.json                           # generated source-tree index
html/                                  # static files copied into the published source
main.go                                # media conversion and index generator
```

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
2. Converts any PNG/JPEG files to WebP and MP3/WAV files to Opus.
3. Regenerates `source/files.json`.
4. Copies `html/` into `source/` and deploys `source/` to the `gh-pages` branch.

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
