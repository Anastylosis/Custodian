# Custodian User Manual

Custodian keeps one best copy of every scene in a Stash library. This manual
covers every command, the config file, the safety model, and the Stash
behaviours it is built around.

If you only read one thing, read [Safety model](#safety-model).

- [Install](#install)
- [First-time setup](#first-time-setup)
- [The chain](#the-chain)
- [Commands](#commands)
- [Configuration reference](#configuration-reference)
- [Safety model](#safety-model)
- [Reviewing in Stash](#reviewing-in-stash)
- [Sidecar files (subtitles)](#sidecar-files-subtitles)
- [Known Stash behaviours](#known-stash-behaviours)
- [Troubleshooting](#troubleshooting)

## Install

```sh
brew install anastylosis/tap/custodian     # macOS and Linux
yay -S custodian                           # Arch (AUR)
sudo dpkg -i custodian_*_amd64.deb         # Debian / Ubuntu
sudo rpm -i custodian_*_amd64.rpm          # Fedora / RHEL
```

Or download a binary from
[Releases](https://github.com/Anastylosis/Custodian/releases), or build:

```sh
go build -o custodian ./cmd/custodian
go install github.com/Anastylosis/Custodian/cmd/custodian@latest
```

The binary is self-contained: pure-Go SQLite, no cgo, no runtime dependencies.

## First-time setup

```sh
# 1. Write a config file.
custodian config init
# wrote default config to ~/.config/custodian/config.yaml

# 2. Point it at your Stash.
$EDITOR ~/.config/custodian/config.yaml
#   stash:
#     url: http://your-nas:9999

# 3. If your Stash has an API key, export it. It is never written to the
#    config file — only the name of the env var is.
export STASH_API_KEY=...

# 4. Check that everything Custodian needs is reachable.
custodian doctor
```

`custodian config path` prints which file is in use. Resolution order:
`--config`, then `./config.yaml` if it exists, then
`~/.config/custodian/config.yaml`.

The ledger — the SQLite mirror of your library — lives at
`./custodian.ledger.sqlite` if that file exists, otherwise
`~/.local/share/custodian/custodian.ledger.sqlite`. Every command takes
`--ledger` to override it. At 61k scenes the file is about 65 MB.

## The chain

```
sync → scan → canon → rejudge → plan → apply → prune
```

Each step reads what the last one wrote into the ledger. `canon` and `rejudge`
are optional and only matter for groups whose copies disagree on runtime.
`identify` is opt-in and off by default — it never runs unless you pass
`--via` — and feeds newly-identified scenes back through the chain above on
your next `sync`. `undo` reverses an `apply`. `file` and `captions` are
hygiene that rides along with the library rather than steps in the chain.

Running `custodian` with no arguments prints this list.

## Commands

### `custodian sync`

Mirrors Stash's scenes and files into the ledger.

Incremental by default: only scenes whose `updated_at` is newer than the last
sync's high-water mark are fetched. A first full sync of 61k scenes takes
under a minute; an incremental one that finds nothing takes milliseconds.

Incremental sync cannot see a scene that was deleted from Stash outright —
there is no `updated_at` event for "this row is gone". Run `--full`
periodically, and after any bulk deletion in Stash, so what vanished gets
tombstoned. Tombstoned rows are kept, never deleted, so the mutation log
keeps its referent.

| Flag | Default | Meaning |
|---|---|---|
| `--full` | off | resync everything and tombstone what is gone |
| `--per-page N` | 500 | scenes fetched per page |
| `--ledger PATH` | — | ledger file to use |

```
sync (incremental)
  since:    2026-08-28T08:50:29Z
  pages:    1
  scenes:   0
  files:    0
  gone:     0
  duration: 0s
```

### `custodian scan`

Runs the evidence generators against the ledger and judges each group.

Generators emit *evidence*, not verdicts. A group accumulates evidence from
all of them, and a policy reads the whole picture.

| Generator | Signal |
|---|---|
| `stash_id` | two scenes point at the same stash-box entry — near-proof of identity |
| `phash` | Stash's own `findDuplicateScenes`: perceptual distance plus duration delta |
| `multifile` | one scene holding several files (Stash attaches those by hash, so they are byte-equivalent) |
| `filename` | structured filenames agree after normalisation |
| `title_date` | title, date and studio agree; duration corroborates but never decides alone |

Verdicts:

| Verdict | Meaning |
|---|---|
| `certain` | shared `stash_id`, runtimes agree within tolerance — safe to auto-decide |
| `conflicted` | shared `stash_id`, runtimes disagree — held for review, or for `canon` |
| `probable` | phash and duration close, metadata corroborating — review only |
| `partial` | one copy matches the canonical runtime, the other is materially shorter: a clip, a trailer, or one part of a split |
| `mistag` | neither copy matches canonical, or one is *longer* than canonical. The fix is metadata, not deletion |
| `rejected` | evidence contradicts — different `stash_id`s from the same endpoint. Kept visible: it is a candidate stash-box mis-tag |
| `dismissed` | you marked it not-a-duplicate |

Only `certain` and `partial` groups are planned and applied by default.

| Flag | Default | Meaning |
|---|---|---|
| `--generators LIST` | all five | comma-separated generators to run |
| `--tolerance-pct F` | 2 | duration tolerance as a percent of the longer runtime |
| `--tolerance-seconds F` | 120 | flat seconds cap on that tolerance |
| `--ledger PATH` | — | ledger file to use |

"Durations agree" means within `min(pct, seconds)`. The default `min(2%, 120s)`
is stash-catalog-sync's empirically tested threshold. Loosening it to 10%
moves groups from `conflicted` to `certain` — on the reference library, 656
certain at the default versus 843 at 10% — which is a decision about how much
you trust a shared `stash_id` on its own.

Every generator but `phash` reads only the ledger. `phash` calls Stash live
(read-only); without a reachable server it is skipped with a note.

### `custodian canon`

Fetches a scene's true runtime from an authority outside your library, so
`rejudge` can tell a trimmed copy from a clip from a mis-tag.

Two sources, both optional; configure whichever you have:

- **stash-box** — `scrapeMultiScenes` against a configured endpoint. On a
  30-scene sample against StashDB, 29 matched.
- **FSS** — an [FSS](https://github.com/Anastylosis/FSS) catalogue, read from
  its SQLite database and/or its JSON exports. Matching is exact-key on
  normalised title plus date, so its hit rate is lower but its answers are
  unambiguous.

| Flag | Default | Meaning |
|---|---|---|
| `--source NAME` | all configured | `stash-box`, `fss`, or empty for every source |
| `--endpoint URL` | `sources.stash_box_endpoint` | which stash-box to ask |
| `--fss-db PATH` | `sources.fss.db` | FSS's SQLite database |
| `--fss-json DIR` | `sources.fss.json_dir` | directory of FSS's `*.json` exports |
| `--verdict NAME` | `conflicted` | which groups' scenes need a runtime |
| `--limit N` | 0 | cap how many scenes are looked up |

`canon` only fetches and stores. `rejudge` is what acts on the results.

### `custodian identify`

**Not run by default, and never guesses which route you want** — REBUILD.md
§0's decision: Custodian does not identify scenes unless asked, and it never
sets `stash_ids` itself. `--via` is required.

> Matches are not proof of identity — stash-box fingerprint matching in
> particular has false positives. Nothing is written without `--commit`.

- **`--via url`** — scenes carrying a URL (e.g. from FSS) and no `stash_id`
  are asked, through Stash's own URL scraper, what that page says. Empty
  `title`/`code`/`date`/`details`/`director` fields are filled from the
  answer (never overwritten); a stash-box id in the answer is handed to
  Stash's own `metadataIdentify`, restricted to that one scene.
- **`--via stash-box`** — scenes with no `stash_id` are pre-screened with
  `scrapeMultiScenes`: only a scene with exactly one candidate whose
  duration matches within the trimmed tolerance (5%/90s) is handed to
  `metadataIdentify`; everything else is held and listed as such.

Neither route ever calls `sceneUpdate` with `stash_ids` — the identity
decision stays Stash's own job. After a commit, `identify` re-syncs the
ledger and reports how many scenes gained a `stash_id`.

| Flag | Default | Meaning |
|---|---|---|
| `--via NAME` | — (required) | `url` or `stash-box` |
| `--endpoint URL` | `sources.stash_box_endpoint` | stash-box to scrape (`--via stash-box`) |
| `--limit N` | 0 | cap how many scenes are considered |
| `--commit` | off | actually write to Stash |
| `--yes` | off | skip the interactive YES prompt (still needs `--commit`) |
| `--ledger PATH` | — | ledger file to use |

### `custodian rejudge`

Re-classifies every live `conflicted` and `certain` group whose scenes all
have a canonical runtime:

- every scene matches canonical → `certain`
- one matches and the rest are materially shorter → `partial`
- any scene is *longer* than canonical, or sources disagree → `mistag`

A copy within the *trimmed* threshold of canonical — a cut intro or outro —
counts as the same scene and competes on quality. Beyond it, completeness wins
outright: a 1080p copy at half the runtime loses to a complete 720p one.

| Flag | Default | Meaning |
|---|---|---|
| `--trimmed-pct F` | 5 | trimmed tolerance as a percent of canonical |
| `--trimmed-seconds F` | 90 | flat seconds cap on that tolerance |

Read-only against Stash: `rejudge` only reads and writes the ledger.

### `custodian plan`

Chooses the keeper in every group and reports why.

"Best copy" is decided pairwise, in this order, and a difference below a
rule's threshold is a tie that falls through to the next rule:

1. **completeness** — a `partial` copy never competes on quality at all
2. **resolution tier** — 1080-tier beats 720-tier; 1920×1080 and 1920×1088 tie
3. **bitrate**, above a significance threshold
4. **codec preference**
5. **file size**, above a threshold
6. **filed location** — the file already under the organize template's base
7. **older scene id** — the terminal tiebreak

Metadata richness is deliberately not a rule: `apply` unions the losers'
metadata onto the keeper first, so the richer copy loses nothing by losing.

```
group 002fb8ae959b… (certain)
  keeper: 73419 · 4K · 25.6 Mbps · 6.44 GiB · 2025-09-11_Shay.Sights-…_4k.mp4
  loser:  77765 · 1080p · 12.3 Mbps · 3.10 GiB · 2025-09-11_Shay.Sights-…_1080p.mp4
  tier: 4K vs 1080p → 73419 wins
  reclaimable: 3.10 GiB
```

The last line before `reclaimable` names the rule that actually decided it, in
its own terms — not a rule name and a shrug. On the reference library's 656
certain groups: tier decided 302, bitrate 228, codec 12, size 1, location 19,
and the terminal scene-id tiebreak 94.

| Flag | Default | Meaning |
|---|---|---|
| `--verdict NAME` | `certain` | which verdict to plan: `certain`, `conflicted`, `dismissed`, `partial` |
| `--limit N` | 0 | cap how many groups are planned |
| `--json` | off | emit the planned groups as JSON |

`plan` never calls Stash and never touches a file.

### `custodian apply`

Acts on the plan. **Dry run by default.**

1. Downloads a **verified** database backup and refuses to continue without
   one. The manifest is kept beside it.
2. Unions each loser's metadata onto the keeper via `sceneUpdate`. The keeper
   is complete from this point.
3. Tags every loser `_custodian_quarantine`, moves its file into the
   quarantine directory with `moveFiles`, and moves its subtitles after it.

Scenes in a `mistag` group are tagged `_custodian_mistag` instead, and nothing
is moved: the fix there is metadata, not deletion.

Nothing is deleted. The loser scene stays in Stash carrying its tag.

```
=== custodian apply (DRY RUN) ===
quarantine: /data/_quarantine
tag:        _custodian_quarantine
backup:     would download a verified backup to ~/.local/share/custodian/backups first

group 009feb303cf8… keeper 78859
  union:   would sceneUpdate scene 78859: tag_ids
  loser 50753 (174.55 MiB)
    tag:     would add
    move:    /data/Lisa Ann/2014-04-19_Lisa.Ann-Afternoon.Tease_480p.mp4 → /data/_quarantine/…
  reclaimable: 174.55 MiB

would quarantine: 3 groups, 3.85 GiB reclaimable once pruned
skipped:    0 groups
failed:     0 groups

DRY RUN — nothing was sent to Stash, nothing moved on disk, and the
mutation log is untouched. Re-run with --commit to act on this plan.
```

| Flag | Default | Meaning |
|---|---|---|
| `--commit` | off | actually write to Stash and move files |
| `--yes` | off | skip the interactive YES prompt (still needs `--commit`) |
| `--quarantine-dir PATH` | `quarantine.dir` | where losers' files go, in the server's path notation |
| `--backup-dir PATH` | `quarantine.backup_dir` | where the verified backup is written |
| `--limit N` | 0 | cap how many groups are acted on |

A commit run also writes `<backup-name>.changes.json` beside its backup: per
group, the keeper's pre-union field values, each loser's original path, the
sidecars moved, and the tag added.

### `custodian prune`

The only irreversible step, and it runs on nothing that was not first
quarantined and logged. **Dry run by default.**

For every group `apply` quarantined more than `--age` ago, whose losers still
carry the quarantine tag:

1. take a verified database backup;
2. `sceneMerge` the losers into the keeper, which carries their play history;
3. `deleteFiles` their videos from disk.

A loser someone un-tagged in Stash has been rescued, and its whole group is
left alone. Deleting the quarantine directory by hand also works — Stash's
next clean drops the scenes.

| Flag | Default | Meaning |
|---|---|---|
| `--age D` | `30d` | how long a quarantine must have sat |
| `--commit` | off | actually merge and delete |
| `--yes` | off | skip the interactive YES prompt |
| `--backup-dir PATH` | `quarantine.backup_dir` | where the verified backup is written |
| `--limit N` | 0 | cap how many groups are acted on |

`prune` is not undoable. Its verified backup is its reversal.

### `custodian undo`

Reverses what `apply` did to one group — or, with `--run`, to every group one
apply invocation quarantined — by reading what it wrote to the mutation log.
**Dry run by default.**

1. Remove the quarantine tag from each loser.
2. Move each loser's file back to its recorded original path, refusing if a
   different file now occupies it, and move its subtitles with it.
3. Restore the keeper's recorded pre-union field values, skipping any field
   someone has changed since the apply rather than overwriting it.

`undo` never adds anything. Only a group still in the `quarantined` verdict
can be undone; `prune` has already merged and deleted a `reclaimed` one.

```sh
custodian undo 009feb303cf8            # one group, by signature
custodian undo --run 14                # everything one apply run quarantined
custodian undo --run 14 --commit
```

### `custodian file`

Moves and renames files so the library has a consistent directory structure,
computed from a template and the scene's metadata.

Video moves go through Stash's `moveFiles`; sidecars travel with them.
Scenes that are the losing side of a quarantined or partial duplicate group
are skipped — they are about to leave the library.

| Flag | Default | Meaning |
|---|---|---|
| `--commit` | off | actually move files |
| `--yes` | off | bypass the interactive YES prompt |
| `--limit N` | 0 | cap how many scenes are considered |

Scenes missing any of `organize.required_fields` (performer and date by
default) are left where they are. On the reference library, a full dry run
over 61,217 scenes takes under a second and reports 1,040 to move, 528 to
rename, 26,914 already filed, 31,980 with no usable metadata, and 755
conflicts — target paths two scenes both want, which you should inspect
before any commit.

### `custodian captions`

Matches loose subtitle files — ones that were never named after their video —
back to scenes, and moves them into place.

```sh
custodian captions scan  --dir /data/webm
custodian captions scan  --dir ~/Downloads/Subs --json report.json
custodian captions apply --dir /data/webm --commit
```

`--dir` can point anywhere. A directory that exists on this machine is read
locally; otherwise it is treated as a path on the library backend and
translated through `sidecars.path_map`. Subtitles and library can be on
different machines: apply streams each file across and removes the original,
landing on a temporary name first so a half-written subtitle is never indexed
as a valid caption.

| Flag | Default | Meaning |
|---|---|---|
| `--dir PATH` | — | folder holding the subtitles |
| `--recursive` | on | descend into subdirectories |
| `--candidates N` | 3 | how many candidates to show per subtitle (`scan`) |
| `--json PATH` | — | also write the full result to this file (`scan`) |
| `--include-likely` | off | also move LIKELY matches, not just CONFIRMED (`apply`) |
| `--commit` | off | actually move files (`apply`) |

#### How matching works

Filenames alone are not enough. These are real pairs:

```
StudioHandleXXX_Mommy-and-Son-Nesting-Season+(transcribed+on+25-Jul-2024).srt
  → 2023-05-23_Some.Performer-stepMommy.and.stepSon.Nesting.Season_1080p.mp4

thehousenextdoor2024 - The Reluctant Dog Sitter - Compressed.srt
  → The-Reluctant-Pet-Sitter-Part-1.mp4
```

Independent signals are combined:

| Signal | Weight | Notes |
|---|---|---|
| Studio / JAV code | +60 | `VENU-765`, `RCTD-590` — near-decisive when present |
| Exact filename | +100 | the subtitle is already named after the video |
| Name similarity | +0…70 | scene **title** and **filename** scored separately, better wins |
| **Runtime** | +0…45, −30 | the subtitle's last cue against the scene duration |
| Matching date | +25 | `YYYY-MM-DD` in the filename against the scene date |
| Creator agreement | +20 | narrows the field; never decides on its own |

**Title and filename are compared independently.** Libraries carry the
identifying information in one place or the other: in a measured 61k-scene
library 52% of scenes had no title at all and relied entirely on a structured
filename, while a stash-box-tagged library is the reverse — good titles beside
filenames like `1080p.wmv`. Scoring a merged blob served neither. The report
says which won: `title 100%` or `filename 57%`.

**Creator names come from your own library.** Performer and studio names plus
their aliases are fetched from Stash and squashed into handles, split out of
name similarity so two unrelated clips by one creator do not look alike.
Nothing is hard-coded, so it stays correct as the library grows.

**Runtime is what makes this trustworthy.** Two scenes can share a title, a
performer and a studio, but not a length:

```
Nurse+River+Soothes+the+Aching+Patient+WM+(transcribed…).srt   10:52
  → #55739  Nurse River Soothes the Aching Patient   10:54   +2s     ✓
    #75175  Nurse River Soothes the Aching Patient   33:49   +1377s  ✗
```

Names were a dead tie; runtime was not. A subtitle running *past* the end of a
video counts as evidence against the pairing, though a few seconds of overrun
is tolerated — the measurement takes the maximum timestamp, which includes cue
end times.

#### Verdicts

| Verdict | Meaning | `captions apply` |
|---|---|---|
| `CONFIRMED` | name and runtime agree, no rival close | moved |
| `LIKELY` | good evidence, weaker on one axis | moved only with `--include-likely` |
| `AMBIGUOUS` | several candidates too close to separate | left alone |
| `UNMATCHED` | nothing credible in the library | left alone |

The gap to the runner-up matters as much as the absolute score: a high score
with a near-tie is exactly where an automatic choice would be wrong, so it is
reported as `AMBIGUOUS` for you to settle.

Each moved file is named `<video-stem>.<lang>.srt`, and the language is
detected from the subtitle's **own text**, not its filename. Script detection
handles CJK, Hangul and Cyrillic; Latin-script languages are separated by
function-word frequency. A subtitle whose language cannot be determined is
left alone rather than guessed at — a wrong caption tag is worse than none.

Stash only reads `srt` and `vtt` captions. Other formats in
`sidecars.extensions` are still moved so they travel with their video, but
Stash will not display them.

### `custodian groups`

Lists duplicate-candidate groups from the last scan, with the evidence.

```
group 00558d6a4f3a… (conflicted)
  reason: 2454.06s vs 2672.64s
  scenes:
    67994 · 40m54s · 1.99 GiB · 2016-10-20_Lauren.Phillips-…_1080p_1.mp4
    9904 · 44m33s · 3.78 GiB · 2016-10-20_Lauren.Phillips-…_1080p.mp4
  reclaimable: 1.99 GiB
```

| Flag | Default | Meaning |
|---|---|---|
| `--verdict NAME` | all | filter to one verdict |
| `--limit N` | 0 | cap how many groups are printed |
| `--json` | off | emit as JSON |

Two subcommands overrule the policy:

```sh
custodian groups dismiss 00558d6a4f3a --reason "different scenes, same title"
custodian groups restore 00558d6a4f3a
```

`dismiss` records the decision against the group's **file hashes**, not its
scene ids — scene ids die on a merge and change on a rescan, so a decision
keyed on one would not survive the thing it was made about. Every `scan`
re-reads these decisions and re-dismisses the group it rebuilds. `restore`
clears the decision so the group is judged again on the next scan.

The signature argument accepts the truncated prefix `groups` prints, ellipsis
and all, as long as it is unique; an ambiguous prefix is refused rather than
guessed at.

### `custodian log`

Prints the mutation log, newest first: every write Custodian made, recorded
before it was sent and stamped with its outcome afterwards, so a run that died
mid-way still shows what it was attempting. A dry run writes nothing here.

| Flag | Default | Meaning |
|---|---|---|
| `--limit N` | 50 | how many entries to print (0 = all) |

### `custodian status`

The headline numbers. See the [README](README.md#status) for a full example.

`--json` emits the same figures as a JSON object.

### `custodian config`

| Subcommand | What it does |
|---|---|
| `init` | write a default config file, refusing to overwrite an existing one |
| `path` | print the config file path in use |
| `show` | print the *effective* config: defaults merged with your overrides |
| `validate` | check the file for retired keys and bad values |

`validate` exists because YAML has no notion of a key that used to mean
something. yaml.v3 silently drops a key with no matching field, so a config
written for an older Custodian keeps loading and quietly stops meaning
anything. `validate` names each retired key and what replaced it:

```
~/.config/custodian/config.yaml

  apply     retired — `apply` has one behaviour: union, tag, quarantine. It
            is a dry run unless you pass --commit. The loser and keeper tags
            are quarantine.tag and quarantine.mistag_tag
  merge     retired — the union policy comes from stash-go
            (DefaultUnionPolicy) so every project here merges scenes the same
            way
  scoring   retired — the keeper rules are built in and ordered resolution
            tier -> bitrate -> codec -> size -> filed location -> older scene
            id; `custodian plan` quotes which one decided each group…
```

### `custodian doctor`

Read-only. Checks that Stash answers, that the ledger is readable, that the
sidecar backend opens, and that `sidecars.path_map` resolves real library
paths to readable ones — then prints a **gap report** from the ledger
(REBUILD.md §7 phase 0): files with no phash, files sitting in the
quarantine directory that no logged move put there ("foreign" quarantine),
scenes with no primary file, scenes tombstoned in the ledger but still
referenced by a live duplicate group, and how the ledger's last sync
compares to what Stash reports right now. Each finding is one line with a
suggested action. Run it after editing the config, and before the first
`--commit`.

```
gap report:
  phash gap:      1401 files, 2.34 TiB, 1 with a stash_id already — `custodian doctor --generate-phashes`
                    /data/tmp4: 1360 of 4071 live
                    /data/tmp3: 23 of 5040 live
                    /data/_quarantine: 16 of 16 live
  quarantine:     16 foreign file(s), 13.86 GiB — not put there by any logged move; inspect by hand
                    /data/_quarantine/…
  no primary:     none
  tombstoned:     none
  sync age:       last 2026-08-28T08:50:29Z (incremental); ledger matches Stash at 61217 scenes
```

`--generate-phashes` switches `doctor` into a different mode: it collects
the scene ids of every live file with no phash and, with `--commit`, hands
them to Stash's generate task (`MetadataGenerate`, `Phashes: true`), waits
for the job, then re-fetches those scenes by id and upserts them into the
ledger — `MetadataGenerate` does not bump `scene.updated_at`, so an
incremental `sync` would never notice they changed. **Dry run by default.**

```
generate phashes: 1401 file(s) with none
  first paths:
    /data/Tara Tainton/2009-12-21_Tara.Tainton-Getting.Off.with.My.Sexy.Bandanas_480p.mp4
    /data/_quarantine/…

DRY RUN — nothing was sent to Stash. Re-run with --commit to act.
```

| Flag | Default | Meaning |
|---|---|---|
| `--sample N` | 5 | how many real library paths to test the mapping against |
| `--path PATH` | — | test one Stash path instead of sampling |
| `--json` | off | emit the gap report as JSON instead of the text checks |
| `--generate-phashes` | off | generate phashes for every live file that has none, instead of printing the report |
| `--commit` | off | with `--generate-phashes`, actually start the job |
| `--yes` | off | skip the interactive YES prompt (still needs `--commit`) |
| `--limit N` | 0 | with `--generate-phashes`, cap how many files are acted on |

## Configuration reference

`custodian config init` writes a fully commented file; every field is
documented inline in
[`internal/config/default.yaml`](internal/config/default.yaml). The shape:

```yaml
stash:
  url: http://localhost:9999
  api_key_env: STASH_API_KEY     # name of the env var, never the key itself

scan:
  phash_distance: 4              # used by the phash generator only
  duration_diff_seconds: 1.0

organize:
  base_dir: /data
  path_template: "{performer}/{date}_{performers}-{title}_{resolution}.{ext}"
  space_char: "."
  folder_space_char: " "
  performer_strategy: first_alphabetical
  max_performers_in_filename: 3
  required_fields: [performer, date]
  rename_in_place: true

sidecars:
  enabled: true
  extensions: [srt, ass, ssa, vtt, sub, idx, smi]
  access: auto                   # auto | local | ssh | off
  path_map: []
  ssh: {host: "", port: 22, user: "", key_file: "", …}

quarantine:
  dir: /data/_quarantine
  tag: _custodian_quarantine
  mistag_tag: _custodian_mistag
  backup_dir: ""                 # empty → ~/.local/share/custodian/backups

sources:
  stash_box_endpoint: ""
  fss:
    db: ""
    json_dir: ""
```

### `stash`

The API key is read from the environment variable named in `api_key_env` and
never written to the config file, never logged, and never stored on the loaded
config struct. Leave the variable unset if your Stash has no key.

### `scan`

`phash_distance` and `duration_diff_seconds` are passed to Stash's
`findDuplicateScenes` and affect the `phash` generator only. Distance 0 is
byte-equivalent perceptual hashes; 4 catches re-encodes; above 8 the false
positives outnumber the finds. Loosening it is not the way to find more
duplicates — the `stash_id` signal finds two orders of magnitude more.

The duration tolerances that decide `certain` versus `conflicted` are `scan`
flags, not config: they are a judgement call you make per run.

### `organize`

Template variables, filled from the scene's metadata:

| Variable | Value |
|---|---|
| `{performer}` | primary performer, per `performer_strategy` |
| `{performers}` | all performers joined by `_`, omitted entirely above `max_performers_in_filename` |
| `{studio}` | studio name |
| `{title}` | scene title |
| `{date}` | scene date, `YYYY-MM-DD` |
| `{year}` | the first four characters of the date |
| `{resolution}` | `480p`, `720p`, `1080p`, `4k`, `8k` |
| `{ext}` | file extension |
| `{id}` | Stash scene id — always unique, the fallback |

Folder separators in the template create subdirectories. `space_char` applies
to filename components, `folder_space_char` to folder names, so
`Some Performer` can stay a readable folder while becoming
`Some.Performer` in the filename. Paths are truncated with an `_{id}` suffix
before they exceed filesystem limits.

Two studio-first alternatives, for a Whisparr-compatible layout:

```yaml
path_template: "{studio}/{date} - {title}/{date}_{performer}-{title}_{resolution}.{ext}"
path_template: "{studio}/{date}_{performer}-{title}_{resolution}.{ext}"
```

`base_dir` is also what the keeper's location tiebreak reads: a file already
under it counts as filed.

### `quarantine`

`dir` is in **the server's** path notation and must sit inside a configured
Stash library path, or `moveFiles` refuses. It need not exist — `moveFiles`
creates it.

`backup_dir` is on the machine running Custodian, and empty means
`~/.local/share/custodian/backups`.

### `sources`

Both are optional and only feed `custodian canon`. Set
`stash_box_endpoint` when your Stash has more than one stash-box configured
and you do not want to pass `--endpoint` every time. `fss.db` and
`fss.json_dir` may both be set; the two are read together.

### Retired keys

`scoring`, `review_policy`, `apply`, `merge`, `orphans` and
`stash_box_fingerprints` no longer exist. `custodian config validate` names
each one it finds and what took its place.

## Safety model

1. **Dry run is the default.** Every command that writes anything computes its
   plan in full and writes nothing without `--commit`. That protection is
   never removed.
2. **`--commit` asks for a typed `YES`**, with a summary of what will happen.
   `--yes` bypasses the prompt for scripted use, but `--commit` is still
   required separately, so no single typo can start a destructive run.
3. **A verified database backup precedes every commit.** `apply` and `prune`
   download Stash's database, verify it is real SQLite rather than a zip of
   blobs, and refuse to continue if they cannot. A manifest is written beside
   it.
4. **Quarantine, not deletion.** `apply` moves a loser's file into a directory
   inside the library and tags its scene. The scene stays in Stash, visible
   and rescuable, until `prune` — a separate, deliberate command — removes it
   30 days later.
5. **Every write is logged before it is sent.** The mutation log records what,
   when, from and to, including sidecar and quarantine moves, and is stamped
   with the outcome afterwards. `custodian log` reads it; `prune` and `undo`
   depend on it.
6. **A changes file per commit run.** `<backup-name>.changes.json` records the
   keeper's pre-union field values, each loser's original path, the sidecars
   moved, and the tag added.
7. **`undo` reverses an apply** from that log, and only ever removes what
   Custodian added. It refuses to move a file back onto a path something else
   now occupies, and skips restoring any keeper field someone has changed
   since.
8. **`prune` is the only irreversible step**, it runs only on what was
   quarantined and logged, it skips anything that was un-tagged, and its
   verified backup is its reversal.
9. **The API key is never logged and never stored in the config file** — only
   the name of the environment variable is.
10. **Sidecars are never overwritten.** A subtitle whose destination exists is
    left in place and reported. Cross-filesystem moves copy to a temporary
    name first, so an interrupted copy cannot leave a truncated subtitle where
    Stash would index it.

## Reviewing in Stash

Custodian has no review UI. Review happens in Stash's own interface, through
tags, because that is where the video player, the metadata and the muscle
memory already are.

| Tag | On | Means |
|---|---|---|
| `_custodian_quarantine` | every loser `apply` quarantined | its file has moved to the quarantine directory and `prune` will delete it after 30 days |
| `_custodian_mistag` | every scene in a `mistag` group | neither copy matches the canonical runtime, or one is longer than it. The metadata is wrong, not the file |

Both tag names are configurable under `quarantine`.

**To review a quarantine:** filter Stash by `_custodian_quarantine`, play what
you are unsure of, and remove the tag from anything that should stay. `prune`
skips any group with an un-tagged loser, so an un-tag is a veto — no further
action needed. Then run `custodian undo <signature>` to put the file back
where it was.

**To review mis-tags:** filter by `_custodian_mistag`. These are scenes whose
`stash_id` points at the wrong stash-box entry. Custodian will not touch them:
the fix is to correct the `stash_id` in Stash, then re-run `sync` and `scan`.

**To dismiss a group permanently**, run
`custodian groups dismiss <signature>`. The decision is keyed on the set of
file hashes rather than scene ids, so it survives a merge or a rescan — scene
ids do not.

## Sidecar files (subtitles)

A sidecar is a file that shares a video's name:

```
clip_1080p.mp4
clip_1080p.en.srt
clip_1080p.es.srt
```

### Why this needs special handling

Stash does **not** index subtitles as files. At scan time it looks next to
each video for `<stem>.srt` or `<stem>.<lang>.srt` and attaches what it finds
as a *property of the scene*. A caption therefore has no file id, no row in
the file table, no identity of its own. You can see this on any Stash
instance — a caption the UI is actively displaying returns nothing from
`findFiles`:

| `findFiles` query | count |
|---|---|
| path includes `.mp4` | 51,494 |
| path includes `.srt` | **0** |

`moveFiles` takes `ids: [ID!]`. There is nothing to pass it for a subtitle.
This is an API limitation, not a design choice. So Custodian moves sidecars
itself — the only part of the tool that touches the filesystem directly, and
the reason `sidecars.path_map` exists.

When a video moves or is renamed, its sidecars follow, keeping their language
suffix:

```
tmp2/clip_1080p.mp4      →  Some Performer/2024-01-01_Clip_4k.mp4     (via Stash)
tmp2/clip_1080p.en.srt   →  Some Performer/2024-01-01_Clip_4k.en.srt  (via custodian)
tmp2/clip_1080p.es.srt   →  Some Performer/2024-01-01_Clip_4k.es.srt  (via custodian)
```

Preserving `.en` / `.es` is what keeps multi-language captions working: a bare
`<stem>.srt` is filed by Stash as language `00` (unknown), and two
unknown-language tracks would collide on the same filename.

### `path_map`

This is the part people get wrong. Stash reports paths as seen from inside its
own process — usually `/data/...` in a Docker deployment, which often exists
nowhere else. Map that onto something the machine running Custodian can reach.
Longest matching prefix wins.

| Where Custodian runs | `path_map` |
|---|---|
| On the NAS itself | `stash: /data` → `local: /share/main/Videos` |
| Linux box, share mounted | `stash: /data` → `local: /mnt/nas/Videos` |
| Windows, mapped drive | `stash: /data` → `local: 'Z:\Videos'` |
| Windows, UNC path | `stash: /data` → `local: '\\nas\main\Videos'` |
| Same filesystem as Stash | leave `path_map` empty |

Windows paths are handled natively: backslashes, drive letters and UNC roots
are all understood, and prefix matching is case-insensitive for them.

### When the library isn't mounted

Use the SSH backend. It speaks SFTP rather than shelling out to `ssh`, which
matters for media filenames containing spaces, `+`, `(`, `'` and `&`.

```yaml
sidecars:
  access: ssh
  path_map:
    - stash: /data
      local: /share/main/Videos    # the path on the NAS
  ssh:
    host: nas.local
    user: me
    key_file: ~/.ssh/id_ed25519    # or leave empty to use ssh-agent
    password_env: SJ_SSH_PASSWORD  # secrets come from the environment
    accept_new_host_key: false
```

Authentication order: explicit key, ssh-agent, then `~/.ssh/id_ed25519` /
`id_rsa` / `id_ecdsa`, then password. Host keys are verified against
`~/.ssh/known_hosts`. `accept_new_host_key` trusts an *unknown* host on first
contact; a **changed** key on an already-known host is always rejected.

### Verify it before relying on it

```sh
custodian doctor
```

This resolves real library paths through your `path_map` and reads them back,
so a mapping that parses fine but points nowhere fails loudly here rather than
silently skipping files during an apply. Use `--path /data/some/file.mp4` to
probe one specific path.

After a run that moved subtitles, run a Stash scan over the affected folders
so the captions are re-attached.

## Known Stash behaviours

Verified against Stash v0.31.1 (schema 85). Each one cost a bug to discover;
none of them are guesses.

- `sceneMerge` does **not** union scene-level metadata. The caller passes
  `values`, which is why `apply` computes the union itself.
- Stash attaches a re-scanned file to an existing scene by **oshash/md5
  only**, never phash — so the several files inside one scene are always
  byte-equivalent. That is the `multifile` signal.
- Cross-scene identical-hash groups cannot persist: Stash folds them into one
  scene at scan time. Only the within-scene case exists.
- `findDuplicateScenes` is unpaginated. It has to be stream-decoded.
- `deleteFiles` removes the file from **disk**. `destroyFiles` removes only
  the database row, and the next scan re-adds the file. The names suggest the
  opposite of what they do.
- Subtitles have no file id, so `moveFiles` cannot move them. Filesystem
  access is the only route.
- `moveFiles` creates the destination directory itself. The quarantine
  directory need not pre-exist — it only has to lie inside a library path.
- `backup(download: true, includeBlobs: true)` returns a **zip** when blobs are
  stored on disk, which is the normal configuration. Only a raw-SQLite
  download can be verified, which is what the backup gate insists on.
- The `resolution` string on a file lies: 720-wide is not 720p, and 4K is
  reported as "Full HD". Use width and height.
- `SceneFilter.UpdatedAfter` works and is what makes incremental sync
  possible, but it is not mirrored into saved-filter criteria.

## Troubleshooting

**`custodian doctor` says the library cannot be opened.** `sidecars.access` is
`auto`, which picks `ssh` when `sidecars.ssh.host` is set and `local`
otherwise. If the library is a mount that is not currently mounted, `local`
will fail on every path.

**`doctor` resolves paths but finds no sidecars.** That is a correct result if
there are none. `--path` a video you know has a subtitle beside it to be sure.

**`apply` refuses to start: no verified backup.** Stash returned a zip rather
than raw SQLite, which happens when blobs are stored on disk. That is the
gate working — a zip cannot be verified, and an unverified backup is not a
backup.

**`moveFiles` refuses the quarantine directory.** `quarantine.dir` must sit
inside a path Stash has configured as a library, in the server's own notation.
`/data/_quarantine` when the library is `/data`.

**`scan` reports far fewer groups than you expect.** Check
`without stash_id` in `custodian status`. The `stash_id` signal cannot see a
scene that has no stash-box entry, and on a typical library that is half of
it. `phash` and `filename` cover some of the rest.

**A group you disagree with keeps coming back.** `custodian groups dismiss
<signature>`. Decisions are keyed on the set of file oshashes, so a dismissal
survives a rescan.

**Everything is `conflicted`.** The default duration tolerance is
`min(2%, 120s)`. Two copies of the same scene from different sources often
differ by more. `custodian canon` plus `custodian rejudge` is the principled
answer; `--tolerance-pct 10` is the blunt one.
