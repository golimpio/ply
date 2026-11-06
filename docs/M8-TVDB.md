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
  - The status line reads "Shows: TVDB", "Shows: TMDB (no TVDB key)", "TVDB unreachable" (an outage) or "TVDB rejected the
    key or PIN" (a refusal: the two are kept apart, `Resolved.TVDBDown` and `TVDBRefused`).
  - "Test" checks the login: `POST /api/settings/test {"provider": "tmdb" | "tvdb"}` logs in (TVDB, with the PIN if there
    is one) or asks TMDB's configuration, with the key **in use** (a flag, the environment, the settings or the build
    key; the page cannot read the stored one back), and answers `200 {"ok": true}` or `{"ok": false, "error": "..."}`.
    A key that does not work is an answer, not a failed request; the error never holds a key or a PIN. 400 for another
    provider, 503 without settings. It is private like `/api/folders` (a page of another site is refused: it makes
    the server log in somewhere). A TVDB key that logs in clears the status line's "rejected" or "unreachable" at once
    (the worker retries what it put off when its wait is over, or at once after a PUT/PATCH of settings, which calls `Reload`).
  - The card carries TVDB's attribution, as their terms require (`tvdbAttribution` in `/api/settings`).
- `/api/settings` gains `tvdbKeySet`, `tvdbPinSet`, `tvdbKeySource`, `tvdbPinSource` (`"flag"`, `"env"`, `"settings"` or
  `""`, as the key's), `tvdbAttribution` and `tvdbAttributionUrl`; the answer to a
  PUT or PATCH that stored a key also has `tvdbKeyChecked` (a PIN alone does not set it: it is the answer about a key). PATCH accepts `tvdbKey` and `tvdbPin` ("" removes).
  A PIN that a flag or the environment gives cannot be removed here, as the limits a flag fixes cannot be changed:
  `{"tvdbPin": ""}` is `409 {"error": "tvdbPin is set by -tvdb-pin and cannot be removed here"}` (`PLY_TVDB_PIN` for
  the environment), before anything is stored. Storing one is allowed (the flag still beats it), as for the key.
  The key is checked with a login (with the PIN that goes with it) before it is stored, as TMDB's is.
- **Last tested:** `/api/settings` also has, for each provider (`tmdb`, `tvdb`, `mdblist`), `<p>KeyTestedAt` (unix seconds;
  `0` = never tested, or the key in use changed since) and `<p>KeyTestOk` (`false` when there is no test): `tmdbKeyTestedAt`,
  `tmdbKeyTestOk`, `tvdbKeyTestedAt`, `tvdbKeyTestOk`, `mdblistKeyTestedAt`, `mdblistKeyTestOk`, always present.
  - What sets them: `POST /api/settings/test` (a key that is refused and a provider out of reach are both a failed test; the
    answer keeps its shape) and a PUT/PATCH that saved a key and checked it (the `*KeyChecked` paths), with the outcome of
    that check. A save that is refused records a failure only for the key already in use (saved again). A request that
    made no check (no key, lookups off, another provider) is not a test.
  - What clears them: any change of the key **in use** (stored, removed, or shadowed by a flag or the environment at
    start). A stored key that comes or goes under a flag's changes nothing. TVDB's test is of the key and the PIN together,
    so a PIN change clears it.
  - How: the result is kept with a fingerprint, a 16 hex digit prefix of a SHA-256 of the provider and the key (and, for
    TVDB, the PIN). It counts only while the key in use has that fingerprint (so a flag or the environment is caught at
    start too); an `Update` that changes the key in use drops it as well, so an old key that comes back has no test. A
    test that ends after the key was changed (a slow one) is not kept. Neither the key nor the fingerprint is returned or logged.
  - **What is persisted:** the result is always kept in memory (`Settings.tests`; a restart loses it). It is also written
    to the settings table, one row per provider, `keytest_<p>` = `<fingerprint> <unix seconds> <ok|failed>`, **only when every
    secret the fingerprint covers is itself stored in that table**: the TMDB or MDBList key with `tmdbKeySource` or `mdblistKeySource` = `settings`;
    for TVDB the key with `tvdbKeySource` = `settings` and, if there is a PIN, `tvdbPinSource` = `settings`. A key or PIN
    from a flag, the environment or the build is never behind a row (the fingerprint is not salted, and a short PIN next to
    one would be open to a guess made offline by whoever has the database). A row found while the key in use is not wholly
    stored there is deleted (by `GET /api/settings`, or by a test), whatever wrote it.
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
- A fetch that is dropped for its order also leaves the `next_episode_*` columns alone: they are the next episode of the
  order the fetch was made in.
- The worker's own match of a show at TMDB (`LinkSeriesTitle` with a state to expect: the fallback, for another folder or
  name of a show that is on TVDB already) is linked to the TVDB row as it is: nothing is written (not the title, its
  genres, its cast or its seasons; the worker does not prefetch the images or remember the NFO either), the row stays
  TVDB's with its episodes. A person's pick of the TMDB title (no state to expect) does turn it into TMDB's, as below.
- A title that leaves TVDB (a person picks its TMDB title by hand) is TMDB's: its episodes, numbered in TVDB's order, are
  dropped and `episode_order` is `aired` again; TMDB has no such thing.
- A TVDB person (`people.tvdb_id`) is its own row until its record at TVDB gives a TMDB id (`/people/{id}/extended`, when
  its page is first opened). If another person has that TMDB id the two are merged into the TMDB person (credits move,
  the TVDB id goes on it, the TVDB row goes); details already fetched from TMDB are kept, and so are its name and photo
  when a TVDB credit comes again. Two TVDB people that TVDB lists under one TMDB person stay two (the row holds one
  TVDB id). `GET /api/people/{id}` answers for the surviving person (its `id` is the new one), also for a request that
  lost a race with another that merged the person first (the store finds the survivor by the alias, or by the TMDB id).
- **A merged person's old id** (`009_people_alias.sql`): `people_alias(old_id PRIMARY KEY, new_id REFERENCES people ON
  DELETE CASCADE)` is written by the merge, which also moves the aliases of the person that goes (a chain a, b, c is
  a, b -> c; the read follows chains anyway). `GET /api/people/{oldId}` is then
  `404 {"error": "merged", "into": <newId>}`; an id that never was a person, or whose survivor is gone too (the
  cascade removed the alias when the library no longer had them), is the plain `404 {"error": "person not found"}`.
  A trigger removes an alias when a new person takes the id (SQLite hands out the highest one again).
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
  show at thetvdb.com gives that one show, score 1 (a slug is found by an exact slug among `/search` results). `tvdb` takes its id after a
  colon, a space, both or neither, in any case ("TVDB:81189", "tvdb 81189", "tvdb : 81189"); the id is digits only.
- Aired is TVDB's `official` season type; `dvd` and `absolute` are asked for by name. In the absolute order a show is one
  season numbered through (season 0 specials stay); a show with no episodes in the order asked for is `ErrNoSuchOrder` (409),
  never an empty show.
- With only a TVDB key, movies stay pending (they need TMDB); with only a TMDB key everything is as in M6.

**Refresh.** TVDB's `/updates` (since the last poll, paged) is the feed for the shows with `source = tvdb`; TMDB's changes
feed is for movies and the other shows; ids of the two are never mixed. The 14 day fallback and `next_air_date + 6h` are
unchanged (TVDB's `nextAired`, and the episode that airs that day in the show's order).

TVDB cannot stall TMDB's work. A refresh has two shares, one for each provider's titles (`refreshParts`), each with its
own feed and its own poll time (`metadata_changes_poll` for TMDB, `metadata_tvdb_changes_poll` for TVDB): a provider
out of reach, or refusing the key, holds back its own share and nobody else's.
- TVDB's feed out of reach: TVDB's share is put off 15 minutes (`refreshRetry`), its poll time stays, and TMDB's share
  (its changed movies and shows) is refreshed and its poll moves. A refused key in the feed is the same, put off an hour.
- On the age path a show from TVDB that fails because TVDB refuses the key, or cannot be reached, ends the asks of TVDB
  for that call (the other shows from TVDB are passed over) and the rest goes on. Then TVDB's poll time and the time of
  the last refresh do not move: nothing was refreshed for those titles.
- Each share has its own time of the last refresh (`metadata_tmdb_last_refresh`, `metadata_tvdb_last_refresh`) and is due
  once a period after it, so a TVDB that refuses the key for days does not keep TMDB's share from starting each period.
  `metadata_last_refresh` is kept for the status (`lastRefresh`): the older of the two, once both have been done (an
  install from before has the one time, which stands for a share that has none yet). `Worker.Reload` (a new key) ends
  every wait.
A refused TMDB key still ends the pass. A show from TVDB is not in
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
"Shows: TMDB (no TVDB key)", "TVDB unreachable" (TVDB's last answer was a network failure) or "TVDB rejected the key or PIN"
(it was a refusal). A key that logs in (`Settings.Update`) clears both.

**Attribution.** thetvdb.com/api-information (read 2026-11-05) asks for a direct link to TheTVDB.com shown to users who see
its metadata, and gives the sample wording used for `tvdbAttribution`: "Metadata provided by TheTVDB. Please consider
adding missing information or subscribing." (the wording is a sample, not mandated); `tvdbAttributionUrl` is the link.

**Known limits.** The "absolute order is suggested" hint needs the name parser to say that a number had no season; it
does not yet.

## As built (web UI)
- **Settings, TVDB card** (`TvdbKey.svelte`), next to TMDB's:
  - The key and an optional subscriber PIN, in one form, so a key that needs a PIN is checked with it. Both are
    password fields that never show what is stored.
  - All three key fields (TMDB, TVDB key, PIN) carry `autocomplete="new-password"` and the 1Password, LastPass and
    Bitwarden ignore attributes. A password manager must never fill ply's own access token into them: Save would
    send it to a third party.
  - Save sends only what was typed. A 400 shows the server's words; a 502 says TVDB could not be reached. With
    lookups off it says the key or PIN was stored without a check.
  - Remove key and Remove PIN show only for a value stored from this page (`tvdbKeySource`, `tvdbPinSource` =
    `settings`). A value from a flag or the environment shows where it comes from and has no field.
  - **Test** (`KeyTest.svelte`, on both cards): `POST /api/settings/test`. It is disabled while saving, with no key,
    or with lookups off, each with the reason. A result is dropped when the settings change. A Test that works
    refetches the status, so `showsLine` updates.
  - The attribution is shown word for word, with its link.
- **Status card:** `showsLine` is shown as the server gives it. The no-key wording accounts for both keys. With only a
  TVDB key it polls while a pass runs, never for movies that are pending until a TMDB key comes.
- **Show page:** an "Episode order" select (Aired, DVD, Absolute) for shows whose source is TVDB. It sends
  `PATCH {episodeOrder}`, then refetches. "N episodes don't fit this order." shows when `unmapped` > 0. A 409
  shows the server's words and puts the select back; a 502 says TVDB can't be reached.
- **Source badges** (`SourceBadge.svelte`): TVDB, TMDB or "From .nfo" on the Item and Show pages and in Fix match
  results.
- **Fix match:**
  - It accepts the forms `ParseTVDBRef` accepts (`tvdb:N`, `TVDB:N`, `tvdb N`, `tvdbN`, thetvdb.com series links)
    and sends them as `{tvdb}`. A TVDB result sends `{tvdbId}`.
  - A TVDB ref in a film's dialog is refused ("TVDB has shows only").
  - The error texts match the server's exact strings.
- **People:** a 404 `{"error":"merged","into":N}` replaces the address with person N. Any other 404 says the
  person isn't in the library.
- Checked by `web/e2e/tvdb-ui.mjs` against a mocked API; every scenario names the mutant it catches.

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
