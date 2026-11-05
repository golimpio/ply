# M8 — TVDB for TV shows, TMDB as the fallback

The user's decision: shows come from TheTVDB (v4 API), and TMDB is the fallback. Movies stay on TMDB.
This builds on M6 (docs/M6-METADATA.md). Only what differs is written here.

## When TVDB is used
- **Shows use TVDB** when a working TVDB key is configured, plus a subscriber PIN if the key needs one.
  Otherwise they use TMDB, exactly as in M6.
- **Fallback, per show:**
  - TVDB finds nothing above the match threshold: try TMDB.
  - TVDB is unreachable: back off as M6 does. Never fall back just because the network is down, or a show
    would flip between providers.
- `-metadata=false` turns both off.
- The order of local sources is unchanged. NFO `<uniqueid type="tvdb">` and `tvdbid` were already parsed as
  hints (ids.go). With TVDB on they are matched exactly, through `/series/{id}`.

## Keys
- **TVDB v4 keys are per project.**
  - Under the "user-supported" model, the project key ships with the app (ldflags `metadata.DefaultTVDBKey`),
    and each user enters their subscriber PIN.
  - A licensed key needs no PIN.
- **Precedence**, as for TMDB:
  - key: `-tvdb-key` > `PLY_TVDB_KEY` > the settings table > ldflags;
  - PIN: `-tvdb-pin` > `PLY_TVDB_PIN` > the settings table.
- Neither the key nor the PIN is ever returned or logged.
- **Login:** `POST /v4/login {apikey, pin}` returns a JWT, valid for one month. It is kept in memory only,
  renewed after 25 days, and renewed once on a 401 before giving up.

## Identity and store (`008_tvdb.sql`)
- A series matched through TVDB has `titles.tvdb_id` set, and `tmdb_id` from TVDB's `remoteIds` when present.
  - The title's `source` column gains `tvdb` (CHECK list).
  - `UNIQUE (kind, tmdb_id)` stays. Add `UNIQUE (kind, tvdb_id)`.
- **People:** `people.tvdb_id INTEGER UNIQUE`.
  - A TVDB person whose `remoteIds` give a TMDB id merges into the existing TMDB person.
  - Otherwise it is a new person.
  - The person pages link across both.
- **Episode order:** `titles.episode_order TEXT NOT NULL DEFAULT 'aired' CHECK (... IN ('aired','dvd','absolute'))`.
  - Files map to episodes using the chosen TVDB season type: `official`, `dvd` or `absolute`.
  - Absolute order is for anime-style names (`Show - 123.mkv`). In a shows folder, a lone number with no season
    is read as an absolute number (M7's parser exposes it), and absolute order is suggested.
- **Images:** refs are `tvdb:/banners/...`.
  - Served at `/api/images/tvdb/{size}/{path...}`, which fetches only from `artworks.thetvdb.com` (allowlist).
  - Paths are restricted to `[A-Za-z0-9/_.-]`, with no `..`.
  - Cached like TMDB images.
  - Sizes: `w92`–`w342` use TVDB's `_t` thumbnail when there is one. Larger sizes use the original.
- **Language:** `-metadata-language en-US` maps to TVDB's 3-letter code (`eng`). Use the translations endpoint;
  when a translation is missing, use the original-language text.

## Provider
- `Provider` and `Candidate` are TMDB-shaped today: `TMDBID int64`.
  - Generalise them to `ID int64` plus `Source string` ("tmdb" | "tvdb").
  - Add a series-only provider in front: `Shows` = TVDB with a TMDB fallback. Movies call TMDB directly.
- **Refresh:**
  - `GET /v4/updates?since=<unix>&type=series` replaces TMDB's changes feed for TVDB shows. It is paged.
  - The 14-day age fallback stays.
  - Airing shows are refreshed at `nextAired` + 6h.
- **Fix match:**
  - The search box accepts `tvdb:12345` and `thetvdb.com/series/<slug>` links. A slug is resolved through
    `/search?query=` with `type=series` and an exact slug match.
  - Results say where they came from: "TVDB" or "TMDB".
  - The user may pick a TMDB result even when TVDB is on. That match is manual and sticks.
- **Switching providers:**
  - When TVDB becomes available, shows matched through TMDB are moved over by id. TMDB `external_ids.tvdb_id`
    gives an exact TVDB id, so this costs no searches.
  - Manual matches move the same way and stay manual.
  - A show TVDB doesn't know stays on TMDB.

## API and UI
- **Settings:** a "TVDB" card holds the PIN field, plus a key field only when no key is built in.
  - The status line reads "Shows: TVDB", "Shows: TMDB (no TVDB key)" or "TVDB unreachable".
  - "Test" checks the login: `POST /api/settings/test {"provider": "tmdb" | "tvdb"}` logs in (TVDB, with the PIN if there
    is one) or asks TMDB's configuration, with the key **in use** (a flag, the environment, the settings or the build
    key; the page cannot read the stored one back), and answers `200 {"ok": true}` or `{"ok": false, "error": "..."}`.
    A key that does not work is an answer, not a failed request; the error never holds a key or a PIN. 400 for another
    provider, 503 without settings. It is private like `/api/folders` (a page of another site is refused: it makes
    the server log in somewhere).
  - The card carries TVDB's attribution, as their terms require (`tvdbAttribution` in `/api/settings`).
- `/api/settings` gains `tvdbKeySet`, `tvdbPinSet`, `tvdbKeySource`, `tvdbAttribution` and `tvdbAttributionUrl`; the answer to a
  PUT or PATCH that stored a key also has `tvdbKeyChecked` (a PIN alone does not set it: it is the answer about a key). PATCH accepts `tvdbKey` and `tvdbPin` ("" removes).
  The key is checked with a login (with the PIN that goes with it) before it is stored, as TMDB's is.
- **Show page:** an "Episode order" picker (Aired / DVD / Absolute) when the show came from TVDB.
  `PATCH /api/titles/{id} {episodeOrder}` re-maps the files and refreshes; its answer has `unmapped`, how many of the
  show's episode files have no episode in the new order. `episodeOrder` is in a title's detail only when its `source` is
  `tvdb` (a show from TMDB has no order to show).
- Every title gets `source: "tvdb" | "tmdb" | "nfo" | "mixed"`, shown small on the detail page.

## As built (server side)

**Store.** `008_tvdb.sql` rebuilds `titles` (SQLite cannot alter a CHECK): create the new table, copy, drop, rename,
with foreign keys off. The runner knows a migration that starts with the line `-- ply:foreign-keys-off`: it turns the
keys off on its connection before the transaction, runs `PRAGMA foreign_key_check` before the commit (a violation is an
error and nothing is committed) and turns them on again, whatever happened. The runner applies every migration whose
version is **not recorded**, in order, not only those above the highest recorded: a database that has 8 and not 7 (the
two branches numbered theirs apart) gets 7 too. A `tvdb_id` that two titles shared (TMDB's
external ids can say so) stays on the one with the lowest id. `people` gets `tvdb_id` and a unique index on it.

- A show has one row, found by its own id first (TVDB's for `source = tvdb`, else TMDB's) and then by the other it
  carries, so a show that moves between providers keeps its row, episodes, credits and matches. An id that another row
  already has is not written (the unique index would fail the fetch).
- `source` is `tvdb` for a show from TVDB, and **stays `tvdb` when an NFO is laid over it** (`mixed` is TMDB data with an
  NFO on top): the source is what tells a refresh which provider to ask.
- TVDB's genre ids are not TMDB's, so none is stored: genres are matched by name and new ones get ids of their own.
- `episode_order` is kept by an upsert that does not give one. `TitleInput.ResetSeasons` replaces all of a title's
  seasons and episodes: a TVDB fetch always brings every season, so it sets it.
- A fetch from TVDB says the order it was made in (`TitleInput.ExpectOrder`; its `EpisodeOrder` is empty, so a refresh
  never writes an order). If the stored order is another by the time it is stored (a person changed it while the show was
  being fetched), the title is written but its seasons are dropped: a refresh that was already under way cannot put the
  old order, or episodes numbered in it, back.
- A title that leaves TVDB (a person picks its TMDB title by hand) is TMDB's: its episodes, numbered in TVDB's order, are
  dropped and `episode_order` is `aired` again; TMDB has no such thing.
- A TVDB person (`people.tvdb_id`) is its own row until its record at TVDB gives a TMDB id (`/people/{id}/extended`, when
  its page is first opened). If another person has that TMDB id the two are merged into the TMDB person (credits move,
  the TVDB id goes on it, the TVDB row goes); details already fetched from TMDB are kept, and so are its name and photo
  when a TVDB credit comes again. Two TVDB people that TVDB lists under one TMDB person stay two (the row holds one
  TVDB id). `GET /api/people/{id}` answers for the surviving person (its `id` is the new one), also for a request that
  lost a race with another that merged the person first (the store finds the survivor by the TMDB id).
- The archive of a removed folder (M7) keeps a manual match made through TVDB by its TVDB id (`item_archive.series_tvdb_id`,
  next to `series_id` for TMDB: a show's slot holds one of the two, the latest). `RestoreArchived`, `PendingMatches` and
  `ForgetArchivedMatch` call it the kind `store.KindSeriesTVDB` (`"series-tvdb"`), which the scanner passes on
  unchanged and `cmd/ply/restore.go` applies with `Ref{TVDBID}`.

**Providers.** `metadata.Provider` is one place (`TMDB`, `TVDBProvider`), with `Name()`; `Candidate` has `ID` and `Source`,
`Series(ctx, id, order, seasons)` takes the episode order. `metadata.Providers{TMDB, TVDB}` is what the worker asks:

- Movies are TMDB's. Shows: `MatchShow` asks TVDB; a match below `MatchThreshold` (0.80) is also looked for at TMDB and the
  better score wins, TVDB's on a tie. A TMDB id hint alone goes straight to TMDB; a TVDB id or an IMDb id goes to TVDB.
- TVDB out of reach (or the other one, when TVDB's match is doubtful) is an error: the show is put off with the usual
  backoff, never matched at the other provider. A TVDB key that is refused does **not** end the pass (TMDB's does): the
  show waits an hour (`tvdbRefusedDelay`), TVDB is marked down (the status line), and what is at TMDB goes on.
- The fix-match search lists TVDB's results, then TMDB's, each with its `source`. Typing `tvdb:81189` or the address of a
  show at thetvdb.com gives that one show, score 1 (a slug is found by an exact slug among `/search` results).
- Aired is TVDB's `official` season type; `dvd` and `absolute` are asked for by name. In the absolute order a show is one
  season numbered through (season 0 specials stay); a show with no episodes in the order asked for is `ErrNoSuchOrder` (409),
  never an empty show.
- With only a TVDB key, movies stay pending (they need TMDB); with only a TMDB key everything is as in M6.

**Refresh.** TVDB's `/updates` (since the last poll, paged) is the feed for the shows with `source = tvdb`; TMDB's changes
feed is for movies and the other shows; ids of the two are never mixed. The 14 day fallback and `next_air_date + 6h` are
unchanged (TVDB's `nextAired`, and the episode that airs that day in the show's order).

TVDB cannot stall TMDB's work. In a refresh, a show from TVDB that fails because TVDB refuses the key, or cannot be
reached, ends the asks of TVDB for that call (the other shows from TVDB are passed over) and the rest goes on; an
outage also asks for the refresh again soon, a refused key does not. A refusal of TVDB's changes feed sends the refresh
by age, with the poll time left where it was. A refused TMDB key still ends the pass. A show from TVDB is not in
`SeasonsNeedingFetchAll` (a fetch brings every season), so the fetch of missing seasons never asks TVDB.

The move to TVDB, the refresh and the fetch of a season ask `Pace` before each title, as the matching does.

**Switching.** When TVDB becomes available, the shows that exist then, came from TMDB (also with an NFO on top) and have
a TVDB id from TMDB's `external_ids` are fetched from TVDB by that id: no search; 20 a pass. A match made by hand moves
too and stays manual. A show TVDB does not know, or TMDB gave no TVDB id, stays. It happens once per key
(`metadata_tvdb_switched`; a TVDB key where there was none, or the key taken away, clears it; one key replaced by
another does not: it is the same TVDB, and the shows a person picked at TMDB meanwhile stay there) and only for titles that existed when it began
(`metadata_tvdb_switch_upto`): a show that fell back to TMDB afterwards, and a TMDB title picked by hand while TVDB is
on, are not moved.

**Images.** `tvdb:/banners/...` refs; `GET /api/images/tvdb/{size}/{path...}` with the TMDB size list. The path must be
plain (`TVDBImagePath`, a jpg, jpeg or png extension) and is fetched only from `artworks.thetvdb.com`, whatever the path says.
w92 to w342 are TVDB's `_t` thumbnail (the original when TVDB has none), the rest the original. Cached under
`<data>/images/tvdb/<size>/<path>`.

**Status line.** `GET /api/metadata/status` has `tvdbKeySet`, `shows` (`tvdb`, `tmdb` or `""`) and `showsLine`: "Shows: TVDB",
"Shows: TMDB (no TVDB key)" or "TVDB unreachable" (TVDB's last answer was a network failure or a refused key).

**Attribution.** thetvdb.com/api-information (read 2026-11-05) asks for a direct link to TheTVDB.com shown to users who see
its metadata, and gives the sample wording used for `tvdbAttribution`: "Metadata provided by TheTVDB. Please consider
adding missing information or subscribing." (the wording is a sample, not mandated); `tvdbAttributionUrl` is the link.

**Known limits.** The "absolute order is suggested" hint needs the name parser to say that a number had no season; it
does not yet.

## Ownership
- **velvet-viper:**
  - the TVDB client;
  - the provider generalisation and fallback;
  - migration 006 and the store;
  - the settings and keys;
  - images;
  - the worker refresh;
  - the API.
- **rare-tiger:**
  - the Settings TVDB card;
  - the episode-order picker;
  - the source badges;
  - Fix match for TVDB links and results.
- Reviews and tests follow the house rule: each side reviews the other, and every fix has a test that fails without it.
- The TVDB client is built first, in new files only (`tvdb.go`, `tvdb_types.go`, `tvdb_test.go`, testdata),
  while M7 lands. Everything else waits for M7, because both touch the store, the API and main.

## Key editing (the user asked)
Settings lets the user **edit both keys, TMDB and TVDB**, in the same way:
- See whether a key is set and where it comes from (flag, environment, settings, built in).
- Replace it (it is checked before it is stored).
- Remove a stored key.
- Test it.

A key from a flag or the environment can't be edited in the UI, and the card says so. TMDB already works this way
(M6). TVDB copies it, and adds the optional PIN.

**Checked 2026-11-04:** the user's TVDB key logs in **without a PIN**. Search and episodes both
return 200. So the PIN is optional: send it only when one is set. The key is never written to the repo, which is
public. It goes in the settings table through the UI, or in `PLY_TVDB_KEY`.

## Needs the user
- ~~A TVDB project key~~: received, and it works without a PIN.
- Until both arrive, everything is tested against a fake TVDB server (fixtures in testdata).
