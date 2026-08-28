# Next steps — Custodian rebuild

State on 2026-08-28: every REBUILD.md phase is delivered on branch `rebuild`
(24 commits, unpushed). stash-go v0.10.0 released; one commit past it
(`ScrapeSceneURL`). `.github` pushed with the stash_id fixture and the
`library-writable` input.

## Done by the user (2026-08-28)

- [x] `.github` pushed (`66fb8c2`).
- [x] voydb — parked; `identify --via stash-box` stays StashDB-only for now.
- [ ] Phashes for the 1,401 files: the user runs Stash's own scan/generate.
      Do **not** use `custodian doctor --generate-phashes` yet.

## Yours, in order

1. ~~Move `.github`'s `v1` tag~~ — done, points at `66fb8c2`.
2. ~~Push `rebuild`~~ — done; CI green on all 11 jobs (run 33183367600),
   after `20f77fd` fixed Windows (Stash paths joined with `filepath`). Note:
   ci.yml runs on pushes to master and on PRs — a branch push needs
   `gh workflow run ci.yml --ref rebuild` or a draft PR.
3. After the Stash scan/generate: `custodian sync` then `custodian scan` —
   phash evidence for `/data/tmp4` only appears once those files have phashes.
4. **Inspect** the 16 foreign files already in `/data/_quarantine` (13.9 GiB,
   not put there by Custodian) and the 755 filing conflicts (`custodian file`
   dry run) before any `file --commit`.
5. Release prerequisites (maintainer-only): add Custodian to
   `HOMEBREW_TAP_SSH_KEY`'s selected repositories in infra; confirm the AUR
   key's scope includes Custodian; enable Discussions + an `Announcements`
   category on the repo. Then merge `rebuild` as the CLI-breaking `v1.0.0`
   and watch the homebrew/aur jobs on the first tag.

## Code follow-ups

- Switch `internal/identify` to `stash.ScrapeSceneURL` at stash-go v0.11.0
  and delete its `Execute` copy.
- `internal/cli` coverage is 52.8% (cobra wiring); every pure function is
  covered. Raise only if the org's codecov target demands it.
- The local harness runner (`harness/run.sh` in the session scratchpad)
  belongs in `.github/scripts/stash-harness-local.sh` if wanted.
- Quarantine basename collisions are detected per run only; close with
  `findFiles` once stash-go wraps it.

## First real run, when ready

```
custodian sync            # incremental
custodian scan            # all generators; phash needs stash.url
custodian canon --source stash-box
custodian rejudge
custodian plan
custodian apply           # dry run — read it
custodian apply --commit  # verified backup first, losers to /data/_quarantine
custodian status
# ... 30 days, review in Stash by tag _custodian_quarantine ...
custodian prune --commit
```
