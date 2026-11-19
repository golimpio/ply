# plex-migrate

Move a Plex library into ply: the **posters you chose in Plex**, your **watch state and resume
points**, and correct **title matches** — without re-fixing anything by hand. The media folders stay
exactly as Plex organised them; only metadata/artwork/watch-state move.

It is a standalone, cross-platform Go tool (darwin/linux/windows × amd64/arm64, `CGO_ENABLED=0`).
Build it with `make build-migrate` (→ `bin/plex-migrate`) or `go build ./tools/plex-migrate`.

## How it works

1. **Join by exact file path.** Plex stores each file's absolute path; ply indexes the same files. The
   tool reads `ply.db` (read-only) and matches Plex files to ply items by path — no fuzzy guessing. If
   ply sees the files at a different mount prefix, map it with `-path-map`.
2. **Artwork becomes sidecars.** Plex's *selected* poster/backdrop is written next to the media
   (`<base>-poster.jpg`, `poster.jpg`, `season01-poster.jpg`, `<base>-thumb.jpg`, …). ply serves local
   art over TMDB and **pins it first-wins**, so a later metadata refresh never replaces your migrated
   poster. See `docs/M6-METADATA.md` and `docs/PLEX-MIGRATION.md`.
3. **Watch state** (resume position + watched flag) goes to ply via its API.
4. **Titles match by id.** Plex's IMDb/TMDB ids drive a manual (pinned) match in ply, which then fetches
   fresh TMDB text. Plot/cast/genres are *not* copied — TMDB provides current ones.

**Plex is only ever read.** DB mode opens a scratch *copy* of the database; API mode is read-only. The
only writes are sidecar files next to your media and ply API calls.

## Two ways to read Plex

| Mode | Flag | Needs | Artwork |
|---|---|---|---|
| **API** (preferred) | `-plex-url http://host:32400 -plex-token …` | a running Plex server + token | exact selected image |
| **DB** | `-plex-data "/path/to/Plex Media Server"` | the Plex data folder (DB + `Metadata` bundles) | uploaded poster preferred, else a bundle candidate (approximate when several downloaded candidates exist) |

Get a token: Plex Web → any item → ⋯ → Get Info → View XML → the URL has `X-Plex-Token=…`. Pass it via
`PLEX_TOKEN` env (preferred) or `-plex-token`. It is never logged or committed.

## Usage

```sh
# Dry run (default): print the plan, change nothing.
bin/plex-migrate -plex-data "/var/lib/plexmediaserver/Library/Application Support/Plex Media Server" \
  -ply-data ~/.local/share/ply

# API mode, dry run, with a mount-prefix mapping (Plex /volume1/Movies → ply /mnt/Movies):
PLEX_TOKEN=… bin/plex-migrate -plex-url http://nas:32400 \
  -ply-data /var/lib/ply -path-map /volume1/Movies=/mnt/Movies

# Apply (needs a running ply for matching/watch writes):
PLEX_TOKEN=… PLY_TOKEN=… bin/plex-migrate -plex-url http://nas:32400 \
  -ply-data /var/lib/ply -ply-url http://nas:8420 -path-map /volume1/Movies=/mnt/Movies -apply
```

### Flags

| Flag | Meaning |
|---|---|
| `-plex-data DIR` | Plex data directory (DB mode). Mutually exclusive with `-plex-url`. |
| `-plex-url URL` | Plex server root (API mode). |
| `-plex-token T` | Plex `X-Plex-Token` (or env `PLEX_TOKEN`). |
| `-account N` | Plex account id whose watch state to read (DB mode; default 1 = admin). |
| `-ply-data DIR` | **Required.** ply data directory; reads `ply.db` for the path join. |
| `-ply-url URL` | ply server root, for matching/watch writes (needed for `-apply`). |
| `-ply-token T` | ply API token (or env `PLY_TOKEN`). |
| `-path-map old=new` | Rewrite Plex paths to ply paths (repeatable; longest prefix wins). |
| `-apply` | Actually write. **Without it, the tool only prints a dry-run plan.** |
| `-force` | Overwrite existing sidecars (default: leave them). |

## Prerequisites

- ply has **added the same media folders and scanned them** (the join needs `items.path`). Add the
  roots in ply Settings → Folders (or `-movies`/`-shows`), then scan.
- A **TMDB key** is set in ply (for metadata text after matching). A TVDB key helps shows.
- For `-apply`, **ply is running** and reachable at `-ply-url` (matching/watch go through its API).
- The tool can **write to the media folders** (to place sidecars) and **read `ply.db`**.

## What migrates / what doesn't

- **Migrates:** selected posters & backdrops (films, shows, seasons), episode stills, resume points,
  watched flags, title matches by IMDb/TMDB id.
- **Not yet:** play counts > 1 and historical `last_viewed_at` (the ply API sets position + watched;
  full fidelity needs a direct-store mode), user ratings (ply has no user-rating table), custom
  collections, playlists, music/photos. See `docs/PLEX-MIGRATION.md` §8 and §13.
- A Plex library that already used **Local Media Assets** (sidecars next to the files) needs no art
  import — ply reads the same files; the tool skips existing sidecars.

## Folder/naming compatibility

ply indexes standard Plex naming as-is: `Movie (Year)/Movie (Year).ext`,
`Show/Season 01/Show - S01E01 - Title.ext`, `1x02`, `E02`, `Episode 2`, `Specials`/`Season 00`.
Three advanced forms are known ply parser gaps (the path join still works regardless): multi-episode
`S01E01-E02`, date-based `Show - 2020-01-01`, and anime absolute `Show - 01`. If your library uses
those, close the gap in `internal/library/nameparse.go` (see `docs/M6-METADATA.md` "Later").

## Safety

- Read-only on Plex; DB mode copies the database first.
- Dry-run by default; `-apply` to write; `-force` to overwrite sidecars.
- Sidecars written via temp file + rename (never a half-written poster).
- Idempotent: re-running skips existing sidecars and re-asserts matches/watch state.
- Secrets via env, never logged or committed.

## Layout

- `plex/` — read Plex (API + DB sources), models, guid parsing. Table-tested with fixtures.
- `migrate/` — the path join, plan/dry-run, sidecar naming (reuses `internal/metadata/localart.go`),
  apply. Tested against a fake Plex source and a fake ply API.
- `main.go` — flags, wiring, report.

Design and the full mapping reference: [`docs/PLEX-MIGRATION.md`](../../docs/PLEX-MIGRATION.md).