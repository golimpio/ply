-- ply:foreign-keys-off
-- M8: TheTVDB (docs/M8-TVDB.md).
--
-- titles.source gains 'tvdb', and SQLite cannot alter a CHECK, so titles is
-- rebuilt as the SQLite documentation says (create the new table, copy, drop the
-- old, rename the new, with foreign keys off, which the line above asks the
-- migration runner for): dropping titles with them on would run the ON DELETE
-- actions of everything that refers to it (episodes, credits, seasons, genres, the
-- links of items and shows). The runner checks them again before it commits.
--
-- The rebuilt table also has UNIQUE (kind, tvdb_id) and episode_order. A tvdb_id that
-- two titles shared (TMDB's external ids can say so) stays on the one with the lowest
-- id, and goes from the other, so that the copy cannot fail on the new constraint.

CREATE TABLE titles_new (
    id             INTEGER PRIMARY KEY,
    kind           TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb_id        INTEGER,
    imdb_id        TEXT    NOT NULL DEFAULT '',
    tvdb_id        INTEGER,
    title          TEXT    NOT NULL DEFAULT '',
    original_title TEXT    NOT NULL DEFAULT '',
    sort_title     TEXT    NOT NULL DEFAULT '',
    year           INTEGER NOT NULL DEFAULT 0,
    overview       TEXT    NOT NULL DEFAULT '',
    tagline        TEXT    NOT NULL DEFAULT '',
    release_date   TEXT    NOT NULL DEFAULT '', -- YYYY-MM-DD
    end_date       TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT '',
    runtime_min    INTEGER NOT NULL DEFAULT 0,
    rating         REAL    NOT NULL DEFAULT 0,
    vote_count     INTEGER NOT NULL DEFAULT 0,
    certification  TEXT    NOT NULL DEFAULT '',
    poster         TEXT    NOT NULL DEFAULT '', -- image refs: "tmdb:/abc.jpg", "tvdb:/banners/posters/1-1.jpg"
    backdrop       TEXT    NOT NULL DEFAULT '',
    logo           TEXT    NOT NULL DEFAULT '',
    collection_id  INTEGER REFERENCES collections (id) ON DELETE SET NULL,
    source         TEXT    NOT NULL DEFAULT 'tmdb' CHECK (source IN ('tmdb', 'tvdb', 'nfo', 'mixed')),
    locked         INTEGER NOT NULL DEFAULT 0,  -- user edited: a refresh keeps the text fields
    next_air_date  TEXT,                        -- series: the next episode's air date, YYYY-MM-DD
    fetched_at     INTEGER NOT NULL DEFAULT 0,  -- unix seconds; 0 = never fetched
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    next_episode_season INTEGER NOT NULL DEFAULT 0,
    next_episode_number INTEGER NOT NULL DEFAULT 0,
    next_episode_name   TEXT    NOT NULL DEFAULT '',
    -- series: how files are mapped to episodes, with TVDB's season types official, dvd, absolute
    episode_order  TEXT    NOT NULL DEFAULT 'aired' CHECK (episode_order IN ('aired', 'dvd', 'absolute')),
    UNIQUE (kind, tmdb_id),
    UNIQUE (kind, tvdb_id)
);

INSERT INTO titles_new (id, kind, tmdb_id, imdb_id, tvdb_id, title, original_title, sort_title, year,
        overview, tagline, release_date, end_date, status, runtime_min, rating, vote_count, certification,
        poster, backdrop, logo, collection_id, source, locked, next_air_date, fetched_at, created_at, updated_at,
        next_episode_season, next_episode_number, next_episode_name)
    SELECT t.id, t.kind, t.tmdb_id, t.imdb_id,
        CASE WHEN t.tvdb_id IS NOT NULL
              AND EXISTS (SELECT 1 FROM titles o WHERE o.kind = t.kind AND o.tvdb_id = t.tvdb_id AND o.id < t.id)
             THEN NULL ELSE t.tvdb_id END,
        t.title, t.original_title, t.sort_title, t.year,
        t.overview, t.tagline, t.release_date, t.end_date, t.status, t.runtime_min, t.rating, t.vote_count, t.certification,
        t.poster, t.backdrop, t.logo, t.collection_id, t.source, t.locked, t.next_air_date, t.fetched_at, t.created_at, t.updated_at,
        t.next_episode_season, t.next_episode_number, t.next_episode_name
    FROM titles t;

DROP TABLE titles;
ALTER TABLE titles_new RENAME TO titles;
CREATE INDEX titles_collection ON titles (collection_id);

-- A person of TheTVDB. SQLite cannot add a UNIQUE column, so the index is the constraint;
-- people without a TVDB id (NULL) do not collide in it.
ALTER TABLE people ADD COLUMN tvdb_id INTEGER;
CREATE UNIQUE INDEX people_tvdb ON people (tvdb_id);

-- M7's archive of a removed folder keeps a show's manual match in series_id (a TMDB id). A match
-- made through TVDB is kept by its TVDB id next to it; a row has one of the two, not both.
ALTER TABLE item_archive ADD COLUMN series_tvdb_id INTEGER NOT NULL DEFAULT 0;
