# StashJanitor User Manual

## Install

**Download a release binary** from the
[Releases](https://github.com/Anastylosis/StashJanitor/releases) page — no
Go toolchain required. Extract and you're ready.

Or build from source:
```sh
git clone https://github.com/Anastylosis/StashJanitor.git
cd StashJanitor
go build -o stash-janitor ./cmd/stash-janitor
```

Or via `go install`:
```sh
go install github.com/Anastylosis/StashJanitor/cmd/stash-janitor@latest
```

The binary is fully self-contained (pure-Go SQLite, no cgo, no runtime deps).
Put it wherever you like — a dedicated directory is handy because it also
creates `config.yaml` and `stash-janitor.sqlite` next to itself.

## First-time setup

```sh
# 1. Generate a config file in the current directory.
stash-janitor config init
```

This creates `config.yaml` with commented defaults. Open it and set
your Stash URL:

```yaml
stash:
  url: http://your-nas:9999     # ← change this to your Stash address
  api_key_env: STASH_API_KEY    # reads the API key from this env var
```

The default is `http://localhost:9999`. If your Stash has no API key
configured, you can leave `STASH_API_KEY` unset.

```sh
# 2. (Optional) If your Stash uses an API key:
export STASH_API_KEY=your-key-here
```

The config file and the SQLite cache (`stash-janitor.sqlite`) are
created in whatever directory you run the command from. You can override
their locations with `--config <path>` and `--db <path>`.

To verify connectivity and see your library at a glance:

```sh
stash-janitor stats
```

This shows Stash version, total scenes, library size, percentage with
metadata, orphan count, and the state of each workflow's local cache.
It also warns if cached scenes no longer exist in Stash (stale cache).

## The three workflows

stash-janitor has three independent workflows. Run whichever ones apply to you —
they don't depend on each other.

---

### Workflow A: Cross-scene duplicates

**Problem:** Two separate Stash scenes contain the same video, maybe at
different resolutions or bitrates.

**What stash-janitor does:** Asks Stash for perceptually-similar scene groups (via
phash), scores each group to pick a keeper, and lets you tag, merge, or
delete the losers.

#### 1. Scan

```sh
stash-janitor scenes scan
```

This calls `findDuplicateScenes` on your Stash. At distance=4 (default)
it catches re-encodes; at distance=0 it only catches exact phash matches.

Takes ~2 seconds even at 60k scenes. All results are stored locally in
`stash-janitor.sqlite` — no need to re-scan unless your library changes.

Useful flags:
- `--distance 0` — strictest (byte-equivalent phash only)
- `--distance 8` — aggressive (more matches, more false positives)
- `--max-groups 100` — process only the first 100 groups (useful for testing)

#### 2. Review

```sh
# One-line summary.
stash-janitor scenes status

# Detailed per-group report. Each loser shows WHY it lost.
stash-janitor scenes report --filter decided | less

# Only groups the tool couldn't auto-decide:
stash-janitor scenes report --filter needs-review

# Machine-readable:
stash-janitor scenes report --json > groups.json
```

Example output:
```
#2  status=decided
    signature: 22871|83186
      KEEP  scene 22871     3840x2160  h264    3.90 GiB  [stashID,tags=12,perf=1] /data/.../4k.mp4
      drop  scene 83186     1920x1080  h264    528.48 MiB  [stashID,tags=12,perf=1] /data/.../1080p.mp4
                ↳ kept by: higher resolution: 3840x2160 vs 1920x1080
    reclaimable: 528.48 MiB
```

#### 3. Override (optional)

If the tool picked wrong, tell it. You can use the group `#` from the
report (easier) or the full signature:

```sh
# By group number from the report:
stash-janitor scenes mark --group 224 --as not_duplicate

# Or by full signature:
stash-janitor scenes mark --signature "22871|83186" --as not_duplicate

# Pin a specific keeper (overrides the scorer's pick):
stash-janitor scenes mark --group 224 --as force_keeper --keeper 83186

# Then re-scan to apply the override:
stash-janitor scenes scan
```

#### 4. Apply

Three action modes, from safest to most aggressive:

**Tag (safest — no data loss possible):**
```sh
stash-janitor scenes apply --action tag              # preview (dry run)
stash-janitor scenes apply --action tag --commit     # actually tag in Stash
```
Adds `_dedupe_loser` / `_dedupe_keeper` tags. Then go to Stash UI, filter
by `_dedupe_loser`, eyeball, bulk-delete what you agree with.

**Merge (recommended — preserves all metadata):**
```sh
stash-janitor scenes apply --action merge              # preview
stash-janitor scenes apply --action merge --commit     # interactive YES prompt
stash-janitor scenes apply --action merge --commit --yes  # scripted/cron
```
Per group, the merge pipeline:
1. Computes a metadata union (tags, performers, stash_ids, urls, play
   history, rating, organized).
2. **Filename metadata extraction**: if the keeper has no title/date but a
   loser file has a structured filename like
   `2024-12-15_Performer-Title_1080p.mp4`, parses the date and title and
   fills them in before merging.
3. Calls `sceneMerge` with the computed union as `values`.
4. Picks the best file as primary (resolution → bitrate → filename → size).
5. **Rename-on-merge**: if the winning file has a junk basename but a loser
   had a structured one, renames the winner via `moveFiles` to use the
   loser's filename (with the resolution token swapped to match).
6. Deletes the loser files from disk.

**Delete (destructive — use only when losers have nothing to preserve):**
```sh
stash-janitor scenes apply --action delete --commit
```
Calls `scenesDestroy(delete_file: true)` on every loser. No metadata
preservation. No rename. You see a warning explaining why merge is
better in most cases.

**Submit fingerprints to stash-box (optional, any mode):**
```sh
stash-janitor scenes apply --action tag --commit --submit-fingerprints
```
After a successful commit, sends the keeper scenes' fingerprints to
stash-box so future users who have the same files get automatic matches.

---

### Workflow B: Within-scene multi-file cleanup

**Problem:** A single Stash scene has 2+ files attached (Stash auto-
attaches files with the same oshash/md5). Only one file is needed.

**What stash-janitor does:** Scores the files by filename quality, path priority,
and modification time. Picks the best one as primary, deletes the rest.

```sh
stash-janitor files scan
stash-janitor files status
stash-janitor files report | less                # each loser shows WHY it lost
stash-janitor files apply                        # preview
stash-janitor files apply --commit               # interactive YES → delete losers
stash-janitor files apply --commit --yes         # scripted
```

The filename scoring uses a configurable regex. The default matches your
`YYYY-MM-DD_Performer-Title_Resolution.ext` convention, including import
suffixes (`_1080p_1.mp4`) and resolutions up to 8K. Files matching the
convention win over files that don't. Each loser line in the report shows
`↳ kept by: matches filename convention (loser does not)` or whichever
rule decided.

Override a specific scene:
```sh
stash-janitor files mark --scene-id 56297 --as keep_all
```

---

### Workflow C: Orphans — stash-box metadata lookup

**Problem:** Many scenes have no stash-box metadata (no stash_ids). The
filename is the only clue to what the scene is.

**What stash-janitor does:** Enumerates orphan scenes (stash_id_count = 0), queries
your configured stash-box by phash, stores matches locally for review.
Applying writes the stash_id link back to Stash so Stash's built-in
Scene Tagger can then pull the full metadata.

```sh
# Start small — this is the slow workflow.
stash-janitor orphans scan --max-scenes 500

# See what stash-box recognized.
stash-janitor orphans report --filter matched

# Link the matches back to Stash.
stash-janitor orphans apply --commit

# Then in Stash UI: run the Scene Tagger to pull metadata
# (title, performers, tags, studio, date) from stash-box.

# Later, scan the rest. Re-runs skip already-queried scenes.
stash-janitor orphans scan --max-scenes 5000
stash-janitor orphans scan                       # full library (may take 30+ min)
```

Useful flags:
- `--endpoint URL` — query a specific stash-box (default: first one in Stash config)
- `--endpoint all` — query **every** configured stash-box in one pass
- `--batch-size 50` — bigger batches (faster, heavier on stash-box)
- `--batch-delay 100ms` — less sleep between batches
- `--rescan` — re-query previously-looked-up orphans

The endpoint is auto-discovered from Stash's configuration if not passed.

During the scan, a progress line shows processed/total, matched count,
and estimated time remaining:
```
  progress: 500/33249 (2%)  matched: 5  no_match: 395  skipped: 100  elapsed: 42s  eta: 35m
```

---

## The local cache (stash-janitor.sqlite)

Every scan stores its results in a SQLite file (default `./stash-janitor.sqlite`).
This serves three purposes:

1. **Speed.** Reports and apply commands read from SQLite, not Stash.
   You can scan once and review over multiple days.

2. **Resume.** `orphans scan --max-scenes 500` processes 500, then stops.
   Next run starts where you left off. `scenes scan` re-uses your `mark`
   overrides across runs.

3. **Idempotency.** Applied groups are recorded. Re-running `apply` is
   safe — already-applied groups are skipped.

You can delete `stash-janitor.sqlite` at any time and re-scan. All source-of-truth
data lives in Stash; the SQLite is a read cache and decision journal.

**Stale-cache detection:** `stash-janitor stats` samples a few cached scene IDs
and warns if any no longer exist in Stash. If you see the warning, just
re-scan (`stash-janitor scenes scan`, `stash-janitor files scan`).

## Configuration reference

`stash-janitor config init` generates a fully-commented `config.yaml`. Key sections:

| Section | What it controls |
|---|---|
| `stash.url` | Your Stash instance URL |
| `stash.api_key_env` | Name of env var holding the API key (not the key itself) |
| `scan.phash_distance` | How aggressively to match (0=exact, 4=re-encodes, 8=aggressive) |
| `scan.duration_diff_seconds` | Max duration delta — strong false-positive filter |
| `scoring.scenes.rules` | Ordered rule chain for picking keepers in workflow A |
| `scoring.files.rules` | Ordered rule chain for picking the best file in workflow B |
| `scoring.files.filename_quality.pattern` | Regex for "good" filenames |
| `scoring.codec_priority` | Codec ranking (default: av1 > hevc > h264 > vp9) |
| `scoring.path_priority` | Path prefix ranking (e.g. /sorted > /inbox) |
| `apply.scenes.loser_tag` | Tag name for losers (default: `_dedupe_loser`) |
| `merge.scene_level.*` | How to union metadata during merge (tags: union, title: prefer_keeper, etc.) |
| `merge.post_merge_file_cleanup.rename_winner_filename` | Rename the winner to the loser's structured basename (default: on) |
| `organize.base_dir` | Root directory Stash sees (e.g. `/data`) |
| `organize.path_template` | Template for ideal paths (see Workflow D section) |
| `organize.space_char` | Space replacement in filenames (default: `.`) |
| `organize.folder_space_char` | Space replacement in folders (default: ` `) |
| `organize.performer_strategy` | How to pick primary performer: `first_alphabetical` or `first_listed` |
| `organize.required_fields` | Fields a scene must have to be moved (default: `[performer, date]`) |
| `organize.rename_in_place` | Also rename files already in the right folder (default: `true`) |
| `orphans.write_stash_id_on_apply` | Auto-link stash_ids during orphans apply (default: `false`) |
| `orphans.write_metadata_on_apply` | Also write title/date from scrape (default: `false`) |

Run `stash-janitor config show` to see the *effective* config after merging defaults
with your overrides.

---

### Workflow D: Organize — move and rename files by metadata

**Problem:** Files are scattered across performer folders and tmp dirs
with inconsistent names. You want a clean, browsable structure based
on the metadata in Stash.

**What stash-janitor does:** Computes an ideal path for every scene using a
configurable template + Stash metadata. Compares ideal vs actual, then
proposes moves/renames via Stash's `moveFiles` API.

**Recommended order:** run the other workflows first to maximize metadata:
```sh
stash-janitor orphans scan          # find stash-box matches for orphans
# (link matches in Stash UI via Scene Tagger)
stash-janitor scenes scan           # find and resolve duplicates
stash-janitor scenes apply --action merge --commit
stash-janitor files scan            # clean up multi-file scenes
stash-janitor files apply --commit
# NOW organize:
stash-janitor organize scan
stash-janitor organize report | less
stash-janitor organize apply --commit
```

#### Scan

```sh
stash-janitor organize scan                    # all scenes (can take a while at 60k)
stash-janitor organize scan --max-scenes 500   # test with a small sample first
```

For each scene, stash-janitor checks: does the scene have the required metadata
fields (performer + date by default)? If yes, compute the ideal path
from the template. If no, skip (leave the file where it is).

#### Report

```sh
stash-janitor organize report                    # proposed moves + renames (default)
stash-janitor organize report --filter all       # everything including skips
stash-janitor organize report --filter conflict  # two scenes → same target path
stash-janitor organize report --filter skip      # files without enough metadata
```

Example output:
```
[move] scene 20
  from: /data/unsorted/2018-11-09_Some.Performer-Some.Title_720p.mp4
  to:   /data/Some Performer/2018-11-09_Some.Performer-Some.Title_720p.mp4

[rename] scene 49
  from: /data/Addie Andrews/2020-02-03_Addie.Andrews-Complicit.Consumption_544p.mp4
  to:   /data/Addie Andrews/2020-02-03_Addie.Andrews-Complicit.Consumption_720p.mp4
```

#### Apply

```sh
stash-janitor organize apply                     # dry-run preview
stash-janitor organize apply --commit            # interactive YES → moveFiles via Stash
stash-janitor organize apply --commit --yes      # scripted
```

#### Templates

The default template matches your naming convention:
```yaml
organize:
  path_template: "{performer}/{date}_{performer}-{title}_{resolution}.{ext}"
```

Result: `/data/Some Performer/2024-12-15_Some.Performer-A.Long.Scene.Title_1080p.mp4`

For **Whisparr compatibility**, uncomment the studio-first template in
`config.yaml`:
```yaml
organize:
  path_template: "{studio}/{date} - {title}/{date}_{performer}-{title}_{resolution}.{ext}"
```

Result: `/data/Some Studio/2024-12-15 - A Long Scene Title/2024-12-15_Some.Performer-A.Long.Scene.Title_1080p.mp4`

Available template variables:

| Variable | Example | Source |
|---|---|---|
| `{performer}` | `Some Performer` | Primary performer (first alphabetically) |
| `{studio}` | `Some Studio` | Studio name from Stash |
| `{title}` | `A Long Scene Title` | Scene title (spaces → `space_char`) |
| `{date}` | `2024-12-15` | Scene date |
| `{year}` | `2024` | First 4 chars of date |
| `{resolution}` | `1080p`, `4k` | Derived from video height |
| `{ext}` | `mp4` | File extension |
| `{id}` | `42` | Stash scene ID (guaranteed unique) |

Folder parts use `folder_space_char` (default: space), filename parts
use `space_char` (default: dot). So `Some Performer` becomes the folder
`Some Performer/` but the filename part `Some.Performer`.

## Sidecar files (subtitles)

A **sidecar** is a file that shares a video's name:

```
clip_1080p.mp4
clip_1080p.en.srt
clip_1080p.es.srt
```

### Why this needs special handling

Stash does **not** index subtitles as files. At scan time it looks next to each
video for `<stem>.srt` or `<stem>.<lang>.srt` and attaches whatever it finds as
a *property of the scene*. A caption therefore has no file ID, no row in the
file table, no identity of its own.

You can see this on any Stash instance — a caption that the UI is actively
displaying returns nothing from `findFiles`:

| `findFiles` query | count |
|---|---|
| path includes `.mp4` | 51,494 |
| path includes `.srt` | **0** |

`moveFiles` — the mutation `organize` uses — takes `ids: [ID!]`. There is
nothing to pass it for a subtitle. **This is an API limitation, not a design
choice.** Before this feature existed, `organize` moved a video through
`moveFiles`, Stash moved only what it had an ID for, and every subtitle stayed
behind. The caption silently vanished from the scene.

So stash-janitor moves sidecars itself. This is the **only** part of the tool
that touches the filesystem directly, and it is why the config below exists.

### What it does

When `organize` moves or renames a video, its sidecars follow, keeping their
language suffix:

```
tmp2/clip_1080p.mp4      →  Some Performer/2024-01-01_Clip_4k.mp4     (via Stash)
tmp2/clip_1080p.en.srt   →  Some Performer/2024-01-01_Clip_4k.en.srt  (via stash-janitor)
tmp2/clip_1080p.es.srt   →  Some Performer/2024-01-01_Clip_4k.es.srt  (via stash-janitor)
```

Preserving `.en` / `.es` is what keeps multi-language captions working — a bare
`<stem>.srt` is filed by Stash as language `00` (unknown), and two unknown-
language tracks would collide on the same filename.

### Configuration

```yaml
sidecars:
  enabled: true
  extensions: [srt, ass, ssa, vtt, sub, idx, smi]
  access: auto          # auto | local | ssh | off
  path_map:
    - stash: /data
      local: /share/main/Videos
```

**`path_map` is the part people get wrong.** Stash reports paths as seen from
inside its own process — usually `/data/...` in a Docker deployment, which
often exists nowhere else. You must map that onto something the machine running
stash-janitor can reach. Longest matching prefix wins.

| Where stash-janitor runs | `path_map` |
|---|---|
| On the NAS itself | `stash: /data` → `local: /share/main/Videos` |
| Linux box, share mounted | `stash: /data` → `local: /mnt/nas/Videos` |
| Windows, mapped drive | `stash: /data` → `local: 'Z:\Videos'` |
| Windows, UNC path | `stash: /data` → `local: '\\nas\main\Videos'` |
| Same filesystem as Stash | leave `path_map` empty |

Windows paths are handled natively: backslashes, drive letters and UNC roots
are all understood, and prefix matching is case-insensitive for them.

### When the library isn't mounted

If stash-janitor runs somewhere with no access to the library — a workstation,
with the files on a NAS — use the SSH backend. It speaks SFTP rather than
shelling out to `ssh`, which matters for media filenames containing spaces,
`+`, `(`, `'` and `&`.

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

Authentication is tried in the order you would expect: explicit key, then
ssh-agent, then `~/.ssh/id_ed25519` / `id_rsa` / `id_ecdsa`, then password.
Host keys are verified against `~/.ssh/known_hosts`. `accept_new_host_key`
trusts an *unknown* host on first contact; a **changed** key on an already-known
host is always rejected regardless of the setting.

### Verify it before relying on it

```sh
stash-janitor sidecars check
```

This resolves real scene paths through your `path_map` and reads them back, so
a mapping that parses fine but points nowhere fails loudly here rather than
silently skipping files during an apply.

```
✓ backend reachable: ssh://me@nas.local:22

Testing 5 path(s):
  ✓ /data/Some Performer/Clip_con_subtitulos_1080p.mp4
      sidecar: Clip_con_subtitulos_1080p.en.srt
      sidecar: Clip_con_subtitulos_1080p.es.srt

readable: 5   failed: 0   sidecars found: 2
```

Use `--path /data/some/specific.mp4` to probe one file.

### Safety

- **Never overwrites.** If the destination already exists the sidecar is left
  in place and reported. A clobbered subtitle is unrecoverable; one left behind
  is not.
- **The video move still succeeds** if a sidecar fails. Failures are counted
  and listed rather than aborting the batch.
- **Dry run shows sidecars too** — `organize apply` without `--commit` reports
  how many will move, so there are no surprises at commit time.
- **Cross-filesystem moves** fall back to copy-then-delete, writing to a
  temporary name first so an interrupted copy cannot leave a truncated subtitle
  where Stash would pick it up.

After applying, run a Stash scan over the affected folders so the captions are
re-attached.

## Workflow E: Subs — match loose subtitles to scenes

`organize` keeps subtitles attached going forward. `subs` fixes the ones that
are *already* adrift: files that were never named after their video, or that got
left behind before sidecar support existed.

```sh
# What belongs where? Nothing is moved.
stash-janitor subs scan --dir /data/webm

# Machine-readable too.
stash-janitor subs scan --dir /data/webm --json report.json

# Place the confident ones.
stash-janitor subs apply --dir /data/webm --commit
```

### Where the subtitles are

`--dir` can point anywhere:

| Folder | What happens |
|---|---|
| On this machine (`~/Downloads/Subs`) | read locally, even if the library is remote |
| Inside the library (`/data/webm`) | translated through `sidecars.path_map` |
| Elsewhere on the library host | used as-is, and it says so |

There is no flag for this — a directory that exists on this machine is read
locally, otherwise it is treated as a path on the library backend.

**Subtitles and library can be on different machines.** `subs apply` streams
each file across and removes the original, so a folder on your desktop can feed
a library on a NAS in one command. The transfer lands on a temporary name
first: a half-written subtitle at the real path would be indexed by Stash as a
valid caption.

### How matching works

Filenames alone are not enough. These are real pairs:

```
StudioHandleXXX_Mommy-and-Son-Nesting-Season+(transcribed+on+25-Jul-2024).srt
  → 2023-05-23_Some.Performer-stepMommy.and.stepSon.Nesting.Season_1080p.mp4

thehousenextdoor2024 - The Reluctant Dog Sitter - Compressed.srt
  → The-Reluctant-Pet-Sitter-Part-1.mp4
```

Three independent signals are combined:

| Signal | Weight | Notes |
|---|---|---|
| Studio / JAV code | +60 | `VENU-765`, `RCTD-590` — near-decisive when present |
| Exact filename | +100 | The subtitle is already named after the video |
| Name similarity | +0…70 | Scene **title** and **filename** scored separately, better wins |
| **Runtime** | +0…45, −30 | The subtitle's last cue vs. the scene duration |
| Matching date | +25 | `YYYY-MM-DD` in the filename vs. the scene date |
| Creator agreement | +20 | Narrows the field; never decides on its own |

**Title and filename are compared independently.** Libraries carry the
identifying information in one place or the other: in a measured 61k-scene
library 52% of scenes had no title at all and relied entirely on a structured
filename, while a Stash-Box tagged library is the reverse — good titles beside
filenames like `1080p.wmv`. Scoring a merged blob served neither. The report
tells you which won: `title 100%` or `filename 57%`.

**Creator names come from your own library.** Performer and studio names plus
their aliases are fetched from Stash and squashed into handles, which are split
out of name similarity so two unrelated clips by one creator do not look alike.
Nothing is hard-coded, so it stays correct as your library grows.

**Folder names are a hint.** If subtitles are filed under a performer or studio
folder, that names the creator even when the filename is a bare title. Folders
contribute creator evidence only, never title evidence — a directory says who,
not what.

**Runtime is what makes this trustworthy.** Two scenes can share a title, a
performer and a studio, but not a length. In practice a correct match lands
within a couple of seconds while a same-titled decoy sits minutes away:

```
Nurse+River+Soothes+the+Aching+Patient+WM+(transcribed…).srt   10:52
  → #55739  Nurse River Soothes the Aching Patient   10:54   +2s     ✓
    #75175  Nurse River Soothes the Aching Patient   33:49   +1377s  ✗
```

Names were a dead tie; runtime was not.

A subtitle that runs *past* the end of a video counts as evidence against the
pairing, not for it — though a few seconds of overrun is tolerated, since the
measurement takes the maximum timestamp, which includes cue *end* times.

### Verdicts

| Verdict | Meaning | `subs apply` |
|---|---|---|
| `CONFIRMED` | Name and runtime agree, no rival close | moved |
| `LIKELY` | Good evidence, weaker on one axis | moved only with `--include-likely` |
| `AMBIGUOUS` | Several candidates too close to separate | left alone |
| `UNMATCHED` | Nothing credible in the library | left alone |

The gap to the runner-up matters as much as the absolute score: a high score
with a near-tie is exactly the case where an automatic choice would be wrong,
so it is reported as `AMBIGUOUS` for you to settle.

Subtitles that are *already* correctly named next to their video are counted as
"already attached" and skipped — they need no work.

### Language detection

`subs apply` names each file `<video-stem>.<lang>.srt`, and the language is
detected from the subtitle's **own text**, not its filename — filenames are
frequently wrong or silent about it. Script detection handles CJK, Hangul and
Cyrillic; the Latin-script languages are separated by function-word frequency.

If the language cannot be determined the subtitle is **left alone** rather than
guessed at, because a wrong caption tag is worse than none.

### What it will not do

- **Stash only reads `srt` and `vtt` captions.** Other formats in
  `sidecars.extensions` (`ass`, `ssa`, `sub`, `idx`, `smi`) are still moved so
  they travel with their video, but Stash will not display them. Verified on a
  live library: 1,019 registered captions, every one `srt`, and a correctly
  placed `.ass` file ignored.
- **A subtitle whose language cannot be detected is left alone**, not guessed
  at — a wrong caption tag is worse than none.
- It never overwrites an existing file.
- It never deletes anything. Two subtitles resolving to the same destination —
  duplicate downloads of one transcript — are resolved by keeping the larger
  (fuller) one; the other is reported and left where it is.
- `subs scan` is entirely read-only.

## Safety features

- **Filename-info-loss safety net.** When the scorer picks a higher-quality
  keeper but a loser has a date-bearing structured filename that the keeper
  doesn't, the group is flagged `needs_review` instead of auto-decided.
  This prevents silently losing information encoded only in filenames.
  The check uses a permissive "contains a YYYY-MM-DD style date" test —
  not the strict convention regex — so it catches non-standard filename
  formats too.

- **Rename-on-merge.** Even when the keeper file is the right quality
  choice, it might have a junk filename. The merge pipeline renames it
  to use the loser's structured basename (with the resolution token
  swapped) before deleting the losers. Configurable via
  `merge.post_merge_file_cleanup.rename_winner_filename` (default: on).

- **Filename-to-metadata extraction.** During merge, if neither keeper nor
  losers have a title or date set in Stash but a loser file matches the
  `YYYY-MM-DD_Performer-Title_Resolution.ext` convention, the date and
  title are parsed from the filename and set on the keeper before merging.

## Interactive review (TUI)

For a faster review experience than the CLI report + mark workflow:

```sh
stash-janitor review                          # shows decided + needs_review groups
stash-janitor review --filter all             # shows everything
stash-janitor review --filter needs-review    # focus on the hard cases
```

### Full review-to-delete walkthrough

This is the complete end-to-end flow including the TUI:

```sh
# 1. Scan to find duplicates
stash-janitor scenes scan

# 2. Check what you have
stash-janitor scenes status
#    decided: N       ← scorer picked a winner, ready to merge/tag
#    needs_review: M  ← scorer couldn't decide, you must pick

# 3. Auto-decided groups can be applied immediately (no review needed)
stash-janitor scenes apply --action merge         # preview
stash-janitor scenes apply --action merge --commit # execute

# 4. Review the needs_review groups in the TUI
stash-janitor review --filter needs-review

# 5. In the TUI: for each group, press 'o', pick the scene to keep,
#    press Enter. The status bar updates live.

# 6. Quit the TUI (q). It shows next steps.

# 7. Re-scan to convert your decisions into "decided" groups
stash-janitor scenes scan

# 8. Now merge the newly-decided groups
stash-janitor scenes apply --action merge --commit
```

### TUI navigation

**List mode** — browse all groups at a glance:
- `j`/`k` or `↑`/`↓` to navigate
- `PgUp`/`PgDn` to jump 10 groups
- `Enter` to drill into detail
- `q` to quit (shows next steps if you made changes)

**Detail mode** — one group at a time with full context:
- Color-coded `KEEP` / `drop` / `?` roles for each scene
- Resolution, codec, file size, flags (stashID, organized, tags)
- Per-loser `↳ kept by: <reason>` explanation from the scorer
- Full file paths

### TUI actions

Actions differ depending on the group's status:

**For `needs_review` groups** (the scorer couldn't auto-decide):

| Key | Action |
|-----|--------|
| `o` | **Pick keeper** — arrow-select which scene to keep, Enter to confirm. This is the main action you want. |
| `n` | Mark as not_duplicate (these scenes aren't actually the same content) |
| `d` | Dismiss (skip this group forever) |
| `a` | Shows a warning — you must use `o` first to pick a keeper |
| `↓` | Skip to next group without deciding |
| `Esc` | Back to list |

**For `decided` groups** (the scorer already picked a winner):

| Key | Action |
|-----|--------|
| `a` | Accept the scorer's pick (confirms it as a permanent override) |
| `o` | Change the keeper (if you disagree with the scorer's pick) |
| `n` | Mark as not_duplicate |
| `d` | Dismiss |
| `↓` | Next group |
| `Esc` | Back to list |

### How the TUI connects to apply

The TUI only **saves decisions locally** — it never deletes files or
touches Stash. The actual deletion happens when you run `apply`:

```
TUI decisions → saved to stash-janitor.sqlite
                      ↓
         stash-janitor scenes scan  (re-reads decisions, marks groups decided)
                      ↓
         stash-janitor scenes apply --action merge --commit  (executes)
```

The **status bar** updates live as you make decisions, so you can see
progress: `decided:5 review:3 dismissed:2`.

When you **quit**, the TUI prints:
```
Saved 10 decisions. Next steps:
  1. stash-janitor scenes scan
  2. stash-janitor scenes status
  3. stash-janitor scenes apply --action merge --commit
```

## Tips

- **Run `stash-janitor stats` first** to see your library size, metadata coverage,
  and the state of each workflow's local cache.
- **Start with `--action tag`** until you trust the scoring. Tags are
  reversible; merge/delete are not.
- **Use `--distance 4`** (default). Distance 8 catches more but produces
  false positives. Distance 0 is ultra-safe but misses re-encodes.
- **Pipe reports through `less -S`** to avoid line wrapping on narrow
  terminals.
- **The `--json` flag** on every report/status command makes it easy to
  pipe into `jq` or a script.
- **Re-scan after changes.** If you delete scenes in Stash's UI, the
  local cache becomes stale. `stash-janitor stats` will warn; just re-scan.
- **`stash-janitor.sqlite` is portable.** Copy it to another machine to browse
  reports offline.
- **`stash-janitor scenes mark --group N`** is faster than copying signatures. Use
  the `#N` from the report output directly.
