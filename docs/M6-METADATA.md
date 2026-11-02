# M6 — metadata, organisation, live library

Goal: every file in the library gets correct and complete metadata (posters, backdrops, plot, genres, ratings,
cast and crew, collections, series/season/episode details) without the user doing anything, and when a match
is wrong, the user can fix it in two clicks. New files appear by themselves, with metadata, within seconds.

The working rules in `docs/DESIGN.md` still apply (plumb tools, no commits by agents, no background processes,
positive controls for absence assertions, stdlib first, idiomatic small packages). CGO_ENABLED=0 still builds.

## Sources and precedence

1. **Local files first** (no network): Kodi/Jellyfin-style `.nfo` and local artwork next to the media.
2. **TMDB** (themoviedb.org API v3) for everything else. TMDB also gives IMDb/TVDB ids.
3. **Fallback**: today's filename parse + frame-grab art (`internal/art`) when nothing matches.

TVDB and IMDb are not used (TVDB v4 needs a paid licence; IMDb has no free API).

### TMDB credentials
- Key from, in order: `-tmdb-key` flag, `PLY_TMDB_KEY` env, the `settings` table (set from the web UI),
  build default `ply/internal/metadata.DefaultKey` (set with `-ldflags -X`; empty in the repo).
- Accept both a v3 API key (sent as `api_key` query param) and a v4 read access token (contains `.`; sent as
  `Authorization: Bearer`). Never log a URL or header that contains the key.
- No key → metadata online lookups are off; local `.nfo`/artwork still work; the UI shows a one-time,
  dismissible setup card linking to the settings page.
- Hidden `-tmdb-url` flag / `PLY_TMDB_URL` env (API base) and `-tmdb-image-url` for tests.
- `-metadata=false` disables all online lookups (privacy switch); also a toggle in settings.
- `-metadata-language` default `en-US` (TMDB `language`); region for certifications = the part after `-`.
- Settings page shows the required TMDB attribution: "This product uses the TMDB API but is not endorsed or
  certified by TMDB" plus their logo.

## Identity hints (strongest first)
1. NFO `<uniqueid type="tmdb|imdb|tvdb">`, `<tmdbid>`, `<imdbid>`, `<id>`, or an NFO that is just a URL
   (`themoviedb.org/movie/603`, `imdb.com/title/tt0133093`).
2. Ids in the file or folder name: `tt\d{7,8}`, `[tmdbid-603]`, `{tmdb-603}`, `[imdbid-tt0133093]`,
   `{imdb-tt0133093}`, `[tvdbid-81189]` (Jellyfin/Plex/Emby conventions).
3. Parsed title + year. Movies: if the filename has no year but the parent folder parses to a title with a
   year (`Movies/Blade Runner (1982)/br.mkv`), use the folder. Shows: show name (parsed or folder).

An id hint gives confidence 1.0 (TMDB `/find/{imdb}?external_source=imdb_id` or `/movie/{id}` / `/tv/{id}`).

## Matching (`internal/metadata/match.go`)
- Search `/search/movie?query=&year=` (movies) or `/search/tv?query=&first_air_date_year=` (shows); if no
  results, retry without the year, then with a simplified query (fold accents, `&`→`and`, drop punctuation,
  drop a leading "The/A/An").
- Score each of the top 10 candidates in [0,1]:
  `0.6*titleSim + 0.3*yearScore + 0.1*popularityRank`.
  titleSim = max over (title, original_title) of a normalised similarity (case/accent/punctuation-folded token
  comparison; equal after folding = 1). yearScore: exact 1, ±1 0.8, query year unknown 0.5, else 0.
  popularityRank: 1 for the most popular result, linearly down to 0.
- ≥ 0.80 → `matched`. 0.55–0.80 → `matched` + `needs_review` flag. < 0.55 or no results → `unmatched`.
- Table-driven tests with recorded TMDB search responses (`internal/metadata/testdata/tmdb/*.json`) for: exact
  match, remake disambiguated by year ("The Thing" 1982 vs 2011), number titles ("1917", "2001 A Space
  Odyssey"), accents ("Amélie"), sequels ("Blade Runner 2049"), no year, no results, original-language title.

## What is fetched
- Movie: `/movie/{id}?append_to_response=credits,release_dates,external_ids,images&include_image_language=<lang>,null`
  → title, original title, overview, tagline, release date, runtime, vote average/count, genres,
  certification (release_dates for the region, falling back to US), poster, backdrop, logo (best `logos` entry in
  the language or null language), collection (`belongs_to_collection` → fetch `/collection/{id}` once),
  imdb id, cast (top 30 by order: person id, name, character, profile path), crew (Director, Writer,
  Screenplay, Story, Novel, Original Music Composer, Director of Photography, Producer).
- Show: `/tv/{id}?append_to_response=aggregate_credits,content_ratings,external_ids,images` → same fields plus
  first/last air date, status, networks, created_by (as crew job "Creator"), number of seasons.
  For every season that has files: `/tv/{id}/season/{n}` → season name, overview, poster, air date, and per
  episode name, overview, air date, runtime, still, vote average.
- Person (lazy, when the person page is first opened, then cached): `/person/{id}` → biography, birthday,
  deathday, place of birth, known_for_department, profile path, imdb id.

## Data model — migration `003_metadata.sql`
```
titles(id PK, kind TEXT CHECK(kind IN('movie','series')), tmdb_id INT, imdb_id, tvdb_id INT,
       title, original_title, sort_title, year INT, overview, tagline, release_date TEXT /*YYYY-MM-DD*/,
       end_date TEXT, status, runtime_min INT, rating REAL, vote_count INT, certification,
       poster TEXT, backdrop TEXT, logo TEXT,          -- image refs, see Images
       collection_id INT REFERENCES collections(id) ON DELETE SET NULL,
       source TEXT /*tmdb|nfo|mixed*/, locked INT NOT NULL DEFAULT 0, /*user edited: refresh keeps text fields*/
       next_air_date TEXT /*series: next_episode_to_air.air_date*/,
       fetched_at INT, created_at INT, updated_at INT,
       UNIQUE(kind, tmdb_id))
items  + title_id INT REFERENCES titles(id) ON DELETE SET NULL,   -- movies only (episodes go via series_matches)
       + match_state TEXT NOT NULL DEFAULT 'pending' /*pending|matched|unmatched|manual|disabled*/,
       + needs_review INT NOT NULL DEFAULT 0, + match_score REAL, + match_attempts INT, + next_attempt_at INT,
       + local_poster TEXT, + local_backdrop TEXT, + local_thumb TEXT,  -- absolute paths of sidecar artwork
       + fingerprint TEXT NOT NULL DEFAULT ''  -- size-independent: sha256(first 64 KiB + last 64 KiB); move fallback
series_matches(show TEXT PK /*items.show*/, title_id INT REFERENCES titles(id) ON DELETE SET NULL,
       match_state, needs_review, match_score, match_attempts, next_attempt_at, local_poster, local_backdrop)
seasons(title_id FK CASCADE, season INT, name, overview, poster, air_date, PRIMARY KEY(title_id, season))
episodes(title_id FK CASCADE, season INT, episode INT, name, overview, air_date, runtime_min INT, still,
       rating REAL, PRIMARY KEY(title_id, season, episode))
people(id PK, tmdb_id INT UNIQUE, name, profile TEXT, biography, birthday, deathday, place_of_birth,
       known_for, imdb_id, fetched_at INT)      -- NFO-only people have tmdb_id NULL, deduped by exact name
credits(title_id FK CASCADE, person_id FK CASCADE, kind TEXT /*cast|crew*/, job TEXT /*'' for cast*/,
       character TEXT, ord INT, PRIMARY KEY(title_id, person_id, kind, job))
genres(id PK /*TMDB genre id, or negative for NFO-only*/, name UNIQUE)
title_genres(title_id FK CASCADE, genre_id FK CASCADE, PRIMARY KEY(title_id, genre_id))
collections(id PK, tmdb_id INT UNIQUE, name, overview, poster, backdrop)
settings(key TEXT PK, value TEXT)
```
Indexes: `items(title_id)`, `items(match_state, next_attempt_at)`, `credits(person_id)`,
`title_genres(genre_id)`, `titles(collection_id)`, `people(name)`.
Orphan titles/people/collections (no items left) are pruned after a scan, in one transaction.
Moving/renaming a file keeps its item id (see Watching), so progress, match and edits survive.

## Images
- Image refs are strings: `tmdb:/abc.jpg` (TMDB file path) or `local:<item|series>:<id>:<poster|backdrop|thumb>`.
- `GET /api/images/tmdb/{size}/{file}`: size allowlist `w92 w185 w342 w500 w780 w1280 original`;
  file must match `^[A-Za-z0-9_-]+\.(jpg|png|svg)$`. Served from `<data>/images/tmdb/<size>/<file>`,
  downloaded once on a miss (single-flight per file, timeout, size cap 20 MB, written via temp file + rename),
  `Cache-Control: public, max-age=31536000, immutable`. Fails → 404 (UI falls back to frame-grab art).
- `GET /api/images/local/{kind}/{id}/{slot}` serves sidecar artwork found by the scanner (path from the DB only).
- After a match, the worker prefetches the poster (w342) and backdrop (w1280) so browsing is instant and keeps
  working offline; profile photos and stills are fetched lazily.
- Local artwork names (case-insensitive, jpg/jpeg/png/webp): movie `<base>-poster`, `poster`, `folder`, `cover`;
  backdrop `<base>-fanart`, `fanart`, `backdrop`, `background`; episode thumb `<base>-thumb`, `<base>`;
  show folder `poster`, `folder`, `fanart`, `backdrop`; season `seasonNN-poster`, `Season NN/poster`.
  Local beats TMDB. Folder-level names (`poster.jpg`) apply to a movie only when its folder holds one movie.

## NFO (`internal/metadata/nfo.go`)
`<base>.nfo`, else `movie.nfo` in a one-movie folder; `tvshow.nfo` in the show folder; `<base>.nfo` for episodes.
Parse with `encoding/xml`, tolerant: unknown elements ignored, a non-XML NFO is scanned for TMDB/IMDb URLs/ids.
Fields: title, originaltitle, sorttitle, year, premiered/releasedate, plot, outline, tagline, runtime, mpaa,
rating(s), genre*, studio, director*, credits*, actor{name, role, order, thumb}*, uniqueid*, set{name}.
If it has an id → fetch from TMDB (if enabled) and overlay NFO text fields on top (`source=mixed`). If online
is off or nothing matches → NFO alone (`source=nfo`). Fuzz test the parser.

## Background work (`internal/metadata/worker.go`)
- One worker goroutine per server, started by `main`, stopped on shutdown (no leaks; tests check it).
- Wakes on: startup, a scan/watch batch adding items (`Notify()`), a user action, and a ticker (1 min) for
  retries. Takes `pending` items/series due now (`next_attempt_at <= now`), in batches.
- Rate limit: token bucket 20 req/s, 4 concurrent requests; 429 → honour `Retry-After`; 5xx/network →
  item stays `pending` with backoff 1m, 5m, 30m, 2h, 6h (cap) and `match_attempts++`. Offline never marks a
  file `unmatched`.
- Refresh: daily, re-fetch titles whose `fetched_at` is older than 7 days for series that are not ended /
  movies released in the last 90 days, 30 days for everything else. `locked` titles keep text fields but
  still refresh images/credits. A season with new files and no `episodes` rows is fetched immediately.
- **Changes feed** (preferred over age-based refresh): once a day poll TMDB `/movie/changes` and `/tv/changes`
  (paged, last 24 h) and refresh only library titles in the set; last poll time in `settings`. If the last poll is
  older than 14 days (the endpoint's window), fall back to the age-based refresh above.
- **Airing series**: refresh at `next_air_date + 6h` so a new episode's name/still is there when the file lands;
  the same data drives an "Upcoming" rail and a "new episode" badge.
- **Local sources changed** (`.nfo`/artwork created/changed/removed next to an existing item, reported by the
  scanner's `OnSidecarsChanged`): re-read local sources for those items without a full re-match.
- `-metadata-refresh` flag (default 24h, 0 disables the periodic refresh, not the matching of new files).
- New files: scanner/watcher → `Notify()` → metadata visible in the UI within seconds of the file appearing.
- Status: `{enabled, keySet, running, pending, unmatched, needsReview, lastRefresh, lastError}`.

## Watching (`internal/watch`, `github.com/fsnotify/fsnotify`)
- Watch every directory under each root (add new dirs as they appear; skip dot dirs). Events are debounced
  per directory (2 s quiet), then a **partial scan** of that directory (non-recursive for file events,
  recursive for a new/renamed directory) runs through the existing scanner — same probe pool, same safety.
- A file is indexed only once its size and mtime have been stable for 5 s (copies in progress are skipped and
  re-checked); the periodic scan applies the same rule to files modified in the last 5 s.
- Deletes: a partial scan may delete rows only under a directory that still exists and whose walk had zero
  errors (same rule as roots). A vanished root (unmount) never deletes anything.
- **Moves/renames keep identity**: before deleting missing rows, pair each missing row with a new file of the
  same size and mtime (unique pair only) and update the path instead (keeps id, progress, match, edits).
  Applies to full scans too. If size+mtime pairing fails (copies that reset mtime), fall back to
  size + `fingerprint` (computed at index time).
- If a watch cannot be added (fd/inotify limits, network filesystems that don't deliver events), log once and
  rely on the periodic scan for that root; status reports `watching: true|false` per root.
- `-watch=true` flag (default on). `-scan-interval` stays as the safety net (default 15m).
- Scan status gains `watching` and per-root info.

## Embedded tags
ffprobe format tags as hints and as an offline source: `title`, `date`, `description`/`synopsis`, `show`,
`season_number`, `episode_sort`, `episode_id`, and an `attached_pic` stream (iTunes/mkv cover) as a local poster.

## Later (not M6)
NFO write-back after a manual fix (`-nfo-write`); chapters (`-show_chapters`) for skip-intro / up-next;
nameparse: multi-episode files (S01E01-E02), date-based episodes, anime absolute numbering.

## HTTP API additions
All image URLs are relative and carry `?token=` like other media URLs.
| Method | Path | Result |
|---|---|---|
| GET | `/api/items`, `/api/items/{id}` | ItemSummary gains `titleId?`, `poster?`, `backdrop?`, `logo?`, `rating?`, `certification?`, `genres?:[string]`, `matchState`, `needsReview`; episodes get `episodeTitle`/`overview`/`still` from metadata when present |
| GET | `/api/items/{id}` | ItemDetail gains `meta?: TitleDetail` (movie, or the episode's series) and `episode?: {name, overview, airDate, still, rating}` |
| GET | `/api/shows` | each show gains `titleId?`, `poster?`, `backdrop?`, `year?`, `overview?`, `genres?`, `rating?`, `matchState`, `needsReview` |
| GET | `/api/shows/{name}` | `{show…, meta?: TitleDetail, seasons:[{season, name, overview, poster, airDate, episodeCount}]}` |
| GET | `/api/people/{id}` | `{id, name, profile?, biography?, birthday?, deathday?, placeOfBirth?, knownFor?, imdbId?, credits:[{titleId, kind, job, character, title:TitleCard}]}` — only titles in the library, newest first |
| GET | `/api/people?q=` | `[PersonCard]` (name prefix/substring, people with ≥1 local title, max 20) |
| GET | `/api/genres` | `[{id, name, count}]` |
| GET | `/api/titles?genre=&collection=&person=` | `[TitleCard]` |
| GET | `/api/collections/{id}` | `{id, name, overview, poster, backdrop, titles:[TitleCard]}` (release order) |
| GET | `/api/metadata/search?kind=movie\|series&q=&year=` | `[{tmdbId, title, originalTitle, year, overview, poster, score}]` for the fix dialog |
| PUT | `/api/items/{id}/match` | `{tmdbId}` or `{imdbId}` → re-match this movie file, `match_state=manual`, returns ItemDetail |
| PUT | `/api/shows/{name}/match` | same for a whole show |
| DELETE | `/api/items/{id}/match`, `/api/shows/{name}/match` | unmatch (back to filename data), `manual`, no auto re-match |
| PATCH | `/api/titles/{id}` | `{title?, sortTitle?, year?, overview?, tagline?}` → sets `locked=1` |
| POST | `/api/items/{id}/metadata/refresh`, `/api/shows/{name}/metadata/refresh` | re-fetch now (202) |
| POST | `/api/metadata/refresh` | refresh everything (202) |
| GET | `/api/metadata/status` | worker status (above) |
| GET | `/api/metadata/review` | `[{kind:'movie'\|'series', itemId?, show?, title, year, matchState, needsReview, score, candidate?:TitleCard}]` |
| GET/PUT | `/api/settings` | `{tmdbKeySet, tmdbKeySource:'flag'\|'env'\|'settings'\|'default'\|'', metadataEnabled, language}`; PUT `{tmdbKey?, metadataEnabled?, language?}` validates the key against TMDB `/configuration` (400 with a clear message if bad); the key is never returned |

```
TitleCard   {id, kind, title, year, poster?, backdrop?, rating?, itemId? /*movie: first file*/, show? /*series*/}
TitleDetail TitleCard + {tmdbId?, imdbId?, originalTitle?, sortTitle, overview?, tagline?, releaseDate?,
            endDate?, status?, runtimeMin?, voteCount?, certification?, logo?, genres:[{id,name}],
            collection?: {id, name}, cast:[{personId, name, character, profile?}],
            crew:[{personId, name, job, profile?}], source, locked, matchState, needsReview}
PersonCard  {id, name, profile?, knownFor?}
```
Mutating endpoints need the token like everything else. Unknown ids → 404. Bad input → 400.

## Web (owned by rare-tiger unless agreed otherwise)
- Posters (2:3) on cards with `srcset` (w185/w342/w500), backdrop + logo in the hero, frame-grab fallback.
- Movies grouped by `titleId` (several files = one card, a version picker on the item page).
- Item page: overview, tagline, year · certification · runtime · rating, genre chips (→ genre page), director/
  writer links, cast rail with photos (→ person page), collection link, versions, "Fix match" and "Edit" in a
  "…" menu, "Refresh metadata".
- Fix match dialog: search box prefilled with title/year, results with poster/year/overview, one click to apply;
  also paste a TMDB/IMDb URL or id. Works for movies and whole shows.
- Person page `#/person/<id>`: photo, bio, "In your library" grid grouped Acting / Directing / Writing.
- Genre `#/genre/<id>`, collection `#/collection/<id>` pages; Shows page with series posters; show page with
  season tabs, episode stills and overviews.
- Search palette also finds people, genres and collections.
- Settings `#/settings`: TMDB key (with a link to get one), language, online on/off, metadata status,
  "Needs attention" list (unmatched + needs review) with inline fix, refresh all, scan now, attribution.
- Home: setup card when no key; rails for Continue, Recently added, genres.

## Tests (acceptance)
- Fake TMDB (`httptest`) serving recorded fixtures; no test touches the network. Includes 429 + Retry-After,
  5xx backoff, timeout, malformed JSON.
- Store: migration 003 on a DB that already has M5 data keeps it; orphan pruning; move keeps id.
- Matching tables (above); NFO parser tables + fuzz; local artwork discovery tables.
- Worker: new item → matched → images prefetched; offline → stays pending with backoff; refresh honours
  `locked`; shutdown leaves no goroutines (check with a counter or `goleak`-style comparison of runtime stacks).
- Watch: create/copy-in-progress/rename/move-across-dirs/delete/new-dir/unmount-like (root removed) in a temp
  tree; asserts item rows, that ids survive a rename, and that a growing file is not indexed until stable.
- API: every new endpoint incl. 400/404 and auth; image endpoint rejects traversal and bad sizes; the key never
  appears in any response or log line (capture the logger).
- e2e (`cmd/ply`, integration tag): server with fake TMDB → drop a file in the media dir → within a few
  seconds it is listed with poster + cast; fix match via API; person endpoint lists it; delete file → gone.
- Mutation check: for every non-trivial rule above, a planted bug must fail a test.
