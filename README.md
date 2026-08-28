# Custodian

[![CI](https://github.com/Anastylosis/Custodian/actions/workflows/ci.yml/badge.svg)](https://github.com/Anastylosis/Custodian/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/Anastylosis/Custodian/branch/master/graph/badge.svg)](https://codecov.io/gh/Anastylosis/Custodian)
[![Go Report Card](https://goreportcard.com/badge/github.com/Anastylosis/Custodian)](https://goreportcard.com/report/github.com/Anastylosis/Custodian)

*(formerly StashJanitor)*

Custodian removes bloat from a [Stash](https://github.com/stashapp/stash)
library: every scene exists once, as the best copy you have, and nothing on
disk is there twice or larger than it needs to be. For every duplicate it
finds, the higher-quality copy survives and the losers' metadata is merged
into it first, so richness never has to win a quality fight.

It works off a local ledger — a mirror of Stash's scenes and files — so every
step after `sync` is fast and repeatable. Nothing is deleted on the way: a
loser is moved to a quarantine directory inside the library and tagged, and
only `prune` removes it, weeks later, after a verified database backup.

## Status

```
scenes:            61217 (0 gone)
files:             61217
scenes >1 file:    0
with stash_id:     28946
without stash_id:  32271
files no phash:    1401
bytes on disk:     67.13 TiB
duplicate groups:  1309 (certain 656, conflicted 611, dismissed 0, partial 0, mistag 0, probable 25, rejected 17)
partial:           0 (0 B)
mistag:            0
probable:          25 (19.56 GiB) — review only
rejected:          17 (33.65 GiB)
multifile:         0 scenes
reclaimable:       1.31 TiB
quarantined:       0 groups (0 B)
reclaimed:         0 groups (0 B)
last sync:         2026-08-28T08:50:29Z (incremental), 0 changed
last scan:         2026-08-28T10:44:58Z, certain 656, conflicted 611, dismissed 0, probable 25, rejected 17
```

That is `custodian status` against a real 61,217-scene library, and it is the
point of the tool: one number per line for where the library stands, what is
safe to reclaim, and what is waiting on you.

## The chain

```
sync → scan → canon → rejudge → plan → apply → prune
```

| Command | What it does |
|---|---|
| `sync` | mirror Stash's scenes and files into the local ledger |
| `scan` | find duplicate-candidate groups from the ledger's signals |
| `canon` | fetch canonical runtimes from a stash-box or an FSS catalogue |
| `rejudge` | re-classify groups against those runtimes |
| `plan` | choose the keeper in every group |
| `apply` | union metadata onto the keeper, quarantine the losers |
| `prune` | delete quarantined losers once they are old enough |
| `undo` | reverse an apply from the mutation log |
| `file` | move and rename files to match the organize template |
| `captions` | match loose subtitles to scenes and move them into place |
| `groups` | list duplicate-candidate groups |
| `log` | print the mutation log |
| `status` | print where the library stands |
| `config` | write, locate, print and check the config file |
| `doctor` | check Stash, the ledger and access to the library |

`canon` and `rejudge` are optional; they exist to settle groups whose copies
disagree on runtime. `file` and `captions` are hygiene that rides along —
useful, but not what the tool is for.

## Requirements

- Go 1.25 or newer, if you build from source
- A reachable Stash instance running v0.31.x
- Disk for the ledger, proportional to library size — at 61k scenes it is
  about 65 MB

## Install

**Homebrew** (macOS and Linux)

```sh
brew install anastylosis/tap/custodian
```

**Linux packages**

```sh
# Arch Linux (AUR)
yay -S custodian

# Debian / Ubuntu
sudo dpkg -i custodian_*_amd64.deb

# Fedora / RHEL
sudo rpm -i custodian_*_amd64.rpm
```

`.deb`/`.rpm` are attached to each
[release](https://github.com/Anastylosis/Custodian/releases).

**Release binary** (no Go required)

Download the archive for your platform from
[Releases](https://github.com/Anastylosis/Custodian/releases) and extract:

```sh
# Linux (amd64)
tar xzf custodian-v*-linux-amd64.tar.gz
chmod +x custodian

# macOS (Apple Silicon)
tar xzf custodian-v*-darwin-arm64.tar.gz
chmod +x custodian

# Windows — extract the .zip, you get custodian.exe
```

**From source**

```sh
git clone https://github.com/Anastylosis/Custodian.git
cd Custodian
go build -o custodian ./cmd/custodian
```

**`go install`**

```sh
go install github.com/Anastylosis/Custodian/cmd/custodian@latest
```

The binary is fully self-contained (pure-Go SQLite, no cgo, no runtime deps).

## Quick start

```sh
# 1. Write a config file and point it at your Stash.
custodian config init
$EDITOR ~/.config/custodian/config.yaml   # set stash.url
export STASH_API_KEY=...                  # only if your Stash has one

# 2. Check that everything is reachable.
custodian doctor

# 3. Mirror the library. Minutes the first time, sub-second after that.
custodian sync

# 4. Find the duplicates and see where you stand.
custodian scan
custodian status

# 5. Choose a keeper in every certain group and read the reasons.
custodian plan | less

# 6. Quarantine the losers. Dry run first — this is the default.
custodian apply
custodian apply --commit

# 7. Weeks later, once you are satisfied nothing was wrongly quarantined.
custodian prune --commit
```

### What "dry run" means here

Every command that writes anything is a dry run unless you pass `--commit`.
A dry run computes the whole plan — including the exact mutations that would
be sent — and writes nothing to Stash, nothing to disk, and nothing to the
mutation log.

`--commit` then asks for an interactive `YES` before it proceeds. `--yes`
skips that prompt for scripted use, but `--commit` is still required
separately, so no single typo can start a destructive run.

### What quarantine means

`apply` never deletes. For each duplicate group it:

1. downloads and verifies a database backup, and refuses to continue without one;
2. unions the losers' metadata onto the keeper, so the keeper is complete;
3. tags each loser `_custodian_quarantine` and moves its file — with its
   subtitles — into `/data/_quarantine` inside the library.

The loser scene stays in Stash. You can find it by its tag, watch it, and
rescue it from Stash's own UI by removing the tag; `prune` skips anything
that was un-tagged. `prune`, run deliberately and only against quarantines
older than `--age` (30 days by default), is the one irreversible step, and it
runs on nothing that was not first logged. Before then, `custodian undo`
reverses an apply from the mutation log: tag off, files back, keeper's fields
restored.

## Identification is opt-in

A scene needs a `stash_id` because that is the signal that finds its twins,
not because an unidentified scene is a failure. Custodian never writes one on
its own: StashDB fingerprint matching has false positives, and a wrong
`stash_id` manufactures a duplicate group out of two unrelated scenes. Stash's
auto-tagger stays off unless you ask for it, per run and explicitly, and any
identification shows up in the plan before it happens. An orphan with no twin
is already done — half a real library has no stash-box entry to find, and
phash still works on all of it.

## Where to find help

- [MANUAL.md](MANUAL.md) — every command, the config reference, the safety
  model, and the Stash behaviours that cost a bug to discover
- [internal/config/default.yaml](internal/config/default.yaml) — every config
  field documented inline
- `custodian` with no arguments — the chain, in order
- `custodian <command> --help` — flags for one command

## License

Copyright (C) 2026 Wasylq

[GPL-3.0-only](LICENSE).
