# M10: ratings from the sources that matter

The owner asked (2026-11-05) for ratings from the relevant systems (IMDb, Rotten Tomatoes and so on), with room for
them in the UI. This builds on M6 and M8. The IDs needed are already stored: `titles.imdb_id`, `tmdb_id` and `tvdb_id`.

## Sources

| Source | What | How | Key |
|---|---|---|---|
| **IMDb** | rating /10 and the vote count, for movies, shows and episodes | IMDb's official non-commercial dataset `title.ratings.tsv.gz` (datasets.imdbws.com, refreshed daily, about 7 MB gzipped: `tconst`, `averageRating`, `numVotes`), joined on `imdb_id`; the episodes' ids come from `title.episode.tsv.gz` (about 40 MB: `tconst`, `parentTconst`, `seasonNumber`, `episodeNumber`) | none |
| **TMDB** | user score /10 and votes | already fetched (`titles.rating`, `vote_count`) | the TMDB key |
| **Rotten Tomatoes** | Tomatometer (critics %), the audience score %, and fresh/rotten/certified state | the MDBList API | an optional MDBList key (free: about 1,000 requests a day) |
| **Metacritic** | Metascore /100 | MDBList | same |
| **Letterboxd** | average /5 | MDBList | same |
| **Trakt** | rating % | MDBList | same |

- **TVDB:** v4 has no user rating (its `score` measures popularity), so ply shows none.
- **OMDb:** it gives IMDb, Rotten Tomatoes critics and Metacritic, with fewer sources than MDBList. It's an alternative only if the owner prefers it.
- **Rotten Tomatoes:** it has no public API, so MDBList is the way in.

**Terms**
- **IMDb:**
  - the data is for personal and non-commercial use only, which ply is;
  - it's never redistributed: each install downloads its own copy;
  - Settings shows IMDb's attribution text. Verify the exact wording against IMDb's terms when building.
- **MDBList:** credited, with a link, wherever its numbers are shown.

## Fetching (as queue jobs, low priority, network class; never while a viewer waits)
- **`ratings.imdb`, weekly.** It downloads the dataset if it's newer: `If-Modified-Since` / `ETag`, stored in settings.
  - It streams the gzip and keeps only the `tconst` values present in the library (`imdb_id` for titles, plus the episodes' IMDb ids, which the same job maps from the episode dataset first).
  - The download goes to `<data>/cache/imdb/`; the file is kept for the next diff and is never served.
  - A file of about 1.5 M rows must be parsed with constant memory, and the job yields between chunks.
- **`ratings.mdblist`, one job for every title that is due** (by TMDB id, else IMDb id, else TVDB id) when a key is set.
  - It's due when the title has none yet, or the ratings are over 14 days old (7 days for titles released in the last 90 days).
  - It's paced to the key's daily budget: read the remaining quota from the API if it gives one, otherwise stay at 900 a day.
  - A 429 pauses the service, through the existing limiter.
- **Privacy:** `-metadata=false` or lookups off means no rating downloads either. What leaves the machine is the MDBList key and one TMDB, IMDb or TVDB id per title, to MDBList; the IMDb datasets are downloaded whole, so nothing of the library is sent to IMDb. No titles or file names are sent anywhere by M10.
- **Episode IMDb ids:** IMDb's `title.episode.tsv.gz`, mapped by season and number (see *Episode ids*); stored in `episodes.imdb_id` (migration). No request per episode, to TMDB or to TVDB.

## Store (migration 010)
`title_ratings(title_id, source, value REAL, scale REAL, votes INTEGER, state TEXT, url TEXT, fetched_at INTEGER,
PRIMARY KEY (title_id, source))`.
- `source` is one of: imdb, tmdb, rt_critics, rt_audience, metacritic, letterboxd, trakt.
- `state` is for Rotten Tomatoes. On `rt_critics` it is fresh, rotten or certified; on `rt_audience` it is upright, spilled or verified_hot. Each entry has its own vocabulary, and there is no separate field. It is **derived** by the 60% line unless MDBList gives a word, so `certified` and `verified_hot` will rarely appear.

Episodes get `episode_ratings(imdb_id, source, …)` for IMDb only, keyed by the episode's IMDb id.

## API
A rating entry is `{source, value, scale, score100, votes?, state?, url, fetchedAt}`.
- `score100` is value/scale × 100, rounded. Sorting and filtering use it, so the UI never needs a source's scale.
- `fetchedAt` is RFC 3339.

Where entries appear:
- **Title detail:** `ratings: [entry…]`, in a fixed order: IMDb, Rotten Tomatoes critics, RT audience, Metacritic, Letterboxd, Trakt, TMDB.
- **Episodes:** each episode in the season/episodes answer the Show page already loads carries `ratings` (IMDb), so there are no N+1 requests.
- **Lists and cards:** `rating: entry | null`, the primary source's entry. If the title has none from that source, it's the first available in the fixed order; the entry's `source` says which.
- **List query:**
  - `sort=rating`: by `rating.score100`, descending, with titles that have no rating last;
  - `minRating=N`: `rating.score100 >= N`. A title with no rating is excluded.

Settings:
- **The MDBList key:** `mdblistKeySet` and `mdblistKeySource`, tested with `POST /api/settings/test` and `provider: "mdblist"`.
- **Choices:**
  - `ratingsPrimary` (imdb|rt_critics|metacritic|letterboxd|trakt|tmdb);
  - `ratingsShown`: an ordered array, which can be PATCHed.
- **Read only:**
  - `ratingsAvailable`: the sources this install can produce, given its keys and the privacy switch;
  - `imdbAttribution`, `imdbAttributionUrl`, `mdblistAttribution`, `mdblistAttributionUrl`;
  - `imdbDatasetUpdatedAt`, which is null until the first download.

## UI (rare-tiger)
- **Detail pages:** a row of rating chips under the title, each a link to the source: the IMDb logo-text with 8.7 and the vote count, a tomato with 93% (fresh/rotten/certified art), a popcorn with 91%, a Metacritic box with 74 coloured by band, Letterboxd ★4.1.
- **Elsewhere:**
  - cards show the primary rating subtly;
  - Movies and Shows get "Sort: Rating" and a minimum-rating filter;
  - an episode list shows an IMDb rating per episode.
- **Settings → Ratings:**
  - the MDBList key card, the same pattern as TMDB/TVDB with Test;
  - the primary-source picker;
  - which sources to show;
  - IMDb's attribution, and the dataset's last-updated date.

## Order of work
1. **Server:** the IMDb dataset job plus the TMDB score (no key needed, so it works for everyone), the store, and the API.
2. **MDBList:** the client with fake-server tests, the per-title job, and the budget.
3. **UI.**
4. **Tests:** every step pins its behaviour with mutants, and the parsers are fuzzed. The IMDb parser runs against a synthetic 1.5 M-row file for memory and time.

## As built (server side, 2026-11-05, revised after the review the same day)

**Checked against the sources.**
- **IMDb's terms** (read 2026-11-05). `datasets.imdbws.com` is described at `data.imdb.com/non-commercial-datasets/`
  (`developer.imdb.com/non-commercial-datasets/` redirects there): "for personal and non-commercial use", local copies
  allowed, `title.ratings.tsv.gz` is `tconst`, `averageRating`, `numVotes` and "refreshed daily". The licence it links to
  (`help.imdb.com`, "Can I use IMDb data in my software?") allows limited non-commercial use of the published datasets only,
  forbids altering the data or reusing it "to build a movie database, except for individual personal use", forbids
  scraping, and asks for exactly this notice, which Settings shows (`imdbAttribution`):
  "Information courtesy of IMDb (https://www.imdb.com). Used with permission." Each install downloads its own copy and
  nothing serves it.
- **MDBList's API.** Its own documentation pages could not be read (they are rendered by script). What is built follows
  MDBList's open-source client Kometa (`modules/mdblist.py`), which reads exactly these: `GET https://api.mdblist.com/{imdb|tmdb|tvdb}/{movie|show}/{id}?apikey=KEY`
  gives the title with a `ratings` array of `{source, value, score, votes, url}`; the sources are `imdb`, `tmdb`,
  `metacritic`, `metacriticuser`, `trakt`, `tomatoes`, `tomatoesaudience` (or `popcorn`), `letterboxd` and `myanimelist`;
  `GET /user` gives `api_requests` (the day's allowance) and `api_requests_count` (used today); a 429 whose error says
  "daily" is the allowance, and a 200 with `{"response": false, "error": "API Limit Reached!"}` is the same. The free key
  is 1,000 requests a day, with 1,000 reads per 5 minutes. **Not verified:** whether the Rotten Tomatoes entries say
  fresh/rotten/certified/upright/spilled/verified hot. The mapper takes a `state` or `status` word when there is one and
  otherwise derives `fresh`/`rotten` and `upright`/`spilled` by Rotten Tomatoes' 60% line; `certified` and
  `verified_hot` appear only when MDBList says so (they need review counts the answer is not known to have). A `value`
  above its source's scale (an answer on another scale) is replaced by MDBList's own `score` (0 to 100) rescaled to the
  source's, when there is one; otherwise the entry is left out. The fixtures in the tests are made up in this shape.
- **IMDb's episode dataset** (`title.episode.tsv.gz`, read 2026-11-05 on the same page): `tconst` (the episode),
  `parentTconst` (the parent TV series), `seasonNumber`, `episodeNumber` (integers), `\N` for a missing value, "refreshed
  daily". It replaces asking a provider for each episode's id: the season answers of TMDB and TVDB have no IMDb id for
  episodes (TMDB's reference lists none, and TVDB's needs `GET /episodes/{id}/extended` for each), which would cost one
  request an episode. An earlier version of this milestone did that; it is gone, with the `ratings.episodes` job.

**Store (`010_ratings.sql`).**
- `title_ratings(title_id, source, value, scale, votes, state, url, fetched_at)` as specified, with the source list as a
  CHECK and `ON DELETE CASCADE`. TMDB's score is not stored a second time: it is read from `titles.rating`/`vote_count`
  for a title whose source is `tmdb` or `mixed` (an NFO's own rating is not TMDB's, TVDB has none).
- **`episode_ratings` is keyed by the IMDb id, not by an episode id** (`episodes` has none, and a refresh deletes and writes
  a season's episodes again, which would take the ratings with them): `(imdb_id, source, value, scale, votes, fetched_at)`,
  IMDb only.
- `episodes` gains `imdb_id` and `imdb_src` (`''`, `provider` or `dataset`: where the id came from); `titles` gains
  `ratings_checked` (when MDBList was last asked, also when it had nothing). A refresh keeps the ids the fetch does not
  bring, by season and number, for a show that was in the aired order and still is.
- Writes are background priority, at most 500 rows to a transaction, and yield (`Pace`) between transactions.

**Jobs** (`cmd/ply/ratings.go`; all `jobs.PriorityRefresh`, no `Need`, so only a stalled viewer holds them). Neither job
uses TMDB or TVDB, so neither shares a limiter with the metadata pass (`meta.match`); nothing is gated on it.
- `ratings.imdb`, key `imdb`: the two IMDb files, in this order. (1) **The episode dataset**, only when the library has a show
  with an IMDb id in the aired order: fetched if a week old, read as a stream keeping the episodes of those shows, mapped
  onto the stored episodes (see *Episode ids* below). (2) **The ratings dataset**, always when the library has an IMDb id:
  fetched if a week old, read keeping the library's tconst (titles, and the episodes the mapping gave an id), written with a
  stamp; what the stamp did not reach (an id a title lost, a title that left the dataset) is pruned, unless the file had none
  of the library's ids at all (then nothing is pruned: a short file must not take the ratings away). A failure to get the
  episode file does not stop the ratings; both errors are returned.
  - **Fetching** (both files): a conditional request (`If-None-Match`/`If-Modified-Since` from the last answer, kept in
    settings as `imdb_<set>_etag|modified`), written to a `.ratings-*.part` file, then **read through to its end before it is
    kept**: it must be a gzip, whole, unzip to no more than 3 GiB, start with the dataset's header (the first columns, so a
    column added at the end is fine) and have at least half the data rows of the last copy (`imdb_<set>_rows`; at least one).
    Only then is it flushed and renamed over the copy. A file that fails any of that (`ErrIMDbFormat`, `ErrIMDbShort`) leaves
    the copy we have, is not asked for again for a week (`imdb_<set>_retry`), and with no copy fails the job for good. A
    download over 256 MiB is cut off. Part files older than an hour are swept before a download. Redirects only over https.
  - **Outages**: the copy we have is used; the server is left alone 30 minutes after the first failure, then double each
    time up to a day (`imdb_<set>_fails`, `_retry`); a success clears both.
  - **What was written is recorded as `fingerprint|copy`** (`imdb_<set>_applied`): the fingerprint of the library's ids (or
    of the shows and how many episodes each has), then the copy's ETag (else Last-Modified, else its date). A write that
    failed half way, or a copy nothing was written from, is done again from the file with no request. A copy is not read
    again when neither it nor the fingerprint changed. A library that changed is written again, but not within the set's
    refit time of the last write (`imdb_<set>_ran`): ten minutes for the ratings (`IMDbRatingsRefit`), an hour for the
    episode mapping (`IMDbEpisodesRefit`), so that a scan that matches titles for a while is not read at every look.
  - A file read for a write that is no longer the dataset (changed on disk) is deleted and fetched again.
- `ratings.mdblist`, key `mdblist`: **one job** (`Ratings.RunMDBListDue`) that goes through every title due
  (`store.DueRatings`: never asked, or last asked over 14 days ago, 7 for a title released in the last 90 days), 20 at a
  time, calling `Progress` and `Yield` before each title; a title is run once in a job. By TMDB id first, then IMDb, then
  TVDB: an id MDBList does not know is left for the next, and a show from TVDB whose `remoteIds` carry neither of the
  others is rated through its TVDB id. Nothing else leaves the machine but the key. It ends when nothing is due, the
  day's budget is spent, or it has to stop (below). The budget is the allowance MDBList reports less a tenth (900 of 1,000),
  or 900 when it reports none, counted per UTC day in settings (`mdblist_budget`); the first request of a day reads
  `/user`, and a daily-limit answer ends the day. **A request that is not sent is not counted**: while MDBList has the
  client paused for longer than it waits in place (a 429, or a 200 "API Rate Limit Reached!", which pauses for a minute),
  the title is not charged (`MDBListClient.Gate`). A 401 stops the asking until the key changes or is tested.
  - **A title MDBList will not rate** (a refusal that is not about the key, a limit or a missing title: a 400 or a 422, or a
    200 with `response:false` and words we do not know) is recorded as asked (`ratings_checked`, its ratings kept) and the
    run goes on.
  - **A title MDBList answers with a server error** (5xx, a timeout status, a body that is not JSON) does not hold up the
    ones behind it either: the run goes on past it, and the title is asked again in the next run; the second time in a row
    it is given up on like the case above (the count is in memory, and a success clears it). The run **stops**, with the
    error and for the queue to retry, for what is not the title's: a 429, a network failure (no answer), the key, or
    **three titles in a row** answered with a server error (MDBList is down).
- The schedule is the one on Settings → Schedules (`docs/M12-SCHEDULES.md`, M12): `ratings.mdblist` nightly, and
  `ratings.imdb` weekly, at the times the owner sets. `Ratings.Wake` (a settings change, the privacy switch) wakes
  the scheduler, which runs a task that could not run for a missing key at once; a title matched since the last run
  gets its ratings at the next one, or from "Run now". The jobs keep their own gates: `IMDbDue`, `DueMDBList`, the
  daily MDBList budget and its backoff, so a run that is not due returns at once.
- `-metadata=false`, or the setting, means no download of any kind: the datasets and MDBList are all gated on the privacy
  switch, which they check themselves (not only the schedule). Ratings already stored are still shown.

**Episode ids** (from IMDb's `title.episode.tsv.gz`; no request to TMDB or TVDB, and none per episode). The file is
`tconst, parentTconst, seasonNumber, episodeNumber` (`\N` for a missing value; "refreshed daily", about 40 MB gzipped), read
as a stream keeping the rows whose `parentTconst` is the IMDb id of a library show. It is mapped (`store.PlanEpisodeIMDb`,
then `ApplyEpisodeIMDb`, which writes only what changes, 500 to a transaction):
- **by season and number**, for a show whose `episode_order` is `aired` (a show in the DVD or absolute order is skipped, and
  the file is not even fetched if no show is aired);
- **not the specials**: season 0, and rows with `\N` for the season or the number, are left out;
- **a season only when its counts agree exactly**: a provider that merges a two-parter or splits an episode shifts every
  number after it, and only the count says so (9 stored against 10, 8 against 10). The show's **latest season** keeps a
  slack of two (`EpisodeCountSlack`), as it may be on air, with more announced than IMDb lists; but **a season of five
  episodes or fewer** (`EpisodeSmallSeason`, on either side) needs the exact count too, the latest one included, as two is
  most of it (1 against 3). A season the dataset is ahead of is not the latest. A season that is not mapped is left alone,
  and the ids an earlier mapping gave it are taken away. Same count in another order is not detectable;
- **never over a provider's id**: `episodes.imdb_src` is `provider` for an id that TMDB's or TVDB's data gave, `dataset` for a
  mapped one, and only `dataset` ids are changed or cleared (also when a plan is applied later than it was made);
- a slot that two rows of the dataset claim is mapped to neither; a show the file has no rows for is not touched;
- a refresh of a title keeps the ids its episodes had by season and number, but only for a show that was in the aired order
  and still is, and not for one that left TVDB.
The mapping runs before the ratings are written, so the episodes are rated in the same run.

**Settings and keys.** `-mdblist-key` > `PLY_MDBLIST_KEY` > the settings table; hidden `-mdblist-url`
(`PLY_MDBLIST_URL`), `-imdb-ratings-url` (`PLY_IMDB_RATINGS_URL`) and `-imdb-episodes-url` (`PLY_IMDB_EPISODES_URL`) for
tests. The key is checked with `GET /user`
before it is stored (400 refused, 502 unreachable; not checked, as for the others, with lookups off), is never returned
or logged (cut out of every error, including the transport's, which holds the URL), goes only over https (a server on this
machine is let off), and a redirect is an error.

**API.**
- `RatingEntry`: `{source, value, scale, score100, votes?, state?, url, fetchedAt}`. `votes` is left out when 0,
  `state` for any source but the two of Rotten Tomatoes, `url` is `""` when unknown, `fetchedAt` is RFC 3339 (UTC).
  IMDb's `url` is `https://www.imdb.com/title/<tconst>/`, TMDB's `https://www.themoviedb.org/{movie|tv}/<id>`, the rest
  are MDBList's, kept only when they are https links on the source's own site.
- Title detail (`meta`, `PATCH /api/titles/{id}`): `ratings: [entry...]` in the order imdb, rt_critics, rt_audience,
  metacritic, letterboxd, trakt, tmdb (`[]` when none), and `rating`.
- `rating: entry | null` (always present) on every `TitleCard` (`/api/titles`, collections, a person's credits, review
  candidates) and on the rows of `/api/items` and `/api/shows` and their details: the primary source's entry, else the
  first there is in the fixed order. An episode takes its show's.
- Episodes: each `kind=episode` row of `/api/items` (the list the Show page loads) and `episodeInfo` in an episode's detail
  carry `ratings: [entry]` (IMDb; `[]` when none); movie rows have no `ratings`. `episodeInfo.rating` stays the episode's
  TMDB score as a number.
- `GET /api/items`, `/api/shows`, `/api/titles`: `sort=rating` (best `score100` first, ties in list order, no rating
  last) and `minRating=N` (0 to 100, `score100 >= N`, a title with no rating is left out); anything else is a 400.
- Settings: `mdblistKeySet`, `mdblistKeySource` (`flag|env|settings|""`), `ratingsPrimary`, `ratingsShown`,
  `ratingsAvailable`, `imdbAttribution`, `imdbAttributionUrl`, `mdblistAttribution`, `mdblistAttributionUrl`,
  `imdbDatasetUpdatedAt` (RFC 3339, `null` until the first download; it is the dataset's Last-Modified). PATCH/PUT take
  `mdblistKey` (`""` removes), `ratingsPrimary` (imdb, rt_critics, metacritic, letterboxd, trakt, tmdb) and `ratingsShown`
  (any of the seven sources, each once, in order; `[]` shows none). The answer to a PATCH that stored a key has
  `mdblistKeyChecked`. `POST /api/settings/test {"provider": "mdblist"}` checks the key in use.
  `ratingsAvailable`: `imdb`, the four of MDBList with its key, `tmdb` with a TMDB key, and `[]` with lookups off.

**Known limits.**
- A title whose rating came from an NFO (source `nfo`) has no `tmdb` entry any more: the number was the NFO's, not TMDB's,
  and there is no source for it. Cards of such titles show IMDb's (or MDBList's) when the title has an IMDb, TMDB or TVDB id.
- A show from TVDB with no TMDB id in its `remoteIds` is rated by MDBList through its IMDb id, or, when it has no IMDb
  id either, through its TVDB id. A title with none of the three ids cannot be rated by MDBList: there is nothing to ask
  by (a title from a provider always has one; an NFO-only title may not).
- **A first scan rates in steps.** Titles matched after an IMDb write wait for the next one, which is at least ten minutes
  (`IMDbRatingsRefit`) after it, and an hour (`IMDbEpisodesRefit`) for the episodes' ids; MDBList's ratings do not wait
  for either.
- **Episode ids are only as good as the numbering agrees.** A show in the DVD or absolute order, a season whose count is
  not exactly IMDb's (the show's latest season may be two off, unless it has five episodes or fewer), and the specials
  (season 0) get no episode ids; their episodes show no IMDb rating. A season with the right count in the wrong order
  (one episode merged and another split) is not detectable.
- **`state` of Rotten Tomatoes is derived** (fresh/rotten, upright/spilled by the 60% line) unless MDBList gives a word,
  so `certified` and `verified_hot` will rarely appear.
- **MDBList's batch endpoint is not used.** Kometa's client (`modules/mdblist.py`, `get_items`) does `POST
  https://api.mdblist.com/{imdb|tmdb|tvdb}/{movie|show}/` with `{"ids": [...]}`, 100 at a time, so such an endpoint exists.
  MDBList's own pages could not be read (the one that loads says only "MDBList API"), so what is **unverified** is the shape
  of its answer and whether a batch counts as one request or as one per id against the day's allowance. ply asks one title
  at a time, which costs the same in the worst case and cannot lose a rating to a guessed shape.
- **The part file is flushed (`fsync`) before the rename, but no test can see that**: it is the one line of the fetch with
  no mutant that a test kills.

## As built (web UI, 2026-11-05)

- **Helpers** (`web/src/lib/ratings.ts`): the number as each source shows it (IMDb and TMDB "8.7", Letterboxd "4.1",
  Rotten Tomatoes and Trakt "93%", Metacritic "74"), votes in short form ("1.2M"), Metacritic's bands (61 and up good,
  40 to 60 mixed, under 40 bad), Rotten Tomatoes' state (the server's word, else the 60% line), the label read aloud,
  the tooltip ("IMDb, 1,234,567 votes. Rated 3 days ago."), `orderShown` (the entries in `ratingsShown` order; all of
  them, in the server's order, when the settings can't be read), and `ratingScore` (score100, -1 for none) for sorting
  and filtering. `api.ts` normalises Go's nulls (`ratings: null` → `[]`) and drops an entry of a source it does not know.
- **Detail pages** (`components/Ratings.svelte`, `Rating.svelte`, `RatingGlyph.svelte`): a row of chips under the title's
  facts, in `ratingsShown` order, each the source's short name ("IMDb", "Critics", "Audience", "Metacritic"...) and
  its number, with IMDb's votes; a link to the source in a new tab when the entry has an https URL. The glyphs are our
  own drawings (a tomato or a splat, a bucket of popcorn upright or spilled, a star for Letterboxd, a box coloured by
  band for Metacritic), decoration only: the name and number are text, and each chip has its full label for screen
  readers. The page reads `ratingsShown` from `GET /api/settings` (once per library version, and only when there are
  ratings to order). An episode's page shows its own chips (IMDb) and keeps TMDB's number beside the star.
- **Cards and rows**: the primary rating (`rating`) at the start of the second line of a card (Movies, Shows, genres,
  collections, a person's credits; not the Home rails), and an episode's IMDb rating in the Show page's episode list, in
  the quiet form: the glyph (or the short name) and the number, never a link.
- **Movies and Shows**: "Rating" in the sort, and a minimum (Any rating, 60+, 70+, 80+) on score100, in the URL hash
  (`#/movies?sort=rating&min=70`). Both pages sort and filter the library they already hold, so they don't use the
  server's `sort=rating`/`minRating` (and can't get its 400).
- **Settings → API keys → MDBList** (`components/MdblistKey.svelte`, since Settings has tabs: see DESIGN.md › Web
  UI › Settings): the MDBList key as the TMDB and TVDB keys (password field kept from password managers, a mask when
  set, Remove only for a key stored here, Test with `provider: "mdblist"` and its last test), and MDBList's notice.
- **Settings → Ratings** (`components/RatingsSettings.svelte`): a line on what the MDBList key adds, linking to the API
  keys tab; the primary rating
  (a select of the primary sources this install has); the shown sources as a checklist with move up and down (an
  unticked source goes to the end); every change saved at once (the client's `PUT /api/settings`), one save at a time
  with the latest choice sent last (answers can't cross), said in a `role=status` line, and put back with the server's
  words when refused; a confirmed primary refreshes the cards; once a choice is made the card keeps its own state, so a
  GET of the settings that started before a save can't undo it; the focus stays on the moved row's button (or its
  twin, at the top or bottom) at once; IMDb's notice as the server words it, with its link; and "IMDb ratings updated <date>" or "IMDb ratings not downloaded yet".
- **Activity**: `ratings.imdb` is "IMDb ratings" and `ratings.mdblist` "Ratings from MDBList".
- **Tests**: `lib/ratings.test.ts`, the ratings part of `lib/meta-api.test.ts`, `sortCards` in `lib/credits.test.ts`, and
  `web/e2e/ratings-ui.mjs` (mocked; each scenario names the mutant it kills).
