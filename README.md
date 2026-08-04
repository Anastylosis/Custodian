# Custodian

*(formerly StashJanitor)*

A small CLI for finding and resolving duplicate video scenes in a
[Stash](https://github.com/stashapp/stash) library, safely. Custodian
currently only supports Stash; support for other media managers (e.g. COVE)
is planned but not yet implemented.

Custodian solves three related problems:

- **Workflow A — cross-scene duplicates.** Two or more separate Stash scenes
  whose perceptual hashes are similar (the same content stored as different
  files, possibly different resolutions or encodes).
- **Workflow B — within-scene multi-file cleanup.** A single Stash scene that
  has more than one file attached because Stash auto-attaches re-detected
  files instead of creating a new scene.
- **Workflow C — orphan metadata recovery.** Scenes with no stash-box
  metadata (no `stash_ids`). custodian queries stash-box by phash, finds matches,
  and links them back so Stash's Scene Tagger can pull the full metadata.
- **Workflow E — loose subtitles.** Subtitle files that Stash cannot see,
  because they are not named after their video. custodian matches them to
  scenes using studio codes, title similarity and — decisively — the subtitle's
  own runtime against the scene duration.

All workflows fetch their data from Stash, score/match every candidate,
persist decisions to a local SQLite cache, and let you review everything
before committing any mutation.

> **Default behavior is paranoid.** `apply` is dry-run by default. All
> destructive actions require both `--commit` AND an interactive `YES`
> confirmation. `--yes` bypasses the prompt for scripted use.

See [PLAN.md](PLAN.md) for the full design and the rationale behind every
locked-in decision.

## Status

All three workflows work end-to-end against Stash v0.31.0:

| Command | Status |
|---|---|
| `custodian config init` / `custodian config show` | ✅ |
| `custodian stats` (library dashboard) | ✅ |
| `custodian scenes scan` / `status` / `report` | ✅ |
| `custodian scenes apply --action tag\|merge\|delete` | ✅ |
| `custodian scenes mark --group N` or `--signature` | ✅ |
| `custodian files scan` / `status` / `report` | ✅ |
| `custodian files apply --commit` (primary swap + deleteFiles) | ✅ |
| `custodian files mark --scene-id` | ✅ |
| `custodian orphans scan` / `status` / `report` | ✅ |
| `custodian orphans scan --endpoint all` (multi-endpoint) | ✅ |
| `custodian orphans apply --commit` (link to stash-box) | ✅ |
| `--submit-fingerprints` (all apply commands) | ✅ |
| `custodian review` (interactive TUI) | ✅ |
| Per-loser "kept by: ..." explanations in reports | ✅ |
| Filename-info-loss safety net (workflow A) | ✅ |
| Rename-on-merge (preserve loser filename info) | ✅ |
| Filename-to-metadata extraction on merge | ✅ |
| Stale-cache detection in `custodian stats` | ✅ |
| Orphans scan progress indicator with ETA | ✅ |
| `custodian organize scan` / `report` / `apply` | ✅ |
| Sidecars: subtitles follow videos on move/rename | ✅ |
| `custodian sidecars check` (access + path-map doctor) | ✅ |
| `custodian subs scan` / `apply` (match loose subtitles) | ✅ |

### Subtitles

Stash does not index subtitles as files — it looks next to each video for
`<stem>.srt` / `<stem>.<lang>.srt` at scan time and attaches what it finds to
the scene. Captions therefore have no file ID, and Stash's `moveFiles` mutation
(which takes `ids: [ID!]`) **cannot move them**. Any video that `organize`
relocates would leave its subtitles behind, silently detaching the captions.

Custodian handles both halves of that problem:

```sh
# Keep subtitles attached: they follow videos on move/rename, language intact.
#   clip.en.srt -> 2024-01-01_New.Name_4k.en.srt
custodian sidecars check          # verify access + path mapping first

# Fix subtitles that are ALREADY adrift: match them back to scenes.
custodian subs scan  --dir ~/Downloads/Subs
custodian subs apply --dir ~/Downloads/Subs --commit
```

Matching combines studio/JAV codes, name similarity and — decisively — the
subtitle's own runtime against the scene duration. Two scenes can share a
title, performer and studio, but not a length: in practice the correct match
lands within seconds while a same-titled decoy sits minutes away.

Scene titles and filenames are scored separately, so a library that titles its
scenes and one that encodes everything in filenames both match well. Matched
subtitles are renamed to `<video-stem>.<lang>.srt`, which is the only form
Stash attaches. The subtitles need not be on the same machine as the library.

This is the only part of the tool that touches the filesystem, so it needs to
reach your library. It works against a local path, a mounted share (including
Windows drive letters and UNC paths), or over SSH/SFTP when the library isn't
mounted at all. See [MANUAL.md](MANUAL.md#sidecar-files-subtitles).

See [MANUAL.md](MANUAL.md) for usage instructions and
[PLAN.md](PLAN.md) for the design and rationale.

## Requirements

- Go 1.25 or newer (required by the modernc.org/sqlite dependency)
- A reachable Stash instance running v0.31.0 (other 0.31.x patch releases
  should work; older or newer minor versions are untested)
- A few hundred MB of free disk for the SQLite cache (proportional to
  library size — at 60k scenes the DB is ~50 MB)

## Install

**Option A: Download a release binary** (no Go required)

Go to the [Releases](https://github.com/Anastylosis/Custodian/releases)
page, download the archive for your platform, and extract:

```sh
# Linux (amd64)
tar xzf custodian-v*-linux-amd64.tar.gz
chmod +x custodian

# macOS (Apple Silicon)
tar xzf custodian-v*-darwin-arm64.tar.gz
chmod +x custodian

# Windows — extract the .zip, you get custodian.exe
```

**Option B: Build from source** (requires Go 1.22+)

```sh
git clone https://github.com/Anastylosis/Custodian.git
cd Custodian
go build -o custodian ./cmd/custodian
```

**Option C: `go install`**

```sh
go install github.com/Anastylosis/Custodian/cmd/custodian@latest
```

The binary is fully self-contained (pure-Go SQLite, no cgo, no runtime deps).

## Quick start

```sh
# 1. Generate a config file. This creates config.yaml in the current dir.
./custodian config init

# 2. Edit config.yaml — set your Stash URL:
#    stash:
#      url: http://your-nas:9999
#    (the default is http://localhost:9999)

# 3. Optional: if your Stash has an API key, export it.
export STASH_API_KEY=...

# 4. See where you're at.
./custodian stats

# 4. Scan and review cross-scene duplicates.
./custodian scenes scan
./custodian scenes report --filter decided | less

# 5. Apply (safest first pass: just tag).
./custodian scenes apply --action tag --commit

# 6. Or go full merge (preserves metadata, renames files, reclaims disk):
./custodian scenes apply --action merge --commit

# 7. Clean up multi-file scenes.
./custodian files scan
./custodian files report | less
./custodian files apply --commit

# 8. Find metadata for orphan scenes via stash-box.
./custodian orphans scan --max-scenes 500
./custodian orphans report --filter matched
./custodian orphans apply --commit
```

## Configuration

`custodian config init` writes a fully-commented `config.yaml` next to the binary.
The most important sections:

```yaml
stash:
  url: http://your-stash:9999
  api_key_env: STASH_API_KEY     # name of the env var to read

scan:
  phash_distance: 4              # 0 = byte-equivalent, 4 = re-encodes,
                                 # >8 produces false positives
  duration_diff_seconds: 1.0     # critical false-positive filter

scoring:
  scenes:
    rules:
      - has_stash_id      # scene has stash-box metadata → wins
      - organized
      - resolution
      - bitrate
      - codec_preference
      - file_size
      - tag_count
      - path_priority
  files:
    rules:
      - filename_quality  # regex match → wins
      - path_priority
      - mod_time
    filename_quality:
      pattern: '^\d{4}[-._]\d{2}[-._]\d{2}_.+_(480|540|720|1080|1440|2160|2880|4320|[2468][kK])p?(_\d+)?\.[A-Za-z0-9]+$'

apply:
  scenes:
    default_action: tag           # safe default
    loser_tag: _dedupe_loser
    keeper_tag: _dedupe_keeper
```

The full default file is at
[`internal/config/default.yaml`](internal/config/default.yaml) — every
field is documented inline.

`custodian config show` prints the *effective* config (defaults merged with your
overrides), which is helpful when debugging "is my override being applied?".

## Workflow A — cross-scene duplicates

Stash exposes
`findDuplicateScenes(distance, duration_diff)` which returns groups of
perceptually-similar scenes server-side. `custodian scenes scan` consumes those
groups, scores each one with the configured rule chain, and writes the
results into the local SQLite cache.

```sh
./custodian scenes scan --distance 4 --duration-diff 1.0
```

Useful flags:

- `--distance N` — phash hamming distance (default 4)
- `--duration-diff S` — max duration delta in seconds (default 1.0)
- `--max-groups N` — stop after N groups, useful for chunked iteration at
  large library scale (60k+ scenes)

Once scanned, the local cache is offline-readable:

```sh
./custodian scenes status                              # summary + reclaimable bytes
./custodian scenes report --filter all                 # every group
./custodian scenes report --filter needs-review        # groups the scorer couldn't decide
./custodian scenes report --filter decided --json      # machine-readable output
```

### Apply: tag mode (safe)

```sh
./custodian scenes apply --action tag           # dry run
./custodian scenes apply --action tag --commit  # actually tag in Stash
```

Tag mode finds-or-creates two tags (default `_dedupe_loser` and
`_dedupe_keeper`), uses `bulkSceneUpdate` to add them to the scenes,
and marks the local groups as applied. **No scenes are deleted.** You then
go to Stash's own UI, filter by `_dedupe_loser`, eyeball the suggestions,
and bulk-delete the ones you agree with.

### Apply: merge mode (destructive)

```sh
./custodian scenes apply --action merge           # dry run
./custodian scenes apply --action merge --commit  # interactive YES prompt
./custodian scenes apply --action merge --commit --yes  # bypass prompt for cron
```

Merge mode does the full cross-scene consolidation per group:

1. Fetch full metadata for keeper + losers from Stash.
2. Compute the metadata union per the policy in `merge.scene_level`
   (multi-value fields union, scalar fields prefer keeper, rating100 max,
   organized any-true).
3. **Filename metadata extraction**: if the union still has empty title/date
   but a loser file has a structured filename (e.g. `2024-12-15_Performer-
   Title_1080p.mp4`), parse it and fill in the keeper's empty fields.
4. Call `sceneMerge` with the union as the `values` field — Stash's
   sceneMerge does NOT auto-union scene metadata, so we compute it.
5. The merged keeper now has every loser's files attached. Pick the best
   file as primary (resolution → bitrate → filename → file_size → mod_time).
6. **Rename-on-merge**: if the winning file has a junk basename but a loser
   had a structured one, rename the winner via `moveFiles` to use the
   loser's filename (with resolution token swapped to match the winner's
   actual height).
7. `deleteFiles` on the rest from disk. Mark the group applied.

Per-group failures don't abort the run — they're captured in the per-group
report and the loop continues.

## Workflow B — within-scene multi-file cleanup

Stash attaches multi-files to a scene only by `oshash`/`md5` match (verified
in `pkg/scene/scan.go` in v0.31.0), so all files within a single scene are
**byte-equivalent**. The scoring rules consider only filename, path, and
mod_time — tech specs are guaranteed identical.

```sh
./custodian files scan
./custodian files status
./custodian files report --filter decided   # each loser shows WHY it lost
./custodian files apply                     # dry run
./custodian files apply --commit            # swap primary + delete losers
```

The default `filename_quality` regex matches
`YYYY-MM-DD_<anything>_<resolution>[_N].<ext>` including import suffixes
like `_1080p_1.mp4` and resolutions up to 8K.

## Workflow C — orphan metadata recovery

Scenes with no `stash_ids` (55% of a typical library) are "orphans". custodian
queries stash-box by phash to find matches, then links them back to Stash.

```sh
./custodian orphans scan --max-scenes 500     # start small (stash-box is slow)
./custodian orphans report --filter matched   # review proposed matches
./custodian orphans apply --commit            # write stash_ids back
./custodian orphans scan                      # full library (re-runs skip cache)
./custodian orphans scan --endpoint all       # query every configured stash-box
```

After applying, run Stash's built-in Scene Tagger to pull full metadata
(title, performers, tags, studio) from stash-box for the newly-linked scenes.

## Safety model

1. **`--dry-run` is the default.** You must pass `--commit` to mutate
   anything. This protection is never removed.
2. **Default action is `tag`** — fully reversible.
3. **Destructive actions (merge, delete, files commit) require an
   interactive `YES` prompt** with a summary of what will happen.
   `--yes` bypasses the prompt for scripted use, but `--commit` is still
   required separately, so a typo cannot trigger destruction.
4. **Filename-info-loss safety net.** When the scorer picks a keeper with
   a junk filename but a loser has a date-bearing structured name, the
   group is flagged `needs_review` instead of auto-decided. This prevents
   silently losing information encoded only in filenames.
5. **Idempotent.** Re-running `apply --commit` is safe — applied groups
   are skipped via `applied_at`.
6. **No filesystem touches.** Every destructive op goes through Stash's
   GraphQL API, so the container's view of paths is the only one that
   matters. Works seamlessly with NAS / Docker setups.
7. **API key never logged, never stored in config**, only read from env.
8. **`needs_review` groups are never applied automatically.**
9. **Stale-cache detection.** `custodian stats` samples cached scene IDs and
   warns when they no longer exist in Stash.

## Tests

```sh
go test ./...                                    # unit tests, no Stash needed
SFX_INT_STASH_URL=http://your-stash:9999 \
  go test ./internal/stash/... -run Integration  # read-only integration
```

The integration tests are env-gated so `go test ./...` stays hermetic.

## Architecture

```
┌──────────────┐  GraphQL   ┌──────────┐
│ custodian (binary) │ ─────────► │  Stash   │ ── manages files on disk
└──────┬───────┘            └──────────┘
       │
       ▼
┌──────────────┐
│ custodian.sqlite   │  ← scan cache, decisions, history
└──────────────┘
```

Module layout:

```
cmd/custodian/main.go        — entry point
internal/cli/          — cobra command tree (scenes, files, orphans, stats, config)
internal/config/       — YAML loader (embedded defaults)
internal/stash/        — GraphQL client + query catalog (v0.31.0)
internal/store/        — SQLite persistence (schema v4 with migrations)
internal/scan/         — workflow A/B/C scan orchestration
internal/decide/       — scene + file scoring engines
internal/merge/        — metadata union + filename parser for sceneMerge
internal/apply/        — tag, merge, delete, files-prune, orphans-link, rename
internal/report/       — text + JSON output for all workflows
internal/confirm/      — interactive YES prompt utility
```

## Workflow D — file organization

Moves and renames files on disk (via Stash's `moveFiles` API) so your
library has a consistent, browsable directory structure based on metadata.

```sh
./custodian organize scan                     # compute ideal paths for every scene
./custodian organize report                   # show proposed moves
./custodian organize report --filter conflict # naming collisions
./custodian organize apply --commit           # actually move files via Stash
```

The default template produces your existing convention:
```
/data/<Performer>/<date>_<Performer>-<Title>_<resolution>.<ext>
```

For Whisparr compatibility, switch to a studio-first template in `config.yaml`:
```yaml
organize:
  path_template: "{studio}/{date} - {title}/{date}_{performer}-{title}_{resolution}.{ext}"
```

Scenes without the required metadata (performer + date by default) are
skipped and left where they are. Run `custodian orphans scan` + Stash Scene
Tagger first to maximize metadata coverage, then organize.

## Interactive review (TUI)

`custodian review` launches a terminal UI for walking through duplicate
groups. Especially useful for `needs_review` groups where the scorer couldn't
auto-decide and you need to pick a keeper manually.

```sh
./custodian review                          # decided + needs_review groups
./custodian review --filter needs-review    # just the ones that need you
```

- **For `needs_review` groups**: press `o` to pick which scene to keep, then
  `Enter`. The status bar updates live.
- **For `decided` groups**: press `a` to accept the scorer's pick, or `o` to
  change it.
- `n` = not_duplicate, `d` = dismiss, `q` = quit.

The TUI only saves decisions locally. After reviewing, re-scan and apply:

```sh
custodian scenes scan
custodian scenes apply --action merge --commit
```

See [MANUAL.md](MANUAL.md) for the full review-to-delete walkthrough.

## Where to find help

- [PLAN.md](PLAN.md) — full design, locked-in decisions, phased roadmap
- [MANUAL.md](MANUAL.md) — step-by-step user guide
- [internal/config/default.yaml](internal/config/default.yaml) — every config
  field documented inline
- `./custodian help` — cobra-generated command tree
- `./custodian scenes help`, `./custodian files help`, `./custodian orphans help` — workflow-specific help
