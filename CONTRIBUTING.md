# Contributing

## Cutting a release

Releases are tagged `vMAJOR.MINOR.PATCH`. Pushing the tag runs
[`.github/workflows/release.yml`](.github/workflows/release.yml), which builds
`custodian` for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and
windows/amd64, packages `.deb`/`.rpm` for linux/amd64+arm64, publishes them
with a `SHA256SUMS` file and a build-provenance attestation, publishes to the
AUR and to the `anastylosis/homebrew-tap` Homebrew tap, opens a linked
Discussion, and writes the release notes from the commit log.

```bash
git tag -a v0.4.0 -m "v0.4.0"
git push origin v0.4.0
```

The tag's annotation is rendered as a callout above the commit list — use it
for what the commits cannot say on their own:

```bash
git tag -a v0.4.1 -m "fixes the union preview dropping stash_ids

Re-run `custodian scan` after upgrading; groups found by v0.4.0 are still
valid."
```

### What the release produces

| Artifact | Platforms |
|---|---|
| `.tar.gz` binaries | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 |
| `.zip` binary | windows/amd64 |
| `.deb` / `.rpm` | linux/amd64, linux/arm64 |
| AUR `custodian` package | auto-published after release |
| Homebrew (`anastylosis/homebrew-tap`) | macOS + Linux, arm64 + amd64 |

No Dockerfile, so there is no image.

### AUR and Homebrew

Packaging files live in `packaging/`:

- `packaging/aur/PKGBUILD` — published to the AUR automatically after the
  release (via `KSXGitHub/github-actions-deploy-aur`). Needs
  `AUR_SSH_PRIVATE_KEY`. `packaging/aur/gen-srcinfo.sh --check` runs in CI to
  catch `.SRCINFO` drift.
- `packaging/homebrew/custodian.rb` — a template the release workflow renders
  into [`anastylosis/homebrew-tap`](https://github.com/Anastylosis/homebrew-tap)
  as `Formula/custodian.rb`, substituting the version and checksums. Needs
  `HOMEBREW_TAP_SSH_KEY`, a write deploy key on the tap repo.

`go install github.com/Anastylosis/Custodian/cmd/custodian@vX.Y.Z` still works
as a source-based alternative.

### There is no approval gate, and that is deliberate

The sibling repos pause their release job for a human to attest that they ran
the tests CI cannot. Custodian does not, because CI *can* run them.

Custodian **mutates** a Stash library — it merges scenes, resolves duplicates,
moves files. That is exactly the kind of risk a gate is normally asked to
cover, and a gate would cover it badly: nothing verifies the attestation, it
is asked for once per release, and the person approving is the person who
wrote the change.

Instead, every push runs the `Stash integration` job in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml). It boots a throwaway
`stashapp/stash` container, generates a small library with ffmpeg, scans and
phashes it, creates a performer and a studio, and runs
`go test -count=1 ./internal/mirror/... ./internal/evidence/...
./internal/reclaim/... ./internal/subs/...` against the result with
`SFX_INT_STASH_SEEDED=1` — which makes those tests **assert** the fixtures
rather than skip. `internal/reclaim`'s share of that actually moves and
deletes files in the fixture library, so the job mounts it writable and the
test additionally requires `SFX_INT_STASH_WRITABLE=1`. A regression fails the
commit that introduced it, not a release three weeks later.

So the release checklist is short: the tag is on a commit whose CI is green.

## Tests

```sh
go test ./...                                    # unit tests, no Stash needed
go build ./...
golangci-lint run
```

`go test ./...` must pass with no Stash and no SSH host reachable: everything
that talks to a server is env-gated and skips itself. The `ci` job runs it
under `offline-isolation`, so a unit test that quietly reaches the network
fails there.

### Stash integration

```sh
SFX_INT_STASH_URL=http://your-stash:9999 \
  go test -count=1 ./internal/mirror/... ./internal/evidence/... \
    ./internal/subs/... -run Integration -v
```

Those three are read-only: they query and never write. `internal/reclaim`'s
suite is not, which is why it is not in the line above — it moves files into
the quarantine directory and deletes them, and it refuses to run without
`SFX_INT_STASH_WRITABLE=1` on top of the URL, so pointing a bare
`SFX_INT_STASH_URL` at a real library can never trigger it. It also reads
`SFX_INT_STASH_LIBRARY_DIR`, the host path it stats to confirm a quarantined
file is physically gone rather than only gone from Stash's bookkeeping.

Unset `SFX_INT_STASH_URL` and every `TestIntegration*` function skips, so
`go test ./...` stays hermetic.

`SFX_INT_STASH_SEEDED=1` is a second, separate switch, and it changes what a
missing fixture means:

| | `SFX_INT_STASH_URL` unset | set | set, plus `SFX_INT_STASH_SEEDED=1` |
|---|---|---|---|
| Nothing found | skipped | skipped | **failed** |

Set it only when the library really is the harness fixture set — three scenes,
one of them holding two byte-identical files, one phash-duplicate pair at
distance 0 sharing a fabricated stash_id, one performer and one studio.
Pointed at your own library it will report things that are not wrong with it.
CI sets it; you generally should not.

`internal/reclaim`'s suite consumes those fixtures: it deletes files. Against
a local container, regenerate the fixture library between runs — CI gets a
fresh container each time and does not have to.

It exists because a suite that skips when it finds nothing passes just as
happily against an *empty* Stash, which is a CI job that cannot go red. The
acceptance test for that is to point the tests at an empty Stash with the flag
set and watch them fail.

### SSH / SFTP

The sidecar file operations have their own live suite, gated the same way:

```sh
SJ_INT_SSH_HOST=nas.local \
SJ_INT_SSH_USER=me \
SJ_INT_SSH_PASSWORD_ENV=MY_PW \
  go test ./internal/vfs/... -run Integration -v
```

It writes only inside a freshly created temporary directory under
`SJ_INT_SSH_DIR` (default `/tmp`) and removes it afterwards. `SJ_INT_SSH_PORT`
overrides the default 22. There is no harness for this one — CI has no SSH
host — so it runs when someone runs it.
