# M10: ratings from the sources that matter

The owner asked (2026-11-05) for ratings from the relevant systems (IMDb, Rotten Tomatoes and so on), with room for
them in the UI. This builds on M6 and M8. The IDs needed are already stored: `titles.imdb_id`, `tmdb_id` and `tvdb_id`.

## Sources

| Source | What | How | Key |
|---|---|---|---|
| **IMDb** | rating /10 and the vote count, for movies, shows and episodes | IMDb's official non-commercial dataset `title.ratings.tsv.gz` (datasets.imdbws.com, refreshed daily, about 7 MB gzipped: `tconst`, `averageRating`, `numVotes`), joined on `imdb_id` | none |
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
  - It streams the gzip and keeps only the `tconst` values present in the library (`imdb_id` for titles, plus episode IMDb ids when known).
  - The download goes to `<data>/cache/imdb/`; the file is kept for the next diff and is never served.
  - A file of about 1.5 M rows must be parsed with constant memory, and the job yields between chunks.
- **`ratings.mdblist`, one job per title** (by TMDB id or IMDb id) when a key is set.
  - It's due when the title has none yet, or the ratings are over 14 days old (7 days for titles released in the last 90 days).
  - It's paced to the key's daily budget: read the remaining quota from the API if it gives one, otherwise stay at 900 a day.
  - A 429 pauses the service, through the existing limiter.
- **Privacy:** `-metadata=false` or lookups off means no rating downloads either. Only IMDb ids, TMDB ids and titles ever leave the machine.
- **Episode IMDb ids:** the TMDB episode `external_ids` and the TVDB episode `remoteIds` give them; store `episodes.imdb_id` (migration).

## Store (migration 010)
`title_ratings(title_id, source, value REAL, scale REAL, votes INTEGER, state TEXT, url TEXT, fetched_at INTEGER,
PRIMARY KEY (title_id, source))`.
- `source` is one of: imdb, tmdb, rt_critics, rt_audience, metacritic, letterboxd, trakt.
- `state` is for Rotten Tomatoes. On `rt_critics` it is fresh, rotten or certified; on `rt_audience` it is upright, spilled or verified_hot. Each entry has its own vocabulary, and there is no separate field.

Episodes get `episode_ratings(episode_id, source, …)` for IMDb only.

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
