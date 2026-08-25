# Security Policy

## Credentials

Custodian handles credentials in two places:

- **Stash API key** — authenticates against your Stash instance
- **SSH credentials** — if `sidecars.ssh` is configured (moving subtitle
  files when Stash and custodian do not share a filesystem), a private key
  file, key passphrase and/or password

### Best practices

- Set the Stash API key via the environment variable named in `stash.api_key_env`
  instead of putting it directly in `config.yaml`. Config files on disk are
  readable by any process running as your user.
- Never commit `config.yaml` to version control.
- SSH passphrases and passwords are read from environment variables
  (`sidecars.ssh.passphrase_env` / `password_env`), never stored in config.
- Custodian rejects a changed host key on an already-known SSH host
  regardless of `accept_new_host_key`; that setting only affects first
  contact with an unrecognized host.

## Network

Custodian makes outbound requests to:

- Your Stash instance (GraphQL, default `localhost:9999`)
- The SSH host in `sidecars.ssh`, if configured

No data is sent to any analytics service, or any other third party.
stash-box lookups (the orphan-linking workflow) are proxied through your
Stash instance's own configured stash-box endpoints; custodian itself never
holds a stash-box API key.

## Untrusted input and mutation safety

Custodian mutates a Stash library: it merges duplicate scenes, deletes files,
and moves/renames files (including subtitle sidecars) via a local or SSH
filesystem backend. Unlike a scraper, it does not fetch attacker-controlled
web content — the data it acts on is Stash's own GraphQL responses (scene
metadata, file paths, perceptual hashes). Two things keep a bad match or a
malformed record from turning into data loss:

- **Every destructive action is dry-run by default.** `apply` requires both
  `--commit` and an interactive `YES` confirmation; `--yes` bypasses the
  prompt only for scripted use.
- **Decisions are persisted to a local SQLite cache** before anything is
  applied, so a review pass can inspect exactly what will change before it
  happens.

`sidecars.path_map` translates paths Stash reports into paths custodian can
reach; get it wrong and moves will fail loudly (unreachable path) rather than
writing to the wrong location silently, since the backend is only ever handed
a path it was explicitly told is reachable.

## Reporting a vulnerability

If you find a security issue, please open a GitHub issue or email the
maintainer directly. There is no bug bounty program.
