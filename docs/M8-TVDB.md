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

## Identity and store (`006_tvdb.sql`)
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
  - The status line reads "Shows: TVDB", "Shows: TMDB (no TVDB PIN)" or "TVDB unreachable".
  - "Test" checks the login.
  - The card carries TVDB's attribution, as their terms require (`tvdbAttribution` in `/api/settings`).
- `/api/settings` gains `tvdbKeySet`, `tvdbPinSet`, `tvdbKeyChecked` and `tvdbKeySource`. PATCH accepts `tvdbKey` and `tvdbPin`.
- **Show page:** an "Episode order" picker (Aired / DVD / Absolute) when the show came from TVDB.
  `PATCH /api/titles/{id} {episodeOrder}` re-maps the files and refreshes.
- Every title gets `source: "tvdb" | "tmdb" | "nfo" | "mixed"`, shown small on the detail page.

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

## Needs the user
- A TVDB **project key**, registered at thetvdb.com under API Information, for ply, on the user-supported model.
- The user's own **subscriber PIN**.
- Until both arrive, everything is tested against a fake TVDB server (fixtures in testdata).
