# Plex → ply migration — implementation spec

**Audience:** the agent working on this. It is implemented as the standalone tool
`tools/plex-migrate/` (see its README); this doc remains the design and mapping reference. Read it
fully before changing anything; it assumes the pinned-artwork contract landed in commit `0d9a8dc`
(`docs/M6-METADATA.md`, "## Images"). Where a Plex detail is version-dependent, this doc says
*verify* — introspect the live server/DB rather than hardcoding an assumption.

**Goal:** move a person's Plex library into ply so that, after the cutover, ply shows the same
films and series with the **posters they chose in Plex**, their **watch state and resume points**,
and correct metadata — without them re-fixing anything by hand.

**Non-goals (first cut):** music, photos, live TV/DVR, Plex playlists, Plex "Extras"/trailers,
Plex labels/collections beyond what TMDB gives (see "What does not map"). These are later phases.

---

## 1. The three ideas that make this safe

1. **Join files by path, not by fuzzy title match.** Plex stores the absolute path of every media
   file (`media_parts.file`, or `Media[].Part[].file` in the API). ply indexes the same files
   (`items.path`, UNIQUE). The file→item link is exact; no guessing. Title matching is only needed
   to attach *metadata* (TMDB id), and Plex already carries the external ids.
2. **Artwork migrates as sidecars, and sidecars win.** Write Plex's *selected* poster/backdrop as a
   file next to the media (`poster.jpg`, `season01-poster.jpg`, …). ply serves local art over TMDB
   (`pickArt`) and **pins artwork first-wins**, so a later TMDB refresh never replaces the migrated
   picture. This is the whole reason the pinned-artwork change was a prerequisite.
3. **Let ply fetch the text fresh.** Do not copy Plex's plot/cast/genres verbatim. Match the title by
   its IMDb/TMDB/TVDB id (Plex `Guid`), let ply's metadata worker fetch current TMDB text, and only
   import what TMDB cannot give: the chosen artwork, watch state, resume points, play counts.

Consequence: a Plex library that already used **Local Media Assets** (sidecar `poster.jpg` etc.)
needs *no artwork import at all* — ply reads the same files. Detect this and skip art for those items.

---

## 2. Prerequisites

- ply has scanned the **same media folders** Plex uses (add them as roots, `POST /api/scan`, wait for
  `GET /api/scan` `running:false`). The migration joins on `items.path`, so the files must be indexed.
- A TMDB key is set in ply (`PATCH /api/settings {tmdbKey}` or `-tmdb-key`/`PLY_TMDB_KEY`), online
  lookups on, so matched titles get text. Without a key, artwork/watch-state still migrate; text stays
  filename/NFO-only.
- One of:
  - **API mode:** a running Plex Media Server + an `X-Plex-Token` (see §4). Preferred: version-stable,
    returns the *selected* art and external ids directly.
  - **DB mode:** a **copy** of Plex's SQLite DB + the `Metadata` bundle tree (offline; no server). Faster
    for huge libraries; more version-sensitive. **Never open the live DB read-write; copy it first.**
- Decide the write target: sidecars next to the media (default, survives rescans, human-readable) vs a
  staging dir. Default to sidecars; they are the source of truth ply already understands.

---

## 3. Plex data model (DB mode reference)

Data directory (verify on the actual host):

| Platform | Plex data dir |
|---|---|
| macOS | `~/Library/Application Support/Plex Media Server/` |
| Linux | `/var/lib/plexmediaserver/Library/Application Support/Plex Media Server/` |
| Windows | `%LOCALAPPDATA%\Plex Media Server\` |
| Docker | `/config` (mapped volume) |

- DB: `<data>/Plug-in Support/Databases/com.plexapp.plugins.library.db` (SQLite, WAL). **Copy it**
  (and `-wal`/`-shm` if present, or checkpoint) to a scratch path and open the copy read-only.
- Artwork bundles: `<data>/Metadata/Movies/…` and `<data>/Metadata/TV Shows/…`.
- Resized cache (ignore for migration): `<data>/Cache/PhotoTranscoder/`.

Key tables (confirm columns with `PRAGMA table_info(<table>)` on the copy — they drift between versions):

- `library_sections(id, name, agent, language, …)` — one row per library. Type/agent tells movie vs show.
- `section_locations(library_section_id, root_path)` — the folders a library scans. Map these to ply roots.
- `metadata_items(id, library_section_id, parent_id, "index", type, guid, hash, title, year,
  originally_available_at, summary, rating, audience_rating, thumb_url, art_url, user_thumb_url,
  user_art_url, user_fields, added_at, …)` — the core. `id` is the API `ratingKey`.
  - `type`: **1 = movie, 2 = show, 3 = season, 4 = episode** (8/9/10 = artist/album/track — out of scope).
    Collections/playlists have other type values that vary; **use the API for collections** (§4) rather
    than guessing a type number.
  - Hierarchy: show (2) ← season (3, `parent_id`=show, `index`=season number) ← episode (4,
    `parent_id`=season, `index`=episode number). Specials are season `index` 0.
  - `guid`: the agent id (see §5). `hash`: SHA-1 used for the bundle name — **read this column**, do not
    recompute from `guid` (the `?lang=` suffix and newline make recomputation error-prone).
  - `user_thumb_url` / `user_art_url`: the **selected** poster/backdrop (what the user picked). Empty →
    fall back to `thumb_url` / `art_url` (the agent's default).
  - `user_fields`: locked-field flags; a locked poster (custom pick) is recorded here. Useful to know an
    item has a deliberate custom poster, but `user_thumb_url` already gives the selection.
- `media_items(id, metadata_item_id, …)` → `media_parts(id, media_item_id, file, …)` — **`file` is the
  absolute media path**. Join `metadata_items.id → media_items.metadata_item_id → media_parts.file`.
  One metadata item can have several parts (multi-version); ply models versions as separate items of one
  title.
- `metadata_item_views(account_id, metadata_item_id, view_offset, view_count, last_viewed_at, rating, …)`
  — per-account watch state. `view_offset` = resume position in **milliseconds**; `view_count` = times
  watched to completion; `last_viewed_at` = unix seconds; `rating` = the user's rating (verify scale,
  usually 0–10). Pick the account being migrated (usually `account_id=1`, the admin).
- `guids(metadata_item_id, guid)` — **external ids for the new Plex agents** (`imdb://tt…`, `tmdb://603`,
  `tvdb://81189`). Legacy agents put the id in `metadata_items.guid` instead (§5).
- `tags(id, tag, tag_type, …)` + `taggings(metadata_item_id, tag_id, …)` — genres, collections, labels,
  countries, etc. `tag_type` distinguishes them (genre, collection, label…). Verify values on the copy.

Example joins (run on the copy):

```sql
-- movies with their file path and selected poster
SELECT mi.id, mi.title, mi.year, mi.guid, mi.user_thumb_url, mi.thumb_url, mp.file
FROM metadata_items mi
JOIN media_items m ON m.metadata_item_id = mi.id
JOIN media_parts mp ON mp.media_item_id = m.id
WHERE mi.type = 1;

-- episodes of a show, with season/episode numbers and file path
SELECT s.title AS show, se."index" AS season, e."index" AS episode, e.title, mp.file,
       e.user_thumb_url, e.thumb_url
FROM metadata_items s
JOIN metadata_items se ON se.parent_id = s.id AND se.type = 3
JOIN metadata_items e  ON e.parent_id = se.id AND e.type = 4
JOIN media_items m ON m.metadata_item_id = e.id
JOIN media_parts mp ON mp.media_item_id = m.id
WHERE s.type = 2 AND s.title = ?;

-- watch state for the admin account
SELECT metadata_item_id, view_offset, view_count, last_viewed_at, rating
FROM metadata_item_views WHERE account_id = 1;

-- external ids (new agents)
SELECT metadata_item_id, guid FROM guids WHERE metadata_item_id = ?;
```

### Bundle artwork layout (DB mode)

`metadata_items.hash` = e.g. `009788157bc680044f3e2b4242b71ebfc62391d`. The bundle is:

```
Metadata/Movies/0/09788157bc680044f3e2b4242b71ebfc62391d.bundle/
  Contents/_combined/posters/<agent>_<sha1>     ← downloaded candidates (no file extension)
  Contents/_combined/art/<agent>_<sha1>         ← backdrops
  Contents/_combined/clearLogos/…               ← logos
  Uploads/posters/<sha1>                        ← user-uploaded/selected custom posters
```

(TV shows/seasons/episodes live under `Metadata/TV Shows/` the same way. Older Plex used `_stored`
instead of `_combined`.) Files have **no extension**; sniff the bytes (JPEG/PNG) to pick one. The
*selected* image is named by `user_thumb_url`/`thumb_url` (a `/library/metadata/<id>/thumb/<ts>` URL or a
bundle path) — resolving it to a file is fiddly across versions. **Prefer API mode for artwork** (§4):
`GET /library/metadata/<id>/thumb?download=1` returns the selected bytes directly, no bundle archaeology.
Use bundles only when offline, and verify the selection mapping against a known item first.

---

## 4. Plex HTTP API (API mode reference)

Base `http://<plex-host>:32400`. Every call carries `X-Plex-Token: <token>` (header or `?X-Plex-Token=`).
Responses are XML by default; send `Accept: application/json` for JSON. Verify shapes against the live
server (Plex versions differ).

**Getting a token:** sign in to Plex Web, open any item → ⋯ → Get Info → View XML; the URL contains
`X-Plex-Token=…`. Or read it from the server's `Preferences.xml` (`PlexOnlineToken`) in the data dir.
Treat it as a secret: never log it, never commit it (repo rule).

Endpoints:

- `GET /library/sections` → libraries: `Directory[]` with `key` (section id), `type` (`movie`/`show`),
  `title`, `agent`, `Location[].path`. Map `Location.path` → ply roots.
- `GET /library/sections/{id}/all?type=1` (movies) or `type=2` (shows) → `Metadata[]`. Each has
  `ratingKey`, `guid`, **`Guid[]`** (`{id:"imdb://tt0088763"}`, `{id:"tmdb://105"}`, `{id:"tvdb://299"}`),
  `title`, `year`, `thumb`, `art`, `rating`, `audienceRating`, `viewCount`, `viewOffset` (ms),
  `lastViewedAt`, `addedAt`, and `Media[].Part[].file` (the absolute path).
  - The `Guid[]` array is the reliable source of external ids for the new agents; legacy libraries may
    only have the agent `guid` string (§5).
- Shows → seasons → episodes:
  `GET /library/metadata/{showKey}?includeChildren=1` (children = seasons), then
  `GET /library/metadata/{seasonKey}/children` (episodes). Or `GET /library/sections/{id}/all?type=4`
  for all episodes flat (each carries `parentRatingKey`=season, `grandparentRatingKey`=show,
  `parentIndex`=season, `index`=episode).
- **Selected artwork bytes:** `GET /library/metadata/{ratingKey}/thumb?download=1` (poster) and
  `/art?download=1` (backdrop). These return the image the user currently sees — exactly what we want.
  For a season poster use the season's `ratingKey`; for an episode still use the episode's
  `ratingKey` `/thumb`. Add `&width=…` only if you must; default is the original selection.
- Collections: `GET /library/collections` (or `/library/sections/{id}/collections`) → `Metadata[]`
  type collection with `ratingKey`, `title`, `thumb`, and children via `/library/metadata/{key}/children`.
- Playlists: `GET /playlists` (later phase).

Rate-limit politely (a few requests/sec); a big library is thousands of items. Cache responses to a
scratch dir so a re-run is cheap and idempotent.

---

## 5. External ids: Plex guid → ply match hint

ply matches by IMDb/TMDB/TVDB id (`docs/M6-METADATA.md`, "Identity hints"; `PUT /api/items/{id}/match`
takes `{tmdbId}` or `{imdbId}`, shows take `{tmdbId}`/`{imdbId}` too). Extract from Plex:

- **New agents** (`tv.plex.agents.movie`, `tv.plex.agents.series`, `plex://movie/<hex>`): the ids are in
  the `Guid[]` array / `guids` table: `imdb://tt0088763`, `tmdb://105`, `tvdb://299`. Parse the `provider://id`.
- **Legacy agents:** `metadata_items.guid` is a URL:
  - `com.plexapp.agents.imdb://tt0107302?lang=en` → IMDb `tt0107302`
  - `com.plexapp.agents.themoviedb://603?lang=en` → TMDB `603`
  - `com.plexapp.agents.tvdb://81189?lang=en` (or `thetvdb`) → TVDB `81189`
  Strip the `?lang=…` query. The agent may be `com.plexapp.agents.none` (unmatched) → no id; fall back to
  title+year.
- Prefer **TMDB id** when present (ply's primary source); else IMDb (ply resolves via TMDB `/find`); else
  TVDB (shows, when ply has a TVDB key). Episodes inherit the show's id; match the **show**, not each
  episode (`PUT /api/shows/{name}/match`).

Reference: Plex `Guid` array format and legacy agent guid strings are documented in the Plex developer
forum ("Custom Metadata Providers" announcement) and community mappers (sources at the end).

---

## 6. ply target reference (what to write)

Data dir: macOS `~/Library/Application Support/ply`, Linux `$XDG_DATA_HOME/ply` or `~/.local/share/ply`,
`-data` overrides (`docs/DESIGN.md`). DB `<data>/ply.db`. A migration tool may use `internal/store`
directly (it is in-process) or the HTTP API (remote server). Prefer the API for a running remote server;
use the store for a local, stopped server or for fields the API does not expose.

Relevant tables (migrations in `internal/store/migrations/`):

- `items` (`001_init.sql`): `id, kind('movie'|'episode'), title, year, show, season, episode,
  path(UNIQUE, raw OS bytes — never normalised), root, size, mtime(ns), duration_sec, added_at(s)`.
  **Join Plex `media_parts.file` to `items.path`.** Beware macOS NFC/NFD unicode normalisation of paths;
  compare with the same normalisation ply's scanner used (it stores raw bytes). If a path does not join,
  fall back to size+`fingerprint` (ply keeps a move fingerprint) or report it unmatched — do not guess.
- `progress` (`011_watch_state.sql`): `item_id, position_sec, duration_sec, watched_at(unix s, 0=not),
  play_count, updated_at(ns)`, with `CHECK ((watched_at=0)=(play_count=0))`. A file with no row is
  unstarted/unwatched; marking unwatched deletes the row.
- `titles` / `seasons` / `episodes` (`003_metadata.sql`, `020_season_art.sql`): metadata; `seasons.local_poster`
  is the migrated season sidecar path. Artwork refs are first-wins (pinned).
- `title_ratings` (`010_ratings.sql`): external ratings only (`imdb,tmdb,rt_critics,rt_audience,metacritic,
  letterboxd,trakt`), `value` on the source's scale, `scale`, `votes`, `state`. **There is no user-rating
  table** — see §8.
- `collections` + `title_collections` (`003`, `018_title_collections.sql`): `collections(id,tmdb_id,name,
  overview,poster,backdrop)`, junction `title_collections(title_id,collection_id,ord)`.

Sidecar names ply reads (`internal/metadata/localart.go`; case-insensitive; `.jpg/.jpeg/.png/.webp`):

| Art | Names (first match wins) |
|---|---|
| Movie poster | `<base>-poster`, then `poster`/`folder`/`cover` **only if the folder holds one video** |
| Movie backdrop | `<base>-fanart`, then `fanart`/`backdrop`/`background` (one-video folder) |
| Show poster | `poster`, `folder` (in the show folder) |
| Show backdrop | `fanart`, `backdrop` |
| Season poster | `seasonNN-poster` (`season01-poster`; `season-specials-poster` for 0), else `Season NN/poster` |
| Episode still | `<base>-thumb`, `<base>` |

`<base>` = the media filename without its extension. **Local beats TMDB**; folder-level names apply to a
movie only when its folder has one video. Write the specific `<base>-poster` form when a folder has
several versions, so each file can carry its own art; use `poster.jpg` for a one-film folder.

API hooks (`docs/DESIGN.md` API table; auth `Authorization: Bearer <token>` or `?token=`):

- `POST /api/scan` / `GET /api/scan` — index the media first.
- `PUT /api/items/{id}/match {tmdbId|imdbId}` — movie manual match (pins; `match_state=manual`).
- `PUT /api/shows/{name}/match {tmdbId|imdbId}` — whole-show manual match.
- `PUT /api/items/{id}/progress {positionSec,durationSec}` — resume; marks watched at ≥90%.
- `PUT /api/items/{id}/watched {watched:true|false}` — watch flag (film: all versions).
- `PUT /api/titles/{id}/watched`, `PUT /api/shows/{name}/watched`, `PUT /api/shows/{name}/seasons/{n}/watched`.
- `POST /api/items/{id}/metadata/refresh`, `POST /api/metadata/refresh` — re-fetch text.
- `GET/PATCH /api/settings {tmdbKey,…}` — ensure a key.
- `GET /api/items`, `/api/shows`, `/api/titles`, `/api/collections` — read back for verification.

**Fidelity note:** the progress API sets position and a watched flag, but not an arbitrary `play_count`
or a historical `watched_at`. To import Plex `view_count` (>1) and `last_viewed_at` faithfully, write the
`progress` row through `internal/store` (a migration tool can), respecting the CHECK constraint
(`watched_at<>0` ⇔ `play_count<>0`). If using the API only, set position + watched and accept play_count=1.

---

## 7. Procedure (phased)

Implemented as the standalone `tools/plex-migrate` command (`make build-migrate`). Flags:
`-plex-url`/`-plex-token` (API mode) **or** `-plex-data` (DB mode), `-ply-data` (the path join),
`-ply-url`/`-ply-token` (matching/watch writes), `-path-map old=new`, `-account`, `-force`.
**Dry-run by default**; `-apply` writes. It logs a plan and never touches Plex data read-write.

**Phase 0 — prepare.** Verify ply scanned the roots (join coverage report: how many Plex files found a
ply item). Ensure TMDB key. Copy the Plex DB if DB mode. Build a path→ply-item index from `GET /api/items`
(or `store.ListItems`).

**Phase 1 — match titles.** For each Plex movie: extract external id (§5), `PUT /api/items/{id}/match`.
For each show: match once via `PUT /api/shows/{name}/match`. Record unmatched (no id / title+year failed)
for a review list; do not block the rest. Matching pins the title; the worker fetches TMDB text.

**Phase 2 — artwork.** For each item with a Plex selection:
1. Skip if a sidecar already exists (unless `-force`) — the person may have curated ply art already.
2. Skip if Plex used Local Media Assets and the sidecar is already next to the file (detect: the file
   exists and Plex `thumb` points at a local path) — nothing to copy.
3. Fetch the selected bytes: API `GET /library/metadata/{ratingKey}/thumb?download=1` (poster),
   `/art?download=1` (backdrop); DB mode: resolve `user_thumb_url`→bundle file, sniff type.
4. Write next to the media with the right name (§6): movie `<base>-poster.jpg` (or `poster.jpg` in a
   one-film folder), show `poster.jpg` in the show folder, season `seasonNN-poster.jpg` in the show
   folder, episode `<base>-thumb.jpg`. Backdrop → `<base>-fanart.jpg` / `fanart.jpg`.
5. Trigger ply to notice: the watcher's `OnSidecarsChanged` re-reads art without a re-match; or
   `POST /api/scan`. Because art is first-wins and local beats TMDB, the migrated poster now shows and
   stays.
6. If Plex has no poster (DMCA-removed, or never matched), write nothing — ply falls back to TMDB then
   frame-grab. Report these.

**Phase 3 — watch state.** For each item with `metadata_item_views` (or API `viewOffset`/`viewCount`):
- `position_sec = view_offset/1000`; `duration_sec` from ply's probe (or Plex `Media.duration`).
- watched = `view_count > 0` (or offset ≥ 90% duration). `watched_at = last_viewed_at` (unix s),
  `play_count = max(view_count, 1 if watched else 0)`.
- Write via store for full fidelity, or `PUT …/progress` + `PUT …/watched` via API (play_count=1).
- Respect the CHECK: an unwatched item has `watched_at=0, play_count=0` (or no row).

**Phase 4 — collections (optional).** Read Plex collections (API `/library/collections`), create ply
collections by name/TMDB id and link titles (`title_collections`). Prefer TMDB's own collection when the
film has one; only import a Plex collection that TMDB lacks (custom/franchise). Custom collection poster
→ sidecar or pinned ref.

**Phase 5 — verify & report.** Counts per phase (matched, art written, skipped, watch rows, unmatched),
a positive control (a known item shows the migrated poster and resume point), and a review list of
unmatched/missing-art items. `GET /api/metadata/review` for the unmatched.

---

## 8. What does not map (decide with the user)

- **User ratings.** Plex `metadata_item_views.rating` is the person's own score; ply's `title_ratings`
  holds *external* sources only (M10). Options: (a) drop user ratings; (b) add a ply user-rating feature
  first; (c) stash them in a sidecar NFO `<userrating>` for later. **Open question.** Do not fake an
  external source with the user's score.
- **Critic ratings.** Plex `rating`/`audience_rating` come from its agent (often IMDb/RT). Rather than
  guess the source/scale, let ply fetch fresh ratings (M10 IMDb dataset / MDBList). Optionally map only
  when the source is unambiguous.
- **Genres/labels.** ply genres come from TMDB. Plex custom **labels** have no ply equivalent — drop or
  report. Genres: let TMDB provide; do not copy Plex genre strings.
- **Playlists.** ply's playlist design is separate (EVENT-playlist); migrate in a later phase.
- **Cast/crew, plot, taglines.** Fetch fresh from TMDB after matching; do not copy Plex text (it may be
  stale or from a different source). Plex `summary` can seed an NFO only if TMDB has nothing.
- **Music/photos/DVR.** Out of scope; ply is video-first.

---

## 9. Edge cases

- **Multi-version films** (Plex several `media_parts`, ply several items of one title): match the title
  once; write per-file `<base>-poster` if versions should differ, else one `poster.jpg`. Watch state is
  per file in ply — import each part's offset to its own item.
- **Specials (season 0):** `season-specials-poster.jpg`; episodes `season=0`. ply supports season 0.
- **Anime / absolute order:** Plex may number episodes absolutely; ply's TVDB episode-order handling
  (`episode_order`) applies. Match by TVDB id and let ply's order logic place them; flag mismatches.
- **Unmatched Plex items** (`com.plexapp.agents.none`): no external id → title+year fallback; if still
  unmatched, migrate artwork+watch state anyway (they join by path) and leave metadata for review.
- **Moved/renamed files since Plex indexed them:** the path join fails; fall back to size+fingerprint,
  else report. Never delete or move media.
- **macOS path normalisation:** `items.path` is raw OS bytes; Plex `file` may differ in NFC/NFD. Normalise
  both sides identically for the join, but write sidecars to the real on-disk path.
- **DMCA-removed posters:** Plex may have no poster for some films; skip art, report, let TMDB/frame-grab
  stand in.
- **Already-migrated / re-runs:** idempotent — skip existing sidecars (unless `-force`), upsert watch
  state, do not duplicate matches. A second run should be a no-op or a top-up.

---

## 10. Safety rules (repo + migration)

- **Read-only on Plex.** Copy the DB before opening; never write Plex files, DB, or bundles. Never log
  the `X-Plex-Token`.
- **Never overwrite media or existing ply sidecars** without `-force`. Write art via temp file + rename.
- **Dry-run first** (`-dry-run`): print the plan (matches, art writes, watch rows) and change nothing.
- **No secrets in the repo** (token, keys). Fixtures must use fake tokens/ids.
- **Positive controls** for every absence assertion (e.g. before asserting "no poster written", assert
  the item exists and had a Plex selection).
- Leave no background processes; scratch in `t.TempDir()`/session scratchpad.

---

## 11. Testing plan (acceptance)

- **Fake Plex** (`httptest`) serving recorded API fixtures (sections, all?type=1/2/4, metadata children,
  thumb/art bytes, collections) and/or a fixture SQLite DB built by a test helper. No test touches a real
  Plex server or the network.
- **Mapping tables:** guid→id (legacy + new agents, `?lang=` stripped, `none`), Plex type→ply kind,
  view_offset→position, sidecar naming (one-film vs multi-version folder, season 0, specials).
- **Path join:** exact match; NFC/NFD; a moved file falls back to fingerprint; an unknown path is reported,
  not guessed.
- **Artwork:** selected vs default; skip when sidecar exists; skip Local Media Assets; DMCA-missing →
  no write + report; bytes sniffed (JPEG/PNG), temp+rename.
- **Watch state:** offset→seconds, watched threshold, play_count/last_viewed_at fidelity, CHECK respected,
  unwatched deletes the row.
- **Pinning interaction (the crucial one):** after migration, a TMDB refresh (`UpsertTitle` with a
  different poster) must NOT replace the migrated sidecar; `pickArt` still serves local. Reuse
  `internal/store/artwork_test.go` patterns.
- **e2e** (`cmd/ply`, integration tag): fake Plex + fake TMDB → run migration → ply lists the film with
  the migrated poster, correct resume point and watched flag; a subsequent metadata refresh keeps the
  poster; re-running the migration is a no-op.
- **Mutant checks:** for each non-trivial rule (guid parsing, path join, sidecar name choice, watched
  threshold, pinning), a planted bug must fail a test.

---

## 12. Implementation (as built)

The tool lives in `tools/plex-migrate/` (a standalone command, `make build-migrate`), cross-platform
(`CGO_ENABLED=0`, pure-Go `modernc.org/sqlite`):

- `plex/` — read Plex behind one `Source` interface: `APISource` (HTTP + token, exact selected art) and
  `DBSource` (a read-only copy of the SQLite DB + `Metadata` bundles). Models, guid parsing (legacy +
  new agents), normalised `[]Item`. Table-tested with a fixture DB and a fake API server.
- `migrate/` — `ReadPlyItems` (reads `ply.db` read-only for the path join), `PathMap` (mount-prefix
  rewriting + NFC normalisation), `BuildPlan`/`Render` (the dry-run plan), sidecar naming that reuses
  `internal/metadata/localart.go`, and `Apply` (atomic sidecar writes + ply API match/progress/watched;
  idempotent). Tested against a fake Plex source and a fake ply API.
- `main.go` — flags, wiring, the report; dry-run by default, `-apply` to write.

Stdlib-first; the only ply import is `internal/metadata` (sidecar conventions). Watch state currently
goes through the ply API (position + watched flag); full `play_count`/`last_viewed_at` fidelity would
need a direct-store write mode (future).

---

## 13. Open questions for the user (resolve before/while implementing)

1. User ratings: drop, add a ply feature, or stash in NFO? (§8)
2. API mode vs DB mode as the default? (API is simpler/robust; DB is offline/faster.)
3. Collections: import custom Plex collections, or rely on TMDB collections only?
4. Overwrite policy: never overwrite existing ply sidecars (default) vs `-force`?
5. Which Plex account(s) to migrate watch state for (multi-user servers)?
6. Scope of the first cut: movies+shows+art+watch state (recommended), collections/playlists later?

---

## Sources (Plex data model / API — verify against the live server)

- Plex data dir & bundle layout: Plex support "Where is the Plex Media Server data directory located?"
  (https://support.plex.tv/articles/202915258-where-is-the-plex-media-server-data-directory-located/);
  community bundle trees (https://forums.plex.tv/t/poster-location-in-linux/918764/2).
- `Guid` external-id array (`imdb://`, `tmdb://`, `tvdb://`) and legacy agent guid strings: Plex dev forum
  "Custom Metadata Providers" (https://forums.plex.tv/t/announcement-custom-metadata-providers/934384/84);
  community mapper (https://github.com/josh-gaby/python-plexmovieagentmapper).
- API metadata fields (`viewOffset` ms, `viewCount`, `lastViewedAt`, `Guid`, `Media.Part.file`): Plex
  library OpenAPI mirrors and client docs (e.g. https://pub.dev/documentation/dart_plex/0.2.2/dart_plex/PlexMetadata/PlexMetadata.html).
- Locked-poster `user_fields` flag: https://nocksoft.de/tutorials/poster-in-plex-entsperren/ (verify the
  bit value on the actual DB; prefer `user_thumb_url` for the selection).

ply side is authoritative in this repo: `docs/M6-METADATA.md`, `docs/DESIGN.md`, `docs/M10-RATINGS.md`,
`internal/store/migrations/`, `internal/metadata/localart.go`, `internal/api/`.